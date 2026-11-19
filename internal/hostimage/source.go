// Package hostimage keeps the system image an environment was created from.
// Earlier versions could match the image to the host OS; those choices stay
// readable, and the Debian ones can still finish installing and start.
package hostimage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Identity struct {
	System   string `json:"system"`
	Version  string `json:"version"`
	Build    string `json:"build,omitempty"`
	Edition  string `json:"edition,omitempty"`
	Language string `json:"language,omitempty"`
	Arch     string `json:"arch"`
}

// IsLinux reports whether system is a Linux image this version still runs.
func IsLinux(system string) bool {
	return system == "debian-12" || system == "debian-13"
}

// AbandonedLinux names the Linux guest systems earlier versions installed and
// this version no longer runs.
func AbandonedLinux(system string) bool {
	return system == "ubuntu-22.04" || system == "ubuntu-24.04"
}

// Plan is immutable after the user chooses it. Path refers only to source
// media. Download destinations are derived independently from its digest.
type Plan struct {
	Host            Identity `json:"host"`
	Version         string   `json:"version"`
	Build           string   `json:"build,omitempty"`
	Format          string   `json:"format"`
	URL             string   `json:"url,omitempty"`
	Digest          string   `json:"digest,omitempty"`
	Bytes           int64    `json:"bytes"`
	Path            string   `json:"path,omitempty"`
	Modified        int64    `json:"modified,omitempty"`
	IntegrityURL    string   `json:"integrity_url,omitempty"`
	IntegritySHA256 string   `json:"integrity_sha256,omitempty"`
}

// abandonedSystem names systems whose guests earlier versions installed.
// Their saved plans stay readable; nothing prepares or starts one.
func abandonedSystem(system string) bool {
	return system == "macos" || system == "windows-11" || AbandonedLinux(system)
}

func (p Plan) Key() string {
	if p.URL != "" && p.Digest != "" {
		p.Path, p.Modified = "", 0
	}
	data, _ := json.Marshal(p)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (p Plan) Validate() error {
	if p.Host.Arch != "arm64" && p.Host.Arch != "amd64" {
		return errors.New("本机系统架构暂不支持创建客机")
	}
	if abandonedSystem(p.Host.System) {
		return nil
	}
	if p.Version == "" || p.Version != p.Host.Version || p.Bytes < 0 || p.Bytes > 64<<30 {
		return errors.New("系统镜像与本机版本不匹配")
	}
	if !IsLinux(p.Host.System) {
		return errors.New("系统镜像记录中的系统不受支持")
	}
	if p.Format != "qcow2" {
		return errors.New("Linux 安装镜像与本机不匹配")
	}
	if p.Path != "" && (!filepath.IsAbs(p.Path) || filepath.Clean(p.Path) != p.Path || strings.ContainsAny(p.Path, "\r\n\x00")) {
		return errors.New("本机系统镜像路径无效")
	}
	if p.URL != "" {
		if err := trustedURL(p.URL, p.Host.System); err != nil {
			return err
		}
		if _, err := digestHash(p.Digest); err != nil {
			return err
		}
	}
	if p.Path == "" && p.URL == "" {
		return errors.New("缺少系统安装材料")
	}
	return nil
}

func trustedURL(raw, system string) error {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return errors.New("系统材料下载地址无效")
	}
	if u.Scheme != "https" {
		return errors.New("系统材料下载协议无效")
	}
	if IsLinux(system) && u.Hostname() == "cloud.debian.org" {
		return nil
	}
	return errors.New("系统材料下载地址不属于受支持的系统厂商")
}

func usableFile(path string, size int64) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0 && (size == 0 || info.Size() == size)
}

func (p Plan) CachedPath(cache string) string {
	key := strings.ReplaceAll(p.Digest, ":", "-")
	if key == "" {
		key = p.Key()
	}
	return filepath.Join(cache, key+"."+p.Format)
}

func (p Plan) RemainingBytes(cache string) int64 {
	if p.Path != "" {
		return 0
	}
	if usableFile(p.CachedPath(cache), p.Bytes) {
		return 0
	}
	info, err := os.Lstat(p.CachedPath(cache) + ".partial")
	if err == nil && info.Mode().IsRegular() && info.Size() <= p.Bytes {
		return p.Bytes - info.Size()
	}
	return p.Bytes
}

const sourceRecord = "host-system-source.json"

func Save(cache string, plan Plan) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(cache, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(cache, ".host-system-source-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(plan)
	closeErr := file.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(cache, sourceRecord))
}

func Load(cache string) (Plan, error) {
	var plan Plan
	file, err := os.Open(filepath.Join(cache, sourceRecord))
	if err != nil {
		return plan, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 32<<10))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&plan); err != nil {
		return plan, err
	}
	return plan, plan.Validate()
}
