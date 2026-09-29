// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import (
	"context"
	"encoding/json"
	"slices"
	"time"
)

// DefaultJobWait bounds how long an effect waits for its job's JobRemoved signal. A job that
// has not reported by then leaves its operation running, outcome unknown.
const DefaultJobWait = 100 * time.Second

// State is what the service manager reports for a unit before or after an act.
type State struct {
	LoadState     string   `json:"load_state,omitempty"`
	ActiveState   string   `json:"active_state,omitempty"`
	SubState      string   `json:"sub_state,omitempty"`
	UnitFileState string   `json:"unit_file_state,omitempty"`
	Dependents    []string `json:"dependents,omitempty"`
	// Measured is false when the state could not be read.
	Measured bool `json:"measured"`
}

// Result is one effect as it happened: its refusal, or the call, the job's result and the state
// measured before and after.
type Result struct {
	Op    string `json:"op"`
	Unit  string `json:"unit"`
	Class Class  `json:"class"`
	// Refusal is set when the effect was refused before any call reached the service manager.
	Refusal *Refusal `json:"refusal,omitempty"`
	// Failure is a closed code set when the service manager did not accept the call.
	Failure string `json:"failure,omitempty"`
	Before  State  `json:"before"`
	After   State  `json:"after"`
	// Job is the object path of the queued job.
	Job string `json:"job,omitempty"`
	// JobResult is the result JobRemoved reported for Job, "" when none arrived in time.
	JobResult string `json:"job_result,omitempty"`
	// Changes counts the unit-file changes of enable and disable.
	Changes int `json:"changes,omitempty"`
}

// Status is the read-only answer for one unit: its class, what the class admits, and its state.
type Status struct {
	Unit        string   `json:"unit"`
	Class       Class    `json:"class"`
	Consequence string   `json:"consequence"`
	Allowed     []string `json:"allowed"`
	State       State    `json:"state"`
}

// Listed is one inventory row with its class.
type Listed struct {
	Unit
	Class Class `json:"class"`
}

// Inventory is the read-only list answer.
type Inventory struct {
	Units     []Listed `json:"units"`
	Truncated bool     `json:"truncated"`
}

// Executor performs the helper's subcommands over Bus, and runs the journal reader through
// Journal.
type Executor struct {
	Bus     Bus
	Journal Runner
	// JobWait bounds the wait for JobRemoved; zero means DefaultJobWait.
	JobWait time.Duration
}

// JobMode is the mode of every queued job: fail, so a conflicting queued job refuses this one
// instead of being replaced by it.
const JobMode = "fail"

// CodeEffectFailed is the failure of a call the service manager did not accept.
const CodeEffectFailed = "effect_failed"

// jobMethods are the manager's methods of the effects that queue a job.
var jobMethods = map[string]string{OpStart: "StartUnit", OpStop: "StopUnit", OpRestart: "RestartUnit", OpReload: "ReloadUnit"}

// jobResults are the results JobRemoved reports.
var jobResults = []string{"done", "canceled", "timeout", "failed", "dependency", "skipped"}

// Bounds of the read answers.
const (
	// maxDependents bounds the dependents a state names.
	maxDependents = 64
	// maxListed bounds the units a list answer names.
	maxListed = 1024
)

// List returns the loaded units and their classes, bounded to maxListed units and to
// MaxAnswerBytes of data.
func (x Executor) List(ctx context.Context) (Inventory, error) {
	rows, err := x.Bus.ListUnitsByPatterns(ctx, []string{}, []string{})
	if err != nil {
		return Inventory{}, err
	}
	inventory := Inventory{Units: []Listed{}}
	size := 0
	for _, row := range rows {
		if !ValidUnitName(row.Name) {
			continue
		}
		listed := Listed{Unit: row, Class: Classify(row.Name)}
		data, err := json.Marshal(listed)
		if err != nil || len(inventory.Units) == maxListed || size+len(data)+1 > MaxAnswerBytes-1024 {
			inventory.Truncated = true
			break
		}
		size += len(data) + 1
		inventory.Units = append(inventory.Units, listed)
	}
	return inventory, nil
}

// Status returns one unit's class, what the class admits and its state. A name that is not a
// unit name, or a unit outside the inventory, is refused.
func (x Executor) Status(ctx context.Context, unit string) (Status, error) {
	if refusal := Check(OpStatus, unit); refusal != nil {
		return Status{}, refusal
	}
	state, present, err := x.state(ctx, unit)
	if err != nil {
		return Status{}, err
	}
	if !present {
		return Status{}, notInInventory()
	}
	class := Classify(unit)
	status := Status{Unit: unit, Class: class, Consequence: class.Consequence(), Allowed: []string{}, State: state}
	for _, op := range Ops() {
		if op != OpList && class.Allows(op) {
			status.Allowed = append(status.Allowed, op)
		}
	}
	return status, nil
}

