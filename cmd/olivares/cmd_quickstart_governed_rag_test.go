// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestQuickstartGovernedRAGWritesLiveSourceAndRetrievalConfig(t *testing.T) {
	dir := t.TempDir()
	paths, err := writeGovernedRAGQuickstartFiles(dir, quickstartGovernedRAGOptions{
		source:             "s3",
		sourceName:         "prod-runbooks-live",
		credentialRef:      "store:s3/prod-runbooks-read",
		bucket:             "prod-runbooks",
		prefix:             "claude/",
		region:             "us-east-1",
		agentGatewayListen: "127.0.0.1:8446",
		tenantID:           "ten_demo",
		kbName:             "governed-data",
		agentRef:           "claude-code-governed",
		agentName:          "Claude Code governed RAG",
		identityRef:        "agent:claude-code-governed",
		clearance:          "confidential",
		groupRef:           "group:engineering",
		mcpIssuer:          "https://idp.example.com/",
		mcpJWKSURL:         "https://idp.example.com/.well-known/jwks.json",
	})
	if err != nil {
		t.Fatalf("write governed RAG quickstart: %v", err)
	}

	sources := readJSONFile(t, paths.sources)
	docs := sources["documents"].([]any)
	firstDoc := docs[0].(map[string]any)
	if firstDoc["kind"] != "s3content" {
		t.Fatalf("source kind = %v, want s3content", firstDoc["kind"])
	}
	cfg := firstDoc["config"].(map[string]any)
	if cfg["mode"] != "live" || cfg["credential_ref"] != "store:s3/prod-runbooks-read" || cfg["bucket"] != "prod-runbooks" {
		t.Fatalf("source config = %+v, want live S3 with credential_ref", cfg)
	}

	gateway := readJSONFile(t, paths.gateway)
	mcp := gateway["mcp"].(map[string]any)
	retrieval := mcp["retrieval"].(map[string]any)
	if retrieval["enabled"] != true || retrieval["scope"] != "knowledge:retrieval:read" {
		t.Fatalf("retrieval config = %+v, want enabled knowledge scope", retrieval)
	}
	if mcp["tenant"] != "ten_demo" {
		t.Fatalf("mcp tenant = %v, want ten_demo", mcp["tenant"])
	}

	script, err := os.ReadFile(paths.bootstrap)
	if err != nil {
		t.Fatalf("read bootstrap script: %v", err)
	}
	if !strings.Contains(string(script), "source_mode=live") || !strings.Contains(string(script), "retrieval_semantic") {
		t.Fatalf("bootstrap script must be honest about semantic status and live provenance:\n%s", script)
	}
}

func TestQuickstartGovernedRAGRejectsInvalidJWKSFile(t *testing.T) {
	dir := t.TempDir()
	badJWKS := dir + "/bad-jwks.json"
	if err := os.WriteFile(badJWKS, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("write bad jwks: %v", err)
	}
	_, err := writeGovernedRAGQuickstartFiles(dir, quickstartGovernedRAGOptions{
		source:             "s3",
		sourceName:         "prod-runbooks-live",
		credentialRef:      "store:s3/prod-runbooks-read",
		bucket:             "prod-runbooks",
		agentGatewayListen: "127.0.0.1:8446",
		kbName:             "governed-data",
		agentRef:           "claude-code-governed",
		agentName:          "Claude Code governed RAG",
		identityRef:        "agent:claude-code-governed",
		mcpIssuer:          "https://idp.example.com/",
		mcpJWKSFile:        badJWKS,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("invalid JWKS file error = %v, want invalid JSON", err)
	}
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return out
}
