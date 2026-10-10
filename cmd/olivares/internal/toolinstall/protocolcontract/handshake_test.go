//go:build contract && linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package protocolcontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

type factResult struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Observed any    `json:"observed,omitempty"`
}

type toolResult struct {
	Schema        string       `json:"schema"`
	Tool          string       `json:"tool"`
	Scope         string       `json:"scope"`
	Status        string       `json:"status"`
	GuardEligible bool         `json:"guard_eligible"`
	MeasuredAt    string       `json:"measured_at"`
	Binary        installed    `json:"binary"`
	Facts         []factResult `json:"facts"`
}

func (r *toolResult) check(t *testing.T, id string, ok bool, observed any) {
	t.Helper()
	status := "pass"
	if !ok {
		status = "fail"
		r.Status = "fail"
		t.Errorf("%s: %v", id, observed)
	}
	r.Facts = append(r.Facts, factResult{ID: id, Status: status, Observed: observed})
}

func newToolResult(t *testing.T, tool, scope, filename string) (*toolResult, string) {
	t.Helper()
	output := os.Getenv("OLIVARES_CONTRACT_RESULTS")
	if output == "" {
		output = t.TempDir()
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	r := &toolResult{Schema: "olivares.ai/tool-protocol-contract/v1", Tool: tool, Scope: scope, Status: "pass", MeasuredAt: time.Now().UTC().Format(time.RFC3339)}
	t.Cleanup(func() {
		if t.Failed() {
			r.Status = "fail"
		}
		b, err := json.MarshalIndent(r, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(output, filename), append(b, '\n'), 0600)
		}
		if err != nil {
			t.Error(err)
		}
	})
	return r, output
}

// These are executable protocol checks, not a model/vendor-account journey.
// Neither the contract tag alone nor the ordinary suite downloads a binary.
func TestRealToolHandshake(t *testing.T) {
	if os.Getenv("OLIVARES_CONTRACT") != "1" {
		t.Skip("set OLIVARES_CONTRACT=1 to download official binaries and check their handshakes")
	}
	selected := os.Getenv("OLIVARES_CONTRACT_TOOLS")
	if selected != "" {
		for _, tool := range strings.Split(selected, ",") {
			if tool != "claude" && tool != "codex" && tool != "grok" && tool != "opencode" {
				t.Fatalf("unknown contract tool %q", tool)
			}
		}
	}
	for _, tool := range []string{"claude", "codex", "grok", "opencode"} {
		if selected != "" && !strings.Contains(","+selected+",", ","+tool+",") {
			continue
		}
		t.Run(tool, func(t *testing.T) {
			r, output := newToolResult(t, tool, "credential-free-handshake", tool+".json")
			// Local resume shapes do not qualify recovery of model-turn history.
			for _, id := range []string{"turn-events", "approval-round-trip", "resume"} {
				r.Facts = append(r.Facts, factResult{ID: id, Status: "not_measured"})
			}
			bin, err := installTool(tool)
			if err != nil {
				r.check(t, "official-install", false, err.Error())
				t.FailNow()
			}
			r.Binary = bin
			r.check(t, "official-install", true, bin.Verification)
			switch tool {
			case "claude":
				checkClaude(t, r, output)
			case "codex":
				checkCodex(t, r, output)
			default:
				checkACP(t, r, output)
			}
		})
	}
}

func mustCall(t *testing.T, r *toolResult, p *child, id int, method string, params any, acp bool) map[string]json.RawMessage {
	t.Helper()
	frame, err := p.call(id, method, params, acp)
	if err != nil {
		r.check(t, method+"-response", false, err.Error())
		t.FailNow()
	}
	if acp {
		r.check(t, method+"-envelope", textField(frame, "jsonrpc") == "2.0", textField(frame, "jsonrpc"))
	}
	return frame
}

