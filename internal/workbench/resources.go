package workbench

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/docker/go-units"
	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/containerenv"
	"go.yaml.in/yaml/v3"
)

func (s *Server) instanceDir() string {
	if containerenv.Configured(s.profile) {
		return filepath.Join(s.profile, "state", "container", "environment")
	}
	return filepath.Join(s.profile, "state", "lima", cage.InstanceName(s.profile, s.environmentDir()))
}

func (s *Server) configuredInstanceResources() (memoryBytes int64, cpus int, diskBytes int64) {
	var configured struct {
		Memory string `yaml:"memory"`
		CPUs   int    `yaml:"cpus"`
		Disk   string `yaml:"disk"`
	}
	if b, err := os.ReadFile(filepath.Join(s.instanceDir(), "lima.yaml")); err == nil {
		_ = yaml.Unmarshal(b, &configured)
	}
	memoryBytes, _ = units.RAMInBytes(configured.Memory)
	diskBytes, _ = units.RAMInBytes(configured.Disk)
	if diskBytes <= 0 {
		if info, err := cage.DiskImageUsage(s.instanceDir()); err == nil && info.VirtualBytes > 0 {
			diskBytes = info.VirtualBytes
		}
	}
	return memoryBytes, configured.CPUs, diskBytes
}

func (s *Server) resourceStatus(w http.ResponseWriter, r *http.Request) {
	if containerenv.Configured(s.profile) {
		environment, err := containerenv.Load(s.profile)
		if err != nil {
			requestError(w, err)
			return
		}
		state, err := environment.State(r.Context())
		if err != nil {
			requestError(w, err)
			return
		}
		config := environment.Config()
		jsonReply(w, map[string]any{"running": state.Running, "automatic": false, "runtime": "container",
			"recommended_memory_bytes": config.MemoryBytes, "recommended_cpus": config.CPUs,
			"configured_memory_bytes": state.MemoryBytes, "configured_cpus": state.CPUs,
			"resources_verified": state.ResourcesVerified, "resource_error": state.ResourceError, "pids_limit": state.PIDs,
			"restart_required": false, "observation": nil})
		return
	}
	a, err := cage.LoadResourceAllocation(s.configPath)
	if err != nil {
		requestError(w, err)
		return
	}
	memory, cpus, err := a.Limits()
	allocationError := ""
	if err != nil {
		allocationError = err.Error()
		memory, cpus = a.MemoryGiB, a.CPUs
	}
	maxMemory, maxCPUs := cage.ResourceLimits()
	running := s.vmRunning()
	maximum, configuredCPUs, configuredDiskBytes := s.configuredInstanceResources()
	var observation *cage.ResourceStatus
	status, err := cage.ReadResourceStatus(s.instanceDir())
	if running && err == nil {
		observation = &status
	}
	var guestDiskUsed uint64
	if observation != nil && observation.Guest != nil {
		guestDiskUsed = observation.Guest.DiskUsed
	}
	var hostDiskAllocated int64
	if info, infoErr := cage.DiskImageUsage(s.instanceDir()); infoErr == nil {
		hostDiskAllocated = info.HostAllocatedBytes
		if configuredDiskBytes <= 0 {
			configuredDiskBytes = info.VirtualBytes
		}
	}
	storageProbeDir := filepath.Dir(s.configPath)
	if st, statErr := os.Stat(s.instanceDir()); statErr == nil && st.IsDir() {
		storageProbeDir = s.instanceDir()
	}
	minDiskGiB, maxDiskGiB := cage.DiskBounds(storageProbeDir, uint64(max(int64(0), configuredDiskBytes)), guestDiskUsed)
	// The disk only grows; the range offered starts at its present size.
	if current := int((max(int64(0), configuredDiskBytes) + (1 << 30) - 1) >> 30); current > 0 {
		minDiskGiB = min(current, maxDiskGiB)
	}
	diskGiB, diskErr := a.DiskLimit(storageProbeDir, uint64(max(int64(0), configuredDiskBytes)), guestDiskUsed)
	if diskErr != nil {
		if allocationError == "" {
			allocationError = diskErr.Error()
		}
		diskGiB = a.DiskGiB
	}
	diskChanged := configuredDiskBytes > 0 && int64(diskGiB)<<30 != configuredDiskBytes
	jsonReply(w, map[string]any{
		"running": running, "automatic": a.Automatic,
		"memory_gib": memory, "cpus": cpus, "disk_gib": diskGiB,
		"max_memory_gib": maxMemory, "max_cpus": maxCPUs,
		"min_disk_gib": minDiskGiB, "max_disk_gib": maxDiskGiB,
		"resource_error":           allocationError,
		"recommended_memory_bytes": uint64(cage.AutomaticMemoryGiB(maxMemory)) << 30, "recommended_cpus": maxCPUs,
		"recommended_disk_bytes":   uint64(cage.DefaultDiskGiB) << 30,
		"configured_memory_bytes":  maximum, "configured_cpus": configuredCPUs,
		"configured_disk_bytes":     configuredDiskBytes,
		"host_disk_allocated_bytes": hostDiskAllocated,
		"disk_change_pending":       diskChanged,
		"restart_required":          running && (maximum != int64(memory)<<30 || configuredCPUs != cpus || diskChanged || observation == nil),
		"observation":               observation,
	})
}

