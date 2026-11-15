package appupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sylvinus/agent-vm/internal/version"
	"golang.org/x/mod/semver"
)

const repositoryAPI = "https://api.github.com/repos/" + repository

// CheckItem keeps independent failures visible without discarding other results.
type CheckItem struct {
	Status     string `json:"status"`
	Version    string `json:"version,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Prerelease bool   `json:"prerelease,omitempty"`
	AheadBy    int    `json:"aheadBy,omitempty"`
	BehindBy   int    `json:"behindBy,omitempty"`
	Error      string `json:"error,omitempty"`
	RetryAt    string `json:"retryAt,omitempty"`
}

type AboutCheck struct {
	Build     version.Build `json:"build"`
	Source    CheckItem     `json:"source"`
	Release   CheckItem     `json:"release"`
	CheckedAt string        `json:"checkedAt"`
}

// CheckAbout only reads metadata. It never downloads, installs, or changes the
// native updater's stable/prerelease channel.
func CheckAbout(ctx context.Context, configPath string) AboutCheck {
	ctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	b := version.CurrentBuild()
	client := updateClient(configPath)
	defer client.CloseIdleConnections()
	return checkAbout(ctx, client, b)
}

func checkAbout(ctx context.Context, client *http.Client, b version.Build) AboutCheck {
	result := AboutCheck{Build: b}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); result.Source = checkSource(ctx, client, b.Commit) }()
	go func() { defer wg.Done(); result.Release = checkPublishedRelease(ctx, client, b.Version) }()
	wg.Wait()
	result.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	return result
}

func checkFailure(err error) CheckItem {
	item := CheckItem{Status: "failed", Error: err.Error()}
	var delayed interface{ RetryAt() time.Time }
	if errors.As(err, &delayed) {
		item.RetryAt = delayed.RetryAt().UTC().Format(time.RFC3339)
	}
	return item
}

func metadata(ctx context.Context, client *http.Client, raw string, value any) error {
	resp, err := request(ctx, client, raw)
	if err != nil {
		return err
	}
	b, err := limitedBody(resp, maxMetadata)
	if err != nil {
		return err
	}
	if json.Unmarshal(b, value) != nil {
		return errors.New("更新源返回了无效的信息")
	}
	return nil
}

func validCommit(s string) bool {
	return len(s) == 40 && strings.Trim(s, "0123456789abcdef") == ""
}

func checkSource(ctx context.Context, client *http.Client, current string) CheckItem {
	var latest struct {
		SHA string `json:"sha"`
	}
	if err := metadata(ctx, client, repositoryAPI+"/commits/main", &latest); err != nil {
		return checkFailure(err)
	}
	if !validCommit(latest.SHA) {
		return checkFailure(errors.New("源码更新源未返回有效提交"))
	}
	item := CheckItem{Status: "unknown", Commit: latest.SHA}
	if !validCommit(current) {
		return item
	}
	if current == latest.SHA {
		item.Status = "current"
		return item
	}
	var comparison struct {
		Status string `json:"status"`
		Ahead  int    `json:"ahead_by"`
		Behind int    `json:"behind_by"`
	}
	if err := metadata(ctx, client, repositoryAPI+"/compare/"+current+"..."+latest.SHA+"?per_page=1", &comparison); err != nil {
		failure := checkFailure(err)
		failure.Commit = latest.SHA
		return failure
	}
	// GitHub describes the main-branch head relative to the executable's base.
	switch comparison.Status {
	case "ahead":
		item.Status = "available"
	case "behind":
		item.Status = "ahead"
	case "identical":
		item.Status = "current"
	case "diverged":
		item.Status = "diverged"
	default:
		return checkFailure(errors.New("源码更新源未返回有效的提交关系"))
	}
	item.AheadBy, item.BehindBy = comparison.Ahead, comparison.Behind
	return item
}

func checkPublishedRelease(ctx context.Context, client *http.Client, current string) CheckItem {
	var releases []release
	if err := metadata(ctx, client, releasesURL, &releases); err != nil {
		return checkFailure(err)
	}
	item := CheckItem{Status: "missing"}
	for _, r := range releases {
		if r.Draft || !semver.IsValid(r.Tag) || semver.Canonical(r.Tag) != r.Tag {
			continue
		}
		if item.Version != "" && semver.Compare(r.Tag, "v"+item.Version) <= 0 {
			continue
		}
		item.Version, item.Prerelease = strings.TrimPrefix(r.Tag, "v"), r.Prerelease || semver.Prerelease(r.Tag) != ""
	}
	if item.Version == "" {
		return item
	}
	item.Status = "unknown"
	if tag := "v" + current; semver.IsValid(tag) && semver.Canonical(tag) == tag {
		switch semver.Compare("v"+item.Version, tag) {
		case 1:
			item.Status = "available"
		case 0:
			item.Status = "current"
		case -1:
			item.Status = "ahead"
		}
	}
	return item
}
