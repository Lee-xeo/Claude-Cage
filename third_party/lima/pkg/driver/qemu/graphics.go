// SPDX-License-Identifier: Apache-2.0
package qemu

// Accelerated variants expose real VirGL rendering, not a renamed 2D device.
func graphicsDevice(arch string, windows, accelerated bool) string {
	if arch == "x86_64" || arch == "riscv64" {
		if accelerated {
			return "virtio-vga-gl"
		}
		return "virtio-vga"
	}
	if windows {
		return "ramfb"
	}
	if accelerated {
		return "virtio-gpu-gl-pci"
	}
	return "virtio-gpu"
}
