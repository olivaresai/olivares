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

	"github.com/olivaresai/olivares/appliance/layer/hostops/console"
	"github.com/olivaresai/olivares/appliance/layer/portal/localclient"
	"github.com/olivaresai/olivares/appliance/layer/services"
)

// serviceCommand is one parsed command line of the grammar
// "service <verb> [<unit>] [--lines <n>] [--plan]": list takes no unit, every other verb takes
// one unit name, and only logs takes --lines, from 1 to 500.
type serviceCommand struct {
	verb, unit string
	lines      int
	plan       bool
}

var errServiceGrammar = errors.New("input_refused")

func parseService(args []string) (serviceCommand, error) {
	var c serviceCommand
	if len(args) == 0 || !slices.Contains(services.Ops(), args[0]) {
		return c, errServiceGrammar
	}
	c.verb, args = args[0], args[1:]
	if c.verb != services.OpList {
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return c, errServiceGrammar
		}
		c.unit, args = args[0], args[1:]
	}
	for len(args) > 0 {
		switch {
		case args[0] == "--plan" && !c.plan:
			c.plan, args = true, args[1:]
		case args[0] == "--lines" && c.verb == services.OpLogs && c.lines == 0 && len(args) > 1:
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 1 || n > services.MaxLogLines || strconv.Itoa(n) != args[1] {
				return c, errServiceGrammar
			}
			c.lines, args = n, args[2:]
		default:
			return c, errServiceGrammar
		}
	}
	return c, nil
}

// runService handles a "service" command line and reports whether args were one. It refuses,
// before dialing, a command line outside the grammar, a verb the unit's class does not admit and
// an act without --plan (the act authorization for the service verb is not carried here). A read
// without --plan, list or status, is the local API's module read through read (readService).
// With --plan it asks the local API for the plan with the unit, and lines for logs, as inputs,
// and prints it: exit 0 for the plan, 1 for a refusal with its closed code, 2 when the session
// could not answer.
func runService(args []string, out, diagnostic io.Writer, dial console.Dial, read readDial) (int, bool) {
	if len(args) == 0 || args[0] != "service" {
		return 0, false
	}
	c, err := parseService(args[1:])
	if err != nil {
		_, _ = fmt.Fprintln(diagnostic, services.CodeInputRefused)
		return 1, true
	}
	if c.verb != services.OpList {
		if refusal := services.Check(c.verb, c.unit); refusal != nil {
			_, _ = fmt.Fprintln(diagnostic, refusal.Code)
			if refusal.Class != "" {
				_, _ = fmt.Fprintf(diagnostic, "Class %s, changed by %s. %s\n", refusal.Class, refusal.Owner, refusal.Detail)
			}
			return 1, true
		}
	}
	if !c.plan {
		if services.IsAct(c.verb) {
			_, _ = fmt.Fprintln(diagnostic, services.CodeActNotAdopted)
			_, _ = fmt.Fprintln(diagnostic, "This act needs an act authorization for the service verb, which this console does not carry. Nothing was sent; --plan shows the plan.")
			return 1, true
		}
		return readService(c, out, diagnostic, read), true
	}
	inputs := map[string]any{}
	if c.unit != "" {
		inputs["unit"] = c.unit
	}
	if c.lines != 0 {
		inputs["lines"] = c.lines
	}
	body, err := json.Marshal(inputs)
	if err != nil {
		_, _ = fmt.Fprintln(diagnostic, services.CodeInputRefused)
		return 1, true
	}
	session, err := dial()
	if err != nil {
		return reportService(diagnostic, err), true
	}
	defer session.Close()
	plan, err := session.Plan(services.Module, c.verb, "cli", body)
	if err != nil {
		return reportService(diagnostic, err), true
	}
	lines := []string{printable(plan.Descriptor.Title), printable(plan.Descriptor.Consequence)}
	if c.unit != "" {
		class := services.Classify(c.unit)
		lines = append(lines, fmt.Sprintf("Unit: %s (class %s): %s", c.unit, class, class.Consequence()))
	}
	command := "olivares-appliance service " + c.verb
	if c.unit != "" {
		command += " " + c.unit
	}
	if c.lines != 0 {
		command += " --lines " + strconv.Itoa(c.lines)
	}
	changes := "Changes: disabled (" + printable(plan.Permissions.Code) + ")"
	if plan.Permissions.Apply {
		changes = "Changes: available after confirmation"
	}
	lines = append(lines, "Command: "+command, changes)
	if _, err := fmt.Fprintln(out, strings.Join(lines, "\n")); err != nil {
		return 2, true
	}
	return 0, true
}

// reportService prints a session's refusal or failure code and returns its exit.
func reportService(w io.Writer, err error) int {
	var refused *localclient.Refused
	var failure *localclient.Failure
	switch {
	case errors.As(err, &refused):
		_, _ = fmt.Fprintln(w, printable(refused.Code))
		return 1
	case errors.As(err, &failure):
		code := failure.Code
		if code == "local_unavailable" {
			code += " " + failure.Reason
		}
		_, _ = fmt.Fprintln(w, printable(code))
		return 2
	}
	_, _ = fmt.Fprintln(w, "session_lost")
	return 2
}

// printable replaces control characters, so text from the local API cannot drive the terminal.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return '?'
		}
		return r
	}, s)
}
