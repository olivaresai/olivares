// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/engine/enginetest"
)

// The native OpenCode ChatGPT method skips terminal selections, prints a device
// URL/code and writes its own auth.json. No vendor account is authenticated here.
const openCodeLoginFixture = `#!/bin/sh
[ "$PWD" = "$HOME" ] || exit 3
[ "$XDG_DATA_HOME" = "$HOME/.local/share" ] || exit 4
case "$1 $2" in
"auth login")
 [ "$3 $4 $5" = "--provider openai --method" ] || exit 5
 [ "$6" = "ChatGPT Pro/Plus (headless)" ] || exit 6
 printf '●  Go to: https://auth.openai.com/codex/device\n●  Enter code: ABCD-12345\n'
 sleep 1
 mkdir -p "$XDG_DATA_HOME/opencode"
 echo '{"openai":{"type":"oauth","refresh":"fixture","access":"fixture","expires":0}}' > "$XDG_DATA_HOME/opencode/auth.json"
 exit 0;;
"auth list")
 if [ -f "$XDG_DATA_HOME/opencode/auth.json" ]; then printf '●  OpenAI oauth\n└  1 credentials\n'; else printf '└  0 credentials\n'; fi
 exit 0;;
esac
exit 2
`

func TestOpenCodeDefaultLoginAndSessionShareNativeHome(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) { testOpenCodeDefaultLogin(t, backend, false) })
	}
}

// A retained pre-change profile already owns the default config directory, but
// its immutable HOME lives under profile-homes. Login and automatic resolution
// must both use that identity, without moving its homes or copying credentials.
func TestOpenCodeDefaultLoginReusesPreChangeProfile(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) { testOpenCodeDefaultLogin(t, backend, true) })
	}
}

