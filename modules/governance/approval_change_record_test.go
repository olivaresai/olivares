// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

const changeRecordAction = "gitpublish.push"

// fakeChangeSystem answers the reader a Business edition wires: one record, whose
// state a test moves, or an outage.
type fakeChangeSystem struct {
	mu    sync.Mutex
	rec   governance.ChangeRecord
	err   error
	calls []string
	// tenants is the tenant each read was made for; perKey answers a record by its key
	// before rec does.
	tenants []model.TenantID
	perKey  map[string]governance.ChangeRecord
	// onFirstRead runs once, during the first read and outside the lock: a test changes
	// the world while the engine is waiting on the change system.
	onFirstRead func()
}

type conflictApprovalData struct {
	api.ModuleData
	remaining int
}

func (d *conflictApprovalData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(&conflictApprovalScope{Scope: sc, remaining: &d.remaining})
	})
}

type conflictApprovalScope struct {
	store.Scope
	remaining *int
}

func (s *conflictApprovalScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if kind == "governance.approval" {
		return &conflictApprovalRepo{GenericRepo: repo, remaining: s.remaining}, err
	}
	return repo, err
}

type conflictApprovalRepo struct {
	store.GenericRepo
	remaining *int
}

func (r *conflictApprovalRepo) Update(ctx context.Context, rec model.Record) (model.Record, error) {
	if *r.remaining > 0 {
		*r.remaining--
		return nil, store.ErrConflict
	}
	return r.GenericRepo.Update(ctx, rec)
}

func (f *fakeChangeSystem) read(_ context.Context, tenant model.TenantID, system, destination, key string) (governance.ChangeRecord, error) {
	f.mu.Lock()
	f.calls = append(f.calls, system+"/"+destination+"/"+key)
	f.tenants = append(f.tenants, tenant)
	first, hook, rec, err := len(f.calls) == 1, f.onFirstRead, f.rec, f.err
	if own, ok := f.perKey[key]; ok {
		rec = own
	}
	f.mu.Unlock()
	if first && hook != nil {
		hook()
	}
	return rec, err
}

// lastTenant is the tenant the latest read was made for.
func (f *fakeChangeSystem) lastTenant() model.TenantID {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tenants) == 0 {
		return ""
	}
	return f.tenants[len(f.tenants)-1]
}

func (f *fakeChangeSystem) set(rec governance.ChangeRecord, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec, f.err = rec, err
}

func (f *fakeChangeSystem) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// changeRecordHarness is a tenant with a policy bound to CHG0030001, two admins and
// an open approval for the governed action.
type changeRecordHarness struct {
	h        *harness
	system   *fakeChangeSystem
	tenant   model.TenantID
	admin    string
	second   string
	approval string
}

func newChangeRecordHarness(t *testing.T, spec map[string]any) *changeRecordHarness {
	t.Helper()
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "change-record")
	_, second := h.roleUser(admin, tenant, "second@x.io", "admin")
	fake := &fakeChangeSystem{}
	h.gov.UseChangeRecordReader(fake.read)
	storePolicy(t, h, tenant, "bound to a change record", spec)
	r := h.createApproval(admin, tenant, map[string]any{"subject_kind": "gitpublish.target", "subject_ref": "prod", "action": changeRecordAction})
	if r.code != http.StatusCreated {
		t.Fatalf("create approval = %d %s", r.code, r.raw)
	}
	id, _ := r.body["id"].(string)
	return &changeRecordHarness{h: h, system: fake, tenant: tenant, admin: admin, second: second, approval: id}
}

// editPolicy changes the first stored policy of the tenant in place.
func (c *changeRecordHarness) editPolicy(change func(*model.Policy)) {
	c.h.t.Helper()
	err := c.h.st.Mutate(context.Background(), c.tenant, func(sc store.Scope) error {
		pols, _, err := sc.Policies().List(context.Background(), model.Query{Limit: 10})
		if err != nil || len(pols) == 0 {
			return fmt.Errorf("policies = %d %v", len(pols), err)
		}
		p := pols[0]
		change(&p)
		_, err = sc.Policies().Update(context.Background(), p)
		return err
	})
	if err != nil {
		c.h.t.Fatalf("edit the policy: %v", err)
	}
}

func rebindTo(key string) func(*model.Policy) {
	return func(p *model.Policy) {
		p.Spec = serviceNowSpec()
		p.Spec["change_record"] = map[string]any{"system": "servicenow", "key": key}
	}
}

func serviceNowSpec() map[string]any {
	return map[string]any{
		"required_approvals": 1, "match": map[string]any{"action": changeRecordAction},
		"change_record": map[string]any{"system": "servicenow", "key": "CHG0030001"},
	}
}

func (c *changeRecordHarness) status() string {
	c.h.t.Helper()
	r := c.h.do("GET", govPath+"/approvals/"+c.approval, c.admin, nil, tenantHdr(c.tenant))
	s, _ := r.body["status"].(string)
	return s
}

// auditMeta returns the stored meta of every ledger entry of an action, oldest first.
// Walk drops Meta for blinded entries; the canonical walker returns the JSON the
// commitment covers.
func (c *changeRecordHarness) auditMeta(action string) []map[string]any {
	c.h.t.Helper()
	var out []map[string]any
	if err := c.h.st.View(context.Background(), c.tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			c.h.t.Fatal("the audit store has no canonical walker")
		}
		return walker.WalkCanonical(context.Background(), 1, func(e model.AuditEvent, meta string, _ []byte) error {
			if e.Action != action {
				return nil
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(meta), &m); err != nil {
				return err
			}
			out = append(out, m)
			return nil
		})
	}); err != nil {
		c.h.t.Fatalf("walk audit: %v", err)
	}
	return out
}

