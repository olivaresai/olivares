// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// directoryStatusOfFile opens (and on first use creates) the store with the
// SAME edition lineage `serve` and the ceremony present: the migration guard
// derives the rollout identity from the registered schema, so a store created
// with a narrower registration would be refused by the command under test.
func directoryStatusOfFile(t *testing.T, dsn string) store.DirectoryStatus {
	t.Helper()
	registrar, err := bootSchemaRegistrar(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("boot schema registrar: %v", err)
	}
	st, err := coreengine.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: dsn}, registrar)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	defer st.Close() //nolint:errcheck
	if err := st.System(context.Background(), func(sys store.SystemScope) error { _, err := sys.EnsureSystemTenant(context.Background()); return err }); err != nil {
		t.Fatal(err)
	}
	status, supported, err := st.(store.DirectoryStatuser).DirectoryStatus(context.Background())
	if err != nil || !supported {
		t.Fatalf("directory status = %+v supported=%t err=%v", status, supported, err)
	}
	return status
}

func TestDBActivateDirectoryWriterRefusesWithoutExplicitAssertions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "olivares.db")
	if status := directoryStatusOfFile(t, dsn); status.ControlMode != store.DirectoryControlStaged || status.ExpectedGeneration != 1 {
		t.Fatalf("fresh store status = %+v", status)
	}
	out, err := runDB(t, "activate-directory-writer", "--data-dir", dir, "--expected-generation", "1",
		"--actor", "tester", "--reason", "missing assertions")
	if err == nil || !strings.Contains(err.Error(), "--writers-upgraded") || !strings.Contains(err.Error(), "--writers-drained") {
		t.Fatalf("missing assertions = %v\n%s", err, out)
	}
	if status := directoryStatusOfFile(t, dsn); status.ControlMode != store.DirectoryControlStaged || status.ExpectedGeneration != 1 {
		t.Fatalf("refused ceremony changed the store: %+v", status)
	}
	if _, err := runDB(t, "activate-directory-writer", "--data-dir", t.TempDir(), "--expected-generation", "1",
		"--actor", "tester", "--reason", "no store", "--writers-upgraded", "--writers-drained"); err == nil ||
		!strings.Contains(err.Error(), "never creates a store") {
		t.Fatalf("missing store = %v", err)
	}
}

func TestDBActivateDirectoryWriterActivatesOnceAndReportsReopen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "olivares.db")
	_ = directoryStatusOfFile(t, dsn) // creates and migrates the fresh store
	out, err := runDB(t, "activate-directory-writer", "--data-dir", dir, "--expected-generation", "1",
		"--actor", "tester", "--reason", "serve stopped", "--writers-upgraded", "--writers-drained", "--format", "json")
	if err != nil {
		t.Fatalf("activate: %v\n%s", err, out)
	}
	var result directoryActivationResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("activation JSON: %v\n%s", err, out)
	}
	if !result.Changed || !result.ReopenRequired || result.Error != "" ||
		result.Before.ControlMode != string(store.DirectoryControlStaged) || result.Before.ExpectedGeneration != 1 ||
		result.After.ControlMode != string(store.DirectoryControlEnforced) || result.After.ExpectedGeneration != 2 ||
		result.After.WriterPosture != string(store.DirectoryWriterSQLiteCapability) || result.Actor != "tester" {
		t.Fatalf("activation result = %+v", result)
	}
	if status := directoryStatusOfFile(t, dsn); status.ControlMode != store.DirectoryControlEnforced || status.ExpectedGeneration != 2 {
		t.Fatalf("store after activation = %+v", status)
	}
	out, err = runDB(t, "activate-directory-writer", "--data-dir", dir, "--expected-generation", "1",
		"--actor", "tester", "--reason", "retry", "--writers-upgraded", "--writers-drained")
	if err != nil || !strings.Contains(out, "changed:            false") || !strings.Contains(out, "reopen required:    true") {
		t.Fatalf("idempotent retry = %v\n%s", err, out)
	}
	if _, err := runDB(t, "activate-directory-writer", "--data-dir", dir, "--expected-generation", "7",
		"--actor", "tester", "--reason", "wrong generation", "--writers-upgraded", "--writers-drained"); err == nil {
		t.Fatal("wrong expected generation was accepted")
	}
}
