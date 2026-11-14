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
	client := updateClient(configPath)
