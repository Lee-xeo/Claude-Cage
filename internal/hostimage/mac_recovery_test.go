package hostimage

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
)

type recoveryRangeTransport func(*http.Request) (*http.Response, error)

func (f recoveryRangeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type recoveryTestBody struct {
	*bytes.Reader
	closed *bool
}

func (b recoveryTestBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
func (b recoveryTestBody) Close() error { *b.closed = true; return nil }

func recoveryTestRange(t *testing.T, r *http.Request, size int64) (int64, int64) {
	t.Helper()
	var start, end int64
	if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || start < 0 || end < start || end >= size {
		t.Fatalf("invalid request range %q", r.Header.Get("Range"))
	}
	if r.URL.Scheme != "http" || r.URL.Host != "oscdn.apple.com" || r.Header.Get("Cookie") != "AssetToken=fixture" || r.Header.Get("Accept-Encoding") != "identity" {
		t.Fatal("recovery transport or authentication changed")
	}
	if r.URL.Query().Get("range_start") != strconv.FormatInt(start, 10) || r.URL.Query().Get("range_end") != strconv.FormatInt(end, 10) {
		t.Fatal("cache-bypass range differs from requested bytes")
	}
	return start, end
}

func recoveryTestFile(t *testing.T, prefix []byte) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "partial-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if _, err := f.Write(prefix); err != nil {
		t.Fatal(err)
	}
	return f
}

func recoveryTestResponse(start, end, total int64, data []byte, closed *bool) *http.Response {
	return &http.Response{StatusCode: http.StatusPartialContent, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, total)}}, ContentLength: end - start + 1, Body: recoveryTestBody{bytes.NewReader(data), closed}}
}

func TestRecoveryRangesChangeStalledInterval(t *testing.T) {
	data := bytes.Repeat([]byte("signed-image-fixture"), 180000)
	size := int64(len(data))
	prefix := int64(1 << 20)
	file := recoveryTestFile(t, data[:prefix])
	var requests [][2]int64
	var bodies []*bool
	client := &http.Client{Transport: recoveryRangeTransport(func(r *http.Request) (*http.Response, error) {
		start, end := recoveryTestRange(t, r, size)
		requests = append(requests, [2]int64{start, end})
		closed := new(bool)
		bodies = append(bodies, closed)
		payload := data[start : end+1]
		// Reproduce the CDN returning correct headers but no bytes for this interval.
		if start == prefix && end-start+1 >= 512<<10 {
			payload = nil
		}
		return recoveryTestResponse(start, end, size, payload, closed), nil
	})}
	if err := downloadRecoveryRanges(t.Context(), client, "http://oscdn.apple.com/recovery.dmg", "fixture", file, size, prefix); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file.Name())
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("resume did not preserve the complete image", err)
	}
	if len(requests) < 3 || requests[0][0] != prefix || requests[1][0] != prefix || requests[0][1] == requests[1][1] {
		t.Fatal("stalled interval was not changed", requests)
	}
	for _, closed := range bodies {
		if !*closed {
			t.Fatal("response body leaked")
		}
	}
}

func TestRecoveryRangesKeepPartialProgress(t *testing.T) {
	data := bytes.Repeat([]byte("recovery"), 12000)
	size := int64(len(data))
	prefix := int64(257)
	file := recoveryTestFile(t, data[:prefix])
	expected := prefix
	requests := 0
	client := &http.Client{Transport: recoveryRangeTransport(func(r *http.Request) (*http.Response, error) {
		start, end := recoveryTestRange(t, r, size)
		if start != expected {
			t.Fatalf("resume offset=%d, want %d", start, expected)
		}
		requests++
		receivedEnd := min(end+1, start+4096)
		expected = receivedEnd
		return recoveryTestResponse(start, end, size, data[start:receivedEnd], new(bool)), nil
	})}
	if err := downloadRecoveryRanges(t.Context(), client, "http://oscdn.apple.com/recovery.dmg", "fixture", file, size, prefix); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file.Name())
	if err != nil || !bytes.Equal(got, data) || requests <= 5 {
		t.Fatal("received bytes or progress retry budget lost", err, requests)
	}
}

func TestRecoveryRangesBoundNoProgress(t *testing.T) {
	file := recoveryTestFile(t, []byte("saved"))
	requests := 0
	client := &http.Client{Transport: recoveryRangeTransport(func(r *http.Request) (*http.Response, error) {
		start, end := recoveryTestRange(t, r, 2<<20)
		requests++
		return recoveryTestResponse(start, end, 2<<20, nil, new(bool)), nil
	})}
	err := downloadRecoveryRanges(t.Context(), client, "http://oscdn.apple.com/recovery.dmg", "fixture", file, 2<<20, 5)
	got, _ := os.ReadFile(file.Name())
	if err == nil || !strings.Contains(err.Error(), "已保存 5/") || requests != 5 || string(got) != "saved" {
		t.Fatalf("unbounded or destructive retry: requests=%d, error=%v", requests, err)
	}
}

func TestRecoveryRangesRejectMismatchedResponse(t *testing.T) {
	for _, mode := range []string{"offset", "total", "full-response"} {
		t.Run(mode, func(t *testing.T) {
			file := recoveryTestFile(t, []byte("saved"))
			requests := 0
			client := &http.Client{Transport: recoveryRangeTransport(func(r *http.Request) (*http.Response, error) {
				start, end := recoveryTestRange(t, r, 256)
				requests++
				res := recoveryTestResponse(start, end, 256, bytes.Repeat([]byte("x"), 251), new(bool))
				switch mode {
				case "offset":
					res.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/256", end))
				case "total":
					res.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/512", start, end))
				case "full-response":
					res.StatusCode = http.StatusOK
				}
				return res, nil
			})}
			if err := downloadRecoveryRanges(t.Context(), client, "http://oscdn.apple.com/recovery.dmg", "fixture", file, 256, 5); err == nil {
				t.Fatal("accepted an unverified response range")
			}
			got, _ := os.ReadFile(file.Name())
			if string(got) != "saved" || requests != 1 {
				t.Fatal("invalid response modified the partial image")
			}
		})
	}
}