// errCode and errMessage read the coded error envelope {"error":{"code","message"}}.
func errCode(r resp) string {
	e, _ := r.body["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func errMessage(r resp) string {
	e, _ := r.body["error"].(map[string]any)
	m, _ := e["message"].(string)
	return m
}

func scheduled() governance.ChangeRecord { return governance.ChangeRecord{State: "Scheduled"} }

// An approval under a policy bound to a change record succeeds while the record is in
// an allowed state, and the ledger keeps the key, the state and when it was observed.
func TestChangeRecordApprovalSucceedsWhileTheRecordIsScheduled(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(scheduled(), nil)

	r := c.h.decide(c.second, c.tenant, c.approval, "approve")
	if r.code != http.StatusOK || r.body["status"] != "approved" {
		t.Fatalf("approve with the record Scheduled = %d %s, want approved", r.code, r.raw)
	}
	if got := c.system.calls; len(got) != 1 || got[0] != "servicenow/servicenow/CHG0030001" {
		t.Fatalf("reads = %q, want one read of CHG0030001 through the servicenow destination", got)
	}
	if len(c.system.tenants) != 1 || c.system.tenants[0] != c.tenant {
		t.Fatalf("read for tenants %v, want only %s", c.system.tenants, c.tenant)
	}
	metas := c.auditMeta("governance.approval.decision")
	if len(metas) != 1 {
		t.Fatalf("decision ledger entries = %d, want 1", len(metas))
	}
	record, _ := metas[0]["change_record"].(map[string]any)
	if record["system"] != "servicenow" || record["key"] != "CHG0030001" || record["state"] != "Scheduled" || record["observed_at"] != model.NewTimestamp(baseTime).String() {
		t.Fatalf("decision meta change_record = %v, want system, key, state and observed_at %s", record, model.NewTimestamp(baseTime))
	}
}

// A record that is not in an allowed state refuses the approval with one sentence and
// leaves it pending: no decision is recorded.
func TestChangeRecordApprovalIsRefusedWhileTheRecordIsADraft(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(governance.ChangeRecord{State: "Draft"}, nil)

	r := c.h.decide(c.second, c.tenant, c.approval, "approve")
	if r.code != http.StatusConflict || errCode(r) != "change_record_not_approved" {
		t.Fatalf("approve with the record a Draft = %d %s, want 409 change_record_not_approved", r.code, r.raw)
	}
	msg := errMessage(r)
	if !strings.Contains(msg, "CHG0030001") || !strings.Contains(msg, "Draft") || strings.Count(msg, ". ") != 0 {
		t.Fatalf("refusal = %q, want one sentence naming CHG0030001 and Draft", msg)
	}
	if got := c.status(); got != "pending" {
		t.Fatalf("approval after the refusal = %q, want pending", got)
	}
	if n := len(c.auditMeta("governance.approval.decision")); n != 0 {
		t.Fatalf("decision ledger entries = %d, want none", n)
	}
}

// The system's own state words are matched without case, and a policy may list its own.
func TestChangeRecordAllowedStatesAreTheDefaultsOrThePolicys(t *testing.T) {
	for name, tc := range map[string]struct {
		states []string
		state  string
		want   int
	}{
		"default Implement":     {nil, "Implement", http.StatusOK},
		"default lower case":    {nil, "scheduled", http.StatusOK},
		"default Authorize":     {nil, "Authorize", http.StatusConflict},
		"listed Authorize":      {[]string{"authorize"}, "Authorize", http.StatusOK},
		"listed excludes Sched": {[]string{"authorize"}, "Scheduled", http.StatusConflict},
	} {
		t.Run(name, func(t *testing.T) {
			spec := serviceNowSpec()
			if tc.states != nil {
				spec["change_record"].(map[string]any)["states"] = tc.states
			}
			c := newChangeRecordHarness(t, spec)
			c.system.set(governance.ChangeRecord{State: tc.state}, nil)
			if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != tc.want {
				t.Fatalf("approve with the record %q = %d %s, want %d", tc.state, r.code, r.raw, tc.want)
			}
		})
	}
}

// The record's window bounds the approval: before it opens and after it closes the
// approval is refused, inside it the approval stands.
func TestChangeRecordWindowBoundsTheApproval(t *testing.T) {
	for name, tc := range map[string]struct {
		after time.Duration
		want  int
	}{
		"before it opens": {0, http.StatusConflict},
		"as it opens":     {time.Hour, http.StatusOK},
		"inside":          {90 * time.Minute, http.StatusOK},
		"as it closes":    {2 * time.Hour, http.StatusConflict},
		"after it closes": {3 * time.Hour, http.StatusConflict},
	} {
		t.Run(name, func(t *testing.T) {
			c := newChangeRecordHarness(t, serviceNowSpec())
			c.system.set(governance.ChangeRecord{State: "Scheduled", Start: baseTime.Add(time.Hour), End: baseTime.Add(2 * time.Hour)}, nil)
			c.h.clk.advance(tc.after)
			r := c.h.decide(c.second, c.tenant, c.approval, "approve")
			if r.code != tc.want || (tc.want == http.StatusConflict && errCode(r) != "change_record_not_approved") {
				t.Fatalf("approve %s = %d %s, want %d", name, r.code, r.raw, tc.want)
			}
		})
	}
}

// A policy changed while the engine waits on the change system is not decided under the
// record that was read: the engine reads the record the policy now names.
func TestChangeRecordPolicyChangedDuringTheReadIsReadAgain(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(scheduled(), nil)
	c.system.onFirstRead = func() { c.editPolicy(rebindTo("CHG0030002")) }
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s, want approved", r.code, r.raw)
	}
	if got := c.system.calls; len(got) != 2 || got[0] != "servicenow/servicenow/CHG0030001" || got[1] != "servicenow/servicenow/CHG0030002" {
		t.Fatalf("reads = %q, want CHG0030001 then, after the policy changed, CHG0030002", got)
	}
	metas := c.auditMeta("governance.approval.decision")
	if len(metas) != 1 {
		t.Fatalf("decision ledger entries = %d, want 1", len(metas))
	}
	if record, _ := metas[0]["change_record"].(map[string]any); record["key"] != "CHG0030002" {
		t.Fatalf("decision meta change_record = %v, want the record it was decided under, CHG0030002", record)
	}
}

// An observation from before a conflict is not proof of the current state. Both the
// decision and the spend read the record again after a conflicting write.
func TestChangeRecordConflictRetryRefreshesTheState(t *testing.T) {
	t.Run("spend", func(t *testing.T) {
		c := newChangeRecordHarness(t, serviceNowSpec())
		c.system.set(scheduled(), nil)
		if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
			t.Fatalf("approve = %d %s", r.code, r.raw)
		}
		c.system.mu.Lock()
		c.system.calls = nil
		c.system.onFirstRead = func() { c.system.set(governance.ChangeRecord{State: "Canceled"}, nil) }
		c.system.mu.Unlock()
		c.h.gov.UseData(&conflictApprovalData{ModuleData: api.NewModuleData(c.h.st), remaining: 1})
		got, err := c.h.gov.EngineApprovals().Consume(context.Background(), c.tenant, c.approval, "launch-1", "")
		if err != nil || got.Granted || got.Status != "canceled" {
			t.Fatalf("spend after a Scheduled-to-Canceled conflict = %+v %v, want the current revocation", got, err)
		}
	})
}

