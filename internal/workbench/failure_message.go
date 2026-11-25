package workbench

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/host"
	"github.com/sylvinus/agent-vm/internal/usererror"
)

const failureHelp = "在设置中导出诊断包，并附上出错时正在执行的操作。"

var limaNameTooLong = regexp.MustCompile("\\binstance name [`'\"]?[a-z0-9_.-]+[`'\"]? too long\\b")

// Only recognize known diagnostics. Unrecognized process output stays in the
// existing operation log and never becomes an arbitrary user-facing message.
func executionFailure(action string, err error, output string) error {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "unix_path_max") ||
		strings.Contains(lower, "vm name too long") ||
		strings.Contains(output, cage.LimaSocketPathReason) ||
		limaNameTooLong.MatchString(lower) {
		return usererror.New(action, cage.LimaSocketPathReason, cage.LimaSocketPathHelp, err)
	}
	if reason, next, ok := imageDownloadFailure(output); ok {
		return usererror.New(action, reason, next, err)
	}
	for _, rule := range []struct {
		patterns     []string
		reason, next string
	}{
		{[]string{strings.ToLower(host.HypervisorPlatformMissing), strings.ToLower(host.HypervisorNotReady)}, "Windows 虚拟化尚未就绪", "请在“启用或关闭 Windows 功能”中启用 Windows Hypervisor Platform（Windows 虚拟机监控程序平台），确认固件已开启 CPU 虚拟化，重启电脑后再继续。"},
		{[]string{"is still running, or lima cannot say"}, "无法确认环境已停止：环境进程状态异常（例如虚拟机管理进程已退出而虚拟机仍在运行）", "Cage 已保留环境。请重试停止；仍失败时在设置中导出诊断包，不要删除环境目录。"},
		{[]string{`qemu-img": executable file not found`, `qemu-img\": executable file not found`, `qemu-system-x86_64": executable file not found`, `qemu-system-x86_64\": executable file not found`, "lima needs 'qemu-"}, "找不到运行虚拟机所需的 QEMU 组件（如 qemu-img）", "请重新运行 Cage 安装程序以恢复随附的 QEMU；从 WSL 或脚本启动 Cage 时，确认环境变量 PATHEXT 包含 .EXE。"},
		{[]string{"base vm not found"}, "基础模板缺失，无法创建或重置环境", "请先完成环境安装；已有环境的磁盘应保留，不要删除或重置个人数据。"},
		{[]string{"base vm setup did not complete"}, "基础模板尚未安装完成", "请继续环境安装后重试；已有环境的磁盘应保留，不要删除或重置个人数据。"},
		{[]string{"desktop file service state=failed"}, "Claude Desktop 的文件服务已经退出", "请从 Cage 重新打开 Claude Desktop；若应用反复退出，请在设置中查看资源用量并导出诊断包。"},
		{[]string{"curl: (7)", "connection refused"}, "目标服务没有接受连接", "请确认环境及所选代理已启动；文件传递时请重新打开接收文件的应用，再重试。"},
		{[]string{"no space left on device", "not enough space on the disk"}, "磁盘空间不足，无法继续写入", "请释放环境所在磁盘的空间后继续；不要删除 Cage 的个人数据目录。"},
		{[]string{"dpkg was interrupted"}, "上次中断的软件安装未能自动补完，环境的软件包管理器仍停在未完成状态", "请重试一次；仍失败时在设置中导出诊断包。不要手动删除 dpkg 的状态或锁文件。"},
		{[]string{"could not get lock", "unable to acquire the dpkg", "another process using it"}, "环境中已有另一个软件安装或更新进程", "请等待当前安装结束后再继续；不要手动删除安装锁文件。"},
		{[]string{"temporary failure resolving", "could not resolve host", "curl: (6)", "something wicked happened resolving", "no address associated with hostname"}, "下载服务器的域名解析失败", "请在网络与代理设置中检查所选节点和 DNS，再继续安装。"},
		{[]string{"curl: (28)", "connection timed out", "operation timed out"}, "下载或环境连接等待超时", "请检查所选代理是否可用，再继续；已保存的下载会尝试续传。"},
		{[]string{"curl: (60)", "certificate verify failed", "certificate verification failed"}, "下载服务器的证书验证未通过", "请检查电脑时间和所选代理；仍失败时导出诊断包，不要关闭证书验证。"},
		{[]string{"curl: (35)", "unexpected eof while reading", "ssl_error_syscall"}, "与下载服务器的 TLS 加密握手未能完成，连接被中途断开", "这通常发生在所选代理或出口与该服务器之间。请在网络与代理设置中换一个节点后重试；仍失败时，可在“设置 → 网络与代理”中开启“安装与工具下载兼容模式（限制 curl 最高使用 TLS 1.2）”再试。不要关闭证书验证。"},
		{[]string{"407 proxy authentication", "proxy authentication required"}, "代理服务器拒绝了当前登录凭据", "请在网络与代理设置中更新代理账号或订阅，再应用节点。"},
		{[]string{"permission denied", "access is denied"}, "系统拒绝访问安装所需的文件或程序", "请检查应用及环境目录的权限；若安全软件拦截了 Cage，请先核实其拦截记录。"},
		{[]string{"which lima cannot mount"}, "目录路径含有空格，Lima 无法把它共享进环境", "操作详情中列出了这个目录；把它移到不含空格的路径后重试。若是 Cage 配置目录旁的内部传输目录，步骤见故障排查文档“用户目录含空格时环境无法启动”。"},
		{[]string{"is also your own lima's home", "set agent_vm_lima_home to a folder for agent-vm alone"}, "宿主机环境变量 LIMA_HOME 与 Cage 的虚拟机目录重合", "请取消设置 LIMA_HOME 或将其改为其他目录，完全退出并重新打开 Cage 后重试；若正在安装应用更新，请完全退出 Cage 后直接运行官方安装包原地升级，原有数据和虚拟机磁盘会保留。"},
		{[]string{"tried to start delayed item"}, "环境的软件包下载程序反复重试，未能完成安装", "请使用包含 APT 修复的 Cage 构建后继续安装；仍失败时导出诊断包。"},
	} {
		for _, pattern := range rule.patterns {
			if strings.Contains(lower, pattern) {
				return usererror.New(action, rule.reason, rule.next, err)
			}
		}
	}
	return usererror.Wrap(action, err, "打开“操作详情”查看最后的错误；仍无法判断时，"+failureHelp)
}

