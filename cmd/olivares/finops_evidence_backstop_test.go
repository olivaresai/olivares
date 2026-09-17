// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Only the dynamic enumeration is incomplete; writer locks and real SQLite
// alert rows remain authoritative. This is a read-signal fixture, not a new ledger.
type capBoundData struct{ api.ModuleData }

func (d capBoundData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error { return fn(capBoundScope{Scope: sc}) })
}

type capBoundScope struct{ store.Scope }

func (s capBoundScope) LockTransaction(ctx context.Context, key string) error {
	l, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("missing real transaction lock")
	}
	return l.LockTransaction(ctx, key)
}
func (s capBoundScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	r, err := s.Scope.Ext(kind)
	if err != nil || kind != "finops.budget_reservation" {
		return r, err
	}
	return capBoundRepo{GenericRepo: r}, nil
}

type capBoundRepo struct{ store.GenericRepo }

func (r capBoundRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	if len(q.Sort) > 0 {
		return r.GenericRepo.List(ctx, q)
	}
	return nil, model.Page{HasMore: true}, nil
}

type observedEvidenceResolver struct {
	*finops.Module
	legacyCalls int
}

func (r *observedEvidenceResolver) BudgetCapTarget(context.Context, model.TenantID, string) (string, string, bool, error) {
	r.legacyCalls++
	return "api_key", "key_redirected", true, nil
}

