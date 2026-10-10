// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"
)

func assertActivePdpRevision(t *testing.T, h *harness, token string, headers map[string]string, want int64) {
	t.Helper()
	versions := h.do("GET", "/v1/m/governance/pdp/versions", token, nil, headers)
	if versions.code != http.StatusOK {
		t.Fatalf("list versions: %d %s", versions.code, versions.raw)
	}
	active := int64(0)
	activeCount := 0
	for _, raw := range items(versions) {
		version, _ := raw.(map[string]any)
		if version["surface"] == "cedar" && version["active"] == true {
			active = int64(version["revision"].(float64))
			activeCount++
		}
	}
	if activeCount != 1 || active != want {
		t.Fatalf("active cedar revision = %d (count %d), want %d: %s", active, activeCount, want, versions.raw)
	}
}
