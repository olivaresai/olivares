// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package console renders the shared host operation API for command-line and
// terminal users. It does not infer authority from the invoking account.
package console

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/portal/localclient"
)

// Session is the common local API used by both terminal surfaces.
type Session interface {
	Descriptors(surface string) ([]hostops.Descriptor, error)
	Operation(id, surface string) (hostops.View, int, error)
	Plan(module, verb, surface string, inputs json.RawMessage) (hostops.Plan, error)
	Description(module, verb, surface string) (hostops.Descriptor, error)
	Close() error
}
type Dial func() (Session, error)
type command struct {
	kind, id, module, verb string
	json                   bool
}

// Run prints a single operation record for --output json. A plan predates
// confirmation and has no operation id, so JSON record output is refused for
// --plan instead of manufacturing an operation. SSH terminal admission belongs
// to the installed login shell, not to a client-provided flag.
func Run(ctx context.Context, args []string, input io.Reader, out, diagnostic io.Writer, dial Dial) int {
	cmd, err := parse(args)
	if err != nil {
		_, _ = fmt.Fprintln(diagnostic, "input_refused")
		return 1
	}
	session, err := dial()
	if err != nil {
		return reportError(diagnostic, err, cmd.json)
	}
	defer session.Close()
	switch cmd.kind {
	case "tui":
		return runTUI(ctx, session, input, out, diagnostic)
	case "plan":
		plan, err := session.Plan(cmd.module, cmd.verb, "cli", json.RawMessage(`{}`))
		if err != nil {
			return reportError(diagnostic, err, cmd.json)
		}
		_, err = fmt.Fprintf(out, "%s\n%s\nCommand: %s\nChanges: disabled (%s)\n", safe(plan.Descriptor.Title), safe(plan.Descriptor.Consequence), safe(plan.Descriptor.EquivalentCommand), safe(plan.Permissions.Code))
		if err != nil {
			return 2
		}
		return 0
	case "get", "watch":
		for {
			view, _, err := session.Operation(cmd.id, "cli")
			if err != nil {
				return reportError(diagnostic, err, cmd.json)
			}
			if cmd.kind == "get" || view.Record.State != hostops.StateRunning {
				if err := printRecord(out, view.Record, cmd.json); err != nil {
					return 2
				}
				if cmd.kind == "watch" && view.Record.State != hostops.StateSucceeded {
					return 2
				}
				return 0
			}
			// Watch repeats only an explicit read, on the same authenticated session.
			// It never reconnects, submits an operation, or converts 202 into success.
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				if cmd.json {
					_ = printRecord(out, view.Record, true)
				}
				return 2
			case <-timer.C:
			}
		}
	default:
		return 1
	}
}

func parse(args []string) (command, error) {
	var c command
	if len(args) == 0 || (len(args) == 1 && args[0] == "tui") {
		return command{kind: "tui"}, nil
	}
	values := append([]string(nil), args...)
	if len(values) >= 2 && values[len(values)-2] == "--output" && values[len(values)-1] == "json" {
		c.json = true
		values = values[:len(values)-2]
	}
	if len(values) == 3 && values[0] == "op" && (values[1] == "get" || values[1] == "watch") && validID(values[2]) {
		c.kind = values[1]
		c.id = values[2]
		return c, nil
	}
	if len(values) == 3 && values[2] == "--plan" && !c.json && simpleName(values[0]) && simpleName(values[1]) {
		c.kind = "plan"
		c.module = values[0]
		c.verb = values[1]
		return c, nil
	}
	return command{}, errors.New("input_refused")
}
func validID(id string) bool {
	if len(id) != 32 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
func simpleName(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for i, c := range s {
		if (c < 'a' || c > 'z') && (i == 0 || c < '0' || c > '9') && (i == 0 || c != '-') {
			return false
		}
	}
	return true
}
func printRecord(w io.Writer, r hostops.Record, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(r)
	}
	_, err := fmt.Fprintf(w, "%s  %s  %s\n%s\n", safe(r.OperationID), safe(r.Target), safe(r.State), safe(r.LogRef))
	return err
}
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return '?'
		}
		return r
	}, s)
}
func reportError(w io.Writer, err error, asJSON bool) int {
	code := "session_lost"
	exit := 2
	var refused *localclient.Refused
	var failure *localclient.Failure
	if errors.As(err, &refused) {
		code = refused.Code
		exit = 1
	} else if errors.As(err, &failure) {
		code = failure.Code
		if failure.Code == "local_unavailable" {
			code += " " + failure.Reason
		}
	}
	_, _ = fmt.Fprintln(w, code)
	if !asJSON && refused != nil && refused.BeforeRequest && refused.Code == "pidfd_unproven" {
		_, _ = fmt.Fprintln(w, "This appliance does not give the portal a proof of the local process's identity, so the local socket cannot be used, not even for reads and plans. Use the web console for reads, plans and changes. Nothing was sent.")
	}
	return exit
}
