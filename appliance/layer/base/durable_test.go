// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// crashInsideStartServices leaves the record Applying@start-services with the product's start
// queued, as a kill after the start was requested does.
func crashInsideStartServices(t *testing.T, in *Input) (string, *fakeHost) {
	t.Helper()
	dir, h := t.TempDir(), newFakeHost()
	m := newMachine(dir, h, in, h.seams())
	m.crash = func(s Stage, b boundary) bool { return s == StageStartServices && b == afterEffect }
	if _, err := m.Run(context.Background()); !errors.Is(err, errCrashed) {
		t.Fatalf("first run: %v", err)
	}
	return dir, h
}

// ordinaryEvent is something that happens between that crash and the next complete run: it
// ends in a recorded outcome at some stage, and then its cause clears.
type ordinaryEvent struct {
	name  string
	stage Stage
	// happen runs first boot through the event and returns what it recorded; the cause is
	// cleared before it returns.
	happen func(t *testing.T, dir string, h *fakeHost, in, changed *Input) Record
}

func ordinaryEvents() []ordinaryEvent {
	events := []ordinaryEvent{
		{"the answers changed, then were restored", StageValidate, func(t *testing.T, dir string, h *fakeHost, _, changed *Input) Record {
			rec, err := newMachine(dir, h, changed, h.seams()).Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			return rec
		}},
		{"a carrier could not be read", StageValidate, func(t *testing.T, dir string, h *fakeHost, in, _ *Input) Record {
			m := newMachine(dir, h, in, h.seams())
			m.Load = func(context.Context) (Input, error) { return Input{}, Refuse("a carrier input cannot be read") }
			rec, err := m.Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			return rec
		}},
	}
	for _, stage := range Stages[1 : len(Stages)-2] {
		events = append(events, ordinaryEvent{"the recorded " + string(stage) + " effect drifted, then was restored", stage,
			func(t *testing.T, dir string, h *fakeHost, in, _ *Input) Record {
				recorded := h.effects[stage]
				h.effects[stage] = "drifted"
				rec, err := newMachine(dir, h, in, h.seams()).Run(context.Background())
				h.effects[stage] = recorded
				if err != nil {
					t.Fatal(err)
				}
				return rec
			}})
	}
	return events
}

func TestStageMachine_MayHaveStartedSurvivesEveryLaterOutcome(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")
	changed := answersFixture(t, "changed.example.test")
	for _, event := range ordinaryEvents() {
		t.Run(event.name, func(t *testing.T) {
			dir, h := crashInsideStartServices(t, &in)
			if rec := event.happen(t, dir, h, &in, &changed); rec.State != Refused || rec.Stage != event.stage {
				t.Fatalf("the event was not recorded at %s: %+v", event.stage, rec)
			}
			// The start queued before the crash ran: the product created its store and keys.
			m := newMachine(dir, h, &in, h.seams())
			m.Identities = func() ([]string, error) { return []string{"olivares.db", "tls.key"}, nil }
			rec, err := m.Run(context.Background())
			if err != nil || rec.State != Ready || strings.Contains(rec.Reason, "product identities exist") {
				t.Fatalf("the started product was refused as an imported installation: %+v %v", rec, err)
			}
			if h.applies[StageStartServices] != 2 {
				t.Fatalf("start-services applied %d times", h.applies[StageStartServices])
			}
		})
	}
}

func TestStageMachine_ReconcileRefusesOnceTheProductMayHaveStartedWhateverWasRecordedSince(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")
	changed := answersFixture(t, "changed.example.test")
	for _, event := range ordinaryEvents() {
		t.Run(event.name, func(t *testing.T) {
			dir, h := crashInsideStartServices(t, &in)
			event.happen(t, dir, h, &in, &changed)
			before, _, err := Store{Dir: dir}.Load()
			if err != nil {
				t.Fatal(err)
			}
			_, err = newMachine(dir, h, &changed, h.seams()).Reconcile()
			var outcome *Outcome
			if !errors.As(err, &outcome) || outcome.State != Refused {
				t.Fatalf("reconcile forgot stages of a product that may have started: %v", err)
			}
			after, _, _ := Store{Dir: dir}.Load()
			if len(after.Completed) != len(before.Completed) || after.Digest != before.Digest {
				t.Fatalf("a refused reconcile changed the record: %+v", after)
			}
		})
	}
}

func TestStageMachine_ANilLoaderOrIdentityInspectionRefusesWithAReason(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")
	cases := []struct {
		name  string
		unset func(*Machine)
	}{
		{"no answers loader", func(m *Machine) { m.Load = nil }},
		{"no product identity inspection", func(m *Machine) { m.Identities = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, h := t.TempDir(), newFakeHost()
			m := newMachine(dir, h, &in, h.seams())
			tc.unset(m)
			rec, err := runWithoutPanic(t, m)
			if err != nil || rec.State != Refused || rec.Stage != StageValidate || !strings.Contains(rec.Reason, "installed") {
				t.Fatalf("%+v %v", rec, err)
			}
			if len(h.applies) != 0 {
				t.Fatalf("stages applied without a loader or an identity inspection: %v", h.applies)
			}
		})
	}
}
