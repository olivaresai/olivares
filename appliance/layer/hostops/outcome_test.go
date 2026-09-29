// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
)

func TestOperation_EveryReceiptOutcomeHasItsStateAndTargetCustody(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		obs                    hostops.Observation
		state, outcome, reason string
		refused                bool
	}{
		{"performed measured", hostops.Observation{P1: "performed", Postconditions: []string{"measured"}}, "succeeded", "", "", false},
		{"performed unmeasured", hostops.Observation{P1: "performed"}, "partial", "", "", false},
		{"partial", hostops.Observation{P1: "partial", PendingEffect: true}, "partial", "", "", false},
		{"not performed", hostops.Observation{P1: "not-performed"}, "failed", "", "", false},
		{"failed unchanged", hostops.Observation{P1: "failed"}, "failed", "", "", false},
		{"failed changed", hostops.Observation{P1: "failed", MeasuredChange: true}, "partial", "", "", false},
		{"abandoned unchanged", hostops.Observation{P1: "abandoned", PendingEffect: true}, "failed", "", "abandoned", false},
		{"abandoned changed", hostops.Observation{P1: "abandoned", MeasuredChange: true, PendingEffect: true}, "partial", "", "abandoned", false},
		{"unknown", hostops.Observation{P1: "unknown", Postconditions: []string{"observation alone"}}, "running", "unknown", "", false},
		{"outside vocabulary", hostops.Observation{P1: "invented"}, "running", "unknown", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			e, err := hostops.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			cmd := testCommand(strings.Repeat("a4", 16))
			cmd.Target = "packages"
			if _, _, err := e.Submit(cmd, nil); err != nil {
				t.Fatal(err)
			}
			_, err = e.Recover(cmd.OperationID, tc.obs)
			var refused *hostops.InputRefused
			if tc.refused != errors.As(err, &refused) || (!tc.refused && err != nil) {
				t.Fatalf("recover error %v, refused=%v", err, tc.refused)
			}
			reopened, err := hostops.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := reopened.Get(cmd.OperationID)
			if err != nil || got.State != tc.state || got.Outcome != tc.outcome || got.EffectPending {
				t.Fatalf("record %#v, error %v", got, err)
			}
			data, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			if tc.reason != "" && fields["reason"] != tc.reason {
				t.Fatalf("reason missing: %s", data)
			}
			if tc.reason == "" && fields["reason"] != nil {
				t.Fatalf("unexpected reason: %s", data)
			}
			next := cmd
			next.OperationID = strings.Repeat("b4", 16)
			runs := 0
			_, status, err := reopened.Submit(next, func() error { runs++; return nil })
			if tc.state == "running" {
				var locked *hostops.TargetLocked
				if status != 409 || !errors.As(err, &locked) || runs != 0 {
					t.Fatalf("target lost: %d %v %d", status, err, runs)
				}
			} else if err != nil || status != 202 || runs != 1 {
				t.Fatalf("terminal target retained: %d %v %d", status, err, runs)
			}
		})
	}
}

func TestOperation_TerminalPartialCannotBecomeSuccess(t *testing.T) {
	e, err := hostops.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := testCommand(strings.Repeat("a5", 16))
	if _, _, err := e.Submit(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Recover(cmd.OperationID, hostops.Observation{P1: "performed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Recover(cmd.OperationID, hostops.Observation{P1: "performed", Postconditions: []string{"later measurement"}}); err == nil {
		t.Fatal("terminal partial was rewritten as success")
	}
}

func TestOperation_DefiniteReceiptPrecedesPendingAndRevertMeasurements(t *testing.T) {
	for _, tc := range []struct {
		name           string
		obs            hostops.Observation
		state, outcome string
	}{
		{"unknown pending", hostops.Observation{P1: "unknown", PendingEffect: true}, "running", "unknown"},
		{"unknown reverted", hostops.Observation{P1: "unknown", RevertDefined: true, RevertMeasured: true, Postconditions: []string{"revert measured"}}, "running", "unknown"},
		{"unknown pending reverted", hostops.Observation{P1: "unknown", PendingEffect: true, RevertDefined: true, RevertMeasured: true, Postconditions: []string{"revert measured"}}, "running", "unknown"},
		{"not performed pending", hostops.Observation{P1: "not-performed", PendingEffect: true}, "failed", ""},
		{"not performed reverted", hostops.Observation{P1: "not-performed", RevertDefined: true, RevertMeasured: true, Postconditions: []string{"revert measured"}}, "failed", ""},
		{"not performed pending reverted", hostops.Observation{P1: "not-performed", PendingEffect: true, RevertDefined: true, RevertMeasured: true, Postconditions: []string{"revert measured"}}, "failed", ""},
		{"failed unchanged pending", hostops.Observation{P1: "failed", PendingEffect: true}, "failed", ""},
		{"failed unchanged reverted", hostops.Observation{P1: "failed", RevertDefined: true, RevertMeasured: true, Postconditions: []string{"revert measured"}}, "failed", ""},
		{"failed changed reverted", hostops.Observation{P1: "failed", MeasuredChange: true, RevertDefined: true, RevertMeasured: true, Postconditions: []string{"revert measured"}}, "partial", ""},
		{"performed reverted", hostops.Observation{P1: "performed", RevertDefined: true, RevertMeasured: true, Postconditions: []string{"revert measured"}}, "rolled_back", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := hostops.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cmd := testCommand(strings.Repeat("a6", 16))
			if _, _, err := e.Submit(cmd, nil); err != nil {
				t.Fatal(err)
			}
			got, err := e.Recover(cmd.OperationID, tc.obs)
			if err != nil || got.State != tc.state || got.Outcome != tc.outcome || (got.State != "running" && got.EffectPending) {
				t.Fatalf("record %#v error %v", got, err)
			}
			if tc.state == "running" {
				if _, err := e.Recover(cmd.OperationID, hostops.Observation{P1: "unknown"}); err != nil {
					t.Fatalf("unknown became irreconcilable: %v", err)
				}
			}
		})
	}
}
