// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"context"
	"errors"
	"testing"
)

func TestStageMachine_ReconcileReappliesTheAnswerStagesUntilTheProductMayHaveStarted(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")
	corrected := answersFixture(t, "corrected.example.test")
	crashAfter := func(stage Stage) (string, *fakeHost) {
		dir, h := t.TempDir(), newFakeHost()
		m := newMachine(dir, h, &in, h.seams())
		m.crash = func(s Stage, b boundary) bool { return s == stage && b == afterPersist }
		if _, err := m.Run(context.Background()); !errors.Is(err, errCrashed) {
			t.Fatalf("first run: %v", err)
		}
		return dir, h
	}

	// Before the product starts, the stages that depend on the answers apply again.
	dir, h := crashAfter(StageProductConfig)
	rec, err := newMachine(dir, h, &corrected, h.seams()).Reconcile()
	if err != nil || rec.State != Pending || rec.Digest != "" || len(rec.Completed) != 1 || rec.Completed[0].Stage != StageIdentity {
		t.Fatalf("reconcile: %+v %v", rec, err)
	}
	rec, err = newMachine(dir, h, &corrected, h.seams()).Run(context.Background())
	if err != nil || rec.State != Ready || rec.Digest != corrected.Digest {
		t.Fatalf("run after reconcile: %+v %v", rec, err)
	}
	if h.applies[StageIdentity] != 1 || h.applies[StageHostSettings] != 2 || h.applies[StageProductConfig] != 2 {
		t.Fatalf("applies after reconcile: %v", h.applies)
	}

	// A recorded effect the host no longer matches refuses at its stage; reconcile applies it again.
	dir, h = crashAfter(StageStorage)
	h.effects[StageProductConfig] = "edited by hand"
	rec, err = newMachine(dir, h, &in, h.seams()).Run(context.Background())
	if err != nil || rec.State != Refused || rec.Stage != StageProductConfig {
		t.Fatalf("drifted configuration: %+v %v", rec, err)
	}
	if _, err := newMachine(dir, h, &in, h.seams()).Reconcile(); err != nil {
		t.Fatalf("reconcile after drift: %v", err)
	}
	rec, err = newMachine(dir, h, &in, h.seams()).Run(context.Background())
	if err != nil || rec.State != Ready || h.applies[StageProductConfig] != 2 || h.applies[StageIdentity] != 1 {
		t.Fatalf("run after reconciling drift: %+v %v %v", rec, err, h.applies)
	}

	// Once the product may have started, reconcile refuses and changes nothing.
	dir, h = crashAfter(StageStartServices)
	before, _, _ := Store{Dir: dir}.Load()
	_, err = newMachine(dir, h, &corrected, h.seams()).Reconcile()
	after, _, _ := Store{Dir: dir}.Load()
	var outcome *Outcome
	if !errors.As(err, &outcome) || outcome.State != Refused || len(after.Completed) != len(before.Completed) || after.Digest != before.Digest {
		t.Fatalf("reconcile after the product start: %v %+v", err, after)
	}

	// Nothing recorded: nothing to reconcile.
	empty := newFakeHost()
	if _, err := newMachine(t.TempDir(), empty, &corrected, empty.seams()).Reconcile(); !errors.As(err, &outcome) {
		t.Fatalf("an empty record: %v", err)
	}
}
