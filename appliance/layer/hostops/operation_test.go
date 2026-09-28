// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
)

func TestOperation_SameIDIsARetrievalNeverARerun(t *testing.T) {
	dir := t.TempDir()
	engine, err := hostops.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	first := testCommand(strings.Repeat("ab", 16))
	var runs int
	got, _, err := engine.Submit(first, func() error {
		runs++
		return nil
	})
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if runs != 1 {
		t.Fatalf("first submit ran the effect %d times, want 1", runs)
	}
	if got.OperationID != first.OperationID || got.PlanDigest != first.PlanDigest || got.Target != first.Target {
		t.Fatalf("recorded %+v, want the submitted operation", got)
	}
	if got.LogRef != "journal:OLIVARES_OPERATION_ID="+first.OperationID {
		t.Fatalf("log ref %q", got.LogRef)
	}

	resent := first
	resent.Surface = "cli"
	again, _, err := engine.Submit(resent, func() error {
		runs++
		return nil
	})
	if err != nil {
		t.Fatalf("resend: %v", err)
	}
	if runs != 1 {
		t.Fatalf("resend ran the effect again: %d runs", runs)
	}
	if !reflect.DeepEqual(again, got) {
		t.Fatalf("resend returned %+v, recorded %+v", again, got)
	}

	reopened, err := hostops.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	var runsAfter int
	fromDisk, _, err := reopened.Submit(resent, func() error {
		runsAfter++
		return nil
	})
	if err != nil {
		t.Fatalf("submit after reopen: %v", err)
	}
	if runsAfter != 0 || !reflect.DeepEqual(fromDisk, got) {
		t.Fatalf("reopen ran %d times and returned %+v, want the recorded operation and no effect", runsAfter, fromDisk)
	}
}

