// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package hostagent

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	"gotest.tools/v3/assert"

	"github.com/lima-vm/lima/v2/pkg/guestagent/api"
	guestagentclient "github.com/lima-vm/lima/v2/pkg/guestagent/api/client"
	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/portfwd"
)

// invalidIPGuestService streams one port event whose IP is not an IP address.
// A compromised guest can report an arbitrary string there; this one carries
// ANSI escapes that would rewrite the operator's terminal if it reached
// PortForwardEvent.GuestAddr.
type invalidIPGuestService struct {
	api.UnimplementedGuestServiceServer
}

func (*invalidIPGuestService) GetInfo(context.Context, *emptypb.Empty) (*api.Info, error) {
	return &api.Info{}, nil
}

func (*invalidIPGuestService) GetEvents(_ *emptypb.Empty, stream api.GuestService_GetEventsServer) error {
	return stream.Send(&api.Event{
		AddedLocalPorts: []*api.IPPort{{Ip: "1.2.3.4\x1b[2Kspoofed\x07", Port: 8080, Protocol: "tcp"}},
	})
}

func TestProcessGuestAgentEventsRejectsInvalidIP(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(grpc.Creds(guestagentclient.NewCredentials()))
	api.RegisterGuestServiceServer(server, &invalidIPGuestService{})
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	client, err := guestagentclient.NewGuestAgentClient(listener.DialContext)
	assert.NilError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	a := &HostAgent{guestAgentAliveCh: make(chan struct{})}
	err = a.processGuestAgentEvents(t.Context(), client)
	assert.ErrorContains(t, err, "invalid IP")
}

// portGuestService reports one forwarded TCP port and records every tunnel
// it is asked to open.
type portGuestService struct {
	api.UnimplementedGuestServiceServer

	port    int32
	tunnels chan<- string
}

func (*portGuestService) GetInfo(context.Context, *emptypb.Empty) (*api.Info, error) {
	return &api.Info{}, nil
}

func (s *portGuestService) GetEvents(_ *emptypb.Empty, stream api.GuestService_GetEventsServer) error {
	return stream.Send(&api.Event{
		AddedLocalPorts: []*api.IPPort{{Ip: "127.0.0.1", Port: s.port, Protocol: "tcp"}},
	})
}

func (s *portGuestService) Tunnel(stream api.GuestService_TunnelServer) error {
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	s.tunnels <- msg.GuestAddr
	for {
		if _, err := stream.Recv(); err != nil {
			return nil
		}
	}
}

func newBufconnGuestAgentClient(t *testing.T, guestService api.GuestServiceServer) *guestagentclient.GuestAgentClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(grpc.Creds(guestagentclient.NewCredentials()))
	api.RegisterGuestServiceServer(server, guestService)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	client, err := guestagentclient.NewGuestAgentClient(listener.DialContext)
	assert.NilError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NilError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	assert.NilError(t, l.Close())
	return port
}

// A forwarded listener outlives the guest agent client that reported its port.
// After the host agent replaces that client, a new connection to the same
// listener must be carried by the new client, even before (or without) the
// guest reporting the port again (#826).
func TestPortForwardListenerFollowsReplacedGuestAgentClient(t *testing.T) {
	port := freeTCPPort(t)
	staleTunnels := make(chan string, 4)
	stale := newBufconnGuestAgentClient(t, &portGuestService{port: int32(port), tunnels: staleTunnels})
	freshTunnels := make(chan string, 4)
	fresh := newBufconnGuestAgentClient(t, &portGuestService{port: int32(port), tunnels: freshTunnels})

	rules := []limatype.PortForward{{
		GuestIPMustBeZero: new(false),
		GuestIP:           net.IPv4(127, 0, 0, 1),
		GuestPortRange:    [2]int{port, port},
		HostIP:            net.IPv4(127, 0, 0, 1),
		HostPortRange:     [2]int{port, port},
		Proto:             limatype.ProtoTCP,
	}}
	a := &HostAgent{
		guestAgentAliveCh: make(chan struct{}),
		grpcPortForwarder: portfwd.NewPortForwarder(rules, false, false, nil),
	}
	a.grpcTunnelDialer = portfwd.DialContextToCurrentGRPCTunnel(a.getClient)
	t.Cleanup(func() { _ = a.grpcPortForwarder.Close() })

	a.clientMu.Lock()
	a.client = stale
	a.clientMu.Unlock()
	// The stale client's event stream reports the port once and then ends,
	// as it does when the guest agent goes away.
	err := a.processGuestAgentEvents(t.Context(), stale)
	assert.Assert(t, err != nil)

	hostAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	connect := func() net.Conn {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			conn, err := net.DialTimeout("tcp", hostAddr, time.Second)
			if err == nil {
				return conn
			}
			if time.Now().After(deadline) {
				t.Fatalf("listener %s did not open: %v", hostAddr, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	conn := connect()
	select {
	case <-staleTunnels:
	case <-time.After(5 * time.Second):
		t.Fatal("the first client did not carry the tunnel")
	}
	assert.NilError(t, conn.Close())

	// Reconnect: close the stale client and install a new one, as
	// getOrCreateClient does. No new port event arrives.
	a.clientMu.Lock()
	assert.NilError(t, a.client.Close())
	a.client = fresh
	a.clientMu.Unlock()

	conn = connect()
	defer conn.Close()
	_, err = conn.Write([]byte("ping"))
	assert.NilError(t, err)
	select {
	case got := <-freshTunnels:
		assert.Equal(t, got, hostAddr)
	case <-time.After(5 * time.Second):
		t.Fatal("the listener did not dial through the replaced client")
	}
	select {
	case got := <-staleTunnels:
		t.Fatalf("the closed client carried a tunnel to %s", got)
	default:
	}
}
