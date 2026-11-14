package guestprofile

import (
	"errors"
	"regexp"
	"strings"
)

var homePattern = regexp.MustCompile(`^/home/[A-Za-z][A-Za-z0-9_.-]{0,62}$`)

func validateHardwareOptions(o Options) error {
	for _, home := range []string{o.WorkHome, o.BrowserHome} {
		if home != "" && !homePattern.MatchString(home) {
			return errors.New("主目录须为 /home/ 下独立的单层目录，仅含字母、数字、点、连字符或下划线")
		}
		if home == "/home/cage" || home == "/home/cage-browser" {
			return errors.New("主目录不能使用基础镜像的保留账户目录")
		}
	}
	if o.WorkHome != "" && o.WorkHome == o.BrowserHome {
		return errors.New("两个账户不能共用主目录")
	}
	switch o.GraphicsBackend {
	case "", "auto", "metal", "d3d11", "vulkan", "opengl", "software":
	default:
		return errors.New("图形渲染后端无效")
	}
	switch o.GPUBackend {
	case "", "default", "virgl":
	default:
		return errors.New("虚拟 GPU 后端无效")
	}
	if o.WebGL == "disabled" && o.GraphicsBackend != "" && o.GraphicsBackend != "auto" {
		return errors.New("关闭 WebGL 时不能要求指定 3D 渲染后端")
	}
	if o.GPUBackend == "virgl" && o.GraphicsBackend == "vulkan" {
		return errors.New("当前 VirGL 路径仅提供 OpenGL；Vulkan 需要独立的 Venus/Gfxstream 支持")
	}
	if o.AudioModel != "" {
		m, ok := findAudioModel(o.AudioModel)
		if !ok {
			return errors.New("音频型号不存在")
		}
		if o.AudioDescription != "" && o.AudioDescription != m.Label {
			return errors.New("音频描述须与已选型号一致")
		}
		if o.AudioName != "" && !strings.HasPrefix(o.AudioName, "alsa_output.usb-"+m.Stem+"_") {
			return errors.New("音频设备名与已选型号不一致")
		}
	}
	return nil
}

func (p *Profile) WorkHomePath() string {
	if p == nil {
		return "/home/cage"
	}
	if p.WorkHome != "" {
		return p.WorkHome
	}
	return "/home/" + p.Username
}
func (p *Profile) BrowserHomePath() string {
	if p == nil {
		return "/home/cage-browser"
	}
	if p.BrowserHome != "" {
		return p.BrowserHome
	}
	return "/home/" + p.BrowserUsername
}

// Platform refers to the guest OS, not the host. No renderer names are spoofed.
func (o Options) ValidatePlatform(platform string) error {
	if o.GraphicsBackend == "metal" && platform != "darwin" {
		return errors.New("Metal 后端仅支持 macOS 客机")
	}
	if o.GraphicsBackend == "d3d11" && platform != "windows" {
		return errors.New("D3D11 后端仅支持 Windows 客机")
	}
	if o.GraphicsBackend == "vulkan" && platform != "linux" {
		return errors.New("Vulkan 后端当前仅支持 Linux 客机")
	}
	if o.GPUBackend == "virgl" && platform != "linux" {
		return errors.New("VirGL 后端当前仅支持 Linux QEMU 客机")
	}
	return nil
}

func (p *Profile) GraphicsFlags() []string {
	if p == nil {
		return nil
	}
	switch p.GraphicsBackend {
	case "metal", "d3d11", "vulkan":
		return []string{"--use-gl=angle", "--use-angle=" + p.GraphicsBackend}
	case "opengl":
		return []string{"--use-gl=angle", "--use-angle=gl"}
	case "software":
		return []string{"--use-gl=angle", "--use-angle=swiftshader"}
	}
	return nil
}
