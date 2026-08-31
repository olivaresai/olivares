// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/codex/session"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// The Codex governed decider is tested against a REAL signed ledger on an in-memory
// SQLite store, not against a mock of one. R-01 is a claim about what the store actually
// does with a redelivered claim, so a fake that returns whatever we told it to would prove
// nothing.

type codexFixture struct {
	store  store.Store
	tenant model.TenantID
	dec    *codexHookDecider
	res    *recordingResolver
}

// recordingResolver stands in for modules/sessions. It mints a stable sid per
// (provider, external id) pair — which is the SG-00 guarantee this file relies on and
// which identity_test.go proves against the real index.
type recordingResolver struct {
	calls  int
	minted map[string]string
	err    error
}

func (r *recordingResolver) ResolveSession(_ context.Context, _ model.TenantID, b sessions.SessionBinding) (string, error) {
	r.calls++
	if r.err != nil {
		return "", r.err
	}
	if r.minted == nil {
		r.minted = map[string]string{}
	}
	key := strings.ToLower(strings.TrimSpace(b.Provider)) + ":" + strings.TrimSpace(b.ExternalID)
	if sid, ok := r.minted[key]; ok {
		return sid, nil
	}
	sid := "osn_" + model.NewID().String()
	r.minted[key] = sid
	return sid, nil
}

type codexAuthenticator struct{ principal auth.Principal }

func (a codexAuthenticator) Authenticate(context.Context, string) (auth.Principal, error) {
	return a.principal, nil
}

func newCodexFixture(t *testing.T) *codexFixture {
	t.Helper()
	ctx := context.Background()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate audit signing key: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("build audit signer: %v", err)
	}
	st, err := coreengine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", SignEvent: signer.SignEvent,
	}, nil)
	if err != nil {
		t.Fatalf("open signed store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "codex-hook", Slug: "codex-hook", Status: model.StatusActive})
		if err == nil {
			tenant = org.TenantID
		}
		return err
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}

	res := &recordingResolver{}
	dec := &codexHookDecider{
		tenant:   tenant,
		authr:    codexAuthenticator{principal: auth.ScopedPrincipal(model.NewID(), "codex-agent", tenant, auth.RoleEditor)},
		sessions: res,
		store:    st,
		clock:    func() time.Time { return time.Date(2026, 8, 3, 1, 15, 58, 0, time.UTC) },
		log:      discardLog(),
	}
	return &codexFixture{store: st, tenant: tenant, dec: dec, res: res}
}

func codexReq(event, external, toolUseID string) session.Request {
	return session.Request{
		Event:             event,
		ExternalSessionID: external,
		Tool:              "Bash",
		ToolUseID:         toolUseID,
		ResourceKind:      "shell",
		ResourceRef:       "echo hello",
		Mode:              "write",
		At:                time.Date(2026, 8, 3, 1, 15, 58, 0, time.UTC),
	}
}

