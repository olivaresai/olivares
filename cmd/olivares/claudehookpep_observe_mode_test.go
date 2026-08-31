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

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// observeTestFarFuture is a fixed, well-past-any-run expiry so an observe fixture holds a LIVE
// grant (E3 requires observe_until in the future); the expiry-specific tests set their own.
var observeTestFarFuture = time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)

// --- constrained-observe mode: DUAL matrix -----------------------------------------
// (a) observe SHADOWS an AUTHORED (ClassPolicy) deny/ask → allow + recorded shadow.
// (b) observe still ENFORCES every platform invariant (identity confinement, kill
//     switch, firewall, fail-closed errors, Bash ambiguity, malformed rule/default, unknown
//     class) → deny, NEVER shadowed.
// (c) enforce (the default) is byte-for-byte the prior behavior (contrasted inline + the
//     whole pre-existing hook/bash/firewall suite still passes).

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

// classedScopedForbid forbids every projectable request with a chosen class: ClassPolicy for
// an authored scoped forbid (shadowable), ClassInvariant for the workspace-confinement /
// fail-closed forbid (must enforce even in observe).
type classedScopedForbid struct {
	class  auth.DecisionClass
	reason string
}

func (f classedScopedForbid) Scoped(context.Context, auth.Request) (auth.ScopedDecision, error) {
	return auth.ScopedDecision{Effect: auth.EffectForbid, Reason: firstNonEmptyStr(f.reason, "classed scoped forbid"), Class: f.class}, nil
}

// setHookMode sets the tenant's mode. Switching to observe also installs a LIVE grant (a
// far-future window + its content id) and a clock, since E3 gates observe on an unexpired
// grant; switching to enforce clears the grant. Expiry-specific tests override via setObserveGrant.
func setHookMode(f *hookLedgerFixture, m hookEnforcementMode) {
	rt := f.dec.tenants[f.tenant]
	rt.mode = m
	if m == modeObserve {
		boot := time.Now()
		rt.observeUntil = observeTestFarFuture
		rt.observeBootMono = boot
		rt.observeWindow = observeTestFarFuture.Sub(boot)
		rt.observeGrantID = observeGrantID(f.tenant, rt.observeUntil, hookPolicyFingerprint(rt.policy))
		if f.dec.clock == nil {
			f.dec.clock = time.Now
		}
		f.dec.observeExpired.Delete(f.tenant)
	} else {
		rt.observeUntil = time.Time{}
		rt.observeBootMono = time.Time{}
		rt.observeWindow = 0
		rt.observeGrantID = ""
	}
	f.dec.tenants[f.tenant] = rt
}

// setObserveGrant installs an explicit observe window + clock for expiry tests, clearing any
// prior expired-latch so the fixture starts from a known state. bootMono/window are anchored to
// the fixture's initial clock reading (the wall-clock path is what fake clocks exercise).
func setObserveGrant(f *hookLedgerFixture, until time.Time, now func() time.Time) {
	rt := f.dec.tenants[f.tenant]
	boot := now()
	rt.mode = modeObserve
	rt.observeUntil = until
	rt.observeBootMono = boot
	rt.observeWindow = until.Sub(boot)
	rt.observeGrantID = observeGrantID(f.tenant, until, hookPolicyFingerprint(rt.policy))
	f.dec.tenants[f.tenant] = rt
	f.dec.clock = now
	f.dec.observeExpired.Delete(f.tenant)
}

