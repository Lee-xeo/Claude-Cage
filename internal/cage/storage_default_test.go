package cage

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writePackagedWindowsApp(t *testing.T, app string) string {
	t.Helper()
	backend := filepath.Join(app, "resources", "cage.exe")
	writeStorageFixture(t, backend, []byte("backend"))
	writeStorageFixture(t, filepath.Join(app, "Cage.exe"), []byte("launcher"))
	writeStorageFixture(t, filepath.Join(app, "Cage.install-id"), []byte("io.github.lee-xeo.claude-cage"))
	return backend
}

func TestWindowsInitialDataDirectoryAndApplicationBoundary(t *testing.T) {
	profile, root := newStorageFixture(t)
	app := filepath.Join(root, "Cage")
	backend := filepath.Join(app, "resources", "cage.exe")
	writeStorageFixture(t, backend, []byte("backend"))
	writeStorageFixture(t, filepath.Join(app, "Cage.exe"), []byte("launcher"))
	excluded := windowsTargetStorageExcludedRoots()
	if got := initialStorageDirectory(backend, "windows", excluded); got != "" {
		t.Fatalf("unmarked build default=%s", got)
	}
	writeStorageFixture(t, filepath.Join(app, "Cage.install-id"), []byte("io.github.lee-xeo.claude-cage"))
	data := filepath.Join(app, "CageData")
	if got := initialStorageDirectory(backend, "windows", excluded); got != data {
		t.Fatalf("default=%s want=%s", got, data)
	}
	for _, platform := range []string{"darwin", "linux"} {
		if got := initialStorageDirectory(backend, platform, excluded); got != "" {
			t.Fatalf("%s default changed to %s", platform, got)
		}
	}
	if err := validateStorageTarget(profile, data, app, "windows"); err != nil {
		t.Fatalf("reserved Windows data folder rejected: %v", err)
	}
	for _, target := range []string{app, filepath.Join(app, "resources", "data"), filepath.Join(app, "OtherData")} {
		if err := validateStorageTarget(profile, target, app, "windows"); err == nil {
			t.Fatalf("program boundary opened for %s", target)
		}
	}
	if err := validateStorageTarget(profile, data, app, "darwin"); err == nil {
		t.Fatal("Mac application directory allowed")
	}
	if err := os.Remove(filepath.Join(app, "Cage.install-id")); err != nil {
		t.Fatal(err)
	}
	if err := validateStorageTarget(profile, data, app, "windows"); err == nil {
		t.Fatal("unmarked installation allowed data inside app")
	}
}

