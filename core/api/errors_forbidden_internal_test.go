// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// forbiddenFor changes the sentence only: it stays errForbidden, 403 "forbidden",
// and names the lowest built-in role that includes the permission, or none.
func TestForbiddenForKeepsTheCodeAndNamesTheLowestRole(t *testing.T) {
	for _, tc := range []struct {
		perm       auth.Permission
		serverWide bool
		want       string
	}{
		{"agent:read", false, `This needs the "agent:read" permission in this organization (built-in roles: viewer and above).`},
		{"agent:write", false, `This needs the "agent:write" permission in this organization (built-in roles: editor and above).`},
		{"user:read", false, `This needs the "user:read" permission in this organization (built-in roles: admin and above).`},
		{"agent:admin", false, `This needs the "agent:admin" permission in this organization (built-in role: owner).`},
		{"system:admin", false, `This needs the "system:admin" permission in this organization.`},
		{"user:read", true, `This needs the "user:read" permission across the whole server, which only a superadmin holds.`},
		{"", false, "forbidden"},
	} {
		err := forbiddenFor(tc.perm, tc.serverWide)
		if got := err.Error(); got != tc.want {
			t.Errorf("forbiddenFor(%q, %v) = %q, want %q", tc.perm, tc.serverWide, got, tc.want)
		}
		if status, code := statusFor(err); !errors.Is(err, errForbidden) || status != http.StatusForbidden || code != "forbidden" {
			t.Errorf("forbiddenFor(%q) maps to %d %q, want 403 forbidden through errForbidden", tc.perm, status, code)
		}
	}
}
