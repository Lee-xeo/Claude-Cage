package vm

import (
	"bytes"
	"testing"

	"github.com/lima-vm/lima/v2/pkg/limatype"
)

func TestMacOSInstallerActiveOnlyForDarwinInstaller(t *testing.T) {
	darwin, linux := limatype.DARWIN, limatype.LINUX
	for _, test := range []struct {
		os   *string
		opts map[string]any
		want bool
	}{
		{&darwin, map[string]any{"qemu": map[string]any{"macOS": map[string]any{"installer": true}}}, true},
		{&darwin, map[string]any{"qemu": map[string]any{"macOS": map[string]any{"installer": false}}}, false},
		{&darwin, nil, false},
		{&linux, map[string]any{"qemu": map[string]any{"macOS": map[string]any{"installer": true}}}, false},
	} {
		config := &limatype.LimaYAML{OS: test.os, VMOpts: test.opts}
		if got := macOSInstallerActive(config); got != test.want {
			t.Errorf("os=%s opts=%v: got %t", *test.os, test.opts, got)
		}
	}
	if macOSInstallerActive(nil) {
		t.Error("missing configuration treated as installer")
	}
}

func TestQuietBootLogDropsRefusedForwards(t *testing.T) {
	var out bytes.Buffer
	log := quietBootLog{&out}
	refused := `time="2026-11-15T11:42:06+08:00" level=info msg="[hostagent] tcpproxy: for incoming conn 127.0.0.1:1, error dialing \"192.168.5.15:22\": connect tcp: connection was refused"` + "\n"
	ready := `time="2026-11-15T12:15:54+08:00" level=info msg="READY."` + "\n"
	for _, line := range []string{refused, ready} {
		if n, err := log.Write([]byte(line)); err != nil || n != len(line) {
			t.Fatalf("write = %d, %v", n, err)
		}
	}
	if out.String() != ready {
		t.Fatalf("log = %q", out.String())
	}
}
