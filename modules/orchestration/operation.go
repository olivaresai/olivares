// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// operation.go — the SHARED single-operation + durable-outbox primitive
// used by BOTH the direct schedule fire (D-05) and the workflow acting steps
// (D-06). It implements the frozen evidence law (sdk/evidence.go): a governed
// external effect reserves its OperationID and commits its evidence anchor AND
// its outbox intent in ONE transaction BEFORE the effect is emitted, so
//
//   - the gate's pure-read Status can never authorize a second dispatch of the
//     same approval (the operation's UNIQUE(tenant, approval_ref) burns it);
//   - the SAME OperationID + SAME EffectDigest is an idempotent retry that
//     replays the recorded outcome and never re-emits;
//   - the SAME OperationID + a DIFFERENT EffectDigest is a replay/rebind and is
//     refused (sdk.FailureReplay — NOT a new EvidenceFault);
//   - a Seq==0 degrade-drop confirms ONLY the loss accounting: no operation, no
//     outbox, and the effect is refused.
//
// The state strings below are INTERNAL lifecycle labels; none is an EvidenceFault.

// Acting surfaces + binding profile (versioned).
const (
	surfaceScheduleFire = "orchestration.schedule.fire"
	surfaceWorkflowStep = "orchestration.workflow.step"
	surfaceWorkflowRun  = "orchestration.workflow.run"
	bindingProfileV1    = "orchestration.target-binding.v1"
)

// operation lifecycle states (colOpState) — internal, NOT an EvidenceFault.
const (
	opStateClaimed    = "claimed"    // reserved + anchored, effect not yet confirmed
	opStateDispatched = "dispatched" // the effect was actuated
	opStateDeclared   = "declared"   // approved but no actuator wired (honest no-op)
	opStateFailed     = "failed"     // the dispatch failed BEFORE any transport write (retryable only with a new approval)
	opStateUnknown    = "unknown"    // dispatch outcome ambiguous — NEVER re-actuated (at-most-once)
)

// outbox lifecycle states (colObState).
//
// HONESTY NOTE: the outbox today is a CLAIM-ONLY durable intent ledger —
// there is NO asynchronous drainer. The effect is emitted SYNCHRONOUSLY by the
// acting request/step right after the claim commits, and the outbox row is
// settled to its terminal state in the same settle transaction as the operation.
// obStateDispatchStarted is therefore RESERVED for a future leader-gated drainer
// and is deliberately never transitioned here: a "dispatch_started" that no
// worker owns would be an at-least-once hazard. Do NOT add a drainer that
// rescues a stale claim without first implementing a real ready→dispatch_started
// CAS at the actuation boundary — the safety of the current design rests on the
// operation being single-use and every replay being REFUSED, not on a drainer.
const (
	obStateReady           = "ready"
	obStateDispatchStarted = "dispatch_started" // RESERVED (future drainer); never set today
	obStateDispatched      = "dispatched"
	obStateFailed          = "failed"
	obStateUnknown         = "unknown"
)

// _ references the reserved state so it is not flagged as unused while it awaits
// a future drainer (see the HONESTY NOTE above).
var _ = obStateDispatchStarted

var (
	// errOperationReplay is a same-OperationID / DIFFERENT-EffectDigest rebind:
	// the caller maps it to sdk.FailureReplay (a FailureClass, not an EvidenceFault).
	errOperationReplay = errors.New("orchestration: operation replay (effect digest mismatch)")
	// errOperationRaced is a concurrent claim that lost the UNIQUE insert; the
	// caller re-runs the claim once and finds the winner (idempotent replay).
	errOperationRaced = errors.New("orchestration: operation claim raced; retry")
	// errEvidenceGap is a Seq==0 degrade-drop: the loss accounting committed but
	// no per-operation anchor exists, so the effect MUST be refused.
	errEvidenceGap = errors.New("orchestration: evidence degraded (spool gap); effect refused")
)

// operationSpec is the identity + binding a caller reserves for one effect.
type operationSpec struct {
	tenant        string // the owning tenant (part of the full-binding digest)
	approvalRef   string // the single-use approval (direct-fire dedup key); "<run>:<step>" for a step
	operationID   string // deterministic id for a step; "" ⇒ server-mint a fresh UUID (direct fire)
	surface       string
	action        string
	planHash      string
	policyVersion string // the governing policy/contract version bound into the digest
	bindProfile   string
	targetFp      string // approved target fingerprint (opaque; HMAC for steps)
	scheduleRef   string // correlation (direct fire)
	auditTarget   model.ID
}

