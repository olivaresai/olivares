// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// posture_legacy_test.go pins the direction the external contrast on PR #615 found open:
// a fix that closes a bypass must not strand the requests that were already in flight
// when it shipped. An arrangement that blocks whoever SHOULD pass is as much a defect as
// one that admits whoever should not — and before this one closed both doors at
// once, because the identical two-person check guarded approve AND reject and the only
// two decision routes are those two (modules/sourcescope/api.go).
//
// The row these tests seed is the row the schema actually admits: proposer_user is
// Nullable (modules/sourcescope/schema.go), so every request written before that column
// existed arrives with it empty. PersonRefFromActor can recover a person from a
// "user:<id>" actor because Actor() literally contains the id; it cannot from a
// "token:<id>" one — and that token is NOT necessarily a system token. A token minted by
// a person carries their UserID (core/auth/accounts.go), but the previous
// createPostureRequest persisted only Actor(), so the person was never written down.

// legacyPendingRequest writes a posture request in the PRE shape — an actor string
// and no person — straight through the store, because no supported API path can mint one
// any more. That is the point: this is an upgrade artifact, not a state a client can
// create, so nothing but a store-level fixture can reproduce it.
func legacyPendingRequest(t *testing.T, h *harness, tenant model.TenantID, proposerActor string) string {
	t.Helper()
	var id string
	err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("sourcescope.posture_request"))
		if err != nil {
			return err
		}
		rec, err := repo.Create(context.Background(), model.Record{
			"source_type": "model", "source_ref": "m-legacy",
			"op": "disable_scoping", "target_id": "", "proposed": "",
			"reason": "written before proposer_user existed",
			// The legacy shape, exactly: the actor string, and NO person column.
			"proposer": proposerActor,
			"status":   "pending", "decided_by": "", "decided_by_user": "",
		})
		if err != nil {
			return err
		}
		id = rec.String(model.ColID)
		return nil
	})
	if err != nil {
		t.Fatalf("seed legacy posture request: %v", err)
	}
	if id == "" {
		t.Fatal("seeded row has no id")
	}
	return id
}

// TestPosture_LegacyTokenProposedRowCanStillBeClosed is the availability half.
//
// A request proposed with a person's API token before the person column existed cannot be
// APPROVED — the engine genuinely cannot name the proposer, so it cannot promise two
// people, and inventing an answer would be the original defect from the other end. But it
// must still be CLOSABLE. Refusing a relaxation is the safe direction: an unattributable
// party may stop an action, it may never authorize one. That rule was already written for
// the governance quorum (modules/governance/approvals.go); this is the same rule applied
// to the second decision route, so the queue can be drained and the change re-proposed
// under the schema that records the person.
func TestPosture_LegacyTokenProposedRowCanStillBeClosed(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	second := h.tokenFor(admin, tenant, "second-person@acme.io", auth.RoleAdmin)

	id := legacyPendingRequest(t, h, tenant, "token:legacy-cred-1")

	// APPROVE stays refused: no person stands behind the proposer, so "two people"
	// cannot be confirmed. Deny-closed is correct here and must not regress.
	ap := h.do("POST", "/v1/m/sourcescope/posture-requests/"+id+"/approve", second, nil, tenantHdr(tenant))
	if ap.code != http.StatusConflict {
		t.Fatalf("approving an unattributable legacy row must stay refused, got %d %s", ap.code, ap.raw)
	}
	// And the refusal must tell the operator the way out, or the 409 is just a wall.
	if msg := refusalMessage(t, ap); !strings.Contains(msg, "reject") {
		t.Fatalf("the refusal must name the available exit, got %q", msg)
	}

	// REJECT is the exit. Before this returned 409 too and the row stayed pending
	// forever, with no cancellation or administrative-close route anywhere in the module.
	rj := h.do("POST", "/v1/m/sourcescope/posture-requests/"+id+"/reject", second, nil, tenantHdr(tenant))
	if rj.code != http.StatusOK {
		t.Fatalf("a legacy row must be closable by rejection, got %d %s", rj.code, rj.raw)
	}
	after := h.do("GET", "/v1/m/sourcescope/posture-requests/"+id, admin, nil, tenantHdr(tenant))
	if after.body["status"] != "rejected" {
		t.Fatalf("status = %s, want rejected", after.raw)
	}
}

