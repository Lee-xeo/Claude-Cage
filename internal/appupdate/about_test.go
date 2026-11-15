package appupdate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/version"
)

func aboutClient(t *testing.T, handle func(*http.Request) (int, any)) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.UserAgent(), "Cage") {
			t.Errorf("product identity in user agent: %q", r.UserAgent())
		}
		status, value := handle(r)
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(b))), ContentLength: int64(len(b)), Request: r}, nil
	})}
}

func TestAboutReleaseIncludesPrereleasesWithoutRequiringPlatformAssets(t *testing.T) {
	for _, current := range []struct{ version, status string }{{"0.1.0", "available"}, {"0.3.0-rc.10", "current"}, {"0.3.0", "ahead"}, {"development", "unknown"}} {
		t.Run(current.version, func(t *testing.T) {
			client := aboutClient(t, func(r *http.Request) (int, any) {
				return 200, []release{{Tag: "v0.2.0"}, {Tag: "v0.3.0-rc.9", Prerelease: true}, {Tag: "v0.3.0-rc.10", Prerelease: true}, {Tag: "v0.4.0", Draft: true}, {Tag: "not-a-version"}}
			})
			got := checkPublishedRelease(context.Background(), client, current.version)
			if got.Status != current.status || got.Version != "0.3.0-rc.10" || !got.Prerelease {
				t.Fatalf("%+v", got)
			}
		})
	}
	if got := checkPublishedRelease(context.Background(), responseClient(`[{"tag_name":"v0.1.0","draft":true}]`, 200), "0.1.0"); got.Status != "missing" {
		t.Fatalf("%+v", got)
	}
}

func TestAboutSourceComparisonDirection(t *testing.T) {
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, test := range []struct{ relation, status string }{{"ahead", "available"}, {"behind", "ahead"}, {"diverged", "diverged"}, {"identical", "current"}} {
		t.Run(test.relation, func(t *testing.T) {
			client := aboutClient(t, func(r *http.Request) (int, any) {
				switch r.URL.Path {
				case "/repos/" + repository + "/commits/main":
					return 200, map[string]any{"sha": head}
				case "/repos/" + repository + "/compare/" + base + "..." + head:
					return 200, map[string]any{"status": test.relation, "ahead_by": 3, "behind_by": 2}
				default:
					t.Errorf("unexpected request: %s", r.URL)
					return 404, nil
				}
			})
			got := checkSource(context.Background(), client, base)
			if got.Status != test.status || got.Commit != head || got.AheadBy != 3 || got.BehindBy != 2 {
				t.Fatalf("%+v", got)
			}
		})
	}
	for _, test := range []struct{ commit, status string }{{head, "current"}, {"", "unknown"}, {"not-a-commit", "unknown"}} {
		calls := 0
		client := aboutClient(t, func(r *http.Request) (int, any) { calls++; return 200, map[string]any{"sha": head} })
		got := checkSource(context.Background(), client, test.commit)
		if got.Status != test.status || calls != 1 {
			t.Fatalf("%+v; calls=%d", got, calls)
		}
	}
}

func TestAboutChecksPreservePartialSuccess(t *testing.T) {
	client := aboutClient(t, func(r *http.Request) (int, any) {
		if strings.HasSuffix(r.URL.Path, "/releases") {
			return 200, []release{{Tag: "v0.2.0-beta.1", Prerelease: true}}
		}
		return 503, nil
	})
	got := checkAbout(context.Background(), client, version.Build{Version: "0.1.0"})
	if got.Source.Status != "failed" || got.Source.Error == "" || got.Release.Status != "available" || got.CheckedAt == "" {
		t.Fatalf("%+v", got)
	}
	client = aboutClient(t, func(r *http.Request) (int, any) {
		if strings.HasSuffix(r.URL.Path, "/commits/main") {
			return 200, map[string]any{"sha": strings.Repeat("a", 40)}
		}
		return 429, nil
	})
	got = checkAbout(context.Background(), client, version.Build{Version: "0.1.0"})
	if got.Source.Status != "unknown" || got.Release.Status != "failed" || got.Release.RetryAt == "" {
		t.Fatalf("%+v", got)
	}
}

func TestAboutMetadataRejectsUntrustedPaths(t *testing.T) {
	for _, path := range []string{"commits/other", "compare/../../main", "compare/" + strings.Repeat("a", 40) + "...main", "issues", "../Other/releases"} {
		u, _ := url.Parse(repositoryAPI + "/" + path)
		if allowedURL(u, false) {
			t.Fatalf("accepted %s", u)
		}
	}
	if got := checkSource(context.Background(), responseClient(`{"sha":"invalid"}`, 200), ""); got.Status != "failed" {
		t.Fatalf("%+v", got)
	}
}
