// Package guestprofile stores public, creation-time guest preferences. It never
// copies host hardware identifiers and none of its values are credentials.
package guestprofile

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lima-vm/lima/v2/pkg/guestidentity"
	"github.com/sylvinus/agent-vm/internal/host"
)

const Filename = "guest-profile.json"

// Empty fields request a random value; WebGL defaults to native behavior.
type Options struct {
	Hostname         string `json:"hostname"`
	Username         string `json:"username"`
	BrowserUsername  string `json:"browser_username"`
	AudioName        string `json:"audio_name"`
	AudioDescription string `json:"audio_description"`
	DisplayWidth     int    `json:"display_width"`
	DisplayHeight    int    `json:"display_height"`
	BrowserWidth     int    `json:"browser_width"`
	BrowserHeight    int    `json:"browser_height"`
	ExtensionName    string `json:"extension_name"`
	WebGL            string `json:"webgl"`
	Canvas           string `json:"canvas"`
}

type Profile struct {
	Version int `json:"version"`
	Options
	ExtensionSuffix string `json:"extension_suffix"`
}

// Matches treats unspecified inputs as requests to reuse the generated value.
func (o Options) Matches(saved Options) bool {
	for _, p := range [][2]string{{o.Hostname, saved.Hostname}, {o.Username, saved.Username}, {o.BrowserUsername, saved.BrowserUsername}, {o.AudioName, saved.AudioName}, {o.AudioDescription, saved.AudioDescription}, {o.ExtensionName, saved.ExtensionName}, {o.WebGL, saved.WebGL}, {o.Canvas, saved.Canvas}} {
		if p[0] != "" && p[0] != p[1] {
			return false
		}
	}
	for _, p := range [][2]int{{o.DisplayWidth, saved.DisplayWidth}, {o.DisplayHeight, saved.DisplayHeight}, {o.BrowserWidth, saved.BrowserWidth}, {o.BrowserHeight, saved.BrowserHeight}} {
		if p[0] != 0 && p[0] != p[1] {
			return false
		}
	}
	return true
}

