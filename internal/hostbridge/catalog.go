package hostbridge

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
)

// The plugins that come with the application, each in its own folder.
//
//go:embed catalog
var catalogFiles embed.FS

// Catalog lists the plugins that come with the application.
func Catalog() []Manifest {
	entries, _ := catalogFiles.ReadDir("catalog")
	var list []Manifest
	for _, entry := range entries {
		data, err := catalogFiles.ReadFile(path.Join("catalog", entry.Name(), ManifestFile))
		if err != nil {
			continue
		}
		if manifest, err := parseManifest(data); err == nil && manifest.Name == entry.Name() {
			list = append(list, manifest)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// InstallFromCatalog copies the named plugin into its own folder under dir.
// It never replaces a folder that is already there.
func InstallFromCatalog(name, dir string) error {
	if !pluginName.MatchString(name) {
		return errors.New("unknown plugin")
	}
	source := path.Join("catalog", name)
	if _, err := catalogFiles.ReadFile(path.Join(source, ManifestFile)); err != nil {
		return errors.New("unknown plugin")
	}
	target := filepath.Join(dir, name)
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("plugin %s is already installed", name)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Copy beside the target, so a failure leaves no half-installed plugin.
	staging, err := os.MkdirTemp(dir, ".installing-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	err = fs.WalkDir(catalogFiles, source, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(filepath.FromSlash(source), filepath.FromSlash(file))
		if err != nil {
			return err
		}
		to := filepath.Join(staging, relative)
		if entry.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		data, err := catalogFiles.ReadFile(file)
		if err != nil {
			return err
		}
		return os.WriteFile(to, data, 0o755)
	})
	if err != nil {
		return err
	}
	return os.Rename(staging, target)
}
