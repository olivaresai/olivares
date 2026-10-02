// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
)

// The default PostgreSQL install has the application and owner roles and no
// BYPASSRLS admin pool, so a cross-tenant org read is refused. On it the sessions
// module starts, the active kill-switch sweep stops a running session, and the
// sessions work outbox ticks without a failure: both enumerate the tenants this
// install can read (servedWorkTenants). Real PostgreSQL, real boot, real API, a
// protocol stub for the agent CLI.
func TestDefaultPostgresSessionsStartAndTheKillSwitchStopsASession(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	folder, home, dir := t.TempDir(), t.TempDir(), t.TempDir()
	for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION", envSessionTokenFile, envSessionRuntimeWIF} {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(envSessionKillSwitchSweep, "100ms")
	t.Setenv(workOutboxPumpIntervalEnv, "200ms")
	agent := filepath.Join(folder, "agent.py")
	stub := "#!" + python + "\nimport json, sys\n" +
		"print(json.dumps({'type':'system','subtype':'init','session_id':'pg-stub'}), flush=True)\n" +
		"for line in sys.stdin:\n" +
		"    print(json.dumps({'type':'result','subtype':'success','is_error':False,'result':'ok','session_id':'pg-stub'}), flush=True)\n"
	if err := os.WriteFile(agent, []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envSessionClaudeBin, agent)

	logs := &loopLog{}
	eng, err := boot(t.Context(), bootConfig{DataDir: dir, Engine: "postgres", DSN: pg.App, OwnerDSN: pg.Owner,
		Version: version, Logger: slog.New(logs), ApplyModuleProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if got := moduleStatus(eng, "olivares.sessions"); got != runtime.StatusRunning {
		t.Fatalf("sessions runtime status = %q, want running", got)
	}
	if _, ok := logs.find("active kill-switch sweep started", ""); !ok {
		t.Fatal("the active kill-switch sweep did not start")
	}
	pep, err := buildClaudeHookPEPServer(eng, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pep.Close() })
	if err := eng.hookCredentials().bindEndpoint(listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	go func() { _ = pep.Serve(listener) }()

	var admin, tenant string
	do := func(method, path string, body any, status ...int) map[string]any {
		t.Helper()
		code, result, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, admin, tenant, body)
		if !slices.Contains(status, code) {
			t.Fatalf("%s %s = %d, want %v: %s", method, path, code, status, raw)
		}
		return result
	}
	setup, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	setupResult := do("POST", "/v1/setup", map[string]any{"token": setup, "email": "pg@olivares.ai", "password": "fixture-password-2026!"}, http.StatusCreated)
	login := do("POST", "/v1/auth/login", map[string]any{"email": "pg@olivares.ai", "password": "fixture-password-2026!"}, http.StatusOK)
	admin, _ = login["token"].(string)
	org, _ := setupResult["organization"].(map[string]any)
	tenant, _ = org["tenant_id"].(string)

	// The tenant set this install can read names the new tenant.
	served, err := servedWorkTenants(t.Context(), eng.store)
	if err != nil || !slices.Equal(served, []model.TenantID{model.TenantID(tenant)}) {
		t.Fatalf("served work tenants = %v (%v), want [%s]", served, err, tenant)
	}

	ws := do("POST", "/v1/m/sessions/workspaces", map[string]any{"root_path": folder, "name": "folder"}, http.StatusCreated)
	profile := do("POST", "/v1/m/sessions/provider-profiles", map[string]any{"driver": "claude", "config_home": home, "user_home": home, "auth_source": "provider_account_home", "display_name": "stub"}, http.StatusCreated)
	run := do("POST", "/v1/m/sessions/runs", map[string]any{"name": "pg default", "transport": "stream-json", "permission_mode": "default", "isolation": "native", "workspace_ref": ws["workspace_ref"], "provider_profile_ref": profile["profile_ref"]}, http.StatusCreated)
	if run["state"] != "running" {
		t.Fatalf("launch state = %v, want running", run["state"])
	}
	runRef, _ := run["run_ref"].(string)

	do("POST", "/v1/m/governance/killswitch", map[string]any{"scope_kind": "estate", "reason": "default postgres proof"}, http.StatusCreated, http.StatusOK)
	deadline := time.Now().Add(15 * time.Second)
	for {
		got := do("GET", "/v1/m/sessions/runs/"+runRef, nil, http.StatusOK)
		if got["state"] == "stopped" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run state = %v 15 s after the estate stop, want stopped by the kill-switch sweep", got["state"])
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The work outbox ticks without a failure, both called and on its schedule.
	if err := eng.communicationPump.runOnce(t.Context()); err != nil {
		t.Fatalf("work outbox tick = %v, want nil", err)
	}
	time.Sleep(time.Second)
	logs.mu.Lock()
	defer logs.mu.Unlock()
	for _, r := range logs.recs {
		if r.level >= slog.LevelWarn && (strings.Contains(r.msg, "periodic job failed") || strings.Contains(r.msg, "component failed") ||
			strings.Contains(r.msg, "sessions-work-outbox: cannot enumerate") || strings.Contains(r.msg, "waiting for an approval")) {
			t.Errorf("failure logged on the default PostgreSQL install: %s %v", r.msg, r.attrs)
		}
	}
}