func TestWindowsInitialDataDirectorySkipsAppDataTempAndJunctions(t *testing.T) {
	profile, root := newStorageFixture(t)
	localAppData := filepath.Join(root, "AppData", "Local")
	roamingAppData := filepath.Join(root, "AppData", "Roaming")
	tempDir := filepath.Join(root, "Temp")
	excluded := []string{localAppData, roamingAppData, tempDir, filepath.Join(root, "AppData")}

	// 1. Direct install inside LocalAppData, Roaming AppData, and Temp must not suggest CageData.
	for _, parent := range []string{
		filepath.Join(localAppData, "Programs", "Cage"),
		filepath.Join(roamingAppData, "Cage"),
		filepath.Join(tempDir, "Cage"),
	} {
		backend := writePackagedWindowsApp(t, parent)
		if got := initialStorageDirectory(backend, "windows", excluded); got != "" {
			t.Fatalf("app in excluded root %s suggested %q, want empty", parent, got)
		}
	}

	// 2. Sibling prefix ("AppDataX") or outside path ("Outside/Cage") still recommends CageData.
	for _, parent := range []string{
		filepath.Join(root, "AppDataX", "Cage"),
		filepath.Join(root, "Outside", "Cage"),
	} {
		backend := writePackagedWindowsApp(t, parent)
		want := filepath.Join(parent, "CageData")
		if got := initialStorageDirectory(backend, "windows", excluded); got != want {
			t.Fatalf("app in safe root %s suggested %q, want %q", parent, got, want)
		}
	}

	// 3. Directory link/junction pointing into LocalAppData must also be rejected.
	realAppInAppData := filepath.Join(localAppData, "Programs", "LinkedCage")
	writePackagedWindowsApp(t, realAppInAppData)
	appAlias := filepath.Join(root, "LinkedApp")
	if err := linkStorage(appAlias, realAppInAppData); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(appAlias) })
	if got := initialStorageDirectory(filepath.Join(appAlias, "resources", "cage.exe"), "windows", excluded); got != "" {
		t.Fatalf("junctioned app pointing into AppData suggested %q, want empty", got)
	}

	// 4. Safe app whose pre-existing CageData is a link/junction into AppData must be rejected.
	safeApp := filepath.Join(root, "SafeWithLinkedData")
	safeBackend := writePackagedWindowsApp(t, safeApp)
	appDataTarget := filepath.Join(localAppData, "CageDataTarget")
	if err := os.MkdirAll(appDataTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedData := filepath.Join(safeApp, "CageData")
	if err := linkStorage(linkedData, appDataTarget); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(linkedData) })
	if got := initialStorageDirectory(safeBackend, "windows", excluded); got != "" {
		t.Fatalf("CageData junction pointing into AppData suggested %q, want empty", got)
	}

	// 5. Explicit user target selection rejects AppData and junctions into AppData on Windows,
	// while still allowing safe directories outside AppData (and macOS/Linux).
	appDataChoice := filepath.Join(localAppData, "Programs", "Cage", "CageData")
	if err := validateStorageTarget(profile, appDataChoice, filepath.Join(localAppData, "Programs", "Cage"), "windows"); err == nil {
		t.Fatal("validateStorageTarget accepted Windows target inside LOCALAPPDATA")
	}
	roamingChoice := filepath.Join(roamingAppData, "CageStorage")
	if err := validateStorageTarget(profile, roamingChoice, filepath.Join(root, "Outside", "Cage"), "windows"); err == nil {
		t.Fatal("validateStorageTarget accepted Windows target inside APPDATA")
	}
	junctionChoice := filepath.Join(appAlias, "NewData")
	if err := validateStorageTarget(profile, junctionChoice, filepath.Join(root, "Outside", "Cage"), "windows"); err == nil {
		t.Fatal("validateStorageTarget accepted Windows target resolving through link into LOCALAPPDATA")
	}
	safeChoice := filepath.Join(root, "SafeStorage")
	if err := validateStorageTarget(profile, safeChoice, filepath.Join(root, "Outside", "Cage"), "windows"); err != nil {
		t.Fatalf("validateStorageTarget rejected safe Windows target %s: %v", safeChoice, err)
	}
	if err := validateStorageTarget(profile, roamingChoice, filepath.Join(root, "Outside", "Cage"), "linux"); err != nil {
		t.Fatalf("non-Windows platform unexpectedly rejected AppData-like path: %v", err)
	}
}

func TestWindowsExistingAppDataStorageCanStillMigrateOut(t *testing.T) {
	profile, root := newStorageFixture(t)
	localAppData := filepath.Join(root, "AppData", "Local")
	legacyDir := filepath.Join(localAppData, "Programs", "Cage", "CageData")
	legacyState := filepath.Join(legacyDir, "state")
	disk := filepath.Join(legacyState, "lima", "guest", "disk")
	writeStorageFixture(t, disk, []byte("existing-vm-disk"))
	id := "0123456789abcdef0123456789abcdef"
	writeStorageFixture(t, filepath.Join(legacyDir, storageMarker), []byte(id))
	if err := writeStorageJSON(filepath.Join(profile, locationFile), storageRecord{Directory: legacyDir, ID: id}); err != nil {
		t.Fatal(err)
	}
	if err := linkStorage(storageAnchor(profile), legacyState); err != nil {
		t.Fatal(err)
	}

	current, err := GetStorageLocation(profile)
	if err != nil {
		t.Fatalf("existing AppData storage must remain readable: %v", err)
	}
	if !current.Custom || !current.HasEnvironment || current.Directory != legacyDir {
		t.Fatalf("unexpected current location: %+v", current)
	}

	safeDest := filepath.Join(root, "MigratedCageData")
	moved, err := MoveStorage(context.Background(), profile, safeDest, current, nil)
	if err != nil {
		t.Fatalf("migrating out of AppData failed: %v", err)
	}
	if moved.Directory != safeDest || !moved.HasEnvironment {
		t.Fatalf("unexpected moved location: %+v", moved)
	}
	assertStorageFile(t, filepath.Join(safeDest, "state", "lima", "guest", "disk"), []byte("existing-vm-disk"))
	if runtime.GOOS == "windows" {
		// On Windows, InitialStorageDirectory on an AppData-located executable returns "".
		backend := writePackagedWindowsApp(t, filepath.Join(localAppData, "Programs", "Cage"))
		if got := InitialStorageDirectory(backend); got != "" {
			t.Fatalf("InitialStorageDirectory on Windows AppData install = %q, want empty", got)
		}
	}
}
