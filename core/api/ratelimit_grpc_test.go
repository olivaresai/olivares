// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/olivaresai/olivares/core/api/genpb/apiv1"
	"github.com/olivaresai/olivares/core/api/ratelimit"
	"github.com/olivaresai/olivares/core/auth"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
	sdkplugin "github.com/olivaresai/olivares/sdk/plugin"
	olv1 "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

// TestRateLimitGRPCSurface proves the gRPC ControlPlane surface is metered by the
// SAME limiter as REST: a tenant cannot escape its per-tenant write quota by moving
// from /v1 to gRPC. An over-limit write returns codes.ResourceExhausted.
func TestRateLimitGRPCSurface(t *testing.T) {
	h := newRLHarness(t, ratelimit.ModeEnforce, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.tenantToken(admin, tenant, "a@acme.io")

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
	authCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+tok)

	// Write burst is 2: the first two creates admit, the rest deny with ResourceExhausted
	// (the gRPC mapping of 429). Assert BOTH — admits then denials — so a deny-everything
	// regression (which would also "see a denial") cannot pass.
	var admitted, exhausted int
	for i := 0; i < 6; i++ {
		_, err := cl.CreateAgent(authCtx, &apiv1.CreateAgentRequest{Tenant: tenant.String(), Name: "g", Kind: "k"})
		switch status.Code(err) {
		case codes.OK:
			admitted++
		case codes.ResourceExhausted:
			exhausted++
		default:
			t.Fatalf("unexpected gRPC status: %v", err)
		}
	}
	if admitted != 2 {
		t.Fatalf("gRPC admitted %d writes, want exactly the write burst (2)", admitted)
	}
	if exhausted != 4 {
		t.Fatalf("gRPC throttled %d writes, want 4 (6 attempts - burst 2); the surface may bypass the limiter", exhausted)
	}
}

// TestRateLimitIngestStreamIsExcluded proves the collector IngestService (ingest:write)
// is NOT metered by the per-tenant API quota: even after the tenant's API write bucket
// is exhausted, observation pushes still succeed. Ingest is a trusted mTLS push channel
// with its own backpressure — metering it would throttle observability.
func TestRateLimitIngestStreamIsExcluded(t *testing.T) {
	// One rate-limited server that serves BOTH REST (to drain the bucket) and the gRPC
	// IngestService (to push), so they share the same limiter.
	pub := &capturePublisher{}
	h := newRLHarness(t, ratelimit.ModeEnforce, nil, pub)
	srv := h.srv
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// A tenant-bound ADMIN token holds ingest:write (admin-tier) AND agent:write.
	adminTok := h.tenantTokenRole(admin, tenant, "ing@acme.io", auth.RoleAdmin)

	// Exhaust the tenant's REST write bucket (default tier write burst 2).
	var drained bool
	for i := 0; i < 6; i++ {
		if h.do("POST", "/v1/agents", adminTok, map[string]any{"name": "x", "kind": "k"}, tenantHdr(tenant)).code == http.StatusTooManyRequests {
			drained = true
		}
	}
	if !drained {
		t.Fatal("tenant write bucket should be exhausted by the REST writes")
	}

	// Now push observations over gRPC with the SAME tenant token. If ingest were metered,
	// the exhausted write bucket would 429 it; instead every push is accepted.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := srv.NewGRPCServer()
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cl := olv1.NewIngestServiceClient(conn)
	obsPB, err := sdkplugin.ObservationToPB(sdkmodel.EdgeObservation{
		OriginRef: "claude", ResourceKind: "postgres.table", ResourceRef: "public.t",
		Mode: sdkmodel.ModeRead, Source: sdkmodel.SignalOTEL, ObservedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+adminTok)
		stream, serr := cl.Push(ctx)
		if serr != nil {
			t.Fatalf("push %d open: %v", i, serr)
		}
		_ = stream.Send(&olv1.IngestEnvelope{Tenant: tenant.String(), Source: "edge", Observation: obsPB})
		if _, rerr := stream.CloseAndRecv(); status.Code(rerr) == codes.ResourceExhausted {
			t.Fatalf("ingest push %d was rate-limited (ResourceExhausted) — ingest must be excluded from the API quota", i)
		} else if rerr != nil {
			t.Fatalf("push %d: %v", i, rerr)
		}
	}
}
