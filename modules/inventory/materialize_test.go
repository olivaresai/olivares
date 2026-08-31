// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// baseTime is a fixed instant so tests are deterministic.
var baseTime = time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

// newInv opens a real SQLite store with the inventory schema, provisions a
// tenant, wires the module's data handle, and returns all three. The store is a
// real dual-engine store (via the public engine seam), so the tests exercise the
// real generic repository, extension tables and unique indexes — not a fake.
func newInv(t *testing.T, opts ...Option) (*Module, store.Store, model.TenantID) {
	t.Helper()
	m := New(opts...)
	ctx := context.Background()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	m.UseData(api.NewModuleData(st))
	return m, st, tenant
}

// mkEdge builds an edge observation.
func mkEdge(originKind, originRef, resKind, resRef string, mode sdkmodel.AccessMode, src sdkmodel.SignalSource, tool string, at time.Time) sdkmodel.EdgeObservation {
	return sdkmodel.EdgeObservation{
		OriginKind: originKind, OriginRef: originRef,
		ResourceKind: resKind, ResourceRef: resRef,
		Mode: mode, Source: src, Confidence: sdkmodel.ConfidenceAttributed,
		ToolRef: tool, ObservedAt: at,
	}
}

// countKind returns how many catalog entries of a kind exist in the tenant.
func countKind(t *testing.T, st store.Store, tenant model.TenantID, kind string) int {
	t.Helper()
	n := 0
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(catalogEntryKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{eq(colEntityKind, kind)}, Limit: listCap})
		n = len(recs)
		return err
	}); err != nil {
		t.Fatalf("countKind %s: %v", kind, err)
	}
	return n
}

// feed pushes a batch of edges through the materializer.
func (m *Module) feed(t *testing.T, tenant model.TenantID, edges ...sdkmodel.EdgeObservation) {
	t.Helper()
	for _, e := range edges {
		if err := m.onEdge(context.Background(), tenant.String(), e); err != nil {
			t.Fatalf("onEdge: %v", err)
		}
	}
}

// representativeBatch is a batch covering every origin and resource kind the
// contract emits, so one feed materializes the whole estate.
func representativeBatch(at time.Time) []sdkmodel.EdgeObservation {
	return []sdkmodel.EdgeObservation{
		// cooperative session activity (otel): a file read, an MCP tool use, an
		// MCP server connection and an MCP prompt.
		mkEdge("session", "sess-1", "file", "/etc/app/config.yaml", sdkmodel.ModeRead, sdkmodel.SignalOTEL, "Read", at),
		mkEdge("session", "sess-1", rkMCPTool, "github/create_issue", sdkmodel.ModeUnknown, sdkmodel.SignalOTEL, "mcp__github__create_issue", at),
		mkEdge("session", "sess-1", rkMCPServer, "github", sdkmodel.ModeUnknown, sdkmodel.SignalOTEL, "", at),
		mkEdge("session", "sess-1", rkMCPPrompt, "github/triage", sdkmodel.ModeUnknown, sdkmodel.SignalOTEL, "", at),
		// declared MCP capability (mcp_annotation, UNTRUSTED): the server exposes
		// create_issue as readwrite.
		mkEdge("mcp_server", "github", rkMCPTool, "github/create_issue", sdkmodel.ModeReadWrite, sdkmodel.SignalMCPAnnotation, "create_issue", at),
		// non-cooperative observed access (other connectors over the same bus):
		// an agent writes a file, an identity reads an object store.
		mkEdge("agent", "ci-bot", "file", "/var/lib/data.db", sdkmodel.ModeWrite, sdkmodel.SignalEBPF, "", at),
		mkEdge("identity", "arn:aws:iam::role/reader", "s3.bucket", "arn:aws:s3:::reports", sdkmodel.ModeRead, sdkmodel.SignalCloudTrail, "", at),
	}
}

