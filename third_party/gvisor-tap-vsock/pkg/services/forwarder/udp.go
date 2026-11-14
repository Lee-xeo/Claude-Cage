package forwarder

import (
	"net"
	"net/netip"
	"sync"

	"github.com/containers/gvisor-tap-vsock/pkg/types"
	log "github.com/sirupsen/logrus"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

func UDP(s *stack.Stack, nat map[tcpip.Address]tcpip.Address, natLock *sync.Mutex, ec2MetadataAccess bool, outbound types.Outbound) *udp.Forwarder {
	return udp.NewForwarder(s, func(r *udp.ForwarderRequest) {
		localAddress := r.ID().LocalAddress

		if (!ec2MetadataAccess) && linkLocal().Contains(localAddress) || (localAddress == header.IPv4Broadcast) {
			return
		}

		natLock.Lock()
		if replaced, ok := nat[localAddress]; ok {
			localAddress = replaced
		}
		natLock.Unlock()
		dst, ok := netip.AddrFromSlice(localAddress.AsSlice())
		target := netip.AddrPortFrom(dst, r.ID().LocalPort)
		if !ok || !outbound.Allows("udp", target) {
			return
		}

		var wq waiter.Queue
		ep, tcpErr := r.CreateEndpoint(&wq)
		if tcpErr != nil {
			if _, ok := tcpErr.(*tcpip.ErrConnectionRefused); ok {
				// transient error
				log.Debugf("r.CreateEndpoint() = %v", tcpErr)
			} else {
				log.Errorf("r.CreateEndpoint() = %v", tcpErr)
			}
			return
		}

		p, _ := NewUDPProxy(&autoStoppingListener{underlying: gonet.NewUDPConn(&wq, ep)}, func() (net.Conn, error) {
			return dialAllowed(outbound, "udp", target, (&net.Dialer{}).DialContext)
		})
		go func() {
			p.Run()

			// note that at this point packets that are sent to the current forwarder session
			// will be dropped. We will start processing the packets again when we get a new
			// forwarder request.
			ep.Close()
		}()
	})
}
