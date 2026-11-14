package guestos

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const macOSInstallRecoveryRecord = "macos-install-recovery.json"

const (
	MacOSInstallAwaitingGUIAuthorization = "awaiting_gui_authorization"
	MacOSInstallComponentsVerified       = "components_verified"
	MacOSInstallAccountVerified          = "ordinary_account_verified"
	MacOSInstallServicesPrepared         = "services_prepared"
)

// MacOSInstallRecovery is the temporary identity for a created but not yet
// bound macOS guest. It never enables ordinary workbench routes.
type MacOSInstallRecovery struct {
	Version        int       `json:"version"`
	InstallationID uuid.UUID `json:"installation_id"`
	Instance       string    `json:"instance"`
	User           string    `json:"user"`
	MachineID      string    `json:"machine_id"`
	Phase          string    `json:"phase"`
	LastError      string    `json:"last_error,omitempty"`
}

func (r MacOSInstallRecovery) Validate() error {
	if r.Version != 1 || r.InstallationID == uuid.Nil || r.Instance != "mac" ||
		!ValidMacOSAccountName(r.User) || len(r.MachineID) != 64 ||
		strings.Trim(r.MachineID, "0123456789abcdef") != "" ||
		!validMacOSInstallPhase(r.Phase) || len(r.LastError) > 4096 {
		return errors.New("macOS 安装恢复记录无效")
	}
	return nil
}

func validMacOSInstallPhase(phase string) bool {
	switch phase {
	case MacOSInstallAwaitingGUIAuthorization, MacOSInstallComponentsVerified,
		MacOSInstallAccountVerified, MacOSInstallServicesPrepared:
		return true
	default:
		return false
	}
}

// LoadMacOSProfileOrRecovery returns a profile-shaped identity for the
// restricted pre-bind session. Callers must still enforce macSetupPending.
func LoadMacOSProfileOrRecovery(profile string) (*MacOSProfile, error) {
	bound, err := LoadMacOSProfile(profile)
	if err != nil || bound != nil {
		return bound, err
	}
	recovery, err := LoadMacOSInstallRecovery(profile)
	if err != nil || recovery == nil {
		return nil, err
	}
	result := &MacOSProfile{Version: 1, Instance: recovery.Instance, User: recovery.User,
		MachineID: recovery.MachineID, SetupID: recovery.InstallationID.String()}
	identity, err := ReadIntelMacOSIdentity(filepath.Join(profile, "state", "lima", "mac"))
	if err != nil {
		return nil, err
	}
	if identity != nil {
		result.Version, result.Backend, result.UUID = 2, MacOSQEMU, identity.UUID
	}
	return result, nil
}
func CanRecoverMacOSInstall(profile string, id uuid.UUID) (bool, error) {

	install, err := LoadInstallation(profile)
	if err != nil {
		return false, err
	}
	if install == nil || install.System != MacOS || install.ID != id {
		return false, nil
	}
	if err := validateMacOSInstallDisk(profile); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	_, err = macMachineID(profile)
	return err == nil, err
}
func validateMacOSInstallDisk(profile string) error {
	info, err := os.Lstat(filepath.Join(profile, "state", "lima", "mac", "disk"))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("macOS 客机系统盘尚未就绪")
	}
	return nil
}
func LoadMacOSInstallRecovery(profile string) (*MacOSInstallRecovery, error) {
	path := filepath.Join(profile, macOSInstallRecoveryRecord)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 {
		return nil, errors.New("macOS 安装恢复记录无法读取")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var recovery MacOSInstallRecovery
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&recovery) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("macOS 安装恢复记录格式无效")
	}
	if err := recovery.Validate(); err != nil {
		return nil, err
	}
	install, err := LoadInstallation(profile)
	if err != nil {
		return nil, err
	}
	if install == nil || install.System != MacOS || install.ID != recovery.InstallationID {
		return nil, errors.New("macOS 安装恢复记录与 pending 安装不匹配")
	}
	machineID, err := macMachineID(profile)
	if err != nil {
		return nil, err
	}
	if err := validateMacOSInstallDisk(profile); err != nil {
		return nil, err
	}
	if machineID != recovery.MachineID {
		return nil, errors.New("macOS 安装恢复记录与当前客机不匹配")
	}
	return &recovery, nil
}

func CreateMacOSInstallRecovery(profile, user string, id uuid.UUID) error {
	install, err := LoadInstallation(profile)
	if err != nil {
		return err
	}
	if install == nil || install.System != MacOS || install.ID != id {
		return errors.New("macOS 安装恢复与当前 pending 安装不匹配")
	}
	if !ValidMacOSAccountName(user) {
		return errors.New("macOS 安装恢复账户无效")
	}
	machineID, err := macMachineID(profile)
	if err != nil {
		return err
	}
	recovery := MacOSInstallRecovery{Version: 1, InstallationID: id, Instance: "mac", User: user,
		MachineID: machineID, Phase: MacOSInstallAwaitingGUIAuthorization}
	if current, err := LoadMacOSInstallRecovery(profile); err != nil {
		return err
	} else if current != nil {
		if current.InstallationID == id && current.User == user && current.MachineID == machineID {
			return nil
		}
		return errors.New("已有其他 macOS 安装恢复记录")
	}
	return writeMacOSInstallRecovery(profile, recovery)
}

func SetMacOSInstallRecoveryPhase(profile string, id uuid.UUID, phase string, cause error) error {
	if !validMacOSInstallPhase(phase) {
		return errors.New("macOS 安装恢复阶段无效")
	}
	recovery, err := LoadMacOSInstallRecovery(profile)
	if err != nil {
		return err
	}
	if recovery == nil || recovery.InstallationID != id {
		return errors.New("macOS 安装恢复记录已变化")
	}
	allowed := phase == MacOSInstallAwaitingGUIAuthorization || phase == recovery.Phase ||
		recovery.Phase == MacOSInstallAwaitingGUIAuthorization && phase == MacOSInstallComponentsVerified ||
		recovery.Phase == MacOSInstallComponentsVerified && phase == MacOSInstallAccountVerified ||
		recovery.Phase == MacOSInstallAccountVerified && phase == MacOSInstallServicesPrepared
	if !allowed {
		return errors.New("macOS 安装恢复阶段顺序无效")
	}
	recovery.Phase = phase
	recovery.LastError = ""
	if cause != nil {
		recovery.LastError = cause.Error()
	}
	return writeMacOSInstallRecovery(profile, *recovery)
}

func RemoveMacOSInstallRecovery(profile string, id uuid.UUID) error {
	recovery, err := LoadMacOSInstallRecovery(profile)
	if err != nil {
		return err
	}
	if recovery == nil || recovery.InstallationID != id {
		return errors.New("macOS 安装恢复记录已变化")
	}
	return os.Remove(filepath.Join(profile, macOSInstallRecoveryRecord))
}

func writeMacOSInstallRecovery(profile string, recovery MacOSInstallRecovery) error {
	if err := recovery.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(recovery)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(profile, ".macos-install-recovery-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(profile, macOSInstallRecoveryRecord)); err != nil {
		return err
	}
	dir, err := os.Open(profile)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
