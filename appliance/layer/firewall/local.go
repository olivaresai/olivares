// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall

import (
	"context"
	"errors"
	"slices"

	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// CodeRestorationInterrupted answers a restoration whose record an earlier call left pending: that
// call ended before it recorded a result, so this one loads nothing and claims nothing. The confirmed
// policy and the measurement state what holds; a new operation id restores again.
const CodeRestorationInterrupted = "restoration_interrupted"

// LocalStatus is the local entry point's status answer: the confirmed policy's digest and rows, the
// open windows, and the target a restoration would load now with the application rows it drops, or
// the closed code that refuses deriving it.
type LocalStatus struct {
	ConfirmedDigest string               `json:"confirmed_digest"`
	ConfirmedRows   []policy.MeasuredRow `json:"confirmed_rows"`
	OpenWindows     []string             `json:"open_windows"`
	TargetDigest    string               `json:"target_digest"`
	TargetRows      []policy.MeasuredRow `json:"target_rows"`
	DroppedAppRows  []policy.AppRow      `json:"dropped_app_rows"`
	TargetRefusal   string               `json:"target_refusal,omitempty"`
}

// LocalAnswer answers one admitted document of the local entry point, olivares-portal-firewall-local,
// through the owner. status reads the owner's state and derives the target, changing nothing; revert
// is the owner's Revert of an unconfirmed window; restore derives the target from answers on this host
// and asks the owner's Restore. A target that cannot be derived refuses with its closed code before
// the owner is asked, so nothing is loaded or recorded. A restoration is performed only when its
// record says restored.
func LocalAnswer(ctx context.Context, o *Owner, answers Answers, r helperschema.FirewallLocalRequest) helperschema.Response {
	switch r.Op {
	case helperschema.FirewallLocalStatus:
		return localStatus(ctx, o, answers)
	case helperschema.FirewallLocalRevert:
		return windowAnswer(o.Revert(ctx, r.OperationID))
	case helperschema.FirewallLocalRestore:
		target, err := answers.Target(ctx)
		if err != nil {
			return restorationAnswer(Restoration{}, err)
		}
		return restorationAnswer(o.Restore(ctx, r.OperationID, target))
	}
	return helperschema.Response{Result: helperschema.ResultRefused, Code: helperschema.CodeInputRefused, Detail: "not a subcommand of this helper"}
}

// localStatus answers the status read. Every list is a list, never null.
func localStatus(ctx context.Context, o *Owner, answers Answers) helperschema.Response {
	confirmed, ok, err := o.Confirmed()
	if err != nil {
		return failed("the confirmed policy cannot be read")
	}
	windows, err := o.Windows()
	if err != nil {
		return failed("the windows cannot be read")
	}
	status := LocalStatus{ConfirmedRows: []policy.MeasuredRow{}, OpenWindows: []string{}, TargetRows: []policy.MeasuredRow{},
		DroppedAppRows: []policy.AppRow{}}
	if ok {
		status.ConfirmedDigest = policy.Digest(confirmed)
		status.ConfirmedRows = append(status.ConfirmedRows, policy.Rows(confirmed)...)
	}
	for _, w := range windows {
		if w.State == WindowPending {
			status.OpenWindows = append(status.OpenWindows, w.OperationID)
		}
	}
	target, err := answers.Target(ctx)
	if err != nil {
		status.TargetRefusal = codeOf(err)
		return withBundle(helperschema.Response{Result: helperschema.ResultAnswered}, status)
	}
	status.TargetDigest = policy.Digest(target)
	status.TargetRows = append(status.TargetRows, policy.Rows(target)...)
	if ok {
		status.DroppedAppRows = append(status.DroppedAppRows, slices.Clone(confirmed.Apps)...)
	}
	return withBundle(helperschema.Response{Result: helperschema.ResultAnswered}, status)
}

// restorationAnswer answers a restoration from its record: performed only when the record says
// restored; failed with the recorded reason when it says failed; failed as interrupted when an earlier
// call left it pending. A refusal before any effect is refused with the owner's closed code, and a
// load, read-back or record that failed is failed.
func restorationAnswer(r Restoration, err error) helperschema.Response {
	response := helperschema.Response{Result: helperschema.ResultPerformed}
	switch {
	case err != nil:
		response = helperschema.Response{Result: helperschema.ResultFailed, Code: helperschema.CodeEffectFailed, Detail: "the firewall owner failed"}
		var refusal *Refusal
		if errors.As(err, &refusal) {
			response.Code, response.Detail = refusal.Code, refusal.Detail
			switch refusal.Code {
			case CodeLoadFailed, CodeMeasurementFailed, CodeStateUnreadable:
			default:
				response.Result = helperschema.ResultRefused
			}
		}
	case r.State == RestorationFailed:
		response = helperschema.Response{Result: helperschema.ResultFailed, Code: r.Reason,
			Detail: "this operation's restoration failed and is recorded; its record states what was measured"}
	case r.State != RestorationRestored:
		response = helperschema.Response{Result: helperschema.ResultFailed, Code: CodeRestorationInterrupted,
			Detail: "an earlier call with this operation id ended before it recorded a result; nothing was loaded now"}
	}
	if r.OperationID == "" {
		return response
	}
	return withBundle(response, r)
}
