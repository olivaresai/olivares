// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/catalog"
	"github.com/olivaresai/olivares/modules/sessions"
)

// `mcp add --tool` runs a catalog-declared tool from its verified install in the
// engine's tools root, pinned to its version and executable digest: the server
// tests green as installed, and a changed executable refuses to start.
func TestMCPAddToolPinsTheDeclaredInstall(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	if runtime.GOOS != "linux" {
		t.Skip("declared tools install Linux archives")
	}
	cli, call, dataDir := mcpRealHandler(t)
	executable := installDeclaredMCPFixture(t, python, filepath.Join(dataDir, "tools"))
	digest := func() string {
		raw, err := os.ReadFile(executable)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	want := digest()

	if out := cli("add", "fixture", "--tool", "fixture-mcp@1.0.0"); !strings.Contains(out, "fixture works:") || !strings.Contains(out, "\n  echo\n") {
		t.Fatalf("mcp add --tool did not test the installed server:\n%s", out)
	}
	snap := call("GET", "/v1/console/mcp-gateway", nil, http.StatusOK)
	row := snap["servers"].([]any)[0].(map[string]any)
	pin, _ := row["pin"].(map[string]any)
	if row["command"] != executable || row["transport"] != "stdio" || pin["program"] != "fixture-mcp" || pin["version"] != "1.0.0" || pin["sha256"] != want {
		t.Fatalf("pinned server = command %v transport %v pin %v; want %s with sha256 %s", row["command"], row["transport"], row["pin"], executable, want)
	}
	if out := cli("ls"); !strings.Contains(out, "fixture-mcp@1.0.0 (pinned)") {
		t.Fatalf("mcp ls does not show the pin:\n%s", out)
	}

	// A pin naming another digest or another command than the install is refused.
	id := row["id"].(string)
	for _, server := range []map[string]any{
		{"name": "bad-digest", "pin": map[string]any{"program": "fixture-mcp", "version": "1.0.0", "sha256": strings.Repeat("0", 64)}},
		{"name": "bad-command", "command": "/bin/sh", "pin": map[string]any{"program": "fixture-mcp", "version": "1.0.0"}},
	} {
		call("POST", "/v1/console/mcp-gateway/servers", map[string]any{"version": snap["version"], "server": server}, http.StatusBadRequest)
	}
	// The console form knows neither targets nor pins: its save keeps both.
	workspace := model.NewID().String()
	cli("assign", "fixture", "--workspace", workspace)
	snap = call("GET", "/v1/console/mcp-gateway", nil, http.StatusOK)
	form := map[string]any{"name": "fixture", "transport": "stdio", "url": "", "command": executable, "args": []string{}, "env": map[string]string{}, "env_secret_refs": map[string]string{}, "egress_cidrs": []string{}, "trust": map[string]any{}, "enabled": false}
	snap = call("PUT", "/v1/console/mcp-gateway/servers/"+id, map[string]any{"version": snap["version"], "server": form}, http.StatusOK)
	row = snap["servers"].([]any)[0].(map[string]any)
	targets, _ := row["targets"].([]any)
	if pin, _ := row["pin"].(map[string]any); pin["sha256"] != want || len(targets) != 1 || targets[0].(map[string]any)["target_id"] != workspace {
		t.Fatalf("a console save dropped the pin or the targets: pin %v targets %v", row["pin"], row["targets"])
	}

	// A version that was never installed here is refused and nothing is added.
	other := map[string]any{"name": "other", "pin": map[string]any{"program": "fixture-mcp", "version": "2.0.0"}}
	call("POST", "/v1/console/mcp-gateway/servers", map[string]any{"version": snap["version"], "server": other}, http.StatusBadRequest)
	if servers := call("GET", "/v1/console/mcp-gateway", nil, http.StatusOK)["servers"].([]any); len(servers) != 1 {
		t.Fatalf("an uninstalled version was added: %v", servers)
	}

	// A changed executable is not the pinned one: its next start is refused.
	if err := os.Chmod(executable, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(executable, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("# changed after install\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if digest() == want {
		t.Fatal("the fixture did not change the executable")
	}
	snap = call("GET", "/v1/console/mcp-gateway", nil, http.StatusOK)
	snap = call("POST", "/v1/console/mcp-gateway/servers/"+id+"/test", map[string]any{"version": snap["version"]}, http.StatusOK)
	probe := snap["servers"].([]any)[0].(map[string]any)["probe"].(map[string]any)
	if probe["state"] != "unreachable" || probe["reason"] != "process_start" || !strings.Contains(fmt.Sprint(probe["detail"]), "changed since it was pinned") {
		t.Fatalf("a changed pinned executable started: %v", probe)
	}
	// Its stored pin still lets the administrator reassign and turn it off.
	cli("unassign", "fixture", "--workspace", workspace)
	if out := cli("disable", "fixture"); out != "fixture is off.\n" {
		t.Fatalf("disable of a server whose install changed = %q", out)
	}
}

// installDeclaredMCPFixture installs a catalog-declared stdio MCP server through
// toolinstall's approved-plan flow into root and returns its executable.
func installDeclaredMCPFixture(t *testing.T, python, root string) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	program := "#!" + python + "\nimport sys,json\nif '--version' in sys.argv:\n print('fixture-mcp 1.0.0'); sys.exit(0)\nfor line in sys.stdin:\n r=json.loads(line); m=r.get('method')\n if 'id' not in r: continue\n result={'protocolVersion':'2025-11-25','capabilities':{'tools':{}},'serverInfo':{'name':'fixture-mcp','version':'1.0.0'}} if m=='initialize' else {'tools':[{'name':'echo','description':'Echo fixture','inputSchema':{'type':'object'}}]}\n print(json.dumps({'jsonrpc':'2.0','id':r['id'],'result':result}),flush=True)\n"
	if err := os.WriteFile(filepath.Join(repo, "bin", "fixture-mcp"), []byte(program), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "bin"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "MCP fixture"}, {"tag", "v1.0.0"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v %s", err, out)
		}
	}
	c := exec.Command("git", "archive", "--format=tar.gz", "v1.0.0")
	c.Dir = repo
	archive, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fixture.tar.gz" {
			t.Errorf("unexpected network request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	declaration := catalog.ToolDeclaration{SourceURL: server.URL + "/fixture.tar.gz", Version: "1.0.0", SHA256: fmt.Sprintf("%x", sha256.Sum256(archive)), Program: "fixture-mcp", ConfigHome: "fixture-mcp", Kind: "mcp"}
	engine := toolinstall.NewEngineWithCapabilities(nil, toolinstall.EngineOptions{HTTPClient: server.Client()})
	req := toolinstall.RequestV2{Driver: declaration.Driver(), Version: declaration.Version, Platform: toolinstall.PlatformV2{OS: "linux", Arch: runtime.GOARCH}, DestRoot: root, Declaration: &declaration}
	plan, err := engine.PlanV2(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := toolinstall.MarshalPlanV2(plan)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := toolinstall.ReadPlanV2(bytes.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	receipt, _, err := engine.InstallV2(context.Background(), req, approved, nil)
	if err != nil {
		t.Fatal(err)
	}
	return receipt.Destination.Executable
}

// A session starts a pinned server only when its resolved executable still has
// the pinned digest; a mismatch never reaches the runner.
func TestManagedSessionStartsOnlyThePinnedExecutable(t *testing.T) {
	m, f, p := mcpManagementFixture(t)
	raw, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	pinned := fmt.Sprintf("%x", sha256.Sum256(raw))
	for _, tc := range []struct {
		digest string
		starts bool
	}{{strings.Repeat("0", 64), false}, {pinned, true}} {
		runner := &recordingRunner{}
		companion := sessions.RuntimeCompanion{LaunchSpec: sessions.LaunchSpec{Dir: t.TempDir()}, Runner: runner, Context: t.Context(), LaunchID: model.NewID()}
		row := auth.MCPGatewayServer{ID: model.NewID().String(), MCPGatewayServerInput: auth.MCPGatewayServerInput{Name: "Pinned", Transport: "stdio", Command: os.Args[0],
			Pin: &auth.MCPGatewayPin{Program: "fixture-mcp", Version: "1.0.0", SHA256: tc.digest}}}
		_, err := m.cachedSessionServer(t.Context(), f.tenant, p, companion, 1, row)
		if err == nil {
			t.Fatal("the recording runner started a server")
		}
		if started := len(runner.specs) == 1; started != tc.starts {
			t.Fatalf("digest %s: reached the runner=%v, want %v (%v)", tc.digest[:8], started, tc.starts, err)
		}
		if !tc.starts && !strings.Contains(err.Error(), "changed since it was pinned") {
			t.Fatalf("a mismatched pin failed for another reason: %v", err)
		}
	}
}
