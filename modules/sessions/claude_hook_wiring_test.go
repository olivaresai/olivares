// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Every Claude Code stream-json launch that carries the PEP endpoint gets the
// hook settings, create and resume alike; other drivers and launches without a
// mounted PEP are untouched.
func TestPrepareClaudeHooksWiresTheHookSettings(t *testing.T) {
	dataDir := t.TempDir()
	m := New(WithClaudeHookPEP(dataDir, "/usr/local/bin/olivares"))
	pepEnv := []EnvVar{{Name: "OLIVARES_HOOK_PEP_URL", Value: "https://127.0.0.1:8443/v1/hooks"}, {Name: "OLIVARES_HOOK_PEP_TOKEN", Value: "launch-token"}}

	spec := LaunchSpec{Env: pepEnv}
	if err := m.prepareClaudeHooks(&spec, CreateRunParams{Transport: TransportStreamJSON}, "run-1"); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(dataDir, "run", "run-1", "pep-settings.json")
	if i := slices.Index(spec.Args, "--settings"); i < 0 || spec.Args[i+1] != settings {
		t.Fatalf("args = %v, want --settings %s", spec.Args, settings)
	}
	if _, err := os.Stat(settings); err != nil {
		t.Fatalf("hook settings not written: %v", err)
	}

	plain := LaunchSpec{}
	if err := m.prepareClaudeHooks(&plain, CreateRunParams{Transport: TransportStreamJSON}, "run-2"); err != nil || len(plain.Args) != 0 {
		t.Fatalf("no PEP mounted: args=%v err=%v, want untouched", plain.Args, err)
	}
	codex := LaunchSpec{Env: pepEnv}
	codexParams := CreateRunParams{Transport: TransportStreamJSON, ProviderHome: &ProviderHomeSnapshot{Driver: "codex"}}
	if err := m.prepareClaudeHooks(&codex, codexParams, "run-3"); err != nil || len(codex.Args) != 0 {
		t.Fatalf("codex: args=%v err=%v, want untouched", codex.Args, err)
	}
	broken := LaunchSpec{Env: []EnvVar{{Name: "OLIVARES_HOOK_PEP_URL", Value: "https://x"}}}
	if err := m.prepareClaudeHooks(&broken, CreateRunParams{Transport: TransportStreamJSON}, "run-4"); err == nil {
		t.Fatal("a PEP launch whose hook settings cannot be written must be refused")
	}
}