func observeLedgerFixture(t *testing.T, policy hookPolicyDoc) *hookLedgerFixture {
	t.Helper()
	f := newHookLedgerFixture(t, policy)
	setHookMode(f, modeObserve)
	return f
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

func assertShadow(t *testing.T, meta map[string]any, wantShadowed, reasonSubstr string) {
	t.Helper()
	if meta["decision"] != claude.DecisionAllow {
		t.Fatalf("a shadowed verdict must be anchored as an ALLOW, got decision=%#v", meta["decision"])
	}
	if meta["enforcement_mode"] != "observe" {
		t.Fatalf("enforcement_mode = %#v, want %q", meta["enforcement_mode"], "observe")
	}
	if meta["shadowed_decision"] != wantShadowed {
		t.Fatalf("shadowed_decision = %#v, want %q", meta["shadowed_decision"], wantShadowed)
	}
	if sr, _ := meta["shadow_reason"].(string); !strings.Contains(sr, reasonSubstr) {
		t.Fatalf("shadow_reason = %q, want substring %q", sr, reasonSubstr)
	}
}

func assertNoShadow(t *testing.T, meta map[string]any) {
	t.Helper()
	if _, ok := meta["enforcement_mode"]; ok {
		t.Fatalf("an invariant/enforce decision must NOT carry a shadow record: %#v", meta)
	}
	if _, ok := meta["shadowed_decision"]; ok {
		t.Fatalf("an invariant/enforce decision must NOT carry shadowed_decision: %#v", meta)
	}
}

func TestResolveEnforcementMode(t *testing.T) {
	cases := map[string]hookEnforcementMode{
		"":          modeEnforce, // fail-safe default
		"enforce":   modeEnforce,
		"observe":   modeObserve,
		"OBSERVE":   modeObserve, // case-insensitive
		" observe ": modeObserve, // trimmed
		"shadow":    modeEnforce, // NOT an alias — only "observe" selects observe
		"typo":      modeEnforce, // unknown ⇒ enforce (never silently drop enforcement)
	}
	for in, want := range cases {
		if got := resolveEnforcementMode(in); got != want {
			t.Errorf("resolveEnforcementMode(%q) = %d, want %d", in, got, want)
		}
	}
}

// (a) SHADOW: authored business denials become recorded allows in observe --------------------

func TestObserveShadowsLocalRuleDeny(t *testing.T) {
	policy := hookPolicyDoc{
		Version: "obs-rule-deny/v1",
		Default: claude.DecisionAllow,
		Rules:   []hookPolicyRule{{Tool: "Bash", Decision: claude.DecisionDeny, Reason: "shell denied by policy"}},
	}

	fe := newHookLedgerFixture(t, policy) // enforce (default)
	if res, _ := decideAnchored(t, fe, hookLedgerInput(fe.tenant, "Bash", hookResourceKindShell, "bash", "write")); res.Permission != claude.DecisionDeny {
		t.Fatalf("enforce must DENY the authored rule, got %q", res.Permission)
	}

	fo := observeLedgerFixture(t, policy)
	res, meta := decideAnchored(t, fo, hookLedgerInput(fo.tenant, "Bash", hookResourceKindShell, "bash", "write"))
	if res.Permission != claude.DecisionAllow {
		t.Fatalf("observe must SHADOW the authored rule to allow, got %q (%s)", res.Permission, res.Reason)
	}
	assertShadow(t, meta, claude.DecisionDeny, "shell denied by policy")
	if meta["mode"] != "write" { // the access-mode key must NOT be overwritten by the enforcement mode
		t.Fatalf("meta.mode (access mode) clobbered: %#v", meta["mode"])
	}
	// Shadow-tail properties: a shadowed would-be deny surfaces a NEUTRAL/empty reason to the
	// agent (clean measurement) and drops the would-be verdict's Block/rewrite.
	if res.Reason != "" {
		t.Fatalf("a shadowed would-be deny must surface an empty reason, got %q", res.Reason)
	}
	if res.Block {
		t.Fatalf("a shadowed would-be deny must not carry Block")
	}
	if res.UpdatedInput != nil {
		t.Fatalf("a shadowed would-be deny must not carry a rewrite, got %#v", res.UpdatedInput)
	}
}

func TestObserveShadowsExplicitDefaultDeny(t *testing.T) {
	policy := hookPolicyDoc{Version: "obs-default-deny/v1", Default: claude.DecisionDeny}

	fe := newHookLedgerFixture(t, policy)
	if res, _ := decideAnchored(t, fe, hookLedgerInput(fe.tenant, "Read", hookResourceKindFile, "/srv/x", "read")); res.Permission != claude.DecisionDeny {
		t.Fatalf("enforce explicit default deny must DENY, got %q", res.Permission)
	}

	fo := observeLedgerFixture(t, policy)
	res, meta := decideAnchored(t, fo, hookLedgerInput(fo.tenant, "Read", hookResourceKindFile, "/srv/x", "read"))
	if res.Permission != claude.DecisionAllow {
		t.Fatalf("observe must shadow an EXPLICIT default deny, got %q", res.Permission)
	}
	assertShadow(t, meta, claude.DecisionDeny, "deny-closed default")
}

func TestObserveShadowsPDPPolicyForbid(t *testing.T) {
	fo := observeLedgerFixture(t, hookPolicyDoc{Default: claude.DecisionAllow})
	fo.dec.eval = classedEval{allow: false, class: auth.ClassPolicy, reason: "pdp business rule"}
	res, meta := decideAnchored(t, fo, hookLedgerInput(fo.tenant, "Read", hookResourceKindFile, "/srv/x", "read"))
	if res.Permission != claude.DecisionAllow {
		t.Fatalf("observe must shadow an AUTHORED PDP forbid, got %q", res.Permission)
	}
	assertShadow(t, meta, claude.DecisionDeny, "PDP policy forbids")
}

func TestObserveShadowsAuthoredScopedForbid(t *testing.T) {
	fo := observeLedgerFixture(t, hookPolicyDoc{Default: claude.DecisionAllow})
	fo.dec.scoped = classedScopedForbid{class: auth.ClassPolicy, reason: "authored scoped forbid"}
	in := hookLedgerInput(fo.tenant, "mcp__payments__charge", hookResourceKindMCP, "payments/charge", "write")
	res, meta := decideAnchored(t, fo, in)
	if res.Permission != claude.DecisionAllow {
		t.Fatalf("observe must shadow an AUTHORED scoped forbid, got %q", res.Permission)
	}
	assertShadow(t, meta, claude.DecisionDeny, "central scoped policy forbids")
}

// A local ALLOW that carries a governed rewrite must keep it even when an OVERLAY policy is
// shadowed (the call proceeds with the local allow's effects); the shadow rides to the ledger.
func TestObserveLocalAllowKeepsRewriteUnderOverlayShadow(t *testing.T) {
	policy := hookPolicyDoc{
		Version: "obs-allow-rewrite/v1",
		Default: claude.DecisionAllow,
		Rules:   []hookPolicyRule{{Tool: "Write", Decision: claude.DecisionAllow, Rewrite: map[string]any{"dry_run": true}}},
	}
	fo := observeLedgerFixture(t, policy)
	fo.dec.eval = classedEval{allow: false, class: auth.ClassPolicy, reason: "pdp business rule"}
	res, meta := decideAnchored(t, fo, hookLedgerInput(fo.tenant, "Write", hookResourceKindFile, "/srv/x", "write"))
	if res.Permission != claude.DecisionAllow {
		t.Fatalf("local allow + shadowed overlay must allow, got %q", res.Permission)
	}
	if res.UpdatedInput["dry_run"] != true {
		t.Fatalf("the local allow's governed rewrite must survive an overlay shadow, got %#v", res.UpdatedInput)
	}
	assertShadow(t, meta, claude.DecisionDeny, "PDP policy forbids")
}

// (b) INVARIANT: platform controls still enforce in observe ----------------------------------

// THE escape test: a WORKSPACE-CONFINEMENT forbid is ClassInvariant and must never be
// shadowed, or observe would re-open the confinement escape closed.
func TestObserveScopedConfinementStillDenies(t *testing.T) {
	fo := observeLedgerFixture(t, hookPolicyDoc{Default: claude.DecisionAllow})
	fo.dec.scoped = classedScopedForbid{class: auth.ClassInvariant, reason: "workspace confinement"}
	in := hookLedgerInput(fo.tenant, "mcp__x__y", hookResourceKindMCP, "server/tool", "write")
	res, meta := decideAnchored(t, fo, in)
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("workspace confinement MUST enforce in observe (escape) — got %q", res.Permission)
	}
	assertNoShadow(t, meta)
}

