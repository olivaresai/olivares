//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"strings"
	"testing"
)

// Fleet-wide managed settings are Business: publishing a revision, its history,
// the signed pull, the attested check-in and the drift view answer 501 and name
// Business. The no-effect authoring reads stay in Community.
func TestCommunityFleetManagedSettingsUnavailable(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	hdr := tenantHdr(tenant)
	const base = "/v1/m/claude-policy/managed-settings"

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/publish", map[string]any{"content": `{"forceRemoteSettingsRefresh":true}`}},
		{"GET", "/versions", nil},
		{"GET", "/versions/1", nil},
		{"GET", "/artifact", nil},
		{"POST", "/checkin", map[string]any{"scope": "host-1"}},
		{"GET", "/distribution", nil},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := h.do(tc.method, base+tc.path, tok, tc.body, hdr)
			if r.code != http.StatusNotImplemented {
				t.Fatalf("Community %s %s = %d %s; want 501", tc.method, tc.path, r.code, r.raw)
			}
			errBody, _ := r.body["error"].(map[string]any)
			if msg, _ := errBody["message"].(string); !strings.Contains(msg, "Business") {
				t.Fatalf("the refusal must name Business in error.message: %s", r.raw)
			}
		})
	}

	for _, path := range []string{"/validate", "/dry-run"} {
		r := h.do("POST", base+path, tok, map[string]any{"content": `{"forceRemoteSettingsRefresh":true}`}, hdr)
		if r.code != http.StatusOK {
			t.Fatalf("Community authoring %s = %d %s; want 200", path, r.code, r.raw)
		}
		if path == "/dry-run" && !strings.Contains(r.raw, "not diffed") {
			t.Fatalf("a dry-run with no observed-config source must say the effect was not diffed: %s", r.raw)
		}
	}
}

// The refusal sits behind the route's permission tier: a viewer still gets 403 on the
// admin and write routes, and reaches the 501 only on the read routes.
func TestCommunityFleetManagedSettingsKeepsItsPermissionTiers(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, viewer := h.roleUser(admin, tenant, "viewer@acme.io", "viewer")
	hdr := tenantHdr(tenant)
	const base = "/v1/m/claude-policy/managed-settings"

	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{"POST", "/publish", map[string]any{"content": `{}`}, http.StatusForbidden},
		{"POST", "/checkin", map[string]any{"scope": "host-1"}, http.StatusForbidden},
		{"GET", "/versions", nil, http.StatusNotImplemented},
		{"GET", "/versions/1", nil, http.StatusNotImplemented},
		{"GET", "/artifact", nil, http.StatusNotImplemented},
		{"GET", "/distribution", nil, http.StatusNotImplemented},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			if r := h.do(tc.method, base+tc.path, viewer, tc.body, hdr); r.code != tc.want {
				t.Fatalf("viewer %s %s = %d %s; want %d", tc.method, tc.path, r.code, r.raw, tc.want)
			}
		})
	}
}

// The workstation tool inventory rides on the same fleet check-in and is Business:
// Community answers its read route and a check-in that carries tools with the same
// 501 that names Business.
func TestCommunityFleetInventoryUnavailable(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	hdr := tenantHdr(tenant)
	for _, tc := range []struct {
		name, method, path string
		body               any
	}{
		{"read", "GET", "/v1/m/claude-policy/inventory", nil},
		{"check-in with tools", "POST", "/v1/m/claude-policy/managed-settings/checkin", map[string]any{
			"scope": "host-1",
			"tools": []any{map[string]any{"driver": "claude", "version": "2.1.296", "state": "ready"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do(tc.method, tc.path, tok, tc.body, hdr)
			if r.code != http.StatusNotImplemented {
				t.Fatalf("Community %s %s = %d %s; want 501", tc.method, tc.path, r.code, r.raw)
			}
			errBody, _ := r.body["error"].(map[string]any)
			if msg, _ := errBody["message"].(string); !strings.Contains(msg, "Business") {
				t.Fatalf("the refusal must name Business in error.message: %s", r.raw)
			}
		})
	}
}

// The inventory read route is a read-tier route: a viewer reaches the refusal.
func TestCommunityFleetInventoryKeepsItsReadTier(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, viewer := h.roleUser(admin, tenant, "viewer@acme.io", "viewer")

	if r := h.do("GET", "/v1/m/claude-policy/inventory", viewer, nil, tenantHdr(tenant)); r.code != http.StatusNotImplemented {
		t.Fatalf("viewer GET /inventory = %d %s; want 501", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/claude-policy/inventory", "", nil, tenantHdr(tenant)); r.code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /inventory = %d %s; want 401", r.code, r.raw)
	}
}

// The other tools' managed files are fleet surfaces too: publishing them, their history,
// the signed pull, the check-in and the drift view answer 501 and name Business.
func TestCommunityFleetManagedSettingsOfOtherToolsUnavailable(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.tenantAdmin()
	hdr := tenantHdr(tenant)
	for _, tool := range otherToolSurfaces {
		base := "/v1/m/claude-policy/" + tool.surface
		for _, tc := range []struct {
			method, path string
			body         any
		}{
			{"POST", "/publish", map[string]any{"content": tool.good}},
			{"GET", "/versions", nil},
			{"GET", "/artifact", nil},
			{"POST", "/checkin", map[string]any{"scope": "host-1"}},
			{"GET", "/distribution", nil},
		} {
			t.Run(tool.surface+tc.path, func(t *testing.T) {
				r := h.do(tc.method, base+tc.path, tok, tc.body, hdr)
				if r.code != http.StatusNotImplemented {
					t.Fatalf("Community %s %s%s = %d %s; want 501", tc.method, tool.surface, tc.path, r.code, r.raw)
				}
				errBody, _ := r.body["error"].(map[string]any)
				if msg, _ := errBody["message"].(string); !strings.Contains(msg, "Business") {
					t.Fatalf("the refusal must name Business in error.message: %s", r.raw)
				}
			})
		}
	}
}
