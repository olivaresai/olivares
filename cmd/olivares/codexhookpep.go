// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/codex/session"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk"
)

// codexhookpep.go is the GOVERNED half of the Codex session PEP: the part that may touch
// /core, and therefore the part that resolves identity, consults the PDP and anchors the
// decision to the tamper-evident ledger.
//
// # Why this is not claudehookpep.go with a flag
//
// Measured the alternative before writing a line, because the brief's rule is that
// needing to change connectors/claude means you are duplicating rather than reusing.
//
// The first draft of this reasoning was WRONG in one respect and the contrast caught it:
// "anchorDecision is not importable" is irrelevant here, because this file is in the SAME
// package main — visibility was never the obstacle. The real obstacles are its RECEIVER
// (*claudeHookDecider, a type built out of Claude-shaped inputs) and its provider-specific
// constants: hookActionCapability is "claude.tool.use" and rides the ledger event as
// TargetKind and the PDP verb root. (The ledger Action itself, "hook.tool.<decision>", is
// generic — an earlier note here claimed otherwise and that claim is withdrawn.) A Codex
// decision anchored through Claude's capability would be attributed to Claude in every
// evidence query, which is falsifying provenance, not reusing code.
//
// The contrast proposed a better end state than either option: ONE provider-neutral AGPL
// governor driven by an immutable per-engine PROFILE, with the Apache adapters only
// parsing and rendering their own wire. That is right, and this file adopts its half —
// every provider-specific value below lives in codexProfile rather than being scattered
// through the logic, so lifting the machine into a shared governor is a move, not a
// rewrite. Doing the lift itself means editing claudehookpep.go, which is not this
// session's territory: it is pack SG-01-Codex-d, with an owner, in the design note.
//
// What IS reused today, because it is importable and Apache: the SDK evidence primitives
// (sdk.EvidenceBinding, sdk.ClassifyAnchor, receipt.MustRefuse), the shared PDP seam
// (auth.PolicyEvaluator), and the store's evidence-operation journal.

// hookGovernanceProfile is the immutable per-engine identity of a governed hook surface.
// Every value in it is a thing that MUST differ between engines for the evidence to name
// the right one; collecting them in a struct is what makes the difference auditable at a
// glance instead of spread across a file.
type hookGovernanceProfile struct {
	// Provider is the SG-00 alias provider, lower case.
	Provider string
	// Surface is the PEP surface recorded on the evidence claim.
	Surface string
	// Capability is the PDP verb root AND the ledger TargetKind.
	Capability string
	// ActionRoot prefixes the ledger action ("<root>.<verdict>").
	ActionRoot string
	// Hash domains, one per commitment, never shared with another engine.
	OperationDomain string
	EffectDomain    string
	DecisionDomain  string
}

// codexProfile is the Codex engine's profile.
var codexProfile = hookGovernanceProfile{
	Provider:        session.EngineCodex,
	Surface:         codexHookPEPSurface,
	Capability:      codexHookCapability,
	ActionRoot:      "codex.hook",
	OperationDomain: codexOperationDomain,
	EffectDomain:    codexHookEffectDomain,
	DecisionDomain:  codexHookDecisionDomain,
}

// codexHookCapability is the governance action a gated Codex tool-call opens, and the verb
// root of the PDP request. It is deliberately NOT "claude.tool.use".
const codexHookCapability = "codex.tool.use"

// Hash domains. Separate constants keep Codex commitments from colliding with any other
// length-prefixed hash written to the ledger — including Claude's. Each one carries its
// own literal: a valueless entry in a const block silently REPEATS the previous
// expression, which here would give two different domains the same value and quietly
// destroy the separation this block exists to provide.
const (
	codexHookDecisionDomain = "olivares.codex.hook.decision.v1"
	codexOperationDomain    = "olivares.codex.hook.operation.v1"
	codexHookEffectDomain   = "olivares.codex.hook.effect.v1"
	codexHookPEPSurface     = "codex-hook-pep"
)

