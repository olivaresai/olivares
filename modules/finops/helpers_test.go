// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// baseTime is a fixed instant inside June 2026 so period math is deterministic.
var baseTime = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

// fakeHost is an in-test sdk.Host that captures published events, so the alert
// emission path (host.Publish of a FindingReport) can be asserted.
type fakeHost struct {
	mu     sync.Mutex
	events []event.Event
}

func (h *fakeHost) Publish(_ context.Context, e event.Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
	return nil
}
func (h *fakeHost) Subscribe([]event.Type, event.Handler) (func(), error) { return func() {}, nil }
func (h *fakeHost) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
func (h *fakeHost) Config() sdk.Config { return sdk.Config{} }

type finopsTestData struct {
	api.ModuleData
	st store.Store
}

func (d finopsTestData) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	return d.st.AuthView(ctx, fn)
}

// forcedAggregateData replaces only the cost-sample repository on read paths.
// It lets enforcement tests hit the scan cap without inserting one million rows.
type forcedAggregateData struct {
	api.ModuleData
	costPerPage int64
	truncated   bool
}

func (d forcedAggregateData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error {
		return fn(forcedAggregateScope{Scope: sc, repo: forcedAggregateRepo{
			costPerPage: d.costPerPage,
			truncated:   d.truncated,
		}})
	})
}

type forcedAggregateScope struct {
	store.Scope
	repo store.GenericRepo
}

func (s forcedAggregateScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	if kind == costSampleKind {
		return s.repo, nil
	}
	return s.Scope.Ext(kind)
}

type forcedAggregateRepo struct {
	store.GenericRepo
	costPerPage int64
	truncated   bool
}

func (r forcedAggregateRepo) List(context.Context, model.Query) ([]model.Record, model.Page, error) {
	page := model.Page{}
	if r.truncated {
		page = model.Page{Cursor: "forced-next-page", HasMore: true}
	}
	return []model.Record{{
		colCostMicroUSD: r.costPerPage,
		colInputTokens:  int64(1),
		colOutputTokens: int64(1),
	}}, page, nil
}

func forceAggregateResult(m *Module, costPerPage int64, truncated bool) {
	m.UseData(forcedAggregateData{ModuleData: m.data, costPerPage: costPerPage, truncated: truncated})
}

// findings returns the captured finding.reported events.
func (h *fakeHost) findings() []sdkmodel.FindingReport {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []sdkmodel.FindingReport
	for _, e := range h.events {
		if f, ok := event.FindingOf(e); ok {
			out = append(out, f)
		}
	}
	return out
}

// newFin opens a real SQLite store with the module schema, provisions a tenant,
// wires the data handle and a capturing host. It takes testing.TB so benchmarks
// (reserve-ledger hot path) can reuse the exact setup.
func newFin(t testing.TB) (*Module, store.Store, model.TenantID, *fakeHost) {
	t.Helper()
	return openFinCfg(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
}

// openFinCfg is newFin parametrized by store config, so the cross-backend
// race test can open the SAME module against Postgres (a real concurrent writer).
// Since the Postgres leg gets its own database; the unique org slug is kept
// so the helper stays safe on any reused database.
func openFinCfg(t testing.TB, cfg store.Config) (*Module, store.Store, model.TenantID, *fakeHost) {
	t.Helper()
	m := New()
	fh := &fakeHost{}
	m.host = fh
	ctx := context.Background()
	st, err := engine.Open(ctx, cfg, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	slug := "acme-" + uniqueSlugSuffix(t)
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	m.UseData(finopsTestData{ModuleData: api.NewModuleData(st), st: st})
	return m, st, tenant, fh
}

// mkCost builds a cost sample.
func mkCost(provider, modelRef, session string, in, out, cost int64, at time.Time) sdkmodel.CostSample {
	return sdkmodel.CostSample{
		ProviderRef: provider, ModelRef: modelRef, SessionRef: session,
		InputTokens: in, OutputTokens: out, CostMicroUSD: cost, OccurredAt: at,
	}
}

// ingest pushes a sample through the ingestion path.
func (m *Module) ingest(t *testing.T, tenant model.TenantID, c sdkmodel.CostSample) {
	t.Helper()
	if err := m.onCost(context.Background(), tenant, c, nil); err != nil {
		t.Fatalf("onCost: %v", err)
	}
}

// countCosts returns the number of canonical CostRecord rows in the tenant.
func countCosts(t *testing.T, st store.Store, tenant model.TenantID) int {
	t.Helper()
	n := 0
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		recs, _, err := sc.Costs().List(context.Background(), model.Query{Limit: listCap})
		n = len(recs)
		return err
	}); err != nil {
		t.Fatalf("countCosts: %v", err)
	}
	return n
}

