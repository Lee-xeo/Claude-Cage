package guestos

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/sylvinus/agent-vm/internal/hostimage"
)

func TestHostSourcePublishesOneVerifiedMediaReceipt(t *testing.T) {
	profile := t.TempDir()
	source := hostimage.Plan{Host: hostimage.Identity{System: Windows11, Version: "25H2", Build: "26200.9", Edition: "Professional", Language: "zh-CN", Arch: "amd64"}, Version: "25H2", Build: "26200.8", Format: "esd", URL: "http://dl.delivery.mp.microsoft.com/example.esd", Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 10}
	install := Installation{Version: 1, ID: uuid.New(), System: Windows11, EditorTerms: true, Source: source}
	if err := SaveInstallation(profile, install); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(filepath.Join(profile, installRecord))
	ready := source
	ready.Path = filepath.Join(profile, "system.esd")
	ready.Modified = 123
	if err := RecordPreparedMedia(profile, install.ID, ready); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadInstallation(profile)
	if err != nil || loaded.Media != ready.Path || loaded.Source != ready {
		t.Fatalf("loaded: %+v %v", loaded, err)
	}
	if err := SaveInstallation(profile, *loaded); err != nil {
		t.Fatal(err)
	}
	if err := RecordPreparedMedia(profile, install.ID, ready); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(profile, installRecord))
	if string(original) != string(after) {
		t.Fatal("changed original installation choice")
	}
	changed := ready
	changed.Host.Edition = "Enterprise"
	if err := RecordPreparedMedia(profile, install.ID, changed); err == nil {
		t.Fatal("accepted another edition")
	}
	if err := RecordPreparedMedia(profile, uuid.New(), ready); err == nil {
		t.Fatal("accepted another installation")
	}
	changed = ready
	changed.Path = filepath.Join(profile, "other.esd")
	if err := RecordPreparedMedia(profile, install.ID, changed); err == nil {
		t.Fatal("replaced published media")
	}
}

func TestLegacyInstallationOmitsSource(t *testing.T) {
	profile := t.TempDir()
	if err := SaveInstallation(profile, Installation{Version: 1, ID: uuid.New(), System: MacOS}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(profile, installRecord))
	if strings.Contains(string(data), `"source"`) {
		t.Fatal("legacy choice acquired source metadata")
	}
}

func TestLinuxCloneKeepsItsChoiceAfterBaseInstallation(t *testing.T) {
	for _, name := range []string{"base-building", "base"} {
		t.Run(name, func(t *testing.T) {
			profile := t.TempDir()
			install := Installation{
				Version: 1, ID: uuid.New(), System: "debian-12",
				Source: hostimage.Plan{
					Host:    hostimage.Identity{System: "debian-12", Version: "12", Arch: "amd64"},
					Version: "12", Format: "qcow2", Path: filepath.Join(profile, "debian.img"),
					Digest: "sha256:" + strings.Repeat("a", 64),
				},
			}
			if err := SaveInstallation(profile, install); err != nil {
				t.Fatal(err)
			}
			disk := filepath.Join(profile, "state", "lima", name)
			if err := os.MkdirAll(disk, 0700); err != nil {
				t.Fatal(err)
			}
			unstarted, err := UnstartedInstallation(profile, install)
			if err != nil || unstarted {
				t.Fatalf("existing base was treated as an unstarted choice: %v %v", unstarted, err)
			}
			if err := ClearUnstartedInstallation(profile, install.ID); err == nil {
				t.Fatal("cleared a choice that already produced a base disk")
			}
			if err := FinishLinuxInstallation(profile, install.ID); err == nil {
				t.Fatal("finished without a durable Linux choice")
			}
			if pending, err := LoadInstallation(profile); err != nil || pending == nil {
				t.Fatal("lost installation recovery information", err)
			}
			if err := hostimage.WriteLinuxTemplate(filepath.Join(profile, "state"), install.Source); err != nil {
				t.Fatal(err)
			}
			if err := FinishLinuxInstallation(profile, install.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(disk); err != nil {
				t.Fatal("finishing the installation affected its disk", err)
			}
		})
	}
}

// Ubuntu installations recorded by earlier versions stay readable. One that
// created nothing can be withdrawn; none can be finished.
func TestUbuntuInstallationRecordsStayReadable(t *testing.T) {
	plan := func(profile, system string) hostimage.Plan {
		version := strings.TrimPrefix(system, "ubuntu-")
		return hostimage.Plan{
			Host:    hostimage.Identity{System: system, Version: version, Arch: "amd64"},
			Version: version, Format: "qcow2", Path: filepath.Join(profile, "ubuntu.img"),
			Digest: "sha256:" + strings.Repeat("a", 64),
		}
	}
	for name, install := range map[string]func(string) Installation{
		"catalog": func(profile string) Installation {
			return Installation{Version: 1, ID: uuid.New(), System: Ubuntu2204, Source: plan(profile, Ubuntu2204), SourceKind: LinuxImageSource}
		},
		"host 22.04": func(profile string) Installation {
			return Installation{Version: 1, ID: uuid.New(), System: Ubuntu2204, Source: plan(profile, Ubuntu2204)}
		},
		"host 24.04": func(profile string) Installation {
			return Installation{Version: 1, ID: uuid.New(), System: "ubuntu-24.04", Source: plan(profile, "ubuntu-24.04")}
		},
	} {
		t.Run(name, func(t *testing.T) {
			profile := t.TempDir()
			record := install(profile)
			if err := SaveInstallation(profile, record); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadInstallation(profile)
			if err != nil || loaded == nil || *loaded != record {
				t.Fatalf("record became unreadable: %+v %v", loaded, err)
			}
			if err := FinishLinuxInstallation(profile, record.ID); err == nil {
				t.Fatal("finished an Ubuntu installation")
			}
			disk := filepath.Join(profile, "state", "lima", "base")
			if err := os.MkdirAll(disk, 0700); err != nil {
				t.Fatal(err)
			}
			if err := ClearUnstartedInstallation(profile, record.ID); err == nil {
				t.Fatal("withdrew a record that already has a disk")
			}
			if _, err := os.Stat(disk); err != nil {
				t.Fatal("refusal affected the disk", err)
			}
			if err := os.Remove(disk); err != nil {
				t.Fatal(err)
			}
			if err := ClearUnstartedInstallation(profile, record.ID); err != nil {
				t.Fatal(err)
			}
			if loaded, err := LoadInstallation(profile); err != nil || loaded != nil {
				t.Fatal("unstarted record was not withdrawn", err)
			}
		})
	}
	missing := Installation{Version: 1, ID: uuid.New(), System: Ubuntu2204}
	if err := missing.Validate(); err == nil {
		t.Fatal("accepted an Ubuntu record without its source")
	}
}