// Ledger ROLES. A role says what an entry IS — the decision for this hook call, or the
// compensating record of a decision that had to be downgraded — and never what the verdict
// WAS.
//
// That distinction is a correction earned in contrast, and it is the difference between
// having rebind detection and only thinking you do. An earlier cut used the outcome
// (terminal-allow / terminal-deny) as part of the operation identity. The consequence was
// that a redelivery answered differently — a policy edited between the two — computed a
// DIFFERENT operation id and quietly appended a second entry, instead of colliding with
// the first and being caught. The store's whole contract is: same operation + different
// effect = ErrEvidenceRebind (sdk/evidence.go). Putting the effect in the id opts out of it.
const (
	codexRoleDecision     = "decision"
	codexRoleCompensation = "compensating-downgrade"
)

// actorRef is the audit attribution pair kept together so a call site cannot pass the
// name with somebody else's kind.
type actorRef struct{ name, kind string }

// codexSessionResolver is the SG-00 identity seam. *sessions.Module satisfies it; tests
// inject a fake. Resolution is the ONLY way a Codex session id becomes one of ours.
type codexSessionResolver interface {
	ResolveSession(ctx context.Context, tenant model.TenantID, b sessions.SessionBinding) (string, error)
}

// codexHookDecider is the governed decider behind the Codex hook PEP.
type codexHookDecider struct {
	// tenant is the single tenant this endpoint governs. The hint header is CHECKED
	// against it, never trusted to select it: letting an inbound header choose the tenant
	// would let a hook attribute its own decisions wherever it liked.
	tenant   model.TenantID
	authr    principalAuthenticator
	eval     auth.PolicyEvaluator // nil ⇒ no external overlay
	sessions codexSessionResolver
	store    store.Store
	clock    func() time.Time
	log      *slog.Logger
}

var _ session.Decider = (*codexHookDecider)(nil)

// Decide governs one Codex hook call.
//
// Order matters and is deliberate: identity is resolved BEFORE the verdict, so a denied
// call still lands on the right session's timeline (a session that is denied everything is
// exactly the session an operator most needs to see); and the anchor runs LAST, on the
// terminal verdict only, so the ledger records what was actually answered.
func (d *codexHookDecider) Decide(ctx context.Context, req session.Request, bearer string) (session.Decision, error) {
	now := d.clock()

	// 1. Principal. The bearer is authoritative; the hints only refine attribution.
	principal, actor, tier := d.principalOf(ctx, bearer)

	// 2. Authorization, on EVERY request — with a hint or without one.
	//
	// An earlier cut ran the membership test only when a tenant hint was present, which
	// meant the way to skip authorization entirely was to send NOTHING: no hint, no
	// bearer, and the call was governed under this endpoint's tenant as if it belonged
	// there. The loopback bind was the only thing standing in the way, and a bind is not
	// an authorization decision. The precedent's own rule is the right one
	// (claudehookpep.go resolveTenant): a caller who is not a member is refused, and an
	// undeterminable tenant is deny-closed rather than assumed.
	if tier != tierFirm {
		return codexDeny("no authenticated principal on a governed hook call (deny-closed)", ""), nil
	}
	// staticcheck QF1001 propone De Morgan aquí. NO se aplica, y la razón es de seguridad, no de
	// gusto: esta guarda es deny-closed y la forma actual —una disyunción POSITIVA bajo una sola
	// negación— refleja literalmente la política («hay que ser superadmin O miembro»). La forma
	// `!A && !B` es equivalente hoy y pierde esa forma: quien añada una tercera condición mañana
	// tiene que acordarse de negarla también. Cero cambio de comportamiento a cambio de un riesgo
	// real en la comprobación de pertenencia.
	//nolint:staticcheck // QF1001: la disyunción positiva refleja la política; ver arriba
	if !(principal.Superadmin || principal.IsMember(d.tenant)) {
		return codexDeny("principal is not a member of this endpoint's tenant (deny-closed)", ""), nil
	}
	// A hint may only CONFIRM the tenant this endpoint governs. It can never select one:
	// letting an inbound header choose would let a hook file its decisions wherever it
	// liked, membership test or not.
	// the same shared policy. An ABSENT hint is legitimate (it simply confirms
	// nothing); a PRESENT hint must be a usable business tenant AND be this endpoint's.
	hintTID, hintPresent, hintErr := parseBusinessTenant("codex hook request: identity tenant", req.Identity.Tenant)
	if hintErr != nil || (hintPresent && hintTID != d.tenant) {
		return codexDeny("tenant hint does not match this endpoint's tenant", ""), nil
	}

	// 3. Canonical identity (SG-00). The Codex UUIDv7 is an ALIAS; the sid is ours.
	sid, ierr := d.resolveSID(ctx, req, now)
	if ierr != nil {
		// Identity failure is not a license to proceed: without a sid the call cannot be
		// attributed, recorded or later audited, and an unattributable governed action is
		// exactly what this plane exists to prevent.
		d.logw("codex-hook: could not resolve session identity", "err", ierr)
		return codexDeny("session identity could not be resolved (deny-closed)", ""), nil
	}

	// 4. Verdict.
	dec := session.Decision{
		Verdict:    session.VerdictAllow,
		SessionSID: sid,
		// Enforced says only "the GOVERNED decider produced this", which is true by
		// definition here. Whether that amounts to enforcement is a second question the
		// connector answers with CanImpede: a decider's deny on PostToolUse is governed
		// and still arrives after the tool ran. Setting CanImpede here would have folded
		// the two questions into one field and made the distinction unrecoverable.
		Enforced: true,
	}
	if forbidden, reason := d.pdpForbidsCodex(ctx, principal, req); forbidden {
		dec.Verdict = session.VerdictDeny
		dec.Reason = reason
	}

	// 5. Anchor. This is the only ledger writer on the Codex session path.
	return d.anchorCodexDecision(ctx, req, dec, actor), nil
}

