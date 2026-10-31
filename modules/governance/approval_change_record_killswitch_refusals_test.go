// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/governance"
)

func (f *killSwitchChangeRecordFixture) stopEngaged() bool {
	f.h.t.Helper()
	state := f.h.do("GET", govPath+"/killswitch/state", f.admin, nil, tenantHdr(f.tenant))
	return state.body["estate_stopped"] == true
}

func (f *killSwitchChangeRecordFixture) ledger(action string) []map[string]any {
	return (&changeRecordHarness{h: f.h, tenant: f.tenant}).auditMeta(action)
}

// An unreachable change system refuses this re-enable only: the stop stays engaged, the
// grant stays approved and unspent, the ledger says why, and the same grant lifts the
// stop once the system answers.
func TestKillSwitchReenableWithTheChangeSystemDownKeepsTheStopAndTheGrant(t *testing.T) {
	f := newKillSwitchChangeRecordFixture(t, "security.killswitch.reenable")
	f.system.set(governance.ChangeRecord{}, errors.New("timeout"))

	r := reenable(f.h, f.admin, f.tenant, f.stopID)
	if r.code != http.StatusServiceUnavailable || errCode(r) != "change_record_unavailable" {
		t.Fatalf("re-enable with the system down = %d %s, want 503 change_record_unavailable", r.code, r.raw)
	}
	if !f.stopEngaged() {
		t.Fatal("the stop was lifted while the change system could not be read")
	}
	if approval := f.approval(); approval.body["status"] != "approved" || approval.body["consumed"] == true {
		t.Fatalf("approval after the outage = %s, want approved and unspent", approval.raw)
	}
	if got := f.consumedBy(); got != "" {
		t.Fatalf("approval spent during the outage: %q", got)
	}
	var refused bool
	for _, meta := range f.ledger("governance.approval.change_record_refused") {
		refused = refused || meta["code"] == "change_record_unavailable"
	}
	if !refused {
		t.Fatal("the refusal is not in the ledger")
	}

	f.system.set(scheduled(), nil)
	if r := reenable(f.h, f.admin, f.tenant, f.stopID); r.code != http.StatusOK || r.body["status"] != "reenabled" {
		t.Fatalf("re-enable after the system recovered = %d %s, want the same grant to work", r.code, r.raw)
	}
}

// A record whose window has not opened refuses without revoking; once the window has
// closed the grant is revoked and the stop stays engaged.
func TestKillSwitchReenableFollowsTheChangeWindow(t *testing.T) {
	f := newKillSwitchChangeRecordFixture(t, "security.killswitch.reenable")
	f.system.set(governance.ChangeRecord{State: "Scheduled", Start: baseTime.Add(time.Hour), End: baseTime.Add(2 * time.Hour)}, nil)

	r := reenable(f.h, f.admin, f.tenant, f.stopID)
	if r.code != http.StatusConflict || !strings.Contains(errMessage(r), "has not opened") {
		t.Fatalf("re-enable before the window = %d %s, want 409 has not opened", r.code, r.raw)
	}
	if !f.stopEngaged() || f.approval().body["status"] != "approved" || f.consumedBy() != "" {
		t.Fatal("a window that has not opened must leave the stop engaged and the grant approved and unspent")
	}

	f.h.clk.advance(3 * time.Hour)
	r = reenable(f.h, f.admin, f.tenant, f.stopID)
	if r.code != http.StatusConflict || errCode(r) != "change_record_not_approved" {
		t.Fatalf("re-enable after the window = %d %s, want 409 change_record_not_approved", r.code, r.raw)
	}
	if !f.stopEngaged() {
		t.Fatal("the stop was lifted after the change window closed")
	}
	if got := f.approval().body["status"]; got != "canceled" {
		t.Fatalf("approval after the window closed = %v, want canceled", got)
	}
}
