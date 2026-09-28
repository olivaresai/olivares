// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall

import (
	"errors"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// HelperName is the helper's socket name, /run/olivares-helpers/firewall.sock: the seam's firewall
// module helper, which admits the Appliance Console alone.
const HelperName = helperschema.HelperFirewall

// CodeActNotAdopted refuses an act while the product's act authorization for the network verb
// is not composed with the helper.
const CodeActNotAdopted = "act_not_adopted"

// The helper's closed set of subcommands.
const (
	OpStatus  = "status"
	OpApply   = "apply"
	OpConfirm = "confirm"
	OpRevert  = "revert"
)

// Ops returns the closed set of subcommands in a fixed order.
func Ops() []string { return []string{OpStatus, OpApply, OpConfirm, OpRevert} }

// Request is the helper's closed document: {"op", "operation_id", "candidate", "app",
// "revert_after_s"}. status carries nothing else; confirm and revert carry the operation id;
// apply carries the operation id, the whole candidate policy (a closed document of ports,
// interface names and closed choices), optionally the application whose own act it is and the
// revert window in seconds. No field is a path, a rule, a unit, a mode or an invoker.
type Request struct {
	Op           string           `json:"op"`
	OperationID  string           `json:"operation_id,omitempty"`
	Candidate    *policy.Document `json:"candidate,omitempty"`
	App          string           `json:"app,omitempty"`
	RevertAfterS int              `json:"revert_after_s,omitempty"`
}

// Subcommand implements helperschema.Request.
func (r *Request) Subcommand() string { return r.Op }

// Operation implements helperschema.Request.
func (r *Request) Operation() string { return r.OperationID }

// Validate implements helperschema.Request.
func (r *Request) Validate() error {
	switch {
	case r.Op == "":
		return refuse("$.op", "required: one of "+strings.Join(Ops(), ", "))
	case !slices.Contains(Ops(), r.Op):
		return refuse("$.op", "expected one of "+strings.Join(Ops(), ", "))
	case r.Op == OpStatus && (r.OperationID != "" || r.Candidate != nil || r.App != "" || r.RevertAfterS != 0):
		return refuse("$", "status changes nothing and carries no other member")
	case r.Op != OpStatus && !operationID(r.OperationID):
		return refuse("$.operation_id", "required: 32 lowercase hexadecimal digits, minted when the operator confirms")
	case r.Op != OpApply && (r.Candidate != nil || r.App != "" || r.RevertAfterS != 0):
		return refuse("$", "only apply carries a candidate, an application or a revert window")
	case r.Op == OpApply && r.Candidate == nil:
		return refuse("$.candidate", "required: the whole candidate policy")
	case r.Op == OpApply && r.App != "" && !policy.AppSlug(r.App):
		return refuse("$.app", "expected an application slug")
	case r.Op == OpApply && r.RevertAfterS != 0 && (r.RevertAfterS < 60 || r.RevertAfterS > 600):
		return refuse("$.revert_after_s", "expected from 60 to 600")
	}
	if r.Op == OpApply {
		if err := r.Candidate.Validate(); err != nil {
			var input *policy.InputError
			if errors.As(err, &input) {
				return refuse("$.candidate"+strings.TrimPrefix(input.Field, "$"), input.Reason)
			}
			return refuse("$.candidate", "outside the policy schema")
		}
	}
	return nil
}

func refuse(field, reason string) error {
	return &helperschema.InputError{Field: field, Reason: reason}
}

// Rules is the helper's admission table. status is a read for the Appliance Console and the
// repair console on tty1. apply and confirm are the Appliance Console's acts; revert is its act
// and the tty1 console's repair verb.
func Rules() []helperschema.Rule {
	console, tty1 := helperschema.Portal, helperschema.RepairConsole
	return []helperschema.Rule{
		{Subcommand: OpStatus, Invokers: []helperschema.Invoker{console, tty1}},
		{Subcommand: OpApply, Mutating: true, Invokers: []helperschema.Invoker{console}},
		{Subcommand: OpConfirm, Mutating: true, Invokers: []helperschema.Invoker{console}},
		{Subcommand: OpRevert, Mutating: true, Invokers: []helperschema.Invoker{console, tty1}},
	}
}
