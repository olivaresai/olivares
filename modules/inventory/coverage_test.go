// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/inventory"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type coverageSource struct {
	gather func(context.Context, sdk.Sink) error
}

func (s *coverageSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.coverage", APIVersion: sdk.APIVersion, Type: sdk.TypeSource}
}
func (s *coverageSource) Open(context.Context, sdk.Config) error     { return nil }
func (s *coverageSource) Close(context.Context) error                { return nil }
func (s *coverageSource) Gather(c context.Context, k sdk.Sink) error { return s.gather(c, k) }
func coverageScope() sdkmodel.InventoryScope {
	return sdkmodel.InventoryScope{Contract: sdkmodel.AzureInventoryContract, Family: "azure.resource", Selectors: []string{"sub-1"}}
}
func coverageRegistration() event.SourceRegistration {
	return event.SourceRegistration{SourceID: "source-a", SourceRevision: 1, EnvironmentRef: "env-a"}
}
func coverageEdge() sdkmodel.EdgeObservation {
	return mkEdge("azure.subscription", "sub-1", "azure.resource", "/subscriptions/sub-1/providers/test/things/a", sdkmodel.ModeUnknown, "azure", "")
}
func coveragePath(reg event.SourceRegistration) string {
	return fmt.Sprintf("/v1/m/inventory/collections?source_id=%s&source_revision=%d&environment_ref=%s", reg.SourceID, reg.SourceRevision, reg.EnvironmentRef)
}
func coverageCurrent(t *testing.T, h *harness, token string, tenant model.TenantID, reg event.SourceRegistration) map[string]any {
	t.Helper()
	r := h.do("GET", coveragePath(reg), token, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("collections: %d %s", r.code, r.raw)
	}
	items := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items: %s", r.raw)
	}
	return items[0].(map[string]any)
}
func coverageWait(t *testing.T, h *harness, token string, tenant model.TenantID, reg event.SourceRegistration, predicate func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		item := coverageCurrent(t, h, token, tenant, reg)
		if predicate(item) {
			return item
		}
		if time.Now().After(deadline) {
			t.Fatalf("collection did not reach expected state: %+v", item)
		}
		time.Sleep(time.Millisecond * 5)
	}
}
func coverageRun(t *testing.T, m *inventory.Module, tenant model.TenantID, s sdk.SourceConnector, bus eventbus.Bus) *runtime.Runtime {
	t.Helper()
	rt := runtime.New(runtime.Options{Bus: bus})
	if err := rt.AddModule(m, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	if err := rt.AddPreparedSourceRegistered(context.Background(), "coverage-source", rt.PrepareInProcSource(s), sdk.Config{}, tenant.String(), 0, coverageRegistration()); err != nil {
		t.Fatal(err)
	}
	return rt
}
func coverageEmit(ctx context.Context, sink sdk.Sink, members bool) error {
	scope := coverageScope()
	at := time.Now().UTC()
	if err := sink.Emit(ctx, sdkmodel.InventoryCollectionStart{Scope: scope, ObservedAt: at}); err != nil {
		return err
	}
	count := int64(0)
	if members {
		if err := sink.Emit(ctx, sdkmodel.InventoryCollectionMember{Edge: coverageEdge()}); err != nil {
			return err
		}
		count = 1
	}
	return sink.Emit(ctx, sdkmodel.InventoryCollectionReport{State: "complete", Reason: "exhausted", RequestedScope: scope.Fingerprint(), FulfilledScope: scope.Fingerprint(), Count: count, ObservedUntil: at})
}

func TestInventoryCoverageEmptyAndSelection(t *testing.T) {
	m := inventory.New(inventory.WithCollectionCoverage())
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "coverage-empty")
	other := h.createOrg(admin, "coverage-other")
	coverageRun(t, m, tenant, &coverageSource{gather: func(ctx context.Context, s sdk.Sink) error { return coverageEmit(ctx, s, false) }}, nil)
	item := coverageWait(t, h, admin, tenant, coverageRegistration(), func(v map[string]any) bool { return v["last_qualified_success"] != nil })
	current := item["current"].(map[string]any)
	if current["coverage"] != "complete" || current["committed_count"] != float64(0) {
		t.Fatalf("empty result %+v", current)
	}
	reg := coverageRegistration()
	reg.SourceRevision = 2
	next := coverageCurrent(t, h, admin, tenant, reg)
	if next["current"].(map[string]any)["coverage"] != "unknown" || next["last_qualified_success"] != nil {
		t.Fatal("old revision inherited")
	}
	reg = coverageRegistration()
	reg.EnvironmentRef = "env-b"
	if v := coverageCurrent(t, h, admin, tenant, reg); v["current"].(map[string]any)["coverage"] != "unknown" || v["last_qualified_success"] != nil {
		t.Fatal("old environment inherited")
	}
	isolated := coverageCurrent(t, h, admin, other, coverageRegistration())
	if isolated["current"].(map[string]any)["coverage"] != "unknown" {
		t.Fatal("tenant evidence leaked")
	}
	if r := h.do("GET", "/v1/m/inventory/collections", admin, nil, tenantHdr(tenant)); r.code != 400 {
		t.Fatal("current registration selector must be explicit")
	}
	if r := h.do("GET", coveragePath(coverageRegistration()), "", nil, tenantHdr(tenant)); r.code != 401 {
		t.Fatalf("unauthenticated read: %d", r.code)
	}
}