// operationClaim is the committed reservation of one effect.
type operationClaim struct {
	rec     model.Record
	replay  bool
	binding sdk.EvidenceBinding
	receipt sdk.EvidenceReceipt
}

// effectDigest binds the operation to its EXACT effect (sdk.EffectDigest). It is
// the FULL-binding digest (sdk/evidence.go:86): profile+version, tenant, surface,
// action, resolved target fingerprint, the approved plan, the approval reference
// and the policy version. It deliberately EXCLUDES the OperationID (which the
// digest is paired WITH, not derived from), so the same approval fired against a
// changed effect is a rebind (FailureReplay), not a retry.
func (m *Module) effectDigest(spec operationSpec) sdk.EffectDigest {
	// Canonical (length-prefixed) preimage — no controllable field can shift a
	// boundary to collide with another effect's digest (canonicalization).
	return sdk.EffectDigest(canonicalHash("olivares.orchestration.effect.v1",
		spec.bindProfile, spec.tenant, spec.surface, spec.action, spec.planHash,
		spec.approvalRef, spec.targetFp, spec.policyVersion))
}

// claimOperationInTx is the in-transaction body of a claim: it looks up the
// operation, replays it on an exact-digest match, refuses a digest mismatch
// (errOperationReplay), and otherwise anchors the claim + outbox in the CALLER's
// open transaction (so a purely local effect — a workflow run row — can commit
// its anchor and its effect in ONE store transaction, per sdk/evidence.go). It
// NEVER calls ClassifyAnchor (that is a post-commit step the caller runs). It
// returns appendDropped=true on a Seq==0 degrade (the caller must stage nothing
// and let the loss accounting commit).
func (m *Module) claimOperationInTx(ctx context.Context, sc store.Scope, mc api.ModuleContext, spec operationSpec, digest sdk.EffectDigest) (claim operationClaim, appendDropped bool, evidenceRef string, err error) {
	repo, e := sc.Ext(operationKind)
	if e != nil {
		return operationClaim{}, false, "", e
	}
	// Retry key: the deterministic operation_id for a step, else approval_ref.
	var prior model.Record
	var ok bool
	if spec.operationID != "" {
		prior, ok, e = findOne(ctx, repo, eq(colOpOperationID, spec.operationID))
	} else {
		prior, ok, e = findOne(ctx, repo, eq(colOpApprovalRef, spec.approvalRef))
	}
	if e != nil {
		return operationClaim{}, false, "", e
	}
	if ok {
		if prior.String(colOpEffectDigest) != string(digest) {
			return operationClaim{}, false, "", errOperationReplay // same op, different effect ⇒ FailureReplay
		}
		return operationClaim{
			rec:     prior,
			replay:  true,
			binding: sdk.EvidenceBinding{OperationID: sdk.OperationID(prior.String(colOpOperationID)), EffectDigest: digest},
		}, false, "", nil
	}

	// Fresh: mint the OperationID (server-minted UUID for direct fire; the
	// caller-supplied deterministic id for a step) and anchor the claim.
	opID := spec.operationID
	if opID == "" {
		opID = model.NewID().String()
	}
	binding := sdk.EvidenceBinding{OperationID: sdk.OperationID(opID), EffectDigest: digest}

	ev, ae := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:      mc.Principal.Actor(),
		ActorKind:  mc.Principal.ActorKind(),
		Action:     spec.action + ".claim",
		TargetKind: operationKind,
		TargetID:   spec.auditTarget,
		Meta: map[string]any{
			"operation_id": opID, "effect_digest": string(digest),
			"approval_ref": spec.approvalRef, "surface": spec.surface,
		},
	})
	if ae != nil {
		return operationClaim{}, false, "", ae // real error ⇒ rollback, nothing durable
	}
	if ev.Seq == 0 {
		// Degrade-drop: loss accounting committed; stage NO operation/outbox.
		return operationClaim{}, true, "", nil
	}
	evidenceRef = hex.EncodeToString(ev.Hash)

	created, ce := repo.Create(ctx, model.Record{
		colOpApprovalRef: spec.approvalRef, colOpOperationID: opID,
		colOpEffectDigest: string(digest), colOpSurface: spec.surface,
		colOpAction: spec.action, colOpPlanHash: spec.planHash,
		colOpBindProfile: spec.bindProfile, colOpTargetFp: spec.targetFp,
		colOpEvidenceRef: evidenceRef, colOpState: opStateClaimed,
		colOpScheduleRef: nilIfEmpty(spec.scheduleRef),
	})
	if ce != nil {
		if errors.Is(ce, store.ErrConflict) {
			return operationClaim{}, false, "", errOperationRaced // a concurrent winner exists; retry finds it
		}
		return operationClaim{}, false, "", ce
	}
	obRepo, oe := sc.Ext(outboxKind)
	if oe != nil {
		return operationClaim{}, false, "", oe
	}
	if _, oe = obRepo.Create(ctx, model.Record{
		colObOperationID: opID, colObEffectDigest: string(digest),
		colObTargetFp: spec.targetFp, colObState: obStateReady,
	}); oe != nil {
		return operationClaim{}, false, "", oe
	}
	return operationClaim{rec: created, replay: false, binding: binding}, false, evidenceRef, nil
}

