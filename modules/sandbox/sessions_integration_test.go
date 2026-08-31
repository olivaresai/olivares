// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk"
)

// This file proves the II (sessions) ↔ XVII (sandbox) replay integration end-to-end
// with the REAL module-II timeline read, not a fake. Like
// evals_integration_test.go it is TEST-ONLY and the only sandbox file importing the
// sibling; production code never does (contract §4) — the bridge is this tiny
// adapter, whose production form lives in the composition root
// (cmd/olivares/sessionadapters.go).

// sessionsHistoryAdapter mirrors the production composition-root adapter: the
// session's ordered tool/mcp actions become replay steps whose ZERO-PADDED keys sort
// lexicographically in execution order (sandbox re-sorts outputs by step_key at read
// time) and whose inputs are the already-redacted refs a replay mock matches on.
type sessionsHistoryAdapter struct{ ss *sessions.Module }

func (a sessionsHistoryAdapter) Timeline(ctx context.Context, tenant model.TenantID, sessionRef string) ([]ReplayStep, error) {
	events, truncated, err := a.ss.ReplayTimeline(ctx, tenant, sessionRef, 10000)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("session %q timeline exceeds the replayable bound", sessionRef)
	}
	steps := make([]ReplayStep, 0, len(events))
	for i, ev := range events {
		label := ev.ToolRef
		if label == "" {
			label = ev.Kind
		}
		input := ev.ResourceRef
		if input == "" {
			input = ev.ToolRef
		}
		steps = append(steps, ReplayStep{Key: fmt.Sprintf("%05d %s", i+1, label), Input: input})
	}
	return steps, nil
}

// newSessionsDualHarness wires sessions + sandbox into ONE api.Server over one
// store, with replay backed by the real module-II timeline via the adapter. It
// reuses the sandbox harness's HTTP helpers by returning *harness.
func newSessionsDualHarness(t *testing.T) (*harness, store.Store) {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t}

	ss := sessions.New()
	sb := New(WithHistorySource(sessionsHistoryAdapter{ss: ss}))

	register := func(reg store.ExtensionRegistry) error {
		if err := ss.RegisterSchema(reg); err != nil {
			return err
		}
		return sb.RegisterSchema(reg)
	}
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, register)
	if err != nil {
		t.Fatal(err)
	}
	h.st = st
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	ss.UseData(api.NewModuleData(st))
	sb.UseData(api.NewModuleData(st))

	bus := eventbus.NewInProc(eventbus.Options{})
	rt := runtime.New(runtime.Options{Bus: bus})
	if err := rt.AddModule(ss, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddModule(sb, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Stop(ctx); _ = bus.Close() })

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test",
		Modules: []api.Module{ss, sb},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.srv, h.setupTok = srv, plaintext
	return h, st
}

// seedTimelineRow inserts one sessions.timeline row directly (the module's writers
// are bus handlers; the write path itself is covered by the sessions module's own
// tests — here we seed the read-model to drive the replay flow deterministically).
func seedTimelineRow(t *testing.T, st store.Store, tenant model.TenantID, ref string, at time.Time, kind, tool, res string) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.timeline")
		if err != nil {
			return err
		}
		rec := model.Record{"session_ref": ref, "at": model.NewTimestamp(at).String(), "kind": kind}
		if tool != "" {
			rec["tool_ref"] = tool
		}
		if res != "" {
			rec["resource_ref"] = res
		}
		_, err = repo.Create(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatalf("seed timeline row: %v", err)
	}
}