func TestObservePDPEvalErrorStillDenies(t *testing.T) {
	fo := observeLedgerFixture(t, hookPolicyDoc{Default: claude.DecisionAllow})
	fo.dec.eval = erroringEval{err: errors.New("pdp down")}
	res, meta := decideAnchored(t, fo, hookLedgerInput(fo.tenant, "Read", hookResourceKindFile, "/srv/x", "read"))
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("a fail-closed PDP error MUST deny in observe, got %q", res.Permission)
	}
	assertNoShadow(t, meta)
}

func TestObserveUnknownClassDenyStillDenies(t *testing.T) {
	fo := observeLedgerFixture(t, hookPolicyDoc{Default: claude.DecisionAllow})
	fo.dec.eval = classedEval{allow: false, class: auth.DecisionClass(42), reason: "unknown-future"}
	res, meta := decideAnchored(t, fo, hookLedgerInput(fo.tenant, "Read", hookResourceKindFile, "/srv/x", "read"))
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("an UNKNOWN, non-ClassPolicy forbid MUST deny in observe (== ClassPolicy only), got %q", res.Permission)
	}
	assertNoShadow(t, meta)
}

func TestObserveInvalidDefaultStillDenies(t *testing.T) {
	// A typo'd default normalizes to "" → deny-closed, ClassInvariant → NEVER shadowed.
	fo := observeLedgerFixture(t, hookPolicyDoc{Version: "obs-typo/v1", Default: "sometimestypo"})
	res, meta := decideAnchored(t, fo, hookLedgerInput(fo.tenant, "Read", hookResourceKindFile, "/srv/x", "read"))
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("an unrecognized default must deny-closed (invariant) in observe, got %q", res.Permission)
	}
	assertNoShadow(t, meta)
}

func TestObserveConfigChangeDefaultDenyStillDenies(t *testing.T) {
	fo := observeLedgerFixture(t, hookPolicyDoc{})
	in := hookLedgerInput(fo.tenant, "", "", "", "")
	in.Event = "ConfigChange"
	res, meta := decideAnchored(t, fo, in)
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("ConfigChange deny-closed default is invariant; must deny in observe, got %q", res.Permission)
	}
	assertNoShadow(t, meta)
}

// A pre-disposition invariant (kill switch) short-circuits before the observe logic runs, so
// it can never be shadowed — pinned here as a regression.
func TestObserveKillSwitchUnreadableStillDenies(t *testing.T) {
	fo := observeLedgerFixture(t, hookPolicyDoc{Default: claude.DecisionAllow})
	fo.dec.stops = failingKillSwitchGuard{}
	res, meta := decideAnchored(t, fo, hookLedgerInput(fo.tenant, "Read", hookResourceKindFile, "/srv/x", "read"))
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("kill-switch unreadable is fail-closed invariant; must deny in observe, got %q", res.Permission)
	}
	assertNoShadow(t, meta)
}

// --- Bash ambiguity + firewall reorder go through the full HTTP PEP path (needs a command) ---

func setPEPObserve(f *hookPEPFixture) {
	rt := f.dec.tenants[f.tenant]
	boot := time.Now()
	rt.mode = modeObserve
	rt.observeUntil = observeTestFarFuture // E3: observe needs a LIVE grant, else it enforces
	rt.observeBootMono = boot
	rt.observeWindow = observeTestFarFuture.Sub(boot)
	rt.observeGrantID = observeGrantID(f.tenant, rt.observeUntil, hookPolicyFingerprint(rt.policy))
	f.dec.tenants[f.tenant] = rt
	f.dec.observeExpired.Delete(f.tenant)
}

// THE second escape test (Codex-found): a POLICY base-deny that would be shadowed must still
// deny when the Bash command carries an INVARIANT ambiguity the path scan could not resolve —
// otherwise a shadowed base-deny hides an un-inspectable command.
func TestObserveBashAmbiguityIsNotShadowable(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-obs-bash@e2e.test")
	// default deny = a POLICY base-deny; a file path rule makes the policy path-scoped so the
	// Bash scanner runs.
	policy := hookPolicyDoc{
		Version: "obs-bash-guard/v1",
		Default: claude.DecisionDeny,
		Rules:   []hookPolicyRule{{ResourceKind: hookResourceKindFile, Paths: []string{"/etc/**"}, Decision: claude.DecisionDeny}},
	}

	fe := newHookPEPFixture(t, h, policy, false, fixedEval{allow: true}, false)
	if got := decisionOf(fe.call(t, "Bash", map[string]any{"command": "cat /tmp/ok"}, tok, h.tenantA)); got != claude.DecisionDeny {
		t.Fatalf("enforce base-deny must DENY, got %q", got)
	}

	fo := newHookPEPFixture(t, h, policy, false, fixedEval{allow: true}, false)
	setPEPObserve(fo)
	if got := decisionOf(fo.call(t, "Bash", map[string]any{"command": "cat /tmp/ok"}, tok, h.tenantA)); got != claude.DecisionAllow {
		t.Fatalf("observe should SHADOW a policy base-deny on a CLEAN command → allow, got %q", got)
	}
	if got := decisionOf(fo.call(t, "Bash", map[string]any{"command": `cat "unterminated`}, tok, h.tenantA)); got != claude.DecisionDeny {
		t.Fatalf("observe must NOT shadow an AMBIGUOUS Bash command (escape) → deny, got %q", got)
	}
}

