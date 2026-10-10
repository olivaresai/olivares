// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeApprovalCommandFactsReachAuthority(t *testing.T) {
	req := openCodeApprovalForFacts(t, map[string]any{
		"toolCallId": "call-command", "kind": "execute", "title": "provider prose is not a command",
		"rawInput":  map[string]any{"command": "printf ready", "description": "untrusted description"},
		"locations": []any{map[string]any{"path": "/workspace/fixture"}},
	})
	if req.CommandLine != "printf ready" || req.EffectiveCommandLine != "printf ready" || !req.FactsComplete {
		t.Fatalf("native command facts missing: command=%q effective=%q complete=%v", req.CommandLine, req.EffectiveCommandLine, req.FactsComplete)
	}
	if !reflect.DeepEqual(req.FilePaths, []string{"/workspace/fixture"}) || !reflect.DeepEqual(req.EffectiveFilePaths, []string{"/workspace/fixture"}) {
		t.Fatalf("native locations missing: paths=%v effective=%v", req.FilePaths, req.EffectiveFilePaths)
	}
}

func TestOpenCodeApprovalNativeMetadataPathsReachAuthority(t *testing.T) {
	for _, tc := range []struct {
		name    string
		call    map[string]any
		command string
		paths   []string
	}{
		{
			name:    "native shell alias",
			call:    map[string]any{"kind": "execute", "rawInput": map[string]any{"cmd": "printf alias"}},
			command: "printf alias",
		},
		{
			name:    "native command wins over alias",
			call:    map[string]any{"kind": "execute", "rawInput": map[string]any{"command": "printf primary", "cmd": "printf alias"}},
			command: "printf primary",
		},
		{
			name:  "native file metadata without locations",
			call:  map[string]any{"kind": "edit", "rawInput": map[string]any{"filepath": "/workspace/fixture/source.go", "diff": "body must not be retained"}},
			paths: []string{"/workspace/fixture/source.go"},
		},
		{
			name:  "native camel case file path",
			call:  map[string]any{"kind": "read", "rawInput": map[string]any{"filePath": "/workspace/fixture/read.go"}},
			paths: []string{"/workspace/fixture/read.go"},
		},
		{
			name:  "native search path",
			call:  map[string]any{"kind": "search", "rawInput": map[string]any{"path": "/workspace/fixture/search"}},
			paths: []string{"/workspace/fixture/search"},
		},
		{
			name: "native external directories",
			call: map[string]any{"kind": "other", "rawInput": map[string]any{
				"parentDir": "/workspace/fixture/external", "directories": []string{"/workspace/fixture/external", "/workspace/fixture/second"},
			}},
			paths: []string{"/workspace/fixture/external", "/workspace/fixture/second"},
		},
		{
			name: "native move metadata and repeated locations",
			call: map[string]any{
				"kind":      "edit",
				"locations": []any{map[string]any{"path": "/workspace/fixture/old.go"}},
				"rawInput": map[string]any{"files": []any{map[string]any{
					"filePath": "/workspace/fixture/old.go", "movePath": "/workspace/fixture/new.go", "patch": "body must not be retained",
				}}},
			},
			paths: []string{"/workspace/fixture/old.go", "/workspace/fixture/new.go"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := openCodeApprovalForFacts(t, tc.call)
			if !req.FactsComplete || req.CommandLine != tc.command || req.EffectiveCommandLine != tc.command || !reflect.DeepEqual(req.FilePaths, tc.paths) || !reflect.DeepEqual(req.EffectiveFilePaths, tc.paths) {
				t.Fatalf("native metadata missing: command=%q effective=%q paths=%v effective_paths=%v complete=%v", req.CommandLine, req.EffectiveCommandLine, req.FilePaths, req.EffectiveFilePaths, req.FactsComplete)
			}
		})
	}
}

