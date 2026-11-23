package workbench

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestStatusReportsPausedAndClosedNetwork(t *testing.T) {
	s, _ := grantedToolUpdateServer(t)
	s.setJobForTest(Job{})
	read := func() (paused, open bool) {
		t.Helper()
		response := httptest.NewRecorder()
		s.status(response, httptest.NewRequest("GET", "/api/status", nil))
		var status struct {
			Paused *bool `json:"network_paused"`
			Open   *bool `json:"network_open"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || status.Paused == nil || status.Open == nil {
			t.Fatalf("status lacks network fields: %s", response.Body.String())
		}
		return *status.Paused, *status.Open
	}
	if paused, open := read(); paused || !open {
		t.Fatalf("granted gate: paused=%v open=%v", paused, open)
	}
	s.networkPaused = true
	if paused, open := read(); !paused || open {
		t.Fatalf("paused network: paused=%v open=%v", paused, open)
	}
	s.networkPaused = false
	s.proxyNetworkLost = true
	if paused, open := read(); paused || open {
		t.Fatalf("lost proxy network: paused=%v open=%v", paused, open)
	}
	s.proxyNetworkLost = false
	s.safety.Gate = nil
	if _, open := read(); open {
		t.Fatal("missing gate reported open")
	}
}
