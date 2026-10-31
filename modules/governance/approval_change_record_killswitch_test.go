// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

type killSwitchChangeRecordFixture struct {
	h          *harness
	system     *fakeChangeSystem
	tenant     model.TenantID
	admin      string
	stopID     string
	approvalID string
}

func newKillSwitchChangeRecordFixture(t *testing.T, action string) *killSwitchChangeRecordFixture {
	t.Helper()
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "change-record-reenable")
	_, first := h.roleUser(admin, tenant, "first@x.io", "admin")
	_, second := h.roleUser(admin, tenant, "second@x.io", "admin")
	fake := &fakeChangeSystem{}
	h.gov.UseChangeRecordReader(fake.read)
	storePolicy(t, h, tenant, "re-enable bound to a change record", map[string]any{
		"required_approvals": 2,
		"match":              map[string]any{"action": action},
		"change_record":      map[string]any{"system": "servicenow", "key": "CHG0030001"},
	})
	fake.set(scheduled(), nil)

	r := engage(h, admin, tenant, "estate", "", "incident")
	if r.code != http.StatusCreated {
		t.Fatalf("engage = %d %s", r.code, r.raw)
	}
	stopID := r.body["id"].(string)
	r = reenable(h, admin, tenant, stopID)
	if r.code != http.StatusAccepted {
		t.Fatalf("open re-enable approval = %d %s", r.code, r.raw)
	}
	approvalID := r.body["approval"].(map[string]any)["id"].(string)
	for name, token := range map[string]string{"first": first, "second": second} {
		if d := decide(h, token, tenant, approvalID, "approve"); d.code != http.StatusOK {
			t.Fatalf("%s approval = %d %s", name, d.code, d.raw)
		}
	}
	return &killSwitchChangeRecordFixture{h: h, system: fake, tenant: tenant, admin: admin, stopID: stopID, approvalID: approvalID}
}

func (f *killSwitchChangeRecordFixture) approval() resp {
	f.h.t.Helper()
	return f.h.do("GET", govPath+"/approvals/"+f.approvalID, f.admin, nil, tenantHdr(f.tenant))
}

func (f *killSwitchChangeRecordFixture) consumedBy() string {
	f.h.t.Helper()
	var consumed string
	if err := f.h.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("governance.approval")
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(f.approvalID))
		if err != nil {
			return err
		}
		consumed = rec.String("consumed_by")
		return nil
	}); err != nil {
		f.h.t.Fatal(err)
	}
	return consumed
}

func TestR524ReviewerKillSwitchReenableAfterTheRecordIsCanceled(t *testing.T) {
	for _, binding := range []struct {
		name   string
		action string
	}{
		{name: "named action", action: "security.killswitch.reenable"},
		{name: "catch-all policy", action: ""},
	} {
		t.Run(binding.name, func(t *testing.T) {
			f := newKillSwitchChangeRecordFixture(t, binding.action)
			f.system.set(governance.ChangeRecord{State: "Canceled"}, nil)

			r := reenable(f.h, f.admin, f.tenant, f.stopID)
			if r.code != http.StatusConflict || errCode(r) != "change_record_not_approved" ||
				!strings.Contains(r.raw, "CHG0030001") || !strings.Contains(r.raw, "Canceled") {
				t.Fatalf("re-enable after the record was canceled = %d %s, want the record's refusal", r.code, r.raw)
			}
			state := f.h.do("GET", govPath+"/killswitch/state", f.admin, nil, tenantHdr(f.tenant))
			if state.body["estate_stopped"] != true {
				t.Fatalf("the stop was lifted under an approval whose change record is Canceled: %s", state.raw)
			}
			approval := f.approval()
			if approval.body["status"] != "canceled" || approval.body["consumed"] == true {
				t.Fatalf("approval after the refusal = %s, want canceled and unspent", approval.raw)
			}
			if got := f.system.callCount(); got != 3 {
				t.Fatalf("change-record reads = %d, want one per decision and one at the spend", got)
			}
		})
	}
}

func TestKillSwitchReenableSpendsARecordBoundApproval(t *testing.T) {
	for _, binding := range []struct {
		name   string
		action string
	}{
		{name: "named action", action: "security.killswitch.reenable"},
		{name: "catch-all policy", action: ""},
	} {
		t.Run(binding.name, func(t *testing.T) {
			f := newKillSwitchChangeRecordFixture(t, binding.action)
			r := reenable(f.h, f.admin, f.tenant, f.stopID)
			if r.code != http.StatusOK || r.body["status"] != "reenabled" {
				t.Fatalf("re-enable with the record Scheduled = %d %s", r.code, r.raw)
			}
			if approval := f.approval(); approval.body["consumed"] != true {
				t.Fatalf("approval after the re-enable = %s, want spent", approval.raw)
			}
			wantConsumer := "killswitch.reenable:" + f.stopID
			if got := f.consumedBy(); got != wantConsumer {
				t.Fatalf("consumed_by = %q, want %q", got, wantConsumer)
			}
			if got := f.system.callCount(); got != 3 {
				t.Fatalf("change-record reads = %d, want one per decision and one at the spend", got)
			}
		})
	}
}

