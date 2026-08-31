// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// approvals_quorum_persons_test.go is the regression the root approval counter never had.
//
// This counter is the one every other quorum in the estate is downstream of, and the
// external contrast on PR #615 measured exactly how invisible its defect was: reverting it
// to a row count leaves the ENTIRE governance suite green — 327 `=== RUN`, rc 0. The
// original defect therefore had no test either, which is why it survived so long.
//
// The state that breaks it cannot be created through the API, and that is not an
// accident: this binary does not mint a credential without a person, so no supported
// request can produce an approval decision whose decider_user is empty. But the schema
// admits one — decider_user is plain text with no non-empty requirement, and the unique
// index (tenant_id, approval_id, decider_user) happily accepts ONE row holding the empty
// string. Anything that writes decisions outside this handler — a migration, an import, a
// restore, a future writer — can produce it. So the fixture seeds it through the store,
// which is the only honest way to test a state the API refuses to build.

// seedUnattributableApproval writes an `approve` decision row carrying a credential actor
// and NO person, directly through the store. It returns nothing: its whole purpose is the
// row it leaves behind for the handler to count.
func seedUnattributableApproval(t *testing.T, h *harness, tenant model.TenantID, approvalID, actor string) {
	t.Helper()
	err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.approval_decision"))
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"approval_id": approvalID,
			"decision":    "approve",
			"decider":     actor,
			// The whole fixture: a decision with no person behind it.
			"decider_user": "",
			"note":         "seeded: a decision no person stands behind",
			"decided_at":   h.clk.Now().String(),
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed unattributable decision: %v", err)
	}
}

// TestApprovalQuorumCountsPeopleNotRows is the test that dies when the counter counts rows.
//
// Both directions are pinned in one place on purpose. A counter that simply refused
// everything would satisfy the first half and destroy the product; a counter that counted
// rows would satisfy the second half and open the gate. Only counting PEOPLE satisfies
// both, so the pair is what makes either assertion mean anything.
func TestApprovalQuorumCountsAccountsNotRows(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")
	_, a3 := h.roleUser(admin, tenant, "a3@x.io", "admin")

	r := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "required_approvals": 2})
	id := r.body["id"].(string)

	// A decision no person stands behind. Under the row-counting counter this is one
	// approver, and one real human is enough to cross a threshold of two.
	seedUnattributableApproval(t, h, tenant, id, "token:no-person-behind-me")

	// DIRECTION 1 — an unattributable decision must not be a second approver.
	rr := h.decide(a2, tenant, id, "approve")
	if rr.code != http.StatusOK {
		t.Fatalf("a real human's decision must be accepted: %d %s", rr.code, rr.raw)
	}
	if rr.body["status"] != "pending" {
		t.Fatalf("one human plus one unattributable decision crossed a threshold of TWO: status=%v %s", rr.body["status"], rr.raw)
	}
	if got, ok := rr.body["approve_count"].(float64); !ok || got != 1 {
		t.Fatalf("approve_count = %v, want 1 — the unattributable row was counted as an approver (%s)", rr.body["approve_count"], rr.raw)
	}

	// DIRECTION 2 — two real people still approve. Without this the test above is
	// satisfied by a counter that never approves anything, which is not the fix.
	rr = h.decide(a3, tenant, id, "approve")
	if rr.code != http.StatusOK || rr.body["status"] != "approved" {
		t.Fatalf("two distinct people must still cross the threshold: %d status=%v %s", rr.code, rr.body["status"], rr.raw)
	}
	if got, ok := rr.body["approve_count"].(float64); !ok || got != 2 {
		t.Fatalf("approve_count = %v, want 2", rr.body["approve_count"])
	}
}

