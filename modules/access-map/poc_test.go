// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
	"time"

	pgaudit "github.com/olivaresai/olivares/connectors/pgaudit"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// The deployment convention that makes per-agent attribution possible (ARCHITECTURE.md
// §6): the MCP→Postgres connection propagates the Claude Code session id into
// the connection's application_name. The pg-audit frame then carries it, and the
// bridge lifts it to the agent the cooperative (OTEL) path discovered.
const (
	claudeSessionID = "sess-claude-abc123" // OTEL session.id, also set as application_name
	claudeAgentExt  = "agent-claude-7"     // the discovered agent that owns the session
)

// collectSink is an sdk.Sink that captures the EdgeObservations a connector emits,
// so the test can feed the REAL connector's real output through the reactor.
type collectSink struct{ edges []sdkmodel.EdgeObservation }

func (s *collectSink) Emit(_ context.Context, obs sdkmodel.Observation) error {
	if e, ok := obs.(sdkmodel.EdgeObservation); ok {
		s.edges = append(s.edges, e)
	}
	return nil
}

// newStore opens a real dual-engine store (SQLite in-memory) via the public
// engine seam and provisions a business tenant. The reactor registers no module
// tables, so the schema hook is a no-op; the edges live in the core access_edges
// table (module III is a view over the model, ARCHITECTURE.md).
func newStore(t *testing.T) (store.Store, model.TenantID) {
	t.Helper()
	ctx := context.Background()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true},
		func(store.ExtensionRegistry) error { return nil })
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
	return st, tenant
}

// seedDiscoveredAgent records the agent and its session exactly as the
// cooperative path (OTEL inventory) would have materialized them from
// a Claude Code session: an Agent and a Session whose ExternalID is the OTEL
// session.id, linked by AgentID. This is the OTEL leg of the PoC — the identity
// the audit-side bridge resolves to. (OTLP/hook ingestion that produces
// the session.id is independently verified by the claude connector's own suite;
// the OTEL leg is consumed by contract, not driven live, for that reason.)
func seedDiscoveredAgent(t *testing.T, st store.Store, tenant model.TenantID) (agentID, sessionID model.ID) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Create(context.Background(), model.Agent{
			Name: "claude-code", Kind: "claude-code", ExternalID: claudeAgentExt, Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		agentID = a.ID
		s, err := sc.Sessions().Create(context.Background(), model.Session{
			AgentID: a.ID, ExternalID: claudeSessionID, State: model.SessionRunning,
		})
		if err != nil {
			return err
		}
		sessionID = s.ID
		return nil
	}); err != nil {
		t.Fatalf("seed agent/session: %v", err)
	}
	return agentID, sessionID
}

// writeCSVLog writes a PostgreSQL csvlog fixture with the columns the connector
// reads (0 log_time, 1 user_name, 2 database_name, 13 message, 22
// application_name). Each record is a real 23-column PG csvlog row; the message
// holds a pgAudit "AUDIT: …" payload. This drives the REAL pg-audit connector,
// not a stub.
func writeCSVLog(t *testing.T, records [][]string) string {
	t.Helper()
	dir := t.TempDir()
	pathName := filepath.Join(dir, "postgresql.csv")
	f, err := os.Create(pathName)
	if err != nil {
		t.Fatalf("create csvlog: %v", err)
	}
	defer func() { _ = f.Close() }()
	w := csv.NewWriter(f)
	if err := w.WriteAll(records); err != nil {
		t.Fatalf("write csvlog: %v", err)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatalf("flush csvlog: %v", err)
	}
	return pathName
}

