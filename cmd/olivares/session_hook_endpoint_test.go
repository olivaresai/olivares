// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// Start the actual serve path, including boot and its synchronous listener
// acquisition. Each fixture owns every listener until cancellation and drain.
func startHookEndpointEngine(t *testing.T) *engine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan *engine, 1)
	opts := bindAnnounceInsecureOpts(t.TempDir(), "127.0.0.1:0", "127.0.0.1:0")
	opts.seedDemo, opts.quickstart, opts.quiet = true, true, true
	go func() {
		announce := func(ctx context.Context, _ io.Writer, eng *engine, _ consoleAddress) error {
			if _, _, err := eng.authr.BootstrapSuperadminOwning(ctx, demoEmail, demoPassword, eng.demoTenant); err != nil {
				return err
			}
			ready <- eng
			return nil
		}
		// Production re-executes once the seeded modules reconcile their first
		// selection. Re-enter the already-drained serve path with the same options.
		err := runEngine(ctx, io.Discard, opts, announce)
		var restart *selfRestartError
		if ctx.Err() == nil && errors.As(err, &restart) && len(restart.env) == 0 {
			err = runEngine(ctx, io.Discard, opts, announce)
		}
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("engine drain: %v", err)
			}
		case <-time.After(35 * time.Second):
			t.Error("engine did not drain its owned listeners")
		}
	})
	var eng *engine
	select {
	case eng = <-ready:
	case err := <-done:
		// Leave the buffered result available for cleanup as well.
		done <- err
		t.Fatalf("engine startup: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("engine did not reach the bound-listener announcement")
	}
	endpoint := eng.sessionHooks.endpoint()
	host, port, err := net.SplitHostPort(strings.TrimSuffix(strings.TrimPrefix(endpoint, "http://"), "/"))
	if err != nil || host != "127.0.0.1" || port == "0" || port == "" {
		t.Fatalf("engine did not retain an acquired loopback endpoint: %q (%v)", endpoint, err)
	}
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := client.Get(endpoint)
		if err == nil {
			_ = response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bound hook listener did not start serving: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return eng
}

func endpointSessionHook(t *testing.T, eng *engine, run string) (string, string, sessions.LaunchIntent) {
	t.Helper()
	ctx := context.Background()
	tenant := eng.demoTenant
	bearer, _, err := eng.authr.Login(ctx, demoEmail, demoPassword, "hook-endpoint-fixture")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := eng.authr.Authenticate(ctx, bearer)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := eng.sessionsMod.ResolveSession(ctx, tenant, sessions.SessionBinding{Provider: sessions.ProviderOperated, ExternalID: run, Origin: sessions.OriginOperated})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := eng.sessionsMod.Claim(ctx, tenant, sid, principal.Actor(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	intent := sessions.LaunchIntent{PermissionMode: "dontAsk", TemplateBuiltin: true, AllowedTools: []string{"Read", "Edit", "Write", "Bash"}, RunRef: run, Actor: principal.Actor(), ActorKind: principal.ActorKind(), LauncherPrincipal: principal, ClaimSID: sid, Holder: lease.Holder, Fence: lease.Fence}
	env, err := eng.sessionHooks.provisioner().provisionLaunch(ctx, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	spec := sessions.LaunchSpec{Env: env}
	if err := sessions.ConfigureClaudeHookPEP(&spec, eng.dataDir, run, filepath.Join(eng.dataDir, "olivares")); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(eng.dataDir, "run", run, "pep-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string }
		}
	}
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatal(err)
	}
	groups := settings.Hooks["PreToolUse"]
	if len(groups) != 1 || len(groups[0].Hooks) != 1 {
		t.Fatal("protected settings lost the permission hook")
	}
	pieces := strings.SplitN(groups[0].Hooks[0].Command, "--server '", 2)
	if len(pieces) != 2 {
		t.Fatal("protected hook command did not pin its endpoint")
	}
	endpoint := strings.SplitN(pieces[1], "'", 2)[0]
	if endpoint != eng.sessionHooks.endpoint() {
		t.Fatalf("session settings point to %q, engine owns %q", endpoint, eng.sessionHooks.endpoint())
	}
	var token string
	for _, item := range env {
		if item.Name == envHookPEPToken {
			token = item.Value
		}
	}
	if token == "" {
		t.Fatal("session bearer missing")
	}
	return endpoint, token, intent
}

func callEndpointHook(t *testing.T, endpoint, token string, tenant model.TenantID) string {
	t.Helper()
	body := []byte(`{"hook_event_name":"PreToolUse","session_id":"vendor-session","tool_name":"Read","tool_use_id":"endpoint-proof","tool_input":{"file_path":"/tmp/endpoint-proof"}}`)
	var out bytes.Buffer
	if err := claude.RunHookClient(context.Background(), bytes.NewReader(body), &out, claude.HookClientConfig{Endpoint: endpoint, Token: token, Tenant: tenant.String(), Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	var answer struct {
		Output struct {
			Decision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	return answer.Output.Decision
}

func TestDefaultHookPEPEnginesUseSeparateEphemeralEndpoints(t *testing.T) {
	t.Setenv("OLIVARES_HOOK_PEP_CONFIG", "")
	first := startHookEndpointEngine(t)
	second := startHookEndpointEngine(t)
	firstURL, firstToken, firstIntent := endpointSessionHook(t, first, "first-engine-run")
	secondURL, secondToken, secondIntent := endpointSessionHook(t, second, "second-engine-run")
	if firstURL == secondURL {
		t.Fatal("two engines share one hook listener")
	}
	for _, proof := range []struct {
		eng             *engine
		endpoint, token string
		intent          sessions.LaunchIntent
	}{
		{first, firstURL, firstToken, firstIntent}, {second, secondURL, secondToken, secondIntent},
	} {
		if got := callEndpointHook(t, proof.endpoint, proof.token, proof.eng.demoTenant); got != "allow" {
			t.Fatalf("session did not reach its own engine: %s", got)
		}
		found := false
		for _, event := range canonicalLedgerEventsFrom(t, proof.eng.store, proof.eng.demoTenant, 0) {
			if event.event.Action == "hook.tool.allow" && event.meta["session_ref"] == proof.intent.ClaimSID && event.meta["run_ref"] == proof.intent.RunRef {
				found = true
			}
		}
		if !found {
			t.Fatal("own engine did not audit its session's hook")
		}
	}
	if got := callEndpointHook(t, secondURL, firstToken, first.demoTenant); got != "deny" {
		t.Fatalf("another engine accepted the first engine's session bearer: %s", got)
	}
}

func TestDefaultHookPEPStartsWithOldPortOccupied(t *testing.T) {
	t.Setenv("OLIVARES_HOOK_PEP_CONFIG", "")
	occupied, err := net.Listen("tcp", "127.0.0.1:8447")
	if err == nil {
		t.Cleanup(func() { _ = occupied.Close() })
	} else if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatal(err)
	}
	eng := startHookEndpointEngine(t)
	endpoint, token, _ := endpointSessionHook(t, eng, "occupied-port-run")
	if strings.Contains(endpoint, ":8447/") {
		t.Fatal("engine still chose the occupied fixed port")
	}
	if got := callEndpointHook(t, endpoint, token, eng.demoTenant); got != "allow" {
		t.Fatalf("hook with old port occupied: %s", got)
	}
}

func TestSessionHookLaunchRefusesBeforeListenerIsBound(t *testing.T) {
	minted := false
	credentials := &sessionHookCredentials{}
	provisioner := credentials.provisioner()
	provisioner.mintLaunch = func(context.Context, model.TenantID, sessions.LaunchIntent) (string, error) {
		minted = true
		return "unused", nil
	}
	if _, err := provisioner.provisionLaunch(context.Background(), model.TenantID(model.NewID()), sessions.LaunchIntent{}); err == nil || minted {
		t.Fatal("unbound listener minted an unusable session credential")
	}
}
