package workbench

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestWindowsWindowRFBPixelWheel(t *testing.T) {
	for _, tc := range []struct {
		name       string
		negotiated bool
		delta      int32
		accepted   bool
	}{
		{"distance", true, 720, true}, {"reverse", true, -360, true},
		{"unnegotiated", false, 100, false}, {"bounded", true, 2049, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := windowTestFrame(t, 2, 1)
			frame.Target = "verified-frame"
			server, client := net.Pipe()
			defer client.Close()
			client.SetDeadline(time.Now().Add(5 * time.Second))
			inputs := make(chan map[string]any, 1)
			done := make(chan error, 1)
			go func() {
				defer server.Close()
				done <- serveWindowsWindowRFB(server, frame, func(input map[string]any, output any) error {
					if input["action"] == "frame" {
						*output.(*windowsWindowFrame) = frame
					} else {
						inputs <- input
					}
					return nil
				})
			}()
			read := func(n int) []byte {
				t.Helper()
				b := make([]byte, n)
				if _, e := io.ReadFull(client, b); e != nil {
					t.Fatal(e)
				}
				return b
			}
			write := func(b []byte) {
				t.Helper()
				if _, e := client.Write(b); e != nil {
					t.Fatal(e)
				}
			}
			read(12)
			write([]byte("RFB 003.008\n"))
			read(2)
			write([]byte{1})
			read(4)
			write([]byte{1})
			init := read(24)
			read(int(binary.BigEndian.Uint32(init[20:])))
			if tc.negotiated {
				packet := []byte{2, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0}
				fence, wheel := int32(-312), int32(-57345)
				binary.BigEndian.PutUint32(packet[4:], uint32(fence))
				binary.BigEndian.PutUint32(packet[8:], uint32(wheel))
				write(packet)
				read(9)
				header := read(9)
				if string(read(int(header[8]))) != "cage-wheel-v1" {
					t.Fatal("missing capability")
				}
			}
			write([]byte{3, 0, 0, 0, 0, 0, 0, 2, 0, 1})
			read(24)
			packet := make([]byte, 14)
			packet[0] = 250
			binary.BigEndian.PutUint16(packet[2:], 1)
			binary.BigEndian.PutUint32(packet[10:], uint32(tc.delta))
			write(packet)
			if tc.accepted {
				select {
				case input := <-inputs:
					if input["deltaY"] != tc.delta || input["target"] != "verified-frame" || input["action"] != "pointer" {
						t.Fatalf("invalid input %v", input)
					}
				case <-time.After(time.Second):
					t.Fatal("wheel missing")
				}
				// The same stream remains ready for subsequent frames.
				write([]byte{3, 0, 0, 0, 0, 0, 0, 2, 0, 1})
				read(24)
			} else {
				select {
				case e := <-done:
					if e == nil {
						t.Fatal("invalid wheel accepted")
					}
				case <-time.After(time.Second):
					t.Fatal("invalid wheel not rejected")
				}
			}
		})
	}
}