// TestPosture_ProposerCanWithdrawTheirOwnRequest closes the other stranding, which the
// contrast reached through the same door: the two-person check also guarded reject, so a
// proposer could not even withdraw their own pending proposal. Withdrawing a request to
// relax enforcement grants nothing; refusing it left rows pending with no owner able to
// clear them.
func TestPosture_ProposerCanWithdrawTheirOwnRequest(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	id := pendingRelaxation(t, h, admin, tenant)

	// Same person on both sides: approving is still refused as self-approval.
	ap := h.do("POST", "/v1/m/sourcescope/posture-requests/"+id+"/approve", admin, nil, tenantHdr(tenant))
	if ap.code != http.StatusConflict {
		t.Fatalf("self-approval must stay refused, got %d %s", ap.code, ap.raw)
	}
	// Withdrawing it is not an authorization and must be allowed.
	rj := h.do("POST", "/v1/m/sourcescope/posture-requests/"+id+"/reject", admin, nil, tenantHdr(tenant))
	if rj.code != http.StatusOK {
		t.Fatalf("the proposer must be able to withdraw their own request, got %d %s", rj.code, rj.raw)
	}
	after := h.do("GET", "/v1/m/sourcescope/posture-requests/"+id, admin, nil, tenantHdr(tenant))
	if after.body["status"] != "rejected" {
		t.Fatalf("status = %s, want rejected", after.raw)
	}
	// The trail must still name who closed it — an ungated reject is not an anonymous one.
	if by, _ := after.body["decided_by"].(string); !strings.HasPrefix(by, "user:") {
		t.Fatalf("decided_by = %q, want the withdrawing principal's actor", by)
	}
}

// TestPosture_LegacySessionProposedRowStillApprovesForASecondPerson is the direction that
// must NOT be lost while fixing the one above: a legacy row whose actor IS recoverable
// ("user:<id>") still gets a real two-person decision, and still refuses its own proposer.
func TestPosture_LegacySessionProposedRowStillApprovesForASecondPerson(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	second := h.tokenFor(admin, tenant, "second-person@acme.io", auth.RoleAdmin)

	// Take the admin's real session actor from a request the API wrote, so the fixture
	// carries the actor string the engine actually renders rather than one invented here.
	live := h.do("GET", "/v1/m/sourcescope/posture-requests/"+pendingRelaxation(t, h, admin, tenant), admin, nil, tenantHdr(tenant))
	adminActor, _ := live.body["proposer"].(string)
	if !strings.HasPrefix(adminActor, "user:") {
		t.Fatalf("fixture is inert: proposer actor %q is not a session actor", adminActor)
	}

	// The same actor with NO person column — exactly what a row written before that
	// column looks like for a session proposer.
	id := legacyPendingRequest(t, h, tenant, adminActor)

	// Its own proposer is still refused, recovered from the actor string alone.
	if ap := h.do("POST", "/v1/m/sourcescope/posture-requests/"+id+"/approve", admin, nil, tenantHdr(tenant)); ap.code != http.StatusConflict {
		t.Fatalf("the recovered proposer must not approve their own legacy row, got %d %s", ap.code, ap.raw)
	}
	// A genuinely distinct account still can.
	ap := h.do("POST", "/v1/m/sourcescope/posture-requests/"+id+"/approve", second, nil, tenantHdr(tenant))
	if ap.code != http.StatusOK {
		t.Fatalf("a second account must still approve a recoverable legacy row, got %d %s", ap.code, ap.raw)
	}
}
