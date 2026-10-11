// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package mcpgateway

import (
	"encoding/json"
	"testing"
)

// TestRevisionControlsDecodePresence pins the decode half of the revision posture:
// presence, null and value of the two controls come from the same occurrence, in
// any capitalization, and a control stated twice leaves no value behind. The
// document decodes exactly as the operator loader decodes it (json.Unmarshal into
// Config). The composition half — which of these documents refuse and which mode
// the built Resource Server resolves — runs through the production loader and
// builder in cmd/olivares (TestMCPGatewayRevisionControlsPresenceMatchesValue),
// with the same twenty documents.
func TestRevisionControlsDecodePresence(t *testing.T) {
	cases := []struct {
		name            string
		fields          string
		wantModeField   string
		wantModePresent bool
		wantBool        bool
		wantBoolPresent bool
	}{
		{name: "R1 noncanonical mode null is an explicit non-value", fields: `"REVISION_MODE":null`, wantModePresent: true},
		{name: "R1 noncanonical mode empty is an explicit non-value", fields: `"REVISION_MODE":""`, wantModePresent: true},
		{name: "R1 noncanonical false contradicts rc-strict", fields: `"revision_mode":"rc-strict","NEXT_REVISION_HEADERS":false`, wantModeField: RevisionModeRCStrict, wantModePresent: true, wantBoolPresent: true},
		{name: "R1 noncanonical true contradicts legacy", fields: `"revision_mode":"legacy","NEXT_REVISION_HEADERS":true`, wantModeField: RevisionModeLegacy, wantModePresent: true, wantBool: true, wantBoolPresent: true},
		{name: "R1 mode supplied twice with a null", fields: `"revision_mode":"dual","revision_mode":null`},
		{name: "R1 boolean supplied twice with a null", fields: `"revision_mode":"legacy","next_revision_headers":true,"next_revision_headers":null`, wantModeField: RevisionModeLegacy, wantModePresent: true},
		{name: "mode duplicated across capitalizations", fields: `"revision_mode":"dual","REVISION_MODE":"legacy"`},
		{name: "mode alias first then a canonical null", fields: `"REVISION_MODE":"dual","revision_mode":null`},
		{name: "boolean duplicated across capitalizations", fields: `"Next_Revision_Headers":true,"next_revision_headers":false`},
		{name: "duplicates agreeing in value are still ambiguous", fields: `"revision_mode":"dual","REVISION_MODE":"dual","next_revision_headers":true,"NEXT_REVISION_HEADERS":true`},
		{name: "single alias mode", fields: `"REVISION_MODE":"dual"`, wantModeField: RevisionModeDual, wantModePresent: true},
		{name: "single alias boolean true", fields: `"NEXT_REVISION_HEADERS":true`, wantBool: true, wantBoolPresent: true},
		{name: "single alias boolean false", fields: `"Next_Revision_Headers":false`, wantBoolPresent: true},
		{name: "single alias pair agreeing", fields: `"Revision_Mode":"rc-strict","NEXT_REVISION_HEADERS":true`, wantModeField: RevisionModeRCStrict, wantModePresent: true, wantBool: true, wantBoolPresent: true},
		{name: "single alias boolean null stays compatible with absent", fields: `"NEXT_REVISION_HEADERS":null`},
		{name: "neither control", fields: ""},
		{name: "canonical boolean true is dual", fields: `"next_revision_headers":true`, wantBool: true, wantBoolPresent: true},
		{name: "canonical explicit mode without a boolean", fields: `"revision_mode":"dual"`, wantModeField: RevisionModeDual, wantModePresent: true},
		{name: "canonical rc-strict with an agreeing true", fields: `"revision_mode":"rc-strict","next_revision_headers":true`, wantModeField: RevisionModeRCStrict, wantModePresent: true, wantBool: true, wantBoolPresent: true},
		{name: "canonical single boolean null", fields: `"next_revision_headers":null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := `{"mcp":{"resource":"https://mcp.review.example"`
			if tc.fields != "" {
				doc += "," + tc.fields
			}
			doc += `}}`
			var gateway Config
			if err := json.Unmarshal([]byte(doc), &gateway); err != nil {
				t.Fatalf("the document must still decode (the refusals are the composition's): %v", err)
			}
			cfg := gateway.MCP
			if cfg.RevisionMode != tc.wantModeField || cfg.revisionModePresent != tc.wantModePresent {
				t.Errorf("decoded revision_mode = (%q, present %t), want (%q, present %t)",
					cfg.RevisionMode, cfg.revisionModePresent, tc.wantModeField, tc.wantModePresent)
			}
			if cfg.NextRevisionHeaders != tc.wantBool || cfg.nextRevisionHeadersPresent != tc.wantBoolPresent {
				t.Errorf("decoded next_revision_headers = (%t, present %t), want (%t, present %t)",
					cfg.NextRevisionHeaders, cfg.nextRevisionHeadersPresent, tc.wantBool, tc.wantBoolPresent)
			}
		})
	}
}