// pgRow builds a 23-column PostgreSQL csvlog record with the audited fields set.
// auditMsg is the pgAudit payload placed (with the "AUDIT: " prefix) in the
// message column; appName is the application_name (the per-agent bridge).
func pgRow(ts, user, db, auditMsg, appName string) []string {
	rec := make([]string, 23)
	rec[0] = ts
	rec[1] = user
	rec[2] = db
	rec[7] = "AUDIT"               // command_tag (irrelevant to the parser)
	rec[11] = "LOG"                // error_severity
	rec[12] = "00000"              // sql_state_code
	rec[13] = "AUDIT: " + auditMsg // message — the pgAudit record
	rec[22] = appName              // application_name
	return rec
}

// runPgAudit drives the real pg-audit connector over the fixture and returns the
// EdgeObservations it emits.
func runPgAudit(t *testing.T, logPath, sharedAccounts string) []sdkmodel.EdgeObservation {
	t.Helper()
	src := pgaudit.New()
	cfg := sdk.Config{Settings: map[string]string{
		"log_path":        logPath,
		"format":          "csvlog",
		"follow":          "false",
		"shared_accounts": sharedAccounts,
	}}
	ctx := context.Background()
	if err := src.Open(ctx, cfg); err != nil {
		t.Fatalf("pg-audit open: %v", err)
	}
	t.Cleanup(func() { _ = src.Close(ctx) })
	sink := &collectSink{}
	if err := src.Gather(ctx, sink); err != nil {
		t.Fatalf("pg-audit gather: %v", err)
	}
	return sink.edges
}

