// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestDeclaredCapabilitiesFromConfig proves the CLA-14 reactor mapping: a config-sourced
// edge (Source=config, OriginKind=workspace) for each declared surface becomes a wiring
// row of the right DECLARED capability kind (subagent/skill/plugin/output_style), tagged
// signal_source=config so the console distinguishes declared from observed. Skills reuse
// the existing capSkill so a Skill declared AND observed collapses onto one node.
func TestDeclaredCapabilitiesFromConfig(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	at := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	obs := []sdkmodel.Observation{
		edge("workspace", "proj", "config.subagent", "code-reviewer", sdkmodel.ModeUnknown, sdkmodel.SignalConfig, sdkmodel.ConfidenceAttributed, "", at),
		edge("workspace", "proj", "config.skill", "deploy", sdkmodel.ModeUnknown, sdkmodel.SignalConfig, sdkmodel.ConfidenceAttributed, "", at),
		edge("workspace", "proj", "config.plugin", "myplugin", sdkmodel.ModeUnknown, sdkmodel.SignalConfig, sdkmodel.ConfidenceAttributed, "", at),
		edge("workspace", "proj", "config.output_style", "Diagrams first", sdkmodel.ModeUnknown, sdkmodel.SignalConfig, sdkmodel.ConfidenceAttributed, "", at),
		// settings-declared hooks + project-declared MCP servers.
		edge("workspace", "proj", "config.hook", "PreToolUse", sdkmodel.ModeUnknown, sdkmodel.SignalConfig, sdkmodel.ConfidenceAttributed, "", at),
		edge("workspace", "proj", "config.mcp_server", "github", sdkmodel.ModeUnknown, sdkmodel.SignalConfig, sdkmodel.ConfidenceAttributed, "", at),
	}
	stop := h.runEdges(t, tenant, obs, 6)
	defer stop()

	r := h.do("GET", "/v1/m/capabilities/wiring", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("wiring = %d %s", r.code, r.raw)
	}
	byKey := map[string]map[string]any{}
	for _, e := range r.body["edges"].([]any) {
		em := e.(map[string]any)
		byKey[em["capability_kind"].(string)+"/"+em["capability_ref"].(string)] = em
	}
	for _, want := range []string{
		"subagent/code-reviewer",
		"skill/deploy",
		"plugin/myplugin",
		"output_style/Diagrams first",
		// the hook event is a new declared kind; the declared MCP server
		// collapses onto the runtime mcp_server kind (declared-vs-observed).
		"hook/PreToolUse",
		"mcp_server/github",
	} {
		em := byKey[want]
		if em == nil {
			t.Errorf("missing declared capability %q (got %v)", want, mapKeys(byKey))
			continue
		}
		// signal_sources must mark it DECLARED (config), distinct from observed (otel/...).
		found := false
		for _, s := range toStrings(em["signal_sources"]) {
			if s == "config" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s signal_sources = %v, want to include 'config' (declared, not observed)", want, em["signal_sources"])
		}
	}
}

func toStrings(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func mapKeys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
