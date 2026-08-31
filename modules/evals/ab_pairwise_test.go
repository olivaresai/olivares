// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// fakePairJudge is content-based and ORDER-NEUTRAL: the output containing "WIN"
// wins regardless of presentation order, so both order-swapped duals agree.
type fakePairJudge struct{}

func (fakePairJudge) JudgePair(_ context.Context, _ model.TenantID, req PairRequest) (PairVerdict, error) {
	fw := strings.Contains(req.OutputFirst, "WIN")
	sw := strings.Contains(req.OutputSecond, "WIN")
	switch {
	case fw && !sw:
		return PairVerdict{Winner: PairWinnerFirst, Reason: "first satisfied the criterion"}, nil
	case sw && !fw:
		return PairVerdict{Winner: PairWinnerSecond, Reason: "second satisfied the criterion"}, nil
	default:
		return PairVerdict{Winner: PairWinnerTie, Reason: "equivalent"}, nil
	}
}

// biasedPairJudge ALWAYS prefers the first-presented response — a pure position
// bias the order-swap must surface as inconsistency, never as a winner.
type biasedPairJudge struct{}

func (biasedPairJudge) JudgePair(_ context.Context, _ model.TenantID, _ PairRequest) (PairVerdict, error) {
	return PairVerdict{Winner: PairWinnerFirst, Reason: "first looked better"}, nil
}

func abPairwiseBody(t *testing.T, h *harness, admin string, tenant model.TenantID, suiteID string, outA, outB map[string]any) map[string]any {
	t.Helper()
	r := h.do("POST", "/v1/m/evals/ab", admin, map[string]any{
		"suite_ref": suiteID, "subject_ref": "p", "pairwise": true,
		"a": map[string]any{"label": "v1", "outputs": outA},
		"b": map[string]any{"label": "v2", "outputs": outB},
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("ab = %d %s", r.code, r.raw)
	}
	pw, _ := r.body["pairwise"].(map[string]any)
	if pw == nil {
		t.Fatalf("response missing the pairwise block: %s", r.raw)
	}
	return pw
}

// TestABPairwiseSkippedWithoutPairJudge proves the declared degradation: no wired
// PairJudge → mode=skipped with a reason, never a fabricated winner.
func TestABPairwiseSkippedWithoutPairJudge(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{"name": "ab", "subject_kind": "prompt", "scorer": scorerExact})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "x", "expected": "a"})

	pw := abPairwiseBody(t, h, admin, tenant, suiteID,
		map[string]any{"c1": "a"}, map[string]any{"c1": "b"})
	if pw["mode"] != "skipped" || pw["skip_reason"] == "" {
		t.Fatalf("pairwise = %v, want a declared skip", pw)
	}
}

// TestABPairwiseOrderSwapConsistent: an order-neutral judge produces consistent
// duals — wins are declared, position consistency is 1.0 with its CI and n.
func TestABPairwiseOrderSwapConsistent(t *testing.T) {
	h := newHarness(t, nil, WithPairJudge(fakePairJudge{}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{
		"name": "ab", "subject_kind": "prompt", "scorer": scorerExact, "criterion": "best answer",
	})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "x", "expected": "a"})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c2", "input": "y", "expected": "b"})

	pw := abPairwiseBody(t, h, admin, tenant, suiteID,
		map[string]any{"c1": "WIN one", "c2": "WIN two"}, map[string]any{"c1": "meh", "c2": "meh"})
	if pw["mode"] != "judged" {
		t.Fatalf("mode = %v, want judged", pw["mode"])
	}
	if intOf(pw["compared"]) != 2 || intOf(pw["a_wins"]) != 2 || intOf(pw["b_wins"]) != 0 {
		t.Fatalf("tallies = %v, want compared=2 a_wins=2", pw)
	}
	if pw["winner"] != "v1" {
		t.Fatalf("winner = %v, want v1", pw["winner"])
	}
	if intOf(pw["inconsistent"]) != 0 {
		t.Fatalf("inconsistent = %v, want 0", pw["inconsistent"])
	}
	pc, _ := pw["position_consistency"].(map[string]any)
	if pc == nil || floatOf(pc["rate"]) != 1.0 || intOf(pc["n"]) != 2 {
		t.Fatalf("position_consistency = %v, want rate 1.0 n 2", pw["position_consistency"])
	}
}

// TestABPairwisePositionBiasSurfaced: a judge that always prefers the first
// position disagrees with itself under the swap on every case — zero wins, every
// dual inconsistent, consistency 0.0. The bias becomes a NUMBER, not a winner.
func TestABPairwisePositionBiasSurfaced(t *testing.T) {
	h := newHarness(t, nil, WithPairJudge(biasedPairJudge{}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suiteID := h.createSuite(admin, tenant, map[string]any{
		"name": "ab", "subject_kind": "prompt", "scorer": scorerExact, "criterion": "best answer",
	})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c1", "input": "x", "expected": "a"})
	h.addCase(admin, tenant, suiteID, map[string]any{"case_key": "c2", "input": "y", "expected": "b"})

	pw := abPairwiseBody(t, h, admin, tenant, suiteID,
		map[string]any{"c1": "one", "c2": "two"}, map[string]any{"c1": "uno", "c2": "dos"})
	if intOf(pw["a_wins"]) != 0 || intOf(pw["b_wins"]) != 0 {
		t.Fatalf("a position-biased judge must win nothing: %v", pw)
	}
	if intOf(pw["inconsistent"]) != 2 || intOf(pw["ties"]) != 2 {
		t.Fatalf("inconsistent/ties = %v/%v, want 2/2", pw["inconsistent"], pw["ties"])
	}
	if pw["winner"] != nil && pw["winner"] != "" {
		t.Fatalf("winner = %v, want none", pw["winner"])
	}
	pc, _ := pw["position_consistency"].(map[string]any)
	if pc == nil || floatOf(pc["rate"]) != 0.0 {
		t.Fatalf("position_consistency = %v, want rate 0.0", pw["position_consistency"])
	}
}
