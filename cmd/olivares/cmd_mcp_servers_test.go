// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/auth"
)

// fakeMCPRoster is the /v1/console/mcp-gateway roster: versioned writes, servers
// added off, a test that fills the probe, an explicit enable.
type fakeMCPRoster struct {
	*httptest.Server
	mu      sync.Mutex
	version int64
	session bool
	servers []map[string]any
	bodies  []map[string]any
	// keepEmpty makes the fake keep an omitted allowed_tools empty on enable, like an
	// engine that does not accept the tested catalogue.
	keepEmpty bool
	// testProbe, when set, is what a test records instead of a passing probe.
	testProbe map[string]any
}

func newFakeMCPRoster(t *testing.T) *fakeMCPRoster {
	t.Helper()
	f := &fakeMCPRoster{version: 1}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeMCPRoster) snapshot() map[string]any {
	return map[string]any{"version": f.version, "session_tools": f.session, "servers": f.servers}
}

func (f *fakeMCPRoster) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body != nil {
		f.bodies = append(f.bodies, body)
		if v, _ := body["version"].(float64); int64(v) != f.version {
			w.WriteHeader(http.StatusConflict)
			return
		}
	}
	p := strings.TrimPrefix(r.URL.Path, mcpGatewayPath)
	switch {
	case r.Method == http.MethodGet && p == "":
	case r.Method == http.MethodPost && p == "/servers":
		// The engine answers an add with 201 and a never-tested probe.
		s, _ := body["server"].(map[string]any)
		s["id"] = "srv-" + s["name"].(string)
		s["probe"] = map[string]any{"state": "never_tested", "tools": []any{}}
		f.servers = append(f.servers, s)
		f.version++
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/test"):
		for _, s := range f.servers {
			if "/servers/"+s["id"].(string)+"/test" == p {
				s["probe"] = map[string]any{"state": "ok", "tools": []any{
					map[string]any{"name": "read_file"}, map[string]any{"name": "list_directory"}}}
				if f.testProbe != nil {
					s["probe"] = f.testProbe
				}
			}
		}
		f.version++
	case (r.Method == http.MethodPut && strings.HasPrefix(p, "/servers/") || r.Method == http.MethodPost && p == "/servers") &&
		!mcpEngineWritable(body["server"]):
		// The engine decodes a server write strictly (core/api/handlers_mcp_gateway.go).
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "bad_request",
			"message": "provide version and a reference-only server configuration"}})
		return
	case r.Method == http.MethodPut && strings.HasPrefix(p, "/servers/"):
		// The engine's rule (Root 2026-10-02T01:42Z, MC): enabling with allowed_tools
		// omitted lists every tested tool as Ask (destructive), and the server carries
		// proposed_allow, the tools whose hints say read-only, as a proposal only. An
		// explicit list, even empty, is kept.
		in, _ := body["server"].(map[string]any)
		for i, s := range f.servers {
			if "/servers/"+s["id"].(string) == p {
				stored := map[string]any{"id": s["id"], "probe": s["probe"], "proposed_allow": s["proposed_allow"]}
				for k, v := range in {
					stored[k] = v
				}
				if _, set := in["allowed_tools"]; !set && in["enabled"] == true && !f.keepEmpty {
					allowed := []any{}
					probe, _ := s["probe"].(map[string]any)
					tools, _ := probe["tools"].([]any)
					for _, t := range tools {
						tool, _ := t.(map[string]any)
						allowed = append(allowed, map[string]any{"name": tool["name"], "required_scope": "tools:call", "destructive": true})
					}
					stored["allowed_tools"] = allowed
				}
				if stored["allowed_tools"] == nil {
					stored["allowed_tools"] = []any{}
				}
				f.servers[i] = stored
			}
		}
		f.version++
	case r.Method == http.MethodPut && p == "/session-tools":
		f.session, _ = body["enabled"].(bool)
		f.version++
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(f.snapshot())
}

