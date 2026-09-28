// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package repair

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// Plan is a recovery reboot as the console displayed it and the operator confirmed it: the power
// operation id minted for it, the action, the pending window and the sign-in's audit reference.
type Plan struct {
	PowerOperationID string
	Action           string
	Pending          PendingNetwork
	SessionRef       string
}

// planDomain separates a plan's digest from every other digest.
const planDomain = "olivares.ai/network-recovery-power-plan/v1"

// Digest is the SHA-256 of the plan's canonical text: the one digest the operator confirmed and the
// linkage records as confirmed_plan_digest.
func (p Plan) Digest() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{planDomain, p.PowerOperationID, p.Action, p.Pending.OperationID,
		p.Pending.BootID, p.Pending.WindowGeneration, p.Pending.StatusDigest, p.SessionRef}, "\n")))
	return hex.EncodeToString(sum[:])
}

// Refusal refuses a power act before any effect. Gate names the check that refused; Reason is
// fixed text that never repeats a value the caller supplied.
type Refusal struct {
	Gate   string
	Reason string
}

func (r *Refusal) Error() string { return "refused by " + r.Gate + ": " + r.Reason }

func refuse(gate, reason string) error { return &Refusal{Gate: gate, Reason: reason} }

// Gate is one check retained before a power act. exempt is the one pending network window the
// operator confirmed a recovery reboot for, and the zero PendingNetwork for ordinary power: only the
// window gate and the operation store's network row read it, and no other gate is exempted by it.
type Gate interface {
	Name() string
	Check(ctx context.Context, exempt PendingNetwork) error
}

// Serializer is the admission's serialization point. Hold returns once no conflicting host
// operation or lifecycle transition can be admitted until release is called, or refuses.
type Serializer interface {
	Hold(ctx context.Context) (release func(), err error)
}

// Admission is the tty1 power flow's admission: the attested invoker, the operator's qualified
// sign-in, the serialization point, the retained gates and, for a recovery reboot, the linkage
// record committed before the effect. It adds no power verb and no force: the effect is the power
// helper's own reboot or shutdown, with its own admission.
type Admission struct {
	// Invoker attests the process asking: it must be the repair console on tty1.
	Invoker func() (helperschema.Peer, error)
	// Serializer holds the admission from the gates through the effect's handoff.
	Serializer Serializer
	// Gates are the retained checks, read inside the hold.
	Gates []Gate
	// Store commits the recovery reboot's linkage.
	Store Linker
	// Now is the clock the linkage's creation time is read from.
	Now func() time.Time
}

// Ordinary admits an ordinary power act, a reboot or a shutdown, and runs effect inside the hold.
// No window is exempt: an open network window refuses it like every other running operation.
func (a Admission) Ordinary(ctx context.Context, signedIn func() bool, effect func(context.Context) error) error {
	return a.admit(ctx, PendingNetwork{}, signedIn, nil, effect)
}

// Recover admits the recovery reboot plan the operator confirmed by its digest, confirmed, and runs
// effect inside the hold after the linkage is committed. Only the plan's one pending window is
// exempt from the running-operation veto; every other gate still refuses. A plan that is not the
// one displayed, a malformed plan, or any action but reboot refuses before anything is read.
func (a Admission) Recover(ctx context.Context, plan Plan, confirmed string, signedIn func() bool, effect func(context.Context) error) error {
	switch {
	case plan.Action != ActionReboot:
		return refuse("plan", "a recovery reboot is the power verb reboot and nothing else")
	case !lowerHex(plan.PowerOperationID, 32), !lowerHex(plan.SessionRef, 32), plan.Pending.Validate() != nil:
		return refuse("plan", "the plan is outside the power operation's and the guard's types")
	case plan.Digest() != confirmed:
		return refuse("plan", "the plan is not the one the operator confirmed")
	}
	return a.admit(ctx, plan.Pending, signedIn, &plan, effect)
}

// admit is both flows: the invoker, the sign-in, the hold, every gate, the sign-in again, then the
// linkage when there is a plan, then the effect, and the hold released after it.
func (a Admission) admit(ctx context.Context, exempt PendingNetwork, signedIn func() bool, plan *Plan, effect func(context.Context) error) error {
	if a.Invoker == nil {
		return refuse("invoker", "no invoker can be attested")
	}
	peer, err := a.Invoker()
	if err != nil || !peer.Attested || peer.UID != 0 || peer.Account != helperschema.RepairConsole.Account ||
		peer.Unit != helperschema.RepairConsole.Unit || peer.TTY != helperschema.RepairConsole.TTY {
		return refuse("invoker", "only the repair console's own process, root in its unit on /dev/tty1, asks for power here")
	}
	if signedIn == nil || !signedIn() {
		return refuse("sign-in", "the console's qualified sign-in does not hold")
	}
	if a.Serializer == nil {
		return refuse("serialization", "no serialization point is composed")
	}
	release, err := a.Serializer.Hold(ctx)
	if err != nil {
		return refuse("serialization", "the admission could not be held: "+reasonOf(err))
	}
	defer release()
	for _, gate := range a.Gates {
		if err := gate.Check(ctx, exempt); err != nil {
			return refuse(gate.Name(), reasonOf(err))
		}
	}
	if !signedIn() {
		return refuse("sign-in", "the console's qualified sign-in ended while the gates were read")
	}
	if plan != nil {
		if a.Store == nil || a.Now == nil {
			return refuse("linkage", "no linkage store is composed, so nothing was performed")
		}
		linkage := Linkage{Schema: LinkageSchema, PowerOperationID: plan.PowerOperationID, Action: plan.Action,
			ConfirmedPlanDigest: plan.Digest(), SignInSessionRef: plan.SessionRef,
			CreatedAtUTC: a.Now().UTC().Truncate(time.Second).Format(time.RFC3339), PendingNetwork: plan.Pending}
		if err := a.Store.Commit(linkage); err != nil {
			return refuse("linkage", "the linkage record was not committed, so nothing was performed: "+reasonOf(err))
		}
	}
	return effect(ctx)
}

// reasonOf is the text of a gate's own refusal, which each gate states without a caller's value.
func reasonOf(err error) string {
	if refusal, ok := err.(*Refusal); ok {
		return refusal.Reason
	}
	return err.Error()
}
