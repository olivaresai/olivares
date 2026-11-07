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

// codex-cli 0.166 run confined, with a home it may not write, first warns on stderr that it
// could not create its PATH aliases and then answers (measured on the real tool, 2026-11-01).
const stubCodexStatusAfterAWarning = `#!/bin/sh
case "$1 $2" in
"login status")
  echo "WARNING: proceeding, even though we could not create PATH aliases: Permission denied (os error 13)" >&2
  if [ -f "$CODEX_HOME/auth.json" ]; then echo "Logged in using ChatGPT" >&2; exit 0; fi
  echo "Not logged in" >&2; exit 1;;
esac
exit 2
`

// codex-cli 0.166 on a login that is an API key: it names the key, masked, and says when it
// is checked (measured on the real tool with a fake key, 2026-11-03).
const stubCodexStatusAPIKey = `#!/bin/sh
case "$1 $2" in
"login status")
  if [ -f "$CODEX_HOME/auth.json" ]; then printf 'API key configured - sk-stand***00000\nAuthentication is checked when you send a request.\n' >&2; exit 0; fi
  echo "Not logged in" >&2; exit 1;;
esac
exit 2
`

func TestCodexSignInStatusIsReadFromTheStreamTheToolWrites(t *testing.T) {
	for name, tc := range map[string]struct{ stub, method string }{
		"answer alone":    {stubCodexStatusOnStderr, "ChatGPT"},
		"after a warning": {stubCodexStatusAfterAWarning, "ChatGPT"},
		"an API key":      {stubCodexStatusAPIKey, "API key"},
	} {
		t.Run(name, func(t *testing.T) { codexSignInStatusIsRead(t, tc.stub, tc.method) })
	}
}

func codexSignInStatusIsRead(t *testing.T, stub, method string) {
	call, home := newSignInServer(t, func(_ *Module, bin string) {
		if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(stub), 0o755); err != nil {
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
	if code, st := call("GET", "/v1/m/agenttools/sign-in?driver=codex", nil); code != 200 || st["signed_in"] != true || st["method"] != method {
		t.Fatalf("after the login: %d %v, want signed in with %s", code, st, method)
	}
}
