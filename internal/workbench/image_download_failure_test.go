package workbench

import (
	"errors"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/vm"
)

// #629: every Debian image location failed; the message names each mirror,
// its cause and whether partial data is kept, instead of only an exit code.
func TestImageDownloadFailureNamesEachMirror(t *testing.T) {
	output := "Starting the selected base system...\nError: failed to start: failed to download [url]: ...\n" +
		vm.ImageDownloadMarker + " official:eof:kept nju:http-429:kept ustc:http-403:none\n"
	err := executionFailure("准备 Debian 13 镜像", errors.New("exit status 1"), output)
	text := err.Error()
	for _, want := range []string{"Debian 官方源（连接被中途断开，已保留下载进度）", "南京大学镜像（HTTP 429，请求过于频繁，已保留下载进度）", "中国科学技术大学镜像（HTTP 403，服务器拒绝访问）", "换一个节点", "断点续传"} {
		if !strings.Contains(text, want) {
			t.Errorf("message lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, "未返回可识别的原因") {
		t.Fatalf("still unexplained: %s", text)
	}
	reason, next, ok := imageDownloadFailure(vm.ImageDownloadMarker + " official:http-503:none ustc:checksum:none")
	if !ok || !strings.Contains(reason, "HTTP 503，服务器错误") || !strings.Contains(reason, "文件校验不符") || strings.Contains(next, "断点续传") {
		t.Fatalf("reason %q next %q", reason, next)
	}
}

// #713: when the host proxy refused the connection, switching Cage nodes
// cannot help, so the next step names the host proxy settings instead.
func TestImageDownloadFailureNamesHostProxy(t *testing.T) {
	reason, next, ok := imageDownloadFailure(vm.ImageDownloadMarker + " official:proxy:kept nju:proxy:none")
	if !ok || !strings.Contains(reason, "Debian 官方源（无法连接宿主机代理，已保留下载进度）") || !strings.Contains(next, "HTTPS_PROXY") || !strings.Contains(next, "换节点无效") || strings.Contains(next, "换一个节点") || !strings.Contains(next, "断点续传") {
		t.Fatalf("reason %q next %q", reason, next)
	}
}

// Only the fixed codes are accepted: a line that adds any other text is not a
// summary and never reaches the message.
func TestImageDownloadFailureRejectsOtherText(t *testing.T) {
	for _, line := range []string{
		vm.ImageDownloadMarker + " official:eof:kept 请访问 example.com",
		vm.ImageDownloadMarker + " evil:eof:kept",
		vm.ImageDownloadMarker + " official:http-429:kept:extra",
		vm.ImageDownloadMarker + " official:<script>:none",
		vm.ImageDownloadMarker,
		"prefix " + vm.ImageDownloadMarker + " official:eof:kept",
	} {
		if reason, _, ok := imageDownloadFailure(line); ok {
			t.Errorf("accepted %q as %q", line, reason)
		}
	}
}
