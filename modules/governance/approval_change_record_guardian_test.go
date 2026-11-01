// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

type guardianChangeRecordFixture struct {
	*guardianFixture
	state  string
	end    time.Time
	errs   map[string]error
	onRead func()
	reads  int
}

func newGuardianChangeRecordFixture(t *testing.T, action string) *guardianChangeRecordFixture {
	t.Helper()
	return newGuardianChangeRecordFixtureForAction(t, action, gaActionStopAgent)
}

func newGuardianChangeRecordFixtureForAction(t *testing.T, action, arm string) *guardianChangeRecordFixture {
	t.Helper()
	f := &guardianChangeRecordFixture{guardianFixture: newGuardianFixture(t), state: "Scheduled"}
	f.m.UseChangeRecordReader(func(_ context.Context, _ model.TenantID, _, _, key string) (ChangeRecord, error) {
		f.reads++
		if f.onRead != nil {
			f.onRead()
		}
		if err := f.errs[key]; err != nil {
			return ChangeRecord{}, err
		}
		return ChangeRecord{State: f.state, End: f.end}, nil
	})
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(context.Background(), model.Policy{
			Name: "containment bound to a change record", Kind: "approval", Enabled: true,
			Spec: map[string]any{
				"required_approvals": 1,
				"match":              map[string]any{"action": action},
				"change_record":      map[string]any{"system": "servicenow", "key": "CHG0030001"},
			},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if arm == gaActionQuarantineNHI {
		if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
			identity, err := sc.Identities().Create(context.Background(), model.Identity{Name: "svc-change-record", Kind: "vault_entity", ExternalID: "vault:nhi:change-record", Provider: "vault"})
			if err != nil {
				return err
			}
			_, err = sc.Agents().Create(context.Background(), model.Agent{Name: "change-record-bot", Kind: "claude-code", ExternalID: "ext-agent-change-record", Status: model.StatusActive, IdentityID: identity.ID})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.createRule("approve containment", "anomaly_detected", "high", arm, gaModeApproval)
	f.fire("anomaly_detected", sdkmodel.SeverityHigh, "agent", "ext-agent-change-record", "d-change-record")
	actions := f.actions()
	if len(actions) != 1 || actions[0].String(colGAStatus) != gaStatusPending {
		t.Fatalf("containment did not open one pending action: %+v", actions)
	}
	approvalID := actions[0].String(colGAApprovalID)
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(approvalID))
		if err != nil {
			return err
		}
		rec[colStatus] = statusApproved
		rec[colApproveCount] = int64(1)
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *guardianChangeRecordFixture) approval() model.Record {
	f.t.Helper()
	return f.approvalByID(f.actions()[0].String(colGAApprovalID))
}

func (f *guardianChangeRecordFixture) approvalByID(approvalID string) model.Record {
	f.t.Helper()
	var out model.Record
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		out, err = repo.Get(context.Background(), model.ID(approvalID))
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func TestR524ReviewerGuardianContainmentAfterTheRecordIsCanceled(t *testing.T) {
	for _, binding := range []struct {
		name   string
		action string
	}{
		{name: "named action", action: "security.guardian.stop_agent"},
		{name: "catch-all policy", action: ""},
	} {
		t.Run(binding.name, func(t *testing.T) {
			f := newGuardianChangeRecordFixture(t, binding.action)
			f.state = "Canceled"

			res, err := f.m.GuardianSweep(context.Background(), f.tenant)
			if err == nil || !strings.Contains(err.Error(), "CHG0030001") || !strings.Contains(err.Error(), "Canceled") {
				t.Fatalf("sweep after the record was canceled = %+v %v, want the record's refusal", res, err)
			}
			if res.Executed != 0 || len(f.stops()) != 0 {
				t.Fatalf("containment executed under a Canceled change record: %+v, stops=%+v", res, f.stops())
			}
			approval := f.approval()
			if approval.String(colStatus) != statusCanceled || approval.String(colConsumedBy) != "" {
				t.Fatalf("approval after the refusal = %+v, want canceled and unspent", approval)
			}
			if f.reads != 1 {
				t.Fatalf("change-record reads = %d, want one at the spend", f.reads)
			}
			res, err = f.m.GuardianSweep(context.Background(), f.tenant)
			if err != nil || res.Rejected != 1 || f.actions()[0].String(colGADetail) != "approval canceled before containment" {
				t.Fatalf("sweep after the change-record cancellation = %+v %v action=%+v, want a truthful cancellation", res, err, f.actions()[0])
			}
		})
	}
}

func TestGuardianContainmentSpendsARecordBoundApproval(t *testing.T) {
	for _, binding := range []struct {
		name   string
		action string
	}{
		{name: "named action", action: "security.guardian.stop_agent"},
		{name: "catch-all policy", action: ""},
	} {
		t.Run(binding.name, func(t *testing.T) {
			f := newGuardianChangeRecordFixture(t, binding.action)
			res, err := f.m.GuardianSweep(context.Background(), f.tenant)
			if err != nil || res.Executed != 1 {
				t.Fatalf("sweep with the record Scheduled = %+v %v", res, err)
			}
			action := f.actions()[0]
			if action.String(colGAStatus) != gaStatusExecuted || action.String(colGAKillswitchID) == "" || len(f.stops()) != 1 {
				t.Fatalf("containment did not execute and bind its stop: action=%+v stops=%+v", action, f.stops())
			}
			wantConsumer := "guardian:" + action.String(model.ColID)
			if got := f.approval().String(colConsumedBy); got != wantConsumer {
				t.Fatalf("consumed_by = %q, want %q", got, wantConsumer)
			}
			if f.reads != 1 {
				t.Fatalf("change-record reads = %d, want one at the spend", f.reads)
			}
		})
	}
}

func TestGuardianContainmentRecoversAnAlreadyConsumedApproval(t *testing.T) {
	f := newGuardianChangeRecordFixture(t, "security.guardian.stop_agent")
	if res, err := f.m.GuardianSweep(context.Background(), f.tenant); err != nil || res.Executed != 1 {
		t.Fatalf("first sweep = %+v %v", res, err)
	}
	// A concurrent pass can discover the pending action before the winner commits.
	// Reproduce that discovery state with the approval already spent by the winner.
	action := f.actions()[0]
	oldApproval := action.String(colGAApprovalID)
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(guardianActionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(action.String(model.ColID)))
		if err != nil {
			return err
		}
		rec[colGAStatus] = gaStatusPending
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	res, err := f.m.GuardianSweep(context.Background(), f.tenant)
	replacement := f.actions()[0].String(colGAApprovalID)
	if err != nil || res.Failed != 0 || replacement == oldApproval || f.actions()[0].String(colGAStatus) != gaStatusPending {
		t.Fatalf("sweep with an already-consumed approval = %+v %v action=%+v, want a fresh pending approval", res, err, f.actions()[0])
	}
	if got := len(f.stops()); got != 1 {
		t.Fatalf("containment ran again: %d stops", got)
	}
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(replacement))
		if err != nil {
			return err
		}
		rec[colStatus] = statusApproved
		rec[colApproveCount] = int64(1)
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	res, err = f.m.GuardianSweep(context.Background(), f.tenant)
	if err != nil || res.Executed != 1 || f.actions()[0].String(colGAStatus) != gaStatusExecuted {
		t.Fatalf("sweep with the replacement approval = %+v %v action=%+v", res, err, f.actions()[0])
	}
}

func TestGuardianContainmentExpiresAnApprovalBeforeTheSweep(t *testing.T) {
	f := newGuardianChangeRecordFixture(t, "security.guardian.stop_agent")
	approvalID := f.actions()[0].String(colGAApprovalID)
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(approvalID))
		if err != nil {
			return err
		}
		rec[colExpiresAt] = model.NewTimestamp(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)).String()
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	res, err := f.m.GuardianSweep(context.Background(), f.tenant)
	if err != nil || res.Expired != 1 || f.actions()[0].String(colGAStatus) != gaStatusExpired {
		t.Fatalf("sweep with an expired approval = %+v %v action=%+v, want a terminal expiry", res, err, f.actions()[0])
	}
	if got := len(f.stops()); got != 0 {
		t.Fatalf("expired containment executed: %d stops", got)
	}
}

