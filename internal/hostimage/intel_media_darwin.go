package hostimage

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ulikunitz/xz"
)

// PrepareIntelInstaller extracts only clean installation resources. It never
// launches InstallAssistant or installs packages into the host filesystem.
func PrepareIntelInstaller(ctx context.Context, pkg, cache, version string, transport http.RoundTripper) (recovery, appArchive string, err error) {
	if err := os.MkdirAll(cache, 0700); err != nil {
		return "", "", err
	}
	recovery, appArchive = filepath.Join(cache, "recovery.dmg"), filepath.Join(cache, "installer-app.tar.gz")
	if regularFile(recovery) && regularFile(appArchive) {
		return recovery, appArchive, nil
	}
	work, err := os.MkdirTemp(cache, ".intel-media-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(work)
	cmd := exec.CommandContext(ctx, "/usr/bin/xar", "-xf", pkg, "Payload")
	cmd.Dir = work
	if err := cmd.Run(); err != nil {
		return "", "", err
	}
	if err := unpackPBZX(ctx, filepath.Join(work, "Payload"), filepath.Join(work, "Payload.cpio")); err != nil {
		return "", "", err
	}
	stub := filepath.Join(work, "stub")
	if err := os.Mkdir(stub, 0700); err != nil {
		return "", "", err
	}
	cmd = exec.CommandContext(ctx, "/usr/bin/tar", "-xf", filepath.Join(work, "Payload.cpio"), "--no-same-owner", "-C", stub)
	if err := cmd.Run(); err != nil {
		return "", "", err
	}
	apps, err := filepath.Glob(filepath.Join(stub, "Applications", "Install macOS*.app"))
	if err != nil || len(apps) != 1 {
		return "", "", errors.New("Apple installer application missing")
	}
	archive := filepath.Join(work, "installer-app.tar.gz")
	cmd = exec.CommandContext(ctx, "/usr/bin/tar", "--no-mac-metadata", "-czf", archive, "-C", filepath.Dir(apps[0]), filepath.Base(apps[0]))
	if err := cmd.Run(); err != nil {
		return "", "", err
	}
	base, err := PrepareIntelRecovery(ctx, cache, version, transport)
	if err != nil {
		return "", "", err
	}
	raw := filepath.Join(work, "recovery.dmg")
	cmd = exec.CommandContext(ctx, "/usr/bin/hdiutil", "convert", base, "-format", "UDRW", "-o", raw)
	if err := cmd.Run(); err != nil {
		return "", "", err
	}
	if err := os.Rename(raw, recovery); err != nil {
		return "", "", err
	}
	if err := os.Rename(archive, appArchive); err != nil {
		return "", "", err
	}
	return recovery, appArchive, nil
}

func unpackPBZX(ctx context.Context, input, output string) error {
	file, err := os.Open(input)
	if err != nil {
		return err
	}
	defer file.Close()
	var header [12]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return err
	}
	if string(header[:4]) != "pbzx" {
		return errors.New("unsupported Apple installer payload compression")
	}
	flags := binary.BigEndian.Uint64(header[4:])
	out, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	var total int64
	for flags&(1<<24) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		var chunk [16]byte
		if _, err := io.ReadFull(file, chunk[:]); err != nil {
			return err
		}
		flags = binary.BigEndian.Uint64(chunk[:8])
		size := binary.BigEndian.Uint64(chunk[8:])
		if size == 0 || size > 64<<20 {
			return errors.New("invalid Apple payload chunk")
		}
		limited := &io.LimitedReader{R: file, N: int64(size)}
		var source io.Reader = limited
		if size != 1<<24 {
			compressed, err := xz.NewReader(limited)
			if err != nil {
				return err
			}
			source = compressed
		}
		count, err := io.Copy(out, io.LimitReader(source, (1<<30)-total+1))
		if err != nil {
			return err
		}
		total += count
		if total > 1<<30 || limited.N != 0 {
			return errors.New("Apple installer stub exceeds its size limit")
		}
	}
	return out.Sync()
}
