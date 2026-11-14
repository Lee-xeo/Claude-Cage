package appupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/sylvinus/agent-vm/internal/hostproxy"
)

// Application maintenance follows host proxy settings first. The managed route
// is opened only after a host connection failure; HTTP errors retain their
// original meaning, including release absence and update-source rate limits.
func updateClient(configPath string) *http.Client {
	return updateHTTPClient(&updateTransport{
		host: hostproxy.NewTransport(), hostTimeout: 12 * time.Second,
		openManaged: func(ctx context.Context) (*http.Client, error) { return managedClient(ctx, configPath) },
	})
}

func updateHTTPClient(transport http.RoundTripper) *http.Client {
	return &http.Client{Transport: transport, Timeout: 20 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !allowedURL(r.URL, true) {
			return errors.New("更新下载重定向被拒绝")
		}
		return nil
	}}
}

type updateTransport struct {
	host        http.RoundTripper
	hostTimeout time.Duration
	openManaged func(context.Context) (*http.Client, error)
	once        sync.Once
	managed     *http.Client
	managedErr  error
}

func (t *updateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	// Bound connection and response-header time without limiting a healthy
	// archive transfer. Cancelling the caller must never start a second route.
	ctx, cancel := context.WithCancel(req.Context())
	timer := time.AfterFunc(t.hostTimeout, cancel)
	resp, err := t.host.RoundTrip(req.Clone(ctx))
	stopped := timer.Stop()
	if err == nil && stopped && ctx.Err() == nil {
		resp.Body = &routeBody{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	}
	cancel()
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	t.once.Do(func() { t.managed, t.managedErr = t.openManaged(req.Context()) })
	if t.managedErr != nil {
		return nil, &networkUnavailableError{t.managedErr}
	}
	return t.managed.Transport.RoundTrip(req)
}

func (t *updateTransport) CloseIdleConnections() {
	if closer, ok := t.host.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
	if t.managed != nil {
		t.managed.CloseIdleConnections()
	}
}

type routeBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

type networkUnavailableError struct{ cause error }

func (e *networkUnavailableError) Error() string {
	return "主机网络无法连接更新源，Cage 代理也不可用：" + e.cause.Error()
}
func (e *networkUnavailableError) Unwrap() error { return e.cause }

func (b *routeBody) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
}