// The firewall is an invariant that must ALWAYS run in observe — including when the local
// policy deny is shadowable (the enforce-mode skip must not silently exempt the call from DLP).
func TestObserveFirewallStillEnforces(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-obs-fw@e2e.test")
	fo := newHookPEPFixture(t, h, hookPolicyDoc{Default: claude.DecisionDeny}, false, fixedEval{allow: true}, false)
	setPEPObserve(fo)
	fo.dec.hookInspector = &fakeHookInspector{forward: false}
	out := fo.call(t, "Write", map[string]any{"file_path": "/app/c.env", "content": "AKIAIOSFODNN7EXAMPLE"}, tok, h.tenantA)
	if got := decisionOf(out); got != claude.DecisionDeny {
		t.Fatalf("firewall (invariant) must run + deny in observe even when the local policy deny is shadowable; got %q (%v)", got, out)
	}
}

// enforce is the fail-safe default even for a tenant that never set the field: an authored
// deny still denies (contrast to the observe fixtures above).
func TestEnforceIsTheDefaultMode(t *testing.T) {
	policy := hookPolicyDoc{Version: "enforce-default/v1", Default: claude.DecisionAllow,
		Rules: []hookPolicyRule{{Tool: "Bash", Decision: claude.DecisionDeny, Reason: "no shell"}}}
	f := newHookLedgerFixture(t, policy) // mode field left zero
	if f.dec.tenants[f.tenant].mode != modeEnforce {
		t.Fatalf("zero-value mode must be modeEnforce (fail-safe)")
	}
	res, meta := decideAnchored(t, f, hookLedgerInput(f.tenant, "Bash", hookResourceKindShell, "bash", "write"))
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("default (enforce) must DENY the authored rule, got %q", res.Permission)
	}
	assertNoShadow(t, meta)
}

// A CLEAN authored Bash path-deny RULE (recognized decision) is business policy → shadowable in
// observe. Regression pin for the raw-deny-pattern over-tag that made every bash-rule deny invariant.
func TestObserveShadowsCleanBashRuleDeny(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-obs-bashrule@e2e.test")
	policy := hookPolicyDoc{
		Version: "obs-bashrule/v1",
		Default: claude.DecisionAllow, // base allows; the Bash RULE is the authored deny
		Rules:   []hookPolicyRule{{ResourceKind: hookResourceKindShell, Paths: []string{"/etc/**"}, Decision: claude.DecisionDeny, Reason: "no /etc access"}},
	}
	fe := newHookPEPFixture(t, h, policy, false, fixedEval{allow: true}, false)
	if got := decisionOf(fe.call(t, "Bash", map[string]any{"command": "cat /etc/hosts"}, tok, h.tenantA)); got != claude.DecisionDeny {
		t.Fatalf("enforce clean bash-rule deny must DENY, got %q", got)
	}
	fo := newHookPEPFixture(t, h, policy, false, fixedEval{allow: true}, false)
	setPEPObserve(fo)
	if got := decisionOf(fo.call(t, "Bash", map[string]any{"command": "cat /etc/hosts"}, tok, h.tenantA)); got != claude.DecisionAllow {
		t.Fatalf("observe must SHADOW a clean authored bash-RULE deny → allow, got %q", got)
	}
}

// A typo'd Bash rule decision (config error → deny-closed) is ClassInvariant and must DOMINATE a
// shadowable policy base-deny. THE escape: without the fix, the base policy-deny is shadowed and the
// unknown-decision rule is ignored → the command runs.
func TestObserveBashTypoDecisionIsNotShadowable(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "agent-obs-bashtypo@e2e.test")
	policy := hookPolicyDoc{
		Version: "obs-bashtypo/v1",
		Default: claude.DecisionDeny, // a POLICY base-deny that observe WOULD shadow
		Rules:   []hookPolicyRule{{ResourceKind: hookResourceKindShell, Paths: []string{"/etc/**"}, Decision: "typo-not-a-decision"}},
	}
	fo := newHookPEPFixture(t, h, policy, false, fixedEval{allow: true}, false)
	setPEPObserve(fo)
	if got := decisionOf(fo.call(t, "Bash", map[string]any{"command": "cat /etc/passwd"}, tok, h.tenantA)); got != claude.DecisionDeny {
		t.Fatalf("a typo'd bash deny rule must force invariant deny in observe (escape), not be shadowed; got %q", got)
	}
}

// The business-ASK shadow leg: an authored ClassPolicy ask is shadowed to a clean allow (NOT queued
// to HITL). The ledger fixture has no bridge, so a regression that reached gateViaHITL would surface
// as a deny-closed DENY, catching it.
func TestObserveShadowsAuthoredAsk(t *testing.T) {
	fo := observeLedgerFixture(t, hookPolicyDoc{Version: "obs-ask/v1", Default: claude.DecisionAsk})
	res, meta := decideAnchored(t, fo, hookLedgerInput(fo.tenant, "Read", hookResourceKindFile, "/srv/x", "read"))
	if res.Permission != claude.DecisionAllow {
		t.Fatalf("observe must SHADOW an authored ask → allow (never gate it), got %q (%s)", res.Permission, res.Reason)
	}
	assertShadow(t, meta, claude.DecisionAsk, "human approval required")
}

// invariant dominates a shadow: a PDP policy forbid is shadowed, but a scoped CONFINEMENT (invariant)
// on the same call still DENIES, and the ridden shadow is NOT attached to that deny.
func TestObservePDPShadowPlusConfinementStillDenies(t *testing.T) {
	fo := observeLedgerFixture(t, hookPolicyDoc{Default: claude.DecisionAllow})
	fo.dec.eval = classedEval{allow: false, class: auth.ClassPolicy, reason: "pdp business rule"}
	fo.dec.scoped = classedScopedForbid{class: auth.ClassInvariant, reason: "workspace confinement"}
	in := hookLedgerInput(fo.tenant, "mcp__x__y", hookResourceKindMCP, "server/tool", "write")
	res, meta := decideAnchored(t, fo, in)
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("a scoped confinement invariant must dominate a PDP policy shadow → deny, got %q", res.Permission)
	}
	assertNoShadow(t, meta)
}

