// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"cmp"
	"io"
	"net/http"
	"time"
)

// clientStallTimeout is how long an API route waits on a client that stops
// sending its request body or stops reading the reply. It bounds the gap between
// two successive reads or two successive writes, never the whole transfer: a
// 10 GiB restore upload is bounded by its size cap, and a session launch that
// waits minutes on git and a driver handshake by its own budgets. The value and
// the meaning are nginx's defaults for the same two limits (client_body_timeout
// 60s, send_timeout 60s):
// https://nginx.org/en/docs/http/ngx_http_core_module.html#client_body_timeout
// https://nginx.org/en/docs/http/ngx_http_core_module.html#send_timeout
const clientStallTimeout = 60 * time.Second

// stallDeadlines replaces, for every API route, the whole-request deadlines
// NewHTTPServer sets on the connection: net/http arms WriteTimeout when the
// request headers arrive and ReadTimeout covers the whole body, so a slow upload
// was cut with 408 and a reply written after 60 s was lost while the server
// logged it as sent. Here the read deadline moves before each body read and the
// write deadline before each write; the handler's own work runs unbounded by the
// transport. A route that sets its own write deadline (a held publication, an SSE
// stream) keeps it. ReadHeaderTimeout stays the slowloris guard.
func (s *Server) stallDeadlines(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		// Lift the write deadline armed with the request headers; each write below
		// arms its own. A writer without socket deadlines (httptest) is passed through.
		if rc.SetWriteDeadline(time.Time{}) != nil {
			next.ServeHTTP(w, r)
			return
		}
		stall := cmp.Or(s.stall, clientStallTimeout)
		sw := &stallWriter{ResponseWriter: w, rc: rc, stall: stall}
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = &stallBody{ReadCloser: r.Body, rc: rc, stall: stall}
		}
		// net/http flushes the buffered reply after the handler returns.
		defer sw.arm()
		next.ServeHTTP(sw, r)
	})
}

// stallBody moves the connection's read deadline before each read of the body.
type stallBody struct {
	io.ReadCloser
	rc    *http.ResponseController
	stall time.Duration
	ended bool
}

func (b *stallBody) Read(p []byte) (int, error) {
	// Never after the read that ended the body: net/http then reads the connection
	// in the background to notice the client leaving, and a deadline on that read
	// would cancel the request's context. A failed move keeps the server's
	// ReadTimeout, the bound before this change.
	if !b.ended {
		_ = b.rc.SetReadDeadline(time.Now().Add(b.stall))
	}
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.ended = true
	}
	return n, err
}

// stallWriter moves the connection's write deadline before each write and flush,
// until the route sets one itself.
type stallWriter struct {
	http.ResponseWriter
	rc    *http.ResponseController
	stall time.Duration
	owned bool
}

func (w *stallWriter) arm() {
	if !w.owned {
		_ = w.rc.SetWriteDeadline(time.Now().Add(w.stall))
	}
}

func (w *stallWriter) Write(p []byte) (int, error) {
	w.arm()
	return w.ResponseWriter.Write(p)
}

// FlushError and SetWriteDeadline are what http.ResponseController finds on its
// way down the Unwrap chain, so a route's flush and its own deadline pass here.
func (w *stallWriter) FlushError() error {
	w.arm()
	return w.rc.Flush()
}

func (w *stallWriter) SetWriteDeadline(deadline time.Time) error {
	w.owned = true
	return w.rc.SetWriteDeadline(deadline)
}

func (w *stallWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