func serviceHTTPFailure(action string, status int) error {
	reason, next := fmt.Sprintf("服务返回 HTTP %d，尚未取得可识别的具体原因", status), failureHelp
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		reason, next = "服务拒绝了这次连接的授权", "请重新连接当前环境；若仍出现，更新环境组件后再试。"
	case http.StatusNotFound, http.StatusGone:
		reason, next = "目标窗口、文件或服务接口已不存在", "请重新打开目标窗口或重新选择文件；已有环境较旧时，先更新环境组件。"
	case http.StatusConflict:
		reason, next = "目标状态已变化，或另一个操作尚未结束", "请确认当前页面与目标文件，再重新发起这次操作。"
	case http.StatusRequestEntityTooLarge:
		reason, next = "文件或请求内容超过服务允许的大小", "请缩小文件或分批处理，再重新发起这次操作。"
	case http.StatusTooManyRequests:
		reason, next = "服务暂时限制了请求次数", "请等待片刻后重试，避免连续点击。"
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		reason, next = "环境内的服务尚未就绪或已停止响应", "请先确认环境已启动；等待后仍无法连接时，查看环境设置中的组件状态。"
	case http.StatusGatewayTimeout:
		reason, next = "等待环境内服务响应超时", "请检查环境负载并稍后再试；持续超时则导出诊断包。"
	}
	return usererror.New(action, reason, next, nil)
}

func fileServiceFailure(status int, data []byte) error {
	known := map[string]string{
		"上传窗口已关闭，请重新选择文件":         "上传窗口已关闭。请重新点击应用内的上传按钮，再选择文件。",
		"原文件窗口已关闭":                "原文件窗口已关闭。请在接收文件的应用中重新打开文件窗口。",
		"请先完成已打开的文件窗口":            "另一个文件窗口尚未处理。请先完成或关闭该窗口，再重新选择文件。",
		"每个文件不能超过 2 GB":           "单个文件超过 2 GB 上限。请缩小文件后重新上传。",
		"项目文件夹尚未连接，请重新打开项目":       "项目文件夹尚未连接。请先在 Cage 中重新打开该项目，再选择目录。",
		"目标没有接收文件，请点击支持附件的输入框后重试": "目标没有接收文件。请先点击应用内支持附件的输入框，再重新选择文件。",
		"上传已取消":                   "上传已被取消。需要继续时，请重新选择文件。",
		"当前窗口没有请求文件夹":             "当前窗口需要选择文件，不能接收文件夹。请按应用的窗口提示重新选择。",
		"文件夹正在交给应用":               "文件夹仍在传给应用。请等待当前操作结束后再选择。",
		"文件位置无法读取":                "接收文件的应用无法读取该位置。请确认项目目录已连接，并重新选择。",
		"下载服务不可用":                 "环境内的下载服务尚未就绪。请重新打开应用；旧环境请先更新环境组件。",
		"文件请求过大":                  "这次文件请求超过服务允许的大小。请减少一次传递的文件数量。",
		"请选择项目文件夹":                "当前操作需要一个已连接的项目文件夹。请先在 Cage 中打开项目，再重新选择目录。",
	}
	if reason := known[strings.TrimSpace(string(data))]; reason != "" {
		return fmt.Errorf("传递文件失败：%s", reason)
	}
	return serviceHTTPFailure("传递文件", status)
}

func browserOperation(path string) string {
	switch path {
	case "/navigate":
		return "打开网页"
	case "/reload":
		return "刷新网页"
	case "/audit":
		return "读取浏览器检测结果"
	case "/tabs":
		return "读取浏览器标签页"
	case "/paste-image":
		return "粘贴图片"
	case "/editor-restart":
		return "打开编辑器"
	case "/extensions":
		return "读取浏览器扩展"
	default:
		return "执行浏览器操作"
	}
}

type failureOutput struct {
	mu   sync.Mutex
	tail []byte
}

func (b *failureOutput) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	if len(data) > 8192 {
		data = data[len(data)-8192:]
	}
	b.tail = append(b.tail, data...)
	if len(b.tail) > 8192 {
		b.tail = b.tail[len(b.tail)-8192:]
	}
	return n, nil
}
func (b *failureOutput) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.tail) }
func (b *failureOutput) writer(dst io.Writer) io.Writer {
	if dst == nil {
		return b
	}
	return io.MultiWriter(dst, b)
}
