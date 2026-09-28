// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
	"github.com/olivaresai/olivares/appliance/layer/hostops/console"
	"github.com/olivaresai/olivares/appliance/layer/portal/localclient"
	"github.com/olivaresai/olivares/appliance/layer/portal/localsession"
)

// firewallCommand is one parsed command line of the grammar
//
//	firewall show
//	firewall plan [--revert-after <s>]
//	firewall apply [--revert-after <s>] [--plan]
//	firewall confirm <operation> [--plan]
//	firewall revert <operation> [--plan]
//	firewall app-row <app> add|remove [--revert-after <s>] [--plan]
//
// plan is apply --plan. An operation is 32 lowercase hexadecimal digits, an app is an
// application slug and a revert window is from 60 to 600 seconds.
type firewallCommand struct {
	verb, operation, app, change string
	revertAfter                  int
	plan                         bool
}

var errFirewallGrammar = errors.New("input_refused")

func parseFirewall(args []string) (firewallCommand, error) {
	var c firewallCommand
	if len(args) == 0 || !slices.Contains([]string{"show", "plan", firewall.VerbApply, firewall.VerbConfirm, firewall.VerbRevert, firewall.VerbAppRow}, args[0]) {
		return c, errFirewallGrammar
	}
	c.verb, args = args[0], args[1:]
	switch c.verb {
	case firewall.VerbConfirm, firewall.VerbRevert:
		if len(args) == 0 || !operationID(args[0]) {
			return c, errFirewallGrammar
		}
		c.operation, args = args[0], args[1:]
	case firewall.VerbAppRow:
		if len(args) < 2 || !policy.AppSlug(args[0]) || (args[1] != "add" && args[1] != "remove") {
			return c, errFirewallGrammar
		}
		c.app, c.change, args = args[0], args[1], args[2:]
	}
	windowed := c.verb == "plan" || c.verb == firewall.VerbApply || c.verb == firewall.VerbAppRow
	for len(args) > 0 {
		switch {
		case args[0] == "--plan" && !c.plan && c.verb != "show" && c.verb != "plan":
			c.plan, args = true, args[1:]
		case args[0] == "--revert-after" && windowed && c.revertAfter == 0 && len(args) > 1:
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 60 || n > 600 || strconv.Itoa(n) != args[1] {
				return c, errFirewallGrammar
			}
			c.revertAfter, args = n, args[2:]
		default:
			return c, errFirewallGrammar
		}
	}
	if c.verb == "plan" {
		c.verb, c.plan = firewall.VerbApply, true
	}
	return c, nil
}

// runFirewall handles a "firewall" command line and reports whether args were one. show reads
// firewall.status through the local API's module read; it exits 0 whether the firewall is measured
// or not, and says which, 1 for a refusal with its closed code and 2 when the read could not run.
// Every act is refused before dialing without --plan, because this console carries no act
// authorization for the network verb. With --plan it asks the local API for the plan with the
// closed inputs and prints it: exit 0 for the plan, 1 for a refusal with its closed code, 2 when
// the session could not answer.
func runFirewall(args []string, out, diagnostic io.Writer, dial console.Dial, read readDial) (int, bool) {
	if len(args) == 0 || args[0] != "firewall" {
		return 0, false
	}
	c, err := parseFirewall(args[1:])
	if err != nil {
		_, _ = fmt.Fprintln(diagnostic, firewall.CodeInputRefused)
		return 1, true
	}
	if c.verb == "show" {
		return showFirewall(out, diagnostic, read), true
	}
	if !c.plan {
		_, _ = fmt.Fprintln(diagnostic, firewall.CodeActNotAdopted)
		_, _ = fmt.Fprintln(diagnostic, "This act needs an act authorization for the network verb, which this console does not carry. Nothing was sent; --plan shows the plan.")
		return 1, true
	}
	inputs := map[string]any{}
	command := "olivares-appliance firewall " + c.verb
	switch c.verb {
	case firewall.VerbConfirm, firewall.VerbRevert:
		inputs["operation"] = c.operation
		command += " " + c.operation
	case firewall.VerbAppRow:
		inputs["app"], inputs["change"] = c.app, c.change
		command += " " + c.app + " " + c.change
	}
	if c.revertAfter != 0 {
		inputs["revert_after_s"] = c.revertAfter
		command += " --revert-after " + strconv.Itoa(c.revertAfter)
	}
	body, err := json.Marshal(inputs)
	if err != nil {
		_, _ = fmt.Fprintln(diagnostic, firewall.CodeInputRefused)
		return 1, true
	}
	session, err := dial()
	if err != nil {
		return reportService(diagnostic, err), true
	}
	defer session.Close()
	plan, err := session.Plan(firewall.Module, c.verb, "cli", body)
	if err != nil {
		return reportService(diagnostic, err), true
	}
	changes := "Changes: disabled (" + printable(plan.Permissions.Code) + ")"
	if plan.Permissions.Apply {
		changes = "Changes: available after confirmation"
	}
	lines := []string{printable(plan.Descriptor.Title), printable(plan.Descriptor.Consequence), "Command: " + command, changes}
	if _, err := fmt.Fprintln(out, strings.Join(lines, "\n")); err != nil {
		return 2, true
	}
	return 0, true
}

