// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// TestSuperadminCLILifecycle drives the offline `olivares superadmin` command end
// to end against a real SQLite engine: status lists the accounts, disable/enable
// flip a non-last superadmin, and the last ACTIVE superadmin is deny-closed.
func TestSuperadminCLILifecycle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Seed two superadmins (A + B) into the default SQLite file, then release the
	// single-writer file so the CLI can open it.
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Logger: slog.Default()})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if _, err := eng.authr.BootstrapSuperadmin(ctx, "a@acme.test", "supersecret-pw"); err != nil {
		t.Fatalf("bootstrap A: %v", err)
	}
	actor := mustTestOperator("test")
	if _, err := eng.authr.CreateUser(ctx, actor, auth.NewUser{
		Email: "b@acme.test", DisplayName: "B", Password: "supersecret-pw", Superadmin: true,
	}); err != nil {
		t.Fatalf("create B: %v", err)
	}
	_ = eng.Close()

	exec := func(args ...string) (string, error) {
		cmd := newSuperadminCmd()
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetContext(ctx)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}

	// status lists both, reports two active.
	out, err := exec("status", "--data-dir", dir)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "a@acme.test") || !strings.Contains(out, "b@acme.test") {
		t.Fatalf("status missing an account:\n%s", out)
	}
	if !strings.Contains(out, "2 active superadmin") {
		t.Fatalf("status active count wrong:\n%s", out)
	}

	// JSON status exposes the same account/status data without credential fields.
	out, err = exec("status", "--data-dir", dir, "--format", "json")
	if err != nil {
		t.Fatalf("status json: %v\n%s", err, out)
	}
	var status superadminStatusResult
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("status JSON is invalid: %v\n%s", err, out)
	}
	if len(status.Items) != 2 || status.Active != 2 {
		t.Fatalf("status JSON = %#v, want two active superadmins", status)
	}
	if strings.Contains(strings.ToLower(out), "password") {
		t.Fatalf("status JSON exposed a credential field:\n%s", out)
	}

	// disable B by email → now inactive.
	out, err = exec("disable", "--actor", "ana@corp.example", "--reason", "test fixture", "--data-dir", dir, "--email", "b@acme.test")
	if err != nil {
		t.Fatalf("disable B: %v\n%s", err, out)
	}
	if !strings.Contains(out, "inactive") {
		t.Fatalf("disable B output:\n%s", out)
	}

	// A is now the last active superadmin: disabling it is refused.
	out, err = exec("disable", "--actor", "ana@corp.example", "--reason", "test fixture", "--data-dir", dir, "--email", "a@acme.test")
	if err == nil {
		t.Fatalf("disabling the last active superadmin must fail; output:\n%s", out)
	}

	// enable B → active again.
	out, err = exec("enable", "--actor", "ana@corp.example", "--reason", "test fixture", "--data-dir", dir, "--email", "b@acme.test")
	if err != nil {
		t.Fatalf("enable B: %v\n%s", err, out)
	}
	if !strings.Contains(out, "active") {
		t.Fatalf("enable B output:\n%s", out)
	}

	// An unknown email is a clear error, not a silent no-op.
	if _, err := exec("disable", "--actor", "ana@corp.example", "--reason", "test fixture", "--data-dir", dir, "--email", "ghost@acme.test"); err == nil {
		t.Fatal("disabling an unknown email must fail")
	}
}
