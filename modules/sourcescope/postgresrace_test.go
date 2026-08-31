// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/engine/enginetest"

	"github.com/olivaresai/olivares/core/store"
)

// postgresrace_test.go is E-5, executed rather than argued.
//
// THE CLAIM. The confinement count every classifier decides on is read WITHOUT a lock —
// countOtherEnabledAllows / countOtherAssignments go through allExt, a plain List — and
// Mutate opens `BeginTx(ctx, nil)`, i.e. the driver default, which on Postgres is READ
// COMMITTED (core/internal/store/sqlstore/store.go:1383; no advisory lock and no FOR UPDATE
// anywhere on the binding path). Two concurrent deletes of the LAST TWO enabled allows each
// see one other allow, each classify their own delete as ordinary, each touch a DIFFERENT
// row — so nothing conflicts — and both commit. The source ends up global with no approval
// and no pending request: the exact relaxation the gate exists to stop, produced by two
// writes that were individually correct.
//
// WHY IT MUST BE POSTGRES. The rest of this package runs on SQLite :memory:, which has ONE
// writer: the second transaction cannot interleave, so the race is invisible there by
// construction. A concurrency defect "not reproduced" on an engine that cannot exhibit it is
// not evidence of anything — it is the wrong instrument.
//
// MEASURED 2026-08-07, Postgres 16.14: 39 of 40 rounds BOTH deletes returned 204 and
// the source ended globally reachable, with ONE pending request queued across the whole run.
// E-5 is not a narrow window — under concurrency it is the ordinary outcome, and the single
// gated round is the accident.
//
// THESE TESTS DO NOT FIX IT, and they are written to FAIL when the race lands, so they turn
// green the day the fix arrives instead of needing to be remembered. The fix is not available
// from here: neither store.Scope nor GenericRepo exposes a row lock, an advisory lock or an
// isolation level, and Mutate hardcodes BeginTx(ctx, nil) in core. Closing it means changing
// the store contract — call, with the options and their cost named in the PR.
//
// So they are OPT-IN. A deliberately red test in the default suite would go red for every
// other session over a defect that is not theirs, and the first thing anyone does with a
// permanently red test is stop reading it. The skip message carries the measurement, so the
// green nobody opted into cannot be read as "no defect here".
const e5ReproEnv = "OLIVARES_SOURCESCOPE_RACE_REPRO"

// pgHarness provisions a private Postgres database and returns a harness on it.
func pgHarness(t *testing.T) *harness {
	t.Helper()
	if os.Getenv(e5ReproEnv) == "" {
		t.Skipf("the unconfinement race is OPEN and reproduces: 39/40 rounds measured on Postgres "+
			"16.14 (2026-08-07) BOTH deleted the last two enabled allows and left the source global "+
			"with no dual-control request. countOtherEnabledAllows/countOtherAssignments read without "+
			"a lock and Mutate runs at READ COMMITTED (core/internal/store/sqlstore/store.go:1383). "+
			"Set %s=1 to re-run the reproduction; it FAILS while the defect is open and passes once "+
			"the store can lock the confinement read.", e5ReproEnv)
	}
	if !enginetest.PostgresAvailable(t) {
		t.Skip("no Postgres configured (OLIVARES_TEST_POSTGRES_SUPERUSER_DSN); E-5 cannot be reproduced on SQLite")
	}
	dsns := enginetest.IsolatedPostgres(t)
	// MaxConns > 1 or there is no concurrency to measure: the pool would serialize the two
	// requests and the test would report a green that means "I only ever ran one".
	return newHarnessOn(t, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 8})
}