// lateCoverageBus models an accepted queue whose member has not committed yet.
// It delegates actual delivery to the real in-process bus on release.
type lateCoverageBus struct {
	eventbus.Bus
	mu     sync.Mutex
	saved  *event.Event
	queued chan struct{}
}

func (b *lateCoverageBus) Publish(ctx context.Context, e event.Event) error {
	if e.InventoryMember != nil {
		b.mu.Lock()
		if b.saved == nil {
			copy := e
			b.saved = &copy
			close(b.queued)
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()
	}
	return b.Bus.Publish(ctx, e)
}
func (b *lateCoverageBus) release(t *testing.T) event.Event {
	t.Helper()
	b.mu.Lock()
	e := *b.saved
	b.mu.Unlock()
	if err := b.Bus.Publish(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestInventoryCoverageProjectionAndReplay(t *testing.T) {
	m := inventory.New(inventory.WithCollectionCoverage())
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "coverage-late")
	base := eventbus.NewInProc(eventbus.Options{})
	b := &lateCoverageBus{Bus: base, queued: make(chan struct{})}
	t.Cleanup(func() { _ = base.Close() })
	rt := coverageRun(t, m, tenant, &coverageSource{gather: func(ctx context.Context, s sdk.Sink) error {
		if err := coverageEmit(ctx, s, true); err != nil {
			return err
		}
		return s.Emit(ctx, sdkmodel.EdgeObservation{OriginKind: "identity", OriginRef: "activity-person", ResourceKind: "azure.api", ResourceRef: "read", Source: "azure_activity", ObservedAt: time.Now().UTC()})
	}}, b)
	select {
	case <-b.queued:
	case <-time.After(3 * time.Second):
		t.Fatal("no queued member")
	}
	item := coverageWait(t, h, admin, tenant, coverageRegistration(), func(v map[string]any) bool { return v["current"].(map[string]any)["host_finished_at"] != nil })
	current := item["current"].(map[string]any)
	if current["projection"] != "pending" || current["admitted_count"] != float64(1) || current["committed_count"] != float64(0) || item["last_qualified_success"] != nil {
		t.Fatalf("enqueue was treated as receipt: %+v", item)
	}
	e := b.release(t)
	item = coverageWait(t, h, admin, tenant, coverageRegistration(), func(v map[string]any) bool { return v["last_qualified_success"] != nil })
	run := item["current"].(map[string]any)["run_id"]
	if err := base.Publish(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	// The remote push path can carry a member but cannot reattach it to this run.
	if err := rt.Ingest(context.Background(), tenant.String(), "coverage-source", sdkmodel.InventoryCollectionMember{Edge: coverageEdge()}); err != nil {
		t.Fatal(err)
	}
	// A new incomplete pass is visible without inheriting the prior current result.
	at := time.Now().UTC()
	next := event.InventoryRun{ID: "second-run", Tenant: tenant.String(), Registration: coverageRegistration(), StartedAt: at}
	if err := m.BeginRun(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if err := m.StartCollection(context.Background(), next, sdkmodel.InventoryCollectionStart{Scope: coverageScope(), ObservedAt: at}); err != nil {
		t.Fatal(err)
	}
	nextItem := coverageCurrent(t, h, admin, tenant, coverageRegistration())
	if nextItem["current"].(map[string]any)["coverage"] != "unknown" || nextItem["last_qualified_success"].(map[string]any)["run_id"] != run {
		t.Fatalf("history/current conflated: %+v", nextItem)
	}
	// Same C1 event, different run metadata, through the actual bus consumer.
	altered := e
	copy := *e.InventoryMember
	copy.RunID = next.ID
	altered.InventoryMember = &copy
	if err := base.Publish(context.Background(), altered); err != nil {
		t.Fatal(err)
	}
	conflict := coverageWait(t, h, admin, tenant, coverageRegistration(), func(v map[string]any) bool { return v["current"].(map[string]any)["rejection_reason"] != nil })
	if conflict["current"].(map[string]any)["committed_count"] != float64(0) {
		t.Fatal("conflicting replay appended a member")
	}
}

func TestInventoryCoverageGatherFailure(t *testing.T) {
	for _, mode := range []string{"error", "panic", "panic-nil", "missing", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			m := inventory.New(inventory.WithCollectionCoverage())
			h := newHarness(t, m)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "coverage-failure")
			coverageRun(t, m, tenant, &coverageSource{gather: func(ctx context.Context, s sdk.Sink) error {
				if mode == "missing" {
					return nil
				}
				if err := coverageEmit(ctx, s, false); err != nil {
					return err
				}
				switch mode {
				case "error":
					return errors.New("synthetic provider error")
				case "panic":
					panic("synthetic provider panic")
				case "panic-nil":
					panic(nil)
				case "duplicate":
					_ = s.Emit(ctx, sdkmodel.InventoryCollectionReport{State: "complete", Reason: "exhausted", RequestedScope: coverageScope().Fingerprint(), FulfilledScope: coverageScope().Fingerprint(), ObservedUntil: time.Now().UTC()})
				}
				return nil
			}}, nil)
			item := coverageWait(t, h, admin, tenant, coverageRegistration(), func(v map[string]any) bool { return v["current"].(map[string]any)["host_finished_at"] != nil })
			if item["last_qualified_success"] != nil || item["current"].(map[string]any)["coverage"] == "complete" {
				raw, _ := json.Marshal(item)
				t.Fatalf("failed pass qualified: %s", raw)
			}
		})
	}
}

func TestInventoryCoverageCancelAndAbsentPort(t *testing.T) {
	t.Run("cancel after proposal", func(t *testing.T) {
		m := inventory.New(inventory.WithCollectionCoverage())
		h := newHarness(t, m)
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "coverage-cancel")
		proposed := make(chan struct{})
		rt := coverageRun(t, m, tenant, &coverageSource{gather: func(ctx context.Context, s sdk.Sink) error {
			if err := coverageEmit(ctx, s, false); err != nil {
				return err
			}
			close(proposed)
			<-ctx.Done()
			return ctx.Err()
		}}, nil)
		select {
		case <-proposed:
		case <-time.After(3 * time.Second):
			t.Fatal("no proposal")
		}
		item := coverageCurrent(t, h, admin, tenant, coverageRegistration())
		if item["last_qualified_success"] != nil {
			t.Fatal("proposal qualified before Gather returned")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := rt.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		item = coverageCurrent(t, h, admin, tenant, coverageRegistration())
		if item["current"].(map[string]any)["coverage"] == "complete" || item["last_qualified_success"] != nil {
			t.Fatal("canceled pass qualified")
		}
	})
	t.Run("absent port preserves resources", func(t *testing.T) {
		m := inventory.New()
		h := newHarness(t, m)
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "coverage-legacy")
		coverageRun(t, m, tenant, &coverageSource{gather: func(ctx context.Context, s sdk.Sink) error { return coverageEmit(ctx, s, true) }}, nil)
		h.waitCatalog(tenant, 1)
		item := coverageCurrent(t, h, admin, tenant, coverageRegistration())
		if item["current"].(map[string]any)["coverage"] != "unknown" || item["last_qualified_success"] != nil {
			t.Fatal("legacy run fabricated coverage")
		}
	})
}