func (s *Server) applyResources(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Allocation *cage.ResourceAllocation `json:"allocation"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if containerenv.Configured(s.profile) {
		http.Error(w, "容器资源由服务器部署配置管理", http.StatusBadRequest)
		return
	}
	if input.Allocation != nil {
		if _, _, err := input.Allocation.Limits(); err != nil {
			requestError(w, err)
			return
		}
		// Limits lowers an older saved value to this host's ceiling; a
		// value being saved now has to fit.
		if _, maxCPUs := cage.ResourceLimits(); !input.Allocation.Automatic && input.Allocation.CPUs > maxCPUs {
			requestError(w, fmt.Errorf("CPU 须为 1–%d 个逻辑核心", maxCPUs))
			return
		}
		_, _, configuredDiskBytes := s.configuredInstanceResources()
		var guestDiskUsed uint64
		if s.vmRunning() {
			if obs, obsErr := cage.ReadResourceStatus(s.instanceDir()); obsErr == nil && obs.Guest != nil {
				guestDiskUsed = obs.Guest.DiskUsed
			}
		}
		storageProbeDir := filepath.Dir(s.configPath)
		if st, statErr := os.Stat(s.instanceDir()); statErr == nil && st.IsDir() {
			storageProbeDir = s.instanceDir()
		}
		if !input.Allocation.Automatic && input.Allocation.DiskGiB > 0 && int64(input.Allocation.DiskGiB)<<30 < configuredDiskBytes {
			requestError(w, cage.DiskShrinkUnavailable)
			return
		}
		if _, err := input.Allocation.DiskLimit(storageProbeDir, uint64(max(int64(0), configuredDiskBytes)), guestDiskUsed); err != nil {
			requestError(w, err)
			return
		}
		if !s.op.TryLock() {
			http.Error(w, "另一个操作仍在进行，请稍候", http.StatusConflict)
			return
		}
		err := writePrivate(cage.ResourceAllocationPath(s.configPath), input.Allocation)
		s.op.Unlock()
		if err != nil {
			requestError(w, err)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
		return
	}
	s.launchRequest(w, r, "resources", func() error {
		allocation, err := cage.LoadResourceAllocation(s.configPath)
		if err != nil {
			return err
		}
		if _, _, err := allocation.Limits(); err != nil {
			return err
		}
		_, _, configuredDiskBytes := s.configuredInstanceResources()
		storageProbeDir := filepath.Dir(s.configPath)
		if st, statErr := os.Stat(s.instanceDir()); statErr == nil && st.IsDir() {
			storageProbeDir = s.instanceDir()
		}
		targetDiskGiB, err := allocation.DiskLimit(storageProbeDir, uint64(max(int64(0), configuredDiskBytes)), 0)
		if err != nil {
			return err
		}
		targetDiskBytes := int64(targetDiskGiB) << 30
		if s.vmRunning() {
			if err := s.stopEnvironment(); err != nil {
				return err
			}
		}
		if err := s.operationContext().Err(); err != nil {
			return err
		}
		if err := s.startEnvironment(false); err != nil {
			return err
		}
		if configuredDiskBytes > 0 && targetDiskBytes > configuredDiskBytes {
			growCtx, cancel := context.WithTimeout(s.operationContext(), 60*time.Second)
			defer cancel()
			if _, growErr := s.guestControl(growCtx, cage.GuestDiskGrowScript, "disk-grow"); growErr != nil {
				return fmt.Errorf("虚拟磁盘已扩大，但客机自动扩展分区失败：%w", growErr)
			}
		}
		return nil
	})
}

func (s *Server) shrinkEnvironmentDisk(targetDiskGiB int) error {
	ctx := s.operationContext()
	if !s.vmRunning() {
		if err := s.startEnvironment(false); err != nil {
			return err
		}
	}
	inspectCtx, cancelInspect := context.WithTimeout(ctx, 45*time.Second)
	out, err := s.guestControl(inspectCtx, cage.GuestDiskInspectShrinkScript, "disk-shrink")
	cancelInspect()
	if err != nil {
		return fmt.Errorf("检查环境磁盘分区失败，未改动任何数据：%w", err)
	}
	insp, err := cage.ParseDiskShrinkInspection(out)
	if err != nil {
		return fmt.Errorf("解析环境磁盘分区失败，未改动任何数据：%w", err)
	}
	plan, err := cage.PlanDiskShrink(insp, targetDiskGiB)
	if err != nil {
		return err
	}
	stageCtx, cancelStage := context.WithTimeout(ctx, 3*time.Minute)
	if _, err := s.guestControl(stageCtx, cage.GuestInstallShrinkHookAndStageScript(plan), "disk-shrink"); err != nil {
		cancelStage()
		return fmt.Errorf("准备离线缩小环境文件系统失败，未改动磁盘：%w", err)
	}
	cancelStage()

	// Reboot through initramfs so ext4 and partition 1 shrink while unmounted.
	if err := s.stopEnvironment(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.startEnvironment(false); err != nil {
		return err
	}

	relocateCtx, cancelRelocate := context.WithTimeout(ctx, 2*time.Minute)
	if _, err := s.guestControl(relocateCtx, cage.GuestRelocateGPTAndTrimScript(plan), "disk-shrink"); err != nil {
		cancelRelocate()
		return fmt.Errorf("文件系统缩小校验或重定位 GPT 失败，原虚拟磁盘未截断：%w", err)
	}
	cancelRelocate()

	if err := s.stopEnvironment(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cage.ShrinkColdDiskImage(ctx, s.instanceDir(), targetDiskGiB); err != nil {
		return err
	}
	if err := s.startEnvironment(false); err != nil {
		_ = s.stopEnvironment()
		_ = cage.RollbackDiskShrink(s.instanceDir())
		return fmt.Errorf("缩小后启动环境失败，已自动恢复原磁盘备份：%w", err)
	}
	cage.CommitDiskShrink(s.instanceDir())
	return nil
}

type networkCheckError struct{ error }

func (e networkCheckError) Unwrap() error { return e.error }

// staticDraftPendingError reports a saved, not yet verified static IP
// baseline. Unlike networkCheckError it does not imply a running guest: a
// draft saved before the first start leaves the environment stopped.
type staticDraftPendingError struct{ error }

func (e staticDraftPendingError) Unwrap() error { return e.error }

func (s *Server) checkNetworkIfWanted() error {
	s.mu.Lock()
	paused := s.networkPaused
	s.mu.Unlock()
	if paused {
		return nil
	}
	var err error
	err = s.runSafetyCheck()
	if err != nil {
		return networkCheckError{err}
	}
	return nil
}