func checkCodex(t *testing.T, r *toolResult, output string) {
	p := startChild(t, r.Binary.Executable, cliruntime.CodexArgs(cliruntime.LaunchRequest{}), output, r.Tool)
	f := mustCall(t, r, p, 1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "olivares_contract", "version": "0.0.0"}, "capabilities": map[string]bool{"experimentalApi": false}}, false)
	init := objectField(f, "result")
	r.check(t, "initialize-result", init != nil && len(f["error"]) == 0, nil)
	ua := textField(init, "userAgent")
	r.check(t, "initialize-version", strings.Contains(ua, r.Binary.Version), ua)
	if err := p.send(map[string]any{"method": "initialized"}); err != nil {
		r.check(t, "initialized-notification", false, err.Error())
		return
	}
	f = mustCall(t, r, p, 2, "account/read", map[string]bool{"refreshToken": false}, false)
	account := objectField(f, "result")
	r.check(t, "account-read-empty-home", string(account["account"]) == "null" && string(account["requiresOpenaiAuth"]) == "true", account)
	f = mustCall(t, r, p, 3, "thread/start", map[string]any{"approvalPolicy": "untrusted", "sandbox": "read-only", "cwd": p.cmd.Dir}, false)
	thread := objectField(objectField(f, "result"), "thread")
	r.check(t, "thread-start-root", strings.TrimSpace(textField(thread, "id")) != "" && textField(thread, "parentThreadId") == "" && textField(thread, "agentRole") == "", map[string]string{"id": textField(thread, "id"), "parentThreadId": textField(thread, "parentThreadId"), "agentRole": textField(thread, "agentRole")})
	f = mustCall(t, r, p, 4, "thread/resume", map[string]any{"threadId": textField(thread, "id"), "approvalPolicy": "untrusted", "sandbox": "read-only", "cwd": p.cmd.Dir}, false)
	errObject := objectField(f, "error")
	r.check(t, "thread-resume-without-rollout-refusal", string(errObject["code"]) == "-32600" && strings.Contains(textField(errObject, "message"), "no rollout found") && strings.Contains(textField(errObject, "message"), textField(thread, "id")) && len(f["result"]) == 0, errObject)
	// No turn/start, input or model request follows thread/start.
}

func checkACP(t *testing.T, r *toolResult, output string) {
	args := []string{"acp", "--hostname", "127.0.0.1"}
	if r.Tool == "grok" {
		args = cliruntime.GrokArgs(cliruntime.LaunchRequest{})
	}
	p := startChild(t, r.Binary.Executable, args, output, r.Tool)
	f := mustCall(t, r, p, 1, "initialize", acpInitializeParams(), true)
	init := objectField(f, "result")
	r.check(t, "initialize-protocol-version", string(init["protocolVersion"]) == "1", init["protocolVersion"])
	r.check(t, "initialize-capabilities", objectField(init, "agentCapabilities") != nil, nil)
	version := textField(objectField(init, "agentInfo"), "version")
	field := "agentInfo.version"
	if r.Tool == "grok" {
		version = textField(objectField(init, "_meta"), "agentVersion")
		field = "_meta.agentVersion"
	}
	r.check(t, "initialize-version", version == r.Binary.Version, map[string]string{"field": field, "value": version})
	if r.Tool == "grok" {
		var methods []map[string]json.RawMessage
		err := json.Unmarshal(init["authMethods"], &methods)
		usable := false
		for _, m := range methods {
			if textField(m, "id") != "" {
				usable = true
			}
		}
		r.check(t, "initialize-auth-methods", err == nil && usable, methods)
	}
	f = mustCall(t, r, p, 2, "session/new", map[string]any{"cwd": p.cmd.Dir, "mcpServers": []any{}}, true)
	if errObject := objectField(f, "error"); errObject != nil {
		r.check(t, "session-new-auth-required", string(errObject["code"]) == "-32000", errObject)
	} else {
		r.check(t, "session-new-id", strings.TrimSpace(textField(objectField(f, "result"), "sessionId")) != "", textField(objectField(f, "result"), "sessionId"))
	}
	id := textField(objectField(f, "result"), "sessionId")
	method := acpResumeMethod(init)
	r.check(t, "initialize-resume-method", method != "", map[string]any{"method": method, "capabilities": init["agentCapabilities"]})
	if method == "" {
		return
	}
	if r.Tool == "grok" {
		// No session can be created here without authentication. Measure only
		// the refusal for an absent ID in this test's empty account home.
		f = mustCall(t, r, p, 3, method, map[string]any{"sessionId": "018fb245-9780-7c11-b862-13e5a9709fc2", "cwd": p.cmd.Dir, "mcpServers": []any{}}, true)
		errObject := objectField(f, "error")
		r.check(t, "session-resume-missing-id-refusal", string(errObject["code"]) == "-32603" && textField(objectField(errObject, "data"), "code") == "FS_NOT_FOUND" && len(f["result"]) == 0, errObject)
	} else {
		f = mustCall(t, r, p, 3, method, map[string]any{"sessionId": id, "cwd": p.cmd.Dir, "mcpServers": []any{}}, true)
		checkEmptySessionResume(t, r, "session-resume-empty-session", id, f)
		root := filepath.Dir(p.cmd.Dir)
		p.stop()
		p = startChildAt(t, r.Binary.Executable, args, output, r.Tool+"-restart", root)
		f = mustCall(t, r, p, 4, "initialize", acpInitializeParams(), true)
		init = objectField(f, "result")
		r.check(t, "restart-initialize-version", textField(objectField(init, "agentInfo"), "version") == r.Binary.Version && acpResumeMethod(init) == method, init)
		f = mustCall(t, r, p, 5, method, map[string]any{"sessionId": id, "cwd": p.cmd.Dir, "mcpServers": []any{}}, true)
		checkEmptySessionResume(t, r, "session-resume-after-restart", id, f)
	}
	// No authenticate, session/prompt or external MCP server is requested.
}

