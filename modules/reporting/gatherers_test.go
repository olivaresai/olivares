// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var (
	e3bFrom = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	e3bTo   = time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC)
)

func TestGatherAuditDataAggregatesFilteredLedgerActions(t *testing.T) {
	h := newGatherHarness(t, nil)
	ctx := context.Background()
	h.clock.set(time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC))
	h.appendAudit(t, "outside.before")
	h.clock.set(time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC))
	h.appendAudit(t, "agent.create")
	h.clock.set(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC))
	h.appendAudit(t, "policy.update")
	h.clock.set(time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC))
	h.appendAudit(t, "agent.create")
	h.clock.set(time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC))
	h.appendAudit(t, "outside.after")

	got, err := h.module.gatherAuditData(ctx, h.mc.Data, ReportParams{From: e3bFrom, To: e3bTo})
	if err != nil {
		t.Fatalf("gatherAuditData: %v", err)
	}
	if got.LedgerHead != 6 || !got.CheckpointOK || got.CheckpointCount != 6 {
		t.Fatalf("ledger summary = head:%d ok:%v checked:%d", got.LedgerHead, got.CheckpointOK, got.CheckpointCount)
	}
	if got.TotalEvents != 3 {
		t.Fatalf("TotalEvents = %d, want 3", got.TotalEvents)
	}
	counts := actionCounts(got.EventsByAction)
	if counts["agent.create"] != 2 || counts["policy.update"] != 1 {
		t.Fatalf("EventsByAction = %+v, want agent.create=2 policy.update=1", counts)
	}
	if counts["outside.before"] != 0 || counts["outside.after"] != 0 {
		t.Fatalf("outside events leaked into range: %+v", counts)
	}
}

func TestGatherFinOpsDataAggregatesTotalsAndBuckets(t *testing.T) {
	h := newGatherHarness(t, nil)
	modelA, modelB, providerA := model.NewID(), model.NewID(), model.NewID()
	h.seedCosts(t, []model.CostRecord{
		costAt(modelA, providerA, 10, 5, 1_000_000, time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)),
		costAt(modelA, providerA, 20, 10, 2_000_000, time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)),
		costAt(modelB, "", 1, 2, 500_000, time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)),
		costAt(model.NewID(), model.NewID(), 99, 99, 9_999_999, time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)),
	})

	got, err := h.module.gatherFinOpsData(context.Background(), h.mc.Data, ReportParams{From: e3bFrom, To: e3bTo})
	if err != nil {
		t.Fatalf("gatherFinOpsData: %v", err)
	}
	if got.TotalMicroUSD != 3_500_000 || got.InputTokens != 31 || got.OutputTokens != 17 || got.Samples != 3 {
		t.Fatalf("finops totals = %+v, want cost=3500000 input=31 output=17 samples=3", got)
	}
	byModel := bucketMap(got.ByModel)
	assertBucket(t, byModel[modelA.String()], 3_000_000, 30, 15, 2)
	assertBucket(t, byModel[modelB.String()], 500_000, 1, 2, 1)
	byProvider := bucketMap(got.ByProvider)
	assertBucket(t, byProvider[providerA.String()], 3_000_000, 30, 15, 2)
	assertBucket(t, byProvider["(unknown)"], 500_000, 1, 2, 1)
}

func TestGatherAccessDataListsIdentityUsers(t *testing.T) {
	h := newGatherHarness(t, nil)
	ids := h.seedIdentities(t, []model.Identity{
		{Name: "Alice Admin", Kind: "iam_principal", ExternalID: "alice"},
		{Name: "Build Bot", Kind: "service_account", ExternalID: "bot"},
	})

	got, err := h.module.gatherAccessData(context.Background(), h.mc.Data)
	if err != nil {
		t.Fatalf("gatherAccessData: %v", err)
	}
	if got.Generated.IsZero() {
		t.Fatal("Generated is zero")
	}
	users := userMap(got.Users)
	if users[ids[0].String()] != "Alice Admin" || users[ids[1].String()] != "Build Bot" {
		t.Fatalf("users = %+v, want seeded identities", users)
	}
}

