// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package effectgate

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// One human approval authorizes one effect, and is spent once. Every gated effect
// of the composition root asks here, in one of two shapes:
//
//   - gateEffect, over the approval bridge, for a caller re-issued on every retry
//     that holds no approval between calls: the MCP gateway's tools/call, the
//     legacy Claude Code hook and a privileged session launch;
//   - holdEffect, over the engine approval queue, for a caller that holds the call
//     until a human answers: the session's Claude Code hook, a session MCP tool
//     and a provider permission prompt.
//
// This module alone opens or finds the approval, waits for it or reports it
// pending, checks that the answer is about this effect, rechecks the caller's
// authority after a wait, spends the approval once (a break-glass grant is spent
// when the engine grants it) and withdraws a request the call stopped waiting for
// before its deadline. Callers state the effect and map the answer onto their own
// reply.

// effectOutcome is what the approval of one effect came to. The zero value refuses.
type Outcome uint8

const (
	Refused     Outcome = iota // not approved; Status names the answer
	Allowed                    // the effect may run
	Pending                    // gateEffect: no answer yet; the caller is re-issued
	Replay                     // approved, but another consumer already spent it
	Unspendable                // approved, but no longer spendable
	Foreign                    // holdEffect: the answer is about another effect
	Unshown                    // holdEffect: the queued request does not show the reviewed facts
)

// effectStep is the step at which an approval returned an error. Every error is a
// denial; the step tells an outage from a refusal in the caller's reply.
type Step uint8

const (
	Open     Step = iota + 1 // opening, finding or reading the approval
	Register                 // projecting the wait onto the supervised run
	Wait                     // waiting for the answer; the context cause is wrapped
	Recheck                  // the caller's authority after the answer
	Spend                    // spending the approval
)

// effectAnswer is the approval of one effect. Failed is set exactly when the
// approval returned an error.
type Answer struct {
	Outcome   Outcome
	Ref       string // the approval, or its "breakglass:" or "no-gate:" reference
	Status    string // the approval's own status: nbApproved, nbBreakGlass, nbPending, ...
	BoundHash string // gateEffect: the plan hash the approval is bound to
	Spent     bool   // spent once for the consumer, or a break-glass use the engine recorded
	Failed    Step
}

// approvalSpend is how an approved answer authorizes a gated effect.
type ApprovalSpend uint8

const (
	// spendOnce spends the approval for one consumer.
	SpendOnce ApprovalSpend = iota
	// reuseInWindow lets an approved grant serve identical calls inside its time
	// box, unspent: the published rule for an MCP gateway call without a round
	// trip (docs/contracts/mcp-gateway-management.md). Single use still holds
	// among round trips.
	ReuseInWindow
)

// gatedEffect is an effect whose caller re-derives its approval from the plan
// hash on every retry.
type GatedEffect struct {
	Tenant                          model.TenantID
	Action, SubjectKind, SubjectRef string
	PlanHash                        string // binds the approval to the exact plan (anti-TOCTOU)
	Reason, RequestedBy, SessionRef string
	// Ref names an approval the caller already holds. It is read, scope-checked
	// against the effect, instead of opening or finding one.
	Ref string
	// Consumer spends the approval. Empty mints a fresh server-side nonce: the
	// first spend binds it and any later one is a replay, so a retry without a
	// non-forgeable transport id needs a new human decision.
	Consumer      string
	PolicyVersion string
	Spend         ApprovalSpend
}

// effectBridge is the approval bridge as gateEffect uses it. *approvalBridge
// satisfies it.
type Bridge interface {
	GateOnceForSession(ctx context.Context, tenant model.TenantID, action, subjectKind, subjectRef, planHash, reason, requestedBy, sessionRef string) (ref, status, boundHash string, err error)
	StatusScoped(ctx context.Context, tenant model.TenantID, ref, planHash, wantAction, wantSubjectKind, wantSubjectRef string) (status, boundHash string, err error)
	ConsumeApproval(ctx context.Context, tenant model.TenantID, ref, consumerID, policyVersion string) (granted, replay bool, err error)
}

