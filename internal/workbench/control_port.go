package workbench

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

const (
	controllerFirstPort = 17777
	controllerPortCount = 31
)

type controllerPortRecord struct {
	Port int `json:"port"`
}

// listenController keeps the workbench origin stable, because the origin
// scopes its cookies and browser storage. Windows can reserve whole port
// blocks for Hyper-V, WSL or container networking, which may cover every
// preferred port. The system then assigns a free port. It is remembered, so
// later starts keep that origin while it stays available.
func listenController(profile string) (net.Listener, int, error) {
	for port := controllerFirstPort; port < controllerFirstPort+controllerPortCount; port++ {
		if listener, err := listenLoopback(port); err == nil {
			return listener, port, nil
		}
	}
	path := filepath.Join(profile, "control-port.json")
	var record controllerPortRecord
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &record) == nil && record.Port >= 1024 && record.Port <= 65535 {
		if listener, err := listenLoopback(record.Port); err == nil {
			return listener, record.Port, nil
		}
	}
	listener, err := listenLoopback(0)
	if err != nil {
		return nil, 0, fmt.Errorf("无法在本机回环地址上监听工作台端口: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if port < 1024 {
		listener.Close()
		return nil, 0, fmt.Errorf("系统分配的工作台端口 %d 无效", port)
	}
	// Losing the record only costs the stable origin, not the start.
	_ = writePrivate(path, controllerPortRecord{Port: port})
	return listener, port, nil
}

func listenLoopback(port int) (net.Listener, error) {
	return net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
}