func realCapEvidence(t *testing.T, bound bool) (*finops.Module, model.TenantID, event.Event) {
	t.Helper()
	ctx := context.Background()
	fin := finops.New()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, fin.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err = st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "cap-evidence", Slug: "cap-evidence", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	data := api.NewModuleData(st)
	if bound {
		fin.UseData(capBoundData{ModuleData: data})
	} else {
		fin.UseData(data)
	}
	createBudgetPolicy(t, st, tenant, "captured-cap", map[string]any{"dimension": "api_key", "key": "key_original", "period": "monthly", "limit_micro_usd": int64(10), "reserved_micro_usd": int64(12), "action": "block", "thresholds": []float64{1}})
	bus := eventbus.NewInProc(eventbus.Options{})
	t.Cleanup(func() { _ = bus.Close() })
	ch := make(chan event.Event, 1)
	unsub, err := bus.Subscribe([]event.Type{event.TypeFindingReported}, func(_ context.Context, e event.Event) error {
		if e.Source == finops.Name {
			select {
			case ch <- e:
			default:
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unsub.Unsubscribe)
	rt := runtime.New(runtime.Options{Bus: bus})
	if err = rt.AddModule(fin, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err = rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Stop(ctx) })
	publishCost(t, bus, tenant, sdkmodel.CostSample{ProviderRef: "anthropic", ModelRef: "model", APIKeyRef: "key_original", CostMicroUSD: 0, OccurredAt: time.Now().UTC()})
	select {
	case e := <-ch:
		return fin, tenant, e
	case <-time.After(5 * time.Second):
		t.Fatal("real producer did not publish a committed cap")
		return nil, "", event.Event{}
	}
}

func TestBudgetEvidenceBackstopGovernedTarget(t *testing.T) {
	for _, class := range []string{"exact", "lower_bound"} {
		t.Run(class, func(t *testing.T) {
			fin, tenant, e := realCapEvidence(t, class == "lower_bound")
			f, ok := event.FindingOf(e)
			if !ok || f.BudgetEvidence == nil || f.BudgetEvidence.AmountClass != class || f.BudgetEvidence.Crossing != "proven" {
				t.Fatalf("producer=%+v", e)
			}
			resolver := &observedEvidenceResolver{Module: fin}
			allow := []claudeapi.AdminAllowRule{{Action: claudeapi.ActionDeactivateKey, Subjects: []string{"key_original", "key_redirected"}}}
			bs, d := backstopWith(tenant, cmdStubAdminGate{status: claudeapi.AdminApproved, approvers: []string{"human"}}, allow, resolver, false)
			if err := bs.onFinding(context.Background(), e); err != nil {
				t.Fatal(err)
			}
			if reqs := d.snapshot(); len(reqs) != 1 || reqs[0].path != "/v1/organizations/api_keys/key_original" || resolver.legacyCalls != 0 {
				t.Fatalf("lost validated target: %+v legacy=%d", reqs, resolver.legacyCalls)
			}
			// Durable proof does not authorize a pending HITL request or override allowlist.
			pending, pd := backstopWith(tenant, cmdStubAdminGate{status: claudeapi.AdminPending}, allow, resolver, false)
			_ = pending.onFinding(context.Background(), e)
			if len(pd.snapshot()) != 0 {
				t.Fatal("proof bypassed pending HITL")
			}
			denied, dd := backstopWith(tenant, cmdStubAdminGate{status: claudeapi.AdminApproved, approvers: []string{"human"}}, nil, resolver, false)
			_ = denied.onFinding(context.Background(), e)
			if len(dd.snapshot()) != 0 {
				t.Fatal("proof bypassed allowlist")
			}
		})
	}
}

type refusedEvidenceResolver struct {
	legacyCalls, evidenceCalls int
	err                        error
}

func (r *refusedEvidenceResolver) BudgetCapTarget(context.Context, model.TenantID, string) (string, string, bool, error) {
	r.legacyCalls++
	return "api_key", "key_original", true, nil
}
func (r *refusedEvidenceResolver) BudgetEvidenceCapTarget(context.Context, model.TenantID, sdkmodel.FindingReport) (string, string, bool, error) {
	r.evidenceCalls++
	return "", "", false, r.err
}
func TestBudgetEvidenceBackstopNoFallback(t *testing.T) {
	fin, tenant, original := realCapEvidence(t, false)
	for _, tc := range []string{"foreign", "registration", "missing_resolver", "invalid", "unknown", "lookup_error", "unverified", "hash_mismatch", "legacy"} {
		t.Run(tc, func(t *testing.T) {
			e := original
			f, _ := event.FindingOf(e)
			f.BudgetEvidence = f.BudgetEvidence.Clone()
			var res budgetCapResolver = fin
			refused := &refusedEvidenceResolver{}
			switch tc {
			case "foreign":
				e.Source = "foreign"
			case "registration":
				e.SourceRegistration = &event.SourceRegistration{}
			case "missing_resolver":
				res = stubCapResolver{dimension: "api_key", key: "key_original", ok: true}
			case "invalid":
				f.BudgetEvidence.SchemaVersion = 42
			case "unknown":
				f.Kind = "finops_budget_evaluation_incomplete"
				f.Severity = sdkmodel.SeverityMedium
				f.BudgetEvidence.AlertID = ""
				f.BudgetEvidence.AmountClass = "unknown"
				f.BudgetEvidence.Crossing = "unproven"
				f.BudgetEvidence.Causes = []string{"cost_read_failed"}
			case "lookup_error":
				refused.err = errors.New("unavailable")
				res = refused
			case "unverified":
				res = refused
			case "hash_mismatch":
				f.DetailHash = strings.Repeat("f", 64)
			case "legacy":
				f.BudgetEvidence = nil
				res = refused
				e.Source = "uncertified-legacy"
			}
			e.Payload = f
			bs, d := backstopWith(tenant, cmdStubAdminGate{status: claudeapi.AdminApproved, approvers: []string{"human"}}, []claudeapi.AdminAllowRule{{Action: claudeapi.ActionDeactivateKey, Subjects: []string{"key_original"}}}, res, false)
			_ = bs.onFinding(context.Background(), e)
			want := 0
			if tc == "legacy" {
				want = 1
			}
			if len(d.snapshot()) != want || refused.legacyCalls != want {
				t.Fatalf("calls=%d legacy=%d want %d", len(d.snapshot()), refused.legacyCalls, want)
			}
			if (tc == "lookup_error" || tc == "unverified") && refused.evidenceCalls != 1 {
				t.Fatal("resolver not exercised")
			}
		})
	}
}