// gateEffect opens or finds the effect's approval (reusing an approved grant
// inside its time box, or authorizing under an active break-glass grant), or
// reads the one the caller names, and answers at once: pending is an answer.
func Gate(ctx context.Context, bridge Bridge, e GatedEffect) (Answer, error) {
	a := Answer{Ref: e.Ref}
	var err error
	if e.Ref != "" {
		a.Status, a.BoundHash, err = bridge.StatusScoped(ctx, e.Tenant, e.Ref, e.PlanHash, e.Action, e.SubjectKind, e.SubjectRef)
	} else {
		a.Ref, a.Status, a.BoundHash, err = bridge.GateOnceForSession(ctx, e.Tenant, e.Action, e.SubjectKind, e.SubjectRef, e.PlanHash, e.Reason, e.RequestedBy, e.SessionRef)
	}
	if err != nil {
		return Answer{Failed: Open}, err
	}
	switch a.Status {
	case governance.GateStatusPending:
		a.Outcome = Pending
	case governance.GateStatusBreakGlass:
		// The engine recorded this use, with its ledger event and finding, when it
		// granted it; the underlying approval stays in the human queue.
		a.Outcome, a.Spent = Allowed, e.Spend == SpendOnce
	case governance.GateStatusApproved:
		if e.Spend == ReuseInWindow {
			a.Outcome = Allowed
			return a, nil
		}
		consumer := e.Consumer
		if consumer == "" {
			consumer = NewSingleUseConsumerID()
		}
		granted, replay, err := bridge.ConsumeApproval(ctx, e.Tenant, a.Ref, consumer, e.PolicyVersion)
		if err != nil {
			a.Failed = Spend
			return a, err
		}
		a.Outcome, a.Spent = spendOutcome(granted, replay)
	}
	return a, nil
}

// heldEffect is an effect whose caller holds the call until a human answers.
type HeldEffect struct {
	Tenant model.TenantID
	// Principal proposes the request and withdraws it when the call stops waiting.
	Principal auth.Principal
	// Request is what the human reviews. Its session, action, subject kind and
	// subject identify the effect; the subject carries Consumer, so every held call
	// opens its own approval.
	Request       governance.ApprovalRequest
	Consumer      string
	PolicyVersion string
	// Wait projects the hold onto the supervised run until the earlier of the
	// context deadline and the request's policy expiry. Nil: no projection.
	Wait func(context.Context, auth.Principal, string, time.Time) (func(), error)
	// Shown confirms that the queued request shows the reviewed facts. Nil: no check.
	Shown func(governance.Approval) bool
	// Recheck repeats the caller's authority after the answer, before the spend.
	// Nil: no recheck.
	Recheck func(context.Context) error
	Log     *slog.Logger
}

// effectQueue is the engine approval queue as holdEffect uses it.
// *governance.EngineApprovals satisfies it.
type Queue interface {
	Request(ctx context.Context, tenant model.TenantID, principal auth.Principal, in governance.ApprovalRequest) (governance.Approval, error)
	Wait(ctx context.Context, tenant model.TenantID, ref string) (governance.Approval, error)
	Cancel(ctx context.Context, tenant model.TenantID, principal auth.Principal, ref string) (governance.Approval, error)
	Consume(ctx context.Context, tenant model.TenantID, ref, consumerID, policyVersion string) (governance.ApprovalConsumption, error)
}

