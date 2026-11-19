package workbench

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserFailureExplainsOperationAndKnownCause(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body, want string
	}{
		{400, `{"error":"Tab no longer exists"}`, "标签页已关闭"},
		{401, `{"error":"private token=do-not-display"}`, "授权"},
		{503, `{"error":"private token=do-not-display"}`, "尚未就绪"},
		{500, `{"error":"private token=do-not-display"}`, "HTTP 500"},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			guest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer guest.Close()
			client := localBrowserClient(t, context.Background(), guest)
			defer client.close()
			_, err := client.request(context.Background(), "test-token", "POST", "/navigate", []byte(`{}`))
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "do-not-display") {
				t.Fatalf("wrong user failure: %v", err)
			}
		})
	}
}

func TestFileServiceRetainsKnownRejectionAndHidesUnknownBody(t *testing.T) {
	known := fileServiceFailure(400, []byte("上传窗口已关闭，请重新选择文件"))
	if !strings.Contains(known.Error(), "上传窗口已关闭") || !strings.Contains(known.Error(), "重新点击") {
		t.Fatalf("file window reason lost: %v", known)
	}
	unknown := fileServiceFailure(500, []byte("unknown response token=private-file-secret"))
	if !strings.Contains(unknown.Error(), "HTTP 500") || strings.Contains(unknown.Error(), "private-file-secret") {
		t.Fatalf("unsafe unknown response: %v", unknown)
	}
}
