// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/portal/helperclient"
)

// firewallRestoreAnswer is what the operator types at the network verb's question to restore the
// firewall policy derived from this appliance's answers.
const firewallRestoreAnswer = "firewall-restore"

// firewallRestoreConfirm is the question the firewall restoration's plan ends with.
const firewallRestoreConfirm = "Type the firewall operation id shown above to confirm this restoration, or anything else to ask for nothing:"

// firewallTimeout bounds one request to the firewall's local entry point: a status read runs sshd -T
// once, and a restore loads and reads back one table, twice when it fails.
const firewallTimeout = 30 * time.Second

// FirewallRestorePlan is a firewall restoration as the console showed it to its operator: the operation
// id minted for it and the target digest it showed.
type FirewallRestorePlan struct {
	OperationID  string
	TargetDigest string
}

// PlanFirewallRestore reads the status of the firewall's local entry point and, when a restoration can
// run, returns its plan with the statement to show: the confirmed policy, the target derived from this
// appliance's validated answers with its rows, the application rows it drops and the operation id
// minted for it. Otherwise it returns no plan and says why. Either way nothing but the status is asked.
func (c Console) PlanFirewallRestore() (*FirewallRestorePlan, string) {
	const refused = "firewall-restore: refused: "
	if !c.qualified() {
		return nil, "firewall-restore: " + signInRequired
	}
	if c.helper == nil {
		return nil, "firewall-restore: no helper is wired to this console, so nothing was asked."
	}
	ctx, cancel := context.WithTimeout(context.Background(), firewallTimeout)
	defer cancel()
	response, err := c.helper.Call(ctx, helperschema.HelperFirewallLocal, &helperschema.FirewallLocalRequest{Op: helperschema.FirewallLocalStatus})
	if err != nil || response.Result != helperschema.ResultAnswered {
		return nil, refused + "the firewall's status is unknown" + helperCode(response, err) + ", so nothing was asked."
	}
	var status firewall.LocalStatus
	decoder := json.NewDecoder(bytes.NewReader(response.Bundle))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil {
		return nil, refused + "the firewall's status is not the local entry point's answer, so nothing was asked."
	}
	switch {
	case status.TargetRefusal != "":
		return nil, refused + "the target cannot be derived from this appliance's answers (" + status.TargetRefusal + "), so nothing was asked."
	case len(status.OpenWindows) > 0:
		return nil, refused + "the firewall window " + strings.Join(status.OpenWindows, ", ") + " is open; it is reverted at its deadline " +
			"or by its own revert, and a restoration waits for it, so nothing was asked."
	case status.TargetDigest == "" || status.ConfirmedDigest == "":
		return nil, refused + "the firewall's status names no confirmed policy or no target, so nothing was asked."
	case status.TargetDigest == status.ConfirmedDigest:
		return nil, "firewall-restore: the confirmed policy " + status.ConfirmedDigest + " is already the one derived from this " +
			"appliance's answers, so nothing was asked."
	}
	operation, err := helperschema.NewOperationID(c.randomness())
	if err != nil {
		return nil, refused + "no firewall operation id could be minted, so nothing was asked."
	}
	plan := FirewallRestorePlan{OperationID: operation, TargetDigest: status.TargetDigest}
	dropped := "none"
	if len(status.DroppedAppRows) > 0 {
		var rows []string
		for _, row := range status.DroppedAppRows {
			rows = append(rows, row.App+" "+row.Port+" on "+strings.Join(row.Interfaces, ","))
		}
		dropped = strings.Join(rows, "; ")
	}
	return &plan, "Firewall restore. The confirmed policy is " + status.ConfirmedDigest + ": " + describeRows(status.ConfirmedRows) +
		". The target derived from this appliance's validated answers is " + status.TargetDigest + ": " + describeRows(status.TargetRows) +
		". It drops these application rows, each of which returns through its application's own act: " + dropped +
		". This is firewall operation " + operation + ": the firewall's local entry point records the target before it loads it, " +
		"then loads it, reads it back from the kernel, makes it the confirmed policy that every boot loads and records the result. " +
		"It is not a revert and opens no window."
}

