// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// appliance-firstboot applies an appliance's first boot, reports its recorded status,
// reconciles changed answers before the product starts, prints the plan of the answers its
// carriers deliver, and checks that an image root is a clean template.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/olivaresai/olivares/appliance/answers"
	"github.com/olivaresai/olivares/appliance/answers/carriers"
	"github.com/olivaresai/olivares/appliance/layer/base"
)

const usage = "usage: appliance-firstboot apply | status | reconcile | plan | check-template ROOT"

// applyTimeout ends a run before the units' TimeoutStartSec=15min, so a slow run records why
// it stopped instead of being killed.
const applyTimeout = 10 * time.Minute

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 1 && args[0] == "apply":
		return apply(stdout, stderr)
	case len(args) == 1 && args[0] == "status":
		return status(stdout, stderr)
	case len(args) == 1 && args[0] == "reconcile":
		return reconcile(stdout, stderr)
	case len(args) == 1 && args[0] == "plan":
		return plan(stdout, stderr)
	case len(args) == 2 && args[0] == "check-template" && args[1] != "":
		return checkTemplate(args[1], stdout, stderr)
	}
	_, _ = fmt.Fprintln(stderr, usage)
	return 2
}

// resolve reads the installed carriers. Their errors name carriers, references and
// schema paths only.
func resolve(ctx context.Context) (carriers.Delivery, string, error) {
	selected, err := carriers.Selection(carriers.SelectionPath)
	if err != nil {
		return carriers.Delivery{}, "", err
	}
	installed := carriers.Installed(os.Getenv("CREDENTIALS_DIRECTORY"), carriers.ExecRunner)
	d, err := carriers.Resolve(ctx, selected, installed...)
	return d, selected, err
}

// load resolves the carriers and validates the answers they deliver.
func load(ctx context.Context) (base.Input, error) {
	d, selected, err := resolve(ctx)
	switch {
	case errors.Is(err, carriers.ErrNoCarrier):
		return base.Input{}, base.Wait(err.Error())
	case err != nil:
		return base.Input{}, base.Refuse(err.Error())
	}
	return base.NewInput(d.String(), selected, d.Document)
}

// refusedRecord reports a record this version refuses to read: another schema.
func refusedRecord(err error) bool {
	var schema *base.SchemaError
	return errors.As(err, &schema)
}

// apply exits 0 when first boot is ready or pending, 1 when it is refused (a record of
// another schema included), and 2 when it could not run or record its state.
func apply(stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()
	store := base.Store{Dir: base.StateDir}
	unlock, err := store.Lock()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "first boot cannot take its lock:", err)
		return 2
	}
	defer unlock()
	host := base.Host{Root: "/", Run: carriers.ExecRunner}
	m := &base.Machine{
		Store:      store,
		Load:       load,
		Identities: func() ([]string, error) { return base.ProductIdentities("/") },
		Seams: base.Seams{
			Identity:      base.OSIdentity{Host: host},
			HostSettings:  base.CloudInitHost{Host: host},
			ProductConfig: base.ProductConfig{Host: host},
			Storage:       base.Storage{Host: host},
			SetupDelivery: base.RefusingSetupDelivery{},
			Firewall:      base.UnmeasuredFirewall{},
			StartServices: base.ProductService{Host: host},
			Readiness:     base.ProductReadiness{Host: host, Poll: 2 * time.Second},
		},
		Log: stdout,
	}
	rec, err := m.Run(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "first boot cannot record its state:", err)
		if refusedRecord(err) {
			return 1
		}
		return 2
	}
	if rec.State == base.Refused {
		return 1
	}
	return 0
}

// status prints the record and exits 0 only when it says ready, 1 when refused and 2
// otherwise: pending, applying or no record is not a measured ready state.
func status(stdout, stderr io.Writer) int {
	rec, found, err := base.Store{Dir: base.StateDir}.Load()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cannot read the first-boot record:", err)
		if refusedRecord(err) {
			return 1
		}
		return 2
	}
	if !found {
		_, _ = fmt.Fprintln(stderr, "first boot has recorded no state yet")
		return 2
	}
	out, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return 2
	}
	_, _ = fmt.Fprintf(stdout, "%s\n", out)
	switch rec.State {
	case base.Ready:
		return 0
	case base.Refused:
		return 1
	}
	return 2
}

// reconcile recovers from changed answers, or from a recorded effect the host no longer
// matches, before the product starts: it forgets the recorded stages that depend on the
// answers so first boot applies them again. It exits 0 when it reconciled, 1 when refused
// (the product may have started, nothing is recorded, or the record has another schema), and
// 2 when it could not run.
func reconcile(stdout, stderr io.Writer) int {
	store := base.Store{Dir: base.StateDir}
	unlock, err := store.Lock()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "first boot cannot take its lock:", err)
		return 2
	}
	defer unlock()
	code, line := reconcileOutcome((&base.Machine{Store: store, Log: stdout}).Reconcile())
	out := stdout
	if code != 0 {
		out = stderr
	}
	_, _ = fmt.Fprintln(out, line)
	return code
}

// reconcileNext runs first boot again inside its unit: with the credentials the unit imports
// and the unit's hardening, which `appliance-firstboot apply` run from a shell has neither of.
const reconcileNext = "sudo systemctl start olivares-appliance-firstboot.service"

// reconcileOutcome maps Reconcile's result to the exit code and the line the operator reads.
func reconcileOutcome(_ base.Record, err error) (int, string) {
	var outcome *base.Outcome
	switch {
	case refusedRecord(err), errors.As(err, &outcome) && outcome.State == base.Refused:
		return 1, "first boot cannot reconcile: " + err.Error()
	case err != nil:
		return 2, "first boot cannot reconcile: " + err.Error()
	}
	return 0, "first boot reconciled: the stages that depend on the answers apply again; run `" + reconcileNext +
		"` so they run with the unit's credentials and hardening"
}

// plan resolves the carriers read-only and prints the answers module's plan.
func plan(stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d, _, err := resolve(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		if errors.Is(err, carriers.ErrNoCarrier) {
			return 2
		}
		return 1
	}
	p, err := answers.Build(bytes.NewReader(d.Document))
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	out, err := p.JSON()
	if err != nil {
		return 2
	}
	_, _ = fmt.Fprintln(stderr, "answers from", d)
	if _, err := stdout.Write(out); err != nil {
		return 2
	}
	return 0
}

// checkTemplate exits 0 when root holds no instance identity, 1 naming each one it holds.
func checkTemplate(root string, stdout, stderr io.Writer) int {
	found, err := base.TemplateFindings(root)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cannot inspect", root+":", err)
		return 2
	}
	for _, name := range found {
		_, _ = fmt.Fprintln(stdout, "instance identity present:", name)
	}
	if len(found) > 0 {
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "clean template: no instance identity")
	return 0
}