// TestReplayFromSessionsTimeline is the replay contract end-to-end: a 12-action
// session reconstructs in order (≥10 actions prove the zero-padded keys keep the
// read order faithful where 'step-10' < 'step-2' would lie), telemetry rows do not
// replay, mocks resolve against the recorded resource refs, the replay is
// deterministic, and a session with no timeline stays honestly DEGRADED.
func TestReplayFromSessionsTimeline(t *testing.T) {
	h, st := newSessionsDualHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	const ref = "sess-replay"
	for i := 0; i < 12; i++ {
		seedTimelineRow(t, h.st, tenant, ref, base.Add(time.Duration(i)*time.Second),
			"tool", "Read", fmt.Sprintf("res-%02d", i+1))
	}
	// Interleaved telemetry that must NOT become steps.
	seedTimelineRow(t, st, tenant, ref, base.Add(30*time.Second), "cost", "", "")
	seedTimelineRow(t, st, tenant, ref, base.Add(31*time.Second), "finding", "", "")

	body := map[string]any{
		"session_ref": ref,
		"mocks":       []map[string]any{{"resource": "res-03", "response": "mocked-content"}},
	}
	r := h.do("POST", "/v1/m/sandbox/replay", admin, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("replay = %d %s", r.code, r.raw)
	}
	if r.body["status"] != "completed" {
		t.Errorf("status = %v, want completed", r.body["status"])
	}
	if got := int(r.body["steps_total"].(float64)); got != 12 {
		t.Fatalf("steps_total = %d, want 12 (telemetry rows must not replay)", got)
	}
	if ok := int(r.body["steps_ok"].(float64)); ok != 1 {
		t.Errorf("steps_ok = %d, want 1 (one mock hit)", ok)
	}

	// Read order == execution order: keys are zero-padded so the lexicographic
	// re-sort at read time preserves the timeline sequence past step 10.
	runID := r.body["id"].(string)
	out := h.do("GET", "/v1/m/sandbox/runs/"+runID+"/outputs", admin, nil, tenantHdr(tenant))
	if out.code != http.StatusOK {
		t.Fatalf("outputs = %d %s", out.code, out.raw)
	}
	items, _ := out.body["items"].([]any)
	if len(items) != 12 {
		t.Fatalf("outputs = %d, want 12", len(items))
	}
	keys := make([]string, 0, len(items))
	for i, it := range items {
		m := it.(map[string]any)
		key := m["step_key"].(string)
		keys = append(keys, key)
		wantKey := fmt.Sprintf("%05d Read", i+1)
		if key != wantKey {
			t.Errorf("output[%d] key = %q, want %q", i, key, wantKey)
		}
		if i == 2 {
			if m["output"] != "mocked-content" || m["mock_hit"] != true {
				t.Errorf("step 3 = %v / mock_hit=%v, want the mocked response", m["output"], m["mock_hit"])
			}
		}
	}
	if !sort.StringsAreSorted(keys) {
		t.Errorf("step keys are not in lexicographic execution order: %v", keys)
	}

	// Determinism: the same session_ref + mocks yields the identical outputs hash.
	again := h.do("POST", "/v1/m/sandbox/replay", admin, body, tenantHdr(tenant))
	if again.code != http.StatusCreated {
		t.Fatalf("second replay = %d %s", again.code, again.raw)
	}
	if r.body["outputs_hash"] == "" || again.body["outputs_hash"] != r.body["outputs_hash"] {
		t.Errorf("outputs_hash differs across identical replays: %v vs %v", r.body["outputs_hash"], again.body["outputs_hash"])
	}

	// No timeline ⇒ honestly DEGRADED with zero steps, never fabricated.
	ghost := h.do("POST", "/v1/m/sandbox/replay", admin, map[string]any{"session_ref": "ghost"}, tenantHdr(tenant))
	if ghost.code != http.StatusCreated {
		t.Fatalf("ghost replay = %d %s", ghost.code, ghost.raw)
	}
	if ghost.body["status"] != "degraded" || int(ghost.body["steps_total"].(float64)) != 0 {
		t.Errorf("ghost replay = %v/%v, want degraded/0", ghost.body["status"], ghost.body["steps_total"])
	}
}

// TestCompareFromEmptySessionTimeline pins the honest-degradation rule on the
// session branch of POST /compare: a session whose timeline yields no steps must
// produce two DEGRADED runs and an INCONCLUSIVE verdict — never two confident
// zero-step "completed" runs whose equal empty hashes fabricate "unchanged" as
// append-only deploy-decision evidence.
func TestCompareFromEmptySessionTimeline(t *testing.T) {
	h, _ := newSessionsDualHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("POST", "/v1/m/sandbox/compare", admin, map[string]any{
		"session_ref": "ghost", "baseline_variant": "v1", "candidate_variant": "v2",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("compare = %d %s", r.code, r.raw)
	}
	if r.body["verdict"] != "inconclusive" {
		t.Errorf("verdict = %v, want inconclusive (zero-step runs prove nothing)", r.body["verdict"])
	}
	for _, ref := range []string{"baseline_run_ref", "candidate_run_ref"} {
		runID, _ := r.body[ref].(string)
		run := h.do("GET", "/v1/m/sandbox/runs/"+runID, admin, nil, tenantHdr(tenant))
		if run.code != http.StatusOK {
			t.Fatalf("get %s = %d %s", ref, run.code, run.raw)
		}
		if run.body["status"] != "degraded" || int(run.body["steps_total"].(float64)) != 0 {
			t.Errorf("%s = %v/%v, want degraded/0", ref, run.body["status"], run.body["steps_total"])
		}
	}
}
