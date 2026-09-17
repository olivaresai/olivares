// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
	sdkplugin "github.com/olivaresai/olivares/sdk/plugin"
	pb "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type budgetIngressTap struct {
	eventbus.Bus
	mu     sync.Mutex
	events []event.Event
}

func (b *budgetIngressTap) Publish(ctx context.Context, e event.Event) error {
	b.mu.Lock()
	b.events = append(b.events, e)
	b.mu.Unlock()
	return b.Bus.Publish(ctx, e)
}
func (b *budgetIngressTap) snapshot() []event.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]event.Event(nil), b.events...)
}

// Uses real tenant membership, gRPC authentication/RBAC, collector IngestSink,
// protobuf conversion, API ingestion and Runtime.Ingest/busSink. The tap observes
// publications without substituting for any admission/decoding boundary.
func TestBudgetEvidenceAuthenticatedCollectorDenied(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "budget-transport")
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": "budget@acme.com", "password": "collectorpass123"}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("create user: %d %s", r.code, r.raw)
	}
	uid := r.body["id"].(string)
	r = h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": auth.RoleAdmin}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("membership: %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": "budget@acme.com", "password": "collectorpass123"}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("login: %d %s", r.code, r.raw)
	}
	token := r.body["token"].(string)
	b := &budgetIngressTap{Bus: eventbus.NewInProc(eventbus.Options{})}
	defer b.Close()
	rt := runtime.New(runtime.Options{Bus: b})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := rt.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	srv, err := api.New(api.Options{Store: h.st, Authenticator: h.authr, Authorizer: auth.NewAuthorizer(nil), Signer: h.signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")), Version: "test", Ingest: rt})
	if err != nil {
		t.Fatal(err)
	}
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
	cl := pb.NewIngestServiceClient(conn)
	for _, shape := range []string{"value", "pointer", "present_empty", "future", "legacy"} {
		t.Run(shape, func(t *testing.T) {
			f := sdkmodel.FindingReport{Kind: "finops_budget_cap", SubjectKind: "budget", SubjectRef: "b14c91e9-b593-48d6-94aa-fc8de67c1f51", DetailHash: strings.Repeat("a", 64), BudgetEvidence: &sdkmodel.BudgetAlertEvidenceSummary{SchemaVersion: 1, DigestVersion: 1, AlertID: "bd283469-7c26-4256-9798-f3b175a8df2a", AmountClass: "exact", Crossing: "proven"}}
			if shape == "present_empty" {
				f.BudgetEvidence = &sdkmodel.BudgetAlertEvidenceSummary{}
			}
			if shape == "future" {
				f.BudgetEvidence.SchemaVersion = 2
			}
			if shape == "legacy" {
				f.BudgetEvidence = nil
			}
			var obs sdkmodel.Observation = f
			if shape == "pointer" {
				obs = &f
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
			sink := sdkplugin.NewIngestSink(cl, tenant.String(), sdkmodel.BudgetEvidenceProducer)
			if err := sink.Emit(ctx, obs); err != nil {
				t.Fatalf("collector send: %v", err)
			}
			accepted, err := sink.CloseAndRecv()
			if shape == "legacy" {
				got := b.snapshot()
				if err != nil || accepted != 1 || len(got) != 1 || got[0].Tenant != tenant.String() || got[0].Source != sdkmodel.BudgetEvidenceProducer {
					t.Fatalf("legacy transport changed: accepted=%d err=%v events=%v", accepted, err, got)
				}
				finding, ok := event.FindingOf(got[0])
				if !ok || finding.BudgetEvidence != nil {
					t.Fatal("legacy acquired evidence")
				}
			} else if err == nil || accepted != 0 || len(b.snapshot()) != 0 {
				t.Fatalf("collector Source spoof admitted: accepted=%d err=%v events=%d", accepted, err, len(b.snapshot()))
			}
		})
	}
}