// TestApprovalQuorumOneHumansTwoCredentialsIsOneApprover is the class defect stated at the
// counter rather than at a comparison: the same human, twice, through two credentials.
//
// It reaches the counter the way the estate actually does — the person is the key, so a
// second decision by the SAME person is refused as a duplicate decider and the count never
// moves. The seeded row makes the credential strings genuinely different, so a counter
// keyed on `decider` would see two.
func TestApprovalQuorumOneAccountsTwoCredentialsIsOneApprover(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	uid, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")

	r := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "required_approvals": 2})
	id := r.body["id"].(string)

	if rr := h.decide(a2, tenant, id, "approve"); rr.code != http.StatusOK || rr.body["status"] != "pending" {
		t.Fatalf("first approval: %d status=%v %s", rr.code, rr.body["status"], rr.raw)
	}

	// The same human's OTHER credential: a different actor string, the same person. This
	// is the shape the whole primitive exists for, written straight into the decision
	// table so no handler-level duplicate check can absorb it first.
	err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, rerr := sc.Ext(model.Kind("governance.approval_decision"))
		if rerr != nil {
			return rerr
		}
		_, rerr = repo.Create(context.Background(), model.Record{
			"approval_id": id, "decision": "approve",
			"decider":      "token:a-token-a2-minted",
			"decider_user": uid, // the SAME person as the decision above
			"note":         "seeded: the same human's second credential",
			"decided_at":   h.clk.Now().String(),
		})
		return rerr
	})
	// The write MUST be refused, and by the person key rather than by the credential: the
	// unique index is (tenant_id, approval_id, decider_user), so one human's second
	// credential collides with their first decision even though the two `decider` strings
	// differ. This is the same rule as the counter's, stated one layer down — measured
	// here rather than assumed, because "the index will catch it" is exactly the kind of
	// claim that turns out to be keyed on the wrong column.
	if err == nil {
		t.Fatal("one person's SECOND credential was accepted as a second decision row — the unique index is not keyed on the person")
	}
	if !strings.Contains(err.Error(), "decider_user") {
		t.Fatalf("the write was refused, but not by the person key: %v", err)
	}

	// And the count is unmoved by the attempt: still one person, still pending.
	got := h.do("GET", govPath+"/approvals/"+id, admin, nil, tenantHdr(tenant))
	if got.code != http.StatusOK {
		t.Fatalf("get approval = %d %s", got.code, got.raw)
	}
	if got.body["status"] != "pending" || got.body["approve_count"].(float64) != 1 {
		t.Fatalf("one human's two credentials moved the quorum: status=%v approve_count=%v %s",
			got.body["status"], got.body["approve_count"], got.raw)
	}
	// A genuinely distinct person still crosses it — the legitimate direction.
	if rr := h.decide(admin, tenant, id, "approve"); rr.code != http.StatusOK || rr.body["status"] != "approved" {
		t.Fatalf("a distinct admin must still cross the threshold: %d status=%v %s", rr.code, rr.body["status"], rr.raw)
	}
}

// TestApprovalSeparationOfDutyHoldsForACredentialWithNoPerson is the regression the
// external contrast caught in OWN change, and it is the same class of defect the
// whole branch is about: a caller that reads one value of the verdict and lets the rest
// fall through.
//
// Splitting PersonSameCredential out of PersonSame was right at the primitive —
// TwoDistinctPeople still refuses it under every policy — but this caller did not ask
// TwoDistinctPeople whether to refuse. It asked for `!ok && verdict == PersonSame`, so the
// brand-new fourth value walked straight past a guard that used to stop it.
//
// The row that reaches it is a request whose person was never recorded (requested_by_user
// empty, as an imported or pre-column row arrives) but whose actor string is the decider's
// own. Under AcceptWhenUndetermined, `!ok` is true for exactly the two verdicts that mean
// "knowably one party", so `!ok` alone is the whole rule and the extra clause was the bug.
func TestApprovalSeparationOfDutyHoldsForACredentialWithNoPerson(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	uid, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")

	r := h.createApproval(a2, tenant, map[string]any{"action": "deploy"})
	id := r.body["id"].(string)

	// Age the row: the person column empty, the actor string still the requester's own.
	err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, rerr := sc.Ext(model.Kind("governance.approval"))
		if rerr != nil {
			return rerr
		}
		rec, rerr := repo.Get(context.Background(), model.ID(id))
		if rerr != nil {
			return rerr
		}
		rec["requested_by_user"] = ""
		rec["requested_by"] = "user:" + uid
		_, rerr = repo.Update(context.Background(), rec)
		return rerr
	})
	if err != nil {
		t.Fatalf("age the approval row: %v", err)
	}

	// The same principal whose actor opened it must still be refused.
	if rr := h.decide(a2, tenant, id, "approve"); rr.code != http.StatusForbidden {
		t.Fatalf("the requester decided their own request through a credential the engine could not attribute: %d %s", rr.code, rr.raw)
	}
	// And a genuinely different person is still not blocked by the same guard.
	if rr := h.decide(admin, tenant, id, "approve"); rr.code != http.StatusOK {
		t.Fatalf("a distinct person must still decide it: %d %s", rr.code, rr.raw)
	}
}