func testOpenCodeDefaultLogin(t *testing.T, backend string, retained bool) {
	t.Helper()
	for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION", envSessionTokenFile, envSessionRuntimeWIF} {
		t.Setenv(name, "")
	}
	data, bin := t.TempDir(), filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(bin, []byte(openCodeLoginFixture), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envSessionOpenCodeBin, bin)
	cfg := bootConfig{DataDir: data, Engine: "sqlite", Version: version, Logger: discardLogger(), ApplyModuleProfile: true}
	if backend == "postgres" {
		pg := enginetest.IsolatedPostgresSplitOwner(t)
		cfg.Engine, cfg.DSN, cfg.OwnerDSN = "postgres", pg.App, pg.Owner
	}
	eng, err := boot(t.Context(), cfg)
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
	created := call("POST", "/v1/setup", map[string]any{"token": setup, "email": "opencode@olivares.ai", "password": "fixture-password-2026!"}, 201)
	login := call("POST", "/v1/auth/login", map[string]any{"email": "opencode@olivares.ai", "password": "fixture-password-2026!"}, 200)
	token = login["token"].(string)
	tenant = created["organization"].(map[string]any)["tenant_id"].(string)
	server := httptest.NewServer(eng.api.Handler())
	t.Cleanup(server.Close)
	wantHome := filepath.Join(data, toolLoginsDir, tenant, "opencode")
	var retainedRef string
	var retainedUpdatedAt string
	if retained {
		configHome := filepath.Join(wantHome, ".config", "opencode")
		wantHome = filepath.Join(data, "profile-homes", "ppf_retained")
		for _, dir := range []string{configHome, wantHome} {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		old := call("POST", "/v1/m/sessions/provider-profiles", map[string]any{
			"driver": "opencode", "auth_source": "provider_account_home",
			"config_home": configHome, "user_home": wantHome, "display_name": "Retained OpenCode",
		}, 201)
		retainedRef, retainedUpdatedAt = old["profile_ref"].(string), old["updated_at"].(string)
		if st := call("GET", "/v1/m/agenttools/sign-in?driver=opencode&tenant_id="+url.QueryEscape(tenant), nil, 200); st["signed_in"] != false {
			t.Fatalf("unsigned retained profile status = %v", st)
		}
	}
	out, stderr, err := execSessionCLI(t, nil, "tool", "login", "opencode", "--server", server.URL, "--token", token, "--tenant", tenant)
	if err != nil {
		t.Fatalf("tool login opencode: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "https://auth.openai.com/codex/device") || !strings.Contains(out, "ABCD-12345") || !strings.Contains(out, "OpenCode is signed in.") {
		t.Fatalf("login output = %s", out)
	}
	status := call("GET", "/v1/m/agenttools/sign-in?driver=opencode&tenant_id="+url.QueryEscape(tenant), nil, 200)
	if status["signed_in"] != true {
		t.Fatalf("status = %v", status)
	}
	resolved := call("POST", "/v1/m/sessions/provider-profiles/resolve", map[string]any{"driver": "opencode"}, 200)
	if resolved["reason"] != "own_login" {
		t.Fatalf("resolve = %v", resolved)
	}
	profile := resolved["profile"].(map[string]any)
	ref := profile["profile_ref"].(string)
	if retained && (ref != retainedRef || resolved["created"] != false || profile["updated_at"] != retainedUpdatedAt) {
		t.Fatalf("retained identity was replaced or changed: before %s at %s, resolve %v", retainedRef, retainedUpdatedAt, resolved)
	}
	configuration := call("GET", "/v1/m/sessions/provider-profiles/"+url.PathEscape(ref)+"/configuration", nil, 200)
	home := configuration["user_home"].(string)
	if home != wantHome {
		t.Fatalf("session HOME %q cannot see native login in %q", home, wantHome)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "opencode", "auth.json")); err != nil {
		t.Fatalf("session cannot see the tool's login: %v", err)
	}
	if retained {
		if got := configuration["config_home"]; got != filepath.Join(data, toolLoginsDir, tenant, "opencode", ".config", "opencode") {
			t.Fatalf("retained configuration home changed: %v", configuration)
		}
		if _, err := os.Stat(filepath.Join(data, toolLoginsDir, tenant, "opencode", ".local", "share", "opencode", "auth.json")); !os.IsNotExist(err) {
			t.Fatalf("default login wrote into another native HOME: %v", err)
		}
		if st := call("GET", "/v1/m/agenttools/sign-in?driver=opencode&tenant_id="+url.QueryEscape(tenant)+"&account_ref="+url.QueryEscape(ref), nil, 200); st["signed_in"] != true {
			t.Fatalf("default and retained-profile status disagree: %v", st)
		}
	}
	// A separately named account uses its own HOME for login and sessions.
	account := call("POST", "/v1/m/sessions/provider-accounts", map[string]any{"driver": "opencode", "name": "opencode-b"}, 201)
	accountRef := account["account_ref"].(string)
	accountStatus := "/v1/m/agenttools/sign-in?driver=opencode&tenant_id=" + url.QueryEscape(tenant) + "&account_ref=" + url.QueryEscape(accountRef)
	if st := call("GET", accountStatus, nil, http.StatusOK); st["signed_in"] != false {
		t.Fatalf("new account reused default login: %v", st)
	}
	out, stderr, err = execSessionCLI(t, nil, "tool", "login", "opencode", "--account", "opencode-b", "--server", server.URL, "--token", token, "--tenant", tenant)
	if err != nil {
		t.Fatalf("named login: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "OpenCode is signed in.") {
		t.Fatalf("named login output = %s", out)
	}
	named := call("GET", "/v1/m/sessions/provider-profiles/"+url.PathEscape(accountRef)+"/configuration", nil, 200)
	namedHome := named["user_home"].(string)
	if namedHome == home {
		t.Fatal("named account shares the default login HOME")
	}
	if _, err := os.Stat(filepath.Join(namedHome, ".local", "share", "opencode", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if st := call("GET", accountStatus, nil, 200); st["signed_in"] != true {
		t.Fatalf("named status = %v", st)
	}
	if retained {
		// The default lookup must keep the retained profile's launch guards:
		// replacing its HOME cannot redirect a status read or a new login.
		if err := os.Rename(home, home+"-retained"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), home); err != nil {
			t.Fatal(err)
		}
		call("GET", "/v1/m/agenttools/sign-in?driver=opencode&tenant_id="+url.QueryEscape(tenant), nil, http.StatusConflict)
		call("POST", "/v1/m/agenttools/sign-in", map[string]any{"driver": "opencode", "tenant_id": tenant}, http.StatusConflict)
	}
}
