package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
	"os"
	"path/filepath"
)

func proxyCommand(command, profile string, cfg cage.Config) error {
	if command == "proxy-stop" {
		if err := closeManagedGates(profile); err != nil {
			return err
		}
		return managedproxy.Stop(profile)
	}
	p, err := managedproxy.Applied(profile, cfg.ProxyURL, cfg.ProxyRevision)
	if errors.Is(err, managedproxy.ErrLocalProxyNode) {
		closeErr := closeManagedGates(profile)
		_, runtimeErr := managedproxy.BoundInterface(filepath.Join(managedproxy.Dir(profile), "config.yaml"))
		if errors.Is(runtimeErr, managedproxy.ErrLocalProxyNode) {
			return errors.Join(err, closeErr, managedproxy.Stop(profile))
		}
		return errors.Join(err, closeErr)
	}
	if err != nil {
		return err
	}
	name, err := managedproxy.PhysicalInterface(context.Background())
	if err != nil {
		_ = closeManagedGates(profile)
		return err
	}
	if managedproxy.BindingMatches(filepath.Join(managedproxy.Dir(profile), "config.yaml"), name) && managedproxy.Running(context.Background(), profile, p) {
		return nil
	}
	if err = closeManagedGates(profile); err != nil {
		return err
	}
	p.Revision = managedproxy.Token()
	p.GuestDNS, p.BootstrapDNS = cfg.DNSEndpoints(), cfg.BootstrapEndpoints()
	cfg.ProxyRevision = p.Revision
	if err = p.Save(profile); err != nil {
		return err
	}
	path := os.Getenv(cage.ConfigEnv)
	if path == "" {
		path = filepath.Join(profile, "config.json")
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err = managedproxy.Write(path, data); err != nil {
		return err
	}
	files, err := managedGateFiles(profile)
	if err != nil {
		return err
	}
	for _, file := range files {
		if client, e := cage.OpenGateClient(filepath.Dir(file)); e == nil {
			if _, e = client.Status(context.Background()); e == nil {
				if _, e = client.SyncProxyRevision(context.Background()); e != nil {
					return e
				}
			}
		}
	}
	return managedproxy.Start(context.Background(), profile, p)
}

func closeManagedGates(profile string) error {
	files, err := managedGateFiles(profile)
	if err != nil {
		return err
	}
	for _, file := range files {
		if client, e := cage.OpenGateClient(filepath.Dir(file)); e == nil {
			if _, e = client.Status(context.Background()); e == nil {
				if _, e = client.Close(context.Background(), "代理核心停止或重启，等待重新检测"); e != nil {
					return e
				}
			}
		}
	}
	return nil
}

func managedGateFiles(profile string) ([]string, error) {
	files, _ := filepath.Glob(filepath.Join(profile, "state", "lima", "*", "cage-gate.json"))
	containers, _ := filepath.Glob(filepath.Join(profile, "state", "container", "*", "cage-gate.json"))
	files = append(files, containers...)
	return files, nil
}

func checkProxyTransport(ctx context.Context, profile string, cfg cage.Config) error {
	return checkProxyTransportWith(ctx, profile, cfg, managedproxy.CheckRuntimeBinding, cage.ProbeExitIP, managedproxy.WindowsNetworkConflictHint)
}

func checkProxyTransportWith(
	ctx context.Context,
	profile string,
	cfg cage.Config,
	checkBinding func(context.Context, string) managedproxy.BindingStatus,
	probe func(context.Context, cage.Config) (string, error),
	conflictHint func(context.Context, string) string,
) error {
	status := checkBinding(ctx, profile)
	if status.Drifted() {
		return fmt.Errorf("proxy transport check failed: %s", status.Message())
	}
	if _, err := probe(ctx, cfg); err != nil {
		if !errors.Is(err, context.Canceled) && (errors.Is(err, cage.ErrExitProbeTimeout) || errors.Is(err, cage.ErrProxyConnectFailed) || errors.Is(err, cage.ErrTLSHandshakeFailed)) {
			if hint := conflictHint(ctx, status.Bound); hint != "" {
				return fmt.Errorf("proxy transport check failed (no direct fallback): %w %s", err, hint)
			}
		}
		return fmt.Errorf("proxy transport check failed (no direct fallback): %w", err)
	}
	return nil
}
