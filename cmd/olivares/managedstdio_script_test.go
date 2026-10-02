// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

func TestManagedStdioScriptOutsideFolder(t *testing.T) {
	if confine.Probe().Mode != confine.ModeLandlock {
		t.Skip("this regression requires Landlock")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	folder, install, protected := t.TempDir(), t.TempDir(), t.TempDir()
	neighbor, key := filepath.Join(install, "unlisted.txt"), filepath.Join(protected, "secret-store.key")
	for _, path := range []string{neighbor, key} {
		if err := os.WriteFile(path, []byte("must remain unreadable"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths, _ := json.Marshal([]string{neighbor, key})
	script := filepath.Join(install, "server.py")
	source := `import json, sys
def denied(path):
    try:
        with open(path) as f: f.read()
        return False
    except PermissionError:
        return True
for line in sys.stdin:
    req = json.loads(line)
    if 'id' not in req: continue
    result = {'protocolVersion':'2025-11-25', 'capabilities':{'tools':{}}} if req['method'] == 'initialize' else {'tools':[{'name':'outside_script', 'description':json.dumps([denied(p) for p in ` + string(paths) + `]), 'inputSchema':{'type':'object'}}]}
    print(json.dumps({'jsonrpc':'2.0', 'id':req['id'], 'result':result}), flush=True)
`
	if err := os.WriteFile(script, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &mcpManagement{eng: &engine{sessionsMod: sessions.New(sessions.WithConfinement([]string{protected}, false))}}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	check := func(toolsJSON string) {
		t.Helper()
		var denied []bool
		if json.Unmarshal([]byte(toolsJSON), &denied) != nil || len(denied) != 2 || !denied[0] || !denied[1] {
			t.Fatalf("script grant exposed an unlisted neighbor or engine key: %s", toolsJSON)
		}
	}
	// The administrator's Test path and the live session path must grant the
	// same script file, without granting its parent or the engine data directory.
	tools, err := m.probeLocalServer(ctx, model.TenantID(model.NewID().String()), auth.MCPGatewayServer{MCPGatewayServerInput: auth.MCPGatewayServerInput{Command: python, Args: []string{script}}})
	if err != nil || len(tools) != 1 {
		t.Fatalf("outside-folder script discovery: %v", err)
	}
	check(tools[0].Description)
	spec := sessions.LaunchSpec{Program: python, Args: []string{script}, Dir: folder, Isolation: sessions.IsolationNative, WaitDelay: time.Second}
	if err := m.confineLocalServer(&spec); err != nil {
		t.Fatal(err)
	}
	client, err := launchManagedStdio(ctx, sessions.NewProcRunner(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err = client.ListTools(ctx)
	if err != nil || len(tools) != 1 {
		t.Fatalf("outside-folder script in a session: %v", err)
	}
	check(tools[0].Description)
}

func TestManagedStdioParentOfEngineHomeIsNotGranted(t *testing.T) {
	if confine.Probe().Mode != confine.ModeLandlock {
		t.Skip("this regression requires Landlock")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	parent, folder, install := t.TempDir(), t.TempDir(), t.TempDir()
	home := filepath.Join(parent, "engine-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	// Use only a synthetic home and credential; never inspect a vendor account.
	t.Setenv("HOME", home)
	credential := filepath.Join(home, "private-fixture.txt")
	if err := os.WriteFile(credential, []byte("private home fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, _ := json.Marshal(credential)
	script := filepath.Join(install, "server.py")
	source := `import json, sys
try:
    with open(` + string(path) + `) as f: f.read()
    denied = False
except PermissionError:
    denied = True
for line in sys.stdin:
    req = json.loads(line)
    if 'id' not in req: continue
    result = {'protocolVersion':'2025-11-25', 'capabilities':{'tools':{}}} if req['method'] == 'initialize' else {'tools':[{'name':'home_boundary', 'description':str(denied), 'inputSchema':{'type':'object'}}]}
    print(json.dumps({'jsonrpc':'2.0', 'id':req['id'], 'result':result}), flush=True)
`
	if err := os.WriteFile(script, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &mcpManagement{eng: &engine{sessionsMod: sessions.New(sessions.WithConfinement([]string{t.TempDir()}, false))}}
	spec := sessions.LaunchSpec{Program: python, Args: []string{script, parent}, Dir: folder, Isolation: sessions.IsolationNative, WaitDelay: time.Second}
	if err := m.confineLocalServer(&spec); err != nil {
		t.Fatal(err)
	}
	if spec.Confinement == nil {
		t.Fatal("fixture did not enable process confinement")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	client, err := launchManagedStdio(ctx, sessions.NewProcRunner(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(ctx)
	if err != nil || len(tools) != 1 {
		t.Fatalf("server with parent-of-home argument did not start: %v", err)
	}
	if tools[0].Description != "True" {
		t.Fatal("registered directory argument exposed the engine user's home")
	}
	for _, grant := range spec.Confinement.ReadOnly {
		if grant == parent {
			t.Fatal("parent of the engine user's home was granted read-only")
		}
	}
}

func TestMCPManagementStdioFailureReasonPersistsAndWarns(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	const credential = "fixture-mcp-secret-do-not-publish"
	for _, tc := range []struct{ name, source, want, reason string }{
		{"stderr", "import os, sys\nprint('earlier startup line', file=sys.stderr)\nprint('permission denied reading /fixture/server.js: ' + os.environ['MCP_SECRET'], file=sys.stderr)\nsys.exit(7)\n", "permission denied reading /fixture/server.js", "process_exit"},
		{"silent_exit", "import sys\nsys.exit(7)\n", "exit status 7", "process_exit"},
		{"escaped_secret", "import os, sys\ns = os.environ['MCP_SECRET']\nprint('\"\\\\u0066' + s[1:] + '\"', file=sys.stderr)\nsys.exit(7)\n", "credential [redacted]", "process_exit"},
		{"long_stderr", "import sys\nprint('permission denied reading /fixture/server.js ' + 'diagnostic ' * 1000, file=sys.stderr)\nsys.exit(7)\n", "permission denied reading /fixture/server.js", "process_exit"},
		{"missing_command", "", "no such file or directory", "process_start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, f, actor := mcpManagementFixture(t)
			var logs bytes.Buffer
			m.eng.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
			m.eng.sessionsMod = sessions.New(sessions.WithConfinement([]string{t.TempDir()}, false))
			if _, err := m.secrets.Put(t.Context(), actor, f.tenant, "mcp/diagnostic", credential, "fixture"); err != nil {
				t.Fatal(err)
			}
			in := auth.MCPGatewayServerInput{Name: tc.name, Command: python, EnvSecretRefs: map[string]string{"MCP_SECRET": "store:mcp/diagnostic"}}
			if tc.source == "" {
				in.Command = filepath.Join(t.TempDir(), "missing-server")
			} else {
				script := filepath.Join(t.TempDir(), "failure.py")
				if err := os.WriteFile(script, []byte(tc.source), 0o600); err != nil {
					t.Fatal(err)
				}
				in.Args = []string{script}
			}
			out, err := m.PutServer(t.Context(), actor, f.tenant, 0, "", in)
			if err != nil {
				t.Fatal(err)
			}
			out, err = m.TestServer(t.Context(), actor, f.tenant, out.Version, out.Servers[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			persisted, err := m.Get(t.Context(), f.tenant)
			if err != nil {
				t.Fatal(err)
			}
			for _, snapshot := range []auth.MCPGatewaySnapshot{out, persisted} {
				raw, _ := json.Marshal(snapshot.Servers[0].Probe)
				var probe struct{ State, Reason, Detail string }
				if json.Unmarshal(raw, &probe) != nil || probe.State != "unreachable" || probe.Reason != tc.reason || !strings.Contains(probe.Detail, tc.want) {
					t.Fatalf("failed stdio test lost its cause: %s", raw)
				}
				if strings.Contains(string(raw), credential) || strings.ContainsAny(probe.Detail, "\n\r\x1b") || len(probe.Detail) > 512 {
					t.Fatal("diagnostic exposed a credential or an unbounded/control line")
				}
			}
			if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), tc.want) || strings.Contains(logs.String(), credential) {
				t.Fatal("engine WARN did not carry the safe failure cause")
			}
		})
	}
}

func TestManagedMCPRegisteredCodePathsKeepProtectedBoundaries(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	data, folder, account, install := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	tools := filepath.Join(data, "tools", "fixture")
	if err := os.MkdirAll(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(data, "secret-store.key")
	code := filepath.Join(tools, "server.py")
	for _, path := range []string{key, code} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(install, "key-link.py")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}
	m := &mcpManagement{eng: &engine{dataDir: data, sessionsMod: sessions.New(sessions.WithConfinement([]string{data}, false))}}
	for _, path := range []string{key, link} {
		spec := sessions.LaunchSpec{Program: python, Args: []string{path}, Dir: folder}
		if err := m.confineLocalServer(&spec); err == nil {
			t.Fatal("configured file argument opened a protected engine key")
		}
	}
	spec := sessions.LaunchSpec{Program: python, Args: []string{code}, Dir: folder, Confinement: &confine.Policy{ReadWrite: []string{folder, account}, ReadOnly: []string{key}}}
	if err := m.confineLocalServer(&spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.Confinement.ReadWrite) != 1 || spec.Confinement.ReadWrite[0] != folder {
		t.Fatal("stdio child inherited a writable account home")
	}
	found := false
	for _, grant := range spec.Confinement.ReadOnly {
		if grant == key {
			t.Fatal("stdio child inherited a readable engine credential")
		}
		found = found || grant == code
	}
	if !found {
		t.Fatal("registered code under data/tools was not readable")
	}
}
