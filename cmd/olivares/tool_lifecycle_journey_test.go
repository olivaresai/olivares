// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//go:build unix

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/agenttoolsapi"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/driverfacts"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// This journey uses labelled, locally signed protocol stand-ins, never vendor accounts.
// The real CLI, HTTP handlers, SQLite transactions, installer, verifier and native child run.
func TestToolLifecycleJourneyStartsReceiptBytesAndAuditsRemoval(t *testing.T) {
	ctx := t.Context()
	fixtureHome, fixtureBin := t.TempDir(), t.TempDir()
	commands := []string{"gpg", "gpgconf", "sha256sum", "sh"}
	if os.Getenv("OLIVARES_TOOL_JOURNEY_BROWSER") == "1" {
		commands = append(commands, "node", "pnpm", "dirname", "sed", "uname")
		// Keep installed package-manager software visible while vendor homes
		// stay isolated. Starting this journey never downloads Corepack tools.
		corepackHome := os.Getenv("COREPACK_HOME")
		if corepackHome == "" {
			cache, err := os.UserCacheDir()
			if err != nil {
				t.Fatal(err)
			}
			corepackHome = filepath.Join(cache, "node", "corepack")
		}
		t.Setenv("COREPACK_HOME", corepackHome)
		t.Setenv("COREPACK_ENABLE_NETWORK", "0")
	}
	for _, name := range commands {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(fixtureBin, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", fixtureHome)
	t.Setenv("PATH", fixtureBin)
	for _, facts := range driverfacts.All() {
		if facts.ConfigHomeEnv != "" {
			t.Setenv(facts.ConfigHomeEnv, "")
		}
	}
	f := newAgentToolFixture(t)
	installer := toolInstallEngine(ctx)
	resolve := func(driver string) string {
		in, ok, _ := installer.LatestInstalled(context.Background(), f.root, driver)
		if ok {
			return in.Executable
		}
		facts, _ := driverfacts.Lookup(driver)
		return facts.Program
	}
	runner, err := sessions.NewOfficialRunner("claude")
	if err != nil {
		t.Fatal(err)
	}
	sm := sessions.New(sessions.WithRunner(runner), sessions.WithSessionWorkspaceRoot(t.TempDir()), sessions.WithStopWaitDelay(2*time.Second), sessions.WithCredentialSource(sessions.CredentialSourceFunc(func(context.Context, sessions.CredentialRequest) (sessions.Credential, error) {
		return sessions.Credential{ID: "fixture", Token: "synthetic-only", Scheme: "fixture", NotAfter: time.Now().Add(time.Hour)}, nil
	})), sessions.WithProgramResolver(resolve, func(ctx context.Context, driver, version string) (string, error) {
		in, err := installer.InstalledVersion(ctx, f.root, driver, version)
		return in.Executable, err
	}))
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, sm.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.System(ctx, func(sc store.SystemScope) error { _, err := sc.EnsureSystemTenant(ctx); return err }); err != nil {
		t.Fatal(err)
	}
	sm.UseData(api.NewModuleData(st))
	authenticator := auth.NewAuthenticator(st, nil)
	sm.UseStanding(authenticator)
	defer sm.Stop(context.Background())
	sm.UseExecutionEnvironmentRef("env_00000000-0000-0000-0000-000000000001")
	sm.UseToolLoginsRoot(filepath.Join(fixtureHome, "tool-logins"))
	tm, err := agenttoolsapi.New(ctx, installer, f.root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close()
	tm.SetSessions(sm)
	tm.SetProgramResolver(resolve)
	tm.SetChildCommand(confinedToolCommand(os.Getenv("OLIVARES_DATA_DIR")))
	tm.SetLoginHome(func(ctx context.Context, tenant model.TenantID, driver, account string) (string, string, error) {
		if account != "" {
			return sm.ProviderLoginHome(ctx, tenant, driver, account)
		}
		return sm.DefaultToolLoginHome(ctx, tenant, driver)
	})
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	setupToken := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	setupCredential, _, err := setupToken.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	server, err := api.New(api.Options{Store: st, Authenticator: authenticator, Authorizer: auth.NewAuthorizer(nil), PrincipalEvidenceProducer: authenticator, Signer: signer, SetupToken: setupToken, Modules: []api.Module{sm, tm}})
	if err != nil {
		t.Fatal(err)
	}
	// Community first boot provisions the tenant and its owner; managing
	// additional organizations through /v1/system/orgs is Enterprise-only.
	code, setup, raw := doDemoViewJSON(t, server.Handler(), "POST", "/v1/setup", "", "", map[string]any{"token": setupCredential, "email": "tools@olivares.invalid", "password": "fixture-password-2026!"})
	if code != http.StatusCreated {
		t.Fatalf("POST /v1/setup = %d %s", code, raw)
	}
	org, _ := setup["organization"].(map[string]any)
	tenant, _ := org["tenant_id"].(string)
	if tenant == "" {
		t.Fatalf("setup response has no tenant: %s", raw)
	}
	token, _, err := authenticator.Login(ctx, "tools@olivares.invalid", "fixture-password-2026!", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, tenant string, body any, want int) map[string]any {
		t.Helper()
		code, result, raw := doDemoViewJSON(t, server.Handler(), method, path, token, tenant, body)
		if code != want {
			t.Fatalf("%s %s = %d %s", method, path, code, raw)
		}
		return result
	}
	srv := httptest.NewServer(server.Handler())
	defer srv.Close()
	cli := func(args ...string) {
		t.Helper()
		args = append(args, "--server", srv.URL, "--token", token, "--tenant", tenant)
		out, stderr, err := execSessionCLI(t, nil, args...)
		if err != nil {
			t.Fatalf("CLI %s = %v %s%s", args[0], err, out, stderr)
		}
	}
	publish := func(version string) {
		body := fmt.Sprintf(`#!/bin/sh
# Labelled protocol stand-in for tool lifecycle qualification.
if [ "$1" = "--version" ]; then printf '%%s (Claude Code)\n' '%s'; exit 0; fi
if [ "$1" = "auth" ]; then printf '{"loggedIn":false}\n'; exit 0; fi
trap 'exit 0' TERM
sha256sum "$0" > ./started.sha256
printf '{"type":"system","subtype":"init","session_id":"fixture-session"}\n'
while IFS= read -r line; do printf '{"type":"assistant","echo":true}\n'; done
`, version)
		f.srv.Publish(t, f.key, version, map[string][]byte{"linux-x64": []byte(body)})
		f.srv.SetPointer("latest", version)
	}
	publish("2.1.261")
	cli("tool", "install", "claude", "--version", "2.1.261")
	publish("2.1.262")
	cli("tool", "update", "claude")
	call("GET", agentToolsPath+"/inventory", "", nil, 200)
	workspaceDir := t.TempDir()
	workspace := call("POST", "/v1/m/sessions/workspaces", tenant, map[string]any{"name": "Research", "root_path": workspaceDir}, 201)
	ref := workspace["workspace_ref"].(string)
	// This is exactly the console's save envelope, with explicit tenant selection on the system door.
	browser := func(string) {}
	if os.Getenv("OLIVARES_TOOL_JOURNEY_BROWSER") == "1" {
		browser = toolJourneyBrowser(t, srv.URL, tenant, ref)
		browser("pin")
	} else {
		call("PUT", agentToolsPath+"/pins", "", map[string]any{"tenant_id": tenant, "driver": "claude", "workspace_ref": ref, "version": "2.1.261"}, 200)
	}
	profile, err := sm.CreateProfile(ctx, model.TenantID(tenant), sessions.CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(), DisplayName: "Journey"})
	if err != nil {
		t.Fatal(err)
	}
	run := call("POST", "/v1/m/sessions/runs", tenant, map[string]any{"provider_profile_ref": profile.Ref, "workspace_ref": ref, "isolation": "native"}, 201)
	runRef := run["run_ref"].(string)
	receipt, err := installer.InstalledVersion(ctx, f.root, "claude", "2.1.261")
	if err != nil {
		t.Fatal(err)
	}
	var started []byte
	for deadline := time.Now().Add(5 * time.Second); ; {
		started, err = os.ReadFile(filepath.Join(workspaceDir, "started.sha256"))
		if err == nil {
			break
		}
		if !os.IsNotExist(err) || time.Now().After(deadline) {
			t.Fatalf("native child receipt marker: %v", err)
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if fields := strings.Fields(string(started)); len(fields) != 2 || fields[0] != receipt.SHA256 || fields[1] != receipt.Executable {
		t.Fatalf("started bytes %q differ from verified receipt %+v", started, receipt)
	}
	browser("remove-in-use")
	denied := call("DELETE", agentToolsPath+"/releases", "", map[string]any{"driver": "claude", "version": "2.1.261"}, 409)
	if denied["error"].(map[string]any)["code"] != "tool_in_use" {
		t.Fatalf("removal refusal=%v", denied)
	}
	call("POST", "/v1/m/sessions/runs/"+runRef+"/stop", tenant, nil, 200)
	if os.Getenv("OLIVARES_TOOL_JOURNEY_BROWSER") == "1" {
		browser("remove-stopped")
	} else {
		cli("tool", "rm", "claude", "--version", "2.1.261")
	}
	if _, err := os.Stat(receipt.Executable); !os.IsNotExist(err) {
		t.Fatalf("removed binary remains: %v", err)
	}
	call("POST", "/v1/m/sessions/runs", tenant, map[string]any{"provider_profile_ref": profile.Ref, "workspace_ref": ref, "isolation": "native"}, 400)
	if os.Getenv("OLIVARES_TOOL_JOURNEY_BROWSER") == "1" {
		browser("clear")
	} else {
		cli("tool", "pin", "claude", "--workspace", ref, "--clear")
	}
	if err := st.View(ctx, model.SystemTenantID, func(sc store.Scope) error {
		walker := sc.Audit().(store.CanonicalWalker)
		completed := 0
		if err := walker.WalkCanonical(ctx, 1, func(ev model.AuditEvent, canonical string, _ []byte) error {
			if ev.Action == "agenttools.remove.completed" {
				completed++
				meta := map[string]any{}
				if err := json.Unmarshal([]byte(canonical), &meta); err != nil {
					return err
				}
				if meta["sha256"] != receipt.SHA256 {
					t.Fatal("removal digest not audited")
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if completed != 1 {
			t.Fatalf("completion audits=%d", completed)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("signed receipt sha256=%s equals real child's bytes; in-use=409/tool_in_use; stopped removal succeeds and is audited", receipt.SHA256)
}

func toolJourneyBrowser(t *testing.T, apiURL, tenant, workspace string) func(string) {
	t.Helper()
	web, err := filepath.Abs("../../web")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "COREPACK_HOME=" + os.Getenv("COREPACK_HOME"), "COREPACK_ENABLE_NETWORK=0", "VITE_API_TARGET=" + apiURL, "PLAYWRIGHT_NO_COPY_PROMPT=1"}
	if browsers := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); browsers != "" {
		env = append(env, "PLAYWRIGHT_BROWSERS_PATH="+browsers)
	}
	vite := exec.CommandContext(t.Context(), "pnpm", "exec", "vite", "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--strictPort")
	vite.Dir, vite.Env, vite.SysProcAttr = web, env, &syscall.SysProcAttr{Setpgid: true}
	output, err := os.CreateTemp(t.TempDir(), "console-startup-*.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	vite.Stdout, vite.Stderr = output, output
	if err := vite.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-vite.Process.Pid, syscall.SIGTERM); _ = vite.Wait() })
	ready := false
	client := &http.Client{Timeout: time.Second}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for !ready {
		select {
		case <-deadline.C:
			startup, _ := os.ReadFile(output.Name())
			t.Fatalf("disposable console did not become ready: %s", startup)
		case <-t.Context().Done():
			t.Fatal("console journey canceled")
		case <-ticker.C:
			resp, err := client.Get(base)
			if err == nil {
				resp.Body.Close()
				ready = resp.StatusCode == 200
			}
		}
	}
	return func(action string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "pnpm", "exec", "playwright", "test", "e2e/tool-lifecycle.spec.ts", "--workers=1")
		cmd.Dir, cmd.Env = web, append(env, "PLAYWRIGHT_BASE_URL="+base, "PLAYWRIGHT_TOOL_LIFECYCLE_ACTION="+action, "PLAYWRIGHT_TOOL_LIFECYCLE_TENANT="+tenant, "PLAYWRIGHT_TOOL_LIFECYCLE_WORKSPACE="+workspace)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("console %s: %v\n%s", action, err, out)
		}
		t.Logf("console %s: PASS", action)
	}
}