// resolveSID turns Codex's own session id into the canonical sid. Provider is the constant
// "codex" — never taken from the payload, because a provider a caller can choose is a
// provider a caller can impersonate, and SG-00's whole collision guarantee rests on the
// provider being part of the alias key.
func (d *codexHookDecider) resolveSID(ctx context.Context, req session.Request, now time.Time) (string, error) {
	if d.sessions == nil {
		return "", errors.New("codex-hook: no session identity plane is wired")
	}
	if strings.TrimSpace(req.ExternalSessionID) == "" {
		return "", sessions.ErrNoExternalID
	}
	return d.sessions.ResolveSession(ctx, d.tenant, sessions.SessionBinding{
		Provider:   codexProfile.Provider,
		ExternalID: req.ExternalSessionID,
		Origin:     sessions.OriginObserved,
		At:         now,
	})
}

// principalOf resolves the bearer. An unresolvable bearer is not an error here: it yields
// the unknown tier, and the unknown tier is ALWAYS denied (line ~167, deny-closed) — there is
// no knob, and there must not be one.
//
// ⛔ HUBO UNA `requireFirm` AQUÍ Y SE RETIRÓ EL 2026-08-19, adjudicado por el hub (r23). No la
// restaures «por simetría» con claudehookpep.go: el canon §1-bis y el package doc de `addongate`
// lo prohíben con todas las letras —«never gate the evaluation path of a deny-closed security
// control»—, y la distinción que lo sostiene es que retener una capacidad no abre nada, pero
// BORRAR UN HECHO DE AUTORIZACIÓN sí. Una perilla que apaga esta denegación convierte una
// exigencia de identidad firme en algo que el fichero de configuración del operador puede
// desactivar.
//
// Y no se retiró una función: el campo NUNCA se leyó. Lo que se retiró fue una afirmación falsa
// del interfaz — el operador ponía `require_firm`, lo veía confirmado en el log del arranque, y
// no gobernaba nada.
func (d *codexHookDecider) principalOf(ctx context.Context, bearer string) (auth.Principal, actorRef, string) {
	if d.authr == nil || strings.TrimSpace(bearer) == "" {
		return auth.Principal{}, actorRef{}, tierUnknown
	}
	p, err := d.authr.Authenticate(ctx, bearer)
	if err != nil || p.IsPurposeRestricted() {
		return auth.Principal{}, actorRef{}, tierUnknown
	}
	actor, kind := codexActorOf(p)
	return p, actorRef{name: actor, kind: kind}, tierFirm
}