// showFirewall reads firewall.status, every page of one read of the console's, and prints the
// measured policy and its ports, or why it is unmeasured, then the confirmed policy and each window.
func showFirewall(out, diagnostic io.Writer, read readDial) int {
	session, err := read()
	if err != nil {
		return reportRead(diagnostic, err)
	}
	defer session.Close()
	answer, err := readPages(session, localsession.QueryFirewallStatus)
	if err == nil && answer.Firewall == nil {
		err = &localclient.Failure{Code: "response_unverified", Reason: "record_malformed", Sent: true}
	}
	if err != nil {
		return reportRead(diagnostic, err)
	}
	head := answer.Firewall
	lines := []string{"Firewall: unmeasured (" + printable(head.Unmeasured) + "); the console stays on loopback."}
	if head.Unmeasured == "" {
		lines = []string{fmt.Sprintf("Firewall policy %s, measured at %s in boot %s; input %s for what no row admits.",
			printable(head.PolicyDigest), printable(head.MeasuredAt), printable(head.BootID), printable(head.InputPolicy))}
		for _, row := range answer.FirewallRows {
			if row.Kind != localsession.FirewallPort {
				continue
			}
			where := strings.Join(row.Interfaces, ", ")
			if len(row.Interfaces) == 1 && row.Interfaces[0] == policy.EveryInterface {
				where = "every interface"
			}
			lines = append(lines, printable(row.Port+" on "+where))
		}
		lines = append(lines, "Always kept: established and related replies, loopback, and IPv6 neighbor and router discovery.")
	}
	if head.ConfirmedDigest == "" {
		lines = append(lines, "No policy is confirmed yet.")
	} else {
		lines = append(lines, "Confirmed policy "+printable(head.ConfirmedDigest)+".")
	}
	for _, row := range answer.FirewallRows {
		switch {
		case row.Kind != localsession.FirewallWindow:
		case row.State == firewall.WindowPending:
			lines = append(lines, "Window "+printable(row.OperationID)+": pending, candidate "+printable(row.CandidateDigest)+"; it reverts unless confirmed.")
		case row.Reason != "":
			lines = append(lines, "Window "+printable(row.OperationID)+": "+printable(row.State)+" ("+printable(row.Reason)+").")
		default:
			lines = append(lines, "Window "+printable(row.OperationID)+": "+printable(row.State)+".")
		}
	}
	lines = append(lines, "As read at "+printable(answer.ReadAt)+"; this read changed nothing.")
	if _, err := fmt.Fprintln(out, strings.Join(lines, "\n")); err != nil {
		return 2
	}
	return 0
}

// operationID reports whether s has an operation id's shape: 32 lowercase hexadecimal digits.
func operationID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if (s[i] < '0' || s[i] > '9') && (s[i] < 'a' || s[i] > 'f') {
			return false
		}
	}
	return true
}