// classifyClaim converts the in-tx claim result into a post-commit *operationClaim
// with its receipt (never inside the transaction). A committed degrade-drop
// returns errEvidenceGap.
func classifyClaim(claim operationClaim, appendDropped bool, evidenceRef string, digest sdk.EffectDigest, spec operationSpec) (*operationClaim, error) {
	if appendDropped {
		binding := sdk.EvidenceBinding{OperationID: sdk.OperationID(spec.operationID), EffectDigest: digest}
		_ = sdk.ClassifyAnchor(binding, "", true, sdk.EvidenceFaultNone)
		return nil, errEvidenceGap
	}
	ref := evidenceRef
	if claim.replay {
		ref = claim.rec.String(colOpEvidenceRef)
	}
	claim.receipt = sdk.ClassifyAnchor(claim.binding, ref, false, sdk.EvidenceFaultNone)
	return &claim, nil
}

// claimOperation reserves the operation (its OperationID) and commits the
// evidence anchor + outbox intent in ONE transaction, per the frozen law. It
// returns a replay claim when the operation already exists (idempotent), and
// errOperationReplay / errOperationRaced / errEvidenceGap for the refusing
// paths. The caller emits the effect ONLY for a fresh (non-replay) claim whose
// receipt AnchoredFor its binding, then settles via settleOperation.
func (m *Module) claimOperation(ctx context.Context, mc api.ModuleContext, spec operationSpec) (*operationClaim, error) {
	digest := m.effectDigest(spec)
	var claim operationClaim
	var appendDropped bool
	var evidenceRef string
	err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		var e error
		claim, appendDropped, evidenceRef, e = m.claimOperationInTx(ctx, sc, mc, spec, digest)
		return e
	})
	if err != nil {
		return nil, err
	}
	return classifyClaim(claim, appendDropped, evidenceRef, digest, spec)
}

// settleOperation records the terminal outcome of a claimed operation and its
// outbox in ONE transaction, alongside any caller-supplied side effect (e.g.
// advanceFired / recordDecision). A settle failure leaves the operation
// "claimed" and the outbox "dispatch_started" ⇒ a retry replays the ambiguity
// and NEVER re-actuates (at-most-once after the ambiguous point).
func (m *Module) settleOperation(ctx context.Context, sc store.Scope, claim *operationClaim, opState, obState, dispatchRef, outcome string) error {
	opRepo, err := sc.Ext(operationKind)
	if err != nil {
		return err
	}
	opID := string(claim.binding.OperationID)
	opRec, ok, err := findOne(ctx, opRepo, eq(colOpOperationID, opID))
	if err != nil {
		return err
	}
	if !ok {
		// A missing claim row at settle time is an INTEGRITY error, not a silent
		// success: the caller's transaction must roll back so the effect is not
		// recorded as settled against a claim that vanished.
		return fmt.Errorf("orchestration: settle: operation %s claim row missing (integrity)", opID)
	}
	opRec[colOpState] = opState
	if dispatchRef != "" {
		opRec[colOpDispatchRef] = dispatchRef
	}
	if outcome != "" {
		opRec[colOpOutcome] = clamp(outcome, maxNameLen)
	}
	if _, err = opRepo.Update(ctx, opRec); err != nil {
		return err
	}
	obRepo, err := sc.Ext(outboxKind)
	if err != nil {
		return err
	}
	obRec, ok, err := findOne(ctx, obRepo, eq(colObOperationID, opID))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("orchestration: settle: outbox %s row missing (integrity)", opID)
	}
	obRec[colObState] = obState
	if dispatchRef != "" {
		obRec[colObDispatchRef] = dispatchRef
	}
	if outcome != "" {
		obRec[colObOutcome] = clamp(outcome, maxNameLen)
	}
	if _, err = obRepo.Update(ctx, obRec); err != nil {
		return err
	}
	return nil
}

// nilIfEmpty returns nil for an empty string so a nullable column stays NULL.
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
