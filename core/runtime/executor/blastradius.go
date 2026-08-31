// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import "fmt"

// BlastRadiusPolicy is a PURE policy over a Diff: it decides whether a change may
// proceed based on how destructive it is. It is the SECOND control on every
// governed mutation — composed on top of the HITL approval the module already
// enforces, never a substitute for it. A change that a human approved via HITL can
// still be blocked here if it is more destructive than policy allows (the classic
// "I approved an update, I did not approve deleting 40 resources" surprise).
//
// The guards mirror the 2026 GitOps bar (Argo CD prune + allowEmpty): a plain
// apply that DELETES/REPLACES real state ("prune") is blocked unless the threshold
// explicitly permits it; an explicit teardown (IntentDestroy) is the deliberate
// path for deletion and is allowed (still bounded, and never an empty-everything
// surprise).
type BlastRadiusPolicy struct {
	// MaxApplyDestructive is the number of destructive items (deletes/replaces) an
	// IntentApply may contain without being blocked. Default 0: a plain apply that
	// would prune or replace real state is blocked — destruction must be an explicit
	// retire or an explicitly-raised threshold (the allowEmpty/prune guard).
	MaxApplyDestructive int
	// AllowDestroy permits IntentDestroy (retire) to delete. Default true: retire is
	// a deliberate, HITL-approved teardown. Set false to forbid teardown entirely.
	AllowDestroy bool
	// MaxDestroyItems caps how many items a single retire may delete without being
	// blocked. Default 0 = unlimited (a retire removes exactly this deployment's
	// resources). A positive value guards against a runaway teardown.
	MaxDestroyItems int
}

// DefaultBlastRadiusPolicy is the conservative default: no destructive change in a
// plain apply (prune/replace must be explicit), teardown allowed and unbounded in
// count (a retire deletes this deployment's own resources).
func DefaultBlastRadiusPolicy() BlastRadiusPolicy {
	return BlastRadiusPolicy{MaxApplyDestructive: 0, AllowDestroy: true, MaxDestroyItems: 0}
}

// Decision is the gate's verdict. Reason is a short, non-sensitive explanation
// surfaced to the operator and the ledger.
type Decision struct {
	Allowed bool
	Reason  string
}

// destructiveCount counts the items in a diff that delete or replace real state:
// everything in Deletes, plus any create/update item explicitly marked Destructive
// (an in-place replace).
func destructiveCount(d Diff) int {
	n := len(d.Deletes)
	for _, set := range [][]ChangeItem{d.Creates, d.Updates} {
		for _, it := range set {
			if it.Destructive {
				n++
			}
		}
	}
	return n
}

// Decide applies the policy to a diff under an intent.
func (p BlastRadiusPolicy) Decide(d Diff, intent Intent) Decision {
	switch intent {
	case IntentDestroy:
		if !p.AllowDestroy {
			return Decision{Allowed: false, Reason: "teardown is disabled by blast-radius policy (AllowDestroy=false)"}
		}
		if p.MaxDestroyItems > 0 && len(d.Deletes) > p.MaxDestroyItems {
			return Decision{Allowed: false, Reason: fmt.Sprintf(
				"teardown would delete %d resource(s); exceeds the blast-radius cap of %d — split the retire or raise the cap",
				len(d.Deletes), p.MaxDestroyItems)}
		}
		return Decision{Allowed: true, Reason: fmt.Sprintf("approved teardown of %d resource(s)", len(d.Deletes))}

	case IntentRollback:
		// A rollback is a recovery action (reversing a prior apply); allow it.
		return Decision{Allowed: true, Reason: "rollback (recovery action) permitted"}

	default: // IntentApply
		dn := destructiveCount(d)
		if dn > p.MaxApplyDestructive {
			return Decision{Allowed: false, Reason: fmt.Sprintf(
				"apply contains %d destructive change(s) (delete/replace) but the blast-radius threshold for an apply is %d; this is a prune/replace that must be an explicit retire or run under a raised threshold",
				dn, p.MaxApplyDestructive)}
		}
		return Decision{Allowed: true, Reason: fmt.Sprintf("apply within blast-radius (%s, %d change(s))", d.BlastRadius.String(), d.Count())}
	}
}
