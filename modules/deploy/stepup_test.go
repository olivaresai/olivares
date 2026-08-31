// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"net/http"
	"testing"
)

// deploy apply/retire are CRITICAL infrastructure mutations: a HUMAN
// session must carry a fresh AAL3 step-up. The standard harness operators are
// step-up-verified (harness stepUp); this test mints one that is not.

// TestApplyRequiresStepUp pins the deny: an AAL1 admin session is refused on
// apply AND retire with the machine-readable step_up_required code, before any
// phase-1/phase-2 work happens; after stepping up the same session proceeds.
func TestApplyRequiresStepUp(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	elevated := h.roleToken(root, tid, "ops@acme.io", "admin")
	defID := h.createDef(elevated, tid, "billing-agent", agentSpec("img:1", "agent:billing"))

	// A plain password session with the same admin role, NO step-up.
	r := h.do("POST", "/v1/users", root, map[string]any{"email": "aal1@acme.io", "password": "memberpass1"}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("create user = %d %s", r.code, r.raw)
	}
	if rr := h.do("POST", "/v1/memberships", root, map[string]any{"user_id": r.body["id"].(string), "tenant": tid.String(), "role": "admin"}, nil); rr.code != http.StatusCreated {
		t.Fatalf("grant = %d %s", rr.code, rr.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": "aal1@acme.io", "password": "memberpass1"}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("login = %d %s", r.code, r.raw)
	}
	low := r.body["token"].(string)

	for _, leg := range []string{"apply", "retire"} {
		rr := h.do("POST", "/v1/m/deploy/definitions/"+defID+"/"+leg, low, map[string]any{}, tenantHdr(tid))
		if rr.code != http.StatusForbidden {
			t.Fatalf("%s at AAL1 = %d %s, want 403", leg, rr.code, rr.raw)
		}
		if rr.body["error"].(map[string]any)["code"] != "step_up_required" {
			t.Fatalf("%s error code = %s, want step_up_required", leg, rr.raw)
		}
	}
	if h.exec.applyCalls != 0 {
		t.Fatalf("executor touched by an under-assured session (%d calls)", h.exec.applyCalls)
	}

	// The same session proceeds after the step-up (phase 1 requests approval).
	h.stepUp(low)
	if rr := h.applyPhase1(low, tid, defID); rr.code != http.StatusAccepted {
		t.Fatalf("apply phase1 after step-up = %d %s, want 202", rr.code, rr.raw)
	}
}
