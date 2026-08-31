// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// The heart of the E2E: the R/RW access graph and the PERMITTED-vs-OBSERVED diff
// (module III), built from the seed source's edge observations flowing through the
// real bus → access-map → store → API. Asserts on real materialized state, plus
// the self-audit trail every privileged graph read must leave (docs/SECURITY-HARDENING.md).

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/seed"
)

// edgeByResource returns the first graph/drift edge whose resource_ref matches.
func edgeByResource(edges []map[string]any, ref string) map[string]any {
	for _, e := range edges {
		if e["resource_ref"] == ref {
			return e
		}
	}
	return nil
}

func TestE2E_AccessGraph_RWAndConfidence(t *testing.T) {
	h := newHarness(t)
	g := h.getJSON(h.adminToken, h.tenantA, "/v1/m/accessmap/graph?limit=200")
	edges := items2(g, "edges")
	nodes := items2(g, "nodes")

	if len(edges) == 0 || len(nodes) == 0 {
		t.Fatalf("empty graph: %d edges, %d nodes", len(edges), len(nodes))
	}

	// A firmly-attributed READ from a real collector: observed, not permitted.
	secrets := edgeByResource(edges, seed.ResSecrets)
	if secrets == nil {
		t.Fatal("missing secrets edge")
	}
	assertEq(t, "secrets.mode", secrets["mode"], "read")
	assertEq(t, "secrets.signal_source", secrets["signal_source"], "pg_audit")
	assertEq(t, "secrets.confidence", secrets["confidence"], "attributed")
	assertEq(t, "secrets.observed", secrets["observed"], true)
	assertEq(t, "secrets.permitted", secrets["permitted"], false)

	// A read/write access, granted AND exercised (merged on its natural key).
	orders := edgeByResource(edges, seed.ResOrders)
	if orders == nil {
		t.Fatal("missing orders edge")
	}
	assertEq(t, "orders.mode", orders["mode"], "readwrite")
	assertEq(t, "orders.observed", orders["observed"], true)
	assertEq(t, "orders.permitted", orders["permitted"], true)

	// The shared-pool write is honestly APPROXIMATE, never faked to attributed.
	logs := edgeByResource(edges, seed.ResLogs)
	if logs == nil {
		t.Fatal("missing logs edge")
	}
	assertEq(t, "logs.confidence", logs["confidence"], "approximate")

	// Nodes are server-derived from the edge endpoints and include agents,
	// identities and resources.
	kinds := map[string]bool{}
	for _, n := range nodes {
		if k, ok := n["kind"].(string); ok {
			kinds[k] = true
		}
	}
	for _, want := range []string{"agent", "identity"} {
		if !kinds[want] {
			t.Errorf("graph nodes missing kind %q (have %v)", want, kinds)
		}
	}
}

func TestE2E_AccessDrift_PermittedVsObserved(t *testing.T) {
	h := newHarness(t)
	d := h.getJSON(h.adminToken, h.tenantA, "/v1/m/accessmap/drift?limit=200")

	unexpected := items2(d, "unexpected_accesses")
	unused := items2(d, "unused_grants")
	if len(unexpected) == 0 || len(unused) == 0 {
		t.Fatalf("trivial drift: %d unexpected, %d unused", len(unexpected), len(unused))
	}

	// Flatten the {kind, reconciliation_pending, edge} entries to their resource.
	type drow struct {
		ref     string
		pending bool
	}
	flatten := func(entries []map[string]any) []drow {
		out := make([]drow, 0, len(entries))
		for _, e := range entries {
			edge, _ := e["edge"].(map[string]any)
			ref, _ := edge["resource_ref"].(string)
			pending, _ := e["reconciliation_pending"].(bool)
			out = append(out, drow{ref: ref, pending: pending})
		}
		return out
	}
	hasRef := func(rows []drow, ref string) (drow, bool) {
		for _, r := range rows {
			if r.ref == ref {
				return r, true
			}
		}
		return drow{}, false
	}

	ux := flatten(unexpected)
	ug := flatten(unused)

	// FIRM unexpected access: secrets, with reconciliation_pending NOT set.
	if r, ok := hasRef(ux, seed.ResSecrets); !ok {
		t.Error("secrets not flagged as unexpected access")
	} else if r.pending {
		t.Error("secrets should be a FIRM unexpected access (pending=false)")
	}

	// RECONCILIATION PENDING: exactly one entry, and it is billing.
	pendingCount := 0
	for _, r := range ux {
		if r.pending {
			pendingCount++
			if r.ref != seed.ResBilling {
				t.Errorf("unexpected pending entry on %q, want billing", r.ref)
			}
		}
	}
	if pendingCount != 1 {
		t.Errorf("reconciliation_pending entries = %d, want exactly 1 (billing)", pendingCount)
	}

	// UNUSED GRANT: archive (granted write, never observed).
	if _, ok := hasRef(ug, seed.ResArchive); !ok {
		t.Error("archive not flagged as an unused grant")
	}

	// RECONCILED / healthy: customers, orders, exports are granted AND exercised,
	// so they appear in NEITHER drift list (proves the diff is not "everything").
	for _, clean := range []string{seed.ResCustomers, seed.ResOrders, seed.ResExports} {
		if _, ok := hasRef(ux, clean); ok {
			t.Errorf("%s wrongly flagged as unexpected (it is granted+observed)", clean)
		}
		if _, ok := hasRef(ug, clean); ok {
			t.Errorf("%s wrongly flagged as unused (it is granted+observed)", clean)
		}
	}

	// The summary counts agree with the lists.
	assertEq(t, "unexpected_count", d["unexpected_count"], float64(len(unexpected)))
	assertEq(t, "unused_count", d["unused_count"], float64(len(unused)))
}

func TestE2E_AccessReads_SelfAudit(t *testing.T) {
	h := newHarness(t)

	// Two privileged reads that must leave a trail.
	if code, _ := h.req("GET", "/v1/m/accessmap/graph?limit=10", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("graph read = %d", code)
	}
	if code, _ := h.req("GET", "/v1/m/accessmap/drift?limit=10", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("drift read = %d", code)
	}

	// The ledger records both, attributed to the real principal.
	aud := h.getJSON(h.adminToken, h.tenantA, "/v1/audit?from=1&limit=500")
	actions := map[string]string{} // action -> actor
	for _, ev := range items(aud) {
		if a, ok := ev["action"].(string); ok {
			actor, _ := ev["actor"].(string)
			actions[a] = actor
		}
	}
	for _, want := range []string{"access_map.graph.read", "access_map.drift.read"} {
		actor, ok := actions[want]
		if !ok {
			t.Errorf("audit ledger missing action %q", want)
			continue
		}
		if got := actor; len(got) < len("user:") || got[:5] != "user:" {
			t.Errorf("action %q actor = %q, want user:<uuid>", want, got)
		}
	}

	// The chain still verifies after the reads.
	v := h.getJSON(h.adminToken, h.tenantA, "/v1/audit/verify")
	assertEq(t, "audit.ok", v["ok"], true)
}
