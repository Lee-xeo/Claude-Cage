package maclogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/digitalocean/go-qemu/qmp"
	driver "github.com/lima-vm/lima/v2/pkg/driver/qemu"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
	"github.com/spakin/netpbm"
	"github.com/sylvinus/agent-vm/internal/guestos"
	"golang.org/x/image/draw"
)

func init() {
	driver.OwnsMacOSOffscreenView = func(dir string) bool {
		if !ownsLoginDirectory(dir) {
			return false
		}
		bound, err := guestos.LoadMacOSProfileOrRecovery(filepath.Dir(os.Getenv("CLAUDE_CAGE_CONFIG")))
		return err == nil && bound != nil && bound.Backend == guestos.MacOSQEMU
	}
	driver.MacOSOffscreenView = func(ctx context.Context, dir string) error {
		if !driver.OwnsMacOSOffscreenView(dir) {
			return errors.New("invalid Intel macOS login view")
		}
		socket := filepath.Join(dir, filenames.QMPSock)
		deadline := time.Now().Add(20 * time.Second)
		for {
			monitor, err := qmp.NewSocketMonitor("unix", socket, 3*time.Second)
			if err == nil {
				err = monitor.Connect()
				if err == nil {
					monitor.Disconnect()
					break
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if time.Now().After(deadline) {
				return errors.New("Intel macOS display is not ready")
			}
			time.Sleep(50 * time.Millisecond)
		}
		temp, err := os.MkdirTemp(dir, ".login-frames-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temp)
		return serveLogin(ctx, dir, &qemuSurface{socket: socket, directory: temp})
	}
}

type qemuSurface struct {
	socket    string
	directory string
	mu        sync.Mutex
	enabled   bool
}

func (s *qemuSurface) Enable(value bool) { s.mu.Lock(); defer s.mu.Unlock(); s.enabled = value }

func (s *qemuSurface) command(name string, args any) error {
	data, err := json.Marshal(struct {
		Execute   string `json:"execute"`
		Arguments any    `json:"arguments,omitempty"`
	}{name, args})
	if err != nil {
		return err
	}
	monitor, err := qmp.NewSocketMonitor("unix", s.socket, 3*time.Second)
	if err != nil {
		return err
	}
	if err := monitor.Connect(); err != nil {
		return err
	}
	defer monitor.Disconnect()
	_, err = monitor.Run(data)
	return err
}

func (s *qemuSurface) Image(region []int) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return nil, errors.New("login view closed")
	}
	path := filepath.Join(s.directory, "scanout.ppm")
	defer os.Remove(path)
	if err := s.command("screendump", map[string]string{"filename": path}); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, errors.New("invalid QEMU login frame")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	config, err := netpbm.DecodeConfig(file)
	if err != nil || config.Width < 640 || config.Width > 4096 || config.Height < 400 || config.Height > 2560 {
		return nil, errors.New("unexpected guest scanout dimensions")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	frame, err := netpbm.Decode(io.LimitReader(file, 16<<20), nil)
	if err != nil {
		return nil, err
	}
	bounds := frame.Bounds()
	if bounds.Dx() < 640 || bounds.Dx() > 4096 || bounds.Dy() < 400 || bounds.Dy() > 2560 {
		return nil, errors.New("unexpected guest scanout dimensions")
	}
	output := image.NewRGBA(image.Rect(0, 0, 1024, 640))
	draw.ApproxBiLinear.Scale(output, output.Bounds(), frame, bounds, draw.Src, nil)
	var result image.Image = output
	if len(region) != 0 {
		if len(region) != 4 || region[0] < 0 || region[1] < 0 || region[2] <= 0 || region[3] <= 0 || region[0]+region[2] > 1024 || region[1]+region[3] > 640 {
			return nil, errors.New("invalid login frame region")
		}
		result = output.SubImage(image.Rect(region[0], region[1], region[0]+region[2], region[1]+region[3]))
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, result, &jpeg.Options{Quality: 92}); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (s *qemuSurface) events(events ...any) bool {
	return s.command("input-send-event", map[string]any{"events": events}) == nil
}
func qemuButton(button string, down bool) any {
	return map[string]any{"type": "btn", "data": map[string]any{"button": button, "down": down}}
}
func qemuKey(code string, down bool) any {
	return map[string]any{"type": "key", "data": map[string]any{"down": down, "key": map[string]string{"type": "qcode", "data": code}}}
}
func (s *qemuSurface) position(x, y int) bool {
	if x < 0 || x >= 1024 || y < 0 || y >= 640 {
		return false
	}
	return s.events(map[string]any{"type": "abs", "data": map[string]any{"axis": "x", "value": x * 32767 / 1024}}, map[string]any{"type": "abs", "data": map[string]any{"axis": "y", "value": y * 32767 / 640}})
}
func (s *qemuSurface) Click(x, y int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || !s.position(x, y) || !s.events(qemuButton("left", true)) {
		return false
	}
	time.Sleep(20 * time.Millisecond)
	return s.events(qemuButton("left", false))
}
func (s *qemuSurface) Key(code int, shift bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := qemuANSIKey(code)
	if !s.enabled || !ok {
		return false
	}
	return s.press(key, shift, false)
}
func (s *qemuSurface) press(key string, shift, command bool) bool {
	var down, up []any
	if shift {
		down = append(down, qemuKey("shift", true))
		up = append(up, qemuKey("shift", false))
	}
	if command {
		down = append(down, qemuKey("meta_l", true))
		up = append(up, qemuKey("meta_l", false))
	}
	down = append(down, qemuKey(key, true))
	up = append([]any{qemuKey(key, false)}, up...)
	if !s.events(down...) {
		s.events(up...)
		return false
	}
	time.Sleep(20 * time.Millisecond)
	return s.events(up...)
}
func (s *qemuSurface) SelectAll() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled && s.press("a", false, true)
}
func (s *qemuSurface) Scroll(x, y, dx, dy int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || dx < -512 || dx > 512 || dy < -512 || dy > 512 || !s.position(x, y) {
		return false
	}
	for _, axis := range []struct {
		value              int
		positive, negative string
	}{{dy, "wheel-down", "wheel-up"}, {dx, "wheel-right", "wheel-left"}} {
		value, button := axis.value, axis.positive
		if value < 0 {
			value = -value
			button = axis.negative
		}
		for count := 0; count < (value+31)/32; count++ {
			if !s.events(qemuButton(button, true), qemuButton(button, false)) {
				return false
			}
		}
	}
	return true
}

func qemuANSIKey(code int) (string, bool) {
	keys := map[int]string{0: "a", 1: "s", 2: "d", 3: "f", 4: "h", 5: "g", 6: "z", 7: "x", 8: "c", 9: "v", 11: "b", 12: "q", 13: "w", 14: "e", 15: "r", 16: "y", 17: "t", 18: "1", 19: "2", 20: "3", 21: "4", 22: "6", 23: "5", 24: "equal", 25: "9", 26: "7", 27: "minus", 28: "8", 29: "0", 30: "bracket_right", 31: "o", 32: "u", 33: "bracket_left", 34: "i", 35: "p", 36: "ret", 37: "l", 38: "j", 39: "apostrophe", 40: "k", 41: "semicolon", 42: "backslash", 43: "comma", 44: "slash", 45: "n", 46: "m", 47: "dot", 48: "tab", 49: "spc", 50: "grave_accent", 51: "backspace", 53: "esc", 116: "pgup", 121: "pgdn", 123: "left", 124: "right", 125: "down", 126: "up"}
	value, ok := keys[code]
	return value, ok
}
