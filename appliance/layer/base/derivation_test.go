// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStore_OnlyAStartInProgressOrCompletedReadsAsMayHaveStarted(t *testing.T) {
	in := answersFixture(t, "olivares.example.test")

	// A composition without the start-services seam is refused before any stage runs.
	dir, h := t.TempDir(), newFakeHost()
	seams := h.seams()
	seams.StartServices = nil
	rec, err := newMachine(dir, h, &in, seams).Run(context.Background())
	if err != nil || rec.State != Refused || rec.Stage != StageStartServices || len(h.applies) != 0 {
		t.Fatalf("missing start-services seam: %+v %v %v", rec, err, h.applies)
	}
	stored, _, err := Store{Dir: dir}.Load()
	if err != nil || stored.ProductMayHaveStarted {
		t.Fatalf("a refusal written before any stage ran reads as a started product: %+v %v", stored, err)
	}
	// Completed, the composition still meets product identities it never started: an import.
	m := newMachine(dir, h, &in, h.seams())
	m.Identities = func() ([]string, error) { return []string{"olivares.db", "tls.key"}, nil }
	rec, err = m.Run(context.Background())
	if err != nil || rec.State != Refused || rec.Stage != StageValidate || !strings.Contains(rec.Reason, "product identities exist") {
		t.Fatalf("the template-identity refusal was skipped: %+v %v", rec, err)
	}
	if len(h.applies) != 0 {
		t.Fatalf("stages applied on an imported installation: %v", h.applies)
	}

	// A record written without the field derives it from a start in progress or completed only.
	cases := []struct {
		name, record string
		want         bool
	}{
		{"applying at start-services", `{"schema": "olivares-appliance-firstboot/v1", "state": "applying", "stage": "start-services", "completed": []}`, true},
		{"start-services completed", `{"schema": "olivares-appliance-firstboot/v1", "state": "refused", "stage": "measure-readiness", ` +
			`"completed": [{"stage": "start-services", "effect": "olivares.service enabled; start queued"}]}`, true},
		{"refused at start-services", `{"schema": "olivares-appliance-firstboot/v1", "state": "refused", "stage": "start-services", "completed": []}`, false},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, recordFile), []byte(tc.record+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, _, err := Store{Dir: dir}.Load()
		if err != nil || got.ProductMayHaveStarted != tc.want {
			t.Fatalf("%s: may have started %v, want %v (%v)", tc.name, got.ProductMayHaveStarted, tc.want, err)
		}
	}
}
