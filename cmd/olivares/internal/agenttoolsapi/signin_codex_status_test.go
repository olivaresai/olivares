// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"os"
	"path/filepath"
	"testing"
)

// codex-cli 0.160 prints `login status` on stderr, not stdout (measured on the real
// tool, 2026-10-05). A reader of stdout alone reported every signed-in Codex as signed out.
const stubCodexStatusOnStderr = `#!/bin/sh
case "$1 $2" in
"login status")
  if [ -f "$CODEX_HOME/auth.json" ]; then echo "Logged in using ChatGPT" >&2; exit 0; fi
  echo "Not logged in" >&2; exit 1;;
esac
exit 2
`

func TestCodexSignInStatusIsReadFromTheStreamTheToolWrites(t *testing.T) {
	call, home := newSignInServer(t, func(_ *Module, bin string) {
		if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(stubCodexStatusOnStderr), 0o755); err != nil {
			t.Fatal(err)
		}
	})
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=codex", nil); code != 200 || st["installed"] != true || st["signed_in"] != false {
		t.Fatalf("before the login: %d %v, want installed and signed out", code, st)
	}
	if err := os.MkdirAll(filepath.Join(home, "codex", ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "codex", ".codex", "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=codex", nil); code != 200 || st["signed_in"] != true || st["method"] != "ChatGPT" {
		t.Fatalf("after the login: %d %v, want signed in with ChatGPT", code, st)
	}
}
