// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// claudehookpep_f9_test.go — Stage 1 (q1-MCP): the hook-PEP anchor must follow the
// F9 anchoring discipline (sdk/evidence.go). The named historical bug: anchor()
// returned store.ErrAuditSpoolFull from INSIDE the store transaction on a degrade-mode
// drop (ev.Seq == 0), rolling back the durable loss accounting (audit_spool_gaps) the
// store had just committed — the gap counter never advanced and the signed audit.gap
// marker never sealed, while the decision layer still (correctly) denied. These tests
// pin the accounting: "commit the gap, THEN refuse".

// hookSpoolFixture is a hookLedgerFixture whose store runs with an ENGAGED audit-spool
// budget (block or degrade). The tenant is provisioned on a file-backed signed store
// with no budget, which is then closed and reopened with a 1-byte budget, mirroring the
// core/internal/store/sqlstore/audit_gap_test.go technique from the composition root.
type hookSpoolFixture struct {
	*hookLedgerFixture
	dsn    string
	signer *audit.Signer
}

func openHookSpoolStore(t *testing.T, dsn string, signer *audit.Signer, budget int64, mode store.AuditSpoolMode) store.Store {
	t.Helper()
	st, err := coreengine.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dsn, SignEvent: signer.SignEvent,
		AuditSpoolMaxBytes: budget, AuditSpoolOnFull: mode,
	}, nil)
	if err != nil {
		t.Fatalf("open spool store (budget=%d mode=%s): %v", budget, mode, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newHookSpoolFixture(t *testing.T, policy hookPolicyDoc, mode store.AuditSpoolMode) *hookSpoolFixture {
	t.Helper()
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate audit signing key: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("build audit signer: %v", err)
	}
	dsn := filepath.Join(t.TempDir(), "hookpep-f9.db")

	// Phase 1: provision the tenant with NO budget so provisioning writes are unaffected.
	seed, err := coreengine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, SignEvent: signer.SignEvent,
	}, nil)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	var tenant model.TenantID
	if err := seed.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{
			Name: "hook-f9", Slug: "hook-f9", Status: model.StatusActive,
		})
		if err == nil {
			tenant = org.TenantID
		}
		return err
	}); err != nil {
		t.Fatalf("provision hook spool tenant: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	// Phase 2: reopen with a 1-byte budget — every non-exempt governed append is now
	// over budget (refused in block mode, dropped-and-accounted in degrade mode).
	st := openHookSpoolStore(t, dsn, signer, 1, mode)

	principal := auth.ScopedPrincipal(model.NewID(), "hook-f9-agent", tenant, auth.RoleEditor)
	dec := &claudeHookDecider{
		tenants: map[model.TenantID]resolvedTenant{
			tenant: {tenant: tenant, policy: policy},
		},
		authr: hookLedgerAuthenticator{principal: principal},
		store: st,
		log:   discardLog(),
	}
	return &hookSpoolFixture{
		hookLedgerFixture: &hookLedgerFixture{store: st, tenant: tenant, pub: pub, dec: dec},
		dsn:               dsn,
		signer:            signer,
	}
}

// hookPendingDrops reads the durable degrade-mode loss counter (audit_spool_gaps, surfaced
// as AuditSpoolStatus.PendingDrops). This is the accounting the rollback bug lost.
func hookPendingDrops(t *testing.T, st store.Store) int64 {
	t.Helper()
	// The local is named after the interface it holds. misspell reads "statuser"
	// as a misspelling of "stature"; it is our own agent noun, and note the linter
	// does NOT flag the exported AuditSpoolStatuser one line down — renaming the
	// local to satisfy it would only break the tie to the name it mirrors.
	//nolint:misspell // agent noun of store.AuditSpoolStatuser, not "stature".
	statuser, ok := st.(store.AuditSpoolStatuser)
	if !ok {
		t.Fatal("store does not expose AuditSpoolStatuser")
	}
	//nolint:misspell // same local as above.
	status, configured, err := statuser.AuditSpoolStatus(context.Background())
	if err != nil {
		t.Fatalf("audit spool status: %v", err)
	}
	if !configured {
		t.Fatal("audit spool budget is not configured on this store")
	}
	return status.PendingDrops
}

// TestHookPEPF9DegradeAllowDowngradesAndCommitsGapAccounting is the RED proof of the
// stage-1 defect: in degrade mode a policy ALLOW must (a) downgrade to DENY
// (evidence-unavailable, unchanged deny-closed semantics) AND (b) advance the durable
// drop counter by EXACTLY 2 — one drop for the allow anchor, one for the downgraded-deny
// re-anchor. With the rollback bug the counter never advances (the sentinel returned from
// inside Mutate rolls the loss accounting back).
func TestHookPEPF9DegradeAllowDowngradesAndCommitsGapAccounting(t *testing.T) {
	f := newHookSpoolFixture(t, hookPolicyDoc{
		Version: "hook-policy/f9-degrade-allow", Default: claude.DecisionAllow,
	}, store.AuditSpoolDegrade)
	base := hookPendingDrops(t, f.store)
	before := hookLedgerHead(t, f.store, f.tenant)

	in := hookLedgerInput(f.tenant, "Read", hookResourceKindFile, "/srv/acme/README.md", "read")
	res, err := f.dec.Decide(context.Background(), in, "test-bearer")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Permission != claude.DecisionDeny || !strings.Contains(res.Reason, "evidence unavailable") {
		t.Fatalf("degrade-mode ALLOW = %q (%s), want evidence-unavailable DENY", res.Permission, res.Reason)
	}
	if got := hookPendingDrops(t, f.store) - base; got != 2 {
		t.Fatalf("durable degrade drops advanced by %d, want exactly 2 (allow anchor + downgraded-deny re-anchor); the in-transaction sentinel rolls back the loss accounting", got)
	}
	// Both appends were dropped: the chain itself must NOT have grown.
	if events := hookLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1); len(events) != 0 {
		t.Fatalf("degrade drop appended %d chain events, want 0", len(events))
	}
}