// A prior overlay ClassPolicy shadow rides to the terminal allow's ledger when an INVARIANT ask
// (unresolved relative path) is HITL-approved — evidence-complete for a call that actually proceeds.
func TestObservePriorShadowRidesApprovedInvariantAsk(t *testing.T) {
	policy := hookPolicyDoc{
		Version: "obs-ask-ride/v1",
		Default: claude.DecisionAllow,
		Rules:   []hookPolicyRule{{ResourceKind: hookResourceKindFile, Paths: []string{"/etc/**"}, Decision: claude.DecisionDeny}}, // path-scoped
	}
	fo := observeLedgerFixture(t, policy)
	fo.dec.eval = classedEval{allow: false, class: auth.ClassPolicy, reason: "pdp business rule"} // overlay shadow
	fo.dec.bridge = &fakeOpener{status: nbApproved}                                               // HITL approves the invariant ask
	// a RELATIVE file ref does not resolve (root is empty) → an INVARIANT unresolved-path ask.
	in := hookLedgerInput(fo.tenant, "Read", hookResourceKindFile, "relative/secret", "read")
	res, meta := decideAnchored(t, fo, in)
	if res.Permission != claude.DecisionAllow {
		t.Fatalf("an invariant ask approved by HITL must allow, got %q (%s)", res.Permission, res.Reason)
	}
	assertShadow(t, meta, claude.DecisionDeny, "PDP policy forbids")
}

// --- E3: observe-grant expiry (deny-closed) + provenance/grant-id ledger meta ---------

// e3AuthoredDenyPolicy: base allows; an authored Bash rule denies → a shadowable ClassPolicy deny.
func e3AuthoredDenyPolicy() hookPolicyDoc {
	return hookPolicyDoc{Version: "e3-expiry/v1", Default: claude.DecisionAllow,
		Rules: []hookPolicyRule{{Tool: "Bash", Decision: claude.DecisionDeny, Reason: "no shell (authored)"}}}
}

func e3BashInput(f *hookLedgerFixture) claude.HookDecisionInput {
	return hookLedgerInput(f.tenant, "Bash", hookResourceKindShell, "bash", "write")
}

// A LIVE grant (now < observe_until) shadows the authored deny and stamps the grant id + scope.
func TestObserveGrantActiveShadowsAndStampsGrant(t *testing.T) {
	f := newHookLedgerFixture(t, e3AuthoredDenyPolicy())
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	setObserveGrant(f, now.Add(time.Hour), func() time.Time { return now })
	res, meta := decideAnchored(t, f, e3BashInput(f))
	if res.Permission != claude.DecisionAllow {
		t.Fatalf("a LIVE observe grant must SHADOW the authored deny → allow, got %q", res.Permission)
	}
	assertShadow(t, meta, claude.DecisionDeny, "no shell")
	if meta[metaShadowSource] != shadowSourceLocalRule {
		t.Fatalf("shadow_source = %#v, want %q", meta[metaShadowSource], shadowSourceLocalRule)
	}
	if meta[metaObserveScope] != observeScopeTenant {
		t.Fatalf("observe_scope = %#v, want %q", meta[metaObserveScope], observeScopeTenant)
	}
	gid, _ := meta[metaObserveGrantID].(string)
	if !strings.HasPrefix(gid, "obsgrant-") {
		t.Fatalf("observe_grant_id = %#v, want an obsgrant- digest", meta[metaObserveGrantID])
	}
}

// An EXPIRED grant (now >= observe_until) deny-closes to ENFORCE — the authored deny denies, no shadow.
func TestObserveGrantExpiredEnforces(t *testing.T) {
	f := newHookLedgerFixture(t, e3AuthoredDenyPolicy())
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	setObserveGrant(f, now.Add(-time.Minute), func() time.Time { return now })
	res, meta := decideAnchored(t, f, e3BashInput(f))
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("an EXPIRED observe grant must ENFORCE → deny, got %q", res.Permission)
	}
	assertNoShadow(t, meta)
}

// The boundary is INCLUSIVE (now == observe_until ⇒ expired), mirroring approvals' !now.Before(exp).
func TestObserveGrantExpiryBoundaryInclusive(t *testing.T) {
	f := newHookLedgerFixture(t, e3AuthoredDenyPolicy())
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	setObserveGrant(f, now, func() time.Time { return now }) // now == until
	res, _ := decideAnchored(t, f, e3BashInput(f))
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("now==observe_until is EXPIRED (inclusive) → deny, got %q", res.Permission)
	}
}

// A backward clock jump must NOT resurrect an expired window: the expired-latch keeps it enforced.
func TestObserveClockRollbackLatchStaysEnforced(t *testing.T) {
	f := newHookLedgerFixture(t, e3AuthoredDenyPolicy())
	until := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	clock := until.Add(time.Minute) // start PAST the window
	setObserveGrant(f, until, func() time.Time { return clock })
	if res, _ := decideAnchored(t, f, e3BashInput(f)); res.Permission != claude.DecisionDeny {
		t.Fatalf("expired grant must deny (and latch), got %q", res.Permission)
	}
	clock = until.Add(-time.Hour) // roll the clock BACK inside the window
	if res, _ := decideAnchored(t, f, e3BashInput(f)); res.Permission != claude.DecisionDeny {
		t.Fatalf("clock rollback must NOT reactivate an expired grant (latch) → deny, got %q", res.Permission)
	}
}