func TestOpenCodeApprovalUnknownMetadataStaysIncomplete(t *testing.T) {
	for _, tc := range []struct {
		name    string
		call    map[string]any
		command string
		paths   []string
	}{
		{
			name: "title is not a command",
			call: map[string]any{"kind": "execute", "title": "printf invented", "rawOutput": map[string]any{"command": "printf output"}},
		},
		{
			name: "explicit empty command does not use alias",
			call: map[string]any{"kind": "execute", "rawInput": map[string]any{"command": "", "cmd": "printf alias"}},
		},
		{
			name:  "opaque input retains available location",
			call:  map[string]any{"kind": "other", "rawInput": "opaque native input", "locations": []any{map[string]any{"path": "/workspace/fixture/known"}}},
			paths: []string{"/workspace/fixture/known"},
		},
		{
			name: "unsupported command retains available location",
			call: map[string]any{"kind": "execute", "rawInput": map[string]any{"command": []string{"printf", "unknown shape"}},
				"locations": []any{map[string]any{"path": "/workspace/fixture/known"}}},
			paths: []string{"/workspace/fixture/known"},
		},
		{
			name:    "unsupported location retains command",
			call:    map[string]any{"kind": "execute", "rawInput": map[string]any{"command": "printf known"}, "locations": []any{map[string]any{"path": 42}}},
			command: "printf known",
		},
		{
			name:    "missing location is incomplete",
			call:    map[string]any{"kind": "execute", "rawInput": map[string]any{"command": "printf known"}, "locations": []any{map[string]any{}}},
			command: "printf known",
		},
		{
			name:  "missing source of a move is incomplete",
			call:  map[string]any{"kind": "edit", "rawInput": map[string]any{"files": []any{map[string]any{"movePath": "/workspace/fixture/new.go"}}}},
			paths: []string{"/workspace/fixture/new.go"},
		},
		{name: "no tool facts", call: map[string]any{"kind": "other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := openCodeApprovalForFacts(t, tc.call)
			if req.FactsComplete || req.CommandLine != tc.command || req.EffectiveCommandLine != tc.command || !reflect.DeepEqual(req.FilePaths, tc.paths) || !reflect.DeepEqual(req.EffectiveFilePaths, tc.paths) {
				t.Fatalf("unknown metadata was invented, erased known facts or marked complete: command=%q paths=%v complete=%v", req.CommandLine, req.FilePaths, req.FactsComplete)
			}
		})
	}
}

func TestOpenCodeApprovalBlankPathsRetainKnownFacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		call map[string]any
	}{
		{
			name: "blank directory sibling",
			call: map[string]any{"rawInput": map[string]any{"command": "ls", "directories": []string{"/owned", " "}}},
		},
		{
			name: "blank location sibling",
			call: map[string]any{
				"rawInput":  map[string]any{"command": "ls"},
				"locations": []any{map[string]any{"path": "/owned"}, map[string]any{"path": "\t\n"}},
			},
		},
		{
			name: "blank optional file path",
			call: map[string]any{
				"rawInput":  map[string]any{"command": "ls", "filePath": " "},
				"locations": []any{map[string]any{"path": "/owned"}},
			},
		},
		{
			name: "blank move target",
			call: map[string]any{"rawInput": map[string]any{
				"command": "ls", "files": []any{map[string]any{"filePath": "/owned", "movePath": " "}},
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := openCodeApprovalForFacts(t, tc.call)
			if req.FactsComplete || req.CommandLine != "ls" || req.EffectiveCommandLine != "ls" || !reflect.DeepEqual(req.FilePaths, []string{"/owned"}) || !reflect.DeepEqual(req.EffectiveFilePaths, []string{"/owned"}) {
				t.Fatalf("blank path erased known facts or marked them complete: command=%q effective=%q paths=%v effective_paths=%v complete=%v", req.CommandLine, req.EffectiveCommandLine, req.FilePaths, req.EffectiveFilePaths, req.FactsComplete)
			}
		})
	}
}

