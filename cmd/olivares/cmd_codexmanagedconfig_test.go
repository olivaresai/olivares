// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"strings"
	"testing"
)

// runCodexManagedConfig executes the `codex managed-config` command with the given policy
// JSON on stdin and the given flag args, returning stdout and any error.
func runCodexManagedConfig(t *testing.T, policyJSON string, args ...string) (string, error) {
	t.Helper()
	cmd := newCodexManagedConfigCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(policyJSON))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestCodexManagedConfigRender(t *testing.T) {
	policy := `{
	  "requirements": {
	    "allowed_sandbox_modes": ["read-only", "workspace-write"],
	    "allow_remote_control": false,
	    "allow_managed_hooks_only": true,
	    "allowed_mcp_servers": [{"name": "docs", "command": "codex-mcp"}]
	  },
	  "managed_config": {
	    "approval_policy": "on-request",
	    "otel": {"exporter": "otlp-http", "log_user_prompt": false, "endpoint": "https://otel.example/v1/logs"}
	  }
	}`
	out, err := runCodexManagedConfig(t, policy)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	for _, want := range []string{
		"# requirements.toml",
		"allow_remote_control = false",
		"allow_managed_hooks_only = true",
		`allowed_sandbox_modes = ["read-only", "workspace-write"]`,
		"[mcp_servers.docs.identity]",
		"# managed_config.toml",
		`approval_policy = "on-request"`,
		"log_user_prompt = false",
		"https://otel.example/v1/logs",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("CLI output missing %q\n%s", want, out)
		}
	}
}

func TestCodexManagedConfigDenyClosed(t *testing.T) {
	// An unknown enum value must FAIL the render (never emit an invalid governance file).
	_, err := runCodexManagedConfig(t, `{"requirements":{"allowed_sandbox_modes":["read-only","yolo"]}}`)
	if err == nil {
		t.Fatal("an invalid policy must fail the render (deny-closed)")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected an invalidity error, got %v", err)
	}
}

func TestCodexManagedConfigValidateOnly(t *testing.T) {
	out, err := runCodexManagedConfig(t, `{"managed_config":{"sandbox_mode":"read-only"}}`, "--validate")
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ok:") {
		t.Errorf("validate-only should report ok, got %s", out)
	}
	if strings.Contains(out, "sandbox_mode =") {
		t.Errorf("validate-only must not write the rendered file, got %s", out)
	}
}

func TestCodexManagedConfigEmptyPolicy(t *testing.T) {
	// A policy that authors nothing is an error (nothing to render), not a silent no-op.
	if _, err := runCodexManagedConfig(t, `{}`); err == nil {
		t.Error("an empty policy must be rejected (nothing to render)")
	}
}
