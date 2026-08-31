// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/modules/knowledge"
)

func TestRetrievalToolPolicies(t *testing.T) {
	t.Run("default scope", func(t *testing.T) {
		policies := RetrievalToolPolicies("")
		if len(policies) != 3 {
			t.Fatalf("expected 3 policies, got %d", len(policies))
		}
		for _, p := range policies {
			if p.RequiredScope != "knowledge:retrieval:read" {
				t.Errorf("expected default scope, got %q for tool %q", p.RequiredScope, p.Name)
			}
			if p.Destructive {
				t.Errorf("retrieval tool %q should not be destructive", p.Name)
			}
		}
		names := map[string]bool{}
		for _, p := range policies {
			names[p.Name] = true
		}
		if !names["search_kb"] || !names["fetch_document"] || !names["list_kbs"] {
			t.Errorf("expected search_kb, fetch_document, list_kbs; got %v", names)
		}
	})

	t.Run("custom scope", func(t *testing.T) {
		policies := RetrievalToolPolicies("custom:scope")
		for _, p := range policies {
			if p.RequiredScope != "custom:scope" {
				t.Errorf("expected custom scope, got %q for tool %q", p.RequiredScope, p.Name)
			}
		}
	})
}

func TestRetrievalUpstreamConstruction(t *testing.T) {
	t.Run("nil module fails", func(t *testing.T) {
		_, err := newRetrievalUpstream(retrievalUpstreamConfig{})
		if err == nil {
			t.Fatal("expected error for nil module")
		}
	})
}

func TestRetrievalToolDefs(t *testing.T) {
	for _, def := range retrievalToolDefs {
		name, ok := def["name"].(string)
		if !ok || name == "" {
			t.Error("tool def missing name")
			continue
		}
		if _, ok := def["description"].(string); !ok {
			t.Errorf("tool %q missing description", name)
		}
		schema, ok := def["inputSchema"].(map[string]any)
		if !ok {
			t.Errorf("tool %q missing inputSchema", name)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("tool %q inputSchema type should be object", name)
		}
	}
}

func TestToolErrorResult(t *testing.T) {
	raw := toolErrorResult("test error message")
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal = %v", err)
	}
	if result["isError"] != true {
		t.Error("expected isError=true")
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatal("expected non-empty content array")
	}
	first := content[0].(map[string]any)
	if first["type"] != "text" {
		t.Error("expected type=text")
	}
	if first["text"] != "test error message" {
		t.Errorf("expected error message, got %q", first["text"])
	}
}

func TestToolSuccessResult(t *testing.T) {
	data := map[string]string{"key": "value"}
	raw, err := toolSuccessResult(data)
	if err != nil {
		t.Fatalf("toolSuccessResult = %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal = %v", err)
	}
	if result["isError"] != nil {
		t.Error("success result should not have isError")
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatal("expected non-empty content array")
	}
	first := content[0].(map[string]any)
	if first["type"] != "text" {
		t.Error("expected type=text")
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(first["text"].(string)), &parsed); err != nil {
		t.Fatalf("parse text = %v", err)
	}
	if parsed["key"] != "value" {
		t.Errorf("expected key=value, got %q", parsed["key"])
	}
}

func TestToolSuccessResultCarriesSourceMode(t *testing.T) {
	raw, err := toolSuccessResult(knowledge.QueryResult{
		Results: []knowledge.QueryResultItem{{
			ChunkID: "c1", DocumentID: "d1", SourceKind: "confluence", SourceRef: "confluence",
			SourceMode: "export", Title: "Runbook", Text: "redacted", Classification: "internal", Score: 1,
		}},
	})
	if err != nil {
		t.Fatalf("toolSuccessResult = %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal = %v", err)
	}
	content := result["content"].([]any)
	first := content[0].(map[string]any)
	var parsed knowledge.QueryResult
	if err := json.Unmarshal([]byte(first["text"].(string)), &parsed); err != nil {
		t.Fatalf("parse text = %v", err)
	}
	if len(parsed.Results) != 1 || parsed.Results[0].SourceMode != "export" {
		t.Fatalf("MCP success payload source_mode = %+v, want export", parsed.Results)
	}
}

func TestRetrievalUpstreamToolsList(t *testing.T) {
	ru := &retrievalUpstream{}
	result, err := ru.handleToolsList()
	if err != nil {
		t.Fatalf("handleToolsList = %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal = %v", err)
	}
	tools, ok := parsed["tools"].([]any)
	if !ok {
		t.Fatal("expected tools array")
	}
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
	names := map[string]bool{}
	for _, tool := range tools {
		tm := tool.(map[string]any)
		names[tm["name"].(string)] = true
	}
	if !names["search_kb"] || !names["fetch_document"] || !names["list_kbs"] {
		t.Errorf("expected all 3 tools, got %v", names)
	}
}

func TestRetrievalUpstreamInitialize(t *testing.T) {
	ru := &retrievalUpstream{}
	result, err := ru.handleInitialize()
	if err != nil {
		t.Fatalf("handleInitialize = %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal = %v", err)
	}
	if parsed["protocolVersion"] == nil {
		t.Error("expected protocolVersion")
	}
	info := parsed["serverInfo"].(map[string]any)
	if info["name"] != "olivares-retrieval" {
		t.Errorf("expected olivares-retrieval, got %q", info["name"])
	}
}

func TestRetrievalUpstreamUnsupportedMethod(t *testing.T) {
	ru := &retrievalUpstream{}
	_, err := ru.Forward(context.Background(), mcp.UpstreamRequest{Method: "unsupported/method"})
	if err == nil {
		t.Fatal("expected error for unsupported method")
	}
}

// TestModuleContextAgentIdentityPropagation verifies that moduleContext sets
// AgentIdentity on the principal from the validated token subject. This
// ensures the source-scope resolver and knowledge Query receive the authenticated
// identity, not a caller-declared agent_ref.
func TestModuleContextAgentIdentityPropagation(t *testing.T) {
	ru := &retrievalUpstream{
		tenant: "tenant-1",
		role:   "viewer",
	}
	subject := "agent-ext-42"
	mc := ru.moduleContext(subject)

	if mc.Principal.AgentIdentity != subject {
		t.Errorf("moduleContext must set AgentIdentity = %q, got %q", subject, mc.Principal.AgentIdentity)
	}
	// The principal ID and display name also encode the subject.
	if mc.Principal.Actor() != "token:"+subject {
		t.Errorf("expected Actor() = token:%s, got %q", subject, mc.Principal.Actor())
	}
}