func TestOpenCodeApprovalOriginalFactsStayOutOfSerializedEvidence(t *testing.T) {
	const command = "PASSWORD=opencode-fixture-secret printf ready"
	const path = "/workspace/fixture/olvs_opencode123456789/file.go"
	const body = "private-file-body-never-an-approval-fact"
	req := openCodeApprovalForFacts(t, map[string]any{
		"kind": "execute", "title": body,
		"rawInput":  map[string]any{"command": command, "content": strings.Repeat(body, 256), "diff": body},
		"locations": []any{map[string]any{"path": path}},
		"content":   []any{map[string]any{"type": "diff", "path": path, "oldText": body, "newText": body}},
		"rawOutput": body,
	})
	if !req.FactsComplete || req.EffectiveCommandLine != command || !reflect.DeepEqual(req.EffectiveFilePaths, []string{path}) {
		t.Fatal("native effective command/path lost their exact process-only values")
	}
	evidence, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"opencode-fixture-secret", "olvs_opencode123456789", body} {
		if strings.Contains(string(evidence), private) {
			t.Fatal("serialized approval evidence retained an original secret or file contents")
		}
	}
	if !strings.Contains(req.CommandLine, "printf ready") || len(req.FilePaths) != 1 {
		t.Fatal("redaction removed the reviewable action or affected path")
	}
}

func TestOpenCodeApprovalOversizedFactsStayIncomplete(t *testing.T) {
	var locations []any
	for i := range 33 {
		locations = append(locations, map[string]any{"path": fmt.Sprintf("/workspace/fixture/%d.go", i)})
	}
	for _, tc := range []struct {
		name string
		call map[string]any
	}{
		{"command exceeds 1024 bytes", map[string]any{"rawInput": map[string]any{"command": strings.Repeat("x", 1025)}}},
		{"more than 32 unique paths", map[string]any{"rawInput": map[string]any{"command": "printf known"}, "locations": locations}},
		{"command and path share byte bound", map[string]any{"rawInput": map[string]any{"command": strings.Repeat("x", 1000)}, "locations": []any{map[string]any{"path": "/" + strings.Repeat("y", 24)}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := openCodeApprovalForFacts(t, tc.call)
			if req.FactsComplete || req.CommandLine != "" || req.EffectiveCommandLine != "" || len(req.FilePaths) != 0 || len(req.EffectiveFilePaths) != 0 {
				t.Fatal("oversized native facts were retained or silently presented as complete")
			}
		})
	}
}

func openCodeApprovalForFacts(t *testing.T, toolCall map[string]any) ProviderApprovalRequest {
	t.Helper()
	if _, exists := toolCall["toolCallId"]; !exists {
		toolCall["toolCallId"] = "call-facts"
	}
	requests := make(chan ProviderApprovalRequest, 1)
	peer := newOpenCodePeer(t, func(cfg *DriverSessionConfig) {
		cfg.Approve = func(_ context.Context, req ProviderApprovalRequest) (ProviderApprovalDecision, error) {
			requests <- req
			return ProviderApprovalDecision{}, nil
		}
	})
	t.Cleanup(func() { peer.session.Close(nil) })
	if _, err := peer.answerHandshake("ses_facts"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if _, err := peer.session.Input(context.Background(), "use the tool"); err != nil {
		t.Fatalf("input: %v", err)
	}
	_, _, _ = peer.nextRequest()
	peer.requestFromServer("permission-facts", acpReqRequestPermission, map[string]any{
		"sessionId": "ses_facts",
		"toolCall":  toolCall,
		"options": []any{
			map[string]any{"optionId": "once", "kind": "allow_once", "name": "Allow once"},
			map[string]any{"optionId": "always", "kind": "allow_always", "name": "Allow always"},
			map[string]any{"optionId": "reject", "kind": "reject_once", "name": "Reject"},
		},
	})
	var req ProviderApprovalRequest
	select {
	case req = <-requests:
		if req.Driver != providerDriverOpenCode || req.ConversationID != "ses_facts" || req.TurnID != peer.session.ActiveTurn() || req.RunRef != "run-test" || req.ProfileRef != "ppf_test" {
			t.Fatal("native facts lost their owned driver/conversation/turn/run/profile binding")
		}
		if req.Method != acpReqRequestPermission || req.Kind != acpKindToolCallPermission || !reflect.DeepEqual(req.Requested, []string{"once", "always", "reject"}) {
			t.Fatal("fact projection changed the permission family or offered grant IDs")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the owned permission never reached its authority")
	}
	reply := peer.next()
	result, _ := reply["result"].(map[string]any)
	outcome, _ := result["outcome"].(map[string]any)
	if reply["id"] != "permission-facts" || outcome["outcome"] != "selected" || outcome["optionId"] != "reject" {
		t.Fatalf("authority refusal changed: %v", reply)
	}
	return req
}