// findEdge returns the persisted access edge whose resource URI matches, or fails.
func findEdge(t *testing.T, st store.Store, tenant model.TenantID, resourceURI string) model.AccessEdge {
	t.Helper()
	var match model.AccessEdge
	found := false
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		edges, _, err := sc.AccessEdges().List(context.Background(), model.Query{Limit: 100})
		if err != nil {
			return err
		}
		for _, e := range edges {
			if e.Metadata["resource_ref"] == resourceURI {
				match = e
				found = true
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("list edges: %v", err)
	}
	if !found {
		t.Fatalf("no persisted edge for resource %q", resourceURI)
	}
	return match
}

// TestPoC1_AttributedAgentTableEdge is the make-or-break gate (ARCHITECTURE.md, §10).
// It drives the REAL pg-audit connector over a PostgreSQL csvlog and proves the
// three PoC claims end-to-end into the real store:
//
//	(a) the statement is classified R/RW by pgAudit (verbatim);
//	(b) it is attributed to the CONCRETE AGENT (not an anonymous credential) when
//	    a per-agent identity carries through (application_name == session id);
//	(c) the result is an AccessEdge(origin=agent, resource=table, mode,
//	    signal_source=pg_audit, confidence, ts) persisted in the store.
func TestPoC1_AttributedAgentTableEdge(t *testing.T) {
	st, tenant := newStore(t)
	agentID, _ := seedDiscoveredAgent(t, st, tenant)

	r := New()
	r.UseData(api.NewModuleData(st))

	ts := "2026-06-03 12:00:00.000 UTC"
	logPath := writeCSVLog(t, [][]string{
		// A Claude-driven MCP server SELECTs a table; the connection's
		// application_name is the Claude session id (the per-agent bridge).
		pgRow(ts, "app_role", "appdb", "SESSION,1,1,READ,SELECT,TABLE,public.customers,,", claudeSessionID),
	})

	edges := runPgAudit(t, logPath, "" /* no shared accounts */)
	if len(edges) != 1 {
		t.Fatalf("pg-audit emitted %d edges, want 1", len(edges))
	}

	// (a) pgAudit classified the SELECT as READ, verbatim, with the table resolved.
	obs := edges[0]
	if obs.Mode != sdkmodel.ModeRead {
		t.Errorf("mode = %q, want read (pgAudit READ)", obs.Mode)
	}
	if obs.Source != sdkmodel.SignalPGAudit {
		t.Errorf("source = %q, want pg_audit", obs.Source)
	}
	if obs.ResourceRef != "appdb.public.customers" {
		t.Errorf("resource = %q, want appdb.public.customers", obs.ResourceRef)
	}
	// The connector attributes to the application_name and, with no shared
	// declaration, marks it attributed — the raw per-agent credential.
	if obs.OriginKind != "identity" || obs.OriginRef != claudeSessionID {
		t.Errorf("connector origin = %s/%s, want identity/%s", obs.OriginKind, obs.OriginRef, claudeSessionID)
	}
	if obs.Confidence != sdkmodel.ConfidenceAttributed {
		t.Errorf("connector confidence = %q, want attributed", obs.Confidence)
	}

	// Feed the REAL observation through the reactor (the bridge + persistence).
	edge, err := r.Ingest(context.Background(), tenant.String(), obs)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	// (b) Attributed to the concrete agent, not the anonymous credential.
	if edge.OriginKind != originAgent {
		t.Errorf("edge origin kind = %q, want agent (bridged)", edge.OriginKind)
	}
	if edge.OriginID != agentID {
		t.Errorf("edge origin id = %q, want the discovered agent %q", edge.OriginID, agentID)
	}
	if edge.Confidence != sdkmodel.ConfidenceAttributed {
		t.Errorf("edge confidence = %q, want attributed", edge.Confidence)
	}
	if b, _ := edge.Metadata["bridged"].(bool); !b {
		t.Errorf("edge not marked bridged; metadata = %v", edge.Metadata)
	}

	// (c) Persisted in the store with the differential fields, queryable as a
	// view over the model.
	persisted := findEdge(t, st, tenant, "appdb.public.customers")
	if persisted.ID != edge.ID {
		t.Errorf("persisted edge id = %q, want %q", persisted.ID, edge.ID)
	}
	if persisted.Mode != sdkmodel.ModeRead || persisted.SignalSource != sdkmodel.SignalPGAudit {
		t.Errorf("persisted mode/source = %s/%s", persisted.Mode, persisted.SignalSource)
	}
	if !persisted.Observed || persisted.Permitted {
		t.Errorf("persisted observed/permitted = %v/%v, want true/false (an observed, not-yet-permitted access)", persisted.Observed, persisted.Permitted)
	}
	if persisted.FirstSeen.String() == "" || persisted.LastSeen.String() == "" {
		t.Error("persisted edge missing first/last seen timestamp")
	}

	// The agent's outgoing neighbors include the table edge — the graph is a view.
	var neighbors []model.AccessEdge
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		neighbors, e = sc.AccessEdges().Neighbors(context.Background(), model.NodeRef{Kind: "agent", ID: agentID}, model.Outgoing)
		return e
	}); err != nil {
		t.Fatalf("neighbors: %v", err)
	}
	if len(neighbors) != 1 || neighbors[0].ResourceID != persisted.ResourceID {
		t.Errorf("agent neighbors = %d, want the 1 table edge", len(neighbors))
	}
}

// TestPoC1_AttributionCollapsesOnSharedPool proves the honest failure mode
// (ARCHITECTURE.md): a shared/pooled service account collapses agent attribution to
// the credential, marked approximate — never a fabricated agent.
func TestPoC1_AttributionCollapsesOnSharedPool(t *testing.T) {
	st, tenant := newStore(t)
	seedDiscoveredAgent(t, st, tenant) // an agent exists, but the pooled access is not it

	r := New()
	r.UseData(api.NewModuleData(st))

	ts := "2026-06-03 12:05:00.000 UTC"
	logPath := writeCSVLog(t, [][]string{
		// A pooled connection (pgbouncer) writes; application_name is the shared
		// pooler, declared shared, so the agent is ambiguous by construction.
		pgRow(ts, "svc_pool", "appdb", "SESSION,2,1,WRITE,INSERT,TABLE,public.orders,,", "pgbouncer"),
	})
	edges := runPgAudit(t, logPath, "pgbouncer" /* declared shared */)
	if len(edges) != 1 {
		t.Fatalf("pg-audit emitted %d edges, want 1", len(edges))
	}
	obs := edges[0]
	// The connector fell back to the role and marked it approximate.
	if obs.Mode != sdkmodel.ModeWrite {
		t.Errorf("mode = %q, want write (pgAudit WRITE)", obs.Mode)
	}
	if obs.OriginRef != "svc_pool" || obs.Confidence != sdkmodel.ConfidenceApproximate {
		t.Errorf("connector origin/confidence = %s/%s, want svc_pool/approximate", obs.OriginRef, obs.Confidence)
	}

	edge, err := r.Ingest(context.Background(), tenant.String(), obs)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	// Collapsed to the credential identity, approximate — NOT an agent.
	if edge.OriginKind != originIdentity {
		t.Errorf("edge origin kind = %q, want identity (collapsed, not faked agent)", edge.OriginKind)
	}
	if edge.Confidence != sdkmodel.ConfidenceApproximate {
		t.Errorf("edge confidence = %q, want approximate", edge.Confidence)
	}
	if b, _ := edge.Metadata["bridged"].(bool); b {
		t.Error("a shared/pooled credential must not be marked bridged")
	}
}

