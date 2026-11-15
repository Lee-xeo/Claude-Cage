package workbench

import (
	"net/http"

	"github.com/sylvinus/agent-vm/internal/appupdate"
	"github.com/sylvinus/agent-vm/internal/version"
)

func (s *Server) appAbout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonReply(w, version.CurrentBuild())
		return
	}
	path := s.appUpdateConfigPath
	if path == "" {
		path = s.configPath
	}
	jsonReply(w, appupdate.CheckAbout(r.Context(), path))
}
