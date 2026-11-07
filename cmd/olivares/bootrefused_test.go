// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// A boot that only an operator can fix exits Refused, which the service units name in
// RestartPreventExitStatus=. A failure a later start may not repeat keeps another code,
// so systemd still restarts it.
func TestServeBootRefusedExitCode(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	for name, version := range map[string]string{
		"store migrated by a newer release":         "9999", // above this binary's newest core migration
		"store carrying a version this build lacks": "12",   // below it, but not in the compiled plan
	} {
		t.Run(name, func(t *testing.T) { schemaRefusedBoot(t, quiet, version) })
	}

	t.Run("snapshot I/O failure stays restartable", func(t *testing.T) {
		dir, request, dbCfg := packageSnapshotFixture(t)
		if err := os.WriteFile(filepath.Join(dir, "backups"), []byte("blocked"), 0o600); err != nil {
			t.Fatal(err)
		}
		eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", DSN: dbCfg.DSN,
			Version: "test", ServeMode: true, upgradeSnapshotRequest: request, Logger: quiet})
		if eng != nil {
			_ = eng.Close()
		}
		if err == nil {
			t.Fatal("boot accepted a failed pre-migration snapshot")
		}
		if got := exitcode.From(err); got == exitcode.Refused {
			t.Fatalf("a snapshot I/O failure exits Refused, so systemd would never retry it: %v", err)
		}
	})
}

// schemaRefusedBoot boots a fresh store, records core version in its migration history as a
// newer release would, and requires the next boot to exit Refused.
func schemaRefusedBoot(t *testing.T, quiet *slog.Logger, version string) {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "olivares.db")
	cfg := bootConfig{DataDir: dir, Engine: "sqlite", DSN: dsn, Version: "test", ServeMode: true, Logger: quiet}
	eng, err := boot(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	packageSnapshotSQL(t, dsn, `INSERT INTO schema_migrations_core (version, name, applied_at, phase)
VALUES ('`+version+`', 'from_a_newer_release', '2026-11-01T00:00:00Z', 'expand')`)
	eng, err = boot(context.Background(), cfg)
	if eng != nil {
		_ = eng.Close()
	}
	if err == nil {
		t.Fatalf("boot accepted a store that records core version %s", version)
	}
	if got := exitcode.From(err); got != exitcode.Refused {
		t.Fatalf("boot on core version %s exits %d, want Refused (%d): %v", version, got, exitcode.Refused, err)
	}
}

// The packaged unit and the installer template must stop restarting on Refused, end
// a crash loop of any other code, and let SIGTERM reach only the engine so it stops
// its sessions itself (with the engine-stopped reason the next start resumes).
func TestSystemdUnitsModelTheEngine(t *testing.T) {
	want := map[string]string{
		"Unit/StartLimitIntervalSec":       "120",
		"Unit/StartLimitBurst":             "5",
		"Service/KillMode":                 "mixed",
		"Service/RestartPreventExitStatus": strconv.Itoa(exitcode.Refused),
	}
	for _, unit := range []string{"packaging/service/systemd.service", "packaging/systemd/olivares.service"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", unit))
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		section := ""
		for sc := bufio.NewScanner(bytes.NewReader(raw)); sc.Scan(); {
			line := sc.Text()
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				section = strings.Trim(line, "[]")
			} else if key, value, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
				got[section+"/"+key] = value
			}
		}
		for key, value := range want {
			if got[key] != value {
				t.Errorf("%s: %s=%q, want %q", unit, key, got[key], value)
			}
		}
	}
}