// An unreachable change system fails closed: the approval is refused as an outage,
// not as a verdict, and stays pending. A rejection never needs the system.
func TestChangeRecordOutageFailsClosedAndRejectionStillWorks(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(governance.ChangeRecord{}, errors.New("dial tcp 10.0.0.1:443: connection refused"))

	r := c.h.decide(c.second, c.tenant, c.approval, "approve")
	if r.code != http.StatusServiceUnavailable || errCode(r) != "change_record_unavailable" {
		t.Fatalf("approve with the system down = %d %s, want 503 change_record_unavailable", r.code, r.raw)
	}
	if msg := errMessage(r); strings.Contains(msg, "10.0.0.1") || strings.Contains(msg, "refused") {
		t.Fatalf("refusal = %q, must not echo the connector's error", msg)
	}
	before := c.system.callCount()
	if r := c.h.decide(c.second, c.tenant, c.approval, "reject"); r.code != http.StatusOK || r.body["status"] != "rejected" {
		t.Fatalf("reject with the system down = %d %s, want rejected", r.code, r.raw)
	}
	if c.system.callCount() != before {
		t.Fatal("a rejection read the change system")
	}
}

// A record the change system does not have refuses the approval as not approved. At the
// spend it refuses without revoking: the system may be hiding the record from its
// integration user, and a grant must not be destroyed for that.
func TestChangeRecordThatDoesNotExistIsRefusedButTheGrantIsKept(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	c.system.set(governance.ChangeRecord{}, governance.ErrChangeRecordNotFound)
	r := c.h.consume(c.admin, c.tenant, c.approval, "launch-1")
	if r.code != http.StatusConflict || errCode(r) != "change_record_not_approved" || !strings.Contains(errMessage(r), "was not found") {
		t.Fatalf("consume against a missing record = %d %s, want 409 change_record_not_approved", r.code, r.raw)
	}
	if st := c.status(); st != "approved" {
		t.Fatalf("approval after the refusal = %q, want approved", st)
	}
	if n := len(c.auditMeta("governance.approval.revoke")); n != 0 {
		t.Fatalf("revoke ledger entries = %d, want none", n)
	}
	c.system.set(scheduled(), nil)
	if got, err := c.h.gov.EngineApprovals().Consume(context.Background(), c.tenant, c.approval, "launch-1", ""); err != nil || !got.Granted {
		t.Fatalf("consume once the system finds it = %+v %v, want granted", got, err)
	}

	other := newChangeRecordHarness(t, serviceNowSpec())
	other.system.set(governance.ChangeRecord{}, governance.ErrChangeRecordNotFound)
	d := other.h.decide(other.second, other.tenant, other.approval, "approve")
	if d.code != http.StatusConflict || errCode(d) != "change_record_not_approved" || !strings.Contains(errMessage(d), "was not found") {
		t.Fatalf("approve against a missing record = %d %s, want 409 change_record_not_approved", d.code, d.raw)
	}
}

// The window can still open: a spend before it refuses and keeps the grant. Once it has
// closed the grant is revoked.
func TestChangeRecordWindowAtTheSpend(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	c.system.set(governance.ChangeRecord{State: "Scheduled", Start: baseTime.Add(time.Hour), End: baseTime.Add(2 * time.Hour)}, nil)
	if r := c.h.consume(c.admin, c.tenant, c.approval, "launch-1"); r.code != http.StatusConflict || !strings.Contains(errMessage(r), "has not opened") || c.status() != "approved" {
		t.Fatalf("consume before the window = %d %s status=%s, want 409 and the grant kept", r.code, r.raw, c.status())
	}
	c.h.clk.advance(3 * time.Hour)
	got, err := c.h.gov.EngineApprovals().Consume(context.Background(), c.tenant, c.approval, "launch-1", "")
	if err != nil || got.Granted || got.Status != "canceled" {
		t.Fatalf("consume after the window = %+v %v, want revoked", got, err)
	}
}

// The grant is checked against the record the approvers saw: a policy pointed at another
// record, or switched off, after the approval does not change what the spend is checked
// against.
func TestChangeRecordGrantIsSpentAgainstTheRecordItWasApprovedUnder(t *testing.T) {
	for name, change := range map[string]func(*model.Policy){
		"policy rebound to a scheduled record": rebindTo("CHG0030002"),
		"policy switched off":                  func(p *model.Policy) { p.Enabled = false },
	} {
		t.Run(name, func(t *testing.T) {
			c := newChangeRecordHarness(t, serviceNowSpec())
			c.system.perKey = map[string]governance.ChangeRecord{"CHG0030001": scheduled(), "CHG0030002": scheduled()}
			if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
				t.Fatalf("approve = %d %s", r.code, r.raw)
			}
			c.system.perKey["CHG0030001"] = governance.ChangeRecord{State: "Canceled"}
			c.editPolicy(change)
			got, err := c.h.gov.EngineApprovals().Consume(context.Background(), c.tenant, c.approval, "launch-1", "")
			if err != nil || got.Granted || got.Status != "canceled" {
				t.Fatalf("consume = %+v %v, want revoked on CHG0030001, the record it was approved under", got, err)
			}
			if last := c.system.calls[len(c.system.calls)-1]; last != "servicenow/servicenow/CHG0030001" {
				t.Fatalf("last read = %q, want CHG0030001", last)
			}
		})
	}
}

// An approval given before any policy bound a record is checked against the record the
// policy names when it is spent; a policy that changes while that read waits is read again.
func TestChangeRecordBoundAfterTheApprovalIsReadAgainWhenItChangesDuringTheRead(t *testing.T) {
	c := newChangeRecordHarness(t, map[string]any{"required_approvals": 1, "match": map[string]any{"action": "unrelated.action"}})
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	c.editPolicy(func(p *model.Policy) { p.Spec = serviceNowSpec() })
	c.system.set(scheduled(), nil)
	c.system.onFirstRead = func() { c.editPolicy(rebindTo("CHG0030002")) }
	if got, err := c.h.gov.EngineApprovals().Consume(context.Background(), c.tenant, c.approval, "launch-1", ""); err != nil || !got.Granted {
		t.Fatalf("consume = %+v %v, want granted", got, err)
	}
	if got := c.system.calls; len(got) != 2 || got[1] != "servicenow/servicenow/CHG0030002" {
		t.Fatalf("reads = %q, want CHG0030001 then, after the policy changed, CHG0030002", got)
	}
}