// TestPostgresConcurrentLastAllowDeletesCanUnconfineASource fires the two deletes
// concurrently, many times over, and reports how often the pair slipped through.
func TestPostgresConcurrentLastAllowDeletesCanUnconfineASource(t *testing.T) {
	h := pgHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	wsEng := h.createWorkspace(tenant, "engineering")
	agentEng := h.createAgent(tenant, "eng-bot", wsEng)
	h.addAgentToGroup(tenant, agentEng.ID, "core", wsEng)
	h.createSession(tenant, "eng-session", agentEng.ID, wsEng)
	approver := h.tokenFor(admin, tenant, "approver@acme.io", "admin")
	pOutsider := h.principalFor(admin, tenant, "outsider@acme.io", "")
	wsOther := h.createWorkspace(tenant, "outside")
	agentOut := h.createAgent(tenant, "out-bot", wsOther)
	h.createSession(tenant, "out-session", agentOut.ID, wsOther)

	const rounds = 40
	unconfined := 0
	for i := range rounds {
		ref := fmt.Sprintf("repo-%d", i)

		// Two enabled allows on one source. The first confines (201); the second is a
		// relaxation, so it goes the honest way, through a second principal.
		if c := h.createBinding(admin, tenant, map[string]any{
			"source_type": "data", "source_ref": ref,
			"scope_tree": "workspace", "scope_ref": "engineering", "enabled": true,
		}); c.code != http.StatusCreated {
			t.Fatalf("round %d: first allow = %d %s", i, c.code, c.raw)
		}
		h.createBindingApproved(admin, approver, tenant, map[string]any{
			"source_type": "data", "source_ref": ref,
			"scope_tree": "agent_group", "scope_ref": "core", "enabled": true,
		})

		l := h.do("GET", "/v1/m/sourcescope/bindings?source_type=data&source_ref="+ref, admin, nil, tenantHdr(tenant))
		ids := []string{}
		for _, it := range items(l) {
			ids = append(ids, it.(map[string]any)["id"].(string))
		}
		if len(ids) != 2 {
			t.Fatalf("round %d: want 2 bindings, got %d: %s", i, len(ids), l.raw)
		}

		// Both deletes at once. Each transaction reads the confinement count before either
		// commits, so each sees the other's row and calls its own delete ordinary.
		var wg sync.WaitGroup
		codes := make([]int, 2)
		start := make(chan struct{})
		for k, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[k] = h.do("DELETE", "/v1/m/sourcescope/bindings/"+id, admin, nil, tenantHdr(tenant)).code
			}()
		}
		close(start)
		wg.Wait()

		// THE MEASUREMENT. If both deletes applied (204/204), the source has no enabled
		// allow left and is reachable by everyone — with nothing pending for anyone to
		// approve or reject.
		if codes[0] == http.StatusNoContent && codes[1] == http.StatusNoContent {
			unconfined++
			d, err := h.resolver.ResolveForSession(t.Context(), tenant, pOutsider, "out-session", "data", ref)
			if err != nil {
				t.Fatalf("round %d: resolve: %v", i, err)
			}
			if !d.Allowed {
				t.Fatalf("round %d: both deletes committed but the source is not global — the premise of this test is wrong: %+v", i, d)
			}
			t.Logf("round %d: RACE LANDED — both deletes returned 204, source is global, reason=%q", i, d.Reason)
		}
	}

	pending := h.do("GET", "/v1/m/sourcescope/posture-requests?status=pending", admin, nil, tenantHdr(tenant))
	t.Logf("unconfinement race on Postgres: %d/%d rounds unconfined a source with no approval; %d pending requests queued",
		unconfined, rounds, len(items(pending)))

	if unconfined > 0 {
		t.Fatalf("UNCONFINEMENT RACE REPRODUCED on Postgres: %d of %d concurrent last-allow delete pairs BOTH applied, "+
			"leaving the source globally reachable with no dual-control request. countOtherEnabledAllows "+
			"reads without a lock (posture.go) and Mutate runs at READ COMMITTED "+
			"(core/internal/store/sqlstore/store.go:1383).", unconfined, rounds)
	}
}

// TestPostgresConcurrentLastAssignmentDeletesCanUnconfineAConnector is the same race on the
// surface just gated, and it is the reason the fix for E-5 cannot be a binding-only
// lock: countOtherAssignments reads exactly as unguardedly as countOtherEnabledAllows, so a
// lock added to one surface would leave the other reproducing.
func TestPostgresConcurrentLastAssignmentDeletesCanUnconfineAConnector(t *testing.T) {
	h := pgHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "engineering")
	h.createWorkspace(tenant, "marketing")
	wsOut := h.createWorkspace(tenant, "outside")
	agentOut := h.createAgent(tenant, "out-bot", wsOut)
	h.createSession(tenant, "out-session", agentOut.ID, wsOut)
	approver := h.tokenFor(admin, tenant, "approver@acme.io", "admin")
	pOutsider := h.principalFor(admin, tenant, "outsider@acme.io", "")

	const rounds = 40
	unconfined := 0
	for i := range rounds {
		conn := fmt.Sprintf("github-%d", i)
		h.createAssignmentOK(admin, tenant, conn, "engineering", true)
		second := h.do("POST", "/v1/m/sourcescope/assignments", admin, map[string]any{
			"connector_name": conn, "workspace_ref": "marketing", "enabled": true,
		}, tenantHdr(tenant))
		if second.code != http.StatusAccepted {
			t.Fatalf("round %d: second assignment = %d %s", i, second.code, second.raw)
		}
		if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+second.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
			t.Fatalf("round %d: approve = %d %s", i, a.code, a.raw)
		}

		l := h.do("GET", "/v1/m/sourcescope/assignments?connector_name="+conn, admin, nil, tenantHdr(tenant))
		ids := []string{}
		for _, it := range items(l) {
			ids = append(ids, it.(map[string]any)["id"].(string))
		}
		if len(ids) != 2 {
			t.Fatalf("round %d: want 2 assignment rows, got %d: %s", i, len(ids), l.raw)
		}

		var wg sync.WaitGroup
		codes := make([]int, 2)
		start := make(chan struct{})
		for k, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[k] = h.do("DELETE", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant)).code
			}()
		}
		close(start)
		wg.Wait()

		if codes[0] == http.StatusNoContent && codes[1] == http.StatusNoContent {
			unconfined++
			d, err := h.resolver.ResolveForSession(t.Context(), tenant, pOutsider, "out-session", "data", conn)
			if err != nil {
				t.Fatalf("round %d: resolve: %v", i, err)
			}
			if !d.Allowed {
				t.Fatalf("round %d: both deletes committed but the connector is not global: %+v", i, d)
			}
			t.Logf("round %d: RACE LANDED — connector %s is visible to every workspace, reason=%q", i, conn, d.Reason)
		}
	}
	t.Logf("unconfinement race (assignment surface) on Postgres: %d/%d rounds unconfined a connector with no approval", unconfined, rounds)
	if unconfined > 0 {
		t.Fatalf("UNCONFINEMENT RACE REPRODUCED on the assignment surface: %d of %d concurrent last-row delete pairs BOTH applied, "+
			"leaving the connector visible to EVERY workspace with no dual-control request.", unconfined, rounds)
	}
}
