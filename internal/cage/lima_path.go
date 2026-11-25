package cage

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
	"github.com/lima-vm/lima/v2/pkg/osutil"
	"github.com/sylvinus/agent-vm/internal/paths"
	"github.com/sylvinus/agent-vm/internal/vmname"
)

const (
	LimaSocketPathReason = "Lima 虚拟机套接字路径过长"
	LimaSocketPathHelp   = "请将 Cage 配置放在路径更短的本地目录后重试。若手动链接了环境存储，请使用路径更短的本地存储目录。"
)

var ErrLimaSocketPathTooLong = errors.New(LimaSocketPathReason)

// ProfileLimaHome is the home Cage passes to its embedded Lima. A managed
// move preserves the logical state link, anchored under the resolved profile.
func ProfileLimaHome(profile string, custom bool) (string, error) {
	if custom {
		real, err := paths.Resolve(profile)
		if err != nil {
			return "", errors.New("无法解析 Cage 配置目录，请检查配置目录及其链接。")
		}
		return filepath.Join(real, "state", "lima"), nil
	}
	// Resolve junctions and symlinks, including the existing prefix before
	// the first create, so Lima sees the same home before and after creation.
	real, err := StoragePhysicalPath(filepath.Join(profile, "state", "lima"))
	if err != nil {
		return "", errors.New("无法解析 Lima 存储目录，请检查环境存储及其链接。")
	}
	return real, nil
}

// ValidateLimaSocketPath takes an instance directory as seen by Lima. The
// trailing NUL must fit as well. Report lengths without exposing the path.
func ValidateLimaSocketPath(instanceDir string) error {
	n := len(filepath.Join(instanceDir, filenames.LongestSock))
	if n < osutil.UnixPathMax {
		return nil
	}
	return fmt.Errorf("%w（需要 %d 字节，最多 %d 字节）。%s", ErrLimaSocketPathTooLong, n, osutil.UnixPathMax-1, LimaSocketPathHelp)
}

// ValidateProfileLimaSocketPaths checks all the instances this profile needs
// to create an environment. It reads the storage record without modifying it.
func ValidateProfileLimaSocketPaths(profile string) error {
	record, err := readStorageRecord(profile)
	if err != nil {
		return errors.New("无法读取 Cage 环境存储配置，请在设置中检查环境存储。")
	}
	home, err := ProfileLimaHome(profile, record.Directory != "")
	if err != nil {
		return err
	}
	for _, name := range []string{vmname.Building, vmname.Template, InstanceName(profile, EnvironmentDir(profile))} {
		if err := ValidateLimaSocketPath(filepath.Join(home, name)); err != nil {
			return err
		}
	}
	return nil
}