// A request that would be refused anyway never reaches the change system.
func TestChangeRecordIsNotReadForARefusalThatDoesNotNeedIt(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(governance.ChangeRecord{}, errors.New("down"))
	if r := c.h.decide(c.admin, c.tenant, c.approval, "approve"); r.code != http.StatusForbidden {
		t.Fatalf("the requester approving their own request = %d %s, want 403", r.code, r.raw)
	}
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	reads := c.system.callCount()
	c.system.set(governance.ChangeRecord{}, errors.New("down"))
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusConflict || strings.Contains(r.raw, "change_record") {
		t.Fatalf("a second decision on a decided approval = %d %s, want the ordinary 409", r.code, r.raw)
	}
	if got := c.system.callCount(); got != reads {
		t.Fatalf("reads = %d after the refusals, want %d from the one approval that needed them", got, reads)
	}
}

// With two approvals required, each approving decision reads the record and the ledger
// keeps what each saw; the first leaves the approval pending.
func TestChangeRecordEveryApprovingDecisionReadsTheRecord(t *testing.T) {
	spec := serviceNowSpec()
	spec["required_approvals"] = 2
	c := newChangeRecordHarness(t, spec)
	_, third := c.h.roleUser(c.admin, c.tenant, "third@x.io", "admin")
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK || r.body["status"] != "pending" {
		t.Fatalf("first approval = %d %s, want pending", r.code, r.raw)
	}
	if r := c.h.decide(third, c.tenant, c.approval, "approve"); r.code != http.StatusOK || r.body["status"] != "approved" {
		t.Fatalf("second approval = %d %s, want approved", r.code, r.raw)
	}
	if n := c.system.callCount(); n != 2 {
		t.Fatalf("reads = %d, want one per approving decision", n)
	}
	if metas := c.auditMeta("governance.approval.decision"); len(metas) != 2 || metas[0]["change_record"] == nil || metas[1]["change_record"] == nil {
		t.Fatalf("decision ledger entries = %v, want both to carry what was seen", metas)
	}
}

// A deployment that wired no reader cannot verify the record, so it approves nothing
// under the policy.
func TestChangeRecordWithoutAReaderApprovesNothing(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "no-reader")
	_, second := h.roleUser(admin, tenant, "second@x.io", "admin")
	storePolicy(t, h, tenant, "bound", serviceNowSpec())
	r := h.createApproval(admin, tenant, map[string]any{"subject_kind": "gitpublish.target", "subject_ref": "prod", "action": changeRecordAction})
	id, _ := r.body["id"].(string)
	if d := h.decide(second, tenant, id, "approve"); d.code != http.StatusServiceUnavailable || errCode(d) != "change_record_unavailable" {
		t.Fatalf("approve without a reader = %d %s, want 503 change_record_unavailable", d.code, d.raw)
	}
}

// Spending an approval re-reads the record: canceled after the approval, the grant
// is revoked before it is used, and the ledger says why.
func TestChangeRecordCanceledAfterApprovalRevokesTheGrantBeforeUse(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	c.system.set(governance.ChangeRecord{State: "Canceled"}, nil)

	got, err := c.h.gov.EngineApprovals().Consume(context.Background(), c.tenant, c.approval, "launch-1", "")
	if err != nil || got.Granted || got.Status != "canceled" {
		t.Fatalf("consume after the record was canceled = %+v %v, want not granted and canceled", got, err)
	}
	if st := c.status(); st != "canceled" {
		t.Fatalf("approval after the revocation = %q, want canceled", st)
	}
	revoked := c.auditMeta("governance.approval.revoke")
	if len(revoked) != 1 {
		t.Fatalf("revoke ledger entries = %d, want 1", len(revoked))
	}
	record, _ := revoked[0]["change_record"].(map[string]any)
	if record["key"] != "CHG0030001" || record["state"] != "Canceled" {
		t.Fatalf("revoke meta change_record = %v, want CHG0030001 Canceled", record)
	}
	again, err := c.h.gov.EngineApprovals().Consume(context.Background(), c.tenant, c.approval, "launch-2", "")
	if err != nil || again.Granted {
		t.Fatalf("a second consume = %+v %v, want not granted", again, err)
	}
}

// A grant whose record is still approved is spent once, as before, and the ledger keeps
// what the spend observed.
func TestChangeRecordStillScheduledGrantsTheSpend(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	got, err := c.h.gov.EngineApprovals().Consume(context.Background(), c.tenant, c.approval, "launch-1", "")
	if err != nil || !got.Granted {
		t.Fatalf("consume with the record Scheduled = %+v %v, want granted", got, err)
	}
	metas := c.auditMeta("governance.approval.consume")
	if len(metas) != 1 {
		t.Fatalf("consume ledger entries = %d, want 1", len(metas))
	}
	if record, _ := metas[0]["change_record"].(map[string]any); record["state"] != "Scheduled" {
		t.Fatalf("consume meta change_record = %v, want state Scheduled", record)
	}
	// The same caller retrying inside the idempotency window does not read again.
	reads := c.system.callCount()
	if again, err := c.h.gov.EngineApprovals().Consume(context.Background(), c.tenant, c.approval, "launch-1", ""); err != nil || !again.Granted {
		t.Fatalf("retry = %+v %v, want granted", again, err)
	}
	if c.system.callCount() != reads {
		t.Fatal("a transport retry read the change system again")
	}
}

