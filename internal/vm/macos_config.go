package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
	"github.com/lima-vm/lima/v2/pkg/limayaml"
	"github.com/sylvinus/agent-vm/internal/guestos"
)

func (l *Lima) CheckMacOSGuest(ctx context.Context, name string) error {
	inst, err := l.inspect(ctx, name)
	if err != nil {
		return err
	}
	c := inst.Config
	if c == nil || c.OS == nil || *c.OS != limatype.DARWIN || c.VMType == nil || len(c.Mounts) != 0 || len(c.AdditionalDisks) != 0 || c.Video.Display == nil || *c.Video.Display != "offscreen" {
		return errors.New("expected an offscreen macOS guest with its own system disk and no host shares")
	}
	for _, p := range c.PortForwards {
		if !p.Ignore {
			return errors.New("macOS guest has an unmanaged forwarded port")
		}
	}
	switch *c.VMType {
	case limatype.VZ:
		if c.Arch == nil || *c.Arch != limatype.AARCH64 {
			return errors.New("invalid macOS VZ architecture")
		}
	case limatype.QEMU:
		var opts limatype.QEMUOpts
		if limayaml.Convert(c.VMOpts[limatype.QEMU], &opts, "vmOpts.qemu") != nil || opts.MacOS == nil || c.Arch == nil || *c.Arch != limatype.X8664 {
			return errors.New("missing Intel macOS hardware configuration")
		}
		identity, err := guestos.ReadIntelMacOSIdentity(inst.Dir)
		if err != nil || identity == nil || identity.UUID != opts.MacOS.UUID {
			return errors.New("Intel macOS hardware identity changed")
		}
		path := filepath.Join(inst.Dir, filenames.MacOSBootDisk)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<20 {
			return errors.New("invalid private Intel macOS boot disk")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			return err
		}
		if hex.EncodeToString(hash.Sum(nil)) != identity.BootSHA256 {
			return errors.New("Intel macOS boot resources changed after installation")
		}
	default:
		return errors.New("unsupported macOS guest backend")
	}
	return NetworkConfined(c)
}
