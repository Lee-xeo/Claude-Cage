package workbench

import (
	"errors"
	"net/http"

	"github.com/sylvinus/agent-vm/internal/proxyimport"
)

var discoverProxyConfigs = proxyimport.Discover

func (s *Server) proxyLocalConfigs(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalConfigAccess(w, r) {
		return
	}
	configs, err := discoverProxyConfigs([]byte(s.token))
	if err != nil {
		requestError(w, err)
		return
	}
	jsonReply(w, map[string]any{"configs": configs})
}

func (s *Server) requireLocalConfigAccess(w http.ResponseWriter, r *http.Request) bool {
	if !s.hasTrustedView(r) {
		http.Error(w, "请在这台电脑的 Cage 窗口中导入本机代理配置；其他连接可手动上传配置文件", http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) localProxyConfig(id string) (proxyInput, error) {
	configs, err := discoverProxyConfigs([]byte(s.token))
	if err != nil {
		return proxyInput{}, err
	}
	for _, config := range configs {
		if config.ID != id {
			continue
		}
		if config.Error != "" {
			return proxyInput{}, errors.New(config.Error)
		}
		return proxyInput{ID: config.SourceID, Name: config.Client + " · " + config.Name, URL: config.URL, Content: string(config.Data)}, nil
	}
	return proxyInput{}, errors.New("这份配置已经更新或不在原位置，请点击“重新检测”后再导入")
}