// An outage at the spend refuses this spend only: the grant stays approved and is
// spendable once the system answers.
func TestChangeRecordOutageAtTheSpendDoesNotRevoke(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	c.system.set(governance.ChangeRecord{}, errors.New("timeout"))
	r := c.h.do("POST", govPath+"/approvals/"+c.approval+"/consume", c.admin, map[string]any{"consumer_id": "launch-1"}, tenantHdr(c.tenant))
	if r.code != http.StatusServiceUnavailable || errCode(r) != "change_record_unavailable" {
		t.Fatalf("consume with the system down = %d %s, want 503 change_record_unavailable", r.code, r.raw)
	}
	if st := c.status(); st != "approved" {
		t.Fatalf("approval after an outage = %q, want approved", st)
	}
	c.system.set(scheduled(), nil)
	if got, err := c.h.gov.EngineApprovals().Consume(context.Background(), c.tenant, c.approval, "launch-1", ""); err != nil || !got.Granted {
		t.Fatalf("consume after recovery = %+v %v, want granted", got, err)
	}
}

// A policy that names no change record keeps its behavior and never reads a change system.
func TestChangeRecordPolicyWithoutARecordReadsNothing(t *testing.T) {
	spec := map[string]any{"required_approvals": 1, "match": map[string]any{"action": changeRecordAction}}
	c := newChangeRecordHarness(t, spec)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK || r.body["status"] != "approved" {
		t.Fatalf("approve = %d %s, want approved", r.code, r.raw)
	}
	if got, err := c.h.gov.EngineApprovals().Consume(context.Background(), c.tenant, c.approval, "launch-1", ""); err != nil || !got.Granted {
		t.Fatalf("consume = %+v %v, want granted", got, err)
	}
	if n := c.system.callCount(); n != 0 {
		t.Fatalf("reads = %d, want none", n)
	}
}

// A malformed change_record is a 400 in every edition, before any edition line applies.
func TestChangeRecordSpecIsValidatedBeforeTheEdition(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "validate")
	longKey := strings.Repeat("A", 65)
	for name, record := range map[string]map[string]any{
		"unknown system":         {"system": "remedy", "key": "CHG1"},
		"no system":              {"key": "CHG1"},
		"no key":                 {"system": "servicenow"},
		"key with a space":       {"system": "servicenow", "key": "CHG 1"},
		"key with a query":       {"system": "servicenow", "key": "CHG1^ORnumber=CHG2"},
		"key too long":           {"system": "servicenow", "key": longKey},
		"jira lists no states":   {"system": "jira", "key": "OPS-1"},
		"destination with a /":   {"system": "servicenow", "key": "CHG1", "destination": "a/b"},
		"empty state":            {"system": "servicenow", "key": "CHG1", "states": []string{" "}},
		"too many states":        {"system": "servicenow", "key": "CHG1", "states": strings.Split(strings.Repeat("s,", 17), ",")[:17]},
		"unknown change_record":  {"system": "servicenow", "key": "CHG1", "window": "any"},
		"state with a long text": {"system": "servicenow", "key": "CHG1", "states": []string{strings.Repeat("x", 65)}},
	} {
		r := h.do("POST", govPath+"/policies", admin, map[string]any{"name": "p", "kind": "approval", "enabled": true,
			"spec": map[string]any{"match": map[string]any{"action": changeRecordAction}, "change_record": record}}, tenantHdr(tenant))
		if r.code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", name, r.code, r.raw)
		}
	}
}

// A gate that reads an approved status instead of spending it asks the engine whether the
// grant still holds. Scheduled it does; canceled it does not, and the grant is revoked
// with the ledger saying why, once.
func TestChangeRecordStillHoldsRevokesAGrantWhoseRecordWasCanceled(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	service := c.h.gov.EngineApprovals()
	if status, err := service.StillHolds(context.Background(), c.tenant, c.approval); err != nil || status != "approved" {
		t.Fatalf("StillHolds with the record Scheduled = %q %v, want approved", status, err)
	}
	if got := c.system.lastTenant(); got != c.tenant {
		t.Fatalf("the check read the change system for tenant %q, want %q", got, c.tenant)
	}
	if n := len(c.auditMeta("governance.approval.revoke")); n != 0 {
		t.Fatalf("revoke ledger entries while Scheduled = %d, want none", n)
	}

	c.system.set(governance.ChangeRecord{State: "Canceled"}, nil)
	if status, err := service.StillHolds(context.Background(), c.tenant, c.approval); err != nil || status != "canceled" {
		t.Fatalf("StillHolds after the record was canceled = %q %v, want canceled", status, err)
	}
	if st := c.status(); st != "canceled" {
		t.Fatalf("approval after the check = %q, want canceled", st)
	}
	revoked := c.auditMeta("governance.approval.revoke")
	if len(revoked) != 1 {
		t.Fatalf("revoke ledger entries = %d, want 1", len(revoked))
	}
	if record, _ := revoked[0]["change_record"].(map[string]any); record["key"] != "CHG0030001" || record["state"] != "Canceled" {
		t.Fatalf("revoke meta change_record = %v, want CHG0030001 Canceled", record)
	}
	reads := c.system.callCount()
	if status, err := service.StillHolds(context.Background(), c.tenant, c.approval); err != nil || status != "canceled" {
		t.Fatalf("StillHolds on the revoked grant = %q %v, want canceled", status, err)
	}
	if c.system.callCount() != reads || len(c.auditMeta("governance.approval.revoke")) != 1 {
		t.Fatal("a revoked grant was read or revoked again")
	}
}

// A change system that cannot answer, or does not have the record, refuses the check and
// revokes nothing: the grant is checked again when the system can say.
func TestChangeRecordStillHoldsRefusesWithoutRevokingWhenTheSystemCannotAnswer(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	service := c.h.gov.EngineApprovals()
	for name, answer := range map[string]struct {
		err  error
		want string
	}{
		"outage":    {errors.New("timeout"), "could not be checked"},
		"not found": {governance.ErrChangeRecordNotFound, "was not found"},
	} {
		c.system.set(governance.ChangeRecord{}, answer.err)
		status, err := service.StillHolds(context.Background(), c.tenant, c.approval)
		if err == nil || !strings.Contains(err.Error(), answer.want) || status == "approved" {
			t.Fatalf("%s: StillHolds = %q %v, want an error saying %q and not approved", name, status, err, answer.want)
		}
		if st := c.status(); st != "approved" {
			t.Fatalf("%s: approval after the refusal = %q, want approved", name, st)
		}
	}
	if n := len(c.auditMeta("governance.approval.revoke")); n != 0 {
		t.Fatalf("revoke ledger entries = %d, want none", n)
	}
	c.system.set(scheduled(), nil)
	if status, err := service.StillHolds(context.Background(), c.tenant, c.approval); err != nil || status != "approved" {
		t.Fatalf("StillHolds after recovery = %q %v, want approved", status, err)
	}
}

