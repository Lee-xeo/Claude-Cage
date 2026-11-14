// SPDX-License-Identifier: Apache-2.0
package qemu

import "testing"

func TestGraphicsDeviceSelection(t *testing.T) {
	for _, row := range []struct {
		arch                 string
		windows, accelerated bool
		device               string
	}{{"x86_64", false, false, "virtio-vga"}, {"x86_64", false, true, "virtio-vga-gl"}, {"aarch64", false, false, "virtio-gpu"}, {"aarch64", false, true, "virtio-gpu-gl-pci"}, {"aarch64", true, false, "ramfb"}} {
		if got := graphicsDevice(row.arch, row.windows, row.accelerated); got != row.device {
			t.Fatalf("%s: %s != %s", row.arch, got, row.device)
		}
	}
}
