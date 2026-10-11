// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"os"
	"strings"
	"testing"
)

// Which tools can sign in is a driver fact. The sign-in sentences, the published
// enums of the sign-in routes and the installer enum must be built from the facts,
// so a tool that gains a SignIn (or an Installer) flows through with no edit here.
func TestNoToolSetIsHardCodedInTheSignInRoutes(t *testing.T) {
	for _, file := range []string{"signin.go", "openapi.go"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		for _, literal := range []string{
			`"claude", "codex", "grok", "opencode"`,
			"claude, codex, grok or opencode",
			"Claude Code, Codex, Grok Build and OpenCode",
		} {
			if strings.Contains(src, literal) {
				t.Errorf("%s hard-codes the tool set %q; read it from driverfacts", file, literal)
			}
		}
	}
}
