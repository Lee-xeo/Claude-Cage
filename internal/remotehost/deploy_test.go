package remotehost

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// exitError 通过回环 SSH 连接取得真实的 *ssh.ExitError。
func exitError(t *testing.T, code uint32) error {
	t.Helper()
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(private)
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	// SSH 双方先同时写版本串，net.Pipe 无缓冲会死锁，这里用回环 TCP。
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		serverConn, err := listener.Accept()
		if err != nil {
			return
		}
		defer serverConn.Close()
		_, channels, requests, err := ssh.NewServerConn(serverConn, config)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		for in := range channels {
			channel, requests, err := in.Accept()
			if err != nil {
				return
			}
			go func() {
				for request := range requests {
					request.Reply(request.Type == "exec", nil)
					if request.Type == "exec" {
						channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{code}))
						channel.Close()
					}
				}
			}()
		}
	}()
	clientConn, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	conn, channels, requests, err := ssh.NewClientConn(clientConn, listener.Addr().String(), &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	client := ssh.NewClient(conn, channels, requests)
	t.Cleanup(func() { client.Close() })
	_, err = runSSH(client, "true", nil)
	var exit *ssh.ExitError
	if !errors.As(err, &exit) || exit.ExitStatus() != int(code) {
		t.Fatalf("got %v, want exit status %d", err, code)
	}
	return err
}

func TestDeploymentErrorMapsExitStatus(t *testing.T) {
	for _, tc := range []struct {
		code     uint32
		want     string
		contains []string
	}{
		{62, "cloud_setup_failed", []string{"cgroup v2"}},
		{77, "cloud_setup_failed", []string{"user@<UID>.service", "Delegate=cpu memory pids", "daemon-reload", "重新登录"}},
		{99, "server_entry_failed", nil},
	} {
		var got *Error
		if !errors.As(deploymentError(exitError(t, tc.code)), &got) || got.Code != tc.want {
			t.Fatalf("exit %d: got %#v, want code %s", tc.code, got, tc.want)
		}
		for _, text := range tc.contains {
			if !strings.Contains(got.Message, text) {
				t.Errorf("exit %d: message %q lacks %q", tc.code, got.Message, text)
			}
		}
	}
	var got *Error
	if !errors.As(deploymentError(errors.New("dial")), &got) || got.Code != "server_entry_failed" {
		t.Fatalf("non-exit error: got %#v", got)
	}
}
