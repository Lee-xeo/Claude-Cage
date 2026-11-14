package workbench

import (
	"context"
	"github.com/sylvinus/agent-vm/internal/guestprofile"
)

func (s *Server) syncNativeBrowserProfile(ctx context.Context) error {
	p, err := guestprofile.Load(s.profile)
	if err != nil || p == nil {
		return err
	}
	input := map[string]any{"browser_width": p.BrowserWidth, "browser_height": p.BrowserHeight, "webgl": p.WebGL, "canvas": p.Canvas}
	if p.GraphicsBackend != "" {
		input["graphics_backend"] = p.GraphicsBackend
	}
	_, err = s.nativeBrowserClient().request(ctx, "POST", "/profile", input)
	return err
}