// TestPoC1_CooperativeOTELLegBridgesToAgent consumes the OTEL output contract
// (claude/observations.go: OriginKind=session, OriginRef=session.id, Source=otel,
// attributed) and shows the same edge attributes to the agent — the cooperative
// leg of the bridge.
//
// Scoping: the cooperative leg is exercised BY
// CONTRACT, not by driving the claude connector's OTLP/hook receiver here. That
// connector's ingestion is independently verified by its own suite; this
// test validates the BRIDGE in isolation against the exact EdgeObservation shape
// the connector emits. The pg-audit leg, where the feasibility risk lives, IS
// driven end-to-end through the real connector (TestPoC1_AttributedAgentTableEdge).
func TestPoC1_CooperativeOTELLegBridgesToAgent(t *testing.T) {
	st, tenant := newStore(t)
	agentID, _ := seedDiscoveredAgent(t, st, tenant)

	r := New()
	r.UseData(api.NewModuleData(st))

	// Exactly the shape claude/observations.go emits for a tool access.
	otel := sdkmodel.EdgeObservation{
		OriginKind:   "session",
		OriginRef:    claudeSessionID,
		ResourceKind: "postgres.table",
		ResourceRef:  "appdb.public.customers",
		Mode:         sdkmodel.ModeRead,
		Source:       sdkmodel.SignalOTEL,
		Confidence:   sdkmodel.ConfidenceAttributed,
		ToolRef:      "mcp__pg__query",
		ObservedAt:   time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC),
	}
	edge, err := r.Ingest(context.Background(), tenant.String(), otel)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if edge.OriginKind != originAgent || edge.OriginID != agentID {
		t.Errorf("cooperative edge origin = %s/%s, want agent/%s", edge.OriginKind, edge.OriginID, agentID)
	}
	if edge.Confidence != sdkmodel.ConfidenceAttributed {
		t.Errorf("cooperative edge confidence = %q, want attributed", edge.Confidence)
	}
}

