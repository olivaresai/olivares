// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/secure"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
	sdkplugin "github.com/olivaresai/olivares/sdk/plugin"
	olv1 "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

// capturePublisher records the observations the ingest server lifts, standing in
// for the runtime in the gRPC auth/decode test.
type capturePublisher struct {
	mu  sync.Mutex
	got []capturedObs
}

type capturedObs struct {
	tenant, source string
	kind           sdkmodel.ObservationType
}

func (c *capturePublisher) Ingest(_ context.Context, tenant, source string, obs sdkmodel.Observation) error {
	c.mu.Lock()
	c.got = append(c.got, capturedObs{tenant: tenant, source: source, kind: obs.ObservationType()})
	c.mu.Unlock()
	return nil
}

func (c *capturePublisher) snapshot() []capturedObs {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedObs(nil), c.got...)
}

// TestGRPCIngestPush is the CB-1 option C proof: a collector PUSHES observations to
// the core's IngestService and the core lifts them through the runtime — and the
// endpoint is hardened exactly like the rest of the gRPC surface. It checks the
// happy path (an ingest:write principal pushes, the observation is accepted and
// dispatched with the right tenant/source) and that the secure-default auth is NOT
// weakened: an unauthenticated push is Unauthenticated, and an editor (who lacks
// the admin-tier ingest:write) is PermissionDenied.
func TestGRPCIngestPush(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	mkUser := func(email, pass, role string) string {
		r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": pass}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("create user %s = %d %s", email, r.code, r.raw)
		}
		uid := r.body["id"].(string)
		if r := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil); r.code != http.StatusCreated {
			t.Fatalf("grant %s = %d %s", email, r.code, r.raw)
		}
		lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": pass}, nil)
		if lr.code != http.StatusOK {
			t.Fatalf("login %s = %d %s", email, lr.code, lr.raw)
		}
		return lr.body["token"].(string)
	}
	adminTok := mkUser("ing-admin@acme.com", "adminpass123", auth.RoleAdmin)
	editorTok := mkUser("ing-editor@acme.com", "editorpass123", auth.RoleEditor)

	// A second server SHARING the store/authenticator, with the ingest publisher
	// enabled (the serve binary enables it on the same gRPC server).
	pub := &capturePublisher{}
	ingestSrv, err := api.New(api.Options{
		Store: h.st, Authenticator: h.authr, Authorizer: auth.NewAuthorizer(nil),
		Signer: h.signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "s.token")),
		Version: "test", Ingest: pub,
	})
	if err != nil {
		t.Fatal(err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := ingestSrv.NewGRPCServer()
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cl := olv1.NewIngestServiceClient(conn)

	obsPB, err := sdkplugin.ObservationToPB(sdkmodel.EdgeObservation{
		OriginRef: "claude", ResourceKind: "postgres.table", ResourceRef: "public.customers",
		Mode: sdkmodel.ModeRead, Source: sdkmodel.SignalOTEL, ObservedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Happy path: an ingest:write (admin) principal pushes; the observation is
	// accepted and dispatched with the right tenant/source.
	push := func(token string) (uint64, error) {
		ctx := context.Background()
		if token != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
		}
		stream, serr := cl.Push(ctx)
		if serr != nil {
			return 0, serr
		}
		_ = stream.Send(&olv1.IngestEnvelope{Tenant: tenant.String(), Source: "edge-collector", Observation: obsPB})
		sum, rerr := stream.CloseAndRecv()
		if rerr != nil {
			return 0, rerr
		}
		return sum.GetAccepted(), nil
	}

	accepted, err := push(adminTok)
	if err != nil {
		t.Fatalf("admin push: %v", err)
	}
	if accepted != 1 {
		t.Fatalf("accepted = %d, want 1", accepted)
	}
	got := pub.snapshot()
	if len(got) != 1 || got[0].tenant != tenant.String() || got[0].source != "edge-collector" || got[0].kind != sdkmodel.ObsEdge {
		t.Fatalf("dispatched observation = %+v, want one edge for tenant/edge-collector", got)
	}

	// The collector's REAL push Sink (the same IngestSink the `collector` mode uses)
	// drives the same endpoint end to end: a locally-emitted observation streams to
	// the core and is dispatched.
	sinkCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+adminTok)
	sink := sdkplugin.NewIngestSink(cl, tenant.String(), "sink-collector")
	if err := sink.Emit(sinkCtx, sdkmodel.CostSample{ProviderRef: "anthropic", ModelRef: "claude", OccurredAt: time.Now().UTC()}); err != nil {
		t.Fatalf("sink emit: %v", err)
	}
	sinkAccepted, err := sink.CloseAndRecv()
	if err != nil {
		t.Fatalf("sink close: %v", err)
	}
	if sinkAccepted != 1 {
		t.Fatalf("sink accepted = %d, want 1", sinkAccepted)
	}

	// Secure default intact: no bearer → Unauthenticated.
	if _, err := push(""); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous push code = %v, want Unauthenticated", status.Code(err))
	}
	// ingest:write is admin-tier: an editor is rejected.
	if _, err := push(editorTok); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("editor push code = %v, want PermissionDenied", status.Code(err))
	}

	// Exactly the two authorized pushes (the edge and the cost sample) dispatched;
	// the rejected pushes dispatched nothing.
	final := pub.snapshot()
	if len(final) != 2 {
		t.Fatalf("publisher saw %d observations, want exactly 2 (rejected pushes must not dispatch)", len(final))
	}
	if final[1].kind != sdkmodel.ObsCost || final[1].source != "sink-collector" {
		t.Fatalf("second dispatched observation = %+v, want a cost sample from sink-collector", final[1])
	}
}

