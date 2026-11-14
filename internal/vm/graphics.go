package vm

import (
	"errors"
	"os"
	"path/filepath"
)

// An explicit VirGL request must not silently become a CPU renderer. The
// driver also checks host/guest support; browser diagnostics measure fallback.
func virGLHostReady() error {
	paths, _ := filepath.Glob("/dev/dri/renderD*")
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			continue
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err == nil {
			f.Close()
			return nil
		}
	}
	return errors.New("VirGL 需要当前用户可访问的 DRM render 节点及支持 OpenGL 的 QEMU；未修改宿主驱动或设备权限")
}