func TestInventoryCoverageTerminalOrderAndConflict(t *testing.T) {
	m := inventory.New(inventory.WithCollectionCoverage())
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "coverage-order")
	at := time.Now().UTC()
	reg := coverageRegistration()
	scope := coverageScope()
	first := event.InventoryRun{ID: "first", Tenant: tenant.String(), Registration: reg, StartedAt: at}
	second := first
	second.ID = "second"
	second.StartedAt = at.Add(-time.Hour)
	for _, run := range []event.InventoryRun{first, second} {
		if err := m.BeginRun(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		if err := m.StartCollection(context.Background(), run, sdkmodel.InventoryCollectionStart{Scope: scope, ObservedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	finish := event.InventoryFinish{Report: sdkmodel.InventoryCollectionReport{State: "complete", Reason: "exhausted", RequestedScope: scope.Fingerprint(), FulfilledScope: scope.Fingerprint(), ObservedUntil: at}, FinishedAt: at}
	if err := m.FinishRun(context.Background(), first, finish); err != nil {
		t.Fatal(err)
	}
	item := coverageCurrent(t, h, admin, tenant, reg)
	if item["current"].(map[string]any)["run_id"] != "second" || item["current"].(map[string]any)["run_order"] != float64(2) || item["current"].(map[string]any)["coverage"] != "unknown" {
		t.Fatal("clock/older completion replaced latest run")
	}
	if err := m.FinishRun(context.Background(), second, finish); err != nil {
		t.Fatal(err)
	}
	if err := m.FinishRun(context.Background(), second, finish); err != nil {
		t.Fatal("identical terminal replay", err)
	}
	changed := finish
	changed.Report.State = "partial"
	changed.Report.Reason = "provider_error"
	if err := m.FinishRun(context.Background(), second, changed); !errors.Is(err, inventory.ErrCollectionRejected) {
		t.Fatal("conflicting terminal accepted", err)
	}
	item = coverageCurrent(t, h, admin, tenant, reg)
	current := item["current"].(map[string]any)
	if current["rejection_reason"] != "terminal_conflict" || current["projection"] != "failed" {
		t.Fatalf("conflict invisible %+v", item)
	}
}

// A transport refusal remains observable even if the connector ignores it and
// later proposes complete. The production consumer still handles all other rows.
type refusingCoverageBus struct{ eventbus.Bus }

func (b refusingCoverageBus) Publish(ctx context.Context, e event.Event) error {
	if e.InventoryMember != nil {
		return errors.New("synthetic queue refusal")
	}
	return b.Bus.Publish(ctx, e)
}
func TestInventoryCoverageInvalidStreamsAndSinkFailure(t *testing.T) {
	for _, mode := range []string{"missing-start", "overlap", "outside-scope", "sink-error"} {
		t.Run(mode, func(t *testing.T) {
			m := inventory.New(inventory.WithCollectionCoverage())
			h := newHarness(t, m)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "coverage-invalid")
			base := eventbus.NewInProc(eventbus.Options{})
			t.Cleanup(func() { _ = base.Close() })
			var bus eventbus.Bus = base
			if mode == "sink-error" {
				bus = refusingCoverageBus{base}
			}
			coverageRun(t, m, tenant, &coverageSource{gather: func(ctx context.Context, s sdk.Sink) error {
				at := time.Now().UTC()
				scope := coverageScope()
				start := sdkmodel.InventoryCollectionStart{Scope: scope, ObservedAt: at}
				if mode != "missing-start" {
					_ = s.Emit(ctx, start)
				}
				if mode == "overlap" {
					_ = s.Emit(ctx, start)
				}
				edge := coverageEdge()
				if mode == "outside-scope" {
					edge.OriginRef = "sub-2"
					edge.ResourceRef = "/subscriptions/sub-2/providers/test/things/a"
				}
				_ = s.Emit(ctx, sdkmodel.InventoryCollectionMember{Edge: edge})
				_ = s.Emit(ctx, sdkmodel.InventoryCollectionReport{State: "complete", Reason: "exhausted", RequestedScope: scope.Fingerprint(), FulfilledScope: scope.Fingerprint(), Count: 1, ObservedUntil: at})
				return nil
			}}, bus)
			item := coverageWait(t, h, admin, tenant, coverageRegistration(), func(v map[string]any) bool { return v["current"].(map[string]any)["host_finished_at"] != nil })
			if item["current"].(map[string]any)["coverage"] == "complete" || item["last_qualified_success"] != nil {
				t.Fatalf("invalid stream qualified: %+v", item)
			}
			if mode != "sink-error" {
				h.waitCatalog(tenant, 1)
			}
		})
	}
}

func TestInventoryCoverageTypedUnsupportedAndScopeIsolation(t *testing.T) {
	t.Run("typed unsupported", func(t *testing.T) {
		m := inventory.New(inventory.WithCollectionCoverage())
		h := newHarness(t, m)
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "coverage-unsupported")
		coverageRun(t, m, tenant, &coverageSource{gather: func(ctx context.Context, s sdk.Sink) error {
			return s.Emit(ctx, sdkmodel.InventoryCollectionReport{State: "unsupported", ObservedUntil: time.Now().UTC()})
		}}, nil)
		item := coverageWait(t, h, admin, tenant, coverageRegistration(), func(v map[string]any) bool { return v["current"].(map[string]any)["host_finished_at"] != nil })
		if item["current"].(map[string]any)["coverage"] != "unsupported" || item["last_qualified_success"] != nil {
			t.Fatalf("typed unsupported was conflated: %+v", item)
		}
	})
	t.Run("new source and scope", func(t *testing.T) {
		m := inventory.New(inventory.WithCollectionCoverage())
		h := newHarness(t, m)
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "coverage-scopes")
		coverageRun(t, m, tenant, &coverageSource{gather: func(ctx context.Context, s sdk.Sink) error { return coverageEmit(ctx, s, false) }}, nil)
		coverageWait(t, h, admin, tenant, coverageRegistration(), func(v map[string]any) bool { return v["last_qualified_success"] != nil })
		reg := coverageRegistration()
		reg.SourceID = "other-source"
		if v := coverageCurrent(t, h, admin, tenant, reg); v["current"].(map[string]any)["coverage"] != "unknown" || v["last_qualified_success"] != nil {
			t.Fatal("other source inherited success")
		}
		run := event.InventoryRun{ID: "different-scope", Tenant: tenant.String(), Registration: coverageRegistration(), StartedAt: time.Now().UTC()}
		if err := m.BeginRun(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		scope := coverageScope()
		scope.Selectors = []string{"sub-2"}
		if err := m.StartCollection(context.Background(), run, sdkmodel.InventoryCollectionStart{Scope: scope, ObservedAt: run.StartedAt}); err != nil {
			t.Fatal(err)
		}
		item := coverageCurrent(t, h, admin, tenant, coverageRegistration())
		if item["current"].(map[string]any)["coverage"] != "unknown" || item["last_qualified_success"] != nil {
			t.Fatalf("different scope inherited success: %+v", item)
		}
	})
}

// This is the actual negotiated SourcePlugin stream, not a replica of its codec.
func coveragePlugin(t *testing.T, source sdk.SourceConnector) sdk.SourceConnector {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	if err := (&plugin.SourcePlugin{Impl: source}).GRPCServer(nil, server); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close(); <-done })
	conn, err := grpc.NewClient("passthrough:///inventory-coverage", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := (&plugin.SourcePlugin{}).GRPCClient(ctx, nil, conn)
	if err != nil {
		t.Fatal(err)
	}
	return raw.(sdk.SourceConnector)
}