// TestHookPEPF9DegradeDenyPreservesReasonAndCommitsGapAccounting: a policy DENY under
// degrade keeps its policy reason (never replaced by the evidence-unavailable downgrade)
// and advances the durable drop counter by exactly 1 (the single deny anchor).
func TestHookPEPF9DegradeDenyPreservesReasonAndCommitsGapAccounting(t *testing.T) {
	f := newHookSpoolFixture(t, hookPolicyDoc{
		Version: "hook-policy/f9-degrade-deny", Default: claude.DecisionDeny,
	}, store.AuditSpoolDegrade)
	base := hookPendingDrops(t, f.store)

	in := hookLedgerInput(f.tenant, "Bash", hookResourceKindShell, "bash", "write")
	res, err := f.dec.Decide(context.Background(), in, "test-bearer")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("degrade-mode DENY = %q (%s), want DENY", res.Permission, res.Reason)
	}
	if strings.Contains(res.Reason, "evidence unavailable") {
		t.Fatalf("degrade-mode DENY replaced the policy reason with the downgrade reason: %q", res.Reason)
	}
	if !strings.Contains(res.Reason, "deny-closed default") {
		t.Fatalf("degrade-mode DENY lost the policy reason: %q", res.Reason)
	}
	if got := hookPendingDrops(t, f.store) - base; got != 1 {
		t.Fatalf("durable degrade drops advanced by %d, want exactly 1 (the deny anchor)", got)
	}
}

