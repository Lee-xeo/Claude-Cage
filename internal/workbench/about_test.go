package workbench

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/appupdate"
	"github.com/sylvinus/agent-vm/internal/version"
)

	if check.Source.Status != "failed" || check.Release.Status != "failed" {
