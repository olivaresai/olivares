// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
)

// authOutageStore fails the auth partition's reads the way the first request
// after a pool reset does: with the store's own unavailable sentinel and the
// driver's cause.
type authOutageStore struct{ store.Store }

func (authOutageStore) AuthView(context.Context, func(store.AuthScope) error) error {
	return fmt.Errorf("%w: %w", store.ErrStoreUnavailable, errors.New("conn closed"))
}

// the regression: the session tools endpoint and the apps gateway read a bearer
// through the authenticator like the REST API does, and told a client with a
// valid token that it was unauthenticated when the store could not be read. A
// store outage is a retryable 503; a token the store answered for as unknown is
// still a 401. The driver's text never reaches the client.
func TestBearerEndpointsAnswerAStoreOutageAs503NotAs401(t *testing.T) {
	healthy, _, _, _, _, _ := workSessionEdgePrincipals(t)
	down := auth.NewAuthenticator(authOutageStore{}, nil)
	// Well formed, so it reaches the store; no such token exists.
	token := auth.PrefixToken + "_selector_secret"
	settings := filepath.Join(t.TempDir(), "managed-settings.json")
	if err := os.WriteFile(settings, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	endpoints := []struct {
		name   string
		handle func(authr *auth.Authenticator) http.Handler
		method string
	}{
		{"session tools", func(a *auth.Authenticator) http.Handler {
			return &sessionMCPHandler{authr: a, issuedSessionOnly: true}
		}, http.MethodPost},
		{"apps gateway managed settings", func(a *auth.Authenticator) http.Handler {
			h := &appsGatewayHandler{authr: a, managedSettingsPath: settings}
			return http.HandlerFunc(h.handleManagedSettings)
		}, http.MethodGet},
		{"apps gateway spend limits", func(a *auth.Authenticator) http.Handler {
			h := &appsGatewayHandler{authr: a}
			return http.HandlerFunc(h.handleSpendLimits)
		}, http.MethodGet},
	}
	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			call := func(a *auth.Authenticator) *httptest.ResponseRecorder {
				r := httptest.NewRequest(ep.method, "/", nil)
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				ep.handle(a).ServeHTTP(w, r)
				return w
			}
			w := call(down)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("store outage: status = %d, want 503 (body %q)", w.Code, w.Body.String())
			}
			if body := w.Body.String(); strings.Contains(body, "conn closed") || strings.Contains(body, "unavailable:") {
				t.Fatalf("store outage: body %q leaks the driver's cause", body)
			}
			if w := call(healthy); w.Code != http.StatusUnauthorized {
				t.Fatalf("unknown token on a healthy store: status = %d, want 401 (body %q)", w.Code, w.Body.String())
			}
		})
	}
}
