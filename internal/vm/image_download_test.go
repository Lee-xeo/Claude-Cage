package vm

import (
	"context"
	"crypto/sha512"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lima-vm/lima/v2/pkg/fileutils"
	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/opencontainers/go-digest"
)

// Real failures from the image downloader, joined as instance.Prepare joins
// its candidates: each becomes one fixed code, partial data noted.
func TestImageDownloadSummaryFromDownloaderErrors(t *testing.T) {
	t.Setenv("CAGE_LIMA_CACHE_DIR", t.TempDir())
	content := []byte("debian image fixture")
	sum := sha512.Sum512(content)
	pinned := digest.NewDigestFromBytes(digest.SHA512, sum[:])
	tooMany := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }))
	defer tooMany.Close()
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer forbidden.Close()
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		_, _ = w.Write(content[:5]) // The connection ends before the declared length.
	}))
	defer cut.Close()
	arch := limatype.NewArch("x86_64")
	var errs []error
	for _, server := range []*httptest.Server{tooMany, forbidden, cut} {
		file := limatype.File{Location: server.URL + "/debian.qcow2", Arch: arch, Digest: pinned}
		_, err := fileutils.DownloadFile(context.Background(), filepath.Join(t.TempDir(), "image"), file, false, "the image", arch, nil)
		if err == nil {
			t.Fatalf("%s downloaded", server.URL)
		}
		errs = append(errs, err)
	}
	failure := fmt.Errorf("failed to start: %w", fileutils.Errors(errs))
	got := ImageDownloadSummary(failure)
	want := ImageDownloadMarker + " other:http-429:kept other:http-403:none other:eof:kept"
	if got != want {
		t.Fatalf("summary = %q\nwant      %q\nfrom %v", got, want, failure)
	}
	if strings.Contains(got, "127.0.0.1") || strings.Contains(got, "http://") {
		t.Fatal("summary exposes a location")
	}
}

func TestImageDownloadSummaryNamesDebianMirrors(t *testing.T) {
	image := "daily/latest/debian-13-genericcloud-amd64.qcow2"
	var lines []string
	for i, cause := range []string{
		"image download interrupted; partial data retained for setup retry: unexpected EOF",
		"unexpected HTTP status Too Many Requests, body=``",
		"image checksum mismatch; partial data discarded: expected digest",
	} {
		lines = append(lines, fmt.Sprintf("failed to download `%s%s`: %s", debianImageMirrors[i].base, image, cause))
	}
	got := ImageDownloadSummary(errors.New(strings.Join(lines, "\n")))
	if want := ImageDownloadMarker + " official:eof:kept nju:http-429:none ustc:checksum:none"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	for _, err := range []error{nil, errors.New("failed to start: exit status 1"), errors.New("failed to download the nerdctl archive")} {
		if got := ImageDownloadSummary(err); got != "" {
			t.Fatalf("%v summarized as %q", err, got)
		}
	}
}

// #713: a host proxy that refuses the connection is not a mirror failure.
func TestImageDownloadSummaryNamesHostProxyFailure(t *testing.T) {
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := closed.Addr().String()
	closed.Close()
	var lines []string
	for i, scheme := range []string{"http", "socks5"} {
		proxy, _ := url.Parse(scheme + "://" + address)
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
		location := debianImageMirrors[i].base + "debian.qcow2"
		_, err := client.Get(location)
		if err == nil {
			t.Fatalf("%s proxy at a closed port answered", scheme)
		}
		lines = append(lines, fmt.Sprintf("failed to download `%s`: %v", location, err))
	}
	got := ImageDownloadSummary(errors.New(strings.Join(lines, "\n")))
	if want := ImageDownloadMarker + " official:proxy:none nju:proxy:none"; got != want {
		t.Fatalf("summary = %q, want %q\nfrom %s", got, want, strings.Join(lines, "\n"))
	}
}

// #841: hostproxy stops a download before any dial when the host's proxy
// settings or PAC cannot be resolved. These are its messages, as net/http
// returns them from the Proxy function.
func TestImageDownloadSummaryNamesHostProxySettingsFailure(t *testing.T) {
	for _, message := range []string{
		"无法解析主机系统代理或 PAC，请检查 Clash 等代理软件后重试；未切换直连",
		"此构建无法读取 macOS 系统代理，请使用正式原生构建或设置 HTTPS_PROXY；未切换直连",
		"无法读取主机桌面代理设置，请检查代理软件或设置 HTTPS_PROXY；未切换直连",
		"此 Linux 构建暂不解析桌面 PAC，请启用主机 TUN 或设置 HTTPS_PROXY 后继续下载",
		"无法读取主机系统代理，未切换直连：The operation timed out",
		"无法解析主机自动代理：timeout",
		"主机自动代理返回空地址，未切换直连",
		"主机 PAC/自动代理解析失败，未切换直连，请检查代理软件：x",
		"主机代理地址无效，请检查系统代理设置",
		"主机代理协议不受支持，请使用 HTTP 或 SOCKS5 系统代理",
		"主机代理设置无效，请检查系统代理设置",
	} {
		client := &http.Client{Transport: &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return nil, errors.New(message) }}}
		location := debianImageMirrors[0].base + "debian.qcow2"
		_, err := client.Get(location)
		if err == nil {
			t.Fatal("request without a proxy decision succeeded")
		}
		got := ImageDownloadSummary(fmt.Errorf("failed to download `%s`: %w", location, err))
		if want := ImageDownloadMarker + " official:proxy-config:none"; got != want {
			t.Errorf("%q: summary = %q, want %q", message, got, want)
		}
	}
}