// pdpForbidsCodex consults the SAME composed PDP the rest of the plane uses, under the
// Codex capability. The evaluator is restrict-only: it can never widen.
func (d *codexHookDecider) pdpForbidsCodex(ctx context.Context, p auth.Principal, req session.Request) (bool, string) {
	if d.eval == nil {
		return false, ""
	}
	r := auth.Request{
		Principal:  p,
		Permission: auth.Permission(codexProfile.Capability + ":" + codexModeVerb(req.Mode)),
		Tenant:     d.tenant,
		Resource: auth.ResourceAttrs{
			Kind: req.ResourceKind,
			ID:   req.ResourceRef,
			Extra: map[string]string{
				"tool":   req.Tool,
				"mode":   req.Mode,
				"engine": session.EngineCodex,
				"event":  req.Event,
			},
		},
	}
	dec, err := d.eval.Evaluate(ctx, r)
	if err != nil {
		return true, "policy evaluation error (deny-closed)" // fail closed
	}
	if !dec.Allow {
		reason := dec.Reason
		if reason == "" {
			reason = "denied by policy"
		}
		return true, reason
	}
	return false, ""
}

// anchorCodexDecision records the terminal verdict on the ledger and applies the rule the
// Claude precedent established and this one keeps: an ALLOW that could not be anchored is
// DOWNGRADED TO DENY, while a DENY that could not be anchored STANDS and shouts.
//
// The anchor is idempotent BY CONSTRUCTION, which is what R-01 demands. The operation id is
// derived deterministically from (tenant, sid, event, tool_use_id, phase) rather than from
// a fresh nonce, so a redelivered hook call computes the SAME id and the store's
// EvidenceOperations.Claim recognizes it as an exact replay and appends NOTHING — the
// UNIQUE(tenant_id, operation_id) index is the ground truth, not a convention.
func (d *codexHookDecider) anchorCodexDecision(ctx context.Context, req session.Request, dec session.Decision, actor actorRef) session.Decision {
	receipt := d.anchorOnce(ctx, req, dec, actor, codexRoleDecision)
	binding := codexEvidenceBinding(d.tenant, req, dec, actor, codexRoleDecision)
	if !receipt.MustRefuse(binding) {
		return dec
	}
	if dec.Verdict != session.VerdictAllow {
		// A deny with no receipt is still a deny. Losing the evidence is serious and is
		// logged as a gap, but weakening the verdict because the ledger faltered would be
		// exactly backwards.
		d.logw("codex-hook: DENY could not be anchored; the verdict stands and the evidence is missing",
			"event", req.Event, "tool", req.Tool, "session", dec.SessionSID)
		return dec
	}
	d.logw("codex-hook: ALLOW could not be anchored; downgrading to deny",
		"event", req.Event, "tool", req.Tool, "session", dec.SessionSID)
	downgraded := session.Decision{
		Verdict:    session.VerdictDeny,
		Reason:     "evidence unavailable (deny-closed)",
		SessionSID: dec.SessionSID,
		Enforced:   dec.Enforced,
	}
	// Compensating re-anchor of the downgrade, on a context that outlives a canceled
	// request: the refusal itself is a governed act and must leave a trace. The verdict is
	// returned whether or not this lands.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexReanchorTimeout)
	defer cancel()
	_ = d.anchorOnce(rctx, req, downgraded, actor, codexRoleCompensation)
	return downgraded
}

const codexReanchorTimeout = 5 * time.Second

// anchorOnce performs the single claim. It returns a receipt the caller classifies.
func (d *codexHookDecider) anchorOnce(ctx context.Context, req session.Request, dec session.Decision, actor actorRef, role string) sdk.EvidenceReceipt {
	binding := codexEvidenceBinding(d.tenant, req, dec, actor, role)
	if d.store == nil {
		return sdk.ClassifyAnchor(binding, "", false, sdk.EvidenceFaultLedgerUnwired)
	}
	if actor.name == "" {
		// An unattributed governed act still gets a ledger entry: the surface itself is
		// the actor of record, which is more honest than an empty column.
		actor = actorRef{name: "codex-hook", kind: "system"}
	}
	var (
		evidenceRef string
		dropped     bool
	)
	txErr := d.store.Mutate(ctx, d.tenant, func(sc store.Scope) error {
		res, err := sc.EvidenceOperations().Claim(ctx, store.EvidenceClaim{
			OperationID:  string(binding.OperationID),
			EffectDigest: string(binding.EffectDigest),
			Surface:      codexProfile.Surface,
			Action:       codexLedgerAction(dec),
			Actor:        actor.name,
			ActorKind:    actor.kind,
		})
		if err != nil {
			return err
		}
		if res.Dropped {
			// F9 discipline: the loss accounting is staged and nothing else is. Return nil
			// so it COMMITS, then refuse — returning a sentinel from inside the
			// transaction is the historical bug this rule abolishes.
			dropped = true
			return nil
		}
		evidenceRef = res.Op.ClaimEvidenceRef
		return nil
	})
	return sdk.ClassifyAnchor(binding, evidenceRef, dropped, codexClassifyStoreFault(txErr))
}

