// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions"
)

// Session launch works both with the default budget gate (required by voice via
// liveingest) and with a dormant budget gate. The latter must not leave a typed
// nil interface that panics on launch. Real boot, API and a protocol stub for the CLI.
func TestDefaultProfileSessionLaunchStartsWithFinops(t *testing.T) {
	testProfileSessionLaunch(t, false)
}

func TestDormantProfileSessionLaunchStartsWithoutFinops(t *testing.T) {
	testProfileSessionLaunch(t, true)
}

func testProfileSessionLaunch(t *testing.T, dormant bool) {
	t.Helper()
	s := bootStubSessionEngine(t, dormant)
	if s.eng.moduleProfile.Active("finops") == dormant || s.eng.moduleProfile.Active("knowledge") {
		t.Fatalf("precondition: dormant=%v active=%v", dormant, s.eng.moduleProfile.ActiveNames())
	}
	ws := s.do("POST", "/v1/m/sessions/workspaces", map[string]any{"root_path": s.folder, "name": "folder"}, http.StatusCreated)
	profile := s.do("POST", "/v1/m/sessions/provider-profiles", map[string]any{"driver": "claude", "config_home": s.home, "user_home": s.home, "auth_source": "provider_account_home", "display_name": "stub"}, http.StatusCreated)
	run := s.do("POST", "/v1/m/sessions/runs", map[string]any{"name": "dormant finops", "transport": "stream-json", "permission_mode": "default", "isolation": "native", "workspace_ref": ws["workspace_ref"], "provider_profile_ref": profile["profile_ref"]}, http.StatusCreated)
	if run["state"] != "running" {
		t.Fatalf("launch state = %v, want running", run["state"])
	}
	s.do("POST", "/v1/m/sessions/runs/"+run["run_ref"].(string)+"/stop", map[string]any{}, http.StatusOK)
}

// stubSessionEngine is a booted engine with a protocol stub as `claude`, its hook
// listener bound and an administrator signed in.
type stubSessionEngine struct {
	eng                  *engine
	do                   func(method, path string, body any, status int) map[string]any
	tenant, folder, home string
}

func bootStubSessionEngine(t *testing.T, dormant bool) stubSessionEngine {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	folder, home, dir := t.TempDir(), t.TempDir(), t.TempDir()
	for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION", envSessionTokenFile, envSessionRuntimeWIF} {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	agent := filepath.Join(folder, "agent.py")
	stub := "#!" + python + "\nimport json, sys\n" +
		"print(json.dumps({'type':'system','subtype':'init','session_id':'dormant-stub'}), flush=True)\n" +
		"for line in sys.stdin:\n" +
		"    print(json.dumps({'type':'result','subtype':'success','is_error':False,'result':'ok','session_id':'dormant-stub'}), flush=True)\n"
	if err := os.WriteFile(agent, []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envSessionClaudeBin, agent)

	// Liveingest requires voice, which requires finops. Keep those consumers off
	// to exercise the session launch with a dormant budget gate.
	if dormant {
		selected := slices.DeleteFunc(standardModuleSelection(), func(name string) bool { return name == "liveingest" })
		if err := saveNodeModuleSelection(dir, selected, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	eng, err := boot(t.Context(), bootConfig{DataDir: dir, Engine: "sqlite", Version: version, Logger: discardLogger(), ApplyModuleProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
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
	do := func(method, path string, body any, status int) map[string]any {
		t.Helper()
		code, result, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, admin, tenant, body)
		if code != status {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, code, status, raw)
		}
		return result
	}
	setup, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	setupResult := do("POST", "/v1/setup", map[string]any{"token": setup, "email": "dormant@olivares.ai", "password": "fixture-password-2026!"}, http.StatusCreated)
	login := do("POST", "/v1/auth/login", map[string]any{"email": "dormant@olivares.ai", "password": "fixture-password-2026!"}, http.StatusOK)
	admin, _ = login["token"].(string)
	org, _ := setupResult["organization"].(map[string]any)
	tenant, _ = org["tenant_id"].(string)
	return stubSessionEngine{eng: eng, do: do, tenant: tenant, folder: folder, home: home}
}

// Each place boot hands a possibly dormant module to an interface gives an
// untyped nil when the module does not run, never a typed nil that a caller would
// treat as present.
func TestDormantModulesLeaveTheirInterfacesNil(t *testing.T) {
	if fin := budgetGateOf(nil); fin != nil {
		t.Errorf("budget gate of a dormant finops = %#v, want nil", fin)
	}
	if cp := contextPolicyOf(nil); cp != nil {
		t.Errorf("context policy of a dormant knowledge = %#v, want nil", cp)
	}
	if rec := sessionRecorderOf(nil); rec != nil {
		t.Errorf("route recorder of a dormant recording = %#v, want nil", rec)
	}
	// The eventing work sink is a struct, not a nil module: with eventing dormant it
	// accepts and drops work events instead of calling eventing.
	var sink sessions.WorkEventSink = workEventSink{eventing: nil, notRunning: true}
	if err := sink.IngestDurable(context.Background(), sessions.WorkEventEnvelope{}); err != nil {
		t.Errorf("work sink with eventing dormant = %v, want nil", err)
	}
	// The posture line names a control whose module does not run as off.
	if got := gatePosture(false, availabilityFailOpen); got != "off (module not enabled)" {
		t.Errorf("posture of an absent gate = %q", got)
	}
	if got := gatePosture(true, availabilityFailOpen); got != availabilityFailOpen.String() {
		t.Errorf("posture of a wired gate = %q", got)
	}
	// The job constructors receive the running view: a dormant module schedules nothing.
	if newEventingPump(func(string) string { return "" }, nil, nil, discardLogger()) != nil ||
		newOrchCadencePump(func(string) string { return "" }, nil, nil, discardLogger()) != nil ||
		newOrchWorkflowPump(func(string) string { return "" }, nil, nil, discardLogger()) != nil ||
		newRetentionSweepLoop(func(string) string { return "" }, nil, nil, discardLogger()) != nil ||
		newNotifyPump(func(string) string { return "" }, nil, nil, discardLogger()) != nil ||
		newLedgerForwardPump(func(string) string { return "" }, nil, nil, discardLogger()) != nil ||
		newAdmissionReconciler(nil, nil, discardLogger()) != nil {
		t.Error("a job constructor scheduled work for a dormant module")
	}
}