// THE Codex P0: a clock rollback with NO traffic during the window (the latch never armed) must
// STILL enforce — the MONOTONIC elapsed test expires the grant on real time-since-boot even when
// the wall clock is rolled back before observeUntil. Verified with real monotonic times: boot 1h
// ago, window 1 min, so the grant is long-expired by elapsed even though observeUntil is far future.
func TestObserveMonotonicElapsedBeatsWallRollback(t *testing.T) {
	f := newHookLedgerFixture(t, e3AuthoredDenyPolicy())
	rt := f.dec.tenants[f.tenant]
	rt.mode = modeObserve
	rt.observeUntil = time.Now().Add(24 * time.Hour) // wall clock says the window is WIDE open
	rt.observeBootMono = time.Now().Add(-time.Hour)  // but boot was a REAL hour ago (monotonic)
	rt.observeWindow = time.Minute                   // and the window was only a minute
	rt.observeGrantID = "obsgrant-mono-test"
	f.dec.tenants[f.tenant] = rt
	f.dec.clock = time.Now
	f.dec.observeExpired.Delete(f.tenant)
	res, meta := decideAnchored(t, f, e3BashInput(f))
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("monotonic elapsed past the window must ENFORCE even when the wall clock says active, got %q", res.Permission)
	}
	assertNoShadow(t, meta)
}

// A nil clock (some fixtures build the decider without one) must deny-close, never panic nor observe.
func TestObserveNilClockEnforces(t *testing.T) {
	f := newHookLedgerFixture(t, e3AuthoredDenyPolicy())
	setObserveGrant(f, time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC), time.Now)
	f.dec.clock = nil // drop the clock
	res, meta := decideAnchored(t, f, e3BashInput(f))
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("a nil clock must ENFORCE (deny-closed), got %q", res.Permission)
	}
	assertNoShadow(t, meta)
}

// The boot resolver: observe is honored ONLY with a valid FUTURE observe_until; everything else
// (enforce mode, missing/invalid/past window) resolves to ENFORCE (deny-closed) with no grant id.
func TestResolveObserveGrant(t *testing.T) {
	tid := newHookLedgerFixture(t, hookPolicyDoc{}).tenant
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour).Format(time.RFC3339)
	past := now.Add(-time.Hour).Format(time.RFC3339)
	cases := []struct {
		name      string
		enf       string
		until     string
		wantMode  hookEnforcementMode
		wantGrant bool
	}{
		{"enforce ignores until", "enforce", future, modeEnforce, false},
		{"observe valid future", "observe", future, modeObserve, true},
		{"observe missing until", "observe", "", modeEnforce, false},
		{"observe empty is enforce", "observe", "   ", modeEnforce, false},
		{"observe unparseable", "observe", "not-a-timestamp", modeEnforce, false},
		{"observe already past", "observe", past, modeEnforce, false},
		{"observe now-equals-until at boot", "observe", now.Format(time.RFC3339), modeEnforce, false}, // !After(now)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, grant := resolveObserveGrant(tid, hookPEPTenant{Enforcement: c.enf, ObserveUntil: c.until, Policy: hookPolicyDoc{Version: "v1"}}, now, discardLog())
			if mode != c.wantMode {
				t.Fatalf("mode = %v, want %v", mode, c.wantMode)
			}
			if c.wantGrant {
				if grant.until.IsZero() || grant.bootMono.IsZero() || grant.window <= 0 || !strings.HasPrefix(grant.id, "obsgrant-") {
					t.Fatalf("a valid grant must carry a future until, a monotonic anchor+window and an id; %+v", grant)
				}
			} else if !grant.until.IsZero() || grant.id != "" || grant.window != 0 {
				t.Fatalf("a deny-closed resolution must carry NO grant; %+v", grant)
			}
		})
	}
}

// observeGrantID distinguishes distinct windows/policies of the same tenant (report grouping).
func TestObserveGrantIDDistinguishesWindows(t *testing.T) {
	tid := newHookLedgerFixture(t, hookPolicyDoc{}).tenant
	a := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	b := a.Add(time.Hour)
	if observeGrantID(tid, a, "v1") == observeGrantID(tid, b, "v1") {
		t.Fatal("different windows must yield different grant ids")
	}
	if observeGrantID(tid, a, "v1") == observeGrantID(tid, a, "v2") {
		t.Fatal("different policy versions must yield different grant ids")
	}
	// Two separate calls, compared through variables: the point is that the function
	// is pure, and writing it as one expression made staticcheck read it as a
	// tautology (SA4000) when it is the property under test.
	firstID, secondID := observeGrantID(tid, a, "v1"), observeGrantID(tid, a, "v1")
	if firstID != secondID {
		t.Fatal("grant id must be deterministic for identical terms")
	}
}

