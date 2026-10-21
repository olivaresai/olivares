// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A session whose tool-calls the policy keeps refusing is not exploring, it is
// hunting for a way round: another credential, a verification-skip flag, another
// target. After a few refusals the agent's next move should be a person's, so the
// run keeps a budget of policy denials and, once it is spent, hands the session to
// a person. The hook PEP feeds it one decision per governed tool-call.

// Defaults of the budget: the floor Anthropic's auto mode uses.
const (
	DefaultDenialConsecutive = 3
	DefaultDenialTotal       = 20
)

const (
	denialBudgetAction  = "sessions.run.denial_budget"
	denialReasonsKept   = 5   // the most recent reasons shown to the person
	denialReasonBytes   = 200 // one reason, clipped
	denialByConsecutive = "consecutive"
	denialByTotal       = "total"
	denialInterrupted   = "interrupt"
	denialStopped       = "stop"

	needsPersonRepeatedDenials = "repeated_policy_denials"
)

// DenialBudget is a tenant's limit on policy denials in one session run. A zero field
// means the default.
type DenialBudget struct {
	Consecutive int `json:"consecutive,omitempty"`
	Total       int `json:"total,omitempty"`
}

func (b DenialBudget) limits() (consecutive, total int) {
	consecutive, total = b.Consecutive, b.Total
	if consecutive <= 0 {
		consecutive = DefaultDenialConsecutive
	}
	if total <= 0 {
		total = DefaultDenialTotal
	}
	return consecutive, total
}

// denialCount is one run's tally, guarded by liveRun.mu. It starts again from zero when
// a person answers a spent budget. The breach it holds keeps one stretch of denials from
// escalating twice, and makes the run "need a person" once it was interrupted.
type denialCount struct {
	consecutive, total int
	reasons            []string
	breach             *denialBreach
}

type denialBreach struct {
	trigger                      string
	consecutive, total           int
	limitConsecutive, limitTotal int
	reasons                      []string
	interrupted                  bool // the turn was interrupted and awaits a person; guarded by liveRun.mu
}

// needsPersonDTO tells a viewer why the session stopped working on its own.
type needsPersonDTO struct {
	Code               string   `json:"code"`
	ConsecutiveDenials int      `json:"consecutive_denials"`
	TotalDenials       int      `json:"total_denials"`
	Reasons            []string `json:"reasons"`
}

// record counts one decision and returns the breach it caused, if any.
func (d *denialCount) record(denied bool, reason string, maxConsecutive, maxTotal int) *denialBreach {
	if !denied {
		d.consecutive = 0
		return nil
	}
	d.consecutive++
	d.total++
	if len(reason) > denialReasonBytes {
		reason = strings.ToValidUTF8(reason[:denialReasonBytes], "") + "…"
	}
	d.reasons = append(d.reasons, reason)
	if len(d.reasons) > denialReasonsKept {
		d.reasons = d.reasons[len(d.reasons)-denialReasonsKept:]
	}
	if d.breach != nil {
		return nil
	}
	var trigger string
	switch {
	case d.consecutive >= maxConsecutive:
		trigger = denialByConsecutive
	case d.total >= maxTotal:
		trigger = denialByTotal
	default:
		return nil
	}
	d.breach = &denialBreach{
		trigger: trigger, consecutive: d.consecutive, total: d.total,
		limitConsecutive: maxConsecutive, limitTotal: maxTotal, reasons: slices.Clone(d.reasons),
	}
	return d.breach
}

// RecordToolDecision counts one governed tool-call decision of a supervised session:
// denied for a policy, firewall or invariant refusal, not denied for an allow. A
// person declining an approval is not a refusal by policy, and the caller does not
// report it. Counts survive ordinary input and start again only when an operator answers
// a spent budget. They live in this process, like the run's other supervised state. The principal must be this run's current session credential.
//
// The breach is handled off the caller's path: the verdict the agent is waiting for
// must not wait for the interrupt to be confirmed.
func (m *Module) RecordToolDecision(principal auth.Principal, denied bool, reason string, budget DenialBudget) error {
	tenant := principal.SessionScope()
	if tenant.IsZero() || tenant.IsSystem() || !principal.IsMember(tenant) || principal.SessionIdentity == "" || principal.SessionRunRef == "" || principal.SessionFence < 1 {
		return auth.ErrUnauthenticated
	}
	lr, ok := m.rt.getLive(tenant, principal.SessionRunRef)
	if !ok || lr.claim.SID != principal.SessionIdentity || lr.claim.Fence != principal.SessionFence {
		return auth.ErrUnauthenticated
	}
	maxConsecutive, maxTotal := budget.limits()
	lr.mu.Lock()
	if lr.finalized || lr.stopRequested || lr.launchFailed {
		lr.mu.Unlock()
		return auth.ErrUnauthenticated
	}
	breach := lr.denials.record(denied, reason, maxConsecutive, maxTotal)
	lr.mu.Unlock()
	if breach != nil {
		go m.escalateDenials(lr, breach)
	}
	return nil
}

