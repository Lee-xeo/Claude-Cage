package forwarder

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
)

func TestDialRequiresCurrentPolicyBeforeForwarding(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			dst := netip.MustParseAddrPort("8.8.8.8:443")
			allowed := true
			policy := types.Outbound(func(n string, target netip.AddrPort) bool {
				return allowed && n == network && target == dst
			})
			client, peer := net.Pipe()
			defer client.Close()
			defer peer.Close()
			calls := 0
			dial := func(_ context.Context, n, address string) (net.Conn, error) {
				calls++
				if n != network || address != dst.String() {
					t.Fatal("dial did not use checked destination")
				}
				allowed = false
				return client, nil
			}
			for range 2 {
				conn, err := dialAllowed(policy, network, dst, dial)
				if conn != nil || !errors.Is(err, errDestinationDenied) {
					t.Fatalf("denied flow returned %v, %v", conn, err)
				}
			}
			if calls != 1 {
				t.Fatalf("dial called %d times", calls)
			}
			if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("denied socket remained open: %v", err)
			}
		})
	}
}
