// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// The pinned CLI's ACP Google login prints its authorization URL and reads a
// code without a newline on the prompt. It writes its own home, then acknowledges
// authenticate; the server stays alive until Olivares ends the completed relay.
const stubGeminiLogin = `#!/bin/sh
[ "$1" = '--acp' ] || exit 2
[ "$NO_BROWSER" = true ] || exit 3
[ "$GEMINI_CLI_NO_RELAUNCH" = true ] || exit 5
read init
case "$init" in *'"method":"initialize"'*) ;; *) exit 8 ;; esac
printf '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"authMethods":[{"id":"oauth-personal"}]}}\n'
read auth
case "$auth" in *'"methodId":"oauth-personal"'*) ;; *) exit 9 ;; esac
printf '\nPlease visit the following URL to authorize the application:\n\nhttps://accounts.google.com/o/oauth2/v2/auth?state=fixture\n\nEnter the authorization code: '
read code
[ "$code" = good-code ] || exit 7
mkdir -p "$GEMINI_CLI_HOME/.gemini"
printf '{}' > "$GEMINI_CLI_HOME/.gemini/oauth_creds.json"
printf '{"security":{"auth":{"selectedType":"oauth-personal"}}}' > "$GEMINI_CLI_HOME/.gemini/settings.json"
printf '{"jsonrpc":"2.0","id":2,"result":{}}\n'
while read rest; do :; done
`

func TestGeminiGoogleSignInUsesTheSelectedProfileHome(t *testing.T) {
	loginRoot := t.TempDir()
	call, _ := newSignInServer(t, func(m *Module, bin string) {
		if err := os.WriteFile(filepath.Join(bin, "gemini-cli"), []byte(stubGeminiLogin), 0755); err != nil {
			t.Fatal(err)
		}
		m.SetLoginHome(func(_ context.Context, tenant model.TenantID, driver, ref string) (string, string, error) {
			base := filepath.Join(loginRoot, tenant.String(), driver, ref)
			// The login's user home differs from the parent of .gemini, as managed accounts do.
			return filepath.Join(base, "home"), filepath.Join(base, "config", ".gemini"), nil
		})
	})
	code, s := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "gemini-cli", "account_ref": "ppf_gemini"})
	if code != 202 || s["state"] != "needs_code" || s["url"] != "https://accounts.google.com/o/oauth2/v2/auth?state=fixture" {
		t.Fatalf("start = %d %v", code, s)
	}
	code, done := call("POST", "/v1/m/agenttools/sign-in/"+s["id"].(string)+"/code", map[string]string{"code": "good-code"})
	if code != 202 || done["state"] != "signed_in" {
		t.Fatalf("code = %d %v", code, done)
	}
	for _, ref := range []string{"ppf_gemini", "ppf_other"} {
		code, st := call("GET", "/v1/m/agenttools/sign-in?driver=gemini-cli&account_ref="+ref, nil)
		if code != 200 || st["signed_in"] != (ref == "ppf_gemini") {
			t.Fatalf("%s status = %d %v", ref, code, st)
		}
	}
}

func TestGeminiSignInRequiresNativeAcknowledgment(t *testing.T) {
	call, _ := newSignInServer(t, func(m *Module, bin string) {
		script := `#!/bin/sh
read init
mkdir -p "$GEMINI_CLI_HOME/.gemini"
printf '{}' > "$GEMINI_CLI_HOME/.gemini/oauth_creds.json"
printf '{"security":{"auth":{"selectedType":"oauth-personal"}}}' > "$GEMINI_CLI_HOME/.gemini/settings.json"
exit 0
`
		if err := os.WriteFile(filepath.Join(bin, "gemini-cli"), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	})
	code, flow := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "gemini-cli"})
	if code != 202 || flow["state"] != "failed" {
		t.Fatalf("no native acknowledgment = %d %v", code, flow)
	}
}

func TestGeminiSavedGoogleLoginIsRefusedBeforeSpawn(t *testing.T) {
	for _, location := range []string{"config", "working directory"} {
		t.Run(location, func(t *testing.T) {
			config, home := filepath.Join(t.TempDir(), ".gemini"), t.TempDir()
			dir := config
			if location == "working directory" {
				dir = filepath.Join(home, ".gemini")
			}
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "settings.json")
			const saved = `{"security":{"auth":{"selectedType":"oauth-personal"}}}`
			if err := os.WriteFile(path, []byte(saved), 0600); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "launched")
			call, _ := newSignInServer(t, func(m *Module, bin string) {
				m.SetLoginHome(func(context.Context, model.TenantID, string, string) (string, string, error) {
					return home, config, nil
				})
				if err := os.WriteFile(filepath.Join(bin, "gemini-cli"), []byte(fmt.Sprintf("#!/bin/sh\nprintf launched > %q\n", marker)), 0755); err != nil {
					t.Fatal(err)
				}
			})
			status, body := call("POST", "/v1/m/agenttools/sign-in", map[string]string{"driver": "gemini-cli"})
			errBody, _ := body["error"].(map[string]any)
			if status != 409 || errBody["code"] != "account_login_refused" {
				t.Fatalf("saved OAuth start = %d %v", status, body)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("login child ran: %v", err)
			}
			if b, err := os.ReadFile(path); err != nil || string(b) != saved {
				t.Fatalf("native settings changed: %v", err)
			}
		})
	}
}

func TestGeminiSavedGoogleLoginUsesExactKeysAndWorkingDirectory(t *testing.T) {
	config, home := t.TempDir(), t.TempDir()
	dir := filepath.Join(home, ".gemini")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`{"security":{"auth":{"selectedType":"oauth-personal","SelectedType":""}}}`, `{"security":{"auth":{"selectedType":"oauth-personal"}}}`} {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if err := geminiSignInStartRefusal(config, home); err != errGeminiSavedGoogleLogin {
			t.Fatalf("startup OAuth bypass = %v", err)
		}
	}
}
