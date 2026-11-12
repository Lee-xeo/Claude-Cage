package workbench

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sylvinus/agent-vm/internal/remotehost"
)

// Replace only the HTTP socket with a pinned in-memory transport. Gateway
// admission, generation switching, reverse proxy cancellation, controller
// middleware, initialization waiting and durable writes all remain real.
type initializationGatewayTransport struct {
	target    *Server
	forwarded chan context.Context
	calls     int
}

func (p *initializationGatewayTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	p.calls++
	req := r.Clone(r.Context())
	req.Host = req.URL.Host
	req.Header.Del("Cookie")
	req.Header.Set("Origin", p.target.origin)
	req.AddCookie(&http.Cookie{Name: p.target.cookieName(), Value: p.target.session})
	req.AddCookie(&http.Cookie{Name: p.target.cookieName() + "-desktop", Value: trustedViewProof(p.target.token)})
	if p.forwarded != nil {
		p.forwarded <- req.Context()
	}
	w := httptest.NewRecorder()
	p.target.handler().ServeHTTP(w, req)
	response := w.Result()
	response.Request = req
	return response, nil
}

func initializationGatewayTarget(id string, s *Server, p *initializationGatewayTransport) *desktopTarget {
	ctx, cancel := context.WithCancel(context.Background())
	session := &remotehost.Session{
		Origin: s.origin, Context: ctx, Transport: p,
		Client:   &http.Client{Transport: p},
		Manifest: remotehost.PublicManifest{Platform: "windows", InstanceID: id},
	}
	return &desktopTarget{id: id, mode: "local", ctx: ctx, cancel: cancel, session: session}
}

func TestInitializationCompletionGatewaySwitchCancelsWaitingRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		oldController := initializationWaitServer(t)
		newController := initializationWaitServer(t)
		oldController.origin = "http://localhost:17778"
		newController.origin = "http://localhost:17779"
		oldBefore := initializationWaitRecord(t, oldController)
		newBefore := initializationWaitRecord(t, newController)
		oldTransport := &initializationGatewayTransport{target: oldController, forwarded: make(chan context.Context, 1)}
		newTransport := &initializationGatewayTransport{target: newController}
		oldTarget := initializationGatewayTarget("old-initialization-view", oldController, oldTransport)
		newTarget := initializationGatewayTarget("new-initialization-view", newController, newTransport)
		defer oldTarget.close()
		defer newTarget.close()
		gateway := &desktopGateway{generation: oldTarget.id, active: oldTarget, pending: newTarget}
		shell := testServer(t)
		shell.remote = &remoteGateway{desktop: gateway}
		request := func(generation string) *http.Request {
			r := httptest.NewRequest(http.MethodPost, shell.origin+"/api/environment/initialization", strings.NewReader(`{"action":"complete"}`))
			r.Header.Set("Origin", shell.origin)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set(connectionHeader, generation)
			r.AddCookie(&http.Cookie{Name: shell.cookieName(), Value: shell.session})
			return r
		}

		oldController.op.Lock()
		locked := true
		defer func() {
			if locked {
				oldController.op.Unlock()
			}
		}()
		done := initializationWaitStart(shell, request(oldTarget.id))
		forwarded := <-oldTransport.forwarded
		initializationWaitPending(t, done)
		time.Sleep(6 * time.Second)
		initializationWaitPending(t, done)
		if err := gateway.commit(context.Background(), switchRequest{ConnectionID: oldTarget.id, CandidateID: newTarget.id}); err != nil {
			t.Fatal(err)
		}
		w := initializationWaitResult(t, done)
		if w.Code < 400 || forwarded.Err() == nil || oldTarget.ctx.Err() == nil {
			t.Fatalf("switch failed to revoke the waiting request: status=%d forwarded=%v target=%v", w.Code, forwarded.Err(), oldTarget.ctx.Err())
		}
		if gateway.context().ConnectionID != newTarget.id || oldTransport.calls != 1 || newTransport.calls != 0 {
			t.Fatalf("old request was redirected or replayed: generation=%s old_calls=%d new_calls=%d", gateway.context().ConnectionID, oldTransport.calls, newTransport.calls)
		}
		initializationWaitUnchanged(t, oldController, oldBefore)
		initializationWaitUnchanged(t, newController, newBefore)
		oldController.op.Unlock()
		locked = false
		time.Sleep(20 * time.Second)
		synctest.Wait()
		initializationWaitUnchanged(t, oldController, oldBefore)
		initializationWaitUnchanged(t, newController, newBefore)
		if !oldController.op.TryLock() {
			t.Fatal("switched request left a detached operation-lock holder")
		}
		oldController.op.Unlock()

		// The stale generation stays rejected; only a fresh request explicitly
		// addressed to the new generation can complete the new controller.
		stale := httptest.NewRecorder()
		shell.handler().ServeHTTP(stale, request(oldTarget.id))
		if stale.Code != http.StatusConflict || oldTransport.calls != 1 || newTransport.calls != 0 {
			t.Fatalf("stale generation reached a controller: %d", stale.Code)
		}
		fresh := httptest.NewRecorder()
		shell.handler().ServeHTTP(fresh, request(newTarget.id))
		if fresh.Code != http.StatusOK || !strings.Contains(fresh.Body.String(), `"completed":true`) || newTransport.calls != 1 {
			t.Fatalf("fresh selected request failed: %d %s", fresh.Code, fresh.Body.String())
		}
		initializationWaitUnchanged(t, oldController, oldBefore)
	})
}
