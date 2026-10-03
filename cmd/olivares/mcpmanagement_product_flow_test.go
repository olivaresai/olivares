// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// HU025: exercise the management HTTP API and a newly launched session, using
// the intended CLI enable payload and the console's explicit Allow policy.
// An explicit empty policy must remain a denial, including after Test/enable.
func TestManagedMCPProductAPIEnableAndConfigureReachNewSession(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	folder, home, install := t.TempDir(), t.TempDir(), t.TempDir()
	for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION", envSessionTokenFile, envSessionRuntimeWIF} {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	agent := filepath.Join(folder, "agent.py")
	if err := os.WriteFile(agent, []byte("#!"+python+"\n"+productMCPCatalogueAgent), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envSessionClaudeBin, agent)
	script := filepath.Join(install, "hu_echo_mcp.py")
	if err := os.WriteFile(script, []byte(productMCPEchoServer), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := boot(t.Context(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test", Logger: discardLogger()})
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
		code, result, _ := doDemoViewJSON(t, eng.api.Handler(), method, path, admin, tenant, body)
		if code != status {
			t.Fatalf("%s %s = %d, want %d", method, path, code, status)
		}
		return result
	}
	setup, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	setupResult := do("POST", "/v1/setup", map[string]any{"token": setup, "email": "mcp-product@olivares.ai", "password": "fixture-password-2026!", "organization": "MCP product test"}, http.StatusCreated)
	login := do("POST", "/v1/auth/login", map[string]any{"email": "mcp-product@olivares.ai", "password": "fixture-password-2026!"}, http.StatusOK)
	admin, _ = login["token"].(string)
	if admin == "" {
		t.Fatal("login returned no credential")
	}
	organization, _ := setupResult["organization"].(map[string]any)
	tenant, _ = organization["tenant_id"].(string)
	if tenant == "" {
		t.Fatal("setup did not create the organization")
	}
	registered := do("POST", "/v1/m/sessions/workspaces", map[string]any{"root_path": folder, "name": "HU folder"}, http.StatusCreated)
	profile := do("POST", "/v1/m/sessions/provider-profiles", map[string]any{"driver": "claude", "config_home": home, "user_home": home, "auth_source": "provider_account_home", "session_tools": []string{}, "session_permission_mode": "default", "display_name": "Faithful protocol stub"}, http.StatusCreated)
	snapshot := do("GET", "/v1/console/mcp-gateway", nil, http.StatusOK)
	// This explicit operator choice keeps the managed-only check meaningful when
	// the native-tools default becomes on; enabling a server is still sufficient.
	snapshot = do("PUT", "/v1/console/mcp-gateway/session-tools", map[string]any{"version": snapshot["version"], "enabled": false}, http.StatusOK)
	definition := map[string]any{"name": "hu-echo", "command": python, "args": []string{"-u", script}}
	snapshot = do("POST", "/v1/console/mcp-gateway/servers", map[string]any{"version": snapshot["version"], "server": definition}, http.StatusCreated)
	row := snapshot["servers"].([]any)[0].(map[string]any)
	id := row["id"].(string)
	serverID, _ := json.Marshal(id)
	if err := os.WriteFile(filepath.Join(folder, "server-id.json"), serverID, 0o600); err != nil {
		t.Fatal(err)
	}
	base := "/v1/console/mcp-gateway/servers/" + id
	testServer := func() {
		t.Helper()
		snapshot = do("POST", base+"/test", map[string]any{"version": snapshot["version"]}, http.StatusOK)
		row := snapshot["servers"].([]any)[0].(map[string]any)
		if row["probe"].(map[string]any)["state"] != "ok" {
			t.Fatal("the registered outside-folder script did not test successfully")
		}
	}
	put := func() {
		t.Helper()
		snapshot = do("PUT", base, map[string]any{"version": snapshot["version"], "server": definition}, http.StatusOK)
	}
	assertSavedPolicy := func(count int, enabled bool) {
		t.Helper()
		snapshot = do("GET", "/v1/console/mcp-gateway", nil, http.StatusOK)
		row := snapshot["servers"].([]any)[0].(map[string]any)
		policies := row["allowed_tools"].([]any)
		if len(policies) != count || row["enabled"] != enabled || row["probe"].(map[string]any)["state"] != "ok" {
			t.Fatalf("persisted policy has %d tools, enabled=%v; want %d/%v with a successful probe", len(policies), row["enabled"], count, enabled)
		}
		if count == 1 && policies[0].(map[string]any)["required_scope"] != "tools:call" {
			t.Fatal("Test/enable changed the configured Allow scope")
		}
	}
	newSession := func(visible, callTool bool) {
		t.Helper()
		resultPath := filepath.Join(folder, "catalogue-result.json")
		_ = os.Remove(resultPath)
		run := do("POST", "/v1/m/sessions/runs", map[string]any{"name": "HU new session", "transport": "stream-json", "permission_mode": "default", "isolation": "native", "workspace_ref": registered["workspace_ref"], "provider_profile_ref": profile["profile_ref"]}, http.StatusCreated)
		ref := run["run_ref"].(string)
		defer do("POST", "/v1/m/sessions/runs/"+ref+"/stop", map[string]any{}, http.StatusOK)
		message := "List the catalogue."
		if callTool {
			message = "List tools and call hu_echo if allowed."
		} else if !visible {
			message = "Probe explicit denial."
		}
		do("POST", "/v1/m/sessions/runs/"+ref+"/input", map[string]any{"message": map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": message}}}, http.StatusAccepted)
		deadline := time.Now().Add(15 * time.Second)
		var raw []byte
		for time.Now().Before(deadline) {
			raw, err = os.ReadFile(resultPath)
			if err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		var result struct {
			Tools          []string `json:"tools"`
			Echo           string   `json:"echo"`
			Error          string   `json:"error"`
			ManagedHealthy bool     `json:"managed_healthy"`
			DenialCode     int      `json:"denial_code"`
		}
		if err != nil || json.Unmarshal(raw, &result) != nil || result.Error != "" {
			t.Fatalf("new session did not return its catalogue: %v (%s)", err, result.Error)
		}
		seen, native := false, false
		for _, name := range result.Tools {
			if strings.HasPrefix(name, "olivares_session_") {
				t.Fatal("legacy communication tool entered the session catalogue")
			}
			if strings.HasSuffix(name, "_hu_echo") {
				seen = true
			}
			native = native || name == "olivares_work_get"
		}
		if !native || seen != visible || (callTool && result.Echo != "hu-echo says: new-session") {
			t.Fatalf("new-session hu_echo visibility=%v want=%v, echo=%q, tools=%v", seen, visible, result.Echo, result.Tools)
		}
		if !visible && (!result.ManagedHealthy || result.DenialCode != -31001) {
			t.Fatal("tool absence did not prove a healthy managed server and a policy denial")
		}
	}
	testServer()
	definition["enabled"] = true // CLI's intended enable accepts the tested catalogue.
	put()
	newSession(true, false) // Managed servers need no separate session-tools enable.
	snapshot = do("PUT", "/v1/console/mcp-gateway/session-tools", map[string]any{"version": snapshot["version"], "enabled": true}, http.StatusOK)
	newSession(true, false) // An unannotated tool retains conservative approval policy.
	definition["enabled"] = false
	definition["allowed_tools"] = []any{} // Explicit denial must never widen.
	put()
	assertSavedPolicy(0, false)
	testServer()
	assertSavedPolicy(0, false)
	definition["enabled"] = true
	put()
	assertSavedPolicy(0, true)
	newSession(false, false)
	// Console Configure -> Allow hu_echo -> Save -> Test -> enable -> NEW session.
	definition["enabled"] = false
	definition["allowed_tools"] = []any{map[string]any{"name": "hu_echo", "required_scope": "tools:call", "destructive": false}}
	put()
	assertSavedPolicy(1, false)
	testServer()
	assertSavedPolicy(1, false)
	definition["enabled"] = true
	put()
	assertSavedPolicy(1, true)
	newSession(true, true)
	// Configure -> unchanged Save on the tested, enabled server. Console empty
	// containers mean the same options as omitted ones; no OAuth trust is added.
	definition["env"] = map[string]string{}
	definition["env_secret_refs"] = map[string]string{}
	definition["egress_cidrs"] = []string{}
	definition["trust"] = map[string]any{}
	put()
	assertSavedPolicy(1, true)
	newSession(true, true)
}

const productMCPEchoServer = `import json, sys
for line in sys.stdin:
    req = json.loads(line)
    if 'id' not in req: continue
    method = req['method']
    if method == 'initialize':
        result = {'protocolVersion':'2025-11-25','capabilities':{'tools':{}},'serverInfo':{'name':'hu-echo','version':'1'}}
    elif method == 'tools/list':
        result = {'tools':[{'name':'hu_echo','description':'HU echo','inputSchema':{'type':'object','properties':{'text':{'type':'string'}},'required':['text']}}]}
    else:
        result = {'content':[{'type':'text','text':'hu-echo says: '+req['params']['arguments']['text']}],'isError':False}
    print(json.dumps({'jsonrpc':'2.0','id':req['id'],'result':result}),flush=True)
`

const productMCPCatalogueAgent = `import json, os, sys, urllib.request, urllib.error
print(json.dumps({'type':'system','subtype':'init','session_id':'product-stub'}),flush=True)
for line in sys.stdin:
    try:
        config = sys.argv[sys.argv.index('--mcp-config')+1]
        with open(config) as f: server = json.load(f)['mcpServers']['olivares']
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        headers = {k:os.path.expandvars(v) for k,v in server['headers'].items()}
        headers['Content-Type'] = 'application/json'
        def rpc(method,params):
            body = json.dumps({'jsonrpc':'2.0','id':1,'method':method,'params':params}).encode()
            try:
                with opener.open(urllib.request.Request(server['url'],data=body,headers=headers),timeout=10) as response: return json.load(response)
            except urllib.error.HTTPError as response:
                return json.load(response)
        rpc('initialize',{'protocolVersion':'2025-11-25','capabilities':{},'clientInfo':{'name':'product-test','version':'1'}})
        headers['MCP-Protocol-Version'] = '2025-11-25'
        notification = json.dumps({'jsonrpc':'2.0','method':'notifications/initialized'}).encode()
        with opener.open(urllib.request.Request(server['url'],data=notification,headers=headers),timeout=10) as response: response.read()
        catalogue = rpc('tools/list',{})
        for tool in catalogue['result']['tools']:
            for field in ('annotations','outputSchema','_meta'):
                if field in tool and not isinstance(tool[field],dict): raise ValueError('invalid optional tool object')
            if 'icons' in tool and not isinstance(tool['icons'],list): raise ValueError('invalid optional tool icons')
            for field in ('readOnlyHint','destructiveHint','idempotentHint','openWorldHint'):
                if field in tool.get('annotations',{}) and not isinstance(tool['annotations'][field],bool): raise ValueError('invalid optional tool hint')
        names = [tool['name'] for tool in catalogue['result']['tools']]
        out = {'tools':names,'echo':''}
        alias = next((name for name in names if name.endswith('_hu_echo')),None)
        if alias and 'call hu_echo' in line:
            result = rpc('tools/call',{'name':alias,'arguments':{'text':'new-session'}})
            out['echo'] = result['result']['content'][0]['text']
        if 'Probe explicit denial' in line:
            with open('server-id.json') as f: server_id = json.load(f)
            server['url'] += '?server='+server_id
            healthy = rpc('initialize',{})
            listing = rpc('tools/list',{})
            out['managed_healthy'] = 'result' in healthy and listing.get('result',{}).get('tools') == []
            denied = rpc('tools/call',{'name':'hu_echo','arguments':{'text':'must not execute'}})
            out['denial_code'] = denied.get('error',{}).get('code')
    except Exception as e:
        out = {'error':type(e).__name__}
    with open('catalogue-result.tmp','w') as f: json.dump(out,f)
    os.replace('catalogue-result.tmp','catalogue-result.json')
    print(json.dumps({'type':'result','subtype':'success','is_error':False,'result':'catalogue checked','session_id':'product-stub'}),flush=True)
`