// A grant somebody already spent is not read again for what it already authorized, but it
// authorizes nothing new once its change record no longer allows the change: the check
// refuses it with the record's sentence. The spend cannot be taken back, so the grant keeps
// its status and nothing is revoked; the refusal reaches the ledger once per run.
func TestChangeRecordStillHoldsRefusesASpentGrantWhoseRecordNoLongerAllowsIt(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	service := c.h.gov.EngineApprovals()
	if got, err := service.Consume(context.Background(), c.tenant, c.approval, "launch-1", ""); err != nil || !got.Granted {
		t.Fatalf("consume = %+v %v, want granted", got, err)
	}
	if status, err := service.StillHolds(context.Background(), c.tenant, c.approval); err != nil || status != "approved" {
		t.Fatalf("StillHolds on a spent grant with the record Scheduled = %q %v, want approved", status, err)
	}

	c.system.set(governance.ChangeRecord{State: "Canceled"}, nil)
	for range 2 {
		status, err := service.StillHolds(context.Background(), c.tenant, c.approval)
		if err == nil || !strings.Contains(err.Error(), "CHG0030001") || status == "approved" {
			t.Fatalf("StillHolds on a spent grant after the record was canceled = %q %v, want the record's refusal and not approved", status, err)
		}
	}
	if st := c.status(); st != "approved" {
		t.Fatalf("spent approval after the refusal = %q, want approved", st)
	}
	if n := len(c.auditMeta("governance.approval.revoke")); n != 0 {
		t.Fatalf("revoke ledger entries = %d, want none for a spent grant", n)
	}
	if n := len(c.auditMeta("governance.approval.change_record_refused")); n != 1 {
		t.Fatalf("refusal ledger entries = %d, want 1 for a repeated refusal", n)
	}
}

// A first spend that commits while the check waits on a record that was just canceled
// stands, and the check does not rely on the grant: it refuses with the record's sentence,
// as for any spent grant, and revokes nothing.
func TestChangeRecordStillHoldsRefusesAGrantSpentWhileItReadsACanceledRecord(t *testing.T) {
	// Approved before the policy bound the record, so the check makes the first read.
	c := newChangeRecordHarness(t, map[string]any{"required_approvals": 1, "match": map[string]any{"action": "unrelated.action"}})
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	c.editPolicy(func(p *model.Policy) { p.Spec = serviceNowSpec() })
	service := c.h.gov.EngineApprovals()
	canceled := governance.ChangeRecord{State: "Canceled"}
	c.system.set(canceled, nil)
	c.system.onFirstRead = func() {
		// The spend read the record before the cancel, then committed.
		c.system.set(scheduled(), nil)
		if got, err := service.Consume(context.Background(), c.tenant, c.approval, "launch-1", ""); err != nil || !got.Granted {
			t.Errorf("the concurrent first spend = %+v %v, want granted", got, err)
		}
		c.system.set(canceled, nil)
	}
	status, err := service.StillHolds(context.Background(), c.tenant, c.approval)
	if err == nil || !strings.Contains(err.Error(), "CHG0030001") || status == "approved" {
		t.Fatalf("StillHolds after a spend raced the canceled record = %q %v, want the record's refusal and not approved", status, err)
	}
	if st := c.status(); st != "approved" {
		t.Fatalf("spent approval after the refusal = %q, want approved", st)
	}
	if n := len(c.auditMeta("governance.approval.revoke")); n != 0 {
		t.Fatalf("revoke ledger entries = %d, want none for a spent grant", n)
	}
	if n := len(c.auditMeta("governance.approval.change_record_refused")); n != 1 {
		t.Fatalf("refusal ledger entries = %d, want 1", n)
	}
}

// An approval no change record governs reads nothing: the check answers from its own state.
func TestChangeRecordStillHoldsLeavesAnUngovernedApprovalAlone(t *testing.T) {
	free := newChangeRecordHarness(t, map[string]any{"required_approvals": 1, "match": map[string]any{"action": changeRecordAction}})
	if r := free.h.decide(free.second, free.tenant, free.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	service := free.h.gov.EngineApprovals()
	if status, err := service.StillHolds(context.Background(), free.tenant, free.approval); err != nil || status != "approved" {
		t.Fatalf("StillHolds without a change record = %q %v, want approved", status, err)
	}
	if got, err := service.Consume(context.Background(), free.tenant, free.approval, "launch-1", ""); err != nil || !got.Granted {
		t.Fatalf("consume = %+v %v, want granted", got, err)
	}
	if status, err := service.StillHolds(context.Background(), free.tenant, free.approval); err != nil || status != "approved" {
		t.Fatalf("StillHolds on a spent grant without a change record = %q %v, want approved", status, err)
	}
	if n := free.system.callCount(); n != 0 {
		t.Fatalf("reads = %d, want none", n)
	}
}

// A spent grant is refused whenever its record stops allowing the change, not only for a
// cancel: a window that has closed is final too, and an outage or a record the system does
// not have is refused without ending anything. Each refusal keeps the grant approved, and
// the grant is relied on again once the record allows the change.
func TestChangeRecordStillHoldsRefusesASpentGrantForEveryReasonTheRecordFails(t *testing.T) {
	for name, tc := range map[string]struct {
		rec  governance.ChangeRecord
		err  error
		want string
	}{
		"window closed": {rec: governance.ChangeRecord{State: "Scheduled", Start: baseTime.Add(-time.Hour), End: baseTime.Add(time.Hour)}, want: "window that has closed"},
		"outage":        {err: errors.New("timeout"), want: "could not be checked"},
		"not found":     {err: governance.ErrChangeRecordNotFound, want: "was not found"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newChangeRecordHarness(t, serviceNowSpec())
			c.system.set(governance.ChangeRecord{State: "Scheduled", Start: baseTime.Add(-time.Hour), End: baseTime.Add(time.Hour)}, nil)
			if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
				t.Fatalf("approve = %d %s", r.code, r.raw)
			}
			service := c.h.gov.EngineApprovals()
			if got, err := service.Consume(context.Background(), c.tenant, c.approval, "launch-1", ""); err != nil || !got.Granted {
				t.Fatalf("consume = %+v %v, want granted", got, err)
			}
			c.system.set(tc.rec, tc.err)
			if name == "window closed" {
				c.h.clk.advance(2 * time.Hour)
			}
			status, err := service.StillHolds(context.Background(), c.tenant, c.approval)
			if err == nil || !strings.Contains(err.Error(), tc.want) || status == "approved" {
				t.Fatalf("StillHolds = %q %v, want an error saying %q and not approved", status, err, tc.want)
			}
			if st := c.status(); st != "approved" {
				t.Fatalf("spent approval after the refusal = %q, want approved", st)
			}
			if n := len(c.auditMeta("governance.approval.revoke")); n != 0 {
				t.Fatalf("revoke ledger entries = %d, want none", n)
			}
			c.system.set(governance.ChangeRecord{State: "Scheduled"}, nil)
			if status, err := service.StillHolds(context.Background(), c.tenant, c.approval); err != nil || status != "approved" {
				t.Fatalf("StillHolds once the record allows the change = %q %v, want approved", status, err)
			}
		})
	}
}

