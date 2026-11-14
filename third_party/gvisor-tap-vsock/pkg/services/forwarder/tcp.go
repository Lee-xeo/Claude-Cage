package forwarder

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/inetaf/tcpproxy"
	log "github.com/sirupsen/logrus"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const linkLocalSubnet = "169.254.0.0/16"

func TCP(s *stack.Stack, nat map[tcpip.Address]tcpip.Address, natLock *sync.Mutex, ec2MetadataAccess bool, outbound types.Outbound, dialers ...types.DialContext) *tcp.Forwarder {
	return tcp.NewForwarder(s, 0, 10, func(r *tcp.ForwarderRequest) {
		localAddress := r.ID().LocalAddress

		if (!ec2MetadataAccess) && linkLocal().Contains(localAddress) {
			r.Complete(true)
			return
		}

		natLock.Lock()
		if replaced, ok := nat[localAddress]; ok {
			localAddress = replaced
		}
		natLock.Unlock()
		dst, ok := netip.AddrFromSlice(localAddress.AsSlice())
		if !ok {
			r.Complete(true)
			return
		}
		dial := (&net.Dialer{}).DialContext
		if len(dialers) > 0 && dialers[0] != nil {
			dial = dialers[0]
		}
		outbound, err := dialAllowed(outbound, "tcp", netip.AddrPortFrom(dst, r.ID().LocalPort), dial)
		if err != nil {
			log.Tracef("net.Dial() = %v", err)
			r.Complete(true)
			return
		}

		var wq waiter.Queue
		ep, tcpErr := r.CreateEndpoint(&wq)
		r.Complete(false)
		if tcpErr != nil {
			_ = outbound.Close()
			if _, ok := tcpErr.(*tcpip.ErrConnectionRefused); ok {
				// transient error
				log.Debugf("r.CreateEndpoint() = %v", tcpErr)
			} else {
				log.Errorf("r.CreateEndpoint() = %v", tcpErr)
			}
			return
		}

		remote := tcpproxy.DialProxy{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return outbound, nil
			},
		}
		remote.HandleConn(gonet.NewTCPConn(&wq, ep))
	})
}

func linkLocal() *tcpip.Subnet {
	_, parsedSubnet, _ := net.ParseCIDR(linkLocalSubnet) // CoreOS VM tries to connect to Amazon EC2 metadata service
	subnet, _ := tcpip.NewSubnet(tcpip.AddrFromSlice(parsedSubnet.IP), tcpip.MaskFromBytes(parsedSubnet.Mask))
	return &subnet
}

// Recheck the numeric destination after a potentially slow proxy handshake or
// direct dial, before any guest payload can cross the new connection.
func dialAllowed(policy types.Outbound, network string, dst netip.AddrPort, dial types.DialContext) (net.Conn, error) {
	if !policy.Allows(network, dst) {
		return nil, errDestinationDenied
	}
	conn, err := dial(context.Background(), network, dst.String())
	if err != nil {
		return nil, err
	}
	if !policy.Allows(network, dst) {
		conn.Close()
		return nil, errDestinationDenied
	}
	return conn, nil
}

var errDestinationDenied = errors.New("destination denied by outbound policy")