func TestGatherExecutiveDataAggregatesOperationalSpendFindingsAndCompliance(t *testing.T) {
	src := &e3bComplianceSource{data: ComplianceData{Frameworks: []FrameworkReport{
		{Name: "ISO 42001", Summary: AssessmentSummary{Total: 4, Satisfied: 2, ByDesign: 1, Gap: 1}},
		{Name: "Empty Framework", Summary: AssessmentSummary{Total: 0, Gap: 2}},
	}}}
	h := newGatherHarness(t, src)
	agentIDs := h.seedAgents(t, 2)
	h.seedSessions(t, []model.Session{
		{AgentID: agentIDs[0], ExternalID: "sess-1", State: model.SessionRunning, StartedAt: model.NewTimestamp(e3bFrom.Add(2 * time.Hour))},
	})
	h.seedIdentities(t, []model.Identity{
		{Name: "Alice", Kind: "iam_principal", ExternalID: "alice"},
		{Name: "Deploy Bot", Kind: "service_account", ExternalID: "deploy"},
	})
	h.seedFindings(t, []model.Finding{
		{Kind: "guardrail", Severity: model.SeverityCritical, Status: model.FindingOpen, Source: "test", Title: "critical open", OccurredAt: model.NewTimestamp(e3bFrom)},
		{Kind: "guardrail", Severity: model.SeverityHigh, Status: model.FindingOpen, Source: "test", Title: "high open", OccurredAt: model.NewTimestamp(e3bFrom)},
		{Kind: "guardrail", Severity: model.SeverityCritical, Status: model.FindingResolved, Source: "test", Title: "closed critical", OccurredAt: model.NewTimestamp(e3bFrom)},
	})
	h.clock.set(time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC))
	h.seedCosts(t, []model.CostRecord{costAt(model.NewID(), model.NewID(), 1, 1, 2_500_000, e3bFrom)})
	h.clock.set(time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC))
	h.seedCosts(t, []model.CostRecord{costAt(model.NewID(), model.NewID(), 1, 1, 9_999_999, e3bTo.Add(24*time.Hour))})
	h.clock.set(time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC))
	h.appendAudit(t, "reporting.seed")

	got, err := h.module.gatherExecutiveData(context.Background(), h.mc, ReportParams{From: e3bFrom, To: e3bTo})
	if err != nil {
		t.Fatalf("gatherExecutiveData: %v", err)
	}
	if got.ActiveAgents != 2 || got.ActiveSessions != 1 || got.ActiveUsers != 2 {
		t.Fatalf("active counts = agents:%d sessions:%d users:%d", got.ActiveAgents, got.ActiveSessions, got.ActiveUsers)
	}
	if got.FindingsOpen != 2 || got.FindingsCritical != 1 {
		t.Fatalf("finding counts = open:%d critical:%d", got.FindingsOpen, got.FindingsCritical)
	}
	if math.Abs(got.TotalSpendUSD-2.5) > 0.000001 {
		t.Fatalf("TotalSpendUSD = %.4f, want 2.5", got.TotalSpendUSD)
	}
	if !got.AuditIntegrityOK {
		t.Fatal("AuditIntegrityOK = false, want true")
	}
	if len(got.ComplianceSummary) != 2 {
		t.Fatalf("ComplianceSummary = %d entries, want 2", len(got.ComplianceSummary))
	}
	if got.ComplianceSummary[0].Name != "ISO 42001" || got.ComplianceSummary[0].SatisfiedPct != 75 || got.ComplianceSummary[0].GapCount != 1 {
		t.Fatalf("first compliance summary = %+v", got.ComplianceSummary[0])
	}
	if got.ComplianceSummary[1].SatisfiedPct != 0 || got.ComplianceSummary[1].GapCount != 2 {
		t.Fatalf("zero-total compliance summary = %+v", got.ComplianceSummary[1])
	}
}