func TestOperation_AcceptedIsRunningNeverSucceeded(t *testing.T) {
	engine, err := hostops.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cmd := testCommand(strings.Repeat("ab", 16))
	var runs int
	got, status, err := engine.Submit(cmd, func() error {
		runs++
		return nil
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if runs != 1 {
		t.Fatalf("accept ran the effect %d times", runs)
	}
	if got.State != hostops.StateRunning || got.Outcome != hostops.OutcomeUnknown || status != 202 {
		t.Fatalf("accepted state %q outcome %q status %d, want running, unknown, 202", got.State, got.Outcome, status)
	}
	if got.State == hostops.StateSucceeded {
		t.Fatal("accept marked the operation succeeded")
	}
	again, statusAgain, err := engine.Submit(cmd, func() error {
		runs++
		return nil
	})
	if err != nil {
		t.Fatalf("resend: %v", err)
	}
	if runs != 1 || !reflect.DeepEqual(again, got) || statusAgain != 202 {
		t.Fatalf("resend runs %d status %d record %+v, want the running record and one effect", runs, statusAgain, again)
	}
}

func TestOperation_LostResponseIsReadBackByID(t *testing.T) {
	dir := t.TempDir()
	engine, err := hostops.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cmd := testCommand(strings.Repeat("ab", 16))
	var runs int
	got, status, err := engine.Submit(cmd, func() error {
		runs++
		return nil
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	// The response is discarded. The next step is a read, not another submit.
	read, readStatus, err := engine.Get(cmd.OperationID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !reflect.DeepEqual(read, got) || readStatus != status || readStatus != 202 || runs != 1 {
		t.Fatalf("read back %+v status %d runs %d, want the recorded running operation", read, readStatus, runs)
	}
	reopened, err := hostops.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	fromDisk, _, err := reopened.Get(cmd.OperationID)
	if err != nil || !reflect.DeepEqual(fromDisk, got) || runs != 1 {
		t.Fatalf("reopen read %+v err %v", fromDisk, err)
	}
	if _, _, err := engine.Submit(cmd, func() error {
		runs++
		return nil
	}); err != nil || runs != 1 {
		t.Fatalf("submit after the lost response ran %d times, err %v", runs, err)
	}
	if _, _, err := engine.Get(strings.Repeat("99", 16)); err == nil {
		t.Fatal("an unknown id was answered as an operation")
	}
	_, _, err = engine.Get("../" + strings.Repeat("ab", 16))
	var refused *hostops.InputRefused
	if !errors.As(err, &refused) || strings.Contains(err.Error(), "..") {
		t.Fatalf("bad id err = %v, want a refusal that does not repeat the value", err)
	}
}

func TestOperation_RolledBackNeedsPostconditions(t *testing.T) {
	engine, err := hostops.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cmd := testCommand(strings.Repeat("ab", 16))
	var runs int
	if _, _, err := engine.Submit(cmd, func() error {
		runs++
		return nil
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	without := hostops.Observation{
		P1:             "performed",
		MeasuredChange: true,
		RevertDefined:  true,
		RevertMeasured: true,
	}
	got, err := engine.Recover(cmd.OperationID, without)
	if err != nil {
		t.Fatalf("recover without postconditions: %v", err)
	}
	if got.State == hostops.StateRolledBack {
		t.Fatal("rolled back without postconditions")
	}
	if got.State != hostops.StatePartial || runs != 1 {
		t.Fatalf("state %s runs %d, want partial and no rerun", got.State, runs)
	}
	with := without
	with.Postconditions = []string{"previous address restored"}
	rollback := testCommand(strings.Repeat("13", 16))
	if _, _, err := engine.Submit(rollback, nil); err != nil {
		t.Fatal(err)
	}
	got, err = engine.Recover(rollback.OperationID, with)
	if err != nil || got.State != hostops.StateRolledBack || runs != 1 {
		t.Fatalf("state %s runs %d err %v, want rolled_back and no rerun", got.State, runs, err)
	}
	if len(got.Postconditions) != 1 || got.Postconditions[0] != "previous address restored" {
		t.Fatalf("postconditions %#v", got.Postconditions)
	}
	next := testCommand(strings.Repeat("12", 16))
	var nextRuns int
	accepted, status, err := engine.Submit(next, func() error {
		nextRuns++
		return nil
	})
	if err != nil || nextRuns != 1 || accepted.State != hostops.StateRunning || status != 202 {
		t.Fatalf("after rollback: state %s status %d runs %d err %v", accepted.State, status, nextRuns, err)
	}

	cases := []struct {
		name    string
		obs     hostops.Observation
		state   string
		outcome string
	}{
		{name: "performed with postconditions", obs: hostops.Observation{P1: "performed", Postconditions: []string{"listener answers"}}, state: hostops.StateSucceeded},
		{name: "performed without postconditions", obs: hostops.Observation{P1: "performed", MeasuredChange: true}, state: hostops.StatePartial},
		{name: "not performed", obs: hostops.Observation{P1: "not-performed"}, state: hostops.StateFailed},
		{name: "failed with a measured change", obs: hostops.Observation{P1: "failed", MeasuredChange: true}, state: hostops.StatePartial},
		{name: "failed with no change", obs: hostops.Observation{P1: "failed"}, state: hostops.StateFailed},
		{name: "unknown", obs: hostops.Observation{P1: "unknown"}, state: hostops.StateRunning, outcome: hostops.OutcomeUnknown},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := strings.Repeat([]string{"01", "02", "03", "04", "05", "06"}[i], 16)
			op := testCommand(id)
			op.Target = "unit:case" + string(rune('a'+i))
			var caseRuns int
			if _, _, err := engine.Submit(op, func() error {
				caseRuns++
				return nil
			}); err != nil {
				t.Fatalf("submit: %v", err)
			}
			got, err := engine.Recover(id, tc.obs)
			if err != nil {
				t.Fatalf("recover: %v", err)
			}
			if got.State != tc.state || got.Outcome != tc.outcome || caseRuns != 1 {
				t.Fatalf("state %q outcome %q runs %d, want %q %q and one effect", got.State, got.Outcome, caseRuns, tc.state, tc.outcome)
			}
		})
	}
}

func TestOperation_DifferentIDOnALockedTargetIs409NamingTheOpenOne(t *testing.T) {
	dir := t.TempDir()
	engine, err := hostops.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	first := testCommand(strings.Repeat("ab", 16))
	var firstRuns int
	openRec, _, err := engine.Submit(first, func() error {
		firstRuns++
		return nil
	})
	if err != nil {
		t.Fatalf("open operation: %v", err)
	}

	second := testCommand(strings.Repeat("12", 16))
	second.Target = first.Target
	var secondRuns int
	_, status, err := engine.Submit(second, func() error {
		secondRuns++
		return nil
	})
	var locked *hostops.TargetLocked
	if !errors.As(err, &locked) {
		t.Fatalf("second submit err = %v, want the target lock", err)
	}
	if status != 409 || locked.Error() != "409 target_locked" {
		t.Fatalf("status %d, refusal %q", status, locked.Error())
	}
	if locked.OperationID != openRec.OperationID || locked.State != openRec.State {
		t.Fatalf("refusal names %s in %s, open operation is %s in %s", locked.OperationID, locked.State, openRec.OperationID, openRec.State)
	}
	if firstRuns != 1 || secondRuns != 0 {
		t.Fatalf("effect runs: open %d, refused %d", firstRuns, secondRuns)
	}

	other := testCommand(strings.Repeat("34", 16))
	other.Target = "packages"
	var otherRuns int
	if _, _, err := engine.Submit(other, func() error {
		otherRuns++
		return nil
	}); err != nil || otherRuns != 1 {
		t.Fatalf("a free target ran %d times, err %v", otherRuns, err)
	}

	life := t.TempDir()
	if err := hostops.InitLifecycle(life); err != nil {
		t.Fatalf("init lifecycle: %v", err)
	}
	lockPath := filepath.Join(life, "lifecycle.lock")
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := hostops.InitLifecycle(life); err != nil {
		t.Fatalf("init lifecycle again: %v", err)
	}
	after, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("the lifecycle lock was replaced")
	}
	if after.Mode().Perm() != 0o640 {
		t.Fatalf("lifecycle lock mode %o, want 0640", after.Mode().Perm())
	}
	portal, err := hostops.OpenLifecycle(life, hostops.RolePortal)
	if err != nil {
		t.Fatalf("open lifecycle: %v", err)
	}
	if err := portal.Exclusive(); err == nil {
		t.Fatal("the portal took an exclusive lifecycle lock")
	}
	if err := portal.Shared(); err != nil {
		t.Fatalf("shared lifecycle lock: %v", err)
	}
	if portal.CanWrite() {
		t.Fatal("the portal can write the lifecycle lock")
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lifecycle lock after the portal opened it: %v", err)
	}
}

func testCommand(id string) hostops.Command {
	return hostops.Command{
		OperationID: id,
		TaskID:      "host.operation.get",
		Module:      "host",
		Verb:        "status",
		Target:      "system",
		PlanDigest:  strings.Repeat("cd", 32),
		Mode:        "product-up",
		Surface:     "web",
		Actor:       "operator",
	}
}

func TestOperation_ExistingIDRefusesChangedPlanIdentity(t *testing.T) {
	for name, change := range map[string]func(*hostops.Command){
		"digest": func(c *hostops.Command) { c.PlanDigest = strings.Repeat("ef", 32) },
		"module": func(c *hostops.Command) { c.Module = "services" },
		"verb":   func(c *hostops.Command) { c.Verb = "restart" },
		"target": func(c *hostops.Command) { c.Target = "packages" },
		"mode":   func(c *hostops.Command) { c.Mode = "timer" },
		"actor":  func(c *hostops.Command) { c.Actor = "another-user" },
		"task":   func(c *hostops.Command) { c.TaskID = "another.task" },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			e, err := hostops.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			cmd := testCommand(strings.Repeat("c5", 16))
			first, _, err := e.Submit(cmd, nil)
			if err != nil {
				t.Fatal(err)
			}
			e, err = hostops.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			resent := cmd
			change(&resent)
			_, status, err := e.Submit(resent, func() error { t.Fatal("changed plan ran"); return nil })
			if status != 409 || err == nil || err.Error() != "409 plan_changed" {
				t.Fatalf("changed plan: %d %v", status, err)
			}
			got, _, err := e.Get(cmd.OperationID)
			if err != nil || !reflect.DeepEqual(got, first) {
				t.Fatalf("original changed: %#v %v", got, err)
			}
			resent = cmd
			resent.Surface = "tui"
			got, _, err = e.Submit(resent, func() error { t.Fatal("retrieval replayed"); return nil })
			if err != nil || !reflect.DeepEqual(got, first) {
				t.Fatalf("same plan: %#v %v", got, err)
			}
		})
	}
}