func TestMaterializeDiscovery(t *testing.T) {
	m, st, tenant := newInv(t)
	m.feed(t, tenant, representativeBatch(baseTime)...)

	// Every entity kind the batch implies is cataloged.
	for kind, want := range map[string]int{
		kindSession:   1,
		kindAgent:     1,
		kindIdentity:  1,
		kindMCPServer: 1, // "github" deduped across the otel and annotation edges
		kindTool:      2, // create_issue (deduped otel-use+annotation) and the Read tool
		kindSkill:     1, // the triage prompt
	} {
		if got := countKind(t, st, tenant, kind); got != want {
			t.Errorf("catalog %s = %d, want %d", kind, got, want)
		}
	}
	// Resources: the two files and the s3 bucket (3).
	if got := countKind(t, st, tenant, kindResource); got != 3 {
		t.Errorf("catalog resource = %d, want 3", got)
	}

	// The core entities were really materialized, not just catalog rows.
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		sessions, _, _ := sc.Sessions().List(context.Background(), model.Query{})
		if len(sessions) != 1 || sessions[0].ExternalID != "sess-1" {
			t.Errorf("sessions = %+v", sessions)
		}
		// A cooperatively-discovered session has no agent reference: agent_id stays
		// unset (NULL), never an empty-string sentinel.
		if len(sessions) == 1 && !sessions[0].AgentID.IsZero() {
			t.Errorf("discovered session agent_id = %q, want unset", sessions[0].AgentID)
		}
		servers, _, _ := sc.MCPServers().List(context.Background(), model.Query{})
		if len(servers) != 1 || servers[0].Name != "github" {
			t.Errorf("mcp servers = %+v", servers)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestMaterializeCMADreamSurface proves the CMA kinds route to the right core
// entities: a dream job → Resource (no Tool fabricated from its ToolRef), a declared
// agent tool → Tool, a roster-named agent definition → Agent.
func TestMaterializeCMADreamSurface(t *testing.T) {
	m, st, tenant := newInv(t)
	m.feed(t, tenant,
		// The dream provenance: the pipeline session read a store and wrote the dream's
		// output; the dream itself is a workspace-carried governed object. The dream id
		// rides ToolRef on provenance edges and must NOT become a Tool.
		mkEdge("session", "sesn_dream", rkCMADream, "drm_1", sdkmodel.ModeRead, sdkmodel.SignalCMA, "drm_1", baseTime),
		mkEdge("session", "sesn_dream", rkCMAMemoryStore, "memstore_out", sdkmodel.ModeWrite, sdkmodel.SignalCMA, "drm_1", baseTime),
		// The declared (PERMITTED) agent surface: a built-in tool and a roster grant.
		mkEdge("agent", "agent_1", rkCMAAgentTool, "bash", sdkmodel.ModeUnknown, sdkmodel.SignalPolicy, "bash", baseTime),
		mkEdge("agent", "agent_1", rkCMAAgentDef, "agent_sub", sdkmodel.ModeUnknown, sdkmodel.SignalPolicy, "", baseTime),
	)

	// Resources: the dream + the output store (2). Tools: ONLY the declared agent tool
	// (the dream/store ToolRefs must not fabricate one). Agents: the coordinator origin
	// + the roster-named definition (2).
	if got := countKind(t, st, tenant, kindResource); got != 2 {
		t.Errorf("catalog resource = %d, want 2 (dream + output store)", got)
	}
	if got := countKind(t, st, tenant, kindTool); got != 1 {
		t.Errorf("catalog tool = %d, want 1 (bash only — no Tool from a dream id)", got)
	}
	if got := countKind(t, st, tenant, kindAgent); got != 2 {
		t.Errorf("catalog agent = %d, want 2 (coordinator + roster member)", got)
	}
}

func TestIdempotentDiscovery(t *testing.T) {
	m, st, tenant := newInv(t)
	batch := representativeBatch(baseTime)
	m.feed(t, tenant, batch...)
	firstSession := countKind(t, st, tenant, kindSession)
	firstResource := countKind(t, st, tenant, kindResource)

	// Re-deliver the identical batch (at-least-once): discovery is idempotent —
	// no catalog entity is duplicated (the AccessEdge graph itself is ).
	m.feed(t, tenant, batch...)
	if got := countKind(t, st, tenant, kindSession); got != firstSession {
		t.Errorf("sessions after redelivery = %d, want %d", got, firstSession)
	}
	if got := countKind(t, st, tenant, kindResource); got != firstResource {
		t.Errorf("resources after redelivery = %d, want %d", got, firstResource)
	}
}

func TestSweepStaleness(t *testing.T) {
	m, st, tenant := newInv(t)
	m.feed(t, tenant, mkEdge("session", "sess-stale", "file", "/x", sdkmodel.ModeRead, sdkmodel.SignalOTEL, "Read", baseTime))

	// Nothing is stale right after observation.
	if n, err := m.Sweep(context.Background(), baseTime.Add(1*time.Minute)); err != nil || n != 0 {
		t.Fatalf("early sweep = %d,%v want 0,nil", n, err)
	}
	// Past the staleAfter window the entries go stale.
	n, err := m.Sweep(context.Background(), baseTime.Add(defaultStaleAfter+time.Minute))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n == 0 {
		t.Fatal("sweep marked nothing stale")
	}
	if got := statusOfKind(t, st, tenant, kindSession); got != statusStale {
		t.Errorf("session status after sweep = %q, want stale", got)
	}
	// Re-seeing the session flips it back to active.
	m.feed(t, tenant, mkEdge("session", "sess-stale", "file", "/x", sdkmodel.ModeRead, sdkmodel.SignalOTEL, "Read", baseTime.Add(time.Hour)))
	if got := statusOfKind(t, st, tenant, kindSession); got != statusActive {
		t.Errorf("session status after re-seen = %q, want active", got)
	}
}

func TestCostMaterializesProviderAndModel(t *testing.T) {
	m, st, tenant := newInv(t)
	if err := m.onCost(context.Background(), tenant.String(), sdkmodel.CostSample{
		ProviderRef: "anthropic", ModelRef: "claude-opus-4-8", SessionRef: "sess-1",
		InputTokens: 100, OutputTokens: 50, CostMicroUSD: 1234, OccurredAt: baseTime,
	}); err != nil {
		t.Fatalf("onCost: %v", err)
	}
	if got := countKind(t, st, tenant, kindProvider); got != 1 {
		t.Errorf("providers = %d, want 1", got)
	}
	if got := countKind(t, st, tenant, kindModel); got != 1 {
		t.Errorf("models = %d, want 1", got)
	}
	// Inventory discovers provider/model but does NOT write the CostRecord ledger
	// (that is FinOps /).
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		costs, _, err := sc.Costs().List(context.Background(), model.Query{})
		if err != nil {
			return err
		}
		if len(costs) != 0 {
			t.Errorf("inventory wrote %d cost records, want 0 (owns the ledger)", len(costs))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// statusOfKind returns the catalog status of the single entry of a kind.
func statusOfKind(t *testing.T, st store.Store, tenant model.TenantID, kind string) string {
	t.Helper()
	status := ""
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(catalogEntryKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{eq(colEntityKind, kind)}, Limit: 1})
		if err != nil {
			return err
		}
		if len(recs) > 0 {
			status = recs[0].String(colStatus)
		}
		return nil
	}); err != nil {
		t.Fatalf("statusOfKind: %v", err)
	}
	return status
}