// answerDenialBreach is called when an operator's input reached the child: the console and
// the CLI. Input from the work plane, a handoff or a retained-authority integration is not
// a person's answer, so a run that needs a person keeps needing one.
func (m *Module) answerDenialBreach(tenant model.TenantID, runRef string) {
	if lr, ok := m.rt.getLive(tenant, runRef); ok {
		lr.answerBreach()
	}
}

// answerBreach is a person's input to the run. When the budget was spent it answers the
// breach, and the budget starts again; input to a run that has not spent its budget
// changes nothing, so the total keeps counting across ordinary prompts.
func (lr *liveRun) answerBreach() {
	lr.mu.Lock()
	if lr.denials.breach != nil {
		lr.denials = denialCount{}
	}
	lr.mu.Unlock()
}

// breachCurrent reports whether b is still the unanswered breach of the run.
func (lr *liveRun) breachCurrent(b *denialBreach) bool {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return lr.denials.breach == b
}

// runNeedsPerson is the run's "needs a person" fact: its turn was interrupted because the
// budget was spent, and nobody has answered yet.
func (m *Module) runNeedsPerson(rec model.Record) *needsPersonDTO {
	lr, ok := m.currentLiveOf(rec)
	if !ok {
		return nil
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	b := lr.denials.breach
	if b == nil || !b.interrupted || lr.finalized || lr.stopRequested || lr.launchFailed {
		return nil
	}
	return &needsPersonDTO{
		Code: needsPersonRepeatedDenials, ConsecutiveDenials: b.consecutive, TotalDenials: b.total,
		Reasons: slices.Clone(b.reasons),
	}
}

// errBreachAnswered stops an escalation whose breach a person answered while it waited.
var errBreachAnswered = errors.New("sessions: the denial-budget breach was answered before it was handled")

// escalateDenials hands a run that spent its budget to a person. With a viewer
// attached it interrupts the turn and leaves the session live for the person's next
// input. With nobody watching there is no one to answer, so it stops the session
// instead of leaving it waiting; an interrupt that fails falls back to the same stop.
// One ledger entry records the breach and what was done. A breach a person answered
// meanwhile, or a run somebody else already ended, is left alone.
func (m *Module) escalateDenials(lr *liveRun, b *denialBreach) {
	if cur, ok := m.rt.getLive(lr.tenant, lr.runRef); !ok || cur != lr {
		return // a successor owns this run now
	}
	lr.mu.Lock()
	ended := lr.finalized || lr.stopRequested
	lr.mu.Unlock()
	if ended || !lr.breachCurrent(b) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*m.rt.waitDelay+30*time.Second)
	defer cancel()
	detail := "repeated policy denials (" + strconv.Itoa(b.consecutive) + " in a row, " + strconv.Itoa(b.total) + " in total)"
	action := denialStopped
	if lr.viewers.Load() > 0 {
		switch err := m.interruptForDenials(ctx, lr, b, detail); {
		case errors.Is(err, errBreachAnswered):
			return
		case err != nil:
			m.warnf("sessions: the denial-budget interrupt failed; stopping the session instead", "run_ref", lr.runRef, "err", redactErr(err))
		default:
			action = denialInterrupted
		}
	}
	if action == denialStopped && (!lr.breachCurrent(b) || !m.stopOwnedRun(lr, detail)) {
		return // answered meanwhile, or somebody else ended the run first
	}
	// The entry gets its own deadline: a slow interrupt must not cost the record.
	rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer rcancel()
	err := m.runtimeData(rctx).Mutate(rctx, lr.tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(rctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem, Action: denialBudgetAction, TargetKind: runKind,
			Meta: map[string]any{
				"run_ref": lr.runRef, "runtime_launch_id": lr.launchID.String(), "action": action, "trigger": b.trigger,
				"consecutive": b.consecutive, "total": b.total,
				"limit_consecutive": b.limitConsecutive, "limit_total": b.limitTotal, "reasons": b.reasons,
			},
		})
		return err
	})
	if err != nil {
		m.warnf("sessions: the denial-budget breach could not be recorded; the containment already happened", "run_ref", lr.runRef, "action", action, "err", redactErr(err))
	}
}

// interruptForDenials is the runtime's own interrupt: it takes the run lock like the
// operator route but needs no operator. It does not apply the work-lease fence that the
// operator routes do: like the kill switch and the duration ceiling it is an act of the
// runtime upon a session it owns, and a leased run is the one that most needs it.
func (m *Module) interruptForDenials(ctx context.Context, lr *liveRun, b *denialBreach, detail string) error {
	release, err := m.rt.lockRunContext(ctx, liveKey(lr.tenant, lr.runRef))
	if err != nil {
		return err
	}
	defer release()
	if !lr.breachCurrent(b) {
		return errBreachAnswered // a person's input held this lock first and answered it
	}
	rec, err := m.loadRun(ctx, lr.tenant, lr.runRef)
	if err != nil {
		return err
	}
	if _, _, err = m.interruptRunLoadedWithReason(ctx, lr.tenant, lr.runRef, model.ActorSystem, model.ActorSystem, detail, rec); err != nil {
		return err
	}
	lr.mu.Lock()
	if lr.denials.breach == b {
		b.interrupted = true
	}
	lr.mu.Unlock()
	return nil
}
