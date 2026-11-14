package appupdate

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/version"
)

func TestHostUpdateUsesProxyWithoutCageProfile(t *testing.T) {
	var connects atomic.Int32
	commit := strings.Repeat("a", 40)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.UserAgent(), "Cage") {
			t.Error("product identity in request")
		}
		if strings.HasSuffix(r.URL.Path, "/commits/main") {
			io.WriteString(w, `{"sha":"`+commit+`"}`)
		} else {
			io.WriteString(w, `[{"tag_name":"v0.2.0-rc.1","prerelease":true}]`)
		}
	}))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != "api.github.com:443" {
			t.Errorf("unexpected proxy request: %s %s", r.Method, r.Host)
			http.Error(w, "bad target", 400)
			return
		}
		connects.Add(1)
		upstream, err := net.Dial("tcp", strings.TrimPrefix(origin.URL, "https://"))
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.Close()
		downstream, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer downstream.Close()
		buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		buffer.Flush()
		go io.Copy(upstream, buffer)
		io.Copy(downstream, upstream)
	}))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	client := updateClient(filepath.Join(t.TempDir(), "not-configured.json"))
	defer client.CloseIdleConnections()
	tr := client.Transport.(*updateTransport).host.(*http.Transport)
	tr.TLSClientConfig = origin.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	tr.TLSClientConfig.ServerName = "example.com"
	got := checkAbout(context.Background(), client, version.Build{Version: "0.1.0", Commit: commit})
	if connects.Load() == 0 || got.Source.Status != "current" || got.Release.Status != "available" {
		t.Fatalf("CONNECTs=%d result=%+v", connects.Load(), got)
	}
}

func TestUpdateTransportFallsBackOnlyOnConnectionFailure(t *testing.T) {
	for _, status := range []int{200, 403, 404, 429, 503} {
		fallbacks := 0
		tr := &updateTransport{host: responseClient("[]", status).Transport, hostTimeout: time.Second, openManaged: func(context.Context) (*http.Client, error) { fallbacks++; return responseClient("[]", 200), nil }}
		client := updateHTTPClient(tr)
		resp, err := client.Get(releasesURL)
		if err != nil || resp.StatusCode != status || fallbacks != 0 {
			t.Fatalf("status=%d fallback=%d error=%v", status, fallbacks, err)
		}
		resp.Body.Close()
		client.CloseIdleConnections()
	}
	var fallbacks atomic.Int32
	tr := &updateTransport{
		host: roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("host network unavailable") }), hostTimeout: time.Second,
		openManaged: func(ctx context.Context) (*http.Client, error) {
			fallbacks.Add(1)
			return aboutClient(t, func(r *http.Request) (int, any) {
				if strings.HasSuffix(r.URL.Path, "/commits/main") {
					return 200, map[string]string{"sha": strings.Repeat("b", 40)}
				}
				return 200, []release{{Tag: "v0.2.0"}}
			}), nil
		},
	}
	client := updateHTTPClient(tr)
	defer client.CloseIdleConnections()
	got := checkAbout(context.Background(), client, version.Build{Version: "0.1.0"})
	if fallbacks.Load() != 1 || got.Source.Status != "unknown" || got.Release.Status != "available" {
		t.Fatalf("fallbacks=%d result=%+v", fallbacks.Load(), got)
	}
}

func TestUpdateHostTimeoutAndCancellation(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		fallbacks := 0
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		tr := &updateTransport{hostTimeout: 10 * time.Millisecond, host: roundTrip(func(r *http.Request) (*http.Response, error) {
			if cancelCaller {
				cancel()
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		}), openManaged: func(ctx context.Context) (*http.Client, error) {
			fallbacks++
			if ctx.Err() != nil {
				t.Error("fallback inherited host attempt timeout")
			}
			return responseClient("[]", 200), nil
		}}
		req, _ := http.NewRequestWithContext(ctx, "GET", releasesURL, nil)
		resp, err := tr.RoundTrip(req)
		if cancelCaller {
			if !errors.Is(err, context.Canceled) || fallbacks != 0 {
				t.Fatalf("cancel: %d %v", fallbacks, err)
			}
		} else {
			if err != nil || fallbacks != 1 {
				t.Fatalf("timeout: %d %v", fallbacks, err)
			}
			resp.Body.Close()
		}
	}
}

func TestUpdateHostDeadlineStopsAfterHeaders(t *testing.T) {
	var transfer context.Context
	tr := &updateTransport{hostTimeout: 20 * time.Millisecond, host: roundTrip(func(r *http.Request) (*http.Response, error) {
		transfer = r.Context()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("archive"))}, nil
	}), openManaged: func(context.Context) (*http.Client, error) {
		t.Fatal("healthy host must not use fallback")
		return nil, nil
	}}
	req, _ := http.NewRequest("GET", releasesURL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-transfer.Done():
		t.Fatal("host deadline cancelled body transfer")
	case <-time.After(60 * time.Millisecond):
	}
	if data, err := io.ReadAll(resp.Body); err != nil || string(data) != "archive" {
		t.Fatalf("%q %v", data, err)
	}
	resp.Body.Close()
	if transfer.Err() != context.Canceled {
		t.Fatal("closing response did not release request context")
	}
}

func TestLiveAboutWithStoppedCageProxy(t *testing.T) {
	if os.Getenv("CAGE_TEST_APP_UPDATE_LIVE") == "" {
		t.Skip("set CAGE_TEST_APP_UPDATE_LIVE=1 for host network verification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	got := CheckAbout(ctx, filepath.Join(t.TempDir(), "not-configured.json"))
	if got.Source.Status == "failed" || !validCommit(got.Source.Commit) || got.Release.Status == "failed" || got.Release.Version == "" {
		t.Fatalf("host-only check: %+v", got)
	}
	t.Logf("Host-only update check succeeded with no Cage profile: source=%s release=%s prerelease=%t", got.Source.Status, got.Release.Version, got.Release.Prerelease)
}
