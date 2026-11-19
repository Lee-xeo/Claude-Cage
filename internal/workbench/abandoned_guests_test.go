package workbench

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/sylvinus/agent-vm/internal/guestos"
	"github.com/sylvinus/agent-vm/internal/hostimage"
)

func TestAbandonedGuestsReportLeftoverDataWithoutTouchingIt(t *testing.T) {
	s := testServer(t)
	status := func() []string {
		t.Helper()
		response := desktopRequestForTest(s, http.MethodGet, "/api/status", nil)
		var view struct {
			Abandoned []string `json:"abandoned_guests"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &view) != nil || view.Abandoned == nil {
			t.Fatalf("status lacks the abandoned guest list: %d %s", response.Code, response.Body.String())
		}
		return view.Abandoned
	}
	if found := status(); len(found) != 0 {
		t.Fatalf("clean profile reported abandoned guests: %v", found)
	}
	const content = `{"version":1}`
	for _, leftover := range []struct {
		system, path string
		directory    bool
	}{
		{"windows", "windows-guest.json", false},
		{"windows", filepath.Join("state", "windows-0123456789abcdef0123456789abcdef"), true},
		{"macos", "macos-guest.json", false},
		{"macos", filepath.Join("state", "lima", "mac"), true},
		{"macos", filepath.Join("state", "guest-media", "macos"), true},
		{"ubuntu", filepath.Join("state", "guest-media", "ubuntu-22.04"), true},
		{"ubuntu", filepath.Join("state", "guest-media", "ubuntu-24.04"), true},
	} {
		path := filepath.Join(s.profile, leftover.path)
		var err error
		if leftover.directory {
			err = os.MkdirAll(path, 0700)
		} else {
			err = os.WriteFile(path, []byte(content), 0600)
		}
		if err != nil {
			t.Fatal(err)
		}
		if found := abandonedGuests(s.profile); !slices.Equal(found, []string{leftover.system}) {
			t.Fatalf("%s not reported as %s: %v", leftover.path, leftover.system, found)
		}
		info, err := os.Lstat(path)
		if err != nil || info.IsDir() != leftover.directory {
			t.Fatal("reporting changed the leftover entry", leftover.path, err)
		}
		if !leftover.directory {
			if data, err := os.ReadFile(path); err != nil || string(data) != content {
				t.Fatal("reporting changed the leftover file", leftover.path, err)
			}
		}
		// Each case stands alone; the test owns this temporary profile.
		if err = os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"windows-guest.json", "macos-guest.json"} {
		if err := os.WriteFile(filepath.Join(s.profile, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(s.profile, "state", "guest-media", "ubuntu-22.04"), 0700); err != nil {
		t.Fatal(err)
	}
	if found := status(); !slices.Equal(found, []string{"macos", "ubuntu", "windows"}) {
		t.Fatalf("status did not list every abandoned guest: %v", found)
	}
}

func TestAbandonedInstallationRecordIsReportedAndRefused(t *testing.T) {
	s := testServer(t)
	install := guestos.Installation{Version: 1, System: guestos.MacOS, ID: uuid.New()}
	if err := guestos.SaveInstallation(s.profile, install); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(s.profile, "guest-install.json")
	before, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if found := s.abandonedGuestList(); !slices.Equal(found, []string{"macos"}) {
		t.Fatalf("pending macOS installation not reported: %v", found)
	}
	for _, body := range []string{`{}`, `{"guest_system":"macos"}`, `{"guest_system":"windows-11"}`} {
		response := desktopRequestForTest(s, http.MethodPost, "/api/start", []byte(body))
		if response.Code != http.StatusConflict {
			t.Fatalf("abandoned installation was started by %s: %d %s", body, response.Code, response.Body.String())
		}
	}
	if s.jobs.snapshot().Running {
		t.Fatal("refused start still launched a job")
	}
	view, err := s.guestInstallView()
	if err != nil || view.Available || view.Reason == "" || view.Pending == nil {
		t.Fatalf("install view does not explain the abandoned record: %+v %v", view, err)
	}
	if after, err := os.ReadFile(record); err != nil || string(after) != string(before) {
		t.Fatal("refusal changed the installation record", err)
	}
}

func ubuntuPlanForTest(profile, system string) hostimage.Plan {
	version := strings.TrimPrefix(system, "ubuntu-")
	return hostimage.Plan{
		Host:    hostimage.Identity{System: system, Version: version, Arch: "amd64"},
		Version: version, Format: "qcow2", Path: filepath.Join(profile, "state", "guest-media", system, "image.qcow2"),
		Digest: "sha256:" + strings.Repeat("a", 64),
	}
}

// profileFilesForTest snapshots every regular file below the profile.
func profileFilesForTest(t *testing.T, profile string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(profile, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		files[path] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// An environment that an earlier version created from Ubuntu is reported, is
// never started or reinstalled as Debian, and keeps every file.
func TestExistingUbuntuEnvironmentIsReportedRefusedAndUntouched(t *testing.T) {
	for _, system := range []string{"ubuntu-22.04", "ubuntu-24.04"} {
		t.Run(system, func(t *testing.T) {
			s := testServer(t)
			media := filepath.Join(s.profile, "state", "guest-media")
			plan := ubuntuPlanForTest(s.profile, system)
			if err := hostimage.Save(media, plan); err != nil {
				t.Fatal(err)
			}
			// The durable image choice and the disk of the finished environment.
			if err := os.WriteFile(filepath.Join(media, "host-system.yaml"), []byte(`{"images":[]}`), 0600); err != nil {
				t.Fatal(err)
			}
			disk := filepath.Join(s.profile, "state", "lima", "base")
			if err := os.MkdirAll(disk, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(disk, "diffdisk"), []byte("user data"), 0600); err != nil {
				t.Fatal(err)
			}
			before := profileFilesForTest(t, s.profile)

			if found := s.abandonedGuestList(); !slices.Equal(found, []string{"ubuntu"}) {
				t.Fatalf("existing %s environment not reported: %v", system, found)
			}
			response := desktopRequestForTest(s, http.MethodGet, "/api/status", nil)
			var view struct {
				Abandoned []string `json:"abandoned_guests"`
				System    string   `json:"guest_system"`
				Error     string   `json:"guest_system_error"`
			}
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &view) != nil ||
				!slices.Equal(view.Abandoned, []string{"ubuntu"}) || view.System != "" || !strings.Contains(view.Error, "Ubuntu") {
				t.Fatalf("status does not explain the Ubuntu environment: %d %s", response.Code, response.Body.String())
			}
			for _, body := range []string{`{}`, `{"guest_system":"debian-13"}`, `{"guest_system":"` + system + `"}`} {
				response := desktopRequestForTest(s, http.MethodPost, "/api/start", []byte(body))
				if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "Ubuntu") {
					t.Fatalf("%s environment was started by %s: %d %s", system, body, response.Code, response.Body.String())
				}
			}
			if s.jobs.snapshot().Running || s.jobs.snapshot().ID != "" {
				t.Fatal("refused start still launched a job")
			}
			if err := s.startEnvironment(false); err == nil || !strings.Contains(err.Error(), "Ubuntu") {
				t.Fatalf("indirect start of the Ubuntu environment: %v", err)
			}
			after := profileFilesForTest(t, s.profile)
			for path, content := range before {
				if got, ok := after[path]; !ok || got != content {
					t.Fatal("refusing the Ubuntu environment changed", path)
				}
			}
		})
	}
}

// A pending Ubuntu installation is explained and refused. It can only be
// withdrawn while nothing was created for it, exactly like the other
// abandoned guests.
func TestPendingUbuntuInstallationIsRefusedAndOnlyWithdrawnUnstarted(t *testing.T) {
	for name, kind := range map[string]string{"catalog": guestos.LinuxImageSource, "host": ""} {
		t.Run(name, func(t *testing.T) {
			s := testServer(t)
			install := guestos.Installation{Version: 1, ID: uuid.New(), System: guestos.Ubuntu2204,
				Source: ubuntuPlanForTest(s.profile, guestos.Ubuntu2204), SourceKind: kind}
			if err := guestos.SaveInstallation(s.profile, install); err != nil {
				t.Fatal(err)
			}
			record := filepath.Join(s.profile, "guest-install.json")
			before, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			if found := s.abandonedGuestList(); !slices.Equal(found, []string{"ubuntu"}) {
				t.Fatalf("pending Ubuntu installation not reported: %v", found)
			}
			for _, body := range []string{`{}`, `{"guest_system":"ubuntu-22.04"}`, `{"guest_system":"debian-13"}`} {
				response := desktopRequestForTest(s, http.MethodPost, "/api/start", []byte(body))
				if response.Code != http.StatusConflict {
					t.Fatalf("pending Ubuntu installation was started by %s: %d %s", body, response.Code, response.Body.String())
				}
			}
			if s.jobs.snapshot().Running {
				t.Fatal("refused start still launched a job")
			}
			if err := s.startEnvironment(false); err == nil {
				t.Fatal("pending Ubuntu installation started indirectly")
			}
			view, err := s.guestInstallView()
			if err != nil || view.Available || !view.Reselect || !strings.Contains(view.Reason, "Ubuntu") || view.Pending == nil {
				t.Fatalf("install view does not explain the Ubuntu record: %+v %v", view, err)
			}
			// Once a disk exists the record stays, with the disk.
			disk := filepath.Join(s.profile, "state", "lima", "base-building")
			if err := os.MkdirAll(disk, 0700); err != nil {
				t.Fatal(err)
			}
			withdraw := []byte(`{"id":"` + install.ID.String() + `"}`)
			if response := desktopRequestForTest(s, http.MethodDelete, "/api/environment/guest-systems", withdraw); response.Code != http.StatusConflict {
				t.Fatalf("withdrew a started Ubuntu installation: %d %s", response.Code, response.Body.String())
			}
			if after, err := os.ReadFile(record); err != nil || string(after) != string(before) {
				t.Fatal("refusal changed the installation record", err)
			}
			if _, err := os.Stat(disk); err != nil {
				t.Fatal("refusal affected the disk", err)
			}
			if err := os.Remove(disk); err != nil {
				t.Fatal(err)
			}
			if response := desktopRequestForTest(s, http.MethodDelete, "/api/environment/guest-systems", withdraw); response.Code != http.StatusOK {
				t.Fatalf("unstarted Ubuntu installation could not be withdrawn: %d %s", response.Code, response.Body.String())
			}
			if pending, err := guestos.LoadInstallation(s.profile); err != nil || pending != nil {
				t.Fatal("withdrawn record is still pending", err)
			}
		})
	}
}

// Nothing offers or accepts a new system choice: a profile without an
// environment starts Debian 13, and every other system is refused.
func TestNewEnvironmentAcceptsOnlyDebian13(t *testing.T) {
	s := testServer(t)
	view, err := s.guestInstallView()
	if err != nil || view.Pending != nil || view.Available || view.Reason != "" {
		t.Fatalf("clean profile has an installation view: %+v %v", view, err)
	}
	for _, body := range []string{`{"guest_system":"ubuntu-22.04"}`, `{"guest_system":"ubuntu-24.04"}`, `{"guest_system":"debian-12"}`} {
		response := desktopRequestForTest(s, http.MethodPost, "/api/start", []byte(body))
		if response.Code != http.StatusConflict || s.jobs.snapshot().ID != "" {
			t.Fatalf("%s was accepted: %d %s", body, response.Code, response.Body.String())
		}
	}
	if response := desktopRequestForTest(s, http.MethodGet, "/api/environment/guest-systems/clone", nil); response.Code == http.StatusOK {
		t.Fatalf("host matching is still served: %d", response.Code)
	}
	if _, err := os.Stat(filepath.Join(s.profile, "guest-install.json")); !os.IsNotExist(err) {
		t.Fatal("a refused choice left an installation record", err)
	}
}
