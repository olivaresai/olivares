// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgentCoreExportConfig(t *testing.T) {
	if cfg, err := loadAgentCoreExportConfig(envFrom(nil), discardLog()); err != nil || len(cfg.Tenants) != 0 {
		t.Fatalf("missing env = (%v,%v), want empty nil", cfg, err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "agentcore-export.json")
	body := `{"tenants":[{
		"tenant":"018ff6a9-3f4a-7a22-8f49-111111111111",
		"region":"us-east-1",
		"account_id":"123456789012",
		"access_key_id":"AKIAEXAMPLE",
		"secret_access_key":"secret",
		"session_token":"token",
		"endpoint":"https://agentcore.test.local",
		"policy_engine_id":"pe-123",
		"enforcement_mode":"LOG_ONLY",
		"allowlist":["pe-123"],
		"mapping":{
			"workspace_gateways":{"payments":["arn:gw"]},
			"subject_claims":{"role:viewer":{"tag":"role","value":"viewer"}},
			"perm_actions":{"agent:read":["Target___read"]},
			"model_actions":{"frontier":["Target___invoke"]},
			"source_actions":{"github":["Target___write"]},
			"source_read_actions":{"github":["Target___read"]}
		}
	}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadAgentCoreExportConfig(envFrom(map[string]string{agentCoreExportConfigEnv: path}), discardLog())
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if len(cfg.Tenants) != 1 || cfg.Tenants[0].PolicyEngineID != "pe-123" {
		t.Fatalf("tenant decode mismatch: %+v", cfg)
	}
	m := cfg.Tenants[0].Mapping.toAgentCoreMapping()
	if got := m.SubjectClaims["role:viewer"]; got.Tag != "role" || got.Value != "viewer" {
		t.Fatalf("subject claim decode mismatch: %+v", got)
	}
	if got := m.WorkspaceGateways["payments"]; len(got) != 1 || got[0] != "arn:gw" {
		t.Fatalf("workspace mapping mismatch: %+v", got)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAgentCoreExportConfig(envFrom(map[string]string{agentCoreExportConfigEnv: bad}), discardLog()); err == nil {
		t.Fatal("malformed JSON must return an error")
	}
}
