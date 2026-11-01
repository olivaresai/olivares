// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cursorLoginFixture = `#!/bin/sh
[ "$PWD" = "$HOME" ] || exit 3
[ -n "$CURSOR_CONFIG_DIR" ] || exit 4
[ "$AGENT_CLI_CREDENTIAL_STORE" = file ] || exit 5
[ "$NO_OPEN_BROWSER" = 1 ] || exit 6
case "$1" in
login)
  printf 'Open a browser and navigate to this link: https://cursor.com/loginDeepControl?challenge=stub\n'
  mkdir -p "$HOME/.config/cursor"
  echo '{}' > "$HOME/.config/cursor/auth.json"
  exit 0;;
status)
  [ "$2 $3" = "--format json" ] || exit 7
  if [ -f "$HOME/.config/cursor/auth.json" ]; then
    printf '{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true}\n'
  else
    printf '{"status":"unauthenticated","isAuthenticated":false,"hasAccessToken":false,"hasRefreshToken":false}\n'
  fi
  exit 0;;
esac
exit 2
`

// A retained Cursor profile owns the default config directory but has its own
// immutable HOME. The composition root must make login and session resolution
// share that HOME, or the session cannot see the credential the login just wrote.
func TestCursorDefaultLoginReusesRetainedProfileHome(t *testing.T) {
	for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION", envSessionTokenFile, envSessionRuntimeWIF} {
		t.Setenv(name, "")
	}
	data, bin := t.TempDir(), filepath.Join(t.TempDir(), "cursor")
	if err := os.WriteFile(bin, []byte(cursorLoginFixture), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envSessionCursorBin, bin)
	eng, err := boot(t.Context(), bootConfig{DataDir: data, Engine: "sqlite", Version: version, Logger: discardLogger(), ApplyModuleProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	var token, tenant string
	call := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		status, result, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, token, tenant, body)
		if status != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, status, want, raw)
		}
		return result
	}
	setup, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	created := call("POST", "/v1/setup", map[string]any{"token": setup, "email": "cursor@olivares.ai", "password": "fixture-password-2026!"}, 201)
	login := call("POST", "/v1/auth/login", map[string]any{"email": "cursor@olivares.ai", "password": "fixture-password-2026!"}, 200)
	token = login["token"].(string)
	tenant = created["organization"].(map[string]any)["tenant_id"].(string)
	server := httptest.NewServer(eng.api.Handler())
	t.Cleanup(server.Close)

	defaultHome := filepath.Join(data, toolLoginsDir, tenant, "cursor")
	retainedHome := filepath.Join(data, "profile-homes", "ppf_retained")
	for _, dir := range []string{filepath.Join(defaultHome, ".cursor"), retainedHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := call("POST", "/v1/m/sessions/provider-profiles", map[string]any{
		"driver": "cursor", "auth_source": "provider_account_home",
		"config_home": filepath.Join(defaultHome, ".cursor"), "user_home": retainedHome,
		"display_name": "Retained Cursor",
	}, 201)
	retainedRef := old["profile_ref"].(string)

	out, stderr, err := execSessionCLI(t, nil, "tool", "login", "cursor", "--server", server.URL, "--token", token, "--tenant", tenant)
	if err != nil {
		t.Fatalf("tool login cursor: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "https://cursor.com/loginDeepControl") || !strings.Contains(out, "Cursor CLI is signed in.") {
		t.Fatalf("login output = %s", out)
	}
	resolved := call("POST", "/v1/m/sessions/provider-profiles/resolve", map[string]any{"driver": "cursor"}, 200)
	profile := resolved["profile"].(map[string]any)
	if resolved["reason"] != "own_login" || profile["profile_ref"] != retainedRef || resolved["created"] != false {
		t.Fatalf("resolve = %v, want retained profile %s", resolved, retainedRef)
	}
	configuration := call("GET", "/v1/m/sessions/provider-profiles/"+url.PathEscape(retainedRef)+"/configuration", nil, 200)
	if configuration["user_home"] != retainedHome {
		t.Fatalf("session HOME = %v, want %s", configuration["user_home"], retainedHome)
	}
	if _, err := os.Stat(filepath.Join(retainedHome, ".config", "cursor", "auth.json")); err != nil {
		t.Fatalf("session cannot see the login written by the tool: %v", err)
	}
	if _, err := os.Stat(filepath.Join(defaultHome, ".config", "cursor", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("login wrote into the fresh default HOME instead of the retained one: %v", err)
	}
}
