// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"strings"
	"testing"
)

// otherToolSurfaces are the managed files of the tools besides Claude Code, each with a
// document the tool's own connector accepts and one it refuses.
var otherToolSurfaces = []struct {
	surface, good, bad, issue string
}{
	{"codex-requirements", "allowed_approval_policies = [\"on-request\"]\n", "allowed_approval_policies = [\"yolo\"]\n", "allowed_approval_policies"},
	{"codex-managed-config", "approval_policy = \"on-request\"\n", "approval_policy = [1\n", "not valid TOML"},
	{"grok-requirements", "[sandbox]\nprofile = \"strict\"\n", "[sandbox]\nprofile = \"wide-open\"\n", "wide-open"},
	{"opencode-config", `{"permission":{"edit":"deny","bash":"ask"},"share":"disabled"}`, `{"permission":{"edit":"sometimes"}}`, "permission.edit"},
	{"gemini-cli-settings", `{"security":{"disableYoloMode":true}}`, `{"general":{"defaultApprovalMode":"yolo"}}`, "defaultApprovalMode"},
}

// Every tool's managed file is an authoring surface in every edition: validate answers
// with the tool's own verdict, dry-run accepts a valid document, and an unknown surface
// is refused with the full list.
func TestOtherToolManagedSurfacesAreAuthored(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	hdr := tenantHdr(tenant)

	for _, tc := range otherToolSurfaces {
		t.Run(tc.surface, func(t *testing.T) {
			base := "/v1/m/claude-policy/" + tc.surface
			good := h.do("POST", base+"/validate", tok, map[string]any{"content": tc.good}, hdr)
			if good.code != http.StatusOK || good.body["ok"] != true {
				t.Fatalf("validate of a good %s document = %d %s; want ok", tc.surface, good.code, good.raw)
			}
			bad := h.do("POST", base+"/validate", tok, map[string]any{"content": tc.bad}, hdr)
			if bad.code != http.StatusOK || bad.body["ok"] != false || !strings.Contains(bad.raw, tc.issue) {
				t.Fatalf("validate of a bad %s document = %d %s; want ok=false naming %q", tc.surface, bad.code, bad.raw, tc.issue)
			}
			if r := h.do("POST", base+"/dry-run", tok, map[string]any{"content": tc.good}, hdr); r.code != http.StatusOK {
				t.Fatalf("dry-run of a good %s document = %d %s", tc.surface, r.code, r.raw)
			}
			if r := h.do("POST", base+"/dry-run", tok, map[string]any{"content": tc.bad}, hdr); r.code != http.StatusBadRequest {
				t.Fatalf("dry-run of a bad %s document = %d %s; want 400", tc.surface, r.code, r.raw)
			}
		})
	}

	r := h.do("POST", "/v1/m/claude-policy/not-a-surface/validate", tok, map[string]any{"content": "{}"}, hdr)
	if r.code != http.StatusBadRequest {
		t.Fatalf("unknown surface = %d %s; want 400", r.code, r.raw)
	}
	for _, want := range []string{"managed-settings", "sandbox", "codex-requirements", "codex-managed-config", "grok-requirements", "opencode-config", "gemini-cli-settings"} {
		if !strings.Contains(r.raw, want) {
			t.Errorf("the unknown-surface refusal does not list %q: %s", want, r.raw)
		}
	}
}
