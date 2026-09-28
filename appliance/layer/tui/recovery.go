// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/netguard"
	"github.com/olivaresai/olivares/appliance/layer/repair"
)

// PowerAdmission is the admission every power act of this console passes: the attested invoker,
// the qualified sign-in read again inside the hold, the serialization point and the retained gates,
// and for a recovery reboot the linkage committed before the effect. repair.Admission is the
// installed one.
type PowerAdmission interface {
	Ordinary(ctx context.Context, signedIn func() bool, effect func(context.Context) error) error
	Recover(ctx context.Context, plan repair.Plan, confirmed string, signedIn func() bool, effect func(context.Context) error) error
}

// WithPowerAdmission returns the console passing every power act through a.
func (c Console) WithPowerAdmission(a PowerAdmission) Console {
	c.admission = a
	return c
}

// WithBootID returns the console reading this boot's identity through boot.
func (c Console) WithBootID(boot func() (string, error)) Console {
	c.boot = boot
	return c
}

// recoveryAnswer is what the operator types at the power question to ask for a recovery reboot.
const recoveryAnswer = "recovery-reboot"

// recoveryConfirm is the question the recovery plan ends with.
const recoveryConfirm = "Type the pending network operation id shown above to confirm this recovery reboot, " +
	"or anything else to ask for nothing:"

// statusTimeout bounds the guard's status read for the plan.
const statusTimeout = 5 * time.Second

// sessionReference is a sign-in that names itself by a non-secret audit reference.
type sessionReference interface {
	Ref() string
}

// RecoveryPlan reads the guard's status and, when it shows exactly one network window of this boot
// whose mutating reply was lost, returns the recovery reboot's plan with the statement to show: the
// pending operation, its boot and window generation, the surfaces it blocks, and the power operation
// id minted for this plan. Otherwise it returns no plan and says why; nothing is asked either way.
func (c Console) RecoveryPlan() (*repair.Plan, string) {
	const refused = "recovery-reboot: refused: "
	if !c.qualified() {
		return nil, "recovery-reboot: " + signInRequired
	}
	reference, ok := c.signIn.(sessionReference)
	if !ok {
		return nil, refused + "the sign-in names no audit reference, so nothing was asked."
	}
	if c.networkClient == nil || c.boot == nil {
		return nil, refused + "this console cannot read the network guard's status, so nothing was asked."
	}
	boot, err := c.boot()
	if err != nil {
		return nil, refused + "this boot's identity cannot be read, so nothing was asked."
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	response, err := c.networkClient.Call(ctx, netguard.EdgeRequest{Action: "status"})
	if err != nil {
		return nil, refused + "the network guard's status is unknown, so nothing was asked."
	}
	pending, err := repair.PendingFromStatus(response, boot)
	if err != nil {
		return nil, refused + refusalText(err) + " Nothing was asked."
	}
	operation, err := helperschema.NewOperationID(c.randomness())
	if err != nil {
		return nil, refused + "no power operation id could be minted, so nothing was asked."
	}
	plan := repair.Plan{PowerOperationID: operation, Action: repair.ActionReboot, Pending: pending, SessionRef: reference.Ref()}
	return &plan, fmt.Sprintf("Recovery reboot for the network operation %s, whose reply was lost in boot %s, window generation %s. "+
		"While it stays pending, network apply and confirm, firewall changes, managed and unattended package operations and "+
		"exposure changes are blocked by it; reads and existing safety withdrawals continue. This is power operation %s: "+
		"the existing reboot through the power helper, with every other gate kept, and its linkage to that network operation "+
		"recorded before the reboot is asked. After the reboot the network guard settles the old calls from the new boot and "+
		"measures restoration; the reboot itself proves no rollback.",
		pending.OperationID, pending.BootID, pending.WindowGeneration, plan.PowerOperationID)
}

// RecoveryReboot asks for the recovery reboot of plan when typed is its pending network operation
// id, through the admission, which reads every retained gate again inside its hold and commits the
// linkage before the power helper is asked. Without a qualified sign-in, or with any other text, it
// asks nothing.
func (c Console) RecoveryReboot(plan repair.Plan, typed string) string {
	if !c.qualified() {
		return "recovery-reboot: " + signInRequired
	}
	if typed != plan.Pending.OperationID {
		return "Nothing was asked of the power helper."
	}
	if c.helper == nil {
		return "recovery-reboot: no helper is wired to this console, so nothing was asked and nothing was performed."
	}
	if c.admission == nil {
		return "recovery-reboot: refused: the power gates are not composed with this console, so nothing was asked of the power helper."
	}
	ctx, cancel := context.WithTimeout(context.Background(), powerTimeout)
	defer cancel()
	var response helperschema.Response
	var callErr error
	err := c.admission.Recover(ctx, plan, plan.Digest(), c.qualified, func(ctx context.Context) error {
		response, callErr = c.helper.Call(ctx, helperschema.HelperPower,
			&helperschema.PowerRequest{Verb: helperschema.PowerReboot, OperationID: plan.PowerOperationID})
		return nil
	})
	if err != nil {
		return "recovery-reboot: " + refusalText(err) + " Nothing was asked of the power helper."
	}
	return "recovery-reboot: power operation " + plan.PowerOperationID + " is recorded as the recovery of network operation " +
		plan.Pending.OperationID + ". " + powerAnswer(helperschema.PowerReboot, response, callErr)
}

// refusalText states an admission's refusal: the gate that refused and its fixed reason.
func refusalText(err error) string {
	var refusal *repair.Refusal
	if errors.As(err, &refusal) {
		return "refused by the " + refusal.Gate + " gate: " + refusal.Reason + "."
	}
	return "refused: the admission could not be completed."
}
