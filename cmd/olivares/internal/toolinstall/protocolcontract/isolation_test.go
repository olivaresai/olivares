//go:build contract && linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package protocolcontract

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestContractChildHasNoInheritedCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "contract-poison")
	t.Setenv("OPENAI_API_KEY", "contract-poison")
	t.Setenv("XAI_API_KEY", "contract-poison")
	t.Setenv("AWS_PROFILE", "contract-poison")
	t.Setenv("CLAUDE_CONFIG_DIR", "/forbidden-config")
	cmd := exec.Command("/usr/bin/env")
	cmd.Env = isolatedEnv(t.TempDir())
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "contract-poison") || strings.Contains(string(b), "/forbidden-config") {
		t.Fatal("the protocol child inherited caller credentials or configuration")
	}
	for _, name := range []string{"HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "GROK_HOME", "XDG_CONFIG_HOME"} {
		if !strings.Contains(string(b), name+"=") {
			t.Errorf("missing isolated %s", name)
		}
	}
}

func TestContractChildStartsWithEmptyConfigHomes(t *testing.T) {
	p := startChild(t, "/bin/sh", []string{"-c", `
for path in "$HOME" "$CLAUDE_CONFIG_DIR" "$CODEX_HOME" "$GROK_HOME" "$XDG_CONFIG_HOME"; do
  test -d "$path" || exit 1
  test -z "$(ls -A "$path")" || exit 1
done
printf '{"empty_homes":true}\n'
`}, t.TempDir(), "empty-config")
	f, err := p.read(func(f map[string]json.RawMessage) bool { return string(f["empty_homes"]) == "true" })
	if err != nil || f == nil {
		t.Fatalf("fresh child config homes: %v", err)
	}
}