// What a change system blocked is in the ledger, not only what it revoked: a refused
// approval, a refused spend and a refused check each leave an entry with the key, the
// state when the system answered, and when it was observed.
func TestChangeRecordRefusalsAreInTheLedger(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(governance.ChangeRecord{State: "Draft"}, nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusConflict {
		t.Fatalf("approve against a draft = %d %s, want 409", r.code, r.raw)
	}
	c.system.set(governance.ChangeRecord{}, errors.New("timeout"))
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusServiceUnavailable {
		t.Fatalf("approve with the system down = %d %s, want 503", r.code, r.raw)
	}
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	c.system.set(governance.ChangeRecord{}, governance.ErrChangeRecordNotFound)
	if r := c.h.consume(c.admin, c.tenant, c.approval, "launch-1"); r.code != http.StatusConflict {
		t.Fatalf("consume against a missing record = %d %s, want 409", r.code, r.raw)
	}
	if _, err := c.h.gov.EngineApprovals().StillHolds(context.Background(), c.tenant, c.approval); err == nil {
		t.Fatal("StillHolds against a missing record answered without an error")
	}

	refused := c.auditMeta("governance.approval.change_record_refused")
	if len(refused) != 4 {
		t.Fatalf("refusal ledger entries = %d, want 4 (%v)", len(refused), refused)
	}
	type want struct{ step, code, state string }
	for i, w := range []want{
		{"decision", "change_record_not_approved", "Draft"},
		{"decision", "change_record_unavailable", ""},
		{"spend", "change_record_not_approved", "not found"},
		{"check", "change_record_not_approved", "not found"},
	} {
		got := refused[i]
		record, _ := got["change_record"].(map[string]any)
		if got["step"] != w.step || got["code"] != w.code || record["key"] != "CHG0030001" || record["system"] != "servicenow" {
			t.Errorf("refusal %d = %v, want step %s code %s for CHG0030001", i, got, w.step, w.code)
		}
		if state, _ := record["state"].(string); state != w.state {
			t.Errorf("refusal %d state = %q, want %q", i, state, w.state)
		}
		if observed, _ := record["observed_at"].(string); (w.state != "") != (observed != "") {
			t.Errorf("refusal %d observed_at = %q, want it set exactly when the system answered", i, observed)
		}
	}
}

// The state label is the external system's text. It reaches the API sentence and the
// ledger as printable characters only: no control, newline, bidi override or zero-width
// character survives.
func TestChangeRecordStateLabelKeepsPrintableCharactersOnly(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(governance.ChangeRecord{State: "Dra‮\nf​t\x00\x1b[31m"}, nil)
	r := c.h.decide(c.second, c.tenant, c.approval, "approve")
	if r.code != http.StatusConflict || !strings.Contains(errMessage(r), "is in state Draft[31m, and") {
		t.Fatalf("approve against a draft = %d %s, want 409 naming the printable part of the state", r.code, r.raw)
	}
	for _, bad := range []string{"‮", "\n", "​", "\x00", "\x1b"} {
		if strings.Contains(errMessage(r), bad) {
			t.Errorf("refusal sentence %q holds %q", errMessage(r), bad)
		}
	}
	refused := c.auditMeta("governance.approval.change_record_refused")
	if len(refused) != 1 {
		t.Fatalf("refusal ledger entries = %d, want 1", len(refused))
	}
	if record, _ := refused[0]["change_record"].(map[string]any); record["state"] != "Draft[31m" {
		t.Fatalf("ledger state = %q, want the printable part only", record["state"])
	}
}

// An approval that has not reached its quorum is not a grant: the check answers from its
// own state and reads nothing.
func TestChangeRecordStillHoldsReadsNothingForAnApprovalShortOfItsQuorum(t *testing.T) {
	spec := serviceNowSpec()
	spec["required_approvals"] = 2
	c := newChangeRecordHarness(t, spec)
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK || r.body["status"] != "pending" {
		t.Fatalf("first approval = %d %s, want pending", r.code, r.raw)
	}
	reads := c.system.callCount()
	if status, err := c.h.gov.EngineApprovals().StillHolds(context.Background(), c.tenant, c.approval); err != nil || status != "pending" {
		t.Fatalf("StillHolds short of the quorum = %q %v, want pending", status, err)
	}
	if c.system.callCount() != reads {
		t.Fatal("the check read the change system for an approval nobody can use")
	}
}

// The state that is compared with the allowed states is the one the system returned, not
// the printable part of it: a label that differs by an invisible character is another state.
func TestChangeRecordStateIsComparedAsTheSystemReturnedIt(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(governance.ChangeRecord{State: "Sche​duled"}, nil)
	r := c.h.decide(c.second, c.tenant, c.approval, "approve")
	if r.code != http.StatusConflict || errCode(r) != "change_record_not_approved" {
		t.Fatalf("approve with the state %q = %d %s, want 409 change_record_not_approved", "Sche​duled", r.code, r.raw)
	}
	if st := c.status(); st != "pending" {
		t.Fatalf("approval after the refusal = %q, want pending", st)
	}
}

// A gate that polls an approved grant during an outage must not grow the ledger with every
// poll: the first refusal of a kind is recorded, the repeats are not, and a recovery ends
// the run so the next outage is recorded again.
func TestChangeRecordRepeatedCheckRefusalsAreRecordedOnce(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	service := c.h.gov.EngineApprovals()
	poll := func(want int) {
		t.Helper()
		for range 5 {
			if _, err := service.StillHolds(context.Background(), c.tenant, c.approval); err == nil {
				t.Fatal("StillHolds answered while the system was down")
			}
		}
		if n := len(c.auditMeta("governance.approval.change_record_refused")); n != want {
			t.Fatalf("refusal ledger entries = %d, want %d", n, want)
		}
	}
	recover := func() {
		t.Helper()
		c.system.set(scheduled(), nil)
		if status, err := service.StillHolds(context.Background(), c.tenant, c.approval); err != nil || status != "approved" {
			t.Fatalf("StillHolds after recovery = %q %v, want approved", status, err)
		}
	}
	c.system.set(governance.ChangeRecord{}, errors.New("timeout"))
	poll(1)
	recover()
	c.system.set(governance.ChangeRecord{}, errors.New("timeout"))
	poll(2) // the same refusal after a recovery is a new run
	c.system.set(governance.ChangeRecord{}, governance.ErrChangeRecordNotFound)
	poll(3) // another kind of refusal is another fact
}

// ledgerOutage is the module's data handle with a ledger that refuses every append while
// down is set, and the store otherwise.
type ledgerOutage struct {
	api.ModuleData
	down atomic.Bool
}

func (l *ledgerOutage) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return l.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		if l.down.Load() {
			sc = downLedgerScope{sc}
		}
		return fn(sc)
	})
}