func TestInventoryCoveragePluginRefusalCannotQualify(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(fmt.Sprintf("refuse_%t", refuse), func(t *testing.T) {
			m := inventory.New(inventory.WithCollectionCoverage())
			h := newHarness(t, m)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "coverage-plugin")
			source := &coverageSource{gather: func(ctx context.Context, sink sdk.Sink) error {
				if err := coverageEmit(ctx, sink, false); err != nil {
					return err
				}
				if refuse {
					// A complete-zero proposal has crossed the wire. The connector ignores
					// the later local codec refusal and reports Gather success.
					_ = sink.Emit(ctx, (*sdkmodel.InventoryCollectionReport)(nil))
				}
				return nil
			}}
			coverageRun(t, m, tenant, coveragePlugin(t, source), nil)
			item := coverageWait(t, h, admin, tenant, coverageRegistration(), func(v map[string]any) bool { return v["current"].(map[string]any)["host_finished_at"] != nil })
			current := item["current"].(map[string]any)
			if refuse {
				if current["coverage"] != "partial" || current["reason"] != "gather_error" || item["last_qualified_success"] != nil {
					t.Fatalf("ignored plugin refusal qualified: %+v", item)
				}
			} else if current["coverage"] != "complete" || current["expected_count"] != float64(0) || item["last_qualified_success"] == nil {
				t.Fatalf("normal empty plugin result failed: %+v", item)
			}
		})
	}
}

