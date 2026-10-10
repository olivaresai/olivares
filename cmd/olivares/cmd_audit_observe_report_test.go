// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

func obsEvent(seq int64, kv map[string]any) observeReportEvent {
	return observeReportEvent{seq: seq, meta: kv}
}

func shadowMeta(decision, effective, source, grant, attempt string, downgrade bool) map[string]any {
	m := map[string]any{
		hookpep.MetaEnforcementMode:  hookpep.EnforcementModeObserve,
		"decision":                   effective,
		hookpep.MetaShadowedDecision: decision,
		hookpep.MetaShadowSource:     source,
		hookpep.MetaObserveScope:     hookpep.ObserveScopeTenant,
		hookpep.MetaObserveGrantID:   grant,
	}
	if attempt != "" {
		m[hookpep.MetaDecisionAttemptID] = attempt
	}
	if downgrade {
		m[hookpep.MetaEffectiveDowngrade] = true
	}
	return m
}

func TestBuildObserveReportBucketsAndGroups(t *testing.T) {
	events := []observeReportEvent{
		obsEvent(2, shadowMeta(claude.DecisionDeny, claude.DecisionAllow, hookpep.ShadowSourceLocalRule, "g1", "a1", false)),
		obsEvent(3, shadowMeta(claude.DecisionAsk, claude.DecisionAllow, hookpep.ShadowSourceLocalDefault, "g1", "a2", false)),
		obsEvent(4, shadowMeta(claude.DecisionDeny, claude.DecisionDeny, hookpep.ShadowSourcePDP, "g2", "a3", true)), // downgraded
		obsEvent(5, map[string]any{"decision": claude.DecisionAllow}),                                                // non-observe, ignored
	}
	rep := buildObserveReport(events)
	if rep.Total != 3 || rep.Proceeded != 2 || rep.Downgraded != 1 {
		t.Fatalf("totals: total=%d proceeded=%d downgraded=%d, want 3/2/1", rep.Total, rep.Proceeded, rep.Downgraded)
	}
	if len(rep.Grants) != 2 {
		t.Fatalf("want 2 grant groups, got %d", len(rep.Grants))
	}
	byGrant := map[string]observeReportGrant{}
	for _, g := range rep.Grants {
		byGrant[g.GrantID] = g
	}
	if g := byGrant["g1"]; g.Total != 2 || g.Proceeded != 2 || g.BySource[hookpep.ShadowSourceLocalRule] != 1 || g.BySource[hookpep.ShadowSourceLocalDefault] != 1 {
		t.Fatalf("g1 aggregation wrong: %+v", g)
	}
	if g := byGrant["g2"]; g.Total != 1 || g.Downgraded != 1 || g.BySource[hookpep.ShadowSourcePDP] != 1 || g.ByDecision[claude.DecisionDeny] != 1 {
		t.Fatalf("g2 aggregation wrong: %+v", g)
	}
}

// An ambiguous-commit double-write (an ALLOW + a downgraded DENY sharing one attempt id) is ONE
// logical decision whose effective terminal is the DENY — counted once, as downgraded.
func TestBuildObserveReportDedupesAmbiguousCommit(t *testing.T) {
	events := []observeReportEvent{
		obsEvent(2, shadowMeta(claude.DecisionDeny, claude.DecisionAllow, hookpep.ShadowSourceLocalRule, "g1", "shared", false)),
		obsEvent(3, shadowMeta(claude.DecisionDeny, claude.DecisionDeny, hookpep.ShadowSourceLocalRule, "g1", "shared", true)),
	}
	rep := buildObserveReport(events)
	if rep.Total != 1 || rep.Downgraded != 1 || rep.Proceeded != 0 {
		t.Fatalf("ambiguous pair must collapse to ONE downgraded decision, got total=%d down=%d proc=%d", rep.Total, rep.Downgraded, rep.Proceeded)
	}
}

// An observe row without a valid would-be decision is UNCLASSIFIABLE — surfaced as malformed,
// never silently dropped from the census nor miscounted.
func TestBuildObserveReportMalformed(t *testing.T) {
	events := []observeReportEvent{
		{seq: 2, meta: map[string]any{hookpep.MetaEnforcementMode: hookpep.EnforcementModeObserve, hookpep.MetaDecisionAttemptID: "a1"}}, // no shadowed_decision
		{seq: 3, meta: map[string]any{hookpep.MetaEnforcementMode: hookpep.EnforcementModeObserve, hookpep.MetaShadowedDecision: "bogus", hookpep.MetaDecisionAttemptID: "a2"}},
	}
	rep := buildObserveReport(events)
	if rep.Total != 0 || rep.Malformed != 2 {
		t.Fatalf("malformed observe rows: total=%d malformed=%d, want 0/2", rep.Total, rep.Malformed)
	}
}

// An unrecognized shadow_source is bucketed "unknown" (its own bucket) and still counted — never
// folded into a known source.
func TestBuildObserveReportUnknownSource(t *testing.T) {
	events := []observeReportEvent{
		obsEvent(2, shadowMeta(claude.DecisionDeny, claude.DecisionAllow, "some_future_source", "g1", "a1", false)),
	}
	rep := buildObserveReport(events)
	if rep.Total != 1 || rep.Grants[0].BySource["unknown"] != 1 {
		t.Fatalf("unknown source must bucket as 'unknown' and count, got %+v", rep.Grants)
	}
}