// TestHookPEPF9DegradeGapSealsSignedMarkerAfterRecovery drives the committed loss
// accounting through to the product promise: after the degrade episode, raising the
// budget and appending one governed event seals a SIGNED in-chain audit.gap marker with
// the EXACT dropped count, and the whole chain (including the declared hole) verifies —
// hash chain and Ed25519 event signatures. With the rollback bug there is no pending gap
// to seal, so no marker ever appears.
func TestHookPEPF9DegradeGapSealsSignedMarkerAfterRecovery(t *testing.T) {
	ctx := context.Background()
	f := newHookSpoolFixture(t, hookPolicyDoc{
		Version: "hook-policy/f9-degrade-seal", Default: claude.DecisionDeny,
	}, store.AuditSpoolDegrade)
	base := hookPendingDrops(t, f.store)

	in := hookLedgerInput(f.tenant, "Bash", hookResourceKindShell, "bash", "write")
	res, err := f.dec.Decide(ctx, in, "test-bearer")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Permission != claude.DecisionDeny {
		t.Fatalf("degrade-mode DENY = %q (%s), want DENY", res.Permission, res.Reason)
	}
	const drops = int64(1)
	if got := hookPendingDrops(t, f.store) - base; got != drops {
		t.Fatalf("durable degrade drops advanced by %d, want %d", got, drops)
	}

	// Recover: reopen the SAME ledger with headroom; the next unsigned governed append
	// seals the pending gap as a signed in-chain marker in front of itself.
	if err := f.store.Close(); err != nil {
		t.Fatalf("close degraded store: %v", err)
	}
	recovered := openHookSpoolStore(t, f.dsn, f.signer, 1<<30, store.AuditSpoolDegrade)
	if err := recovered.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		ev, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: "user:recover", ActorKind: model.ActorUser, Action: "agent.update",
			Meta: map[string]any{"resumed": true},
		})
		if err != nil {
			return err
		}
		if ev.Seq == 0 {
			return fmt.Errorf("append still dropped after budget raise")
		}
		return nil
	}); err != nil {
		t.Fatalf("sealing append after budget raise: %v", err)
	}

	events := canonicalLedgerEventsFrom(t, recovered, f.tenant, 0)
	var marker *canonicalLedgerEvent
	for i := range events {
		if events[i].event.Action == store.ActionAuditGap {
			if marker != nil {
				t.Fatalf("multiple audit.gap markers sealed for one episode")
			}
			marker = &events[i]
		}
	}
	if marker == nil {
		t.Fatalf("no signed audit.gap marker sealed after recovery; the degrade episode is invisible in the chain (loss accounting was rolled back)")
	}
	if got := marker.meta[store.GapMetaCount]; got != float64(drops+base) {
		t.Fatalf("audit.gap marker count = %#v, want %d", got, drops+base)
	}
	if got := marker.meta[store.GapMetaReason]; got != store.GapReasonSpoolFull {
		t.Fatalf("audit.gap marker reason = %#v, want %q", got, store.GapReasonSpoolFull)
	}
	if len(marker.event.Sig) != ed25519.SignatureSize {
		t.Fatalf("audit.gap marker signature length = %d, want %d", len(marker.event.Sig), ed25519.SignatureSize)
	}
	if got := hookPendingDrops(t, recovered); got != 0 {
		t.Fatalf("pending drops after seal = %d, want 0 (the marker consumes the accounting)", got)
	}
	verifyHookLedger(t, &hookLedgerFixture{store: recovered, tenant: f.tenant, pub: f.pub, dec: f.dec})
}

// TestHookPEPF9BlockModeSpoolFullDowngradesAllowWithoutDegradeDrop: in BLOCK mode the store
// itself refuses the append with store.ErrAuditSpoolFull (transaction rolled back, nothing
// durable). The ALLOW must downgrade to DENY, and NO degrade drop may be counted — block
// mode never trades evidence for availability.
func TestHookPEPF9BlockModeSpoolFullDowngradesAllowWithoutDegradeDrop(t *testing.T) {
	f := newHookSpoolFixture(t, hookPolicyDoc{
		Version: "hook-policy/f9-block-allow", Default: claude.DecisionAllow,
	}, store.AuditSpoolBlock)
	base := hookPendingDrops(t, f.store)
	before := hookLedgerHead(t, f.store, f.tenant)

	in := hookLedgerInput(f.tenant, "Read", hookResourceKindFile, "/srv/acme/README.md", "read")
	res, err := f.dec.Decide(context.Background(), in, "test-bearer")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Permission != claude.DecisionDeny || !strings.Contains(res.Reason, "evidence unavailable") {
		t.Fatalf("block-mode ALLOW = %q (%s), want evidence-unavailable DENY", res.Permission, res.Reason)
	}
	if got := hookPendingDrops(t, f.store) - base; got != 0 {
		t.Fatalf("block-mode spool-full counted %d degrade drops, want 0", got)
	}
	if events := hookLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1); len(events) != 0 {
		t.Fatalf("block-mode refusal appended %d chain events, want 0", len(events))
	}
}

