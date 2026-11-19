package managedproxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
)

func nodeUser(id string) string { return "node-" + id }

func (s Store) nodePassword(id string) string {
	mac := hmac.New(sha256.New, []byte(s.Auth))
	mac.Write([]byte(nodeUser(id)))
	return hex.EncodeToString(mac.Sum(nil))
}

// NodeEndpoint uses the same private core as the environment. Each credential
// matches one outbound node, and cannot select the global proxy group.
func (s Store) NodeEndpoint(id string) (string, error) {
	if _, ok := s.Find(id); !ok {
		return "", errors.New("账号绑定的代理节点已移除，请重新选择")
	}
	return fmt.Sprintf("socks5://%s@127.0.0.1:%d", url.UserPassword(nodeUser(id), s.nodePassword(id)), s.Port), nil
}
