// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"github.com/olivaresai/olivares/core/auth"
	"testing"
)

func TestWhoamiReportsSessionLifetime(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	r := h.do("GET", "/v1/auth/whoami", admin, nil, nil)
	if r.code != 200 || r.body["session_ttl_seconds"] != auth.DefaultSessionTTL.Seconds() {
		t.Fatalf("session lifetime = %v, status %d", r.body["session_ttl_seconds"], r.code)
	}
}
