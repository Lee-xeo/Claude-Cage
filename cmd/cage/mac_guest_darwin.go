//go:build darwin && (amd64 || (arm64 && cgo && !no_vz))

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sylvinus/agent-vm/internal/guestos"
	"github.com/sylvinus/agent-vm/internal/vm"
)

func runMacGuest(ctx context.Context, profile, state string, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cage guest-os macos create <absolute.ipsw> | bind <user> | start | stop")
	}
	lima, err := vm.NewLima(filepath.Join(state, "lima"), state)
	if err != nil {
		return err
	}
	const name = "mac"
	switch args[0] {
	case "update-components":
		flags := flag.NewFlagSet("update-components", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		manifest := flags.String("manifest", "", "")
		updateTools := flags.Bool("tools", false, "")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || !filepath.IsAbs(*manifest) {
			return errors.New("需要本次原生组件更新清单")
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return guestos.MaintainMacOSComponents(ctx, profile, filepath.Join(filepath.Dir(exe), "guest-os"), *manifest, *updateTools, os.Stdout, os.Stderr)
	case "install":
		if len(args) != 1 {
			return errors.New("unexpected install argument")
		}
		return installMacWorkbench(ctx, profile, state, lima)
	case "create":
		if len(args) != 2 {
			return errors.New("supply the absolute path to an official macOS IPSW")
		}
		instances, err := lima.List(ctx)
		if err != nil {
			return err
		}
		switch {
		case len(instances) == 0:
			err = createMacOSGuest(ctx, profile, state, lima, name, args[1])
		case len(instances) == 1 && instances[0].Name == name:
			if bound, loadErr := guestos.LoadMacOSProfile(profile); loadErr != nil || bound != nil {
				return errors.New("this macOS guest already has a binding; use start")
			}
			err = matchMacOSGuestSource(ctx, profile, lima, name, args[1])
		default:
			return errors.New("create requires a dedicated macOS guest profile")
		}
		if err != nil {
			return err
		}
		return startInstallingMacOSGuest(ctx, profile, lima, name)
	case "bind":
		if len(args) != 2 {
			return errors.New("supply the ordinary guest account and its independently verified guest SSH host public key on stdin")
		}
		if err := lima.CheckMacOSGuest(ctx, name); err != nil {
			return err
		}
		key, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
		if err != nil || len(key) > 4096 {
			return errors.New("could not read the guest host public key")
		}
		return guestos.BindMacOSProfile(profile, args[1], key)
	case "start", "stop":
		if len(args) != 1 {
			return errors.New("unexpected macOS guest lifecycle argument")
		}
		m, err := guestos.LoadMacOSProfileOrRecovery(profile)
		if err != nil {
			return err
		}
		if m == nil {
			return errors.New("this profile is not bound to a prepared macOS guest")
		}
		if args[0] == "stop" {
			return stopPreparedMacOSGuest(ctx, profile, lima, m.Instance)
		}
		recovery, err := guestos.LoadMacOSInstallRecovery(profile)
		if err != nil {
			return err
		}
		if recovery != nil {
			install, err := guestos.LoadInstallation(profile)
			if err != nil || install == nil {
				return errors.New("macOS 恢复缺少原安装记录")
			}
			media := install.Media
			if media == "" && m.Backend != guestos.MacOSQEMU {
				media = filepath.Join(state, "guest-media", install.System, "MacOS.ipsw")
			}
			if err := matchMacOSGuestSource(ctx, profile, lima, m.Instance, media); err != nil {
				return err
			}
		} else if err := lima.CheckMacOSGuest(ctx, m.Instance); err != nil {
			return err
		}
		return lima.Start(ctx, m.Instance, os.Stderr)
	default:
		return fmt.Errorf("unknown macOS guest command %q", args[0])
	}
}
