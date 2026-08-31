// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// surfaceTitles returns the finding titles for a built catalog, for assertions.
func surfaceTitles(cat catalog) []string {
	fs := surfaceFindings("srv", cat, fixedTime())
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Title
	}
	return out
}

func TestSurfaceFindingsAdvertisedCapabilities(t *testing.T) {
	cat := catalog{server: InitializeResult{Capabilities: map[string]any{
		"tools":       map[string]any{},
		"elicitation": map[string]any{},
		"sampling":    map[string]any{},
		"logging":     map[string]any{},
		"completions": map[string]any{},
		"experimental": map[string]any{
			"tasks": map[string]any{},
		},
	}}}
	fs := surfaceFindings("srv", cat, fixedTime())

	var elicit, sampling *model.FindingReport
	titles := map[string]bool{}
	for i := range fs {
		titles[fs[i].Title] = true
		if strings.Contains(fs[i].Title, "elicitation") {
			elicit = &fs[i]
		}
		if strings.Contains(fs[i].Title, "sampling") {
			sampling = &fs[i]
		}
	}

	// elicitation + sampling are advertised → MCP10-tagged findings (input vectors).
	if elicit == nil || !strings.HasPrefix(elicit.Title, "[MCP10]") {
		t.Errorf("advertised elicitation should produce an [MCP10] finding, got %+v", elicit)
	}
	if sampling == nil || !strings.HasPrefix(sampling.Title, "[MCP10]") {
		t.Errorf("advertised sampling should produce an [MCP10] finding, got %+v", sampling)
	}
	if elicit != nil && elicit.Kind != findingSurface {
		t.Errorf("surface finding kind = %q", elicit.Kind)
	}
	// logging/completions surfaced as info catalog metadata.
	if !hasTitleContaining(fs, "logging") || !hasTitleContaining(fs, "completions") {
		t.Errorf("logging/completions should be surfaced: %+v", titles)
	}
	// experimental sub-feature surfaced by name.
	if !hasTitleContaining(fs, "experimental capability tasks") {
		t.Errorf("experimental.tasks should be surfaced by name: %+v", titles)
	}
}

func TestSurfaceFindingsToolOutputSchemaAndIcons(t *testing.T) {
	cat := catalog{
		server: InitializeResult{Capabilities: map[string]any{"tools": map[string]any{}}},
		tools: []Tool{
			{Name: "structured", OutputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "iconed", Icons: json.RawMessage(`[{"src":"data:image/png;base64,xx"}]`)},
			{Name: "plain"},
		},
	}
	titles := surfaceTitles(cat)
	if !containsSub(titles, "structured output") {
		t.Errorf("a tool with outputSchema should surface structured-output metadata: %v", titles)
	}
	if !containsSub(titles, "icons") {
		t.Errorf("a tool with icons should surface icons metadata: %v", titles)
	}
}

func TestSurfaceFindingsEmptyWhenNoGovernedSurface(t *testing.T) {
	// A server advertising only the listing surface (tools/resources/prompts) with
	// no governed capabilities and no outputSchema/icons emits NO surface findings —
	// no fabrication of a surface that is not advertised.
	cat := catalog{server: InitializeResult{Capabilities: map[string]any{
		"tools": map[string]any{}, "resources": map[string]any{}, "prompts": map[string]any{},
	}}}
	if fs := surfaceFindings("srv", cat, fixedTime()); len(fs) != 0 {
		t.Errorf("no governed surface should emit no findings, got %+v", fs)
	}
}

func hasTitleContaining(fs []model.FindingReport, sub string) bool {
	for _, f := range fs {
		if strings.Contains(f.Title, sub) {
			return true
		}
	}
	return false
}

func containsSub(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
