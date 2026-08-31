// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// posture_persons_test.go reproduces, through the REAL HTTP stack, the self-signature
// the #580 contrast measured on this gate, and pins the three-valued rule that closes
// it. Three lanes fixed the same defect in core/api/dr_handler.go and none of them
// touched this file — that is the class, stated as a fact about the estate.

// refusalMessage reads the module error envelope ({"error":{"message":…}}, dto.go:40).
// Asserting on the MESSAGE and not only the status is what separates "refused for the
// person rule" from "refused because the fixture broke somewhere else".
func refusalMessage(t *testing.T, r resp) string {
	t.Helper()
	env, ok := r.body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error envelope in %s", r.raw)
	}
	msg, _ := env["message"].(string)
	return msg
}

// pendingRelaxation drives a relaxing delete to the pending state and returns the
// posture-request id. It is the shortest path to a two-party decision on this module.
func pendingRelaxation(t *testing.T, h *harness, proposer string, tenant model.TenantID) string {
	t.Helper()
	c := h.createBinding(proposer, tenant, map[string]any{
		"source_type": "model", "source_ref": "m-persons", "scope_tree": "user",
		"scope_ref": "user-x", "effect": "forbid", "enabled": true,
	})
	if c.code != http.StatusCreated {
		t.Fatalf("create forbid = %d %s", c.code, c.raw)
	}
	del := h.do("DELETE", "/v1/m/sourcescope/bindings/"+c.body["id"].(string), proposer, nil, tenantHdr(tenant))
	if del.code != http.StatusAccepted || del.body["status"] != "pending" {
		t.Fatalf("relaxing delete must be 202 pending, got %d %s", del.code, del.raw)
	}
	return del.body["id"].(string)
}

// TestPosture_OneHumanCannotApproveTheirOwnRelaxationWithTheirOwnToken is the
// reproduction. The proposer is a SESSION; the approver is an API TOKEN THAT SAME
// HUMAN minted for themselves. Principal.Actor() renders "user:<UserID>" for the
// first and "token:<CredID>" for the second, so the pre comparison saw two
// different strings and applied the relaxation for one person.
func TestPosture_OneAccountCannotApproveItsOwnRelaxationWithItsOwnToken(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// The SAME human's second credential. Issued by the admin session, so
	// model.APIToken.UserID is that admin's user id (core/auth/accounts.go stamps
	// UserID: actor.UserID on every issuance path).
	tr := h.do("POST", "/v1/tokens", admin, map[string]any{"name": "my-own-ci", "tenant": tenant.String(), "role": auth.RoleAdmin}, nil)
	if tr.code != http.StatusCreated {
		t.Fatalf("issue token = %d %s", tr.code, tr.raw)
	}
	ownToken, _ := tr.body["token"].(string)
	if ownToken == "" {
		t.Fatalf("no token in %s", tr.raw)
	}

	id := pendingRelaxation(t, h, admin, tenant)

	// The fixture is only meaningful if the two credentials really do render
	// different actor strings — otherwise the old code would have refused anyway and
	// this test would pass against the defect it exists to catch.
	got := h.do("GET", "/v1/m/sourcescope/posture-requests/"+id, admin, nil, tenantHdr(tenant))
	if got.code != http.StatusOK {
		t.Fatalf("get posture request = %d %s", got.code, got.raw)
	}
	proposerActor, _ := got.body["proposer"].(string)
	if !strings.HasPrefix(proposerActor, "user:") {
		t.Fatalf("fixture is inert: proposer actor %q is not a session actor", proposerActor)
	}

	ap := h.do("POST", "/v1/m/sourcescope/posture-requests/"+id+"/approve", ownToken, nil, tenantHdr(tenant))
	if ap.code != http.StatusConflict {
		t.Fatalf("self-approval with one's OWN token must be refused, got %d %s", ap.code, ap.raw)
	}
	if msg := refusalMessage(t, ap); !strings.Contains(msg, "different person") {
		t.Fatalf("refusal must name the PERSON rule, got %q", msg)
	}

	// Deny-closed means the request survives: a genuine second person can still decide it.
	after := h.do("GET", "/v1/m/sourcescope/posture-requests/"+id, admin, nil, tenantHdr(tenant))
	if after.body["status"] != "pending" {
		t.Fatalf("a refused approval must leave the request pending, got %s", after.raw)
	}
}

// TestPosture_TwoDistinctPeopleStillApprove is the other direction, and it is not
// decoration: a fix that closes the self-signature by refusing everything would pass
// the test above and break the product. The gate must still let two accounts through.
func TestPosture_TwoDistinctAccountsStillApprove(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	approver := h.tokenFor(admin, tenant, "second-person@acme.io", auth.RoleAdmin)

	id := pendingRelaxation(t, h, admin, tenant)

	ap := h.do("POST", "/v1/m/sourcescope/posture-requests/"+id+"/approve", approver, nil, tenantHdr(tenant))
	if ap.code != http.StatusOK {
		t.Fatalf("two distinct accounts must still approve, got %d %s", ap.code, ap.raw)
	}
	after := h.do("GET", "/v1/m/sourcescope/posture-requests/"+id, admin, nil, tenantHdr(tenant))
	if after.body["status"] != "approved" {
		t.Fatalf("status = %s", after.raw)
	}
	// The trail must name the PERSON as well as the credential — a row that stores
	// only the actor cannot be compared by person at all, which is why the comparison
	// could not be made before.
	if by, _ := after.body["decided_by"].(string); !strings.HasPrefix(by, "user:") {
		t.Fatalf("decided_by = %q, want the approver's actor", by)
	}
}

// TestPosture_TheSameHumanUnderTwoTokensIsStillOnePerson closes the direction a
// boolean could express but nobody checked: BOTH parties are tokens of the same human,
// so both actor strings differ and both resolve to one user.
func TestPosture_TheSameAccountUnderTwoTokensIsStillOneParty(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	mint := func(name string) string {
		t.Helper()
		r := h.do("POST", "/v1/tokens", admin, map[string]any{"name": name, "tenant": tenant.String(), "role": auth.RoleAdmin}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("issue %s = %d %s", name, r.code, r.raw)
		}
		return r.body["token"].(string)
	}
	first, second := mint("ci-a"), mint("ci-b")
	if first == second {
		t.Fatal("fixture is inert: the two tokens must be different credentials")
	}

	id := pendingRelaxation(t, h, first, tenant)
	ap := h.do("POST", "/v1/m/sourcescope/posture-requests/"+id+"/approve", second, nil, tenantHdr(tenant))
	if ap.code != http.StatusConflict {
		t.Fatalf("two tokens of ONE human must not satisfy two-person, got %d %s", ap.code, ap.raw)
	}
	if msg := refusalMessage(t, ap); !strings.Contains(msg, "different person") {
		t.Fatalf("refusal must name the PERSON rule, got %q", msg)
	}
}
