// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runMigrate executes `olivares migrate <args>` with captured output.
func runMigrate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newMigrateCmd()
	var buf, errOut bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestMigrateStatusBadEngine(t *testing.T) {
	t.Parallel()
	if _, err := runMigrate(t, "status", "--engine", "mysql"); err == nil {
		t.Fatal("expected an error for an unknown engine")
	}
}

func TestMigrateStatusPostgresNeedsDSN(t *testing.T) {
	t.Parallel()
	if _, err := runMigrate(t, "status", "--engine", "postgres"); err == nil {
		t.Fatal("expected an error when --dsn is missing for postgres")
	}
}

func TestMigrateStatusMissingSQLiteDB(t *testing.T) {
	t.Parallel()
	out, err := runMigrate(t, "status", "--engine", "sqlite", "--data-dir", t.TempDir())
	if err == nil {
		t.Fatalf("expected an error for a missing sqlite db, got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "no sqlite database") {
		t.Errorf("unexpected error: %v", err)
	}
}

// A directory at the resolved path is rejected with the same friendly error (not a
// cryptic driver "unable to open database file").
func TestMigrateStatusDirectoryIsNotADB(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "olivares.db"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := runMigrate(t, "status", "--engine", "sqlite", "--data-dir", dir)
	if err == nil || !strings.Contains(err.Error(), "no sqlite database") {
		t.Fatalf("expected the friendly 'no sqlite database' error, got: %v", err)
	}
}

// TestMigrateStatusHappyPath boots a real engine (which migrates a fresh sqlite db),
// then reads it back through the command end-to-end.
func TestMigrateStatusHappyPath(t *testing.T) {
	dir := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", Version: "test"})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	_ = eng.Close()

	out, err := runMigrate(t, "status", "--engine", "sqlite", "--data-dir", dir)
	if err != nil {
		t.Fatalf("migrate status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "schema_migrations_core") {
		t.Errorf("expected the core tracking table in the output:\n%s", out)
	}
	if !strings.Contains(out, "expand") {
		t.Errorf("expected at least one expand migration in the output:\n%s", out)
	}

	out, err = runMigrate(t, "status", "--engine", "sqlite", "--data-dir", dir, "--format", "json")
	if err != nil {
		t.Fatalf("migrate status json: %v\n%s", err, out)
	}
	var items []migrationStatusItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("migrate status JSON is invalid: %v\n%s", err, out)
	}
	if len(items) == 0 || items[0].Table == "" || items[0].Phase == "" {
		t.Fatalf("migrate status JSON is missing structured records: %#v", items)
	}
}

func TestMigrateManifestJSONFormat(t *testing.T) {
	t.Parallel()
	out, err := runMigrate(t, "manifest", "--format", "json")
	if err != nil {
		t.Fatalf("migrate manifest json: %v\n%s", err, out)
	}
	var result struct {
		Manifest schemaManifest `json:"manifest"`
		SHA256   string         `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("migrate manifest JSON is invalid: %v\n%s", err, out)
	}
	if result.Manifest.SchemaVersion == 0 || len(result.SHA256) != 64 {
		t.Fatalf("migrate manifest JSON is incomplete: schema_version=%d sha256=%q", result.Manifest.SchemaVersion, result.SHA256)
	}
}
