// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestToolVisibilitySignalFull(t *testing.T) {
	f := ToolVisibilitySignal("session-1", time.Now(), false, false)
	if f.Kind != findingGovernance {
		t.Errorf("Kind = %q, want %q", f.Kind, findingGovernance)
	}
	if f.SubjectKind != subjectToolVisibility {
		t.Errorf("SubjectKind = %q, want %q", f.SubjectKind, subjectToolVisibility)
	}
	if !strings.Contains(f.Title, "FULL") {
		t.Errorf("full visibility title should contain FULL, got %q", f.Title)
	}
	if f.Severity != model.SeverityInfo {
		t.Errorf("Severity = %q, want info", f.Severity)
	}
}

func TestToolVisibilitySignalPartialProgrammatic(t *testing.T) {
	f := ToolVisibilitySignal("session-2", time.Now(), true, false)
	if !strings.Contains(f.Title, "PARTIAL") {
		t.Errorf("partial visibility title should contain PARTIAL, got %q", f.Title)
	}
	if !strings.Contains(f.Title, "programmatic tool calling") {
		t.Errorf("title should name the reason, got %q", f.Title)
	}
}

func TestToolVisibilitySignalPartialToolSearch(t *testing.T) {
	f := ToolVisibilitySignal("session-3", time.Now(), false, true)
	if !strings.Contains(f.Title, "PARTIAL") {
		t.Errorf("partial visibility title should contain PARTIAL, got %q", f.Title)
	}
	if !strings.Contains(f.Title, "tool search") {
		t.Errorf("title should name the reason, got %q", f.Title)
	}
}

func TestToolVisibilitySignalPartialBoth(t *testing.T) {
	f := ToolVisibilitySignal("session-4", time.Now(), true, true)
	if !strings.Contains(f.Title, "PARTIAL") {
		t.Errorf("partial visibility title should contain PARTIAL, got %q", f.Title)
	}
	if !strings.Contains(f.Title, "programmatic tool calling") || !strings.Contains(f.Title, "tool search") {
		t.Errorf("title should name both reasons, got %q", f.Title)
	}
}

func TestToolInventorySignalAllDeclared(t *testing.T) {
	declared := []string{"Read", "Edit", "Bash"}
	observed := []string{"Read", "Edit", "Bash"}
	f := ToolInventorySignal("session-5", time.Now(), declared, observed)
	if !strings.Contains(f.Title, "3 declared") {
		t.Errorf("title should show 3 declared, got %q", f.Title)
	}
	if !strings.Contains(f.Title, "all observed tools were declared") {
		t.Errorf("no late-loaded tools, but title says otherwise: %q", f.Title)
	}
}

func TestToolInventorySignalLateLoaded(t *testing.T) {
	declared := []string{"Read", "Edit"}
	observed := []string{"Read", "Edit", "Bash", "WebFetch"}
	f := ToolInventorySignal("session-6", time.Now(), declared, observed)
	if !strings.Contains(f.Title, "2 declared") {
		t.Errorf("title should show 2 declared, got %q", f.Title)
	}
	if !strings.Contains(f.Title, "2 loaded post-hoc") {
		t.Errorf("should flag 2 late-loaded tools, got %q", f.Title)
	}
	if !strings.Contains(f.Title, "tool search") {
		t.Errorf("should mention tool search, got %q", f.Title)
	}
}

func TestToolInventorySignalMinimalData(t *testing.T) {
	declared := []string{"Read"}
	observed := []string{"Read", "Bash"}
	f := ToolInventorySignal("session-7", time.Now(), declared, observed)
	// The finding should never contain actual tool names — only counts.
	for _, toolName := range []string{"Read", "Bash"} {
		if strings.Contains(f.DetailHash, toolName) {
			t.Errorf("DetailHash should not contain tool name %q", toolName)
		}
	}
	if len(f.DetailHash) != 64 {
		t.Errorf("DetailHash should be a SHA-256 hex (64 chars), got %d chars", len(f.DetailHash))
	}
}

func TestToolSearchActive(t *testing.T) {
	tools := []MCPToolConfig{
		{Enabled: true, DeferLoading: false},
		{Enabled: true, DeferLoading: false},
	}
	if ToolSearchActive(tools) {
		t.Error("no defer_loading tools should return false")
	}
	tools = append(tools, MCPToolConfig{Enabled: true, DeferLoading: true})
	if !ToolSearchActive(tools) {
		t.Error("a defer_loading tool should return true")
	}
}