func TestKillSwitchReenableRecoversAnAlreadyConsumedApproval(t *testing.T) {
	f := newKillSwitchChangeRecordFixture(t, "security.killswitch.reenable")
	if r := reenable(f.h, f.admin, f.tenant, f.stopID); r.code != http.StatusOK {
		t.Fatalf("first re-enable = %d %s", r.code, r.raw)
	}
	// A concurrent request can discover the active stop before the winner commits.
	// Reproduce that discovery state with the approval already spent by the winner.
	if err := f.h.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("governance.killswitch")
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(f.stopID))
		if err != nil {
			return err
		}
		rec["status"] = "active"
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if r := reenable(f.h, f.admin, f.tenant, f.stopID); r.code != http.StatusConflict {
		t.Fatalf("re-enable with an already-consumed approval = %d %s, want 409, not a missing result", r.code, r.raw)
	}
	r := reenable(f.h, f.admin, f.tenant, f.stopID)
	freshID, _ := r.body["approval"].(map[string]any)["id"].(string)
	if r.code != http.StatusAccepted || freshID == "" || freshID == f.approvalID {
		t.Fatalf("re-enable after releasing the spent approval = %d %s, want a fresh quorum", r.code, r.raw)
	}
}

func TestKillSwitchReenableRecoversAnExpiredApproval(t *testing.T) {
	f := newKillSwitchChangeRecordFixture(t, "security.killswitch.reenable")
	if err := f.h.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("governance.approval")
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(f.approvalID))
		if err != nil {
			return err
		}
		rec["expires_at"] = model.NewTimestamp(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)).String()
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if r := reenable(f.h, f.admin, f.tenant, f.stopID); r.code != http.StatusConflict {
		t.Fatalf("re-enable with an expired approval = %d %s, want 409", r.code, r.raw)
	}
	r := reenable(f.h, f.admin, f.tenant, f.stopID)
	freshID, _ := r.body["approval"].(map[string]any)["id"].(string)
	if r.code != http.StatusAccepted || freshID == "" || freshID == f.approvalID {
		t.Fatalf("re-enable after releasing the expired approval = %d %s, want a fresh quorum", r.code, r.raw)
	}
	metas := (&changeRecordHarness{h: f.h, tenant: f.tenant}).auditMeta("governance.killswitch.reenable.approval.detach")
	if len(metas) != 1 || metas[0]["approval"] != f.approvalID || metas[0]["reason"] == "" {
		t.Fatalf("detachment audit = %v, want the old approval ID and reason", metas)
	}
}

func TestKillSwitchReenableRefusesSubstitutedApproval(t *testing.T) {
	f := newKillSwitchChangeRecordFixture(t, "security.killswitch.reenable")
	if err := f.h.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("governance.approval")
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(f.approvalID))
		if err != nil {
			return err
		}
		rec["action"] = "other.action"
		rec["subject_kind"] = "other"
		rec["subject_ref"] = "other"
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if r := reenable(f.h, f.admin, f.tenant, f.stopID); r.code != http.StatusConflict || !strings.Contains(r.raw, "does not match this stop") {
		t.Fatalf("re-enable with a substituted approval = %d %s, want 409 does not match this stop", r.code, r.raw)
	}
	state := f.h.do("GET", govPath+"/killswitch/state", f.admin, nil, tenantHdr(f.tenant))
	if state.body["estate_stopped"] != true {
		t.Fatalf("the stop was lifted by an unrelated approval: %s", state.raw)
	}
	if got := f.consumedBy(); got != "" {
		t.Fatalf("substituted approval was spent: %q", got)
	}
}

func TestGenericConsumeCannotStarveReenable(t *testing.T) {
	f := newKillSwitchChangeRecordFixture(t, "security.killswitch.reenable")
	_, editor := f.h.roleUser(f.admin, f.tenant, "consumer@x.io", "editor")
	r := f.h.do("POST", govPath+"/approvals/"+f.approvalID+"/consume", editor, map[string]any{"consumer_id": "foreign"}, tenantHdr(f.tenant))
	if r.code != http.StatusConflict || !strings.Contains(string(r.raw), "approval_act_required") {
		t.Errorf("generic consume = %d %s, want coded 409", r.code, r.raw)
	}
	if got := f.consumedBy(); got != "" {
		t.Errorf("generic consume spent grant: %s", got)
	}
	if r := reenable(f.h, f.admin, f.tenant, f.stopID); r.code != http.StatusOK {
		t.Fatalf("approved act refused: %d %s", r.code, r.raw)
	}
}