// Provenance axis: each PRODUCER stamps its own shadow_source, orthogonal to shadowed_decision.
func TestShadowSourceByProducer(t *testing.T) {
	t.Run("local_default", func(t *testing.T) {
		f := observeLedgerFixture(t, hookPolicyDoc{Version: "src-def/v1", Default: claude.DecisionDeny})
		_, meta := decideAnchored(t, f, hookLedgerInput(f.tenant, "Read", hookResourceKindFile, "/srv/x", "read"))
		if meta[metaShadowSource] != shadowSourceLocalDefault {
			t.Fatalf("shadow_source = %#v, want %q", meta[metaShadowSource], shadowSourceLocalDefault)
		}
	})
	t.Run("bash_path", func(t *testing.T) {
		// The ledger fixture cannot carry a raw Bash command (rawInput is unexported), so assert the
		// producer directly: bashAskWithClass stamps bash_path on both the fresh-ask and base-ask legs
		// (the deny leg's bash_path source is exercised end-to-end by TestObserveShadowsCleanBashRuleDeny).
		if got := bashAskWithClass(hookDisposition{}, auth.ClassPolicy); got.source != shadowSourceBashPath {
			t.Fatalf("bashAskWithClass (fresh) source = %q, want %q", got.source, shadowSourceBashPath)
		}
		if got := bashAskWithClass(hookDisposition{decision: claude.DecisionAsk}, auth.ClassPolicy); got.source != shadowSourceBashPath {
			t.Fatalf("bashAskWithClass (base ask) source = %q, want %q", got.source, shadowSourceBashPath)
		}
	})
	t.Run("pdp", func(t *testing.T) {
		f := observeLedgerFixture(t, hookPolicyDoc{Version: "src-pdp/v1", Default: claude.DecisionAllow})
		f.dec.eval = classedEval{allow: false, class: auth.ClassPolicy, reason: "pdp business rule"}
		_, meta := decideAnchored(t, f, hookLedgerInput(f.tenant, "Read", hookResourceKindFile, "/srv/x", "read"))
		if meta[metaShadowSource] != shadowSourcePDP {
			t.Fatalf("shadow_source = %#v, want %q", meta[metaShadowSource], shadowSourcePDP)
		}
	})
	t.Run("scoped", func(t *testing.T) {
		f := observeLedgerFixture(t, hookPolicyDoc{Version: "src-scoped/v1", Default: claude.DecisionAllow})
		f.dec.scoped = classedScopedForbid{class: auth.ClassPolicy, reason: "authored scoped forbid"}
		_, meta := decideAnchored(t, f, hookLedgerInput(f.tenant, "mcp__x__y", hookResourceKindMCP, "server/tool", "write"))
		if meta[metaShadowSource] != shadowSourceScoped {
			t.Fatalf("shadow_source = %#v, want %q", meta[metaShadowSource], shadowSourceScoped)
		}
	})
}

// --- E3b: fail-closed downgrade re-anchors the DENY (evidence integrity) ---------------

// faultStore wraps a real store.Store and fails the first failCalls Mutate calls so the ledger
// anchor fails and the fail-closed downgrade + re-anchor path runs. failBeforeInner=true models
// a clean transient (no write happens); false models an AMBIGUOUS COMMIT (the inner write lands
// but Mutate reports failure) to exercise the decision_attempt_id dedup key.
type faultStore struct {
	store.Store
	failCalls       int
	failBeforeInner bool
	calls           int
}

func (fs *faultStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	fs.calls++
	if fs.calls <= fs.failCalls {
		if !fs.failBeforeInner {
			_ = fs.Store.Mutate(ctx, tenant, fn) // the event lands, but we report failure (ambiguous commit)
		}
		return errors.New("injected ledger failure")
	}
	return fs.Store.Mutate(ctx, tenant, fn)
}

func observeShadowInput(f *hookLedgerFixture) claude.HookDecisionInput {
	// base allows; a PDP business forbid is shadowed → a terminal ALLOW that must anchor.
	f.dec.eval = classedEval{allow: false, class: auth.ClassPolicy, reason: "pdp business rule"}
	return hookLedgerInput(f.tenant, "Read", hookResourceKindFile, "/srv/x", "read")
}

// A transient anchor failure on a shadowed ALLOW downgrades to DENY and RE-ANCHORS the deny with
// the shadow preserved + effective_downgrade — so the promotion report still sees the would-be verdict.
func TestObserveDowngradeReanchorsDenyWithShadow(t *testing.T) {
	f := newHookLedgerFixture(t, hookPolicyDoc{Version: "e3b/v1", Default: claude.DecisionAllow})
	setObserveGrant(f, observeTestFarFuture, time.Now)
	in := observeShadowInput(f)
	before := hookLedgerHead(t, f.store, f.tenant)
	f.dec.store = &faultStore{Store: f.store, failCalls: 1, failBeforeInner: true} // 1st anchor (allow) fails, no write

	res, err := f.dec.Decide(context.Background(), in, "test-bearer")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Permission != claude.DecisionDeny || !strings.Contains(res.Reason, "evidence unavailable") {
		t.Fatalf("a shadowed allow that cannot anchor must fail-closed to DENY, got %q (%s)", res.Permission, res.Reason)
	}
	events := canonicalLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1)
	if len(events) != 1 {
		t.Fatalf("re-anchor must write EXACTLY the downgraded deny, got %d events", len(events))
	}
	meta := events[0].meta
	if meta["decision"] != claude.DecisionDeny {
		t.Fatalf("re-anchored event decision = %#v, want deny", meta["decision"])
	}
	if meta[metaEffectiveDowngrade] != true {
		t.Fatalf("re-anchored deny must carry effective_downgrade=true, got %#v", meta[metaEffectiveDowngrade])
	}
	if meta[metaShadowedDecision] != claude.DecisionDeny || meta[metaShadowSource] != shadowSourcePDP {
		t.Fatalf("re-anchored deny must PRESERVE the shadow (deny/pdp), got decision=%#v source=%#v", meta[metaShadowedDecision], meta[metaShadowSource])
	}
	if s, _ := meta[metaDecisionAttemptID].(string); !strings.HasPrefix(s, "attempt-") {
		t.Fatalf("re-anchored deny must carry a decision_attempt_id, got %#v", meta[metaDecisionAttemptID])
	}
}

// If BOTH the anchor and the re-anchor fail, the DENY still stands (deny-closed) and no event is
// written — never an allow, never a panic.
func TestObserveDowngradeDenyStandsWhenReanchorAlsoFails(t *testing.T) {
	f := newHookLedgerFixture(t, hookPolicyDoc{Version: "e3b2/v1", Default: claude.DecisionAllow})
	setObserveGrant(f, observeTestFarFuture, time.Now)
	in := observeShadowInput(f)
	before := hookLedgerHead(t, f.store, f.tenant)
	f.dec.store = &faultStore{Store: f.store, failCalls: 2, failBeforeInner: true} // both anchor attempts fail

	res, err := f.dec.Decide(context.Background(), in, "test-bearer")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("deny must stand unconditionally when the re-anchor also fails, got %q", res.Permission)
	}
	if events := canonicalLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1); len(events) != 0 {
		t.Fatalf("no event should be written when both anchors fail, got %d", len(events))
	}
}

