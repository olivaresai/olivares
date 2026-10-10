// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/dr/opgate"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

func TestDRStatusReadsPendingSQLiteFenceWithoutOpeningTheStore(t *testing.T) {
	dir := t.TempDir()
	installDataDirControl(t, dir, opgate.StatePending)
	out, err := runDR("restore-status", "--data-dir", dir, "--engine", "sqlite")
	if err == nil || exitcode.From(err) != 1 {
		t.Fatalf("read pending restore status: %v\n%s", err, out)
	}
	if !strings.Contains(out, opgate.StatePending) || !strings.Contains(out, "cafebabecafebabecafebabecafebabe") {
		t.Fatalf("status must report the durable pending fence and its operation: %s", out)
	}
	if keys := dataDirKeys(t, dir); len(keys) != 0 {
		t.Fatalf("status minted signing keys: %v", keys)
	}
	if _, err := os.Stat(filepath.Join(dir, "olivares.db")); !os.IsNotExist(err) {
		t.Fatalf("status created a store: %v", err)
	}
}

func runDRStatusJSON(t *testing.T, args ...string) (drRestoreStatus, string, error) {
	t.Helper()
	cmd := newDRCmd()
	cmd.PersistentFlags().String("output", "json", "output")
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(append([]string{"restore-status"}, args...))
	err := cmd.Execute()
	var status drRestoreStatus
	if decodeErr := json.Unmarshal(out.Bytes(), &status); decodeErr != nil {
		t.Fatalf("status must return one JSON result: %v\n%s", decodeErr, out.String())
	}
	return status, out.String() + stderr.String(), err
}