// togglePublisher is an ObservationPublisher whose Ingest can be switched to fail,
// to exercise the ingest-reject SLI (a publish/backpressure error).
type togglePublisher struct{ fail atomic.Bool }

func (p *togglePublisher) Ingest(_ context.Context, _, _ string, _ sdkmodel.Observation) error {
	if p.fail.Load() {
		return errors.New("ingest: simulated publish/backpressure error")
	}
	return nil
}

// metricLine returns the value of a labelless Prometheus sample line `name value`.
func metricLine(t *testing.T, body, name string) float64 {
	t.Helper()
	for _, ln := range strings.Split(body, "\n") {
		if strings.HasPrefix(ln, name+" ") {
			v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(ln, name+" ")), 64)
			if err != nil {
				t.Fatalf("parse metric %s: %v (line %q)", name, err, ln)
			}
			return v
		}
	}
	t.Fatalf("metric %q not found in scrape:\n%s", name, body)
	return 0
}

// TestIngestSLIMetrics proves ingest SLIs on the real gRPC ingest path: an
// accepted observation records the ingest-latency histogram (the ingest-p99 SLI)
// and increments the accepted throughput counter; a publish/backpressure error
// increments olivares_ingest_rejected_total and records NO duration sample. These
// are the only instruments that make the published ingest SLO measurable.
func TestIngestSLIMetrics(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "sli")

	// An ingest:write principal (admin-tier on the tenant).
	cr := h.do("POST", "/v1/users", admin, map[string]any{"email": "sli@acme.com", "password": "ingestpass123"}, nil)
	if cr.code != http.StatusCreated {
		t.Fatalf("create user = %d %s", cr.code, cr.raw)
	}
	uid := cr.body["id"].(string)
	if r := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": auth.RoleAdmin}, nil); r.code != http.StatusCreated {
		t.Fatalf("grant = %d %s", r.code, r.raw)
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "sli@acme.com", "password": "ingestpass123"}, nil)
	if lr.code != http.StatusOK {
		t.Fatalf("login = %d %s", lr.code, lr.raw)
	}
	tok := lr.body["token"].(string)

	tp := &togglePublisher{}
	ingestSrv, err := api.New(api.Options{
		Store: h.st, Authenticator: h.authr, Authorizer: auth.NewAuthorizer(nil),
		Signer: h.signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "s.token")),
		Version: "test", Ingest: tp,
	})
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := ingestSrv.NewGRPCServer()
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
	push := func() error {
		ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+tok)
		stream, serr := cl.Push(ctx)
		if serr != nil {
			return serr
		}
		_ = stream.Send(&olv1.IngestEnvelope{Tenant: tenant.String(), Source: "edge", Observation: obsPB})
		_, rerr := stream.CloseAndRecv()
		return rerr
	}
	scrape := func() string {
		rec := httptest.NewRecorder()
		ingestSrv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return rec.Body.String()
	}

	// Three accepted observations → three duration samples, zero rejects.
	for i := 0; i < 3; i++ {
		if err := push(); err != nil {
			t.Fatalf("accepted push %d: %v", i, err)
		}
	}
	ms := scrape()
	if got := metricLine(t, ms, "olivares_ingest_duration_seconds_count"); got != 3 {
		t.Errorf("ingest duration_count = %v, want 3 (one per accepted observation)", got)
	}
	if !strings.Contains(ms, "olivares_ingest_duration_seconds_bucket{le=") {
		t.Errorf("ingest-latency histogram buckets missing from /metrics:\n%s", ms)
	}
	if got := metricLine(t, ms, "olivares_ingest_rejected_total"); got != 0 {
		t.Errorf("ingest rejected_total = %v, want 0 (no publish errors yet)", got)
	}

	// A publish/backpressure error → one reject, and still only three durations
	// (a reject must NOT record a latency sample).
	tp.fail.Store(true)
	if err := push(); err == nil {
		t.Fatal("push with a failing publisher should return an error")
	}
	ms = scrape()
	if got := metricLine(t, ms, "olivares_ingest_rejected_total"); got != 1 {
		t.Errorf("ingest rejected_total = %v, want 1 after a publish error", got)
	}
	if got := metricLine(t, ms, "olivares_ingest_duration_seconds_count"); got != 3 {
		t.Errorf("ingest duration_count = %v, want still 3 (a reject records no duration)", got)
	}
}