// The public ModuleData seam models an acknowledgement lost AFTER the mutation
// returned success. It makes no claim to reproduce a PostgreSQL network failure.
type coverageFaultData struct {
	api.ModuleData
	failure error
	after   bool
	calls   atomic.Int64
	failAt  int64
}

func (d *coverageFaultData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	call := d.calls.Add(1)
	if call != d.failAt {
		return d.ModuleData.Mutate(ctx, tenant, fn)
	}
	if d.after {
		if err := d.ModuleData.Mutate(ctx, tenant, fn); err != nil {
			return err
		}
	}
	return fmt.Errorf("synthetic sensitive backend detail: %w", d.failure)
}

func TestInventoryCoverageStoreOutcomes(t *testing.T) {
	for _, failure := range []error{store.ErrCommitOutcomeUnknown, store.ErrStoreUnavailable, context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			m := inventory.New(inventory.WithCollectionCoverage())
			h := newHarness(t, m)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "coverage-store-outcome")
			run := event.InventoryRun{ID: "outcome-run", Tenant: tenant.String(), Registration: coverageRegistration(), StartedAt: time.Now().UTC()}
			scope := coverageScope()
			start := sdkmodel.InventoryCollectionStart{Scope: scope, ObservedAt: run.StartedAt}
			finish := event.InventoryFinish{Report: sdkmodel.InventoryCollectionReport{State: "complete", Reason: "exhausted", RequestedScope: scope.Fingerprint(), FulfilledScope: scope.Fingerprint(), ObservedUntil: run.StartedAt}, FinishedAt: run.StartedAt}
			if err := m.BeginRun(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			if err := m.StartCollection(context.Background(), run, start); err != nil {
				t.Fatal(err)
			}
			d := &coverageFaultData{ModuleData: api.NewModuleData(h.st), failure: failure, after: errors.Is(failure, store.ErrCommitOutcomeUnknown), failAt: 1}
			m.UseData(d)
			err := m.FinishRun(context.Background(), run, finish)
			if !errors.Is(err, failure) || errors.Is(err, inventory.ErrCollectionRejected) {
				t.Fatalf("Store identity lost: %v", err)
			}
			if d.calls.Load() != 1 {
				t.Fatal("uncertain effects retried")
			}
			m.UseData(api.NewModuleData(h.st))
			// Explicit test reconciliation after a known fixture mutation, not an
			// automatic product retry of an ambiguous operation.
			if errors.Is(failure, store.ErrCommitOutcomeUnknown) {
				before := coverageCurrent(t, h, admin, tenant, coverageRegistration())
				if before["last_qualified_success"] == nil {
					t.Fatal("fixture did not commit before losing acknowledgement")
				}
				if err := m.FinishRun(context.Background(), run, finish); err != nil {
					t.Fatal(err)
				}
				after := coverageCurrent(t, h, admin, tenant, coverageRegistration())
				a, _ := json.Marshal(before)
				b, _ := json.Marshal(after)
				if !bytes.Equal(a, b) {
					t.Fatal("exact replay changed durable result")
				}
			}
			bad := run
			bad.ID = ""
			if err := m.BeginRun(context.Background(), bad); !errors.Is(err, inventory.ErrCollectionRejected) || errors.Is(err, failure) {
				t.Fatalf("definite refusal lost: %v", err)
			}
		})
	}
}