func acpInitializeParams() map[string]any {
	return map[string]any{"protocolVersion": 1, "clientInfo": map[string]string{"name": "olivares_contract", "version": "0.0.0"}, "clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false}}
}

func acpResumeMethod(init map[string]json.RawMessage) string {
	caps := objectField(init, "agentCapabilities")
	resume := objectField(caps, "sessionCapabilities")["resume"]
	if len(resume) > 0 && string(resume) != "null" {
		return "session/resume"
	}
	if string(caps["loadSession"]) == "true" {
		return "session/load"
	}
	return ""
}

func checkEmptySessionResume(t *testing.T, r *toolResult, fact, requestedID string, frame map[string]json.RawMessage) {
	t.Helper()
	result := objectField(frame, "result")
	echo := textField(result, "sessionId")
	r.check(t, fact, requestedID != "" && result != nil && len(frame["error"]) == 0 && (echo == "" || echo == requestedID), result)
}

func checkClaude(t *testing.T, r *toolResult, output string) {
	args := cliruntime.ClaudeArgs(cliruntime.LaunchRequest{PermissionMode: "dontAsk", ToolSurfaceDeclared: true})
	p := startChild(t, r.Binary.Executable, args, output, r.Tool)
	if err := p.send(map[string]any{"type": "control_request", "request_id": "contract-init", "request": map[string]any{"subtype": "initialize", "hooks": nil}}); err != nil {
		r.check(t, "control-initialize", false, err.Error())
		return
	}
	f, err := p.read(func(f map[string]json.RawMessage) bool {
		return textField(f, "type") == "control_response" && textField(objectField(f, "response"), "request_id") == "contract-init"
	})
	if err != nil {
		r.check(t, "control-initialize", false, err.Error())
		return
	}
	response := objectField(f, "response")
	r.check(t, "control-response-correlation", textField(response, "subtype") == "success", map[string]string{"subtype": textField(response, "subtype"), "request_id": textField(response, "request_id")})
	body := objectField(response, "response")
	r.check(t, "control-permission-mode", textField(body, "current_permission_mode") == "dontAsk", textField(body, "current_permission_mode"))
	// system/init (session_id, claude_code_version, tools) is emitted at the
	// first user turn, not asserted from the different SDK control response.
	r.Facts = append(r.Facts, factResult{ID: "system-init-session-version-tools", Status: "not_measured"}, factResult{ID: "hook-round-trip", Status: "not_measured"})
}
