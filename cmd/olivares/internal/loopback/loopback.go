// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package loopback holds what every in-process call to the engine's own API
// handler shares: the request context rule and the response recorder.
package loopback

import (
	"bytes"
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
)

// Context strips chi's routing state from ctx so an IN-PROCESS
// loopback request (approval bridge, deploy identity/drift binder, HITL
// decider — the Recorder mechanism) routes FRESH on the engine handler.
//
// Why this exists: chi v5's Mux.ServeHTTP treats a request whose
// context already carries a RouteContext as a SUBROUTER continuation and
// routes with the LEFTOVER state of the outer match. Every loopback caller
// passes the live request context (correct — it must inherit cancellation and
// deadline), so a loopback issued from INSIDE a chi-served handler — a
// schedule/workflow fire opening its approval, a hook-PEP gateOnce, an admin
// action gate — inherited the outer route's consumed RoutePath and 404'd on
// the engine handler. Tests that called the gates with context.Background()
// never saw it; the workflow-run E2E (phase 1 through the real HTTP surface)
// did. Shadowing the key with a nil value makes chi's type assertion fail, so
// the mux allocates a fresh routing context while every other context value,
// cancel and deadline still applies.
//
// It ALSO strips the engine's request-scoped authority (api.DetachRequestContext):
// a loopback presents its own service credential in the Authorization header and
// must never fall back on the outer request's principal. That is defense in depth
// rather than a live fix — every current caller does set the header, and the
// authenticate middleware overwrites the principal from it — but that middleware
// passes an unauthenticated request through untouched, so a future loopback that
// forgot its credential would otherwise execute as whoever made the outer
// request. The seam is the right place to make that impossible.
func Context(ctx context.Context) context.Context {
	ctx = api.DetachRequestContext(ctx)
	if ctx.Value(chi.RouteCtxKey) == nil {
		return ctx
	}
	return context.WithValue(ctx, chi.RouteCtxKey, nil)
}

// Recorder is a minimal http.ResponseWriter that records the status and body of an
// in-process API call (so the caller need not open a socket to its own engine).
// Build it with NewRecorder.
type Recorder struct {
	header http.Header
	Status int
	Body   bytes.Buffer
	wrote  bool
}

// NewRecorder returns a Recorder whose status is 200 until the handler sets one.
func NewRecorder() *Recorder { return &Recorder{header: http.Header{}, Status: http.StatusOK} }

func (c *Recorder) Header() http.Header { return c.header }

func (c *Recorder) WriteHeader(status int) {
	if !c.wrote {
		c.Status = status
		c.wrote = true
	}
}

func (c *Recorder) Write(b []byte) (int, error) {
	c.wrote = true
	return c.Body.Write(b)
}