// createIdentity stores a roster Identity (NHI) and returns its id.
func createIdentity(t *testing.T, st store.Store, tenant model.TenantID, externalID, kind, provider string) model.ID {
	t.Helper()
	var id model.ID
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		i, err := sc.Identities().Create(context.Background(), model.Identity{
			Name: externalID, Kind: kind, ExternalID: externalID, Provider: provider,
		})
		id = i.ID
		return err
	}); err != nil {
		t.Fatalf("createIdentity: %v", err)
	}
	return id
}

// createAgent stores an Agent (optionally bound to a firm identity) and returns its id.
func createAgent(t *testing.T, st store.Store, tenant model.TenantID, name, externalID string, identityID model.ID) model.ID {
	t.Helper()
	var id model.ID
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Create(context.Background(), model.Agent{
			Name: name, ExternalID: externalID, IdentityID: identityID,
		})
		id = a.ID
		return err
	}); err != nil {
		t.Fatalf("createAgent: %v", err)
	}
	return id
}

func createAgentGroup(t *testing.T, st store.Store, tenant model.TenantID, slug string, agentIDs ...model.ID) model.AgentGroup {
	t.Helper()
	var group model.AgentGroup
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		g, err := sc.AgentGroups().Create(context.Background(), model.AgentGroup{
			Name: slug, Slug: slug, Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		group = g
		for _, agentID := range agentIDs {
			if _, err := sc.AgentGroupMembers().Create(context.Background(), model.AgentGroupMember{GroupID: g.ID, AgentID: agentID}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("createAgentGroup: %v", err)
	}
	return group
}

func createCanonicalUser(t *testing.T, st store.Store, name string) model.User {
	t.Helper()
	var user model.User
	if err := st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		var err error
		user, err = as.Users().Create(context.Background(), model.User{
			Email:       "finops-" + model.NewID().String() + "@example.test",
			DisplayName: name,
			Status:      model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("create canonical User %q: %v", name, err)
	}
	return user
}

func createUserGroup(t *testing.T, st store.Store, tenant model.TenantID, name string, userIDs ...model.ID) model.UserGroup {
	t.Helper()
	var group model.UserGroup
	if err := st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		g, err := as.Groups().Create(context.Background(), model.UserGroup{
			TargetTenantID: tenant, DisplayName: name, ExternalID: name,
		})
		if err != nil {
			return err
		}
		group = g
		for _, userID := range userIDs {
			if _, err := as.GroupMembers().Create(context.Background(), model.UserGroupMember{GroupID: g.ID, UserID: userID}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("createUserGroup: %v", err)
	}
	return group
}

// createSession stores a Session owned by an agent (the session→agent→identity chain).
func createSession(t *testing.T, st store.Store, tenant model.TenantID, externalID string, agentID model.ID) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Sessions().Create(context.Background(), model.Session{
			ExternalID: externalID, AgentID: agentID,
		})
		return err
	}); err != nil {
		t.Fatalf("createSession: %v", err)
	}
}

// ingestOutcomeT pushes a graded outcome through the ingestion path.
func (m *Module) ingestOutcomeT(t *testing.T, tenant model.TenantID, in outcomeIngestRequest) {
	t.Helper()
	if err := m.ingestOutcome(context.Background(), tenant, in, nil); err != nil {
		t.Fatalf("ingestOutcome: %v", err)
	}
}

// costSampleRows returns the finops cost_sample read-model rows in a tenant.
func costSampleRows(t *testing.T, st store.Store, tenant model.TenantID) []model.Record {
	t.Helper()
	var out []model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(costSampleKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: listCap})
		out = recs
		return err
	}); err != nil {
		t.Fatalf("costSampleRows: %v", err)
	}
	return out
}

// createBudget stores a budget policy and returns its id.
func createBudget(t testing.TB, st store.Store, tenant model.TenantID, name string, spec budgetSpec) model.ID {
	t.Helper()
	var id model.ID
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Create(context.Background(), model.Policy{
			Name: name, Kind: policyKindBudget, Enabled: true, Spec: spec.toSpecMap(),
		})
		id = p.ID
		return err
	}); err != nil {
		t.Fatalf("createBudget: %v", err)
	}
	return id
}

// uniqueSlugSuffix returns a short random org-slug suffix.
//
// this was string(model.NewID())[:8]. model.NewID is a UUIDv7, whose first
// 8 hex characters are the TOP 32 bits of its 48-bit millisecond timestamp, so
// every call within the same ~65 second window returned the SAME value (measured:
// 1000 consecutive calls produced one distinct value). "Unique" it was not.
func uniqueSlugSuffix(t testing.TB) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("read entropy: %v", err)
	}
	return hex.EncodeToString(b[:])
}