// An AMBIGUOUS COMMIT (the allow write lands but Mutate reports failure) double-writes an allow AND
// a deny — the chain stays valid; the SHARED decision_attempt_id lets a reader detect/dedupe the pair.
func TestObserveDowngradeAmbiguousCommitSharesAttemptID(t *testing.T) {
	f := newHookLedgerFixture(t, hookPolicyDoc{Version: "e3b3/v1", Default: claude.DecisionAllow})
	setObserveGrant(f, observeTestFarFuture, time.Now)
	in := observeShadowInput(f)
	before := hookLedgerHead(t, f.store, f.tenant)
	f.dec.store = &faultStore{Store: f.store, failCalls: 1, failBeforeInner: false} // allow write lands, reports failure

	if _, err := f.dec.Decide(context.Background(), in, "test-bearer"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	events := canonicalLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1)
	if len(events) != 2 {
		t.Fatalf("an ambiguous commit must leave BOTH the allow and the re-anchored deny, got %d", len(events))
	}
	id0, _ := events[0].meta[metaDecisionAttemptID].(string)
	id1, _ := events[1].meta[metaDecisionAttemptID].(string)
	if id0 == "" || id0 != id1 {
		t.Fatalf("the double-written pair must share one decision_attempt_id (dedup key), got %q vs %q", id0, id1)
	}
	if events[0].meta["decision"] != claude.DecisionAllow || events[1].meta["decision"] != claude.DecisionDeny {
		t.Fatalf("the pair must be [allow, deny], got [%v, %v]", events[0].meta["decision"], events[1].meta["decision"])
	}
	if events[1].meta[metaEffectiveDowngrade] != true {
		t.Fatalf("the deny leg must carry effective_downgrade=true, got %#v", events[1].meta[metaEffectiveDowngrade])
	}
}

// --- E3 review follow-ups: grant fingerprint, bash source preservation, canceled-ctx re-anchor ---

// The grant fingerprint distinguishes policies with the SAME version but DIFFERENT rules, so two
// windows never blur in the promotion report (Codex: version alone was insufficient).
func TestHookPolicyFingerprintDistinguishesRules(t *testing.T) {
	a := hookPolicyDoc{Version: "v1", Default: claude.DecisionAllow, Rules: []hookPolicyRule{{Tool: "Bash", Decision: claude.DecisionDeny}}}
	b := hookPolicyDoc{Version: "v1", Default: claude.DecisionAllow, Rules: []hookPolicyRule{{Tool: "Write", Decision: claude.DecisionDeny}}}
	if hookPolicyFingerprint(a) == hookPolicyFingerprint(b) {
		t.Fatal("same version but DIFFERENT rules must fingerprint differently")
	}
	// As above: two calls compared through variables, so the determinism property
	// stays under test instead of reading as SA4000's identical-operands tautology.
	firstFP, secondFP := hookPolicyFingerprint(a), hookPolicyFingerprint(a)
	if firstFP != secondFP {
		t.Fatal("fingerprint must be deterministic")
	}
	tid := newHookLedgerFixture(t, hookPolicyDoc{}).tenant
	until := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	if observeGrantID(tid, until, hookPolicyFingerprint(a)) == observeGrantID(tid, until, hookPolicyFingerprint(b)) {
		t.Fatal("grant ids for rule-different policies (same window) must differ")
	}
}

// bashAskWithClass PRESERVES a base ask's provenance source (a redundant path-rule match must not
// relabel a local_rule ask as bash_path); it only stamps bash_path when it INTRODUCES the ask.
func TestBashAskPreservesBaseSource(t *testing.T) {
	if got := bashAskWithClass(hookDisposition{decision: claude.DecisionAsk, source: shadowSourceLocalRule}, auth.ClassPolicy); got.source != shadowSourceLocalRule {
		t.Fatalf("a base-ask's source must be preserved, got %q want %q", got.source, shadowSourceLocalRule)
	}
	if got := bashAskWithClass(hookDisposition{decision: claude.DecisionAsk, source: shadowSourceLocalDefault}, auth.ClassPolicy); got.source != shadowSourceLocalDefault {
		t.Fatalf("a base local_default ask source must be preserved, got %q", got.source)
	}
	if got := bashAskWithClass(hookDisposition{decision: claude.DecisionAsk}, auth.ClassPolicy); got.source != shadowSourceBashPath {
		t.Fatalf("a base-ask with NO source must take bash_path (the scan is the origin), got %q", got.source)
	}
	if got := bashAskWithClass(hookDisposition{}, auth.ClassPolicy); got.source != shadowSourceBashPath {
		t.Fatalf("a bash-introduced ask must be bash_path, got %q", got.source)
	}
}

// The downgrade re-anchor runs on a DECOUPLED context, so even an already-canceled caller ctx
// still records the downgraded deny (WithoutCancel) — no bare allow that misreads as proceeded.
func TestObserveDowngradeReanchorsUnderCanceledCtx(t *testing.T) {
	f := newHookLedgerFixture(t, hookPolicyDoc{Version: "e3c/v1", Default: claude.DecisionAllow})
	setObserveGrant(f, observeTestFarFuture, time.Now)
	in := observeShadowInput(f)
	before := hookLedgerHead(t, f.store, f.tenant)
	f.dec.store = &faultStore{Store: f.store, failCalls: 1, failBeforeInner: true} // 1st anchor fails; re-anchor must still land
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the caller context is ALREADY canceled
	res, err := f.dec.Decide(ctx, in, "test-bearer")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("a shadowed allow that cannot anchor must fail-closed to DENY, got %q", res.Permission)
	}
	events := canonicalLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1)
	if len(events) != 1 || events[0].meta[metaEffectiveDowngrade] != true {
		t.Fatalf("the re-anchor must record the downgraded deny even under a canceled ctx, got %d events", len(events))
	}
}