// notLeaderHookStore simulates a standby node: every write transaction is refused with
// store.ErrNotLeader before the callback runs. Reads pass through.
type notLeaderHookStore struct{ store.Store }

func (s notLeaderHookStore) Mutate(context.Context, model.TenantID, func(store.Scope) error) error {
	return store.ErrNotLeader
}

// TestHookPEPF9NotLeaderDowngradesAllow: an un-anchorable ALLOW on a non-leader node is a
// DENY (evidence-unavailable downgrade), never a fail-open allow.
func TestHookPEPF9NotLeaderDowngradesAllow(t *testing.T) {
	f := newHookLedgerFixture(t, hookPolicyDoc{
		Version: "hook-policy/f9-notleader", Default: claude.DecisionAllow,
	})
	f.dec.store = notLeaderHookStore{Store: f.store}

	in := hookLedgerInput(f.tenant, "Read", hookResourceKindFile, "/srv/acme/README.md", "read")
	res, err := f.dec.Decide(context.Background(), in, "test-bearer")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Permission != claude.DecisionDeny || !strings.Contains(res.Reason, "evidence unavailable") {
		t.Fatalf("not-leader ALLOW = %q (%s), want evidence-unavailable DENY", res.Permission, res.Reason)
	}
}

// TestHookPEPF9ClassifyStoreFault pins the store-error → evidence-fault taxonomy mapping
// (mirrors modules/capabilities.classifyStoreFault).
func TestHookPEPF9ClassifyStoreFault(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want sdk.EvidenceFault
	}{
		{"nil", nil, sdk.EvidenceFaultNone},
		{"spool full", store.ErrAuditSpoolFull, sdk.EvidenceFaultSpoolFull},
		{"wrapped spool full", fmt.Errorf("mutate: %w", store.ErrAuditSpoolFull), sdk.EvidenceFaultSpoolFull},
		{"not leader", store.ErrNotLeader, sdk.EvidenceFaultLedgerUnavailable},
		{"wrapped not leader", fmt.Errorf("mutate: %w", store.ErrNotLeader), sdk.EvidenceFaultLedgerUnavailable},
		{"other", fmt.Errorf("disk on fire"), sdk.EvidenceFaultWriteError},
	}
	for _, tc := range cases {
		if got := classifyHookStoreFault(tc.err); got != tc.want {
			t.Errorf("classifyHookStoreFault(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestHookPEPF9EvidenceBindingPhaseDifferentiation: the allow attempt and its compensating
// downgrade-deny must carry DIFFERENT OperationIDs (phase-differentiated derivation), so
// the pair never reads as a same-OperationID-different-digest replay under the
// sdk/evidence.go rebind rule; every binding must be Valid.
func TestHookPEPF9EvidenceBindingPhaseDifferentiation(t *testing.T) {
	tenant := model.TenantID("t-f9-binding")
	attempt := "attempt-fixed"
	ph := hookDecisionHash("PreToolUse", "Read", hookResourceKindFile, "/srv/x", "read", "plan", claude.DecisionAllow, "actor", "v1")
	dph := hookDecisionHash("PreToolUse", "Read", hookResourceKindFile, "/srv/x", "read", "plan", claude.DecisionDeny, "actor", "v1")
	rd := hookEffectiveResultDigest(claude.HookDecisionResult{Permission: claude.DecisionAllow})
	drd := hookEffectiveResultDigest(claude.HookDecisionResult{Permission: claude.DecisionDeny, Reason: "evidence unavailable (deny-closed)"})

	allow := hookEvidenceBinding(tenant, attempt, hookPhaseTerminalAllow, ph, rd)
	downgrade := hookEvidenceBinding(tenant, attempt, hookPhaseDowngradedDeny, dph, drd)
	for _, b := range []sdk.EvidenceBinding{allow, downgrade} {
		if !b.Valid() {
			t.Fatalf("binding %+v is not Valid", b)
		}
	}
	if allow.OperationID == downgrade.OperationID {
		t.Fatalf("allow attempt and downgrade-deny share an OperationID; the compensating record would read as a replay/rebind")
	}
	if allow.EffectDigest == downgrade.EffectDigest {
		t.Fatalf("allow attempt and downgrade-deny share an EffectDigest despite differing phase+decision")
	}
	// Determinism: the same inputs re-derive the same binding (idempotent retry identity).
	if again := hookEvidenceBinding(tenant, attempt, hookPhaseTerminalAllow, ph, rd); again != allow {
		t.Fatalf("binding derivation is not deterministic: %+v vs %+v", again, allow)
	}
}

// TestHookPEPF9EffectDigestBindsEffectiveResult pins the derivation-level property the
// Codex review found missing: under a FIXED {tenant, attempt, phase, payloadHash}, every
// field that changes the rendered hook wire form must change the EffectDigest while the
// OperationID stays EQUAL (same operation, different effect ⇒ the sdk/evidence.go replay/
// rebind rule can bite). Also: same phase + different payload ⇒ different digest (the
// weakness the review called out in the original phase test, which varied phase and
// decision together).
func TestHookPEPF9EffectDigestBindsEffectiveResult(t *testing.T) {
	tenant := model.TenantID("t-f9-effect")
	attempt := "attempt-fixed"
	phase := hookPhaseTerminalAllow
	ph := hookDecisionHash("PreToolUse", "Read", hookResourceKindFile, "/srv/x", "read", "plan", claude.DecisionAllow, "actor", "v1")
	base := claude.HookDecisionResult{
		Permission: claude.DecisionAllow, Reason: "permitted", PolicyVersion: "v1",
		PrincipalActor: "actor", IdentityTier: "firm",
	}
	bind := func(res claude.HookDecisionResult) sdk.EvidenceBinding {
		return hookEvidenceBinding(tenant, attempt, phase, ph, hookEffectiveResultDigest(res))
	}
	baseBinding := bind(base)

	with := func(mut func(*claude.HookDecisionResult)) claude.HookDecisionResult {
		v := base
		mut(&v)
		return v
	}
	variants := map[string]claude.HookDecisionResult{
		"different rewrite A": with(func(r *claude.HookDecisionResult) {
			r.UpdatedInput = map[string]any{"file_path": "/governed/a.md"}
		}),
		"different rewrite B": with(func(r *claude.HookDecisionResult) {
			r.UpdatedInput = map[string]any{"file_path": "/governed/b.md"}
		}),
		"empty-but-present rewrite": with(func(r *claude.HookDecisionResult) {
			r.UpdatedInput = map[string]any{}
		}),
		"block": with(func(r *claude.HookDecisionResult) {
			r.Block = true
		}),
		"block softened": with(func(r *claude.HookDecisionResult) {
			r.Block = true
			r.ContinueOnBlock = true
		}),
		"additional context": with(func(r *claude.HookDecisionResult) {
			r.AdditionalContext = "governed feedback"
		}),
		"different reason": with(func(r *claude.HookDecisionResult) {
			r.Reason = "permitted with governed input rewrite"
		}),
	}
	seen := map[sdk.EffectDigest]string{baseBinding.EffectDigest: "base (pass-through)"}
	for name, res := range variants {
		b := bind(res)
		if b.OperationID != baseBinding.OperationID {
			t.Errorf("%s: OperationID changed with the result; it must be fixed by {tenant, attempt, phase}", name)
		}
		if prev, dup := seen[b.EffectDigest]; dup {
			t.Errorf("%s: EffectDigest collides with %q; the digest does not bind the effective result", name, prev)
		}
		seen[b.EffectDigest] = name
	}
	// (c) Same phase, same result, DIFFERENT payload (input) ⇒ different EffectDigest.
	otherPH := hookDecisionHash("PreToolUse", "Read", hookResourceKindFile, "/srv/OTHER", "read", "plan", claude.DecisionAllow, "actor", "v1")
	if got := hookEvidenceBinding(tenant, attempt, phase, otherPH, hookEffectiveResultDigest(base)); got.EffectDigest == baseBinding.EffectDigest {
		t.Errorf("same-phase different-payload bindings share an EffectDigest; the digest dropped the payload commitment")
	}
}

// --- Codex review MUST-FIX: the EffectDigest must bind the EFFECTIVE result (UpdatedInput,
// Block, ContinueOnBlock, AdditionalContext, Reason — the fields that change the rendered
// hook wire form, connectors/claude/pep.go renderHookDecision/postToolUseJSON), and the
// anchored event's CANONICAL meta must commit the binding (operation_id / phase /
// effect_digest), mirroring modules/capabilities/toolpins.go:187-188 — else the evidence
// ref does not come from an event committed for that exact binding (sdk/evidence.go:186).

// singleAnchoredHookMeta runs one Decide and returns the decision result plus the single
// anchored event's canonical meta.
func singleAnchoredHookMeta(t *testing.T, f *hookLedgerFixture, in claude.HookDecisionInput, wantDecision string) (claude.HookDecisionResult, map[string]any) {
	t.Helper()
	before := hookLedgerHead(t, f.store, f.tenant)
	res, err := f.dec.Decide(context.Background(), in, "test-bearer")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.Permission != wantDecision {
		t.Fatalf("permission = %q (%s), want %q", res.Permission, res.Reason, wantDecision)
	}
	events := canonicalLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1)
	if len(events) != 1 {
		t.Fatalf("new hook ledger events = %d, want exactly 1", len(events))
	}
	return res, events[0].meta
}

// requireHookMetaString returns meta[key] as a non-empty string or fails.
func requireHookMetaString(t *testing.T, meta map[string]any, key string) string {
	t.Helper()
	v, ok := meta[key].(string)
	if !ok || strings.TrimSpace(v) == "" {
		t.Fatalf("anchored hook meta %q = %#v, want a non-empty string (the event does not commit its evidence binding)", key, meta[key])
	}
	return v
}

// TestHookPEPF9AnchoredMetaCommitsEvidenceBinding: the anchored decision event's CANONICAL
// meta (hashed into the chain via MetaDigest and Ed25519-signed) must carry the evidence
// binding that was classified — operation_id, phase, effect_digest — so the event hash
// actually commits the binding.
func TestHookPEPF9AnchoredMetaCommitsEvidenceBinding(t *testing.T) {
	f := newHookLedgerFixture(t, hookPolicyDoc{
		Version: "hook-policy/f9-binding-meta",
		Default: claude.DecisionDeny,
		Rules: []hookPolicyRule{{
			Tool: "Read", Decision: claude.DecisionAllow,
			Rewrite: map[string]any{"file_path": "/governed/readme.md"},
		}},
	})
	in := hookLedgerInput(f.tenant, "Read", hookResourceKindFile, "/srv/acme/README.md", "read")
	res, meta := singleAnchoredHookMeta(t, f, in, claude.DecisionAllow)
	if len(res.UpdatedInput) == 0 {
		t.Fatalf("governed rewrite did not ride the allow (UpdatedInput empty); the test would be vacuous")
	}

	gotOp := requireHookMetaString(t, meta, "operation_id")
	gotDigest := requireHookMetaString(t, meta, "effect_digest")
	if gotPhase := requireHookMetaString(t, meta, "phase"); gotPhase != "terminal-allow" {
		t.Fatalf("anchored phase = %q, want %q", gotPhase, "terminal-allow")
	}

	// The committed meta must match the binding CLASSIFIED for the exact result returned:
	// recompute it from the anchored attempt id + the returned result and compare.
	attempt := requireHookMetaString(t, meta, metaDecisionAttemptID)
	ph := hookDecisionHash(in.Event, in.Tool, in.ResourceKind, in.ResourceRef, in.Mode, in.PlanHash,
		claude.DecisionAllow, res.PrincipalActor, res.PolicyVersion)
	want := hookEvidenceBinding(f.tenant, attempt, hookPhaseTerminalAllow, ph, hookEffectiveResultDigest(res))
	if gotOp != string(want.OperationID) {
		t.Fatalf("anchored operation_id = %q, want the classified binding's %q", gotOp, want.OperationID)
	}
	if gotDigest != string(want.EffectDigest) {
		t.Fatalf("anchored effect_digest = %q, want the classified binding's %q", gotDigest, want.EffectDigest)
	}
	verifyHookLedger(t, f)
}

// TestHookPEPF9EffectDigestVariesWithGovernedRewrite: same tenant, principal, input,
// policy VERSION and phase — two DIFFERENT governed rewrites must anchor DIFFERENT
// effect_digests (policy version cannot substitute for policy content; the observe-mode
// tests explicitly permit different rules under one version string).
func TestHookPEPF9EffectDigestVariesWithGovernedRewrite(t *testing.T) {
	const version = "hook-policy/f9-rewrite-content"
	ruleWith := func(rewrite map[string]any) hookPolicyDoc {
		return hookPolicyDoc{
			Version: version, Default: claude.DecisionDeny,
			Rules: []hookPolicyRule{{Tool: "Read", Decision: claude.DecisionAllow, Rewrite: rewrite}},
		}
	}
	f := newHookLedgerFixture(t, ruleWith(map[string]any{"file_path": "/governed/a.md"}))
	in := hookLedgerInput(f.tenant, "Read", hookResourceKindFile, "/srv/acme/README.md", "read")
	resA, metaA := singleAnchoredHookMeta(t, f, in, claude.DecisionAllow)

	// Same version string, DIFFERENT governed rewrite content; same fixture, so tenant,
	// principal (actor), input and policy version are all held constant.
	f.dec.tenants[f.tenant] = resolvedTenant{tenant: f.tenant, policy: ruleWith(map[string]any{"file_path": "/governed/b.md"})}
	resB, metaB := singleAnchoredHookMeta(t, f, in, claude.DecisionAllow)

	if fmt.Sprint(resA.UpdatedInput) == fmt.Sprint(resB.UpdatedInput) {
		t.Fatalf("test harness error: both decisions produced the same rewrite %v", resA.UpdatedInput)
	}
	digestA := requireHookMetaString(t, metaA, "effect_digest")
	digestB := requireHookMetaString(t, metaB, "effect_digest")
	if digestA == digestB {
		t.Fatalf("two different governed rewrites anchored the SAME effect_digest %q; the binding does not cover the effective result", digestA)
	}
}

// TestHookPEPF9EffectDigestVariesWithBlockFlag: same constants, block vs pass-through —
// the Block flag changes the rendered wire form (decision:"block" on PostToolUse), so the
// anchored effect_digest must differ.
func TestHookPEPF9EffectDigestVariesWithBlockFlag(t *testing.T) {
	const version = "hook-policy/f9-block-content"
	ruleWith := func(block bool) hookPolicyDoc {
		return hookPolicyDoc{
			Version: version, Default: claude.DecisionDeny,
			Rules: []hookPolicyRule{{Tool: "Read", Decision: claude.DecisionAllow, Block: block}},
		}
	}
	f := newHookLedgerFixture(t, ruleWith(false))
	in := hookLedgerInput(f.tenant, "Read", hookResourceKindFile, "/srv/acme/README.md", "read")
	resA, metaA := singleAnchoredHookMeta(t, f, in, claude.DecisionAllow)

	f.dec.tenants[f.tenant] = resolvedTenant{tenant: f.tenant, policy: ruleWith(true)}
	resB, metaB := singleAnchoredHookMeta(t, f, in, claude.DecisionAllow)

	if resA.Block == resB.Block {
		t.Fatalf("test harness error: both decisions carried Block=%v", resA.Block)
	}
	digestA := requireHookMetaString(t, metaA, "effect_digest")
	digestB := requireHookMetaString(t, metaB, "effect_digest")
	if digestA == digestB {
		t.Fatalf("block vs pass-through anchored the SAME effect_digest %q; the binding does not cover the effective result", digestA)
	}
}