func TestGatherDataDispatchesAllReportTypes(t *testing.T) {
	src := &e3bComplianceSource{data: ComplianceData{Frameworks: []FrameworkReport{{ID: "iso_27001"}}}}
	h := newGatherHarness(t, src)
	h.appendAudit(t, "agent.create")
	h.seedCosts(t, []model.CostRecord{costAt(model.NewID(), model.NewID(), 2, 3, 1_250_000, e3bFrom)})
	h.seedIdentities(t, []model.Identity{{Name: "Alice", Kind: "iam_principal", ExternalID: "alice"}})
	h.seedAgents(t, 1)

	tests := []struct {
		rt   ReportType
		want any
	}{
		{ReportComplianceEvidence, ComplianceData{}},
		{ReportAuditSummary, AuditData{}},
		{ReportFinOps, FinOpsData{}},
		{ReportAccessReview, AccessData{}},
		{ReportExecutiveSummary, ExecutiveData{}},
	}
	for _, tt := range tests {
		t.Run(string(tt.rt), func(t *testing.T) {
			got, err := h.module.gatherData(context.Background(), h.mc, ReportParams{Type: tt.rt, From: e3bFrom, To: e3bTo, Framework: "iso_27001"})
			if err != nil {
				t.Fatalf("gatherData(%s): %v", tt.rt, err)
			}
			switch tt.want.(type) {
			case ComplianceData:
				if got.(ComplianceData).Frameworks[0].ID != "iso_27001" || src.framework != "iso_27001" {
					t.Fatalf("compliance dispatch = %+v framework=%q", got, src.framework)
				}
			case AuditData:
				if got.(AuditData).TotalEvents != 1 {
					t.Fatalf("audit dispatch = %+v, want 1 event", got)
				}
			case FinOpsData:
				if got.(FinOpsData).TotalMicroUSD != 1_250_000 {
					t.Fatalf("finops dispatch = %+v, want cost 1250000", got)
				}
			case AccessData:
				if len(got.(AccessData).Users) != 1 {
					t.Fatalf("access dispatch = %+v, want 1 user", got)
				}
			case ExecutiveData:
				if got.(ExecutiveData).ActiveAgents != 1 {
					t.Fatalf("executive dispatch = %+v, want 1 agent", got)
				}
			}
		})
	}
	if _, err := h.module.gatherData(context.Background(), h.mc, ReportParams{Type: ReportType("unknown")}); err == nil {
		t.Fatal("gatherData(unknown) error = nil, want error")
	}
}

func TestBucketHelpersAccumulateUnknownAndReturnValueCopies(t *testing.T) {
	buckets := map[string]*SpendBucket{}
	accumBucket(buckets, "model-a", 100, 10, 1)
	accumBucket(buckets, "model-a", 250, 20, 2)
	accumBucket(buckets, "", 50, 3, 4)
	assertBucket(t, buckets["model-a"], 350, 30, 3, 2)
	assertBucket(t, buckets["(unknown)"], 50, 3, 4, 1)

	slice := toBucketSlice(buckets)
	got := bucketMap(slice)
	assertBucket(t, got["model-a"], 350, 30, 3, 2)
	buckets["model-a"].CostMicroUSD = 999
	if got["model-a"].CostMicroUSD != 350 {
		t.Fatalf("toBucketSlice returned aliased bucket, got cost %d", got["model-a"].CostMicroUSD)
	}
}

type gatherHarness struct {
	t      *testing.T
	store  store.Store
	clock  *reportingTestClock
	module *Module
	tenant model.TenantID
	mc     api.ModuleContext
}

func newGatherHarness(t *testing.T, compliance ComplianceSource) *gatherHarness {
	t.Helper()
	ctx := context.Background()
	clk := &reportingTestClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true, Clock: clk}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme", Status: model.StatusActive})
		if err != nil {
			return err
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	m := New(WithComplianceSource(compliance))
	m.UseData(api.NewModuleData(st))
	clk.set(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	return &gatherHarness{
		t: t, store: st, clock: clk, module: m, tenant: tenant,
		mc: api.ModuleContext{Tenant: tenant, Data: api.NewScopedData(st, tenant)},
	}
}

func (h *gatherHarness) appendAudit(t *testing.T, action string) {
	t.Helper()
	if err := h.store.Mutate(context.Background(), h.tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(context.Background(), model.AuditDraft{
			Actor: "user:test", ActorKind: model.ActorUser, Action: action,
		})
		return err
	}); err != nil {
		t.Fatalf("append audit %s: %v", action, err)
	}
}