// ledgerCount counts audit events whose Action begins with the given prefix.
func ledgerCount(t *testing.T, st store.Store, tenant model.TenantID, prefix string) int {
	t.Helper()
	n := 0
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(ev model.AuditEvent) error {
			if strings.HasPrefix(ev.Action, prefix) {
				n++
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	return n
}

// TestR01OneFactIsOneLedgerEntry is the idempotency PROOF the brief demands: if the
// adapter and the decider could both anchor the same fact, there is no design.
//
// The guarantee is structural, not hopeful. The operation id is derived deterministically
// from (tenant, sid, event, tool_use_id, phase), so a redelivered hook call recomputes the
// SAME id and the store's UNIQUE(tenant_id, operation_id) claim recognizes an exact replay
// and appends NOTHING.
func TestR01OneFactIsOneLedgerEntry(t *testing.T) {
	f := newCodexFixture(t)
	req := codexReq(session.EventPreToolUse, "019fc4c3-40c5-7371-9c92-7b269d23897b", "exec-5e34277c")

	first, err := f.dec.Decide(context.Background(), req, "bearer")
	if err != nil {
		t.Fatalf("first decision: %v", err)
	}
	after1 := ledgerCount(t, f.store, f.tenant, "codex.hook.")
	if after1 == 0 {
		t.Fatal("the first governed decision must leave a ledger entry")
	}

	// Deliver the IDENTICAL hook call again — a retry, a duplicated delivery, a client
	// that did not see the first response.
	second, err := f.dec.Decide(context.Background(), req, "bearer")
	if err != nil {
		t.Fatalf("second decision: %v", err)
	}
	after2 := ledgerCount(t, f.store, f.tenant, "codex.hook.")

	if after2 != after1 {
		t.Errorf("R-01 VIOLATED: a redelivered hook call wrote %d ledger entries, expected the count to stay at %d", after2-after1, after1)
	}
	if first.SessionSID != second.SessionSID {
		t.Errorf("the same Codex session must resolve to the same sid: %q vs %q", first.SessionSID, second.SessionSID)
	}
	if first.Verdict != second.Verdict {
		t.Errorf("a replay must not change the verdict: %q vs %q", first.Verdict, second.Verdict)
	}
}

// TestDistinctCallsAreDistinctEntries is the other half of the same guarantee: idempotency
// must not become blindness. Two DIFFERENT tool calls in one session are two facts.
func TestDistinctCallsAreDistinctEntries(t *testing.T) {
	f := newCodexFixture(t)
	base := ledgerCount(t, f.store, f.tenant, "codex.hook.")
	for _, id := range []string{"exec-aaa", "exec-bbb", "exec-ccc"} {
		if _, err := f.dec.Decide(context.Background(), codexReq(session.EventPreToolUse, "sess-1", id), "bearer"); err != nil {
			t.Fatalf("decide %s: %v", id, err)
		}
	}
	if got := ledgerCount(t, f.store, f.tenant, "codex.hook.") - base; got != 3 {
		t.Errorf("three distinct tool calls must leave three entries, got %d", got)
	}
}

// TestLedgerActionIsCodexNotClaude pins the provenance separation. Reusing Claude's
// constants would have written Codex decisions under "hook.tool.*" with a TargetKind of
// "claude.tool.use" — indistinguishable from Claude's in every evidence query. That is the
// single strongest reason this decider exists as its own type.
func TestLedgerActionIsCodexNotClaude(t *testing.T) {
	f := newCodexFixture(t)
	if _, err := f.dec.Decide(context.Background(), codexReq(session.EventPreToolUse, "sess-p", "exec-p"), "bearer"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if n := ledgerCount(t, f.store, f.tenant, "codex.hook."); n == 0 {
		t.Error("a Codex decision must be recorded under a codex.hook.* action")
	}
	if n := ledgerCount(t, f.store, f.tenant, "hook.tool."); n != 0 {
		t.Errorf("a Codex decision must NOT be recorded under Claude's hook.tool.* action, found %d", n)
	}
	if codexHookCapability == hookActionCapability {
		t.Error("the Codex capability must differ from Claude's, or the two engines' decisions are indistinguishable")
	}
}

// TestIdentityFailureDeniesClosed: without a canonical sid the call cannot be attributed,
// recorded or later audited. Proceeding would produce exactly the ungoverned action this
// plane exists to prevent.
func TestIdentityFailureDeniesClosed(t *testing.T) {
	f := newCodexFixture(t)
	f.res.err = errors.New("store down")
	dec, err := f.dec.Decide(context.Background(), codexReq(session.EventPreToolUse, "sess-x", "exec-x"), "bearer")
	if err != nil {
		t.Fatalf("Decide must not error: it must deny: %v", err)
	}
	if dec.Verdict != session.VerdictDeny {
		t.Errorf("an unresolvable identity must deny, got %q", dec.Verdict)
	}
	if dec.SessionSID != "" {
		t.Errorf("a denied-for-identity call must carry no sid, got %q", dec.SessionSID)
	}
}

// TestAllowWithoutReceiptIsDowngraded is the precedent's rule, kept. With no store wired
// there is no receipt, so an ALLOW must come back as a DENY rather than as an unprovable
// permission.
// codexFailingAuthenticator no resuelve NADA: es el caso de producción en que el portador
// existe pero el autenticador no puede validarlo.
type codexFailingAuthenticator struct{}

func (codexFailingAuthenticator) Authenticate(context.Context, string) (auth.Principal, error) {
	return auth.Principal{}, errors.New("authenticator down")
}

// TestNonFirmIdentityAlwaysDenies fija el control deny-closed del que depende TODO lo demás de
// este PEP: una identidad que no es firme se deniega SIEMPRE, sin perilla que lo module.
//
// ⛔ ESTA PRUEBA NACIÓ DE UNA MUTACIÓN QUE SOBREVIVIÓ (2026-08-19). Al retirar la `requireFirm`
// vestigial medí si alguien fijaba la denegación de `codexhookpep.go` (`if tier != tierFirm`), y
// desactivarla dejaba la batería ENTERA en verde. El control existía y no lo probaba nadie: la
// celda de identidad que había tumbaba el resolvedor de SESIÓN, así que denegaba por otra rama.
//
// ⚠ Y LO QUE ESTA PRUEBA NO PUEDE SEPARAR, dicho para que nadie lo lea al revés: mutar SÓLO
// `if tier != tierFirm` la deja VERDE, porque la comprobación de pertenencia que viene detrás
// deniega igual — `principalOf` devuelve un principal VACÍO siempre que el tier no es firme, así
// que «no firme» implica «no miembro» y ningún input alcanzable separa las dos guardas. Mutando
// LAS DOS, esta prueba sí cae (medido). O sea: son defensa en profundidad, no una sola puerta,
// y por eso retirar la perilla no deja nada al descubierto.
//
// Si esta prueba se pone roja, no la relajes: es el hecho de autorización que el canon §1-bis
// prohíbe poder apagar desde configuración.
func TestNonFirmIdentityAlwaysDenies(t *testing.T) {
	t.Run("portador vacío", func(t *testing.T) {
		f := newCodexFixture(t)
		dec, err := f.dec.Decide(context.Background(), codexReq(session.EventPreToolUse, "sess-nf", "exec-nf"), "")
		if err != nil {
			t.Fatalf("Decide must not error: it must deny: %v", err)
		}
		if dec.Verdict != session.VerdictDeny {
			t.Errorf("un portador vacío debe denegar, salió %q", dec.Verdict)
		}
	})

	t.Run("el autenticador no resuelve", func(t *testing.T) {
		f := newCodexFixture(t)
		f.dec.authr = codexFailingAuthenticator{}
		dec, err := f.dec.Decide(context.Background(), codexReq(session.EventPreToolUse, "sess-nf2", "exec-nf2"), "bearer-cualquiera")
		if err != nil {
			t.Fatalf("Decide must not error: it must deny: %v", err)
		}
		if dec.Verdict != session.VerdictDeny {
			t.Errorf("un portador irresoluble debe denegar, salió %q", dec.Verdict)
		}
	})
}

func TestAllowWithoutReceiptIsDowngraded(t *testing.T) {
	f := newCodexFixture(t)
	f.dec.store = nil // no ledger ⇒ no receipt

	dec, err := f.dec.Decide(context.Background(), codexReq(session.EventPreToolUse, "sess-d", "exec-d"), "bearer")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if dec.Verdict != session.VerdictDeny {
		t.Errorf("an ALLOW that could not be anchored must be downgraded to DENY, got %q", dec.Verdict)
	}
	if !strings.Contains(dec.Reason, "evidence unavailable") {
		t.Errorf("the downgrade must say why, got %q", dec.Reason)
	}
	if dec.SessionSID == "" {
		t.Error("the downgraded deny must still carry the session it belongs to")
	}
}

// TestTenantHintCannotRedirect: a header that disagrees with the endpoint's tenant is a
// denial. Letting an inbound hint SELECT the tenant would let a hook file its decisions
// under any tenant it named.
func TestTenantHintCannotRedirect(t *testing.T) {
	f := newCodexFixture(t)
	req := codexReq(session.EventPreToolUse, "sess-t", "exec-t")
	req.Identity.Tenant = model.NewID().String()
	dec, err := f.dec.Decide(context.Background(), req, "bearer")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if dec.Verdict != session.VerdictDeny {
		t.Errorf("a mismatched tenant hint must deny, got %q", dec.Verdict)
	}
	if f.res.calls != 0 {
		t.Error("a mismatched tenant must be refused before any identity is minted")
	}
}

// TestProviderIsNeverTakenFromThePayload guards the SG-00 collision property at its source.
// If the provider could come from the caller, "claude:X" and "codex:X" would stop being two
// sessions the moment a caller said so.
func TestProviderIsNeverTakenFromThePayload(t *testing.T) {
	f := newCodexFixture(t)
	if _, err := f.dec.Decide(context.Background(), codexReq(session.EventPreToolUse, "shared-id", "exec-1"), "bearer"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if _, ok := f.res.minted["codex:shared-id"]; !ok {
		t.Errorf("the binding must always be provider=codex, got keys %v", codexKeysOf(f.res.minted))
	}
}

func codexKeysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- Corrections earned in the E2 contrast (2026-08-03) ---

// TestLifecycleEventsDoNotCollapse is the regression test for a real defect the contrast
// found. The lifecycle events carry no tool_use_id, and SessionStart carries no turn_id
// either, so the first fallback collapsed EVERY SessionStart in a session onto one
// operation id: a startup and a later resume looked like a replay of each other and the
// second was silently not recorded.
func TestLifecycleEventsDoNotCollapse(t *testing.T) {
	f := newCodexFixture(t)
	base := ledgerCount(t, f.store, f.tenant, "codex.hook.")

	startup := codexReq(session.EventSessionStart, "sess-lc", "")
	startup.Tool, startup.ResourceKind, startup.Mode = "", "", ""
	startup.PayloadDigest = "digest-of-the-startup-payload"

	resume := startup
	resume.PayloadDigest = "digest-of-the-resume-payload"

	for _, r := range []session.Request{startup, resume} {
		if _, err := f.dec.Decide(context.Background(), r, "bearer"); err != nil {
			t.Fatalf("decide: %v", err)
		}
	}
	if got := ledgerCount(t, f.store, f.tenant, "codex.hook.") - base; got != 2 {
		t.Errorf("a startup and a resume are two facts and must leave two entries, got %d", got)
	}

	// …and a byte-identical REDELIVERY of one of them is still a replay.
	if _, err := f.dec.Decide(context.Background(), startup, "bearer"); err != nil {
		t.Fatalf("redeliver: %v", err)
	}
	if got := ledgerCount(t, f.store, f.tenant, "codex.hook.") - base; got != 2 {
		t.Errorf("a byte-identical redelivery must not add an entry, count went to %d", got)
	}
}

// TestOperationIDSurvivesAMerge is the second contrast correction. SG-00 merges are
// explicit and audited, and after one the same alias resolves to the WINNER. Keying the
// operation on the resolved sid would give one fact two different ids across a merge, so a
// retry either side of one would anchor twice. The key uses the external id, which does
// not move.
func TestOperationIDSurvivesAMerge(t *testing.T) {
	req := codexReq(session.EventPreToolUse, "sess-merge", "exec-m")
	who := actorRef{name: "token:abc", kind: "token"}
	before := codexEvidenceBinding("t1", req, session.Decision{Verdict: session.VerdictAllow, SessionSID: "osn_loser"}, who, codexRoleDecision)
	after := codexEvidenceBinding("t1", req, session.Decision{Verdict: session.VerdictAllow, SessionSID: "osn_winner"}, who, codexRoleDecision)
	if before.OperationID != after.OperationID {
		t.Error("the operation id must not change when a merge moves the resolved sid: the fact is the same")
	}
	// The EFFECT DIGEST must be merge-stable too, and that is the subtler half. The store
	// treats a known operation arriving with a DIFFERENT digest as a rebind and refuses it
	// — which, through the allow-without-receipt rule, would turn a post-merge retry of a
	// legitimate ALLOW into a spurious DENY. So the digest covers what was ANSWERED (the
	// event, the tool, the resource, the verdict) and deliberately not our internal naming
	// of the session.
	if before.EffectDigest != after.EffectDigest {
		t.Error("the effect digest must not move with the resolved sid: a merge would then rebind and manufacture denies")
	}
	// It must still move when the ANSWER moves, or a rebind could never be detected.
	changed := codexEvidenceBinding("t1", req, session.Decision{Verdict: session.VerdictDeny, Reason: "no", SessionSID: "osn_winner"}, who, codexRoleDecision)
	if changed.EffectDigest == after.EffectDigest {
		t.Error("a different verdict for the same operation must change the effect digest")
	}
}

// TestTenantHintRequiresMembership is the third contrast correction. Checking that the hint
// EQUALS the endpoint's tenant is not enough: without a membership test, anyone who reaches
// the socket and names the right tenant has their calls filed under it.
func TestTenantHintRequiresMembership(t *testing.T) {
	f := newCodexFixture(t)
	// A principal with no grant in this tenant, presenting the correct tenant hint.
	f.dec.authr = codexAuthenticator{principal: auth.ScopedPrincipal(model.NewID(), "outsider", model.TenantID(model.NewID().String()), auth.RoleEditor)}
	req := codexReq(session.EventPreToolUse, "sess-mem", "exec-mem")
	req.Identity.Tenant = f.tenant.String()

	dec, err := f.dec.Decide(context.Background(), req, "bearer")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if dec.Verdict != session.VerdictDeny {
		t.Errorf("a non-member naming the right tenant must be denied, got %q", dec.Verdict)
	}
	if f.res.calls != 0 {
		t.Error("a non-member must be refused before any identity is minted")
	}
}

// --- Corrections earned in the E5 implementation contrast (2026-08-03) ---

// TestOperationIDIsIndependentOfTheOutcome is the rebind-detection guard. With the verdict
// inside the operation id, a redelivery answered differently — a policy edited between the
// two — computed a DIFFERENT id and appended a second entry quietly, instead of colliding
// with the first. The store's contract is same operation + different effect = rebind; that
// only works if the id does not move with the answer.
func TestOperationIDIsIndependentOfTheOutcome(t *testing.T) {
	req := codexReq(session.EventPreToolUse, "sess-o", "exec-o")
	who := actorRef{name: "token:abc", kind: "token"}
	allow := codexEvidenceBinding("t1", req, session.Decision{Verdict: session.VerdictAllow, SessionSID: "osn_1"}, who, codexRoleDecision)
	deny := codexEvidenceBinding("t1", req, session.Decision{Verdict: session.VerdictDeny, Reason: "policy changed", SessionSID: "osn_1"}, who, codexRoleDecision)

	if allow.OperationID != deny.OperationID {
		t.Error("the operation id must not move with the verdict, or a re-answered redelivery slips past rebind detection")
	}
	if allow.EffectDigest == deny.EffectDigest {
		t.Error("the effect digest MUST move with the verdict, or a rebind is undetectable")
	}
	// The compensating downgrade is a DIFFERENT entry by role, not by outcome.
	comp := codexEvidenceBinding("t1", req, session.Decision{Verdict: session.VerdictDeny, SessionSID: "osn_1"}, who, codexRoleCompensation)
	if comp.OperationID == allow.OperationID {
		t.Error("the compensating downgrade is its own record and needs its own operation id")
	}
}

// TestEffectDigestBindsActorAndPolicy: an allow by one principal is not the same fact as an
// allow by another, and a policy version change that flips a verdict must be visible.
func TestEffectDigestBindsActorAndPolicy(t *testing.T) {
	req := codexReq(session.EventPreToolUse, "sess-b", "exec-b")
	base := session.Decision{Verdict: session.VerdictAllow, SessionSID: "osn_1", PolicyVersion: "v1"}
	a := codexEvidenceBinding("t1", req, base, actorRef{name: "token:one", kind: "token"}, codexRoleDecision)
	b := codexEvidenceBinding("t1", req, base, actorRef{name: "token:two", kind: "token"}, codexRoleDecision)
	if a.EffectDigest == b.EffectDigest {
		t.Error("two different principals answering must not produce the same effect digest")
	}
	withV2 := base
	withV2.PolicyVersion = "v2"
	c := codexEvidenceBinding("t1", req, withV2, actorRef{name: "token:one", kind: "token"}, codexRoleDecision)
	if a.EffectDigest == c.EffectDigest {
		t.Error("a different policy version must not produce the same effect digest")
	}
	// The REQUEST is bound too: a different tool on the same operation is a different effect.
	other := req
	other.Tool = "apply_patch"
	d := codexEvidenceBinding("t1", other, base, actorRef{name: "token:one", kind: "token"}, codexRoleDecision)
	if a.EffectDigest == d.EffectDigest {
		t.Error("the effective request must be bound into the effect digest")
	}
}

// TestUnauthenticatedCallIsDeniedWithoutAHint is the authorization hole the contrast found:
// the membership test ran only when a tenant hint was present, so the way to skip
// authorization entirely was to send NOTHING. A loopback bind is not an authorization
// decision.
func TestUnauthenticatedCallIsDeniedWithoutAHint(t *testing.T) {
	f := newCodexFixture(t)
	f.dec.authr = nil // no authenticator ⇒ no principal resolves

	req := codexReq(session.EventPreToolUse, "sess-anon", "exec-anon")
	req.Identity.Tenant = "" // and no hint at all

	dec, err := f.dec.Decide(context.Background(), req, "")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if dec.Verdict != session.VerdictDeny {
		t.Errorf("an unauthenticated call must be denied even with no tenant hint, got %q", dec.Verdict)
	}
	if f.res.calls != 0 {
		t.Error("an unauthenticated call must be refused before any identity is minted")
	}
}

// TestNonMemberIsDeniedWithoutAHint is the same hole from the other side: authenticated,
// but to a tenant that is not this endpoint's, and sending no hint to avoid the check.
func TestNonMemberIsDeniedWithoutAHint(t *testing.T) {
	f := newCodexFixture(t)
	f.dec.authr = codexAuthenticator{principal: auth.ScopedPrincipal(model.NewID(), "outsider", model.TenantID(model.NewID().String()), auth.RoleEditor)}

	req := codexReq(session.EventPreToolUse, "sess-out", "exec-out")
	req.Identity.Tenant = ""

	dec, err := f.dec.Decide(context.Background(), req, "bearer")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if dec.Verdict != session.VerdictDeny {
		t.Errorf("a non-member must be denied whether or not a hint is sent, got %q", dec.Verdict)
	}
}
