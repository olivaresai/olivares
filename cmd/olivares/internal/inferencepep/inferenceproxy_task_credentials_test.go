// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferencepep

import (
	"net/http"
	"testing"
)

func TestTaskInferenceCredentialNeverFallsBackToOrdinaryAuthentication(t *testing.T) {
	a, models, budget, kill, policy := allowAll()
	d := newTestDecider(a, models, budget, kill, policy)
	// This ordinary authenticator deliberately accepts every string. A session
	// credential must still use its own issuer, which is absent here.
	if _, refusal, ok := d.resolveBearerIdentity(t.Context(), "olvsess_not-issued"); ok || refusal.decision.Status != http.StatusUnauthorized {
		t.Fatal("unwired task credential fell back to ordinary authentication")
	}
	if _, _, ok := d.resolveBearerIdentity(t.Context(), "ordinary-fixture"); !ok {
		t.Fatal("ordinary authentication changed")
	}
}
