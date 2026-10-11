// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"strings"
	"testing"
)

// AR measured on 09b: a policy create with an invalid required_approvals (-1,
// 65, 1.5) answered 400 with a Go struct-field decoder error in the text. The
// refusal must name the field and the allowed range in a plain sentence — no Go
// type, struct or unmarshal internals. Same for the other numeric fields.
func TestPolicyCreateNumericFieldsRefuseInPlainEnglish(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, tok := h.roleUser(admin, tenant, "op@acme.io", "admin")

	cases := []struct {
		name string
		spec map[string]any
		want string
	}{
		{"approvals negative", map[string]any{"required_approvals": -1}, "required_approvals must be a whole number from 0 to 64."},
		{"approvals past the cap", map[string]any{"required_approvals": 65}, "required_approvals must be a whole number from 0 to 64."},
		{"approvals not whole", map[string]any{"required_approvals": 1.5}, "required_approvals must be a whole number from 0 to 64."},
		{"expiry not whole", map[string]any{"expires_in_seconds": 1.5}, "expires_in_seconds must be a whole number"},
		{"expiry negative", map[string]any{"expires_in_seconds": -1}, "expires_in_seconds must be a whole number"},
		{"escalation not whole", map[string]any{"escalate_in_seconds": 2.5}, "escalate_in_seconds must be a whole number"},
		{"escalation negative", map[string]any{"escalate_in_seconds": -2}, "escalate_in_seconds must be a whole number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do("POST", "/v1/m/governance/policies", tok, map[string]any{
				"name": "x", "kind": "approval", "enabled": true, "spec": tc.spec,
			}, tenantHdr(tenant))
			if r.code != http.StatusBadRequest {
				t.Fatalf("status = %d %s, want 400", r.code, r.raw)
			}
			if !strings.Contains(r.raw, tc.want) {
				t.Fatalf("the refusal must say %q, got %s", tc.want, r.raw)
			}
			for _, leak := range []string{"approvalSpec", "unmarshal", "of type int", "cannot unmarshal", "Go struct"} {
				if strings.Contains(r.raw, leak) {
					t.Fatalf("the refusal leaks Go internals (%q): %s", leak, r.raw)
				}
			}
		})
	}

	// The control: valid values keep working, byte-equal to before.
	r := h.do("POST", "/v1/m/governance/policies", tok, map[string]any{
		"name": "ok", "kind": "approval", "enabled": true,
		"spec": map[string]any{"required_approvals": 2, "expires_in_seconds": 600, "escalate_in_seconds": 120},
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("a valid policy must create, got %d %s", r.code, r.raw)
	}
}
