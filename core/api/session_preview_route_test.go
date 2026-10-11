// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

// The preview prefix is authorized by its token alone: the previewed app's own
// Authorization header must reach it, the token never reaches a log line, and
// neighbouring paths keep engine authentication.
func TestSessionPreviewPrefixIsTokenAuthorizedAndUnlogged(t *testing.T) {
	calls := 0
	var logs bytes.Buffer
	h := newHarnessOpts(t, func(o *api.Options) {
		o.SessionPreview = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusTeapot) })
		o.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	h.adminLogin()
	const token = "PREVIEW-TOKEN-SECRET"
	if got := h.do("GET", api.SessionPreviewPathPrefix+token+"/x", "the-apps-own-bearer", nil, nil); got.code != http.StatusTeapot || calls != 1 {
		t.Fatalf("preview = %d, calls = %d", got.code, calls)
	}
	if strings.Contains(logs.String(), token) {
		t.Fatalf("a log line holds the preview token:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), api.SessionPreviewPathPrefix) {
		t.Fatalf("the preview request was not logged:\n%s", logs.String())
	}
	for _, refused := range []string{"/session/preview", "/session/previews/x", "/session/mcp/preview/x"} {
		if got := h.do("GET", refused, "the-apps-own-bearer", nil, nil); got.code != http.StatusUnauthorized || calls != 1 {
			t.Fatalf("%s = %d, calls = %d", refused, got.code, calls)
		}
	}
}

// A proxied response that breaks mid-body must reach the client as a broken
// connection, never as a complete body with a 500 error appended to it. The
// aborted request still leaves the in-flight gauge and gets its access-log line.
func TestSessionPreviewAbortedStreamIsNotCompleted(t *testing.T) {
	var logs bytes.Buffer
	h := newHarnessOpts(t, func(o *api.Options) {
		o.SessionPreview = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "PARTIAL")
			_ = http.NewResponseController(w).Flush()
			panic(http.ErrAbortHandler) // what httputil.ReverseProxy does when the upstream body breaks
		})
		o.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	h.adminLogin() // past first-run setup, which would answer before the route
	inFlight := func() string {
		for _, line := range strings.Split(h.do("GET", "/metrics", "", nil, nil).raw, "\n") {
			if strings.HasPrefix(line, "olivares_http_requests_in_flight ") {
				return line
			}
		}
		t.Fatal("/metrics has no olivares_http_requests_in_flight")
		return ""
	}
	before := inFlight()
	t.Cleanup(func() {
		if after := inFlight(); after != before {
			t.Errorf("the aborted request stayed in flight: before %q, after %q", before, after)
		}
		if !strings.Contains(logs.String(), "path="+api.SessionPreviewPathPrefix) {
			t.Errorf("the aborted request has no access-log line:\n%s", logs.String())
		}
	})
	srv := httptest.NewServer(h.srv.Handler())
	t.Cleanup(srv.Close)
	resp, err := srv.Client().Get(srv.URL + api.SessionPreviewPathPrefix + "tok/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err == nil || strings.Contains(string(body), "internal") {
		t.Fatalf("aborted stream read as complete: err=%v body=%q", err, body)
	}
}
