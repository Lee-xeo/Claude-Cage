package workbench

import (
	"regexp"
	"strings"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/usererror"
)

const (
	aptFailureMarker  = "APT_FAILED "
	aptTailLines      = 6
	aptTailLineBytes  = 200
	aptCommandBytes   = 300
	aptNotFoundNeedle = "unable to locate package"
)

var aptMissingPackage = regexp.MustCompile(`(?i)unable to locate package ([a-z0-9][a-z0-9+.-]*)|package '?([a-z0-9][a-z0-9+.-]*)'? has no installation candidate`)

// aptEvidence is what the setup script reported about a failed apt-get call.
type aptEvidence struct {
	exit, command string
	packages      []string
	tail          []string
}

// parseAPTFailure reads the last APT failure marker written by
// agent-vm.setup.sh together with the output lines before it. It returns nil
// when the log has no marker, so older guests keep the generic message.
func parseAPTFailure(log string) *aptEvidence {
	lines := strings.Split(strings.ReplaceAll(log, "\r", ""), "\n")
	at := -1
	payload := ""
	for i := len(lines) - 1; i >= 0; i-- {
		// Older setup scripts may still report the former marker.
		for _, marker := range []string{aptFailureMarker, "CAGE_APT_FAILED "} {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), marker); ok {
				at, payload = i, rest
				break
			}
		}
		if at >= 0 {
			break
		}
	}
	if at < 0 {
		return nil
	}
	ev := &aptEvidence{}
	for _, field := range strings.SplitN(payload, " command=", 2) {
		if strings.HasPrefix(field, "exit=") {
			ev.exit = strings.TrimPrefix(field, "exit=")
		} else {
			ev.command = field
		}
	}
	for _, arg := range strings.Fields(ev.command) {
		if !strings.HasPrefix(arg, "-") && arg != "apt-get" && arg != "install" && arg != "update" && !strings.Contains(arg, "=") {
			ev.packages = append(ev.packages, arg)
		}
	}
	for i := at - 1; i >= 0 && len(ev.tail) < aptTailLines; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			ev.tail = append([]string{clip(line, aptTailLineBytes)}, ev.tail...)
		}
	}
	return ev
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

// packageInstallFailure explains a failed base package stage. Only fields the
// guest reported are shown, and everything passes through diagnostics.Redact so
// proxy addresses, credentials and paths never reach the interface.
func packageInstallFailure(action string, cause error, log string) error {
	ev := parseAPTFailure(log)
	if ev == nil {
		return nil
	}
	tail := strings.Join(ev.tail, "\n")
	reason := "APT 命令以退出码 " + ev.exit + " 结束，输出中没有可识别的原因"
	next := "请查看下方输出末尾；仍无法判断时，导出诊断包并附上这些信息。"
	lower := strings.ToLower(tail)
	matched := false
	for _, rule := range []struct {
		patterns     []string
		reason, next string
	}{
		{[]string{"temporary failure resolving", "could not resolve", "name or service not known", "something wicked happened resolving", "no address associated with hostname"}, "APT 无法解析下载服务器域名", "请在网络与代理设置中检查所选节点和 DNS，再继续安装。"},
		{[]string{"connection timed out", "timed out", "could not connect to", "unable to connect to", "connection failed", "connection refused"}, "APT 连接下载服务器失败或超时", "请检查所选代理是否可用，再继续安装。"},
		{[]string{"proxy authentication required", "407 proxy"}, "代理服务器拒绝了当前登录凭据", "请在网络与代理设置中更新代理账号或订阅，再应用节点。"},
		{[]string{aptNotFoundNeedle, "has no installation candidate"}, "软件源中找不到所需的软件包", "请确认软件源可访问且 apt update 已成功，再继续安装。"},
		{[]string{"no space left on device"}, "磁盘空间不足，无法继续写入", "请释放环境所在磁盘的空间后继续。"},
	} {
		for _, p := range rule.patterns {
			if strings.Contains(lower, p) {
				reason, next, matched = rule.reason, rule.next, true
				break
			}
		}
		if matched {
			break
		}
	}
	pkgs := strings.Join(ev.packages, " ")
	if m := aptMissingPackage.FindStringSubmatch(tail); m != nil {
		if pkg := m[1] + m[2]; pkg != "" {
			pkgs = pkg
		}
	}
	detail := "失败命令：" + clip(ev.command, aptCommandBytes)
	if pkgs != "" && strings.Contains(ev.command, "install") {
		detail += "；涉及软件包：" + clip(pkgs, aptCommandBytes)
	}
	if tail != "" {
		detail += "；错误输出末尾：" + strings.ReplaceAll(tail, "\n", " | ")
	}
	diagnostics.Log("error", "package-install", "reason="+reason+" "+detail)
	return usererror.New(action, reason, diagnostics.Redact(detail)+"。"+next, cause)
}