// Apply performs one effect. A verb outside the effects, a name that is not a unit name, a
// verb the class table refuses and a unit outside the inventory are answered before any act
// reaches the service manager. start, stop, restart and reload subscribe to the manager's job
// signals first, then queue the job with mode fail and take the result of that job's
// JobRemoved; enable and disable change the unit file. The state is measured before and after.
func (x Executor) Apply(ctx context.Context, op, unit string) Result {
	res := Result{Op: op, Unit: unit, Class: Classify(unit)}
	if !IsEffect(op) {
		res.Refusal = &Refusal{Code: CodeInputRefused, Detail: "the verb is not one of this module's effects"}
		return res
	}
	if refusal := Check(op, unit); refusal != nil {
		res.Refusal = refusal
		return res
	}
	before, present, err := x.state(ctx, unit)
	if err != nil {
		res.Failure = CodeEffectFailed
		return res
	}
	if !present {
		res.Refusal = notInInventory()
		return res
	}
	res.Before = before
	if method, ok := jobMethods[op]; ok {
		signals, stop, err := x.Bus.Subscribe(ctx)
		if err != nil {
			res.Failure = CodeEffectFailed
			return res
		}
		defer stop()
		job, err := x.Bus.QueueJob(ctx, method, unit, JobMode)
		if err != nil {
			res.Failure = CodeEffectFailed
			res.After = x.measure(ctx, unit)
			return res
		}
		res.Job = job
		res.JobResult = x.wait(ctx, signals, job)
	} else {
		var changes int
		if op == OpEnable {
			changes, err = x.Bus.EnableUnitFiles(ctx, []string{unit}, false, false)
		} else {
			changes, err = x.Bus.DisableUnitFiles(ctx, []string{unit}, false)
		}
		res.Changes = changes
		if err != nil {
			res.Failure = CodeEffectFailed
		}
	}
	res.After = x.measure(ctx, unit)
	return res
}

// wait returns the result of job's JobRemoved, or "" when none arrives within the bound or
// before ctx ends. Another job's signal is skipped. A result outside systemd's documented set
// is recorded as failed.
func (x Executor) wait(ctx context.Context, signals <-chan JobRemoved, job string) string {
	bound := x.JobWait
	if bound <= 0 {
		bound = DefaultJobWait
	}
	timer := time.NewTimer(bound)
	defer timer.Stop()
	for {
		select {
		case signal, ok := <-signals:
			if !ok {
				return ""
			}
			if signal.Job != job {
				continue
			}
			if !slices.Contains(jobResults, signal.Result) {
				return "failed"
			}
			return signal.Result
		case <-timer.C:
			return ""
		case <-ctx.Done():
			return ""
		}
	}
}

// state reads unit's state: its row in the loaded units, its unit-file state and its
// dependents. present is false when the unit is neither loaded nor has a unit file: it is not in
// the inventory. A unit with a file that is not loaded is inactive.
func (x Executor) state(ctx context.Context, unit string) (State, bool, error) {
	rows, err := x.Bus.ListUnitsByPatterns(ctx, []string{}, []string{unit})
	if err != nil {
		return State{}, false, err
	}
	var s State
	present := false
	for _, row := range rows {
		if row.Name == unit && row.LoadState != "not-found" {
			s.LoadState, s.ActiveState, s.SubState = row.LoadState, row.ActiveState, row.SubState
			present = true
		}
	}
	if fileState, err := x.Bus.GetUnitFileState(ctx, unit); err == nil {
		s.UnitFileState = fileState
		present = true
	}
	if !present {
		return State{}, false, nil
	}
	if s.ActiveState == "" {
		s.ActiveState, s.SubState = "inactive", "dead"
	}
	if dependents, err := x.Bus.Dependents(ctx, unit); err == nil {
		for _, d := range dependents {
			if ValidUnitName(d) && len(s.Dependents) < maxDependents {
				s.Dependents = append(s.Dependents, d)
			}
		}
		slices.Sort(s.Dependents)
	}
	s.Measured = true
	return s, true, nil
}

// measure is state for the after-measurement: a failed or absent reading is unmeasured.
func (x Executor) measure(ctx context.Context, unit string) State {
	s, present, err := x.state(ctx, unit)
	if err != nil || !present {
		return State{}
	}
	return s
}

func notInInventory() *Refusal {
	return &Refusal{Code: CodeInputRefused, Detail: "the unit is not in this host's inventory"}
}
