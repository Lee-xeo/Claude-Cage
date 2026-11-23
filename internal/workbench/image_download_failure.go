package workbench

import (
	"regexp"
	"strings"

	"github.com/sylvinus/agent-vm/internal/vm"
)

// The summary line carries only fixed codes; anything else is not accepted,
// so process output never becomes an arbitrary user-facing message.
var imageDownloadLine = regexp.MustCompile(`^` + vm.ImageDownloadMarker + `((?: (?:official|nju|ustc|other):(?:http-[1-5][0-9][0-9]|eof|timeout|tls|dns|refused|proxy|checksum|other):(?:kept|none))+)\s*$`)

var imageMirrorNames = map[string]string{
	"official": "Debian 官方源",
	"nju":      "南京大学镜像",
	"ustc":     "中国科学技术大学镜像",
	"other":    "其他下载地址",
}

var imageCauseNames = map[string]string{
	"http-403": "HTTP 403，服务器拒绝访问",
	"http-404": "HTTP 404，文件不存在",
	"http-429": "HTTP 429，请求过于频繁",
	"eof":      "连接被中途断开",
	"timeout":  "连接超时",
	"tls":      "TLS 或证书错误",
	"dns":      "域名解析失败",
	"refused":  "连接被拒绝",
	"proxy":    "无法连接宿主机代理",
	"checksum": "文件校验不符，下载内容已丢弃",
	"other":    "其他错误",
}

// imageDownloadFailure explains a failed Debian image download by location:
// which mirror, why, and whether partial data remains for resuming.
func imageDownloadFailure(output string) (reason, next string, ok bool) {
	var tokens []string
	for _, line := range strings.Split(output, "\n") {
		if m := imageDownloadLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			tokens = strings.Fields(m[1])
		}
	}
	if len(tokens) == 0 {
		return "", "", false
	}
	parts, kept, hostProxy := make([]string, 0, len(tokens)), false, false
	for _, token := range tokens {
		fields := strings.Split(token, ":")
		hostProxy = hostProxy || fields[1] == "proxy"
		cause, known := imageCauseNames[fields[1]]
		if !known {
			cause = "HTTP " + strings.TrimPrefix(fields[1], "http-")
			if strings.HasPrefix(fields[1], "http-5") {
				cause += "，服务器错误"
			}
		}
		if fields[2] == "kept" {
			kept = true
			cause += "，已保留下载进度"
		}
		parts = append(parts, imageMirrorNames[fields[0]]+"（"+cause+"）")
	}
	reason = "Debian 镜像的下载地址都未能完成下载：" + strings.Join(parts, "；")
	next = "这通常与所选代理出口和镜像站之间的连接有关。请在网络与代理设置中换一个节点，或稍后再继续安装"
	if hostProxy {
		// The image is fetched by the host through its own proxy settings,
		// so a different Cage node cannot fix this failure.
		next = "镜像由宿主机经系统代理或 HTTP_PROXY、HTTPS_PROXY、ALL_PROXY 环境变量下载，不经过 Cage 节点，换节点无效。请确认这些设置指向正在运行的代理端口（macOS 还要检查 launchctl getenv 返回的用户域变量），再继续安装"
	}
	if kept {
		next += "；已下载的部分会保留，继续安装时从断点续传"
	}
	return reason, next + "。", true
}