// TestPoC1_GuardrailMinimalData asserts the docs/SECURITY-HARDENING.md minimal-data guardrail:
// the persisted edge carries only the bounded, allow-listed, non-sensitive
// metadata — no SQL statement, payload or secret can reach storage. It checks
// both that the key set is closed and that no value contains the SQL body.
func TestPoC1_GuardrailMinimalData(t *testing.T) {
	st, tenant := newStore(t)
	seedDiscoveredAgent(t, st, tenant)
	r := New()
	r.UseData(api.NewModuleData(st))

	ts := "2026-06-03 12:10:00.000 UTC"
	logPath := writeCSVLog(t, [][]string{
		pgRow(ts, "app_role", "appdb", "SESSION,1,1,READ,SELECT,TABLE,public.customers,,", claudeSessionID),
	})
	edges := runPgAudit(t, logPath, "")
	if _, err := r.Ingest(context.Background(), tenant.String(), edges[0]); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	edge := findEdge(t, st, tenant, "appdb.public.customers")
	allowed := map[string]bool{
		"raw_origin_kind": true, "raw_confidence": true, "attribution_reason": true,
		"bridged": true, "canonical_confidence": true, "origin_ref": true,
		"resource_kind": true, "resource_ref": true, "tool_ref": true,
		"coverage_tier": true, "signal_sources": true,
		"attribution_tier": true, "attribution_tier_reason": true,
	}
	for k, v := range edge.Metadata {
		if !allowed[k] {
			t.Errorf("unexpected metadata key %q = %v (minimal-data: key set must be closed)", k, v)
		}
		if s, ok := v.(string); ok {
			// The pgAudit STATEMENT (raw SQL) must never appear anywhere.
			if containsSQL(s) {
				t.Errorf("metadata %q leaked SQL-like content: %q", k, s)
			}
		}
	}
	// tool_ref is the command verb (SELECT), never the statement.
	if edge.Metadata["tool_ref"] != "SELECT" {
		t.Errorf("tool_ref = %v, want the verb SELECT (not the statement)", edge.Metadata["tool_ref"])
	}
}

