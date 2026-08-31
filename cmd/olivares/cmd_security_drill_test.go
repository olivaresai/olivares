// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("OLIVARES_CLI_TRAMPOLINE") == "1" {
		os.Exit(runMain())
	}
	os.Exit(m.Run())
}

func runSecurityDrillTestCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(append([]string{"security", "drill"}, args...))
	err := root.Execute()
	return output.String(), err
}

func TestSecurityDrillEndToEnd(t *testing.T) {
	t.Setenv("OLIVARES_CLI_TRAMPOLINE", "1")
	output, err := runSecurityDrillTestCommand(t)
	if err != nil {
		t.Fatalf("security drill failed: %v\n%s", err, output)
	}
	for _, step := range []string{"produce", "affected", "patched", "below-introduced", "tamper", "wrong-key"} {
		if !strings.Contains(output, "ok "+step+" ") {
			t.Errorf("drill output does not contain the %s step line:\n%s", step, output)
		}
	}
	if !strings.Contains(output, "security drill PASSED — advisory pipeline proven end to end") {
		t.Errorf("drill output does not contain the success verdict:\n%s", output)
	}
}

func TestSecurityDrillRejectsDriftedDraft(t *testing.T) {
	t.Setenv("OLIVARES_CLI_TRAMPOLINE", "1")
	var draft advisoryDraft
	if err := json.Unmarshal(securityDrillEmbeddedDraft, &draft); err != nil {
		t.Fatalf("parse embedded drill draft: %v", err)
	}
	draft.Advisories[0].Affected[0].Ranges[0].Events[1].Fixed = "999.0.0"
	mutated, err := json.MarshalIndent(draft, "", "  ")
	if err != nil {
		t.Fatalf("marshal mutated drill draft: %v", err)
	}
	path := filepath.Join(t.TempDir(), "mutated-draft.json")
	if err := os.WriteFile(path, append(mutated, '\n'), 0o600); err != nil {
		t.Fatalf("write mutated drill draft: %v", err)
	}

	output, err := runSecurityDrillTestCommand(t, "--draft", path)
	if err == nil {
		t.Fatalf("drifted draft produced a false-green drill:\n%s", output)
	}
	failure := err.Error() + "\n" + output
	if !strings.Contains(failure, "guard") && !strings.Contains(failure, "patched") {
		t.Fatalf("drill failure does not name the guard or patched step: %v\n%s", err, output)
	}
}
