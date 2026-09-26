// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// refusingStep is a seam whose owner refuses, as cloud-init's host settings do when the
// kernel hostname is not the declared one.
type refusingStep struct{ err error }

func (s refusingStep) Apply(context.Context, Input) (Effect, error) { return "", s.err }
func (s refusingStep) Verify(context.Context, Input, Effect) error  { return s.err }

// runWithoutPanic turns a panic in Run into a test failure with its value.
func runWithoutPanic(t *testing.T, m *Machine) (rec Record, err error) {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("Run panicked instead of refusing: %v", p)
		}
	}()
	return m.Run(context.Background())
}

func TestStageMachine_ACrashInsideStartServicesIsReconciledWhenTheProductStarted(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")
	dir, h := t.TempDir(), newFakeHost()
	first := newMachine(dir, h, &in, h.seams())
	first.crash = func(s Stage, b boundary) bool { return s == StageStartServices && b == afterEffect }
	if _, err := first.Run(context.Background()); !errors.Is(err, errCrashed) {
		t.Fatalf("first run: %v", err)
	}
	rec, found, err := Store{Dir: dir}.Load()
	if err != nil || !found || rec.State != Applying || rec.Stage != StageStartServices {
		t.Fatalf("the record did not say the product start may have begun: %+v %v %v", rec, found, err)
	}
	// The start queued before the crash ran anyway and created the store and the keys.
	retry := newMachine(dir, h, &in, h.seams())
	retry.Identities = func() ([]string, error) { return []string{"olivares.db", "tls.key", "audit-signing.key"}, nil }
	rec, err = runWithoutPanic(t, retry)
	if err != nil || rec.State != Ready {
		t.Fatalf("the product's own start was refused as an imported installation: %+v %v", rec, err)
	}
	if h.applies[StageStartServices] != 2 || h.applies[StageProductConfig] != 1 {
		t.Fatalf("retry applies: %v", h.applies)
	}
}

func TestStageMachine_AMissingSeamRefusesWithAReasonAndNeverPanics(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")
	cases := []struct {
		name  string
		seams func(*fakeHost) Seams
		stage Stage
	}{
		{"the zero value", func(*fakeHost) Seams { return Seams{} }, StageIdentity},
		{"no setup-token seam", func(h *fakeHost) Seams { s := h.seams(); s.SetupDelivery = nil; return s }, StageSetupDelivery},
		{"no firewall seam", func(h *fakeHost) Seams { s := h.seams(); s.Firewall = nil; return s }, StageFirewall},
		{"no readiness seam", func(h *fakeHost) Seams { s := h.seams(); s.Readiness = nil; return s }, StageReadiness},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, h := t.TempDir(), newFakeHost()
			rec, err := runWithoutPanic(t, newMachine(dir, h, &in, tc.seams(h)))
			if err != nil || rec.State != Refused || rec.Stage != tc.stage || !strings.Contains(rec.Reason, string(tc.stage)) {
				t.Fatalf("%+v %v", rec, err)
			}
			if len(h.applies) != 0 {
				t.Fatalf("stages applied before an incomplete composition was refused: %v", h.applies)
			}
		})
	}
}

func TestStageMachine_AnswersBindOnlyOnceTheirStageAppliedAndTheRefusalNamesAnExistingRecovery(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")
	corrected := answersFixture(t, "corrected.example.test")

	// cloud-init did not apply the declared hostname: nothing that depends on the answers applied.
	dir, h := t.TempDir(), newFakeHost()
	seams := h.seams()
	seams.HostSettings = refusingStep{Refuse("the kernel hostname is not host.hostname; cloud-init owns it")}
	rec, err := newMachine(dir, h, &in, seams).Run(context.Background())
	if err != nil || rec.State != Refused || rec.Stage != StageHostSettings {
		t.Fatalf("first run: %+v %v", rec, err)
	}
	rec, err = newMachine(dir, h, &corrected, h.seams()).Run(context.Background())
	if err != nil || rec.State != Ready {
		t.Fatalf("corrected answers were refused before any stage depending on them applied: %+v %v", rec, err)
	}

	// Once the product configuration applied, changed answers are refused with a recovery that exists.
	dir, h = t.TempDir(), newFakeHost()
	first := newMachine(dir, h, &in, h.seams())
	first.crash = func(s Stage, b boundary) bool { return s == StageProductConfig && b == afterPersist }
	if _, err := first.Run(context.Background()); !errors.Is(err, errCrashed) {
		t.Fatalf("first run: %v", err)
	}
	rec, err = newMachine(dir, h, &corrected, h.seams()).Run(context.Background())
	if err != nil || rec.State != Refused || rec.Stage != StageValidate || !strings.Contains(rec.Reason, "appliance-firstboot reconcile") {
		t.Fatalf("changed answers after the configuration applied: %+v %v", rec, err)
	}
}

func TestStageMachine_AMissingCarrierKeepsTheRecordedStageAndReason(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")
	dir, h := t.TempDir(), newFakeHost()
	seams := h.seams()
	seams.SetupDelivery = RefusingSetupDelivery{}
	refused, err := newMachine(dir, h, &in, seams).Run(context.Background())
	if err != nil || refused.State != Refused || refused.Stage != StageSetupDelivery {
		t.Fatalf("first run: %+v %v", refused, err)
	}
	// A carrier present only at the first boot, such as a removed SMBIOS credential.
	gone := newMachine(dir, h, &in, seams)
	gone.Load = func(context.Context) (Input, error) { return Input{}, Wait("no answers carrier is present") }
	rec, err := gone.Run(context.Background())
	if err != nil || rec.State != refused.State || rec.Stage != refused.Stage || rec.Reason != refused.Reason {
		t.Fatalf("the recorded outcome was replaced: %+v %v", rec, err)
	}
	stored, _, err := Store{Dir: dir}.Load()
	if err != nil || stored.Stage != StageSetupDelivery || stored.Reason != refused.Reason {
		t.Fatalf("stored record: %+v %v", stored, err)
	}
}

func TestStore_AnUnknownRecordSchemaIsRefusedByNameAndLeftInPlace(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")
	dir, h := t.TempDir(), newFakeHost()
	newer := []byte(`{"schema": "olivares-appliance-firstboot/v2", "state": "applying", "completed": []}` + "\n")
	path := filepath.Join(dir, recordFile)
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runWithoutPanic(t, newMachine(dir, h, &in, h.seams()))
	if err == nil || !strings.Contains(err.Error(), "olivares-appliance-firstboot/v2") {
		t.Fatalf("an unknown schema must be refused by name: %v", err)
	}
	if kept, _ := os.ReadFile(path); !bytes.Equal(kept, newer) || len(h.applies) != 0 {
		t.Fatalf("the unknown record was rewritten or stages applied: %s %v", kept, h.applies)
	}
}