// A synchronized writer allows the test to observe the runtime's external log
// sink while Gather runs, without reading recorder internals or racing a buffer.
type coverageLog struct {
	mu sync.Mutex
	bytes.Buffer
}

func (l *coverageLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Buffer.Write(b)
}
func (l *coverageLog) text() string { l.mu.Lock(); defer l.mu.Unlock(); return l.Buffer.String() }

func TestInventoryCoveragePersistenceDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		reason  string
		after   bool
	}{
		{"unknown", errors.Join(store.ErrCommitOutcomeUnknown, context.Canceled), "commit_outcome_unknown", true},
		{"unavailable", store.ErrStoreUnavailable, "persistence_unavailable", false},
		{"canceled", context.Canceled, "persistence_canceled", false},
		{"deadline", context.DeadlineExceeded, "persistence_canceled", false},
		{"unclassified", errors.New("synthetic secret fault"), "persistence_error", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := inventory.New(inventory.WithCollectionCoverage())
			h := newHarness(t, m)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "coverage-diagnostic")
			// Empty collection has exactly Begin, Start and Finish Mutate calls.
			d := &coverageFaultData{ModuleData: api.NewModuleData(h.st), failure: tc.failure, after: tc.after, failAt: 3}
			m.UseData(d)
			logs := &coverageLog{}
			rt := runtime.New(runtime.Options{Logger: slog.New(slog.NewJSONHandler(logs, nil))})
			if err := rt.AddModule(m, sdk.Config{}); err != nil {
				t.Fatal(err)
			}
			if err := rt.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = rt.Stop(ctx)
			})
			source := &coverageSource{gather: func(ctx context.Context, sink sdk.Sink) error { return coverageEmit(ctx, sink, false) }}
			if err := rt.AddPreparedSourceRegistered(context.Background(), "coverage-source", rt.PrepareInProcSource(source), sdk.Config{}, tenant.String(), 0, coverageRegistration()); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(4 * time.Second)
			for !strings.Contains(logs.text(), `"reason":"`+tc.reason+`"`) {
				if time.Now().After(deadline) {
					t.Fatalf("bounded reason missing: %s", logs.text())
				}
				time.Sleep(time.Millisecond * 5)
			}
			if text := logs.text(); strings.Contains(text, "synthetic") || strings.Contains(text, "persistence rejected") || strings.Contains(text, "aborted") {
				t.Fatalf("diagnostic leaked or invented rejection: %s", text)
			}
			if d.calls.Load() != 3 {
				t.Fatalf("unexpected mutation/retry count: %d", d.calls.Load())
			}
			item := coverageCurrent(t, h, admin, tenant, coverageRegistration())
			if tc.after && item["last_qualified_success"] == nil {
				t.Fatal("lost acknowledgement erased durable qualification")
			}
			if !tc.after && item["last_qualified_success"] != nil {
				t.Fatal("uncommitted fixture qualified")
			}
		})
	}
}