// codexLedgerAction is the ledger verb. "codex.hook.<decision>" — never "hook.tool.<d>",
// which is Claude's and would make the two engines' decisions indistinguishable.
func codexLedgerAction(dec session.Decision) string {
	return engineLedgerAction(codexProfile, dec.Verdict, session.VerdictDeny)
}

// codexEvidenceBinding derives the {OperationID, EffectDigest} pair.
//
// The split follows the SDK contract exactly, and the reason it matters is that the store
// enforces it: OperationID identifies WHICH governed act this is, EffectDigest describes
// WHAT was done about it, and the same operation arriving with a different effect is an
// ErrEvidenceRebind rather than a second silent entry.
//
// OperationID = (tenant, provider:external_id, event, discriminator, role). It contains no
// part of the outcome. Three of its details are corrections earned in contrast:
//
//   - It keys on the EXTERNAL session id, not the resolved sid. SG-00 merges are explicit
//     and audited, and after one the same alias resolves to the WINNER; keying on the sid
//     would give one fact two ids across a merge, so a retry either side would anchor twice.
//   - The discriminator is tool_use_id where Codex sends one and the PAYLOAD DIGEST where it
//     does not. The lifecycle events carry no tool_use_id and SessionStart carries no turn_id
//     either, so an earlier fallback collapsed every SessionStart in a session onto one key.
//   - The ROLE is what an entry IS, never what the verdict WAS. With the outcome in the id,
//     a redelivery answered differently computed a different id and appended quietly instead
//     of colliding — which is to say rebind detection was off.
//
// EffectDigest covers the full request AND the answer AND who answered under which policy.
// A thinner digest would let two materially different decisions look identical to the
// replay check, which is the failure the check exists to catch.
func codexEvidenceBinding(tenant model.TenantID, req session.Request, dec session.Decision, actor actorRef, role string) sdk.EvidenceBinding {
	key := req.ToolUseID
	if key == "" {
		key = req.PayloadDigest
	}
	return engineEvidenceBinding(codexProfile, tenant, engineFact{
		Event:             req.Event,
		Tool:              req.Tool,
		Discriminator:     key,
		CallID:            req.ToolUseID,
		ExternalSessionID: req.ExternalSessionID,
		ResourceKind:      req.ResourceKind,
		ResourceRef:       req.ResourceRef,
		Mode:              req.Mode,
		Model:             req.Model,
		PermissionMode:    req.PermissionMode,
		PayloadDigest:     req.PayloadDigest,
		Verdict:           dec.Verdict,
		Reason:            dec.Reason,
		PolicyVersion:     dec.PolicyVersion,
	}, actor, role)
}

// codexClassifyStoreFault maps a transaction error onto the evidence fault taxonomy. An
// unrecognized error is NOT "no fault": it classifies as a write fault, so the receipt
// refuses rather than assuming the ledger is fine.
func codexClassifyStoreFault(err error) sdk.EvidenceFault {
	return engineClassifyStoreFault(err)
}

func codexDeny(reason, sid string) session.Decision {
	return session.Decision{Verdict: session.VerdictDeny, Reason: reason, SessionSID: sid}
}

// codexModeVerb maps the access mode onto the PDP verb suffix.
func codexModeVerb(mode string) string {
	switch mode {
	case "read":
		return "read"
	case "write":
		return "write"
	default:
		return "use"
	}
}

// codexActorOf uses the principal's own audit-actor form ("user:<id>" / "token:<id>"),
// which core/auth guarantees is never a secret and never an email.
func codexActorOf(p auth.Principal) (actor, kind string) {
	if p.CredID.IsZero() && p.UserID.IsZero() {
		return "", ""
	}
	return p.Actor(), string(p.ActorKind())
}

func (d *codexHookDecider) logw(msg string, args ...any) {
	if d.log != nil {
		d.log.Warn(msg, args...)
	}
}
