// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/audit"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/genpb/apiv1"
	"github.com/olivaresai/olivares/core/license"
)

func TestGRPCControlPlane(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

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
	ctx := context.Background()
	authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+admin)

	// GetServerInfo needs no auth.
	info, err := cl.GetServerInfo(ctx, &apiv1.Empty{})
	if err != nil || info.GetSetupRequired() {
		t.Fatalf("server info = %v %v", info, err)
	}

	// CreateAgent through the auth interceptor (same authz path as REST).
	ag, err := cl.CreateAgent(authCtx, &apiv1.CreateAgentRequest{Tenant: tenant.String(), Name: "grpc-bot", Kind: "claude-code"})
	if err != nil || ag.GetId() == "" {
		t.Fatalf("create agent = %v %v", ag, err)
	}
	if ag.GetTenantId() != tenant.String() {
		t.Fatalf("agent tenant = %s, want %s", ag.GetTenantId(), tenant)
	}

	// It is listable.
	list, err := cl.ListAgents(authCtx, &apiv1.ListAgentsRequest{Tenant: tenant.String()})
	if err != nil || len(list.GetAgents()) != 1 {
		t.Fatalf("list = %v %v", list, err)
	}

	// Unauthenticated mutation is rejected.
	if _, err := cl.CreateAgent(ctx, &apiv1.CreateAgentRequest{Tenant: tenant.String(), Name: "x", Kind: "y"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauth create code = %v, want Unauthenticated", status.Code(err))
	}

	// VerifyAudit returns a healthy report.
	vr, err := cl.VerifyAudit(authCtx, &apiv1.VerifyAuditRequest{Tenant: tenant.String()})
	if err != nil || !vr.GetChainOk() {
		t.Fatalf("verify audit = %v %v", vr, err)
	}

	// ...AND IT CAN SAY WHICH OF THREE THINGS IS TRUE OF THE CHECKPOINTS (2026-08-06).
	// `checkpoints_ok` is a boolean and a young chain has no checkpoints yet, so `false` meant
	// two opposite things: "a checkpoint exists and did NOT verify" (tamper, act now) and
	// "nothing has been attested yet" (normal, wait). REST has carried ok|failed|pending from
	// the start; this surface could not express it, so a client saw ok=true beside
	// checkpoints_ok=false and had nothing to go on. This fixture IS a young chain — no
	// checkpoint has been written — which is precisely the case the boolean could not name.
	if got := vr.GetCheckpointStatus(); got != string(audit.CheckpointStatusPending) {
		t.Errorf("checkpoint_status = %q, want %q on a chain with nothing attested yet",
			got, audit.CheckpointStatusPending)
	}
	if vr.GetCheckpointsOk() {
		t.Errorf("checkpoints_ok = true with no checkpoint written; the boolean must keep its old meaning")
	}
	// The overall verdict must NOT be dragged down by a pending checkpoint: structural
	// verification already proves chain integrity, and refusing here would report every fresh
	// ledger as broken.
	if !vr.GetOk() {
		t.Errorf("ok = false on a healthy young chain; pending is not failed")
	}
}

// The gRPC GetServerInfo mirrors the REST badge: it carries the same attested
// display-only license labels (plan, support tier) and gates nothing.
func TestGRPCServerInfoLicenseLabels(t *testing.T) {
	// CHANGED BY: a TERM, because v8 is term-only. What this test is about — the
	// display-only labels and the fact that nothing is gated — is unaffected.
	licNow := time.Now().UTC()
	blob, err := license.Sign(license.Claims{
		Licensee: "Beta Corp", Plan: "commercial", SupportTier: "standard",
		IssuedAt: licNow, ExpiresAt: licNow.Add(365 * 24 * time.Hour),
	}, license.DevPrivateKey())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.LicenseBlob = blob
		o.LicensePublicKey = license.DefaultPublicKey()
	})

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

	info, err := apiv1.NewControlPlaneClient(conn).GetServerInfo(context.Background(), &apiv1.Empty{})
	if err != nil {
		t.Fatalf("server info = %v", err)
	}
	if info.GetLicenseStatus() != "valid" || info.GetLicenseLicensee() != "Beta Corp" {
		t.Fatalf("license status/licensee = (%q,%q)", info.GetLicenseStatus(), info.GetLicenseLicensee())
	}
	if info.GetLicensePlan() != "commercial" || info.GetLicenseSupportTier() != "standard" {
		t.Fatalf("license labels = (plan=%q, support=%q); want commercial/standard", info.GetLicensePlan(), info.GetLicenseSupportTier())
	}
}
