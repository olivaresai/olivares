// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

type holdRoundTrip func(*http.Request) (*http.Response, error)

func (f holdRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// holdPreviewRequest sends one request through ServePreviewHTTP to an app that
// answers at once and then streams a body that stays open until end is called,
// as a dev server's live-reload stream does, and returns once the response has
// begun. The preview URL expires after ttl. end is idempotent and also runs at
// test cleanup.
func holdPreviewRequest(t *testing.T, m *Module, lr *liveRun, ttl time.Duration) (end func() *httptest.ResponseRecorder) {
	t.Helper()
	now := m.now()
	entered := make(chan struct{})
	body, stream := io.Pipe()
	grant := &previewGrant{tenant: lr.tenant, runRef: lr.runRef, proc: lr.proc, port: 5173,
		expires: now.Add(ttl), address: "127.0.0.1:5173", checked: now}
	grant.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) { pr.Out.URL.Scheme, pr.Out.URL.Host = "http", pr.In.Host },
		Transport: holdRoundTrip(func(*http.Request) (*http.Response, error) {
			close(entered)
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body, ContentLength: -1}, nil
		}),
	}
	token := model.NewID().String()
	if !m.previews.put(token, grant, now, m.previewLive) {
		t.Fatal("preview grant refused")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, api.SessionPreviewPathPrefix+token+"/index.html", nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.ServePreviewHTTP(rec, req)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the preview request never reached the app")
	}
	var once sync.Once
	end = func() *httptest.ResponseRecorder {
		once.Do(func() { _ = stream.Close() })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the preview request did not end")
		}
		return rec
	}
	t.Cleanup(func() { end() })
	return end
}
