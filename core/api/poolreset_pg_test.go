// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/olivaresai/olivares/core/api/genpb/apiv1"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/store"
)

// the regression: the status a client sees on the first request after the engine's
// PostgreSQL pool is reset. The pool's connections are cut with no notice and
// the request that lands on one is authenticated first, so the answer is the
// authentication middleware's before it is any handler's.
//
// Measured on PostgreSQL 16.15 before this change: 401 unauthenticated. The
// middleware collapsed every Authenticate error into a bad credential, so a
// signed-in client was told to sign in again about a database it could not
// reach. After it: 503 store_unavailable on REST and Unavailable on gRPC, the
// request after it succeeds on a fresh connection, and a body never carries the
// driver's text.
func TestPostgresPoolResetFirstRequestAnswers503(t *testing.T) {
	const rounds = 20
	if !pgtest.Available(t) {
		t.Skip("no Postgres configured: set OLIVARES_TEST_POSTGRES_SUPERUSER_DSN to run the pool-reset leg")
	}
	dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SingleRole)
	proxy := pgtest.NewSeverProxy(t, dsns.App)
	// MaxConns 1: the cut connection is the one the next request checks out.
	st := openLeaderPostgres(t, store.Config{Engine: store.EnginePostgres, DSN: proxy.DSN(), MaxConns: 1})
	h := newHarnessOptsFromStoreSource(t, harnessStoreSource{borrowed: st}, nil)
	sr := h.do(http.MethodPost, "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	if sr.code != http.StatusCreated {
		t.Fatalf("POST /v1/setup = %d %s", sr.code, sr.raw)
	}
	login := h.do(http.MethodPost, "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	token, _ := login.body["token"].(string)
	org, _ := sr.body["organization"].(map[string]any)
	tenant, _ := org["tenant_id"].(string)
	if login.code != http.StatusOK || token == "" || tenant == "" {
		t.Fatalf("sign-in = %d %s (tenant %q)", login.code, login.raw, tenant)
	}

	// leaks is what a client must never read: the driver's own words.
	leaks := []string{"conn closed", "pgconn", "pgx", "FATAL", "SQLSTATE", "127.0.0.1"}

	t.Run("REST", func(t *testing.T) {
		read := func() resp {
			return h.do(http.MethodGet, "/v1/workspaces", token, nil, map[string]string{"X-Olivares-Tenant": tenant})
		}
		unavailable := 0
		for i := 0; i < rounds; i++ {
			if r := read(); r.code != http.StatusOK {
				t.Fatalf("round %d: request before the sever = %d %s", i, r.code, r.raw)
			}
			proxy.Sever()
			r := read()
			switch r.code {
			case http.StatusOK: // the driver noticed the cut first and the pool dialed anew
			case http.StatusServiceUnavailable:
				unavailable++
				errBody, _ := r.body["error"].(map[string]any)
				if errBody["code"] != "store_unavailable" {
					t.Fatalf("round %d: 503 body = %s, want code store_unavailable", i, r.raw)
				}
				for _, leak := range leaks {
					if strings.Contains(r.raw, leak) {
						t.Fatalf("round %d: the 503 body carries the driver's text %q: %s", i, leak, r.raw)
					}
				}
			default:
				t.Fatalf("round %d: first request after the sever = %d %s, want 200 or 503 store_unavailable", i, r.code, r.raw)
			}
			// A failed first request leaves nothing behind: the next one is served.
			if next := read(); next.code != http.StatusOK {
				t.Fatalf("round %d: request after the first = %d %s, want a transparent recovery", i, next.code, next.raw)
			}
		}
		if unavailable == 0 {
			t.Fatalf("COULD_NOT_LOOK: %d severs and no request met the dead connection, so no status was measured", rounds)
		}
		t.Logf("REST: %d of %d first requests after a sever answered 503 store_unavailable, the rest were served", unavailable, rounds)
	})

	t.Run("gRPC", func(t *testing.T) {
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		gs := h.srv.NewGRPCServer()
		go func() { _ = gs.Serve(lis) }()
		defer gs.Stop()
		conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		cl := apiv1.NewControlPlaneClient(conn)
		ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
		list := func() error {
			_, err := cl.ListAgents(ctx, &apiv1.ListAgentsRequest{Tenant: tenant})
			return err
		}
		unavailable := 0
		for i := 0; i < rounds; i++ {
			if err := list(); err != nil {
				t.Fatalf("round %d: request before the sever: %v", i, err)
			}
			proxy.Sever()
			err := list()
			switch status.Code(err) {
			case codes.OK:
			case codes.Unavailable:
				unavailable++
				for _, leak := range leaks {
					if strings.Contains(err.Error(), leak) {
						t.Fatalf("round %d: the Unavailable status carries the driver's text %q: %v", i, leak, err)
					}
				}
			default:
				t.Fatalf("round %d: first RPC after the sever = %v, want OK or Unavailable", i, err)
			}
			if err := list(); err != nil {
				t.Fatalf("round %d: RPC after the first: %v, want a transparent recovery", i, err)
			}
		}
		if unavailable == 0 {
			t.Fatalf("COULD_NOT_LOOK: %d severs and no RPC met the dead connection, so no code was measured", rounds)
		}
		t.Logf("gRPC: %d of %d first RPCs after a sever answered Unavailable, the rest were served", unavailable, rounds)
	})
}
