// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package grok

import (
	"strings"
	"testing"
)

func TestRenderRequirementsStrictSandbox(t *testing.T) {
	raw, err := RenderRequirements(ManagedPolicy{SandboxProfile: "strict"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "[sandbox]") || !strings.Contains(s, `profile = "strict"`) {
		t.Fatalf("render:\n%s", s)
	}
}

func TestRenderRequirementsRejectsUnknownProfile(t *testing.T) {
	_, err := RenderRequirements(ManagedPolicy{SandboxProfile: "yolo"})
	if err == nil {
		t.Fatal("expected unknown profile to fail")
	}
}

func TestRenderRequirementsMCPLockdown(t *testing.T) {
	empty := []string{}
	raw, err := RenderRequirements(ManagedPolicy{AllowedMCPServers: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "mcp_servers") {
		t.Fatalf("lockdown should render mcp_servers table:\n%s", raw)
	}
}
