// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build e2e && !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

// The provider is a local Messages API protocol fixture. There is no provider
// subscription, API request or real provider credential in this proof. The actual
// vendor binary must read our generated --settings file and honor the verdict.
func TestSessionHookPEPRealClaudeJourney(t *testing.T) {
	olivaresBinary := os.Getenv("OLIVARES_E2E_BINARY")
	if olivaresBinary == "" {
		t.Skip("set OLIVARES_E2E_BINARY to the built product binary")
	}
	claudeBinary, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("Claude Code binary unavailable")
	}
	for _, scenario := range []string{"allow", "deny", "kill"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			p, err := h.authr.Authenticate(context.Background(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, p, tenant, "real-claude-"+scenario)
			token, err := c.mintForPrincipal(p, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			dec := newClaudeHookDecider(&hookpep.Decider{DefaultPolicy: &hookpep.PolicyDoc{Default: "allow"}, Authr: c, Eval: h.set.gov.Evaluator(), Authz: harnessAuthz(h), Scoped: h.set.gov.ScopedGrants(), Store: h.st, Stops: h.set.gov, StopDeny: newStopDenyRecorder(h.st, discardLog()).record, Clock: time.Now, Log: discardLog()})
			pep := httptest.NewServer(claude.NewHookPEP(dec, discardHookAuditor{}, time.Now))
			defer pep.Close()
			if scenario == "deny" {
				code, raw := h.req("POST", "/v1/m/governance/policies", h.adminToken, h.tenantA, map[string]any{"name": "deny-command", "kind": "abac", "enabled": true, "spec": map[string]any{"rules": []any{map[string]any{"deny": true, "permission": "claude.tool.use:use"}}}})
				if code != 201 {
					t.Fatalf("policy: %d %s", code, raw)
				}
			}
			if scenario == "kill" {
				code, raw := h.req("POST", "/v1/m/governance/killswitch", h.adminToken, h.tenantA, map[string]any{"scope_kind": "estate", "reason": "session hook journey"})
				if code != 201 && code != 200 {
					t.Fatalf("kill switch: %d %s", code, raw)
				}
			}
			data, home, folder := t.TempDir(), t.TempDir(), t.TempDir()
			var redirected atomic.Int32
			fakePEP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				redirected.Add(1)
				_, _ = io.WriteString(w, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`)
			}))
			defer fakePEP.Close()
			profile := filepath.Join(home, ".claude")
			if err := os.MkdirAll(profile, 0700); err != nil {
				t.Fatal(err)
			}
			// The agent can modify account settings. An env override there must
			// not redirect the hook protected by the launch settings file.
			accountSettings, _ := json.Marshal(map[string]any{"env": map[string]string{"OLIVARES_HOOK_PEP_URL": fakePEP.URL}})
			if err := os.WriteFile(filepath.Join(profile, "settings.json"), accountSettings, 0600); err != nil {
				t.Fatal(err)
			}
			spec := sessions.LaunchSpec{Args: []string{"--print", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--permission-mode", "bypassPermissions", "--tools", "Bash", "--max-turns", "3", "run the requested tool"}, Env: []sessions.EnvVar{{Name: "OLIVARES_HOOK_PEP_URL", Value: pep.URL}, {Name: "OLIVARES_HOOK_PEP_TOKEN", Value: token}, {Name: "OLIVARES_HOOK_PEP_TENANT", Value: tenant.String()}}}
			if err := sessions.ConfigureClaudeHookPEP(&spec, data, intent.RunRef, olivaresBinary); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(folder, "executed")
			provider := newSessionHookMessagesFixture(t, "printf hook-proof > "+marker)
			defer provider.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, claudeBinary, spec.Args...)
			cmd.Dir = folder
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"), "ANTHROPIC_API_KEY=local-fixture-only", "ANTHROPIC_BASE_URL=" + provider.URL, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "CLAUDE_CODE_SKIP_TERMS_ACCEPTANCE=1", "DISABLE_AUTOUPDATER=1"}
			for _, v := range spec.Env {
				cmd.Env = append(cmd.Env, v.Name+"="+v.Value)
			}
			output, err := runSessionHookProofCLI(t, cmd)
			if err != nil {
				t.Fatalf("Claude: %v %s", err, output)
			}
			if redirected.Load() != 0 {
				t.Fatal("writable account settings redirected the protected hook")
			}
			_, markerErr := os.Stat(marker)
			if (scenario == "allow") != (markerErr == nil) {
				t.Fatalf("tool executed=%v, scenario=%s", markerErr == nil, scenario)
			}
			events := canonicalLedgerEventsFrom(t, h.st, tenant, 0)
			pre, post := false, false
			for _, ev := range events {
				if !strings.HasPrefix(ev.event.Action, "hook.tool.") {
					continue
				}
				if ev.meta["session_ref"] != intent.ClaimSID || ev.meta["run_ref"] != intent.RunRef {
					t.Fatalf("wrong audit run: %v", ev.meta["session_ref"])
				}
				if ev.meta["event"] == "PreToolUse" {
					pre = true
					want := "deny"
					if scenario == "allow" {
						want = "allow"
					}
					if ev.meta["decision"] != want {
						t.Fatalf("pre decision: %v", ev.meta["decision"])
					}
				}
				if ev.meta["event"] == "PostToolUse" {
					post = true
				}
			}
			if !pre || (scenario == "allow" && !post) {
				t.Fatalf("hook wiring pre=%v post=%v; Claude=%s", pre, post, output)
			}
			t.Logf("real Claude: scenario=%s tool executed=%v PreToolUse audited=%v PostToolUse audited=%v", scenario, markerErr == nil, pre, post)
		})
	}
}

func newSessionHookMessagesFixture(t *testing.T, command string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	issued := false
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if strings.HasSuffix(r.URL.Path, "/count_tokens") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"input_tokens":10}`)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Model    string
			Stream   bool
			Tools    []struct{ Name string }
			Messages []struct{ Content json.RawMessage }
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body); err != nil {
			http.Error(w, "fixture request", 400)
			return
		}
		hasBash := false
		for _, tool := range body.Tools {
			if tool.Name == "Bash" {
				hasBash = true
			}
		}
		mu.Lock()
		useTool := hasBash && !issued
		if useTool {
			issued = true
		}
		mu.Unlock()
		block := map[string]any{"type": "text", "text": "done"}
		stop := "end_turn"
		if useTool {
			block = map[string]any{"type": "tool_use", "id": "toolu_pep_fixture", "name": "Bash", "input": map[string]any{"command": command, "description": "Write a proof marker in the session folder"}}
			stop = "tool_use"
		}
		message := map[string]any{"id": "msg_pep_fixture", "type": "message", "role": "assistant", "model": body.Model, "content": []any{block}, "stop_reason": stop, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5}}
		if !body.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(message)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		event := func(name string, value any) {
			raw, _ := json.Marshal(value)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, raw)
		}
		message["content"] = []any{}
		message["stop_reason"] = nil
		event("message_start", map[string]any{"type": "message_start", "message": message})
		start := map[string]any{"type": "text", "text": ""}
		delta := map[string]any{"type": "text_delta", "text": "done"}
		if useTool {
			start = map[string]any{"type": "tool_use", "id": "toolu_pep_fixture", "name": "Bash", "input": map[string]any{}}
			raw, _ := json.Marshal(block["input"])
			delta = map[string]any{"type": "input_json_delta", "partial_json": string(raw)}
		}
		event("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": start})
		event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": delta})
		event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		event("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 5}})
		event("message_stop", map[string]any{"type": "message_stop"})
		if flush, ok := w.(http.Flusher); ok {
			flush.Flush()
		}
	}))
}

