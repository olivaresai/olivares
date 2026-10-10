// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// The console's dictation runs speech recognition in a worker started from /assets/. A
// worker takes its policy from its own response, not from the document, so an asset response
// without one would leave the worker free to send audio anywhere. Every asset response
// therefore confines a worker to the engine's origin, and the document allows such workers.
func TestWorkersStayOnTheEngineOrigin(t *testing.T) {
	if got, ok := directiveValue(buildCSP("n"), "worker-src"); !ok || got != "'self'" {
		t.Errorf("document worker-src = %q (named %v), want 'self'", got, ok)
	}

	rr := do(newSPAHandler(sentinelAPI(), testWebFS()), http.MethodGet, "/assets/app-abc123.js")
	if rr.Code != http.StatusOK {
		t.Fatalf("asset status = %d", rr.Code)
	}
	csp := rr.Header().Get("Content-Security-Policy")
	for directive, want := range map[string]string{
		"default-src": "'self'",
		"connect-src": "'self'",
		"script-src":  "'self' 'wasm-unsafe-eval'",
		"object-src":  "'none'",
		"base-uri":    "'none'",
	} {
		if got, ok := directiveValue(csp, directive); !ok || got != want {
			t.Errorf("asset CSP %s = %q (named %v), want %q; whole policy %q", directive, got, ok, want, csp)
		}
	}
	if strings.Contains(csp, "blob:") || strings.Contains(csp, "data:") || strings.Contains(csp, "*") {
		t.Errorf("asset CSP widens a worker beyond the engine origin: %q", csp)
	}
}

// The dictation model and runtime are tens of MB of barely compressible bytes. An asset that
// large streams from the embedded bundle as it is: copied into memory and gzipped, as the
// small assets are, one press of the microphone left the engine 288 MB larger (measured on
// 2026-10-09: 214 MB before, 503 MB after).
func TestLargeAssetsStreamFromTheBundle(t *testing.T) {
	big := bytes.Repeat([]byte{7}, maxCachedAsset+1)
	fsys := testWebFS()
	fsys["assets/voice/rev/model.onnx"] = &fstest.MapFile{Data: big}
	s := &spaServer{fsys: fsys}

	r := httptest.NewRequest(http.MethodGet, "/assets/voice/rev/model.onnx", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	s.serve(rr, r)
	if rr.Code != http.StatusOK || !bytes.Equal(rr.Body.Bytes(), big) {
		t.Fatalf("status %d, %d bytes; want 200 and the file", rr.Code, rr.Body.Len())
	}
	h := rr.Header()
	if h.Get("Content-Encoding") != "" || h.Get("Content-Length") != strconv.Itoa(len(big)) {
		t.Errorf("Content-Encoding %q, Content-Length %q; want identity, %d", h.Get("Content-Encoding"), h.Get("Content-Length"), len(big))
	}
	if h.Get("Cache-Control") != "public, max-age=31536000, immutable" || h.Get("Content-Security-Policy") != assetCSP || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers lost: %v", h)
	}
	if _, cached := s.assets.Load("assets/voice/rev/model.onnx"); cached {
		t.Error("a large asset was copied into the in-memory asset cache")
	}

	r = httptest.NewRequest(http.MethodGet, "/assets/voice/rev/model.onnx", nil)
	r.Header.Set("Range", "bytes=0-9")
	rr = httptest.NewRecorder()
	s.serve(rr, r)
	if rr.Code != http.StatusPartialContent || rr.Body.Len() != 10 {
		t.Errorf("range: status %d, %d bytes; want 206 and 10", rr.Code, rr.Body.Len())
	}

	// Small assets keep the cached, compressed path.
	r = httptest.NewRequest(http.MethodGet, "/assets/app-abc123.js", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	s.serve(httptest.NewRecorder(), r)
	if _, cached := s.assets.Load("assets/app-abc123.js"); !cached {
		t.Error("a small asset left the in-memory cache")
	}
}