// FirewallRestore asks the firewall's local entry point to restore plan when typed is its operation id,
// after reading the sign-in again and attesting this console's own process as the repair console on
// tty1. Without a qualified sign-in, with any other text, or from another process, it asks nothing. It
// states the owner's recorded result as it came: restored only when the record says so.
func (c Console) FirewallRestore(plan FirewallRestorePlan, typed string) string {
	if !c.qualified() {
		return "firewall-restore: " + signInRequired
	}
	if typed != plan.OperationID {
		return "Nothing was asked of the firewall's local entry point."
	}
	if !c.authority.attestedOnTty1() {
		return "firewall-restore: refused: this console's own process is not attested as the repair console on tty1, so nothing was asked."
	}
	if c.helper == nil {
		return "firewall-restore: no helper is wired to this console, so nothing was asked and nothing was restored."
	}
	ctx, cancel := context.WithTimeout(context.Background(), firewallTimeout)
	defer cancel()
	response, err := c.helper.Call(ctx, helperschema.HelperFirewallLocal,
		&helperschema.FirewallLocalRequest{Op: helperschema.FirewallLocalRestore, OperationID: plan.OperationID})
	var failure *helperclient.Error
	switch {
	case errors.As(err, &failure) && failure.Code == helperschema.CodeConsumerUnavailable:
		return "firewall-restore: the firewall's local entry point is unavailable (" + failure.Reason + "), so nothing was asked and nothing was changed."
	case errors.As(err, &failure) && failure.Code == helperclient.CodeOutcomeUnknown:
		return "firewall-restore: the request for firewall operation " + plan.OperationID + " was sent and no answer came back, so its outcome " +
			"is unknown; its record under that operation id states it."
	case err != nil:
		return "firewall-restore: the request was not sent, so nothing was changed."
	case response.Result == helperschema.ResultRefused:
		return "firewall-restore: the firewall's local entry point refused (" + response.Code + "): " + response.Detail + ". Nothing was loaded."
	}
	var record firewall.Restoration
	decoder := json.NewDecoder(bytes.NewReader(response.Bundle))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || record.OperationID != plan.OperationID {
		return "firewall-restore: the answer (" + response.Result + " " + response.Code + ") carries no record of firewall operation " +
			plan.OperationID + ", so nothing is claimed; its record under that operation id states the outcome."
	}
	if response.Result == helperschema.ResultPerformed && record.State == firewall.RestorationRestored {
		return "firewall-restore: restored: firewall operation " + record.OperationID + " is recorded restored: the target " + record.TargetDigest +
			" replaced " + record.PreviousDigest + ", the kernel was measured holding " + record.MeasuredDigest +
			", and it is the confirmed policy that every boot loads."
	}
	measured := "nothing was measured after it"
	if record.MeasuredDigest != "" {
		measured = "the kernel was measured holding " + record.MeasuredDigest
	}
	return "firewall-restore: failed (" + response.Code + "): firewall operation " + record.OperationID + " is recorded " + record.State +
		" for the target " + record.TargetDigest + "; " + measured + ", and the confirmed policy " + record.PreviousDigest + " was not replaced."
}

// describeRows states each admitted port and where.
func describeRows(rows []policy.MeasuredRow) string {
	if len(rows) == 0 {
		return "no row"
	}
	var said []string
	for _, row := range rows {
		said = append(said, row.Port+" on "+strings.Join(row.Interfaces, ","))
	}
	return strings.Join(said, "; ")
}

// helperCode states a helper's refusal code or the client's, or nothing.
func helperCode(response helperschema.Response, err error) string {
	var failure *helperclient.Error
	switch {
	case errors.As(err, &failure):
		return " (" + failure.Code + ")"
	case err == nil && response.Code != "":
		return " (" + response.Code + ")"
	}
	return ""
}