func (h *gatherHarness) seedCosts(t *testing.T, costs []model.CostRecord) {
	t.Helper()
	if err := h.store.Mutate(context.Background(), h.tenant, func(sc store.Scope) error {
		for _, c := range costs {
			if _, err := sc.Costs().Create(context.Background(), c); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed costs: %v", err)
	}
}

func (h *gatherHarness) seedIdentities(t *testing.T, identities []model.Identity) []model.ID {
	t.Helper()
	ids := make([]model.ID, 0, len(identities))
	if err := h.store.Mutate(context.Background(), h.tenant, func(sc store.Scope) error {
		for _, identity := range identities {
			created, err := sc.Identities().Create(context.Background(), identity)
			if err != nil {
				return err
			}
			ids = append(ids, created.ID)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed identities: %v", err)
	}
	return ids
}

func (h *gatherHarness) seedAgents(t *testing.T, n int) []model.ID {
	t.Helper()
	ids := make([]model.ID, 0, n)
	if err := h.store.Mutate(context.Background(), h.tenant, func(sc store.Scope) error {
		for i := 0; i < n; i++ {
			created, err := sc.Agents().Create(context.Background(), model.Agent{
				Name: "agent", Kind: "claude-code", ExternalID: model.NewID().String(), Status: model.StatusActive,
			})
			if err != nil {
				return err
			}
			ids = append(ids, created.ID)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed agents: %v", err)
	}
	return ids
}

func (h *gatherHarness) seedSessions(t *testing.T, sessions []model.Session) {
	t.Helper()
	if err := h.store.Mutate(context.Background(), h.tenant, func(sc store.Scope) error {
		for _, s := range sessions {
			if _, err := sc.Sessions().Create(context.Background(), s); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
}

func (h *gatherHarness) seedFindings(t *testing.T, findings []model.Finding) {
	t.Helper()
	if err := h.store.Mutate(context.Background(), h.tenant, func(sc store.Scope) error {
		for _, f := range findings {
			if _, err := sc.Findings().Create(context.Background(), f); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed findings: %v", err)
	}
}

func costAt(modelID, providerID model.ID, in, out, cost int64, at time.Time) model.CostRecord {
	return model.CostRecord{
		ModelID: modelID, ProviderID: providerID,
		OccurredAt: model.NewTimestamp(at), InputTokens: in, OutputTokens: out,
		CostMicroUSD: cost, Currency: "USD",
	}
}

type reportingTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *reportingTestClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.now)
}

func (c *reportingTestClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type e3bComplianceSource struct {
	data      ComplianceData
	tenant    model.TenantID
	framework string
}

func (s *e3bComplianceSource) GatherComplianceData(_ context.Context, tenant model.TenantID, framework string) (ComplianceData, error) {
	s.tenant = tenant
	s.framework = framework
	return s.data, nil
}

func actionCounts(in []ActionCount) map[string]int64 {
	out := map[string]int64{}
	for _, c := range in {
		out[c.Action] = c.Count
	}
	return out
}

func bucketMap(in []SpendBucket) map[string]*SpendBucket {
	out := map[string]*SpendBucket{}
	for i := range in {
		b := in[i]
		out[b.Key] = &b
	}
	return out
}

func assertBucket(t *testing.T, got *SpendBucket, cost, in, out int64, samples int) {
	t.Helper()
	if got == nil {
		t.Fatalf("bucket missing, want cost=%d input=%d output=%d samples=%d", cost, in, out, samples)
	}
	if got.CostMicroUSD != cost || got.InputTokens != in || got.OutputTokens != out || got.Samples != samples {
		t.Fatalf("bucket = %+v, want cost=%d input=%d output=%d samples=%d", *got, cost, in, out, samples)
	}
}

func userMap(in []UserAccess) map[string]string {
	out := map[string]string{}
	for _, u := range in {
		out[u.UserID] = u.DisplayName
	}
	return out
}