func TestGuardianReplayNotificationUsesEstateSubject(t *testing.T) {
	f := newGuardianChangeRecordFixture(t, "security.guardian.stop_estate")
	actionID := f.actions()[0].String(model.ColID)
	approvalID := f.actions()[0].String(colGAApprovalID)
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		actionRepo, err := sc.Ext(guardianActionKind)
		if err != nil {
			return err
		}
		action, err := actionRepo.Get(context.Background(), model.ID(actionID))
		if err != nil {
			return err
		}
		action[colGAAction] = gaActionStopEstate
		action[colGATargetKind] = "estate"
		action[colGATargetRef] = ""
		if _, err := actionRepo.Update(context.Background(), action); err != nil {
			return err
		}
		approvalRepo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		approval, err := approvalRepo.Get(context.Background(), model.ID(approvalID))
		if err != nil {
			return err
		}
		approval[colAction] = guardianApprovalPrefix + gaActionStopEstate
		approval[colSubjectKind] = "estate"
		approval[colSubjectRef] = "estate"
		_, err = approvalRepo.Update(context.Background(), approval)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if res, err := f.m.GuardianSweep(context.Background(), f.tenant); err != nil || res.Executed != 1 {
		t.Fatalf("first estate sweep = %+v %v", res, err)
	}
	action := f.actions()[0]
	eventsBeforeReplay := len(f.host.events)
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(guardianActionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(action.String(model.ColID)))
		if err != nil {
			return err
		}
		rec[colGAStatus] = gaStatusPending
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.GuardianSweep(context.Background(), f.tenant); err != nil {
		t.Fatal(err)
	}
	replayed := false
	for _, e := range f.host.events[eventsBeforeReplay:] {
		finding, ok := event.FindingOf(e)
		if ok && finding.Kind == findingGuardianPending {
			replayed = true
			if finding.SubjectRef != "estate" {
				t.Fatalf("estate replay subject = %q, want estate", finding.SubjectRef)
			}
		}
	}
	if !replayed {
		t.Fatal("missing guardian replay notification")
	}
}

func TestGenericConsumeCannotStarveGuardian(t *testing.T) {
	f := newGuardianChangeRecordFixture(t, "security.guardian.stop_agent")
	id := f.actions()[0].String(colGAApprovalID)
	_, err := f.m.EngineApprovals().Consume(context.Background(), f.tenant, id, "foreign", "")
	var refusal approvalDecisionError
	if !errors.As(err, &refusal) || refusal.Status != http.StatusConflict || refusal.Code != "approval_act_required" {
		t.Errorf("generic consume = %v, want coded 409", err)
	}
	if got := f.approval().String(colConsumedBy); got != "" {
		t.Errorf("generic consume spent grant: %s", got)
	}
	res, err := f.m.GuardianSweep(context.Background(), f.tenant)
	if err != nil || res.Executed != 1 || len(f.stops()) != 1 {
		t.Fatalf("approved act refused: %+v %v", res, err)
	}
}