func TestMCPAddTestsTheServerAndLeavesItOff(t *testing.T) {
	f := newFakeMCPRoster(t)
	out, errb, err := execSessionCLI(t, nil, mcpArgs(f.URL, "add", "files",
		"--secret-env", "API_KEY=store:mcp/files", "--", "npx", "-y", "@modelcontextprotocol/server-filesystem", ".")...)
	if err != nil {
		t.Fatalf("mcp add: %v\n%s", err, errb)
	}
	s := f.servers[0]
	if s["command"] != "npx" || strings.Join(toStrings(s["args"]), " ") != "-y @modelcontextprotocol/server-filesystem ." ||
		s["enabled"] != false || s["url"] != nil {
		t.Fatalf("registered server = %v", s)
	}
	if refs, _ := s["env_secret_refs"].(map[string]any); refs["API_KEY"] != "store:mcp/files" {
		t.Fatalf("secret env = %v", s["env_secret_refs"])
	}
	for _, want := range []string{"Added files (off). Testing it…", "files works: 2 tools.", "  read_file", "next: olivares mcp enable files"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
}

// TestMCPAddTakesTheEnginesCreatedAndListsItNotTested is J6 on refresh 02: the engine
// answers an add with 201, which `mcp add` refused as an error, and `mcp ls` called a
// never-tested server "test failed: never tested".
func TestMCPAddTakesTheEnginesCreatedAndListsItNotTested(t *testing.T) {
	f := newFakeMCPRoster(t)
	out, errb, err := execSessionCLI(t, nil, mcpArgs(f.URL, "add", "files", "--no-test", "--", "npx", "-y", "server-filesystem", ".")...)
	if err != nil || !strings.Contains(out, "Added files (off). Test it: olivares mcp test files") {
		t.Fatalf("mcp add: err=%v out=%q stderr=%q", err, out, errb)
	}
	out, _, err = execSessionCLI(t, nil, mcpArgs(f.URL, "ls")...)
	if err != nil {
		t.Fatalf("mcp ls: %v", err)
	}
	if !strings.Contains(out, "not tested") || strings.Contains(out, "test failed") {
		t.Fatalf("mcp ls =\n%s", out)
	}
}

// TestMCPTestAndListSayWhyALocalServerFailed is HU 019: a local server that could not
// start was shown as "unreachable" with no reason. The engine now records a reason and
// one redacted detail line; `mcp test` and `mcp ls` print it.
func TestMCPTestAndListSayWhyALocalServerFailed(t *testing.T) {
	f := newFakeMCPRoster(t)
	f.servers = []map[string]any{{"id": "srv-files", "name": "files", "command": "./serve.sh", "enabled": false,
		"probe": map[string]any{"state": "never_tested", "tools": []any{}}}}
	f.testProbe = map[string]any{"state": "unreachable", "reason": "process_start", "tools": []any{},
		"detail": "fork/exec ./serve.sh: permission denied"}
	_, _, err := execSessionCLI(t, nil, mcpArgs(f.URL, "test", "files")...)
	want := "The test of files failed: did not start: fork/exec ./serve.sh: permission denied. " +
		"Check its command or URL, then: olivares mcp test files"
	if exitcode.From(err) != exitcode.Err || err.Error() != want {
		t.Fatalf("mcp test: err = %v", err)
	}
	out, _, err := execSessionCLI(t, nil, mcpArgs(f.URL, "ls")...)
	if err != nil || !strings.Contains(out, "did not start: fork/exec ./serve.sh: permission denied") {
		t.Fatalf("mcp ls: err=%v\n%s", err, out)
	}

	for _, tc := range []struct {
		probe map[string]any
		want  string
	}{
		{map[string]any{"state": "unreachable", "reason": "process_exit", "detail": "Error: Cannot find module 'x'."},
			"exited: Error: Cannot find module 'x'"},
		{map[string]any{"state": "unreachable", "reason": "timeout"}, "unreachable: timed out"},
		{map[string]any{"state": "unreachable", "reason": "http_status", "http_status": float64(503)}, "unreachable: HTTP 503"},
		{map[string]any{"state": "unreachable"}, "unreachable (the engine gave no reason)"},
		{map[string]any{"state": "egress_denied"}, "blocked by the egress policy"},
		{map[string]any{"state": "unreachable", "reason": "process_start", "detail": "bad\x1b[2Jname"}, "did not start: badname"},
	} {
		if got := mcpProbeFailure(tc.probe); !strings.HasPrefix(got, tc.want) {
			t.Errorf("%v: %q, want %q", tc.probe, got, tc.want)
		}
	}
}

func TestMCPEnableKeepsTheServerAndSaysWhenSessionsDoNotUseIt(t *testing.T) {
	f := newFakeMCPRoster(t)
	f.servers = []map[string]any{{"id": "srv-1", "name": "docs", "url": "https://mcp.example.com/mcp", "enabled": false,
		"trust": map[string]any{}, "egress_cidrs": []any{}, "allowed_tools": []any{},
		"probe": map[string]any{"state": "ok", "tools": []any{map[string]any{"name": "search"}}}}}
	out, errb, err := execSessionCLI(t, nil, mcpArgs(f.URL, "enable", "docs")...)
	if err != nil {
		t.Fatalf("mcp enable: %v\n%s", err, errb)
	}
	put := f.bodies[len(f.bodies)-1]
	sent, _ := put["server"].(map[string]any)
	if sent["enabled"] != true || sent["url"] != "https://mcp.example.com/mcp" || sent["id"] != nil || sent["probe"] != nil {
		t.Fatalf("enable sent %v, want the same server with enabled=true and no id/probe", sent)
	}
	if !strings.Contains(out, "docs is on.") || !strings.Contains(out, "olivares mcp sessions on") {
		t.Fatalf("mcp enable =\n%s", out)
	}
}

// TestMCPEnableLetsSessionsCallTheTestedTools is HU 025 (MC): enable copied the saved
// row's allowed_tools [] (the engine stores [] for every new server) into the enable
// call, so no tool was allowed while the CLI said "on". Enable alone sends no list, so the
// engine applies its default to the tested tools; a list someone set stays as set; the CLI
// names each tool's policy.
func TestMCPEnableLetsSessionsCallTheTestedTools(t *testing.T) {
	f := newFakeMCPRoster(t)
	f.session = true
	tools := func(names ...string) []any {
		var out []any
		for _, n := range names {
			out = append(out, map[string]any{"name": n})
		}
		return out
	}
	f.servers = []map[string]any{
		{"id": "srv-files", "name": "files", "command": "npx", "enabled": false, "trust": map[string]any{},
			"egress_cidrs": []any{}, "allowed_tools": []any{},
			"probe": map[string]any{"state": "ok", "tools": tools("read_file", "list_directory")}},
		{"id": "srv-docs", "name": "docs", "url": "https://mcp.example.com/mcp", "enabled": false, "trust": map[string]any{},
			"egress_cidrs": []any{}, "allowed_tools": []any{map[string]any{"name": "search", "required_scope": "tools:call"}},
			"probe": map[string]any{"state": "ok", "tools": tools("search", "delete_page")}},
	}
	out, errb, err := execSessionCLI(t, nil, mcpArgs(f.URL, "enable", "files")...)
	if err != nil {
		t.Fatalf("enable files: %v\n%s", err, errb)
	}
	sent, _ := f.bodies[len(f.bodies)-1]["server"].(map[string]any)
	if _, set := sent["allowed_tools"]; set {
		t.Fatalf("enable sent allowed_tools %v; an empty saved list must be omitted so the tested tools are accepted", sent["allowed_tools"])
	}
	// Root 2026-10-02T01:42Z made every tool of an enable without a list Ask; this line said
	// "Sessions can call 2 of its tools" while the engine allowed them all.
	if !strings.Contains(out, "files is on.\n  Ask first: read_file, list_directory\n") {
		t.Fatalf("enable files =\n%s", out)
	}
	out, errb, err = execSessionCLI(t, nil, mcpArgs(f.URL, "enable", "docs")...)
	if err != nil {
		t.Fatalf("enable docs: %v\n%s", err, errb)
	}
	sent, _ = f.bodies[len(f.bodies)-1]["server"].(map[string]any)
	if list, _ := sent["allowed_tools"].([]any); len(list) != 1 {
		t.Fatalf("enable changed the list someone set: %v", sent["allowed_tools"])
	}
	if !strings.Contains(out, "docs is on.\n  Run without approval: search\n  Not allowed: delete_page\n") {
		t.Fatalf("enable docs =\n%s", out)
	}
}

// TestMCPEnableNeverSaysOnWhenNoToolIsAllowed: if the engine still allows no tool, the
// CLI does not say "on" or "sessions use"; it says no tool is allowed (exit 1).
func TestMCPEnableNeverSaysOnWhenNoToolIsAllowed(t *testing.T) {
	f := newFakeMCPRoster(t)
	f.session, f.keepEmpty = true, true
	f.servers = []map[string]any{{"id": "srv-files", "name": "files", "command": "npx", "enabled": false,
		"trust": map[string]any{}, "egress_cidrs": []any{}, "allowed_tools": []any{},
		"probe": map[string]any{"state": "ok", "tools": []any{map[string]any{"name": "read_file"}}}}}
	out, _, err := execSessionCLI(t, nil, mcpArgs(f.URL, "enable", "files")...)
	if exitcode.From(err) != exitcode.Err || err == nil ||
		!strings.Contains(err.Error(), "files is on, but no tool is allowed, so sessions cannot call any of its tools") {
		t.Fatalf("err = %v (exit %d)", err, exitcode.From(err))
	}
	if strings.Contains(out, "is on.") || strings.Contains(out, "Sessions can call") {
		t.Fatalf("the CLI claimed a usable server:\n%s", out)
	}
	out, _, err = execSessionCLI(t, nil, mcpArgs(f.URL, "ls")...)
	if err != nil || !strings.Contains(out, "on, no tool allowed") {
		t.Fatalf("mcp ls: err=%v\n%s", err, out)
	}
}

func TestMCPAddRefusesSomethingThatIsNeitherACommandNorAURL(t *testing.T) {
	f := newFakeMCPRoster(t)
	_, _, err := execSessionCLI(t, nil, mcpArgs(f.URL, "add", "x", "http://plain.example.com")...)
	if exitcode.From(err) != exitcode.Usage {
		t.Fatalf("err = %v, want usage", err)
	}
	if len(f.bodies) != 0 {
		t.Fatal("a refused add reached the engine")
	}
}

func TestMCPConcurrentEditIsAConflict(t *testing.T) {
	f := newFakeMCPRoster(t)
	f.servers = []map[string]any{{"id": "srv-1", "name": "docs", "enabled": true}}
	f.version = 5
	// The engine's version moves between our read and our write.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(f.snapshot())
			f.version++
			return
		}
		f.serve(w, r)
	}))
	defer srv.Close()
	_, _, err := execSessionCLI(t, nil, mcpArgs(srv.URL, "disable", "docs")...)
	if exitcode.From(err) != exitcode.Conflict || !strings.Contains(err.Error(), "Run the command again") {
		t.Fatalf("err = %v, want the conflict sentence", err)
	}
}

// mcpArgs puts the connection flags after `mcp`, where the group declares them.
// mcpEngineWritable is the engine's rule for a server write: only the fields of
// auth.MCPGatewayServerInput. A response-only field (id, probe, proposed_allow) is a 400.
func mcpEngineWritable(server any) bool {
	fields, ok := server.(map[string]any)
	if !ok {
		return false
	}
	writable := map[string]bool{}
	t := reflect.TypeOf(auth.MCPGatewayServerInput{})
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		writable[name] = true
	}
	for k := range fields {
		if !writable[k] {
			return false
		}
	}
	return true
}

func mcpArgs(server, verb string, rest ...string) []string {
	return append(append([]string{"mcp", verb}, sessionCreds(server)...), rest...)
}

func toStrings(v any) []string {
	var out []string
	if list, ok := v.([]any); ok {
		for _, x := range list {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
