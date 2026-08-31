// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import "testing"

// currency-refresh guards. semconv v1.42.0 (2026-06-12, #3696)
// moved gen_ai.* (and openai.*/mcp.*) from the main semantic-conventions repo to
// open-telemetry/semantic-conventions-genai; v1.41.1 (2026-05-11) remains the
// last VERSIONED vocabulary label, while the upstream shape is pinned separately
// to main@c321d7e, verified 2026-07-05 (0 releases). These tests fail loudly if
// the pins drift or the mcp.* attribute set changes shape.

// TestGenAISemconvPin pins the connector's semconv version to the verified value.
// It is MIRRORED (unexported, no live coupling) by modules/observability
// otelGenAIVersion and modules/recording semconvVersion — each package asserts the
// same literal, so a drift in any one is caught by its own test (a one-grep fix; see
// the mirror note in modules/observability/ingestion.go).
func TestGenAISemconvPin(t *testing.T) {
	const want = "1.41.1"
	if genAISemconvVersion != want {
		t.Fatalf("genAISemconvVersion = %q, want %q (last versioned release carrying "+
			"gen-ai; upstream shape pinned separately at main@c321d7e)",
			genAISemconvVersion, want)
	}
	// The current-dialect pin must equal the version pin: a normalized v1.37+ signal
	// is stamped with exactly this release.
	if genAIDialectCurrent != genAISemconvVersion {
		t.Fatalf("genAIDialectCurrent = %q must equal the pin %q", genAIDialectCurrent, genAISemconvVersion)
	}
	if genAISemconvUpstreamRepo != "open-telemetry/semantic-conventions-genai" {
		t.Fatalf("genAISemconvUpstreamRepo = %q", genAISemconvUpstreamRepo)
	}
	if genAISemconvUpstreamRef != "main@c321d7e, verified 2026-07-05" {
		t.Fatalf("genAISemconvUpstreamRef = %q", genAISemconvUpstreamRef)
	}
}

// TestMCPAttributeSet asserts the EXACT four mcp.* attributes the spec defines
// (re-verified 2026-07-05 against semantic-conventions-genai main@c321d7e
// docs/gen-ai/mcp.md):
// mcp.method.name (the only Required one), mcp.protocol.version, mcp.resource.uri,
// mcp.session.id. There is NO mcp.tool.name — the tool rides gen_ai.tool.name and
// the prompt rides gen_ai.prompt.name — so the connector must never key a tool or
// prompt off an mcp.*-namespaced name.
func TestMCPAttributeSet(t *testing.T) {
	want := map[string]string{
		"mcp.method.name":      attrMCPMethod,
		"mcp.protocol.version": attrMCPProtocolVersion,
		"mcp.resource.uri":     attrMCPResourceURI,
		"mcp.session.id":       attrMCPSession,
	}
	for spec, got := range want {
		if got != spec {
			t.Errorf("mcp attribute const = %q, want %q", got, spec)
		}
	}
	// The tool and prompt keys are gen_ai.*-namespaced, NOT mcp.* — proving there is
	// no mcp.tool.name in the mapped surface (the spec routes both through gen_ai.*).
	if attrGenAIToolName != "gen_ai.tool.name" {
		t.Errorf("tool attribute = %q, want gen_ai.tool.name (no mcp.tool.name exists)", attrGenAIToolName)
	}
	if attrGenAIPromptName != "gen_ai.prompt.name" {
		t.Errorf("mcp prompt attribute = %q, want gen_ai.prompt.name", attrGenAIPromptName)
	}
}