// The public document must describe the route the real inventory module mounts,
// including the actual unknown-selection JSON, not just a synthetic route shape.
func TestInventoryCoverageCollectionsPublishedContract(t *testing.T) {
	m := inventory.New(inventory.WithCollectionCoverage())
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "coverage-published-contract")
	doc := api.ModuleOpenAPIDocument([]api.Module{m})
	paths := doc["paths"].(map[string]any)
	operation := paths["/v1/m/inventory/collections"].(map[string]any)["get"].(map[string]any)
	required := map[string]bool{}
	for _, raw := range operation["parameters"].([]any) {
		parameter := raw.(map[string]any)
		if parameter["in"] == "query" {
			required[parameter["name"].(string)] = parameter["required"] == true
		}
	}
	if len(required) != 3 || !required["source_id"] || !required["source_revision"] || !required["environment_ref"] {
		t.Fatalf("published selectors: %v", required)
	}
	response := operation["responses"].(map[string]any)["200"].(map[string]any)
	page := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	item := page["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)
	current := item["properties"].(map[string]any)["current"].(map[string]any)
	got := coverageCurrent(t, h, admin, tenant, coverageRegistration())["current"].(map[string]any)
	if current["additionalProperties"] != false {
		t.Fatal("published current DTO is open")
	}
	properties := current["properties"].(map[string]any)
	for key := range got {
		if properties[key] == nil {
			t.Errorf("actual JSON field %s is unpublished", key)
		}
	}
	for _, field := range current["required"].([]any) {
		if _, ok := got[field.(string)]; !ok {
			t.Errorf("published required field %s absent in actual JSON", field)
		}
	}
	if got["coverage"] != "unknown" || got["run_id"] != "" || got["host_started_at"] != "" {
		t.Fatalf("unknown selection wire shape: %v", got)
	}
}
