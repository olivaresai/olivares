// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import "testing"

func TestParseRedactionDefaultStructural(t *testing.T) {
	for _, in := range []string{"", "   ", ","} {
		p, honored := parseRedaction(in)
		if p != (redactionPolicy{}) {
			t.Errorf("parseRedaction(%q) = %+v, want zero (structural-only)", in, p)
		}
		if len(honored) != 0 {
			t.Errorf("parseRedaction(%q) honored = %v, want none", in, honored)
		}
		if p.summary() != "structural-only" {
			t.Errorf("summary = %q", p.summary())
		}
	}
}

func TestParseRedactionAllowlist(t *testing.T) {
	p, honored := parseRedaction(" Tool_Content , user_prompts , bogus ")
	if !p.allows(capToolContent) || !p.allows(capUserPrompts) {
		t.Errorf("policy = %+v, want tool_content + user_prompts", p)
	}
	if p.allows(capToolDetails) || p.allows(capRawAPIBodies) {
		t.Errorf("policy widened beyond the allowlist: %+v", p)
	}
	// Unknown token "bogus" is ignored, never honored (degrade safe).
	if len(honored) != 2 {
		t.Errorf("honored = %v, want exactly the two recognized categories", honored)
	}
	// summary lists enabled categories in canonical order, not input order.
	if got := p.summary(); got != "structural+user_prompts+tool_content" {
		t.Errorf("summary = %q", got)
	}
}

func TestRedactionAllowsUnknownCategory(t *testing.T) {
	p, _ := parseRedaction("tool_content")
	if p.allows("not_a_category") {
		t.Error("unknown category must never be allowed")
	}
}

func TestSelfAuditFinding(t *testing.T) {
	p, _ := parseRedaction("tool_content")
	f := p.selfAuditFinding(testTime)
	if f.Kind != "self_audit" || f.SubjectRef != Name {
		t.Errorf("self-audit finding = %+v", f)
	}
	if f.Title != "Claude telemetry content-capture posture: structural+tool_content" {
		t.Errorf("title = %q", f.Title)
	}
	if f.DetailHash == "" {
		t.Error("self-audit finding should carry a detail hash")
	}
}
