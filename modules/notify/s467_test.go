// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// Flag B: RouteFingerprint must fold in the operator CONNECTOR config behind
// a destination NAME, so swapping/reconfiguring the connector under an unchanged
// route voids the frozen approval.
func TestS467RouteFingerprintFoldsConnectorConfig(t *testing.T) {
	disp := newFakeDispatcher("d1")
	disp.connFp = map[string]string{"d1": "conn-A"}
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	id := h.mustCreateRoute(editor, tenant, map[string]any{"name": "ops", "destination": "d1"})

	fpA, ok, err := h.mod.RouteFingerprint(context.Background(), tenant, model.ID(id))
	if err != nil || !ok {
		t.Fatalf("RouteFingerprint = %v ok=%v", err, ok)
	}

	// The operator swaps the connector behind destination d1 (kind/settings) while
	// the route row is UNCHANGED.
	disp.mu.Lock()
	disp.connFp["d1"] = "conn-EVIL"
	disp.mu.Unlock()

	fpB, ok, err := h.mod.RouteFingerprint(context.Background(), tenant, model.ID(id))
	if err != nil || !ok {
		t.Fatalf("RouteFingerprint (after swap) = %v ok=%v", err, ok)
	}
	if fpA == fpB {
		t.Fatal("a connector swap under an unchanged route did NOT change the route fingerprint (Flag B)")
	}
}
