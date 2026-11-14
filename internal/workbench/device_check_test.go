package workbench

import (
	"encoding/json"
	"github.com/sylvinus/agent-vm/internal/guestprofile"
	"net/http/httptest"
	"strings"
	"testing"
)

func sampleDevice() *deviceObservation {
	v := &deviceObservation{UserAgent: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/142.0.0.0 Safari/537.36", Platform: "Linux x86_64"}
	v.Canvas.State = "passed"
	v.Canvas.Checksum = "1234abcd"
	v.WebGL.State = "passed"
	v.WebGL.Renderer = "ANGLE (Intel, Intel UHD Graphics, OpenGL)"
	v.WebGL.Version = "WebGL 2.0"
	v.Audio.State = "permission_required"
	return v
}
func checkState(items []deviceCheckItem, id string) string {
	for _, item := range items {
		if item.ID == id {
			return item.State
		}
	}
	return "missing"
}

func TestGraphicsEvidenceDistinguishesFallback(t *testing.T) {
	p, err := guestprofile.New(guestprofile.Options{GraphicsBackend: "opengl"})
	if err != nil {
		t.Fatal(err)
	}
	v := sampleDevice()
	items, class := evaluateDevice(p, v)
	if class != "driver-reported" || checkState(items, "webgl-render") != "passed" {
		t.Fatal("valid render sample rejected")
	}
	v.WebGL.Renderer = "llvmpipe (LLVM 18)"
	items, class = evaluateDevice(p, v)
	if class != "software" || checkState(items, "graphics-backend") != "failed" {
		t.Fatal("software fallback accepted as requested hardware")
	}
	p.GraphicsBackend = "auto"
	items, _ = evaluateDevice(p, v)
	if checkState(items, "graphics-backend") != "warning" {
		t.Fatal("automatic fallback was hidden")
	}
	v.WebGL.Renderer = "Apple Paravirtual device"
	items, class = evaluateDevice(p, v)
	if class != "virtual-gpu" || checkState(items, "webgl-render") != "passed" {
		t.Fatal("virtual device was not classified truthfully")
	}
	v.WebGL.State = "failed"
	items, _ = evaluateDevice(p, v)
	if checkState(items, "webgl-render") != "failed" {
		t.Fatal("incorrect pixels passed")
	}
	items, class = evaluateDevice(p, nil)
	if class != "unobserved" || checkState(items, "browser-evidence") != "unknown" {
		t.Fatal("missing browser evidence passed")
	}
}

func TestPermissionAndMalformedEvidenceNeverPass(t *testing.T) {
	v := sampleDevice()
	items, _ := evaluateDevice(nil, v)
	if checkState(items, "browser-audio") != "unknown" || checkState(items, "user-agent") != "unknown" {
		t.Fatal("unavailable audio/UA evidence passed")
	}
	v.WebGL.Renderer = strings.Repeat("x", 513)
	items, _ = evaluateDevice(nil, v)
	if checkState(items, "browser-evidence") != "unknown" {
		t.Fatal("oversize untrusted evidence accepted")
	}
	s := &Server{profile: t.TempDir()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "http://localhost/api/environment/device-check", strings.NewReader(`{"target":"browser"}`))
	s.deviceCheck(w, r)
	if w.Code != 403 {
		t.Fatal("untrusted window collected guest device evidence")
	}
}

func TestDeviceChecksSavedOptionsAndIdentityConsistency(t *testing.T) {
	p, err := guestprofile.New(guestprofile.Options{WebGL: "disabled", Canvas: "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	v := sampleDevice()
	if err = json.Unmarshal([]byte(`{"brands":[{"brand":"Chromium","version":"142"}],"platform":"Linux"}`), &v.ClientHints); err != nil {
		t.Fatal(err)
	}
	items, _ := evaluateDevice(p, v)
	if checkState(items, "user-agent") != "passed" || checkState(items, "webgl-render") != "failed" || checkState(items, "canvas-render") != "failed" {
		t.Fatal("saved privacy settings were not checked against actual capability")
	}
	v.WebGL.State = "unavailable"
	v.WebGL.Renderer = ""
	v.Canvas.State = "blocked"
	v.Canvas.Checksum = ""
	items, _ = evaluateDevice(p, v)
	if checkState(items, "webgl-render") != "passed" || checkState(items, "canvas-render") != "passed" {
		t.Fatal("matching privacy settings rejected")
	}
	v.ClientHints.Platform = "Windows"
	items, _ = evaluateDevice(p, v)
	if checkState(items, "user-agent") != "failed" {
		t.Fatal("inconsistent browser OS passed")
	}
	v.ClientHints.Platform = "Linux"
	v.ClientHints.Brands[0].Version = "141"
	items, _ = evaluateDevice(p, v)
	if checkState(items, "user-agent") != "failed" {
		t.Fatal("inconsistent browser version passed")
	}
}
