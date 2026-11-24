package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

func TestCheckProxyTransportWith(t *testing.T) {
	cfg := cage.DefaultConfig()
	noHint := func(context.Context, string) string { return "" }

	t.Run("healthy binding and passing probe", func(t *testing.T) {
		probed := false
		err := checkProxyTransportWith(
			context.Background(),
			t.TempDir(),
			cfg,
			func(context.Context, string) managedproxy.BindingStatus {
				return managedproxy.BindingStatus{Bound: "WLAN", Current: "WLAN"}
			},
			func(context.Context, cage.Config) (string, error) {
				probed = true
				return "203.0.113.10", nil
			},
			noHint,
		)
		if err != nil || !probed {
			t.Fatalf("expected nil and probe call, got err=%v probed=%v", err, probed)
		}
	})

	for _, tc := range []struct {
		name   string
		status managedproxy.BindingStatus
		want   string
	}{
		{
			name:   "interface changed fails fast without probe",
			status: managedproxy.BindingStatus{Bound: "en0", Current: "en7", Reason: "interface_changed"},
			want:   "cage proxy-start",
		},
		{
			name:   "address changed fails fast without probe",
			status: managedproxy.BindingStatus{Bound: "WLAN", Current: "WLAN", Reason: "address_changed"},
			want:   "cage proxy-start",
		},
		{
			name:   "uplink unavailable fails fast without probe",
			status: managedproxy.BindingStatus{Bound: "WLAN", Reason: "uplink_unavailable", UplinkErr: errors.New("no uplink")},
			want:   "物理 IPv4 网卡",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probed := false
			err := checkProxyTransportWith(
				context.Background(),
				t.TempDir(),
				cfg,
				func(context.Context, string) managedproxy.BindingStatus { return tc.status },
				func(context.Context, cage.Config) (string, error) {
					probed = true
					return "", nil
				},
				noHint,
			)
			if err == nil || probed || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected fail-fast containing %q without probe, got err=%v probed=%v", tc.want, err, probed)
			}
		})
	}

	t.Run("probe timeout with windows tun conflict appends hint", func(t *testing.T) {
		const tunHint = "检测到宿主机存在活跃的 TUN/VPN 默认路由"
		var seenBound string
		err := checkProxyTransportWith(
			context.Background(),
			t.TempDir(),
			cfg,
			func(context.Context, string) managedproxy.BindingStatus {
				return managedproxy.BindingStatus{Bound: "WLAN 2", Current: "WLAN 2"}
			},
			func(context.Context, cage.Config) (string, error) {
				return "", fmt.Errorf("probe failed: %w", cage.ErrExitProbeTimeout)
			},
			func(_ context.Context, bound string) string {
				seenBound = bound
				return tunHint
			},
		)
		if err == nil || seenBound != "WLAN 2" || !strings.Contains(err.Error(), "(no direct fallback)") || !strings.Contains(err.Error(), tunHint) {
			t.Fatalf("unexpected error: %v (bound=%q)", err, seenBound)
		}
	})

	t.Run("context canceled probe error does not append tun hint", func(t *testing.T) {
		hintCalled := false
		err := checkProxyTransportWith(
			context.Background(),
			t.TempDir(),
			cfg,
			func(context.Context, string) managedproxy.BindingStatus {
				return managedproxy.BindingStatus{Bound: "WLAN", Current: "WLAN"}
			},
			func(context.Context, cage.Config) (string, error) {
				return "", context.Canceled
			},
			func(context.Context, string) string {
				hintCalled = true
				return "unexpected hint"
			},
		)
		if err == nil || hintCalled || !strings.Contains(err.Error(), "(no direct fallback)") || strings.Contains(err.Error(), "unexpected hint") {
			t.Fatalf("expected canceled error without hint, got err=%v hintCalled=%v", err, hintCalled)
		}
	})
}
