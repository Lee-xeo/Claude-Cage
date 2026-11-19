package workbench

import "net/http"

// The Linux guest shares the system clipboard, so a web copy never needs to be
// fetched through this route. It still validates the request it declines.
func (s *Server) browserClipboard(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID       string `json:"id"`
		Tab      string `json:"tab"`
		Context  string `json:"context"`
		Document string `json:"document"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if !browserFileID.MatchString(input.ID) || !validInputToken(input.Tab) || !validInputToken(input.Context) || !validInputToken(input.Document) {
		http.Error(w, "复制内容或网页上下文无效", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	ready := s.ready
	s.mu.Unlock()
	if !ready {
		http.Error(w, "环境尚未就绪", http.StatusConflict)
		return
	}
	http.Error(w, "当前客机使用系统共享剪贴板", http.StatusNotImplemented)
}