func TestDRRestoreStatusLabelsUnenrolledWithoutCreatingFiles(t *testing.T) {
	dir := t.TempDir()
	s, _, err := runDRStatusJSON(t, "--data-dir", dir)
	if err != nil || s.State != "legacy_or_lost_unknown" || s.ControlPresent == nil || *s.ControlPresent || s.PublicationAllowed == nil || !*s.PublicationAllowed || s.CustodyMatch != nil {
		t.Fatalf("unenrolled status must not claim a completed restore: %+v, %v", s, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("status changed an unenrolled destination: entries=%v, error=%v", entries, err)
	}
}

func TestDRRestoreStatusCorruptControlIsUnknown(t *testing.T) {
	dir := t.TempDir()
	anchor := installDataDirControl(t, dir, opgate.StatePending)
	if err := os.WriteFile(anchor.RecordPath(), []byte(`{"format":1,"secret":"fixture-only"`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, output, err := runDRStatusJSON(t, "--data-dir", dir)
	if err == nil || exitcode.From(err) != 2 || s.State != "unknown" || s.ControlPresent != nil || s.PublicationAllowed != nil {
		t.Fatalf("corrupt control must be unknown: %+v, %v", s, err)
	}
	if strings.Contains(output, "fixture-only") || len(dataDirKeys(t, dir)) != 0 {
		t.Fatal("status exposed record contents or minted custody")
	}
}

func TestDRRestoreStatusCompletedControlDoesNotLoadCustody(t *testing.T) {
	dir := t.TempDir()
	installDataDirControl(t, dir, opgate.StateComplete)
	before := keyBytesCensus(t, dir)
	if err := os.Remove(filepath.Join(dir, "audit-signing.key")); err != nil {
		t.Fatal(err)
	}
	s, _, err := runDRStatusJSON(t, "--data-dir", dir)
	if err != nil || s.State != opgate.StateComplete || s.CustodyMatch != nil || s.ReasonCode != "complete_control_custody_unchecked" {
		t.Fatalf("status must separate completed control from actual signing custody: %+v, %v", s, err)
	}
	delete(before, "audit-signing.key")
	assertNoNewKeyBytes(t, dir, before)
	if _, err := os.Stat(filepath.Join(dir, "audit-signing.key")); !os.IsNotExist(err) {
		t.Fatal("status minted missing completed custody")
	}
}

func TestDRRestoreStatusPostgresReadsActualPendingControl(t *testing.T) {
	pg := newPGSplitFixture(t, "drwstatus", true)
	cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.appDSN, OwnerDSN: pg.ownerDSN, AdminDSN: pg.adminDSN}
	report, err := coreengine.InstallPendingRestoreControl(t.Context(), cfg, coreengine.PendingRestoreSpec{
		OpID: strings.Repeat("a", 32), PlanSHA256: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	args := append([]string{"--data-dir", dir}, pg.drArgs()...)
	s, _, err := runDRStatusJSON(t, args...)
	if err == nil || exitcode.From(err) != 1 || s.State != opgate.StatePending || s.OperationID != report.OpID || s.PlanSHA256 != report.PlanSHA256 || s.Revision == nil || *s.Revision != report.Revision || s.PublicationAllowed == nil || *s.PublicationAllowed {
		t.Fatalf("status must read the real pending database control: %+v, %v", s, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("status changed local custody: entries=%v, error=%v", entries, err)
	}
}

func TestDRRestoreStatusUnavailablePostgresDoesNotExposeSecrets(t *testing.T) {
	s, output, err := runDRStatusJSON(t, "--engine", "postgres", "--data-dir", t.TempDir(), "--dsn", "postgres://app:fixture-secret@127.0.0.1:1/unavailable")
	if err == nil || exitcode.From(err) != 2 || s.State != "unknown" || s.ControlPresent != nil || s.PublicationAllowed != nil {
		t.Fatalf("unreadable database status must be unknown: %+v, %v", s, err)
	}
	if strings.Contains(output, "fixture-secret") || strings.Contains(output, "postgres://") {
		t.Fatal("status exposed a DSN or its secret")
	}
}

func TestDRRestoreStatusUnknownRevisionIsNull(t *testing.T) {
	_, output, err := runDRStatusJSON(t, "--engine", "unsupported", "--data-dir", t.TempDir())
	if err == nil || exitcode.From(err) != 2 {
		t.Fatalf("unsupported engine must remain unknown: %v", err)
	}
	var fields map[string]any
	if err := json.NewDecoder(strings.NewReader(output)).Decode(&fields); err != nil {
		t.Fatal(err)
	}
	if revision, present := fields["revision"]; !present || revision != nil {
		t.Fatalf("unknown revision must be null, got %v (present=%v)", revision, present)
	}
}

func TestDRRestoreStatusPendingControlsDoNotClaimCustody(t *testing.T) {
	pg := newPGSplitFixture(t, "drwstatuspending", true)
	cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.appDSN, OwnerDSN: pg.ownerDSN, AdminDSN: pg.adminDSN}
	report, err := coreengine.InstallPendingRestoreControl(t.Context(), cfg, coreengine.PendingRestoreSpec{
		OpID: strings.Repeat("a", 32), PlanSHA256: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	anchor, exists, err := opgate.AnchorForDataDir(dir)
	if err != nil || !exists {
		t.Fatalf("local anchor: %v", err)
	}
	lease, held, err := opgate.TryAcquire(opgate.ModeExclusive, anchor)
	if err != nil || !held {
		t.Fatalf("local lease: %v", err)
	}
	record := opgate.Record{
		Format: opgate.Format, Revision: report.Revision, Enrolled: true, State: report.State,
		OpID: report.OpID, PlanSHA256: report.PlanSHA256, ObservedAt: "2026-10-02T00:00:00Z",
		Destination: opgate.Destination{Engine: "postgres", CanonicalPath: anchor.Canonical(),
			Database: report.Database, Schema: report.Schema, SystemIdentifier: report.SystemIdentifier},
	}
	if err := lease.Commit(anchor, record); err != nil {
		_ = lease.Release()
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	s, _, err := runDRStatusJSON(t, append([]string{"--data-dir", dir}, pg.drArgs()...)...)
	if err == nil || exitcode.From(err) != 1 || s.State != opgate.StatePending || s.CustodyMatch != nil {
		t.Fatalf("matching pending controls contain no signing custody to compare: %+v, %v", s, err)
	}
}