// Keep a separate protocol fixture for a CLI that consumes Claude's settings but
// needs no model. The binary hook bridge itself must send real HTTP requests.
func TestSessionHookPEPStubCLIJourney(t *testing.T) {
	bin := os.Getenv("OLIVARES_E2E_BINARY")
	if bin == "" {
		t.Skip("set OLIVARES_E2E_BINARY")
	}
	for _, scenario := range []string{"allow", "deny", "kill"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			p, err := h.authr.Authenticate(context.Background(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, p, tenant, "stub-cli-"+scenario)
			token, err := c.mintForPrincipal(p, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			dec := newClaudeHookDecider(&hookpep.Decider{DefaultPolicy: &hookpep.PolicyDoc{Default: "allow"}, Authr: c, Eval: h.set.gov.Evaluator(), Authz: harnessAuthz(h), Scoped: h.set.gov.ScopedGrants(), Store: h.st, Stops: h.set.gov, StopDeny: newStopDenyRecorder(h.st, discardLog()).record, Clock: time.Now, Log: discardLog()})
			server := httptest.NewServer(claude.NewHookPEP(dec, discardHookAuditor{}, time.Now))
			defer server.Close()
			if scenario == "deny" {
				code, raw := h.req("POST", "/v1/m/governance/policies", h.adminToken, h.tenantA, map[string]any{"name": "deny-stub-write", "kind": "abac", "enabled": true, "spec": map[string]any{"rules": []any{map[string]any{"deny": true, "permission": "claude.tool.use:write"}}}})
				if code != 201 {
					t.Fatalf("policy: %d %s", code, raw)
				}
			}
			if scenario == "kill" {
				code, raw := h.req("POST", "/v1/m/governance/killswitch", h.adminToken, h.tenantA, map[string]any{"scope_kind": "estate", "reason": "stub CLI proof"})
				if code != 201 {
					t.Fatalf("stop: %d %s", code, raw)
				}
			}
			spec := sessions.LaunchSpec{Env: []sessions.EnvVar{{Name: "OLIVARES_HOOK_PEP_URL", Value: server.URL}, {Name: "OLIVARES_HOOK_PEP_TOKEN", Value: token}}}
			folder := t.TempDir()
			if err := sessions.ConfigureClaudeHookPEP(&spec, folder, intent.RunRef, bin); err != nil {
				t.Fatal(err)
			}
			// This stub agent CLI consumes the actual generated settings, invokes
			// the binary bridge, and performs its tool only after explicit allow.
			stub := filepath.Join(folder, "stub-agent.py")
			if err := os.WriteFile(stub, []byte(`import json, os, pathlib, subprocess, sys
settings=json.loads(pathlib.Path(sys.argv[1]).read_text())
command=settings["hooks"]["PreToolUse"][0]["hooks"][0]["command"]
payload={"hook_event_name":"PreToolUse","session_id":"untrusted-vendor-id","tool_name":"Write","tool_use_id":"stub-write","tool_input":{"file_path":sys.argv[2],"content":"proof"}}
reply=subprocess.run(command,shell=True,input=json.dumps(payload),text=True,capture_output=True,check=True)
verdict=json.loads(reply.stdout)["hookSpecificOutput"]["permissionDecision"]
if verdict == "allow": pathlib.Path(sys.argv[2]).write_text("executed")
print(json.dumps({"decision":verdict,"executed":pathlib.Path(sys.argv[2]).exists()}))
`), 0600); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(folder, "executed")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			settingsIndex := slices.Index(spec.Args, "--settings")
			if settingsIndex < 0 || settingsIndex+1 >= len(spec.Args) {
				t.Fatal("fixture launch has no --settings value")
			}
			cmd := exec.CommandContext(ctx, "python3", stub, spec.Args[settingsIndex+1], marker)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
			for _, v := range spec.Env {
				cmd.Env = append(cmd.Env, v.Name+"="+v.Value)
			}
			out, err := runSessionHookProofCLI(t, cmd)
			if err != nil {
				t.Fatalf("stub CLI: %v %s", err, out)
			}
			var result struct {
				Decision string
				Executed bool
			}
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatal(err)
			}
			want := "deny"
			if scenario == "allow" {
				want = "allow"
			}
			if result.Decision != want || result.Executed != (scenario == "allow") {
				t.Fatalf("stub CLI: %s", out)
			}
			found := false
			for _, ev := range canonicalLedgerEventsFrom(t, h.st, tenant, 0) {
				if ev.event.Action == "hook.tool."+want && ev.meta["session_ref"] == intent.ClaimSID && ev.meta["run_ref"] == intent.RunRef {
					found = true
				}
			}
			if !found {
				t.Fatal("stub CLI decision was not audited")
			}
			t.Logf("stub CLI: scenario=%s tool executed=%v decision audited=%v", scenario, result.Executed, found)
		})
	}
}

// Every proof child owns its process group, records its PID in an isolated
// temporary directory, and is killed/reaped on success, failure or cancellation.
func runSessionHookProofCLI(t *testing.T, cmd *exec.Cmd) ([]byte, error) {
	t.Helper()
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	dir := t.TempDir()
	path := filepath.Join(dir, fmt.Sprintf("pep-hook-proof-%d.pids", cmd.Process.Pid))
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d %d\n", cmd.Process.Pid, cmd.Process.Pid)), 0600); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, err
	}
	defer os.Remove(path)
	err := cmd.Wait()
	return output.Bytes(), err
}
