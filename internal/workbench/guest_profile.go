package workbench

import (
	"errors"
	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/guestprofile"
	"net/http"
	"strings"
)

func (s *Server) linuxTemplate(script string) (string, error) {
	if s.macosGuest != nil || s.windowsGuest != nil {
		return script, nil
	}
	p, err := guestprofile.Load(s.profile)
	if err != nil {
		return "", err
	}
	return p.Linux(script), nil
}

func (s *Server) linuxPathHasPrefix(path, prefix string) bool {
	rendered, err := s.linuxTemplate(prefix)
	return err == nil && strings.HasPrefix(path, rendered)
}

func (s *Server) guestProfileOptions(w http.ResponseWriter, r *http.Request) {
	if !s.hasTrustedView(r) {
		http.Error(w, "请在已信任的 Cage 窗口配置客机", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost {
		var options guestprofile.Options
		if !readJSON(w, r, &options) {
			return
		}
		p, err := guestprofile.New(options)
		if err != nil {
			requestError(w, err)
			return
		}
		jsonReply(w, p.Options)
		return
	}
	p, err := guestprofile.Load(s.profile)
	if err != nil {
		requestError(w, err)
		return
	}
	location, err := cage.GetStorageLocation(s.profile)
	if err != nil {
		requestError(w, err)
		return
	}
	jsonReply(w, struct {
		Profile  *guestprofile.Profile `json:"profile"`
		Editable bool                  `json:"editable"`
	}{p, p == nil && !location.HasEnvironment && !s.busy()})
}

// Called inside the installation operation lock, before any disk is created.
// Legacy environments retain their accounts and settings on ordinary upgrades.
func (s *Server) prepareGuestProfile(options *guestprofile.Options) error {
	if options != nil {
		if err := options.Validate(); err != nil {
			return err
		}
	}
	p, err := guestprofile.Load(s.profile)
	if err != nil {
		return err
	}
	if p != nil {
		if options != nil && !options.Matches(p.Options) {
			return errors.New("创建配置已保存，请继续原安装；已有账户和磁盘不会被改名")
		}
		return nil
	}
	location, err := cage.GetStorageLocation(s.profile)
	if err != nil {
		return err
	}
	if location.HasEnvironment {
		if options != nil {
			return errors.New("已有环境不能直接更换创建配置，请创建独立环境")
		}
		return nil
	}
	o := guestprofile.Options{}
	if options != nil {
		o = *options
	}
	_, err = guestprofile.Create(s.profile, o)
	return err
}
