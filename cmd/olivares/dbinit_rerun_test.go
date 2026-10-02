// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	dbInitRerunPhaseEnv = "OLIVARES_TEST_DBINIT_RERUN_PHASE"
	dbInitRerunDirEnv   = "OLIVARES_TEST_DBINIT_RERUN_DIR"
)

// Running db init again on a database the engine has already migrated (to add the
// admin role later, the documented path) re-grants the application role DML on every
// table. The engine must still start: db init re-establishes the core v13 login
// capability boundary in the same transaction. Each start runs in its own process, as
// the engine's restart re-executes its binary.
func TestDBInitAgainAfterTheFirstStartKeepsTheEngineStarting(t *testing.T) {
	pgMaintenanceForTest(t, "ARCH_DB_REINIT_MAINTENANCE")
	dir := filepath.Join(t.TempDir(), "reinit")
	initArgs := []string{"init", "--superuser-dsn", "env:ARCH_DB_REINIT_MAINTENANCE", "--data-dir", dir}
	if output, err := runDB(t, initArgs...); err != nil {
		t.Fatalf("db init on a new database: %v\n%s", err, output)
	}
	start := func(phase string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestDBInitRerunChild$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), "OLIVARES_CLI_TRAMPOLINE=", dbInitRerunPhaseEnv+"="+phase, dbInitRerunDirEnv+"="+dir)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestDBInitRerunChild") {
			t.Fatalf("start %q (its own process) = %v:\n%s", phase, err, out)
		}
	}
	start("first-start")

	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	adminPW := filepath.Join(t.TempDir(), "admin.password")
	if err := os.WriteFile(adminPW, []byte("arch-reinit-admin-password-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	reinit := append(initArgs, "--admin-role", "arch_reinit_admin_"+hex.EncodeToString(suffix[:]), "--admin-password-file", adminPW)
	if output, err := runDB(t, reinit...); err != nil {
		t.Fatalf("db init again with --admin-role: %v\n%s", err, output)
	}

	start("after-reinit")
}

// TestDBInitRerunChild is one start of TestDBInitAgainAfterTheFirstStartKeepsTheEngineStarting,
// in its own process: it starts from the data directory db init saved and asks for readiness.
// Alone it skips.
func TestDBInitRerunChild(t *testing.T) {
	phase, dir := os.Getenv(dbInitRerunPhaseEnv), os.Getenv(dbInitRerunDirEnv)
	if phase == "" || dir == "" {
		t.Skip("a start of TestDBInitAgainAfterTheFirstStartKeepsTheEngineStarting")
	}
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Version: version, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("start %s: %v", phase, err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if code, _, raw := doDemoViewJSON(t, eng.api.Handler(), http.MethodGet, "/readyz", "", "", nil); code != http.StatusOK {
		t.Fatalf("readiness at %s = %d %s, want 200", phase, code, raw)
	}
}