// holdEffect queues a request for the effect and holds until a human answers or
// ctx ends. ctx bounds every step, the spend included. A request the call stops
// waiting for is withdrawn, except at the wait deadline: Wait records that expiry
// itself, and a deadline never becomes a cancellation. A racing terminal decision
// is retained either way.
func Hold(ctx context.Context, queue Queue, e HeldEffect) (Answer, error) {
	request, err := queue.Request(ctx, e.Tenant, e.Principal, e.Request)
	if err != nil {
		return Answer{Failed: Open}, err
	}
	a := Answer{Ref: request.ID, Status: request.Status}
	withdraw := true
	defer func() {
		if withdraw {
			withdrawEffect(ctx, queue, e, request.ID)
		}
	}()
	if e.Shown != nil && !e.Shown(request) {
		a.Outcome = Unshown
		return a, nil
	}
	var endWait func()
	if e.Wait != nil {
		if endWait, err = registerEffectWait(ctx, e, request); err != nil {
			a.Failed = Register
			return a, err
		}
	}
	answer, err := queue.Wait(ctx, e.Tenant, request.ID)
	if endWait != nil {
		endWait()
	}
	if err != nil {
		withdraw = !errors.Is(err, context.DeadlineExceeded)
		a.Failed = Wait
		return a, err
	}
	withdraw = false
	a.Status = answer.Status
	if answer.SessionRef != e.Request.SessionRef || answer.Action != e.Request.Action || answer.SubjectKind != e.Request.SubjectKind || answer.SubjectRef != e.Request.SubjectRef {
		a.Outcome = Foreign
		return a, nil
	}
	if answer.Status != governance.GateStatusApproved {
		return a, nil
	}
	// Human review cannot outlive the authority the call had when it asked.
	if e.Recheck != nil {
		if err := e.Recheck(ctx); err != nil {
			a.Failed = Recheck
			return a, err
		}
	}
	spent, err := queue.Consume(ctx, e.Tenant, request.ID, e.Consumer, e.PolicyVersion)
	if err != nil {
		a.Failed = Spend
		return a, err
	}
	a.Outcome, a.Spent = spendOutcome(spent.Granted, spent.Replay)
	return a, nil
}

// registerEffectWait projects the hold onto the run until the earlier of the live
// deadline and the request's policy expiry. A hold bounded by neither is refused.
func registerEffectWait(ctx context.Context, e HeldEffect, request governance.Approval) (func(), error) {
	until, bounded := ctx.Deadline()
	if request.ExpiresAt != "" {
		expiry, err := model.ParseTimestamp(request.ExpiresAt)
		if err != nil {
			return nil, err
		}
		if !bounded || expiry.Time().Before(until) {
			until, bounded = expiry.Time(), true
		}
	}
	if !bounded {
		return nil, errors.New("approval wait requires a bounded context deadline")
	}
	return e.Wait(ctx, e.Principal, request.ID, until)
}

// withdrawEffect cancels the call's own pending request. Cancel cannot overwrite
// an approved, rejected or expired answer.
func withdrawEffect(ctx context.Context, queue Queue, e HeldEffect, ref string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	closed, err := queue.Cancel(cleanupCtx, e.Tenant, e.Principal, ref)
	if err != nil && (closed.ID == "" || closed.Status == governance.GateStatusPending) && e.Log != nil {
		e.Log.Error("approval cancellation incomplete", "action", e.Request.Action, "approval_ref", ref, "err", err)
	}
}

// spendOutcome reads a spend: a spend by another consumer is a replay, a refused
// one is no longer spendable.
func spendOutcome(granted, replay bool) (Outcome, bool) {
	switch {
	case replay:
		return Replay, false
	case !granted:
		return Unspendable, false
	}
	return Allowed, true
}

const singleUseConsumerPrefix = "singleuse-"

var singleUseSeq atomic.Uint64

// NewSingleUseConsumerID binds an approval without a transport id to a fresh server-side consumer.
func NewSingleUseConsumerID() string {
	var b [24]byte
	_, _ = rand.Read(b[:16])                                // 128 bits of entropy (best-effort)
	binary.BigEndian.PutUint64(b[16:], singleUseSeq.Add(1)) // monotonic tail: uniqueness never depends on the RNG
	return singleUseConsumerPrefix + hex.EncodeToString(b[:])
}
