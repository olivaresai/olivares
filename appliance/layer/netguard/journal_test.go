// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJournal_ExhaustionPreservesPendingAndRefusesNewEffects(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acts")
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	op := Window{Schema: JournalSchema, OperationID: "0123456789abcdef0123456789abcdef", Generation: "first", Calls: []CallRecord{pendingCall()}}
	if err := j.Create(op); err != nil {
		t.Fatal(err)
	}
	op.Calls = make([]CallRecord, MaxCallsPerWindow+1)
	if err := j.Save(op); err == nil {
		t.Fatal("unbounded call log accepted")
	}
	got, err := j.Load(op.OperationID)
	if err != nil || len(got.Calls) != 1 || got.Calls[0].Serial != 37 || got.Calls[0].Settled {
		t.Fatalf("pending identity lost: %+v %v", got, err)
	}
}
func TestJournal_RefusesSymlinkAndPreservesExistingAuthority(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "elsewhere")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "acts")
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(dir); err == nil {
		t.Fatal("symlink journal accepted")
	}
}
func TestSameOperationIDNeverOpensAnotherWindow(t *testing.T) {
	j, err := OpenJournal(filepath.Join(t.TempDir(), "acts"))
	if err != nil {
		t.Fatal(err)
	}
	op := Window{Schema: JournalSchema, OperationID: "0123456789abcdef0123456789abcdef", Generation: "first", Digest: "digest", State: StatePending}
	if err := j.Create(op); err != nil {
		t.Fatal(err)
	}
	op.Generation = "second"
	if err := j.Create(op); err == nil {
		t.Fatal("operation id replaced")
	}
	got, err := j.Load(op.OperationID)
	if err != nil || got.Generation != "first" {
		t.Fatal(got, err)
	}
}
