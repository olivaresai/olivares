// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/governance"
)

// cookbookCedarPolicy is the policy the deny-closed cookbook and the CLI recipes
// tell an operator to dry-run; keep it equal to the cookbook.
const cookbookCedarPolicy = `permit(principal, action, resource);

forbid(principal, action, resource)
  when { resource.kind == "credential" && resource.sensitivity == "secret" };`

// TestPdpDryRunAcceptsPublishedExampleRequest proves the request body every help
// text and doc shows (PDPExampleRequestJSON) is one the live dry-run and explain
// routes accept, and that the cookbook policy denies it. A doc example is only as
// good as the engine's own answer to it: the cookbook once showed a body (string
// principal, `action`) that these routes rejected with a bare 400.
func TestPdpDryRunAcceptsPublishedExampleRequest(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	hdr := tenantHdr(tenant)
	for _, route := range []string{"dry-run", "explain"} {
		got := h.do("POST", "/v1/m/governance/pdp/"+route, tok, map[string]any{
			"engine": "cedar", "source": cookbookCedarPolicy,
			"request": json.RawMessage(governance.PDPExampleRequestJSON),
		}, hdr)
		if got.code != http.StatusOK {
			t.Fatalf("%s rejected the published example request: %d %s", route, got.code, got.raw)
		}
		if got.body["allow"] != false {
			t.Fatalf("%s: the cookbook forbid must deny the example secret credential: %s", route, got.raw)
		}
		// The cookbook policy never reads `permission` or the principal, so the
		// decode of those members is proved by a policy that does.
		byPermission := h.do("POST", "/v1/m/governance/pdp/"+route, tok, map[string]any{
			"engine": "cedar", "source": `forbid(principal, action, resource) when { context.permission == "models:keys:read" };`,
			"request": json.RawMessage(governance.PDPExampleRequestJSON),
		}, hdr)
		if byPermission.code != http.StatusOK || byPermission.body["allow"] != false {
			t.Fatalf("%s: the example's permission member must reach the policy: %d %s", route, byPermission.code, byPermission.raw)
		}
	}
}

func TestPdpDecodeErrorsNameFields(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	for _, route := range []string{"dry-run", "explain"} {
		for _, tc := range []struct{ request, field string }{
			{`{"principal":{"kind":"user"},"action":"read","resource":{"kind":"file"}}`, "request.action"},
			{`{"principal":"private-value","permission":"read","resource":{"kind":"file"}}`, "request.principal"},
			{`{"principal":{"kind":42},"permission":"read"}`, "request.principal.kind"},
		} {
			got := h.do("POST", "/v1/m/governance/pdp/"+route, tok, map[string]any{
				"engine": "cedar", "source": cookbookCedarPolicy, "request": json.RawMessage(tc.request),
			}, tenantHdr(tenant))
			envelope, ok := got.body["error"].(map[string]any)
			if !ok || got.code != http.StatusBadRequest || envelope["code"] != "module_error" {
				t.Fatalf("%s error contract changed: %d %s", route, got.code, got.raw)
			}
			msg, _ := envelope["message"].(string)
			if !strings.Contains(msg, tc.field) {
				t.Errorf("%s: message %q does not name %s", route, msg, tc.field)
			}
			for _, leak := range []string{"pdpExampleRequest", "pdpRequestBody", "private-value", "Go struct"} {
				if strings.Contains(msg, leak) {
					t.Errorf("message exposes %q: %s", leak, msg)
				}
			}
		}
	}
}
