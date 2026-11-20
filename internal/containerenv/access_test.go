package containerenv

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestGuestSSHNegotiatesPinnedHostKeyAndRejectsReplacement(t *testing.T) {
	r := newRuntime(t.TempDir(), fixtureConfig())
	if _, err := r.ensureSSHKey(); err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaHost, err := ssh.NewSignerFromKey(other)
	if err != nil {
		t.Fatal(err)
	}
	known := fmt.Sprintf("cage-container-%s %s", r.identity, ssh.MarshalAuthorizedKey(pinned.PublicKey()))
	if err := os.WriteFile(filepath.Join(r.InstanceDir(), "known_hosts"), []byte(known), 0600); err != nil {
		t.Fatal(err)
	}
	configuration, err := r.guestSSHConfiguration("alex")
	if err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []bool{false, true} {
		hostKey := pinned
		if replacement {
			_, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			hostKey, err = ssh.NewSignerFromKey(private)
			if err != nil {
				t.Fatal(err)
			}
		}
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		serverDone := make(chan struct{})
		go func() {
			defer close(serverDone)
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
			serverConfig := &ssh.ServerConfig{NoClientAuth: true, NoClientAuthCallback: func(conn ssh.ConnMetadata) (*ssh.Permissions, error) {
				if conn.User() != "alex" {
					return nil, fmt.Errorf("unexpected login account %q", conn.User())
				}
				return nil, nil
			}}
			serverConfig.AddHostKey(ecdsaHost)
			serverConfig.AddHostKey(hostKey)
			server, _, _, err := ssh.NewServerConn(connection, serverConfig)
			if err == nil {
				server.Close()
			}
		}()
		connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
		client, _, _, err := ssh.NewClientConn(connection, "cage-container-"+r.identity+":22", configuration)
		if replacement && err == nil {
			t.Error("replaced guest host key was accepted")
		}
		if !replacement && err != nil {
			t.Errorf("pinned ED25519 key was not negotiated alongside ECDSA: %v", err)
		}
		if client != nil {
			client.Close()
		}
		connection.Close()
		listener.Close()
		<-serverDone
	}
}

func TestAccountSetupWaitsForColdStartedSSHService(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the container script runs in a Linux container")
	}
	for _, test := range []struct {
		name     string
		inactive int
		code     int
	}{{"ready after two probes", 2, 0}, {"never ready", 1000, 75}} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			probes := filepath.Join(dir, "probes")
			// The fake unit stays inactive for a fixed number of probes; sleep is a no-op.
			systemctl := fmt.Sprintf("#!/bin/sh\n[ \"$*\" = 'is-active --quiet ssh' ] || exit 91\nprintf x >> %q\n[ \"$(wc -c < %q)\" -gt %d ]\n", probes, probes, test.inactive)
			for name, body := range map[string]string{"systemctl": systemctl, "sleep": "#!/bin/sh\nexit 0\n"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("sh", "-c", "set -eu\n"+waitForSSHService+"printf configured\n")
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
			out, err := cmd.Output()
			code := 0
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(probes)
			if code != test.code || (code == 0) != (string(out) == "configured") {
				t.Fatalf("exit %d output %q after %d probes", code, out, len(data))
			}
			if code == 0 && len(data) != test.inactive+1 {
				t.Fatalf("account setup continued after %d probes", len(data))
			}
			if code != 0 && len(data) != 31 {
				t.Fatalf("readiness wait made %d probes", len(data))
			}
		})
	}
}
