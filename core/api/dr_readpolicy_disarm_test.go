// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
)

// ReadDualControlRestorePolicy is the reader the CLI restore uses to state, in the
// declaration it SEALS, whether the estate it just replaced required two people. It must
// answer what the console answers about the same estate, and it had NO test at all.
//
// THE DEFECT THIS PINS, found by an adversarial review of the integration batch: the
// function built its schedule from the persisted record copying DisarmAt and NOT DisarmBy.
// dualControlArmed short-circuits to `true` the moment DisarmBy is empty
// (dr_handler.go:208-210) — the correct fail-closed rule for a record that names an instant
// but no person — so this reader answered ARMED for every estate carrying a disarm,
// including one whose disarm took effect hours earlier, while the console answered the
// opposite. Two readers of one state, disagreeing.
//
// Note the direction, because it decides how bad this is. The GATE stays closed, so nothing
// was opened and no restore was let through. What broke is the RECORD: a disarmed estate was
// attested as having required two people. In this repository an evidence artifact that says
// the safe thing when the truth is the other one is worse than no artifact, because the
// safety of the sentence is exactly what stops anyone checking it.
func TestReadDualControlRestorePolicyAgreesWithTheConsoleAfterAnElapsedDisarm(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	h, dir, st, _, clk := drHarnessAt(t, start)
	admin := h.adminLogin()
	enableDualControl(t, h, admin)
	stageUpload(t, dir, "upload-1")

	// ARMED, and both readers must say so. Without this leg, "never armed" would pass the
	// disarmed case below just as well.
	armed, found, err := api.ReadDualControlRestorePolicy(context.Background(), st, clk.Now().Time())
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	if !found || !armed {
		t.Fatalf("with dual control enabled the reader says found=%v armed=%v, want true/true", found, armed)
	}

	// Record a disarm and let the cool-down elapse. Weakening is delayed, so the console
	// only reports it disarmed once the instant has passed.
	putSchedule(t, h, admin, map[string]any{
		"enabled": false, "cron": "", "retain_days": 7,
		"require_dual_control_restore": false,
	})

	// STILL ARMED during the cool-down — the half that keeps the fix honest. A change that
	// simply stopped arming would satisfy the elapsed case and break this one.
	armed, _, err = api.ReadDualControlRestorePolicy(context.Background(), st, clk.Now().Time())
	if err != nil {
		t.Fatalf("read policy during cool-down: %v", err)
	}
	if !armed {
		t.Fatalf("a PENDING disarm already reads as disarmed: the cool-down is not being applied")
	}

	clk.advance(2 * time.Hour)

	// The console's answer, which is the truth this reader has to match.
	r := h.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.body["require_dual_control_restore"] != false {
		t.Fatalf("the console still reports the gate armed after the cool-down: %s", r.raw)
	}

	armed, found, err = api.ReadDualControlRestorePolicy(context.Background(), st, clk.Now().Time())
	if err != nil {
		t.Fatalf("read policy after the cool-down: %v", err)
	}
	if !found {
		t.Fatalf("the estate has a schedule; found=false")
	}
	if armed {
		t.Fatalf("DISAGREEMENT: the console says the gate is DISARMED and the reader that seals " +
			"the CLI restore declaration says it was ARMED — the declaration would attest two " +
			"people for a restore that needed one")
	}
}
