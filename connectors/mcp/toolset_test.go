// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"
)

// TestValidateToolNameSEP986 enforces the SEP-986 tool-name rules.
func TestValidateToolNameSEP986(t *testing.T) {
	valid := []string{"search", "io.acme.tool-1", "a", "A_b.c-D", strings.Repeat("x", 128)}
	for _, n := range valid {
		if err := validateToolName(n); err != nil {
			t.Errorf("validateToolName(%q) = %v, want nil", n, err)
		}
	}
	invalid := []string{"", "has space", "emoji😀", "slash/tool", "quote\"", strings.Repeat("x", 129)}
	for _, n := range invalid {
		if err := validateToolName(n); err == nil {
			t.Errorf("validateToolName(%q) = nil, want error", n)
		}
	}
}

// TestToolsetDenyByDefault: a nil/empty toolset denies; an absent tool denies; an
// explicit Deny denies; a valid in-policy tool resolves.
func TestToolsetDenyByDefault(t *testing.T) {
	if _, ok := (*Toolset)(nil).resolve("x"); ok {
		t.Error("nil toolset must deny")
	}
	ts, err := NewToolset([]ToolPolicy{
		{Name: "search", RequiredScope: "tools:read"},
		{Name: "kill", Deny: true},
	})
	if err != nil {
		t.Fatalf("NewToolset: %v", err)
	}
	if p, ok := ts.resolve("search"); !ok || p.RequiredScope != "tools:read" {
		t.Errorf("resolve(search) = (%+v,%v), want the policy", p, ok)
	}
	if _, ok := ts.resolve("absent"); ok {
		t.Error("an absent tool must deny-by-default")
	}
	if _, ok := ts.resolve("kill"); ok {
		t.Error("an explicit Deny entry must deny")
	}
	if _, ok := ts.resolve("bad name"); ok {
		t.Error("a SEP-986-invalid requested name must never resolve")
	}
}

// TestNewToolsetRejectsBadConfig: a malformed (SEP-986) policy name, or a duplicate, is
// a configuration error — never a silently-ignored entry.
func TestNewToolsetRejectsBadConfig(t *testing.T) {
	if _, err := NewToolset([]ToolPolicy{{Name: "bad name"}}); err == nil {
		t.Error("a SEP-986-invalid policy name must fail config load")
	}
	if _, err := NewToolset([]ToolPolicy{{Name: "dup"}, {Name: "dup"}}); err == nil {
		t.Error("a duplicate tool policy must fail config load")
	}
}