type downLedgerScope struct{ store.Scope }

func (s downLedgerScope) Audit() store.AuditLog { return downLedger{s.Scope.Audit()} }

type downLedger struct{ store.AuditLog }

func (downLedger) Append(context.Context, model.AuditDraft) (model.AuditEvent, error) {
	return model.AuditEvent{}, errors.New("ledger down")
}

// A refusal the ledger could not take is not a refusal recorded: the same sentence, polled
// again once the ledger answers, is written then, and only then marks the run.
func TestChangeRecordACheckRefusalTheLedgerCouldNotTakeIsOfferedAgain(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	outage := &ledgerOutage{ModuleData: api.NewModuleData(c.h.st)}
	c.h.gov.UseData(outage)
	service := c.h.gov.EngineApprovals()
	poll := func() {
		t.Helper()
		if _, err := service.StillHolds(context.Background(), c.tenant, c.approval); err == nil {
			t.Fatal("StillHolds answered while the system was down")
		}
	}
	entries := func() int { return len(c.auditMeta("governance.approval.change_record_refused")) }

	c.system.set(governance.ChangeRecord{}, errors.New("timeout"))
	outage.down.Store(true)
	poll()
	if n := entries(); n != 0 {
		t.Fatalf("refusal ledger entries with the ledger down = %d, want 0", n)
	}
	outage.down.Store(false)
	poll()
	poll()
	if n := entries(); n != 1 {
		t.Fatalf("refusal ledger entries once the ledger answered = %d, want 1", n)
	}
}

// A stored-approved grant is not spendable or reliable past its expiry. The status
// check applies the same re-anchor as the consume path and does not read the record.
func TestChangeRecordStillHoldsRefusesAnExpiredApprovedGrant(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	c.expireApproval()
	reads := c.system.callCount()
	status, err := c.h.gov.EngineApprovals().StillHolds(context.Background(), c.tenant, c.approval)
	if err != nil || status != "expired" {
		t.Fatalf("StillHolds with an expired approved grant = %q %v, want expired", status, err)
	}
	if c.system.callCount() != reads {
		t.Fatal("the expiry refusal read the change system")
	}
}

// An approval no change record governs has the same expiry ceiling at a status check as
// at a spend.
func TestChangeRecordStillHoldsRefusesAnExpiredGrantNoRecordGoverns(t *testing.T) {
	free := newChangeRecordHarness(t, map[string]any{"required_approvals": 1, "match": map[string]any{"action": changeRecordAction}})
	if r := free.h.decide(free.second, free.tenant, free.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	free.expireApproval()
	status, err := free.h.gov.EngineApprovals().StillHolds(context.Background(), free.tenant, free.approval)
	if err != nil || status != "expired" {
		t.Fatalf("StillHolds with an expired approved grant and no change record = %q %v, want expired", status, err)
	}
}

// expireApproval moves the stored expiry of the harness's approval into the past.
func (c *changeRecordHarness) expireApproval() {
	c.h.t.Helper()
	if err := c.h.st.Mutate(context.Background(), c.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("governance.approval")
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(c.approval))
		if err != nil {
			return err
		}
		rec["expires_at"] = model.NewTimestamp(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)).String()
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		c.h.t.Fatal(err)
	}
}

func TestChangeRecordStillHoldsRechecksExpiryAfterTheRecordRead(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(scheduled(), nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	if err := c.h.st.Mutate(context.Background(), c.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("governance.approval")
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(c.approval))
		if err != nil {
			return err
		}
		rec["expires_at"] = model.NewTimestamp(baseTime.Add(time.Hour)).String()
		_, err = repo.Update(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	c.system.mu.Lock()
	c.system.calls = nil
	c.system.onFirstRead = func() { c.h.clk.advance(2 * time.Hour) }
	c.system.mu.Unlock()
	status, err := c.h.gov.EngineApprovals().StillHolds(context.Background(), c.tenant, c.approval)
	if err != nil || status != "expired" {
		t.Fatalf("StillHolds after expiry during the record read = %q %v, want expired", status, err)
	}
}

func TestChangeRecordStillHoldsRechecksChangeWindowAfterTheRecordRead(t *testing.T) {
	c := newChangeRecordHarness(t, serviceNowSpec())
	c.system.set(governance.ChangeRecord{State: "Scheduled", End: baseTime.Add(time.Hour)}, nil)
	if r := c.h.decide(c.second, c.tenant, c.approval, "approve"); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	c.h.clk.mu.Lock()
	reads := 0
	c.h.clk.onNow = func() {
		reads++
		if reads == 2 {
			c.h.clk.advance(2 * time.Hour)
		}
	}
	c.h.clk.mu.Unlock()
	status, err := c.h.gov.EngineApprovals().StillHolds(context.Background(), c.tenant, c.approval)
	if err != nil || status != "canceled" {
		t.Fatalf("StillHolds after the change window closed during the read = %q %v, want canceled", status, err)
	}
}
