// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
)

// outageAuthStore fails the auth partition's reads with err. The api_test
// package has its own fake of this shape (authBlindStore); this test needs the
// unexported Server, so it lives in package api.
type outageAuthStore struct {
	store.Store
	err error
}

func (s outageAuthStore) AuthView(context.Context, func(store.AuthScope) error) error { return s.err }

// the regression: a bearer whose credential could not be checked because the store was
// unreachable is a retryable 503 on REST and Unavailable on gRPC, not the 401
// "unauthenticated" that tells a signed-in client to sign in again. This is the
// leg that runs without PostgreSQL; poolreset_pg_test.go shows the same answer
// on a real pool reset. The downstream handler is never reached (deny closed),
// no cookie is touched, and the driver's text never reaches the client.
func TestAuthenticateAnswersAStoreOutageAsUnavailable(t *testing.T) {
	down := fmt.Errorf("begin: %w: %w", store.ErrStoreUnavailable, errors.New("conn closed"))
	s := &Server{
		authr: auth.NewAuthenticator(outageAuthStore{err: down}, nil),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	// Well formed, so it reaches the store.
	bearer := "Bearer " + auth.PrefixToken + "_selector_secret"

	t.Run("REST", func(t *testing.T) {
		reached := false
		h := s.authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
		r := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
		r.Header.Set("Authorization", bearer)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (body %q)", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"store_unavailable"`) {
			t.Fatalf("body %q does not carry the store_unavailable code", w.Body.String())
		}
		if strings.Contains(w.Body.String(), "conn closed") {
			t.Fatalf("body %q leaks the driver's cause", w.Body.String())
		}
		if reached {
			t.Fatal("the handler ran for a credential that could not be checked")
		}
		if got := w.Header().Values("Set-Cookie"); len(got) != 0 {
			t.Fatalf("Set-Cookie = %q, an outage must not clear the client's session cookie", got)
		}
	})

	t.Run("gRPC", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", bearer))
		_, err := s.grpcAuthenticate(ctx)
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("code = %v (%v), want Unavailable", status.Code(err), err)
		}
		if strings.Contains(err.Error(), "conn closed") {
			t.Fatalf("error %q leaks the driver's cause", err)
		}
	})
}
