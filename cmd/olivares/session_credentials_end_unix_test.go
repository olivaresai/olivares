// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine/enginetest"
)

// TestSessionCredentialsEndWithTheSession pins that every credential a launch
// hands its child (the hook PEP bearer, which is also the MCP and inference proxy
// credential, the work token and the communication token) stops authenticating
// once the session ends: when the child exits on its own, when an operator stops
// it, and when the estate kill switch kills it. The child is a real process that
// passes its credentials back over a loopback connection, so they never touch the
// disk. The communication token is minted only where the engine has communication
// credentials on, which this fixture's SQLite boot has; at least one case must
// check it.
func TestSessionCredentialsEndWithTheSession(t *testing.T) {
	communicationChecked := false
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, end := range []string{"exit", "stop", "kill"} {
			t.Run(backend+"/"+end, func(t *testing.T) {
				if testSessionCredentialsEnd(t, backend, end) {
					communicationChecked = true
				}
			})
		}
	}
	if !communicationChecked {
		t.Error("no case was given a communication token, so its end is not checked")
	}
}

// testSessionCredentialsEnd reports whether the launch carried a communication
// token, and so whether its end was checked.
func testSessionCredentialsEnd(t *testing.T, backend, end string) bool {
	// The child hands its credentials back over a loopback connection, the channel
	// a confined child keeps, and then blocks reading it: closing the connection is
	// how the "exit" case tells it to finish on its own.
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal("this qualification needs bash for its /dev/tcp fixture")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	agent := filepath.Join(t.TempDir(), "credential-agent.sh")
	fixture := "#!" + bash + "\ntrap 'exit 0' TERM\n" +
		"exec 3<>/dev/tcp/127.0.0.1/" + port + "\n" +
		"printf '%s\\n%s\\n%s\\n' \"$OLIVARES_HOOK_PEP_TOKEN\" \"$OLIVARES_WORK_TOKEN\" \"$OLIVARES_COMMUNICATION_TOKEN\" >&3\n" +
		"printf '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"credential-end\"}\\n'\n" +
		"read -r _ <&3\nexit 0\n"
	if err := os.WriteFile(agent, []byte(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION", envSessionTokenFile, envSessionRuntimeWIF} {
		t.Setenv(name, "")
	}
	t.Setenv(envSessionClaudeBin, agent)
	// The kill switch reclaims running sessions on its sweep; a short one keeps the
	// "kill" case from waiting out the 15 s production default.
	t.Setenv(envSessionKillSwitchSweep, "250ms")
	cfg := bootConfig{DataDir: t.TempDir(), Engine: backend, Version: "test", Logger: discardLog()}
	if backend == "postgres" {
		pg := enginetest.IsolatedPostgresSplitOwner(t)
		cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = pg.App, pg.Owner, pg.Admin
	}
	eng, err := boot(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if _, err := eng.authr.BootstrapSuperadmin(t.Context(), "credential-end@example.invalid", "credential-end-fixture-123"); err != nil {
		t.Fatal(err)
	}
	admin, _, err := eng.authr.Login(t.Context(), "credential-end@example.invalid", "credential-end-fixture-123", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	var tenant string
	do := func(method, path string, body any, want ...int) map[string]any {
		t.Helper()
		code, result, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, admin, tenant, body)
		for _, w := range want {
			if code == w {
				return result
			}
		}
		t.Fatalf("%s %s: HTTP %d, want %v: %s", method, path, code, want, raw)
		return nil
	}
	org := do(http.MethodPost, "/v1/system/orgs", map[string]any{"name": "Credential end", "slug": "credential-end"}, http.StatusCreated)
	tenant, _ = org["tenant_id"].(string)
	if tenant == "" {
		t.Fatalf("org create returned no tenant: %v", org)
	}
	pep, err := buildClaudeHookPEPServer(eng, discardLog())
	if err != nil || pep == nil {
		t.Fatalf("build launch hook listener: %v", err)
	}
	hooks := httptest.NewServer(pep.Handler)
	t.Cleanup(hooks.Close)
	if err := eng.hookCredentials().bindEndpoint(hooks.Listener.Addr().String()); err != nil {
		t.Fatal(err)
	}

	type handback struct {
		conn  net.Conn
		creds []string
	}
	received := make(chan handback, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			received <- handback{}
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		r := bufio.NewReader(conn)
		var creds []string
		for range 3 {
			line, err := r.ReadString('\n')
			if err != nil {
				break
			}
			creds = append(creds, strings.TrimSuffix(line, "\n"))
		}
		received <- handback{conn: conn, creds: creds}
	}()
	home := t.TempDir()
	workspace := do(http.MethodPost, "/v1/m/sessions/workspaces", map[string]any{"root_path": t.TempDir(), "name": "credential folder"}, http.StatusCreated)
	profile := do(http.MethodPost, "/v1/m/sessions/provider-profiles", map[string]any{"driver": "claude", "auth_source": "provider_account_home", "config_home": home, "user_home": home, "display_name": "credential fixture"}, http.StatusCreated)
	run := do(http.MethodPost, "/v1/m/sessions/runs", map[string]any{"name": "Credential end", "transport": "stream-json", "permission_mode": "plan", "isolation": "native", "workspace_ref": workspace["workspace_ref"], "provider_profile_ref": profile["profile_ref"]}, http.StatusCreated)
	runRef, _ := run["run_ref"].(string)
	if run["state"] != "running" || runRef == "" {
		t.Fatalf("fixture did not launch: %v", run)
	}
	var got handback
	select {
	case got = <-received:
	case <-time.After(10 * time.Second):
		t.Fatal("the child did not hand back its credentials")
	}
	if got.conn != nil {
		t.Cleanup(func() { _ = got.conn.Close() })
	}
	creds := got.creds
	// The hook bearer and the work token are minted for every launch; the
	// communication token exactly when the engine has communication credentials on.
	if len(creds) != 3 || creds[0] == "" || creds[1] == "" {
		t.Fatalf("the launch did not provision its hook bearer and work token (%d lines)", len(creds))
	}
	if !strings.HasPrefix(creds[0], sessionHookTokenPrefix) {
		t.Fatal("OLIVARES_HOOK_PEP_TOKEN is not a session bearer")
	}
	type credential struct {
		name  string
		check func() error
	}
	authenticate := func(token string) func() error {
		return func() error { _, err := eng.authr.Authenticate(t.Context(), token); return err }
	}
	credentials := []credential{
		{"hook PEP bearer", func() error { _, err := eng.sessionHooks.Authenticate(t.Context(), creds[0]); return err }},
		{"work token", authenticate(creds[1])},
	}
	communication := eng.sessionsMod.CommunicationSessionCredentialsEnabled()
	if communication != (creds[2] != "") {
		t.Fatalf("communication credentials on = %v, but the launch was given a communication token = %v", communication, creds[2] != "")
	}
	if communication {
		credentials = append(credentials, credential{"communication token", authenticate(creds[2])})
	}
	check := func(when string, wantLive bool) {
		t.Helper()
		for _, c := range credentials {
			err := c.check()
			if wantLive && err != nil {
				t.Fatalf("%s: the %s is refused while the session runs: %v", when, c.name, err)
			}
			if !wantLive && err == nil {
				t.Errorf("%s: the %s still authenticates after the session ended", when, c.name)
			}
			// Ended access and expiry both wrap ErrUnauthenticated; a store fault does not.
			if !wantLive && err != nil && !errors.Is(err, auth.ErrUnauthenticated) {
				t.Errorf("%s: the %s is refused for another reason than an ended credential: %v", when, c.name, err)
			}
		}
	}
	check("running", true)

	switch end {
	case "exit":
		if err := got.conn.Close(); err != nil {
			t.Fatal(err)
		}
	case "stop":
		do(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/stop", nil, http.StatusOK)
	case "kill":
		do(http.MethodPost, "/v1/m/governance/killswitch", map[string]any{"scope_kind": "estate", "reason": "credential end proof"}, http.StatusOK, http.StatusCreated)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		state := do(http.MethodGet, "/v1/m/sessions/runs/"+runRef, nil, http.StatusOK)["state"]
		if state != "running" && state != "starting" && state != "stopping" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the session did not end after %s (state %v)", end, state)
		}
		time.Sleep(20 * time.Millisecond)
	}
	check("after "+end, false)
	return communication
}
