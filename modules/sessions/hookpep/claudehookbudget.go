// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// A session whose tool-calls the policy keeps refusing is handed to a person
// (sessions.Module.RecordToolDecision). This file decides which decisions count.
// A refusal counts when the policy, the firewall or a platform invariant made it.
// A person declining an approval, or the tool-call being canceled while one waits, is
// not the policy refusing and is not counted. A review nobody answered in time is.

// humanAnswerKey carries one decision's humanAnswer down to the approval gate.
type humanAnswerKey struct{}

// humanAnswer is set by the approval gate when the refusal it returns is the person's.
type humanAnswer struct{ declined bool }

// declinedByPerson marks the decision in progress as a person's refusal.
func declinedByPerson(ctx context.Context) {
	if answer, ok := ctx.Value(humanAnswerKey{}).(*humanAnswer); ok {
		answer.declined = true
	}
}

// countSessionDecision reports a finished decision to the run's denial budget. Only the
// gating events count (a tool-call before it runs); a lifecycle or output event is
// neither a refusal nor a permit of a tool-call. The verdict is already final: a
// failure here is logged and changes nothing.
func (d *Decider) countSessionDecision(tenant model.TenantID, principal auth.Principal, authErr error, in claude.HookDecisionInput, out claude.HookDecisionResult, answer *humanAnswer) {
	if d.SessionDecision == nil || authErr != nil || principal.SessionIdentity == "" || !impedesBeforeTheCall(in.Event) {
		return
	}
	var denied bool
	switch out.Permission {
	case claude.DecisionAllow:
	case claude.DecisionDeny:
		if answer.declined {
			return
		}
		denied = true
	default:
		return // a pending review is neither
	}
	if err := d.SessionDecision(principal, denied, out.Reason, d.Tenants[tenant].denialBudget); err != nil && d.Log != nil {
		if errors.Is(err, auth.ErrUnauthenticated) {
			d.Log.Debug("hook-pep: decision not counted; the run is no longer this credential's", "tenant", tenant.String())
		} else {
			d.Log.Warn("hook-pep: decision could not be counted against the session's denial budget; the verdict stands", "tenant", tenant.String(), "err", err)
		}
	}
}