// containsSQL is a coarse check that a stored string is not a SQL statement body.
func containsSQL(s string) bool {
	for _, frag := range []string{"FROM ", "WHERE ", "VALUES", "SELECT *", "* FROM"} {
		if len(s) >= len(frag) && contains(s, frag) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestPoC1_GraphReadIsAuditedAndTamperEvident asserts the docs/SECURITY-HARDENING.md/§5
// guardrail: viewing the access graph is a privileged action that is recorded in
// the append-only, hash-chained ledger, and the chain verifies intact.
func TestPoC1_GraphReadIsAuditedAndTamperEvident(t *testing.T) {
	st, tenant := newStore(t)
	agentID, _ := seedDiscoveredAgent(t, st, tenant)
	r := New()
	r.UseData(api.NewModuleData(st))

	ts := "2026-06-03 12:15:00.000 UTC"
	logPath := writeCSVLog(t, [][]string{
		pgRow(ts, "app_role", "appdb", "SESSION,1,1,READ,SELECT,TABLE,public.customers,,", claudeSessionID),
	})
	edges := runPgAudit(t, logPath, "")
	if _, err := r.Ingest(context.Background(), tenant.String(), edges[0]); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	// A privileged graph read self-audits before returning the edges.
	got, err := r.AuditedNeighbors(context.Background(), tenant, "user:auditor", model.ActorUser,
		model.NodeRef{Kind: "agent", ID: agentID}, model.Outgoing)
	if err != nil {
		t.Fatalf("audited neighbors: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("audited read returned %d edges, want 1", len(got))
	}

	// The read left a tamper-evident evidence record, and the chain is intact.
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var sawRead bool
		if err := sc.Audit().Walk(context.Background(), 1, func(e model.AuditEvent) error {
			if e.Action == "access_map.graph.read" && e.Actor == "user:auditor" {
				sawRead = true
			}
			return nil
		}); err != nil {
			return err
		}
		if !sawRead {
			t.Error("graph read was not recorded in the append-only ledger")
		}
		rep, err := sc.Audit().Verify(context.Background(), 1)
		if err != nil {
			return err
		}
		if !rep.OK {
			t.Errorf("audit chain not intact: break at seq %d (%s)", rep.BreakAt, rep.Reason)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify audit: %v", err)
	}
}

// TestPoC1_AmbiguousAgentDoesNotFabricate is the honesty regression for the
// cardinal sin (ARCHITECTURE.md, docs/SECURITY-HARDENING.md): when a per-agent credential maps to
// MORE THAN ONE discovered agent, the bridge must NOT pick one and call it
// attributed. The access is recorded against the credential identity (real),
// never a guessed agent.
func TestPoC1_AmbiguousAgentDoesNotFabricate(t *testing.T) {
	st, tenant := newStore(t)
	// Two agents share the same external id — the core tables carry no unique
	// index on external_id (single-writer model), so this is reachable.
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for _, name := range []string{"agent-a", "agent-b"} {
			if _, err := sc.Agents().Create(context.Background(), model.Agent{
				Name: name, Kind: "claude-code", ExternalID: "dup-agent-cred", Status: model.StatusActive,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed ambiguous agents: %v", err)
	}

	r := New()
	r.UseData(api.NewModuleData(st))

	ts := "2026-06-03 12:20:00.000 UTC"
	logPath := writeCSVLog(t, [][]string{
		// A per-agent credential (not shared) whose application_name collides with
		// the two agents' external id, and matches no session.
		pgRow(ts, "app_role", "appdb", "SESSION,1,1,READ,SELECT,TABLE,public.customers,,", "dup-agent-cred"),
	})
	edges := runPgAudit(t, logPath, "")
	if len(edges) != 1 {
		t.Fatalf("pg-audit emitted %d edges, want 1", len(edges))
	}

	edge, err := r.Ingest(context.Background(), tenant.String(), edges[0])
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if edge.OriginKind == originAgent {
		t.Fatalf("FABRICATED ATTRIBUTION: edge attributed to an agent when two agents are equally valid (origin=%s, id=%s)", edge.OriginKind, edge.OriginID)
	}
	if edge.OriginKind != originIdentity {
		t.Errorf("edge origin = %q, want identity (the credential, agent unresolved)", edge.OriginKind)
	}
	if b, _ := edge.Metadata["bridged"].(bool); b {
		t.Error("an ambiguous credential must not be marked bridged")
	}
}

// TestPoC1_AmbiguousAgentOriginCollapses covers the same guard on the direct
// "agent" origin path (a future cooperative/runtime resolver that emits
// OriginKind=agent): two agents sharing the id collapse to approximate, not a
// fabricated pick.
func TestPoC1_AmbiguousAgentOriginCollapses(t *testing.T) {
	st, tenant := newStore(t)
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for _, name := range []string{"agent-a", "agent-b"} {
			if _, err := sc.Agents().Create(context.Background(), model.Agent{
				Name: name, Kind: "api", ExternalID: "shared-ext", Status: model.StatusActive,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r := New()
	r.UseData(api.NewModuleData(st))

	edge, err := r.Ingest(context.Background(), tenant.String(), sdkmodel.EdgeObservation{
		OriginKind: "agent", OriginRef: "shared-ext",
		ResourceKind: "postgres.table", ResourceRef: "appdb.public.orders",
		Mode: sdkmodel.ModeWrite, Source: sdkmodel.SignalEBPF, Confidence: sdkmodel.ConfidenceAttributed,
		ObservedAt: time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if edge.OriginKind == originAgent {
		t.Fatalf("FABRICATED ATTRIBUTION on agent-origin path: %s/%s", edge.OriginKind, edge.OriginID)
	}
	if edge.Confidence != sdkmodel.ConfidenceApproximate {
		t.Errorf("ambiguous agent confidence = %q, want approximate", edge.Confidence)
	}
}

// TestPoC1_NilDataHandleErrors asserts the reactor surfaces a missing data handle
// as an explicit error, never a nil-dereference panic.
func TestPoC1_NilDataHandleErrors(t *testing.T) {
	r := New() // UseData intentionally not called
	if _, err := r.Ingest(context.Background(), "tenant", sdkmodel.EdgeObservation{OriginKind: "session", OriginRef: "s"}); err == nil {
		t.Error("Ingest without a data handle must return an error, not nil")
	}
	if _, err := r.AuditedNeighbors(context.Background(), model.TenantID("t"), "u", model.ActorUser, model.NodeRef{}, model.Outgoing); err == nil {
		t.Error("AuditedNeighbors without a data handle must return an error, not nil")
	}
}
