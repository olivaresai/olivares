// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mcpTrustCatalogueServer is a local MCP server with one tool its own hints call
// read-only and one with no hint.
const mcpTrustCatalogueServer = `import json, sys
for line in sys.stdin:
    req = json.loads(line)
    if 'id' not in req: continue
    method = req['method']
    if method == 'initialize':
        result = {'protocolVersion':'2025-11-25','capabilities':{'tools':{}},'serverInfo':{'name':'notes','version':'1'}}
    elif method == 'tools/list':
        result = {'tools':[
            {'name':'read_note','description':'Read a note','inputSchema':{'type':'object'},'annotations':{'readOnlyHint':True}},
            {'name':'write_note','description':'Write a note','inputSchema':{'type':'object'}}]}
    else:
        result = {'content':[{'type':'text','text':'ok'}],'isError':False}
    print(json.dumps({'jsonrpc':'2.0','id':req['id'],'result':result}),flush=True)
`

// TestMCPEnableAndDisableRoundTripThroughTheRealHandler is MC's J6 on refresh 09: `mcp
// test` worked and `mcp enable` failed with HTTP 400 "provide version and a
// reference-only server configuration", because the CLI copied the server it read,
// response-only proposed_allow included, into its write. The CLI drives the engine's own
// handler here: add, enable (every tool asks; the proposal is shown), enable
// --allow-proposed, disable and enable again.
func TestMCPEnableAndDisableRoundTripThroughTheRealHandler(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION"} {
		t.Setenv(name, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	script := filepath.Join(t.TempDir(), "notes_mcp.py")
	if err := os.WriteFile(script, []byte(mcpTrustCatalogueServer), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test", Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	setup, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	handler := eng.api.Handler()
	call := func(method, path, token, tenant string, body any, want int) map[string]any {
		t.Helper()
		code, result, raw := doDemoViewJSON(t, handler, method, path, token, tenant, body)
		if code != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, code, want, raw)
		}
		return result
	}
	created := call("POST", "/v1/setup", "", "", map[string]any{"token": setup, "email": "mcp-cli@olivares.ai",
		"password": "fixture-password-2026!", "organization": "MCP CLI test"}, http.StatusCreated)
	login := call("POST", "/v1/auth/login", "", "", map[string]any{"email": "mcp-cli@olivares.ai", "password": "fixture-password-2026!"}, http.StatusOK)
	admin, _ := login["token"].(string)
	org, _ := created["organization"].(map[string]any)
	tenant, _ := org["tenant_id"].(string)
	if admin == "" || tenant == "" {
		t.Fatalf("setup/login gave token %q tenant %q", admin, tenant)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cli := func(args ...string) string {
		t.Helper()
		full := append([]string{"mcp", args[0], "--server", srv.URL, "--token", admin, "--tenant", tenant}, args[1:]...)
		out, errb, err := execSessionCLI(t, nil, full...)
		if err != nil {
			t.Fatalf("olivares mcp %s: %v\n%s%s", strings.Join(args, " "), err, out, errb)
		}
		return out
	}

	cli("add", "notes", "--", python, "-u", script)
	snap := call("GET", "/v1/console/mcp-gateway", admin, tenant, nil, http.StatusOK)
	servers, _ := snap["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("servers = %v", snap["servers"])
	}
	if proposal, _ := servers[0].(map[string]any)["proposed_allow"].([]any); len(proposal) != 1 || proposal[0] != "read_note" {
		t.Fatalf("the engine's read has proposed_allow %v, want [read_note]; this test needs the response-only field",
			servers[0].(map[string]any)["proposed_allow"])
	}

	out := cli("enable", "notes")
	for _, want := range []string{"notes is on.\n  Ask first: read_note, write_note\n",
		"The server says these tools only read: read_note. To let them run without approval: olivares mcp enable notes --allow-proposed\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("enable: missing %q in\n%s", want, out)
		}
	}
	if out = cli("enable", "notes", "--allow-proposed"); !strings.Contains(out, "  Run without approval: read_note\n  Ask first: write_note\n") {
		t.Fatalf("enable --allow-proposed =\n%s", out)
	}
	if out = cli("disable", "notes"); out != "notes is off.\n" {
		t.Fatalf("disable = %q", out)
	}
	// Enabled again, the server comes back as the administrator left it.
	if out = cli("enable", "notes"); !strings.Contains(out, "  Run without approval: read_note\n  Ask first: write_note\n") {
		t.Fatalf("enable after disable =\n%s", out)
	}
}
