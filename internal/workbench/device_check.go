package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/sylvinus/agent-vm/internal/guestprofile"
)

type deviceObservation struct {
	UserAgent   string `json:"user_agent"`
	Platform    string `json:"platform"`
	ClientHints *struct {
		Brands []struct {
			Brand   string `json:"brand"`
			Version string `json:"version"`
		} `json:"brands"`
		Platform string `json:"platform"`
		Mobile   bool   `json:"mobile"`
	} `json:"client_hints"`
	Screen struct {
		Width      int     `json:"width"`
		Height     int     `json:"height"`
		ColorDepth int     `json:"color_depth"`
		PixelRatio float64 `json:"pixel_ratio"`
	} `json:"screen"`
	Viewport struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"viewport"`
	Canvas struct {
		State    string `json:"state"`
		Checksum string `json:"checksum,omitempty"`
	} `json:"canvas"`
	WebGL struct {
		State    string `json:"state"`
		API      string `json:"api,omitempty"`
		Vendor   string `json:"vendor"`
		Renderer string `json:"renderer"`
		Version  string `json:"version"`
	} `json:"webgl"`
	Audio struct {
		State  string   `json:"state"`
		Labels []string `json:"labels"`
	} `json:"audio"`
	WebGPU bool `json:"webgpu"`
}

type deviceSystemObservation struct {
	Hostname         string `json:"hostname"`
	Username         string `json:"username"`
	Home             string `json:"home"`
	AudioName        string `json:"audio_name"`
	AudioDescription string `json:"audio_description"`
}

type deviceCheckItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Evidence string `json:"evidence"`
}
type deviceCheckReport struct {
	Target        string                   `json:"target"`
	CheckedAt     time.Time                `json:"checked_at"`
	Checks        []deviceCheckItem        `json:"checks"`
	Browser       *deviceObservation       `json:"browser,omitempty"`
	System        *deviceSystemObservation `json:"system,omitempty"`
	Application   *claudeDesktopState      `json:"application,omitempty"`
	RendererClass string                   `json:"renderer_class,omitempty"`
}

func publicDeviceText(s string, limit int) bool {
	return len(s) <= limit && !strings.ContainsAny(s, "\x00\r\n")
}
func (v *deviceObservation) valid() bool {
	if v == nil || !publicDeviceText(v.UserAgent, 1024) || !publicDeviceText(v.Platform, 128) || !publicDeviceText(v.WebGL.Renderer, 512) || !publicDeviceText(v.WebGL.Vendor, 256) || !publicDeviceText(v.WebGL.Version, 256) || len(v.Audio.Labels) > 8 {
		return false
	}
	for _, label := range v.Audio.Labels {
		if !publicDeviceText(label, 128) {
			return false
		}
	}
	if !publicDeviceText(v.WebGL.API, 16) {
		return false
	}
	if v.Canvas.State != "passed" && v.Canvas.State != "failed" && v.Canvas.State != "blocked" && v.Canvas.State != "unavailable" {
		return false
	}
	if v.WebGL.State != "passed" && v.WebGL.State != "failed" && v.WebGL.State != "unavailable" && v.WebGL.State != "readback_blocked" {
		return false
	}
	if v.Canvas.Checksum != "" && !regexp.MustCompile(`^[a-f0-9]{8}$`).MatchString(v.Canvas.Checksum) {
		return false
	}
	if v.ClientHints != nil {
		if len(v.ClientHints.Brands) > 8 || !publicDeviceText(v.ClientHints.Platform, 128) {
			return false
		}
		for _, brand := range v.ClientHints.Brands {
			if !publicDeviceText(brand.Brand, 64) || !publicDeviceText(brand.Version, 32) {
				return false
			}
		}
	}
	return v.Screen.Width >= 0 && v.Screen.Width <= 32768 && v.Screen.Height >= 0 && v.Screen.Height <= 32768 && v.Screen.PixelRatio >= 0 && v.Screen.PixelRatio <= 16 && v.Viewport.Width >= 0 && v.Viewport.Width <= 32768 && v.Viewport.Height >= 0 && v.Viewport.Height <= 32768
}

var chromeMajor = regexp.MustCompile(`(?:Chrome|Chromium|Edg)/([0-9]+)\.`)
var softwareRenderer = regexp.MustCompile(`(?i)swiftshader|llvmpipe|softpipe|software|\bwarp\b|basic render`)
var virtualRenderer = regexp.MustCompile(`(?i)virgl|paravirtual|virtio`)

func evaluateDevice(p *guestprofile.Profile, v *deviceObservation) ([]deviceCheckItem, string) {
	if !v.valid() {
		return []deviceCheckItem{{"browser-evidence", "浏览器观测", "unknown", "客机尚未提供完整、有效的本地观测数据；请更新组件后重测。"}}, "unobserved"
	}
	items := []deviceCheckItem{}
	add := func(id, title, state, evidence string) {
		items = append(items, deviceCheckItem{id, title, state, evidence})
	}
	ua := chromeMajor.FindStringSubmatch(v.UserAgent)
	if len(ua) == 2 && v.ClientHints != nil {
		matched := false
		inconsistent := false
		platform := ""
		switch {
		case strings.Contains(v.UserAgent, "Windows NT"):
			platform = "Windows"
		case strings.Contains(v.UserAgent, "Macintosh"):
			platform = "macOS"
		case strings.Contains(v.UserAgent, "X11; Linux"):
			platform = "Linux"
		}
		if platform != "" && v.ClientHints.Platform != "" && platform != v.ClientHints.Platform {
			inconsistent = true
		}
		for _, brand := range v.ClientHints.Brands {
			if brand.Brand == "Chromium" || brand.Brand == "Google Chrome" || brand.Brand == "Microsoft Edge" {
				matched = true
				if brand.Version != ua[1] {
					inconsistent = true
				}
			}
		}
		if inconsistent {
			add("user-agent", "浏览器 UA 与 Client Hints", "failed", "实际浏览器主版本或系统平台不一致；请恢复浏览器的原生身份设置。")
		} else if matched {
			add("user-agent", "浏览器 UA 与 Client Hints", "passed", "已提供的浏览器主版本与系统平台信息一致。此项不验证浏览器安装来源。")
		} else {
			add("user-agent", "浏览器 UA 与 Client Hints", "unknown", "浏览器未提供可核对的 Chromium 品牌版本。")
		}
	} else {
		add("user-agent", "浏览器 UA 与 Client Hints", "unknown", "当前文档未暴露 Client Hints，不能据此判定版本一致。")
	}
	class := "unobserved"
	if v.WebGL.Renderer != "" && v.WebGL.Renderer != "WebKit WebGL" && v.WebGL.Renderer != "WebGL" {
		class = "driver-reported"
		if softwareRenderer.MatchString(v.WebGL.Renderer) {
			class = "software"
		} else if virtualRenderer.MatchString(v.WebGL.Renderer) {
			class = "virtual-gpu"
		}
	}
	if p != nil && p.WebGL == "disabled" {
		if v.WebGL.State == "unavailable" {
			add("webgl-render", "WebGL 渲染正确性", "passed", "WebGL 未创建上下文，与已保存的关闭选项一致。")
		} else {
			add("webgl-render", "WebGL 渲染正确性", "failed", "已保存关闭 WebGL，但实际文档仍可创建上下文；请重新启动浏览器并检查组件。")
		}
	} else if v.WebGL.State == "passed" {
		add("webgl-render", "WebGL 渲染正确性", "passed", "着色器编译、链接及 4×4 像素回读符合参考颜色。此结果不证明物理 GPU 直通。")
	} else if v.WebGL.State == "failed" {
		add("webgl-render", "WebGL 渲染正确性", "failed", "着色器或像素回读不符合预期，请检查所选后端和驱动。")
	} else {
		add("webgl-render", "WebGL 渲染正确性", "unknown", "WebGL 不可用或像素读取受限，未完成渲染正确性验证。")
	}
	backend := "auto"
	if p != nil && p.GraphicsBackend != "" {
		backend = p.GraphicsBackend
	}
	if class == "software" && backend != "software" {
		state := "warning"
		if backend != "auto" {
			state = "failed"
		}
		add("graphics-backend", "实际图形后端", state, "检测到软件渲染："+v.WebGL.Renderer)
	} else if class == "unobserved" {
		add("graphics-backend", "实际图形后端", "unknown", "未观测到渲染器，不能声称已启用硬件加速。")
	} else {
		state := "passed"
		evidence := v.WebGL.Renderer
		if class == "virtual-gpu" {
			evidence = "虚拟 GPU 驱动：" + evidence
		}
		if backend == "software" && class != "software" {
			state = "failed"
			evidence = "所选软件后端与实际渲染器不一致：" + evidence
		}
		markers := map[string]string{"metal": "metal", "d3d11": "direct3d11", "vulkan": "vulkan", "opengl": "opengl"}
		if marker := markers[backend]; marker != "" && !strings.Contains(strings.ToLower(v.WebGL.Renderer+" "+v.WebGL.Version), marker) {
			state = "unknown"
			evidence = "驱动报告未能确认所选 API：" + evidence
		}
		if p != nil && p.GPUBackend == "virgl" && !strings.Contains(strings.ToLower(v.WebGL.Renderer), "virgl") {
			state = "failed"
			evidence = "未观测到请求的 VirGL 渲染器：" + evidence
		}
		add("graphics-backend", "实际图形后端", state, evidence)
	}
	if p != nil && p.Canvas == "blocked" && (v.Canvas.State == "passed" || v.Canvas.State == "failed") {
		add("canvas-render", "Canvas 渲染与读取", "failed", "已保存阻止 Canvas 读取，但实际文档仍可回读像素；请重新启动浏览器并检查组件。")
	} else if v.Canvas.State == "passed" {
		add("canvas-render", "Canvas 渲染与读取", "passed", "已完成已知颜色回读，字体样本校验值为 "+v.Canvas.Checksum+"；不要求不同设备必须产生不同值。")
	} else if p != nil && p.Canvas == "blocked" && v.Canvas.State == "blocked" {
		add("canvas-render", "Canvas 渲染与读取", "passed", "图像读取受限，与已保存选项一致。")
	} else if v.Canvas.State == "failed" {
		add("canvas-render", "Canvas 渲染与读取", "failed", "已知颜色回读不符合预期。")
	} else {
		add("canvas-render", "Canvas 渲染与读取", "unknown", "当前无法读取 Canvas 像素，未推断实际图像特征。")
	}
	if len(v.Audio.Labels) == 0 {
		add("browser-audio", "浏览器音频设备", "unknown", "文档未获设备标签权限；检测不会申请麦克风权限，也不会把空标签判为通过。")
	} else {
		add("browser-audio", "浏览器音频设备", "observed", strings.Join(v.Audio.Labels, "；"))
	}
	return items, class
}

// Fixed local commands return only public fields. No process arguments,
// account files, application profiles or full PulseAudio properties leave it.
const deviceSystemProbe = `python3 - <<'CAGE_DEVICE_SYSTEM'
import json, os, pwd, socket, subprocess
account=pwd.getpwuid(os.getuid())
result={'hostname':socket.gethostname(),'username':account.pw_name,'home':account.pw_dir,'audio_name':'','audio_description':''}
try:
    text=subprocess.run(['pactl','--server=unix:/run/cage-audio/native','-f','json','list','sinks'],capture_output=True,text=True,timeout=3,check=True).stdout
    sinks=json.loads(text)
    if len(sinks)==1:
        result['audio_name']=str(sinks[0].get('name',''))[:120]
        result['audio_description']=str(sinks[0].get('description',''))[:128]
except Exception:
    pass
print(json.dumps(result))
CAGE_DEVICE_SYSTEM`

func (s *Server) deviceCheck(w http.ResponseWriter, r *http.Request) {
	if !s.hasTrustedView(r) {
		http.Error(w, "请在已信任的工作台中检测客机", http.StatusForbidden)
		return
	}
	var in struct {
		Target string `json:"target"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Target != "browser" && in.Target != "desktop" {
		http.Error(w, "检测对象无效", 400)
		return
	}
	if !s.vmRunning() {
		http.Error(w, "请先启动客机", 409)
		return
	}
	if !s.op.TryLock() {
		http.Error(w, "环境操作进行中，请稍后检测", 409)
		return
	}
	defer s.op.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	p, err := guestprofile.Load(s.profile)
	if err != nil {
		requestError(w, err)
		return
	}
	report := deviceCheckReport{Target: in.Target, CheckedAt: time.Now(), Checks: []deviceCheckItem{}}
	if s.macosGuest == nil && s.windowsGuest == nil {
		if data, e := s.guest(ctx, deviceSystemProbe); e == nil {
			var machine deviceSystemObservation
			if json.Unmarshal(data, &machine) == nil && publicDeviceText(machine.Hostname, 255) && publicDeviceText(machine.Username, 64) && publicDeviceText(machine.Home, 128) && publicDeviceText(machine.AudioName, 120) && publicDeviceText(machine.AudioDescription, 128) {
				report.System = &machine
			}
		}
	}
	if report.System != nil && p != nil {
		for _, item := range []struct{ id, title, observed, expected string }{{"hostname", "客机主机名", report.System.Hostname, p.Hostname}, {"username", "工作账户", report.System.Username, p.Username}, {"home", "工作主目录", report.System.Home, p.WorkHomePath()}, {"audio-model", "系统音频设备", report.System.AudioDescription, p.AudioDescription}} {
			state := "passed"
			if item.observed == "" {
				state = "unknown"
			} else if !strings.EqualFold(item.observed, item.expected) {
				state = "failed"
			}
			report.Checks = append(report.Checks, deviceCheckItem{item.id, item.title, state, "实际值：" + item.observed})
		}
	}
	if in.Target == "desktop" {
		state, err := s.deviceDesktopState(ctx)
		if err == nil {
			report.Application = state
			status := "unknown"
			evidence := "应用未运行，无法观测进程状态。"
			if state.Running {
				status = "observed"
				evidence = "已观测到 Claude Desktop 运行状态及版本 " + state.Version
			}
			report.Checks = append(report.Checks, deviceCheckItem{"desktop-process", "Claude Desktop 应用", status, evidence})
		}
		report.Checks = append(report.Checks, deviceCheckItem{"desktop-renderer", "Claude Desktop 内部渲染器", "unknown", "官方应用没有受支持的本地渲染自检接口；未读取登录资料、注入代码或开启调试端口，未用浏览器结果代替应用内观测。"})
	} else {
		cfg, e := loadConfig(s.configPath)
		if e != nil {
			requestError(w, e)
			return
		}
		var observation browserObservation
		if s.windowsGuest != nil {
			data, e := s.nativeBrowserClient().request(ctx, "POST", "/audit", map[string]any{"timezone": cfg.Timezone, "network": false})
			err = e
			if e == nil {
				err = json.Unmarshal(data, &observation)
			}
		} else {
			observation, err = s.browserObservation(ctx, cfg, false)
		}
		if err != nil {
			report.Checks = append(report.Checks, deviceCheckItem{"browser-evidence", "浏览器观测", "unknown", "本地诊断文档未完成，请确认浏览器及客机组件可用后重测。"})
		} else {
			items, class := evaluateDevice(p, observation.Device)
			report.Checks = append(report.Checks, items...)
			report.RendererClass = class
			if observation.Device.valid() {
				report.Browser = observation.Device
			}
		}
	}
	jsonReply(w, report)
}

func (s *Server) deviceDesktopState(ctx context.Context) (*claudeDesktopState, error) {
	var state claudeDesktopState
	if s.windowsGuest != nil {
		err := s.windowsDesktopRequest(ctx, "status", nil, &state)
		return &state, err
	}
	if s.macosGuest != nil {
		data, err := s.guest(ctx, `test -d /Applications/Claude.app && printf 'installed\n'; /usr/bin/pgrep -x Claude >/dev/null && printf 'running\n'; /usr/bin/defaults read /Applications/Claude.app/Contents/Info CFBundleShortVersionString 2>/dev/null || true`)
		if err != nil {
			return nil, err
		}
		lines := strings.Fields(string(data))
		for _, line := range lines {
			if line == "installed" {
				state.Installed = true
			} else if line == "running" {
				state.Running = true
			} else if regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){1,4}$`).MatchString(line) {
				state.Version = line
			}
		}
		return &state, nil
	}
	data, err := s.guestQuick(ctx, claudeDesktopSnapshot)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(data, &state) != nil {
		return nil, errors.New("应用状态无效")
	}
	return &state, nil
}