var hostname = regexp.MustCompile(`^[A-Za-z](?:[A-Za-z0-9-]{0,13}[A-Za-z0-9])?$`)
var username = regexp.MustCompile(`^[a-z][a-z0-9_]{0,23}$`)
var audioName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
var suffix = regexp.MustCompile(`^[a-f0-9]{16}$`)
var description = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 .()_-]{0,63}$`)

func (o Options) Validate() error {
	if o.Hostname != "" && !hostname.MatchString(o.Hostname) {
		return errors.New("主机名须为 1–15 位 ASCII 字母、数字或连字符，以字母开头，以字母或数字结尾")
	}
	for _, u := range []string{o.Username, o.BrowserUsername} {
		if u == "" {
			continue
		}
		if !username.MatchString(u) || reserved(u) {
			return errors.New("Linux 账户须为 1–24 位小写字母、数字或下划线，以字母开头，且不能使用系统账户")
		}
	}
	if o.Username != "" && o.Username == o.BrowserUsername {
		return errors.New("工作账户与浏览器账户必须不同")
	}
	if o.AudioName != "" && !audioName.MatchString(o.AudioName) {
		return errors.New("音频设备名须为小写字母、数字或下划线，最长 48 位")
	}
	for _, s := range []string{o.AudioDescription, o.ExtensionName} {
		if s != "" && !description.MatchString(s) {
			return errors.New("设备描述和扩展名称须为 1–64 位普通 ASCII 文字")
		}
	}
	for _, size := range [][2]int{{o.DisplayWidth, o.DisplayHeight}, {o.BrowserWidth, o.BrowserHeight}} {
		if size == [2]int{} {
			continue
		}
		if size[0] < 800 || size[0] > 3840 || size[1] < 600 || size[1] > 2160 {
			return errors.New("尺寸须同时填写宽高：宽 800–3840，高 600–2160")
		}
	}
	if o.DisplayWidth != 0 && o.BrowserWidth != 0 && (o.BrowserWidth > o.DisplayWidth || o.BrowserHeight > o.DisplayHeight) {
		return errors.New("浏览器初始窗口不能大于虚拟显示")
	}
	if o.WebGL != "" && o.WebGL != "native" && o.WebGL != "disabled" {
		return errors.New("WebGL 选项无效")
	}
	if o.Canvas != "" && o.Canvas != "native" && o.Canvas != "blocked" {
		return errors.New("Canvas 读取选项无效")
	}
	return nil
}

func reserved(u string) bool {
	for _, s := range strings.Fields("root daemon bin sys sync games man lp mail news uucp proxy www_data backup list irc gnats nobody systemd_network systemd_timesync messagebus sshd pulse polkitd cage cagesetup cageprovision") {
		if s == u {
			return true
		}
	}
	return false
}

func choose(n int) (int, error) {
	v, e := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if e != nil {
		return 0, e
	}
	return int(v.Int64()), nil
}
func randomText(n int) (string, error) {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		v, e := choose(len(chars))
		if e != nil {
			return "", e
		}
		b[i] = chars[v]
	}
	return string(b), nil
}

func New(o Options) (*Profile, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if o.Hostname == "" {
		i, e := guestidentity.New()
		if e != nil {
			return nil, e
		}
		o.Hostname = i.Hostname
	}
	userRandom := o.Username == ""
	for _, dst := range []*string{&o.Username, &o.BrowserUsername, &o.AudioName} {
		if *dst == "" {
			s, e := randomText(10)
			if e != nil {
				return nil, e
			}
			*dst = "u" + s
		}
	}
	for o.Username == o.BrowserUsername {
		s, e := randomText(10)
		if e != nil {
			return nil, e
		}
		if userRandom {
			o.Username = "u" + s
		} else {
			o.BrowserUsername = "u" + s
		}
	}
	if o.AudioDescription == "" {
		names := []string{"Speakers", "Headphones", "Digital Audio", "Stereo Output", "Built-in Audio", "Line Out"}
		i, e := choose(len(names))
		if e != nil {
			return nil, e
		}
		o.AudioDescription = names[i]
	}
	if o.DisplayWidth == 0 {
		sizes := [][2]int{{1920, 1080}, {1920, 1200}, {2560, 1440}, {2560, 1600}, {2880, 1800}, {3840, 2160}}
		var fitting [][2]int
		for _, size := range sizes {
			if size[0] >= o.BrowserWidth && size[1] >= o.BrowserHeight {
				fitting = append(fitting, size)
			}
		}
		sizes = fitting
		i, e := choose(len(sizes))
		if e != nil {
			return nil, e
		}
		o.DisplayWidth, o.DisplayHeight = sizes[i][0], sizes[i][1]
	}
	if o.BrowserWidth == 0 {
		sizes := [][2]int{{1024, 768}, {1280, 720}, {1280, 800}, {1366, 768}, {1440, 900}, {1600, 900}}
		var sizesFit [][2]int
		for _, s := range sizes {
			if s[0] <= o.DisplayWidth && s[1] <= o.DisplayHeight {
				sizesFit = append(sizesFit, s)
			}
		}
		if len(sizesFit) == 0 {
			o.BrowserWidth, o.BrowserHeight = o.DisplayWidth, o.DisplayHeight
		} else {
			i, e := choose(len(sizesFit))
			if e != nil {
				return nil, e
			}
			o.BrowserWidth, o.BrowserHeight = sizesFit[i][0], sizesFit[i][1]
		}
	}
	if o.ExtensionName == "" {
		names := []string{"Tab Layout", "Browser Workspace", "Window Panels", "Tab Organizer", "Workspace Tools"}
		i, e := choose(len(names))
		if e != nil {
			return nil, e
		}
		o.ExtensionName = names[i]
	}
	if o.WebGL == "" {
		o.WebGL = "native"
	}
	if o.Canvas == "" {
		o.Canvas = "native"
	}
	b := make([]byte, 8)
	if _, e := io.ReadFull(rand.Reader, b); e != nil {
		return nil, e
	}
	const hex = "0123456789abcdef"
	s := make([]byte, 16)
	for i, v := range b {
		s[i*2], s[i*2+1] = hex[v>>4], hex[v&15]
	}
	p := &Profile{Version: 1, Options: o, ExtensionSuffix: string(s)}
	return p, p.Validate()
}

func (p *Profile) Validate() error {
	if p == nil || p.Version != 1 || !suffix.MatchString(p.ExtensionSuffix) {
		return errors.New("客机创建配置版本或扩展标识无效")
	}
	if p.Hostname == "" || p.Username == "" || p.BrowserUsername == "" || p.AudioName == "" || p.AudioDescription == "" || p.ExtensionName == "" || p.DisplayWidth == 0 || p.BrowserWidth == 0 || p.WebGL == "" || p.Canvas == "" {
		return errors.New("客机创建配置不完整")
	}
	return p.Options.Validate()
}

// Load returns nil for legacy environments. A corrupt record never falls back.
func Load(directory string) (*Profile, error) {
	path := filepath.Join(directory, Filename)
	info, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if info.Size() > 8192 {
		return nil, errors.New("客机创建配置超过大小限制")
	}
	if e := host.CheckPrivateFile(path); e != nil {
		return nil, e
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 8193))
	d.DisallowUnknownFields()
	var p Profile
	if e = d.Decode(&p); e != nil {
		return nil, errors.New("客机创建配置格式无效")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, errors.New("客机创建配置须仅含一个 JSON 对象")
	}
	return &p, p.Validate()
}

// Create never overwrites an existing profile, including after a failed install.
func Create(directory string, o Options) (*Profile, error) {
	if p, e := Load(directory); e != nil || p != nil {
		return p, e
	}
	p, e := New(o)
	if e != nil {
		return nil, e
	}
	if e = host.EnsurePrivateDir(directory); e != nil {
		return nil, e
	}
	b, e := json.MarshalIndent(p, "", "  ")
	if e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(directory, Filename), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(e) {
		return Load(directory)
	}
	if e != nil {
		return nil, e
	}
	if e = host.ProtectFile(f.Name()); e == nil {
		_, e = f.Write(append(b, '\n'))
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return nil, e
	}
	return p, nil
}
