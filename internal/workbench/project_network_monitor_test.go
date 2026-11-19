package workbench

import (
	"context"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

func TestCreateProjectDuringSlowPhysicalInterfaceProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, folder, _ := groupFixture(t)
		s.safety.VerifiedAt = time.Now()
		ctx, cancel := context.WithCancel(context.Background())
		const probeDuration = 24 * time.Second
		started := make(chan time.Time, 2)
		previous := physicalProxyInterface
		physicalProxyInterface = func(ctx context.Context) (string, error) {
			started <- time.Now()
			timer := time.NewTimer(probeDuration)
			defer timer.Stop()
			select {
			case <-timer.C:
				return "Ethernet", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			s.monitorNetwork(ctx)
		}()
		defer func() {
			cancel()
			<-stopped
			physicalProxyInterface = previous
		}()
		synctest.Wait()
		var first time.Time
		select {
		case first = <-started:
		default:
			t.Fatal("network monitor did not start its physical interface probe")
		}

		saveGroupRequest(t, s, projectGroup{Name: "slow-probe-project", Folders: []string{folder}}, http.StatusOK)
		response := projectOperationResponse(t, startProjectOperationRequest(t, s, ctx, "/api/projects/open",
			map[string]any{"workspace": folder, "add": false, "activate": true}))
		if response.Code != http.StatusOK || s.workspace() != folder {
			t.Fatalf("project open returned %d %s, workspace=%q", response.Code, response.Body.String(), s.workspace())
		}
		if elapsed := time.Since(first); elapsed >= probeDuration {
			t.Fatalf("project activation waited for the physical interface probe: %v", elapsed)
		}
		groups, err := s.projectGroups()
		if err != nil || len(groups) != 1 || groups[0].Workspace != folder {
			t.Fatalf("project groups = %+v, error = %v; want one saved project", groups, err)
		}

		select {
		case next := <-started:
			if elapsed := next.Sub(first); elapsed < probeDuration+5*time.Second {
				t.Fatalf("next probe started after %v, want at least 5s after the previous round", elapsed)
			}
		case <-time.After(probeDuration + 6*time.Second):
			t.Fatal("network monitor did not start the next probe")
		}
	})
}