// A logical decision whose attempt id has ANY corrupt sibling is UNTRUSTWORTHY — the corrupt row
// could be the downgrade of the valid allow, so classifying it as a clean "proceeded" would
// understate enforcement (the unsafe direction). It must be flagged Malformed (⇒ INCOMPLETE), never
// silently counted. (Reconciled: the panel wanted the valid row counted; the Codex diff review showed
// that is unsafe — a corrupt downgrade sibling would be misread as proceeded.)
func TestBuildObserveReportCorruptSiblingTaintsDecision(t *testing.T) {
	events := []observeReportEvent{
		obsEvent(2, shadowMeta(claude.DecisionDeny, claude.DecisionAllow, hookpep.ShadowSourceLocalRule, "g1", "shared", false)),                                                    // valid allow-shadow
		{seq: 3, meta: map[string]any{hookpep.MetaEnforcementMode: hookpep.EnforcementModeObserve, hookpep.MetaShadowedDecision: "bogus", hookpep.MetaDecisionAttemptID: "shared"}}, // corrupt sibling (could be the downgrade)
	}
	rep := buildObserveReport(events)
	if rep.Total != 0 || rep.Proceeded != 0 {
		t.Fatalf("a decision with a corrupt sibling must NOT be counted as clean, got total=%d proceeded=%d", rep.Total, rep.Proceeded)
	}
	if rep.Malformed != 1 {
		t.Fatalf("a corrupt sibling must taint the decision Malformed (⇒ incomplete), got malformed=%d", rep.Malformed)
	}
}

// END-TO-END: real shadows written by Decide, read back via WalkCanonical, aggregated. This is the
// writer↔reader contract test — a regression that (e.g.) reverted to Walk (which nils Meta) or
// renamed a meta key would ZERO the report here (the vacuous-gate lesson).
func TestObserveReportEndToEnd(t *testing.T) {
	policy := hookpep.PolicyDoc{
		Version: "e2e-report/v1",
		Default: claude.DecisionAllow,
		Rules:   []hookpep.PolicyRule{{Tool: "Bash", Decision: claude.DecisionDeny, Reason: "no shell (authored)"}},
	}
	f := newHookLedgerFixture(t, policy)
	f.dec.Tenants[f.tenant] = hookTenantConfig(t, f.tenant, hookpep.TenantConfig{
		Policy: policy, Enforcement: hookpep.EnforcementModeObserve, ObserveUntil: observeTestFarFuture.Format(time.RFC3339),
	})
	f.dec.Clock = time.Now

	// (1) a local-rule deny shadow (Bash tool rule).
	if res, _ := decideAnchored(t, f, hookLedgerInput(f.tenant, "Bash", hookpep.ResourceKindShell, "bash", "write")); res.Permission != claude.DecisionAllow {
		t.Fatalf("setup: local-rule deny must shadow → allow, got %q", res.Permission)
	}
	// (2) a PDP business forbid shadow on a different call.
	f.dec.Eval = classedEval{allow: false, class: auth.ClassPolicy, reason: "pdp business rule"}
	if res, _ := decideAnchored(t, f, hookLedgerInput(f.tenant, "Read", hookpep.ResourceKindFile, "/srv/x", "read")); res.Permission != claude.DecisionAllow {
		t.Fatalf("setup: pdp forbid must shadow → allow, got %q", res.Permission)
	}

	// Read the ledger the way the CLI does: WalkCanonical (Walk would nil Meta).
	var events []observeReportEvent
	if err := f.store.View(context.Background(), f.tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("ledger does not expose canonical metadata")
		}
		return walker.WalkCanonical(context.Background(), 1, func(ev model.AuditEvent, canonical string, _ []byte) error {
			var meta map[string]any
			if err := json.Unmarshal([]byte(canonical), &meta); err != nil {
				return err
			}
			events = append(events, observeReportEvent{seq: ev.Seq, meta: meta})
			return nil
		})
	}); err != nil {
		t.Fatalf("walk canonical: %v", err)
	}

	rep := buildObserveReport(events)
	if rep.Total != 2 || rep.Proceeded != 2 || rep.Downgraded != 0 {
		t.Fatalf("end-to-end report: total=%d proceeded=%d downgraded=%d, want 2/2/0", rep.Total, rep.Proceeded, rep.Downgraded)
	}
	if len(rep.Grants) != 1 {
		t.Fatalf("both shadows share one grant window, want 1 group, got %d", len(rep.Grants))
	}
	g := rep.Grants[0]
	if g.BySource[hookpep.ShadowSourceLocalRule] != 1 || g.BySource[hookpep.ShadowSourcePDP] != 1 {
		t.Fatalf("by_source wrong: %+v", g.BySource)
	}
	if g.ByDecision[claude.DecisionDeny] != 2 {
		t.Fatalf("by_decision wrong: %+v", g.ByDecision)
	}
}

// classedEval is a PDP overlay whose deny carries a chosen provenance class, so a test
// can exercise a POLICY forbid (shadowable) vs an INVARIANT/unknown forbid (never shadowed).
type classedEval struct {
	allow  bool
	class  auth.DecisionClass
	reason string
}

func (e classedEval) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{Allow: e.allow, Reason: firstNonEmptyStr(e.reason, "classed pdp"), Class: e.class}, nil
}

// decideAnchored runs one Decide and returns its verdict plus the single new canonical ledger
// event's meta. Valid for allow/deny (both anchor exactly one entry); an ask is anchored by
// the HITL bridge, so it is not used for ask paths.
func decideAnchored(t *testing.T, f *hookLedgerFixture, in claude.HookDecisionInput) (claude.HookDecisionResult, map[string]any) {
	t.Helper()
	before := hookLedgerHead(t, f.store, f.tenant)
	res, err := f.dec.Decide(context.Background(), in, "test-bearer")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	events := canonicalLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1)
	if len(events) != 1 {
		t.Fatalf("new ledger events = %d, want exactly 1 (res=%q %q)", len(events), res.Permission, res.Reason)
	}
	return res, events[0].meta
}
