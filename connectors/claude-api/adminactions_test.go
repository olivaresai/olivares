// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// capturedWrite records one write the Actuator's transport issued, so a test can assert
// the method/path/body — and, crucially, that NO write happens when the PEP denies.
type capturedWrite struct {
	method string
	path   string
	body   string
	beta   string // the anthropic-beta header (empty for the non-beta actions)
}

type recordingDoer struct {
	reqs   []capturedWrite
	status int // 0 => 200
}

func (d *recordingDoer) Do(req *http.Request) (*http.Response, error) {
	var b []byte
	if req.Body != nil {
		b, _ = io.ReadAll(req.Body)
	}
	d.reqs = append(d.reqs, capturedWrite{req.Method, req.URL.EscapedPath(), string(b), req.Header.Get("anthropic-beta")})
	st := d.status
	if st == 0 {
		st = http.StatusOK
	}
	return &http.Response{StatusCode: st, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
}

// stubAdminGate is a configurable approval gate. echoPlan controls the PlanHash it binds
// the decision to (the real plan, a wrong one, or none).
type stubAdminGate struct {
	status    AdminActionStatus
	echoPlan  bool
	wrongPlan bool
	err       error
}

func (g stubAdminGate) Authorize(_ context.Context, req AdminActionRequest) (AdminActionDecision, error) {
	if g.err != nil {
		return AdminActionDecision{}, g.err
	}
	plan := ""
	if g.echoPlan {
		plan = req.PlanHash
	}
	if g.wrongPlan {
		plan = "WRONG-" + req.PlanHash
	}
	return AdminActionDecision{ApprovalRef: "appr-1", Status: g.status, PlanHash: plan}, nil
}

type capAdminAuditor struct{ recs []AdminActionRecord }

func (a *capAdminAuditor) Record(_ context.Context, r AdminActionRecord) { a.recs = append(a.recs, r) }

// TestActuator_DenyClosedByDefault proves an Actuator built with ZERO governance config
// (the "inert executor" design) can NEVER act: every action is denied and NO write
// reaches the transport.
func TestActuator_DenyClosedByDefault(t *testing.T) {
	doer := &recordingDoer{}
	a := NewActuator(ActuatorConfig{AdminKey: "sk-ant-admin-test", Doer: doer})

	for _, call := range []func() error{
		func() error {
			return a.DeactivateKey(context.Background(), ActionDeactivateKey, "apikey_1", ActionSpec{})
		},
		func() error { return a.DeprovisionMember(context.Background(), "user_1", ActionSpec{}) },
		func() error { return a.RevokeInvite(context.Background(), "invite_1", ActionSpec{}) },
	} {
		err := call()
		var deny *AdminDenyError
		if !errors.As(err, &deny) {
			t.Fatalf("zero-config Actuator must deny with AdminDenyError, got %v", err)
		}
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("deny-closed Actuator must issue NO writes, got %d", len(doer.reqs))
	}
}

// TestActuator_AllowlistDenyNoWrite proves an action on a subject not on the allowlist is
// denied with no write, even when the gate would approve.
func TestActuator_AllowlistDenyNoWrite(t *testing.T) {
	doer := &recordingDoer{}
	aud := &capAdminAuditor{}
	a := NewActuator(ActuatorConfig{
		AdminKey:  "sk-ant-admin-test",
		Doer:      doer,
		Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionDeactivateKey, Subjects: []string{"apikey_ALLOWED"}}}),
		Gate:      stubAdminGate{status: AdminApproved, echoPlan: true},
		Auditor:   aud,
	})
	err := a.DeactivateKey(context.Background(), ActionDeactivateKey, "apikey_OTHER", ActionSpec{})
	var deny *AdminDenyError
	if !errors.As(err, &deny) {
		t.Fatalf("unlisted subject must be denied, got %v", err)
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("allowlist deny must issue NO writes, got %d", len(doer.reqs))
	}
	if len(aud.recs) != 1 || aud.recs[0].Allowed || !strings.Contains(aud.recs[0].Reason, "allowlist deny") {
		t.Fatalf("allowlist deny must be audited with the right reason, got %+v", aud.recs)
	}
}

// TestActuator_ApprovedEmptyPlanHashDenies proves the anti-TOCTOU hardening: an APPROVED
// gate that does NOT echo the plan (empty PlanHash) is refused — the connector requires
// the gate to prove it bound the exact plan, never trusting an unbound approval.
func TestActuator_ApprovedEmptyPlanHashDenies(t *testing.T) {
	doer := &recordingDoer{}
	aud := &capAdminAuditor{}
	a := NewActuator(ActuatorConfig{
		AdminKey:  "sk-ant-admin-test",
		Doer:      doer,
		Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionDeactivateKey, Subjects: []string{"*"}}}),
		Gate:      stubAdminGate{status: AdminApproved, echoPlan: false}, // approved but no plan echo
		Auditor:   aud,
	})
	if err := a.DeactivateKey(context.Background(), ActionDeactivateKey, "apikey_1", ActionSpec{}); err == nil {
		t.Fatal("an approval with empty PlanHash must be denied (anti-TOCTOU)")
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("unbound approval must issue NO writes, got %d", len(doer.reqs))
	}
	last := aud.recs[len(aud.recs)-1]
	if last.Allowed || !strings.Contains(last.Reason, "plan not bound") {
		t.Fatalf("unbound approval must be audited as plan-not-bound, got %+v", last)
	}
}

// TestActuator_GateNotApprovedNoWrite proves a non-approved gate verdict denies with no
// write and audits the refusal.
func TestActuator_GateNotApprovedNoWrite(t *testing.T) {
	doer := &recordingDoer{}
	aud := &capAdminAuditor{}
	a := NewActuator(ActuatorConfig{
		AdminKey:  "sk-ant-admin-test",
		Doer:      doer,
		Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionDeprovisionMember, Subjects: []string{"*"}}}),
		Gate:      stubAdminGate{status: AdminPending, echoPlan: true},
		Auditor:   aud,
	})
	if err := a.DeprovisionMember(context.Background(), "user_1", ActionSpec{}); err == nil {
		t.Fatal("pending gate must deny")
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("gate-not-approved must issue NO writes, got %d", len(doer.reqs))
	}
	if len(aud.recs) != 1 || aud.recs[0].Allowed {
		t.Fatalf("the refusal must be audited as not-allowed, got %+v", aud.recs)
	}
}

// TestActuator_ApprovedExecutesKeyDeactivate proves an approved + allowlisted + plan-bound
// key deactivation issues exactly the expected POST with the inactive-status body, and
// audits the execution.
func TestActuator_ApprovedExecutesKeyDeactivate(t *testing.T) {
	doer := &recordingDoer{}
	aud := &capAdminAuditor{}
	a := NewActuator(ActuatorConfig{
		AdminKey:  "sk-ant-admin-test",
		Doer:      doer,
		Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionDeactivateKey, Subjects: []string{"apikey_42"}}}),
		Gate:      stubAdminGate{status: AdminApproved, echoPlan: true},
		Auditor:   aud,
	})
	if err := a.DeactivateKey(context.Background(), ActionDeactivateKey, "apikey_42", ActionSpec{RequestedBy: "ops@corp"}); err != nil {
		t.Fatalf("approved action must execute: %v", err)
	}
	if len(doer.reqs) != 1 {
		t.Fatalf("want exactly 1 write, got %d", len(doer.reqs))
	}
	w := doer.reqs[0]
	if w.method != http.MethodPost || w.path != "/v1/organizations/api_keys/apikey_42" {
		t.Errorf("write = %s %s, want POST /v1/organizations/api_keys/apikey_42", w.method, w.path)
	}
	if !strings.Contains(w.body, `"status":"inactive"`) {
		t.Errorf("body = %s, want status:inactive", w.body)
	}
	if len(aud.recs) != 1 || !aud.recs[0].Allowed || aud.recs[0].Reason != "executed" {
		t.Fatalf("execution must be audited as allowed/executed, got %+v", aud.recs)
	}
}

// TestActuator_ArchiveKeySetsArchivedStatus proves the archive action sets status:archived.
func TestActuator_ArchiveKeySetsArchivedStatus(t *testing.T) {
	doer := &recordingDoer{}
	a := NewActuator(ActuatorConfig{
		AdminKey:  "sk-ant-admin-test",
		Doer:      doer,
		Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionArchiveKey, Subjects: []string{"*"}}}),
		Gate:      stubAdminGate{status: AdminApproved, echoPlan: true},
	})
	if err := a.DeactivateKey(context.Background(), ActionArchiveKey, "apikey_9", ActionSpec{}); err != nil {
		t.Fatalf("archive must execute: %v", err)
	}
	if !strings.Contains(doer.reqs[0].body, `"status":"archived"`) {
		t.Errorf("body = %s, want status:archived", doer.reqs[0].body)
	}
}

// TestActuator_DeprovisionExecutesDelete proves member deprovision issues a DELETE.
func TestActuator_DeprovisionExecutesDelete(t *testing.T) {
	doer := &recordingDoer{}
	a := NewActuator(ActuatorConfig{
		AdminKey:  "sk-ant-admin-test",
		Doer:      doer,
		Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionDeprovisionMember, Subjects: []string{"user_7"}}}),
		Gate:      stubAdminGate{status: AdminApproved, echoPlan: true},
	})
	if err := a.DeprovisionMember(context.Background(), "user_7", ActionSpec{}); err != nil {
		t.Fatalf("deprovision must execute: %v", err)
	}
	w := doer.reqs[0]
	if w.method != http.MethodDelete || w.path != "/v1/organizations/users/user_7" {
		t.Errorf("write = %s %s, want DELETE /v1/organizations/users/user_7", w.method, w.path)
	}
}

// TestActuator_OnboardingActionsExecuteExpectedWrites pins the onboarding Admin-API
// writes: exact method, escaped URL path, and JSON body.
func TestActuator_OnboardingActionsExecuteExpectedWrites(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		action   AdminAction
		subject  string
		gate     AdminActionGate
		call     func(*Actuator) error
		wantMeth string
		wantPath string
		wantBody string
	}{
		{
			name:    "invite",
			action:  ActionInviteMember,
			subject: "new.user+team@example.com",
			gate:    stubAdminGate{status: AdminApproved, echoPlan: true},
			call: func(a *Actuator) error {
				return a.InviteMember(ctx, " New.User+Team@Example.COM ", "developer", ActionSpec{})
			},
			wantMeth: http.MethodPost,
			wantPath: "/v1/organizations/invites",
			wantBody: `{"email":"new.user+team@example.com","role":"developer"}`,
		},
		{
			name:     "role update",
			action:   ActionUpdateMemberRole,
			subject:  "user/7",
			gate:     stubAdminGate{status: AdminApproved, echoPlan: true},
			call:     func(a *Actuator) error { return a.UpdateMemberRole(ctx, "user/7", "claude_code_user", ActionSpec{}) },
			wantMeth: http.MethodPost,
			wantPath: "/v1/organizations/users/user%2F7",
			wantBody: `{"role":"claude_code_user"}`,
		},
		{
			name:    "workspace member add",
			action:  ActionAddWorkspaceMember,
			subject: "wrk/1:user/7",
			gate:    stubAdminGate{status: AdminApproved, echoPlan: true},
			call: func(a *Actuator) error {
				return a.AddWorkspaceMember(ctx, "wrk/1", "user/7", "workspace_developer", ActionSpec{})
			},
			wantMeth: http.MethodPost,
			wantPath: "/v1/organizations/workspaces/wrk%2F1/members",
			wantBody: `{"user_id":"user/7","workspace_role":"workspace_developer"}`,
		},
		{
			name:     "workspace admin grant",
			action:   ActionGrantWorkspaceAdmin,
			subject:  "wrk/1:user/7",
			gate:     stubDualGate{status: AdminApproved, approvers: []string{"alice", "bob"}},
			call:     func(a *Actuator) error { return a.GrantWorkspaceAdmin(ctx, "wrk/1", "user/7", ActionSpec{}) },
			wantMeth: http.MethodPost,
			wantPath: "/v1/organizations/workspaces/wrk%2F1/members",
			wantBody: `{"user_id":"user/7","workspace_role":"workspace_admin"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doer := &recordingDoer{}
			a := NewActuator(ActuatorConfig{
				AdminKey:  "sk-ant-admin-test",
				Doer:      doer,
				Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: tc.action, Subjects: []string{tc.subject}}}),
				Gate:      tc.gate,
			})
			if err := tc.call(a); err != nil {
				t.Fatalf("approved action must execute: %v", err)
			}
			if len(doer.reqs) != 1 {
				t.Fatalf("want exactly 1 write, got %d", len(doer.reqs))
			}
			w := doer.reqs[0]
			if w.method != tc.wantMeth || w.path != tc.wantPath || w.body != tc.wantBody {
				t.Fatalf("write = %s %s %s, want %s %s %s", w.method, w.path, w.body, tc.wantMeth, tc.wantPath, tc.wantBody)
			}
		})
	}
}

// TestActuator_OnboardingValidationDenyNoWrite proves client-side policy validation
// happens before the transport and is reported as AdminDenyError.
func TestActuator_OnboardingValidationDenyNoWrite(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		action     AdminAction
		subject    string
		call       func(*Actuator) error
		wantReason string
	}{
		{
			name:       "invite admin role",
			action:     ActionInviteMember,
			subject:    "new@example.com",
			call:       func(a *Actuator) error { return a.InviteMember(ctx, "new@example.com", "admin", ActionSpec{}) },
			wantReason: "Console-only",
		},
		{
			name:       "role update admin role",
			action:     ActionUpdateMemberRole,
			subject:    "user_1",
			call:       func(a *Actuator) error { return a.UpdateMemberRole(ctx, "user_1", "admin", ActionSpec{}) },
			wantReason: "Console-only",
		},
		{
			name:    "workspace billing",
			action:  ActionAddWorkspaceMember,
			subject: "wrk_1:user_1",
			call: func(a *Actuator) error {
				return a.AddWorkspaceMember(ctx, "wrk_1", "user_1", "workspace_billing", ActionSpec{})
			},
			wantReason: "workspace_billing",
		},
		{
			name:    "workspace admin on single-hitl action",
			action:  ActionAddWorkspaceMember,
			subject: "wrk_1:user_1",
			call: func(a *Actuator) error {
				return a.AddWorkspaceMember(ctx, "wrk_1", "user_1", "workspace_admin", ActionSpec{})
			},
			wantReason: string(ActionGrantWorkspaceAdmin),
		},
		{
			name:       "empty email",
			action:     ActionInviteMember,
			subject:    "",
			call:       func(a *Actuator) error { return a.InviteMember(ctx, "", "user", ActionSpec{}) },
			wantReason: "email is required",
		},
		{
			name:       "email without at",
			action:     ActionInviteMember,
			subject:    "not-an-email",
			call:       func(a *Actuator) error { return a.InviteMember(ctx, "not-an-email", "user", ActionSpec{}) },
			wantReason: "must contain @",
		},
		{
			name:       "empty role-update user",
			action:     ActionUpdateMemberRole,
			subject:    "*",
			call:       func(a *Actuator) error { return a.UpdateMemberRole(ctx, "", "user", ActionSpec{}) },
			wantReason: "empty subject",
		},
		{
			name:    "empty workspace",
			action:  ActionAddWorkspaceMember,
			subject: "*",
			call: func(a *Actuator) error {
				return a.AddWorkspaceMember(ctx, "", "user_1", "workspace_user", ActionSpec{})
			},
			wantReason: "workspace id is required",
		},
		{
			name:       "empty workspace user",
			action:     ActionAddWorkspaceMember,
			subject:    "*",
			call:       func(a *Actuator) error { return a.AddWorkspaceMember(ctx, "wrk_1", "", "workspace_user", ActionSpec{}) },
			wantReason: "user id is required",
		},
		{
			name:       "empty grant workspace",
			action:     ActionGrantWorkspaceAdmin,
			subject:    "*",
			call:       func(a *Actuator) error { return a.GrantWorkspaceAdmin(ctx, "", "user_1", ActionSpec{}) },
			wantReason: "workspace id is required",
		},
		{
			name:       "empty grant user",
			action:     ActionGrantWorkspaceAdmin,
			subject:    "*",
			call:       func(a *Actuator) error { return a.GrantWorkspaceAdmin(ctx, "wrk_1", "", ActionSpec{}) },
			wantReason: "user id is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doer := &recordingDoer{}
			aud := &capAdminAuditor{}
			a := NewActuator(ActuatorConfig{
				AdminKey:  "sk-ant-admin-test",
				Doer:      doer,
				Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: tc.action, Subjects: []string{tc.subject}}}),
				Gate:      stubAdminGate{status: AdminApproved, echoPlan: true},
				Auditor:   aud,
			})
			err := tc.call(a)
			var deny *AdminDenyError
			if !errors.As(err, &deny) {
				t.Fatalf("validation must return AdminDenyError, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantReason) {
				t.Fatalf("deny reason %q must contain %q", err.Error(), tc.wantReason)
			}
			if strings.Contains(err.Error(), "sk-ant-admin-test") {
				t.Fatalf("deny error must not contain the admin key: %q", err.Error())
			}
			if len(doer.reqs) != 0 {
				t.Fatalf("validation deny must issue NO writes, got %d", len(doer.reqs))
			}
			if len(aud.recs) != 1 || aud.recs[0].Allowed {
				t.Fatalf("validation deny must be audited as denied, got %+v", aud.recs)
			}
		})
	}
}

// TestActuator_InvitePEPBehavior exercises the full PEP for one onboarding action:
// allowlist deny, pending gate deny, plan mismatch deny, and approved execution.
func TestActuator_InvitePEPBehavior(t *testing.T) {
	ctx := context.Background()
	t.Run("allowlist deny", func(t *testing.T) {
		doer := &recordingDoer{}
		aud := &capAdminAuditor{}
		a := NewActuator(ActuatorConfig{
			AdminKey:  "sk-ant-admin-test",
			Doer:      doer,
			Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionInviteMember, Subjects: []string{"allowed@example.com"}}}),
			Gate:      stubAdminGate{status: AdminApproved, echoPlan: true},
			Auditor:   aud,
		})
		err := a.InviteMember(ctx, "Other@Example.COM", "user", ActionSpec{})
		var deny *AdminDenyError
		if !errors.As(err, &deny) {
			t.Fatalf("unlisted invite must deny, got %v", err)
		}
		if len(doer.reqs) != 0 {
			t.Fatalf("allowlist deny must issue NO writes, got %d", len(doer.reqs))
		}
		if len(aud.recs) != 1 || aud.recs[0].Allowed || aud.recs[0].SubjectRef != "other@example.com" || !strings.Contains(aud.recs[0].Reason, "allowlist deny") {
			t.Fatalf("audit record must carry denied/subject/reason, got %+v", aud.recs)
		}
		if strings.Contains(fmt.Sprintf("%+v", aud.recs[0]), "sk-ant-admin-test") {
			t.Fatalf("audit record must not contain the admin key: %+v", aud.recs[0])
		}
	})

	t.Run("gate pending", func(t *testing.T) {
		doer := &recordingDoer{}
		a := NewActuator(ActuatorConfig{
			AdminKey:  "sk-ant-admin-test",
			Doer:      doer,
			Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionInviteMember, Subjects: []string{"*"}}}),
			Gate:      stubAdminGate{status: AdminPending, echoPlan: true},
		})
		if err := a.InviteMember(ctx, "new@example.com", "developer", ActionSpec{}); err == nil {
			t.Fatal("pending invite approval must deny")
		}
		if len(doer.reqs) != 0 {
			t.Fatalf("pending gate must issue NO writes, got %d", len(doer.reqs))
		}
	})

	t.Run("plan mismatch", func(t *testing.T) {
		doer := &recordingDoer{}
		a := NewActuator(ActuatorConfig{
			AdminKey:  "sk-ant-admin-test",
			Doer:      doer,
			Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionInviteMember, Subjects: []string{"*"}}}),
			Gate:      stubAdminGate{status: AdminApproved, echoPlan: true, wrongPlan: true},
		})
		if err := a.InviteMember(ctx, "new@example.com", "developer", ActionSpec{}); err == nil {
			t.Fatal("plan mismatch must deny")
		}
		if len(doer.reqs) != 0 {
			t.Fatalf("plan mismatch must issue NO writes, got %d", len(doer.reqs))
		}
	})

	t.Run("approved executes once", func(t *testing.T) {
		doer := &recordingDoer{}
		aud := &capAdminAuditor{}
		a := NewActuator(ActuatorConfig{
			AdminKey:  "sk-ant-admin-test",
			Doer:      doer,
			Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionInviteMember, Subjects: []string{"new@example.com"}}}),
			Gate:      stubAdminGate{status: AdminApproved, echoPlan: true},
			Auditor:   aud,
		})
		if err := a.InviteMember(ctx, "NEW@EXAMPLE.COM", "billing", ActionSpec{RequestedBy: "ops@corp"}); err != nil {
			t.Fatalf("approved invite must execute: %v", err)
		}
		if len(doer.reqs) != 1 {
			t.Fatalf("approved invite must execute exactly once, got %d", len(doer.reqs))
		}
		if len(aud.recs) != 1 || !aud.recs[0].Allowed || aud.recs[0].Reason != "executed" || aud.recs[0].SubjectRef != "new@example.com" {
			t.Fatalf("audit record must carry allowed/executed/subject, got %+v", aud.recs)
		}
		if strings.Contains(fmt.Sprintf("%+v", aud.recs[0]), "sk-ant-admin-test") {
			t.Fatalf("audit record must not contain the admin key: %+v", aud.recs[0])
		}
	})
}

// stubDualGate is an approval gate that returns a configurable number of distinct
// approvers, so a test can drive the connector's own dual-control re-verification.
type stubDualGate struct {
	status    AdminActionStatus
	approvers []string
}

func (g stubDualGate) Authorize(_ context.Context, req AdminActionRequest) (AdminActionDecision, error) {
	// N approvers models N distinct PEOPLE, each acting through one credential, so the
	// stub states both identities: the quorum counts people, and a fixture that set only
	// Approvers would assert that credentials count as humans.
	return AdminActionDecision{
		ApprovalRef: "appr-dual", Status: g.status, PlanHash: req.PlanHash,
		Approvers: g.approvers, ApproverPersons: g.approvers,
	}, nil
}

// TestActuator_ArchiveWorkspaceDenyClosedByDefault proves a zero-config Actuator can
// never archive a workspace (the irreversible action is inert by default like the rest).
func TestActuator_ArchiveWorkspaceDenyClosedByDefault(t *testing.T) {
	doer := &recordingDoer{}
	a := NewActuator(ActuatorConfig{AdminKey: "sk-ant-admin-test", Doer: doer})
	err := a.ArchiveWorkspace(context.Background(), "wrkspc_1", ActionSpec{})
	var deny *AdminDenyError
	if !errors.As(err, &deny) {
		t.Fatalf("zero-config archive must deny with AdminDenyError, got %v", err)
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("deny-closed archive must issue NO writes, got %d", len(doer.reqs))
	}
}

// TestActuator_ArchiveWorkspaceRequiresDualControl proves the irreversible archive is
// REFUSED when the gate approves with only ONE distinct approver — the connector
// re-verifies the two-person quorum itself and never trusts a single-approval gate.
func TestActuator_ArchiveWorkspaceRequiresDualControl(t *testing.T) {
	doer := &recordingDoer{}
	aud := &capAdminAuditor{}
	a := NewActuator(ActuatorConfig{
		AdminKey:  "sk-ant-admin-test",
		Doer:      doer,
		Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionArchiveWorkspace, Subjects: []string{"*"}}}),
		Gate:      stubDualGate{status: AdminApproved, approvers: []string{"alice", "alice"}}, // one DISTINCT
		Auditor:   aud,
	})
	err := a.ArchiveWorkspace(context.Background(), "wrkspc_1", ActionSpec{})
	var deny *AdminDenyError
	if !errors.As(err, &deny) {
		t.Fatalf("single-approver archive must deny (dual-control), got %v", err)
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("dual-control deny must issue NO writes, got %d", len(doer.reqs))
	}
	last := aud.recs[len(aud.recs)-1]
	if last.Allowed || !strings.Contains(last.Reason, "dual-control not satisfied") {
		t.Fatalf("must audit dual-control deny, got %+v", last)
	}
	if last.DualControl || last.ApproverCount != 1 {
		t.Fatalf("record must show dual_control=false approvers=1, got %+v", last)
	}
}

// TestActuator_GrantWorkspaceAdminRequiresDualControl proves workspace-admin grant is
// recoverable but privilege-critical: the action identity forces two distinct approvers.
func TestActuator_GrantWorkspaceAdminRequiresDualControl(t *testing.T) {
	t.Run("one distinct approver denies", func(t *testing.T) {
		doer := &recordingDoer{}
		aud := &capAdminAuditor{}
		a := NewActuator(ActuatorConfig{
			AdminKey:  "sk-ant-admin-test",
			Doer:      doer,
			Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionGrantWorkspaceAdmin, Subjects: []string{"*"}}}),
			Gate:      stubDualGate{status: AdminApproved, approvers: []string{"alice", " ", "alice", ""}},
			Auditor:   aud,
		})
		err := a.GrantWorkspaceAdmin(context.Background(), "wrkspc_1", "user_1", ActionSpec{})
		var deny *AdminDenyError
		if !errors.As(err, &deny) {
			t.Fatalf("single-approver grant must deny (dual-control), got %v", err)
		}
		if len(doer.reqs) != 0 {
			t.Fatalf("dual-control deny must issue NO writes, got %d", len(doer.reqs))
		}
		last := aud.recs[len(aud.recs)-1]
		if last.Allowed || last.DualControl || last.ApproverCount != 1 || !strings.Contains(last.Reason, "dual-control not satisfied") {
			t.Fatalf("must audit one distinct approver and deny, got %+v", last)
		}
	})

	t.Run("two distinct approvers executes", func(t *testing.T) {
		doer := &recordingDoer{}
		aud := &capAdminAuditor{}
		a := NewActuator(ActuatorConfig{
			AdminKey:  "sk-ant-admin-test",
			Doer:      doer,
			Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionGrantWorkspaceAdmin, Subjects: []string{"wrkspc_1:user_1"}}}),
			Gate:      stubDualGate{status: AdminApproved, approvers: []string{"alice", "", "bob", "alice"}},
			Auditor:   aud,
		})
		if err := a.GrantWorkspaceAdmin(context.Background(), "wrkspc_1", "user_1", ActionSpec{}); err != nil {
			t.Fatalf("two-approver grant must execute: %v", err)
		}
		if len(doer.reqs) != 1 {
			t.Fatalf("want exactly 1 write, got %d", len(doer.reqs))
		}
		last := aud.recs[len(aud.recs)-1]
		if !last.Allowed || !last.DualControl || last.ApproverCount != 2 || last.Reason != "executed" {
			t.Fatalf("execution must audit allowed/dual_control/2, got %+v", last)
		}
	})
}

// TestActuator_ArchiveWorkspaceExecutesWithDualControl proves an approved, allowlisted,
// plan-bound archive with TWO distinct approvers issues exactly the archive POST and
// audits the dual-control execution.
func TestActuator_ArchiveWorkspaceExecutesWithDualControl(t *testing.T) {
	doer := &recordingDoer{}
	aud := &capAdminAuditor{}
	a := NewActuator(ActuatorConfig{
		AdminKey:  "sk-ant-admin-test",
		Doer:      doer,
		Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionArchiveWorkspace, Subjects: []string{"wrkspc_42"}}}),
		Gate:      stubDualGate{status: AdminApproved, approvers: []string{"alice", "bob"}},
		Auditor:   aud,
	})
	if err := a.ArchiveWorkspace(context.Background(), "wrkspc_42", ActionSpec{RequestedBy: "ops@corp"}); err != nil {
		t.Fatalf("dual-control archive must execute: %v", err)
	}
	if len(doer.reqs) != 1 {
		t.Fatalf("want exactly 1 write, got %d", len(doer.reqs))
	}
	w := doer.reqs[0]
	if w.method != http.MethodPost || w.path != "/v1/organizations/workspaces/wrkspc_42/archive" {
		t.Errorf("write = %s %s, want POST /v1/organizations/workspaces/wrkspc_42/archive", w.method, w.path)
	}
	if w.body != "" {
		t.Errorf("archive body = %q, want empty (no body)", w.body)
	}
	last := aud.recs[len(aud.recs)-1]
	if !last.Allowed || last.Reason != "executed" || !last.DualControl || last.ApproverCount != 2 {
		t.Fatalf("execution must audit allowed/executed/dual_control/2, got %+v", last)
	}
}

// TestActuator_ArchiveWorkspaceAllowlistDeny proves an unlisted workspace is denied with
// no write even with a valid dual-control approval.
func TestActuator_ArchiveWorkspaceAllowlistDeny(t *testing.T) {
	doer := &recordingDoer{}
	a := NewActuator(ActuatorConfig{
		AdminKey:  "sk-ant-admin-test",
		Doer:      doer,
		Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionArchiveWorkspace, Subjects: []string{"wrkspc_ALLOWED"}}}),
		Gate:      stubDualGate{status: AdminApproved, approvers: []string{"alice", "bob"}},
	})
	err := a.ArchiveWorkspace(context.Background(), "wrkspc_OTHER", ActionSpec{})
	var deny *AdminDenyError
	if !errors.As(err, &deny) {
		t.Fatalf("unlisted workspace must be denied, got %v", err)
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("allowlist deny must issue NO writes, got %d", len(doer.reqs))
	}
}

// TestActuator_EmptySubjectDenies proves an empty subject ref is denied (before the
// allowlist/gate) with no write, audited as "empty subject".
func TestActuator_EmptySubjectDenies(t *testing.T) {
	doer := &recordingDoer{}
	aud := &capAdminAuditor{}
	a := NewActuator(ActuatorConfig{
		AdminKey:  "sk-ant-admin-test",
		Doer:      doer,
		Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionDeactivateKey, Subjects: []string{"*"}}}),
		Gate:      stubAdminGate{status: AdminApproved, echoPlan: true},
		Auditor:   aud,
	})
	err := a.DeactivateKey(context.Background(), ActionDeactivateKey, "", ActionSpec{})
	var deny *AdminDenyError
	if !errors.As(err, &deny) {
		t.Fatalf("empty subject must deny with AdminDenyError, got %v", err)
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("empty subject must issue NO writes, got %d", len(doer.reqs))
	}
	if len(aud.recs) != 1 || aud.recs[0].Allowed || !strings.Contains(aud.recs[0].Reason, "empty subject") {
		t.Fatalf("empty subject must be audited, got %+v", aud.recs)
	}
}

// TestActuator_ExecutionFailureSurfaced proves a transport failure on an APPROVED action
// surfaces as a NON-policy error (not AdminDenyError), the write WAS attempted, and the
// decision is audited as allowed (governance approved it) with the "execution failed"
// reason — so a failed cut is never silently swallowed nor mistaken for a policy denial.
func TestActuator_ExecutionFailureSurfaced(t *testing.T) {
	doer := &recordingDoer{status: http.StatusInternalServerError}
	aud := &capAdminAuditor{}
	a := NewActuator(ActuatorConfig{
		AdminKey:  "sk-ant-admin-test",
		Doer:      doer,
		Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionDeactivateKey, Subjects: []string{"*"}}}),
		Gate:      stubAdminGate{status: AdminApproved, echoPlan: true},
		Auditor:   aud,
	})
	err := a.DeactivateKey(context.Background(), ActionDeactivateKey, "apikey_1", ActionSpec{})
	if err == nil {
		t.Fatal("a 5xx execution must surface an error")
	}
	var deny *AdminDenyError
	if errors.As(err, &deny) {
		t.Fatalf("a transport failure must NOT be an AdminDenyError (policy), got %v", err)
	}
	if len(doer.reqs) != 1 {
		t.Fatalf("the write must have been attempted exactly once, got %d", len(doer.reqs))
	}
	last := aud.recs[len(aud.recs)-1]
	if !last.Allowed || !strings.Contains(last.Reason, "execution failed") {
		t.Fatalf("must audit allowed=true with 'execution failed', got %+v", last)
	}
}

// TestActuator_ArchiveWorkspaceExecFailureSurfaced proves the irreversible archive's
// transport-failure path: a 5xx on the archive POST surfaces a NON-policy error, the write
// was attempted once, and the decision is audited allowed=true / dual_control=true with the
// "execution failed" reason (a failed nuclear cut is never silently swallowed).
func TestActuator_ArchiveWorkspaceExecFailureSurfaced(t *testing.T) {
	doer := &recordingDoer{status: http.StatusBadGateway}
	aud := &capAdminAuditor{}
	a := NewActuator(ActuatorConfig{
		AdminKey:  "sk-ant-admin-test",
		Doer:      doer,
		Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionArchiveWorkspace, Subjects: []string{"*"}}}),
		Gate:      stubDualGate{status: AdminApproved, approvers: []string{"alice", "bob"}},
		Auditor:   aud,
	})
	err := a.ArchiveWorkspace(context.Background(), "wrkspc_1", ActionSpec{})
	if err == nil {
		t.Fatal("a 5xx archive must surface an error")
	}
	var deny *AdminDenyError
	if errors.As(err, &deny) {
		t.Fatalf("a transport failure must NOT be an AdminDenyError, got %v", err)
	}
	if len(doer.reqs) != 1 {
		t.Fatalf("the archive must be attempted exactly once, got %d", len(doer.reqs))
	}
	last := aud.recs[len(aud.recs)-1]
	if !last.Allowed || !last.DualControl || last.ApproverCount != 2 || !strings.Contains(last.Reason, "execution failed") {
		t.Fatalf("must audit allowed/dual_control/2/'execution failed', got %+v", last)
	}
}

// TestActuator_IrreversibleDualControlDerivedFromAction proves the defense-in-depth
// hardening: dual-control for an irreversible action is enforced from the ACTION IDENTITY.
func TestActuator_IrreversibleDualControlDerivedFromAction(t *testing.T) {
	doer := &recordingDoer{}
	a := NewActuator(ActuatorConfig{
		AdminKey:  "sk-ant-admin-test",
		Doer:      doer,
		Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionArchiveWorkspace, Subjects: []string{"*"}}}),
		Gate:      stubDualGate{status: AdminApproved, approvers: []string{"solo"}}, // one distinct
	})
	executed := false
	err := a.actuate(context.Background(), governedAction{
		action: ActionArchiveWorkspace, subjectKind: "workspace", subjectRef: "wrkspc_x",
		spec: ActionSpec{}, paramsHash: hashAdminParams("archive_workspace"),
		exec: func(context.Context) error { executed = true; return nil },
	})
	var deny *AdminDenyError
	if !errors.As(err, &deny) {
		t.Fatalf("irreversible action with one approver must deny, got %v", err)
	}
	if executed || len(doer.reqs) != 0 {
		t.Fatal("the irreversible action must NOT execute on a single approver")
	}
}

// TestActuator_PlanHashSeparatesWorkspaceMemberAddAndAdminGrant proves the plan identity
// changes between the single-HITL add action and the privilege-granting admin action for
// the same workspace:user subject.
func TestActuator_PlanHashSeparatesWorkspaceMemberAddAndAdminGrant(t *testing.T) {
	subject := "wrkspc_1:user_1"
	add := AdminPlanHash(ActionAddWorkspaceMember, "workspace_member", subject, hashAdminParams("workspace_role=workspace_user"))
	grant := AdminPlanHash(ActionGrantWorkspaceAdmin, "workspace_member", subject, hashAdminParams("workspace_role=workspace_admin"))
	if add == grant {
		t.Fatal("workspace member add and workspace-admin grant must have different plan hashes")
	}
}

// TestActuator_PlanMismatchDenies proves an approval bound to a DIFFERENT plan (anti-
// TOCTOU) is refused with no write.
func TestActuator_PlanMismatchDenies(t *testing.T) {
	doer := &recordingDoer{}
	a := NewActuator(ActuatorConfig{
		AdminKey:  "sk-ant-admin-test",
		Doer:      doer,
		Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionRevokeInvite, Subjects: []string{"*"}}}),
		Gate:      stubAdminGate{status: AdminApproved, echoPlan: true, wrongPlan: true},
	})
	if err := a.RevokeInvite(context.Background(), "invite_1", ActionSpec{}); err == nil {
		t.Fatal("plan mismatch must deny")
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("plan mismatch must issue NO writes, got %d", len(doer.reqs))
	}
}

// TestActuator_GateErrorFailsClosed proves any gate error denies (fail-closed).
func TestActuator_GateErrorFailsClosed(t *testing.T) {
	doer := &recordingDoer{}
	a := NewActuator(ActuatorConfig{
		AdminKey:  "sk-ant-admin-test",
		Doer:      doer,
		Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionDeprovisionMember, Subjects: []string{"*"}}}),
		Gate:      stubAdminGate{err: errors.New("bridge down")},
	})
	if err := a.DeprovisionMember(context.Background(), "user_1", ActionSpec{}); err == nil {
		t.Fatal("gate error must deny (fail-closed)")
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("gate error must issue NO writes, got %d", len(doer.reqs))
	}
}

// TestAdminActionAllowlist_DenyByDefault proves an empty/nil allowlist denies everything
// and the subject dimension is least-privilege (an empty subject set authorizes nothing).
func TestAdminActionAllowlist_DenyByDefault(t *testing.T) {
	if (*AdminActionAllowlist)(nil).Allowed(ActionDeactivateKey, "apikey_1") {
		t.Error("nil allowlist must deny")
	}
	empty := NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionDeactivateKey, Subjects: nil}})
	if empty.Allowed(ActionDeactivateKey, "apikey_1") {
		t.Error("a rule with no subjects must authorize nothing")
	}
	wild := NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionDeactivateKey, Subjects: []string{"*"}}})
	if !wild.Allowed(ActionDeactivateKey, "apikey_1") {
		t.Error(`"*" must grant any subject of that action`)
	}
	if wild.Allowed(ActionDeprovisionMember, "apikey_1") {
		t.Error("a key-action rule must not authorize a member action")
	}
}

// TestActuator_GroupMembershipExecutesWithBetaHeader proves the RBAC group
// membership writes issue the exact POST/DELETE, carry the ce-user-management beta
// header (the endpoint 404s without it), and — being recoverable — execute on a
// SINGLE approval (they are not in the dual-control set).
func TestActuator_GroupMembershipExecutesWithBetaHeader(t *testing.T) {
	ctx := context.Background()
	t.Run("add", func(t *testing.T) {
		doer := &recordingDoer{}
		a := NewActuator(ActuatorConfig{
			AdminKey:  "sk-ant-admin-test",
			Doer:      doer,
			Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionAddGroupMember, Subjects: []string{"rbac_group_1:user/7"}}}),
			Gate:      stubAdminGate{status: AdminApproved, echoPlan: true}, // single approval
		})
		if err := a.AddGroupMember(ctx, "rbac_group_1", "user/7", ActionSpec{}); err != nil {
			t.Fatalf("approved add must execute: %v", err)
		}
		if len(doer.reqs) != 1 {
			t.Fatalf("want exactly 1 write, got %d", len(doer.reqs))
		}
		w := doer.reqs[0]
		if w.method != http.MethodPost || w.path != "/v1/organizations/rbac_groups/rbac_group_1/members" {
			t.Errorf("write = %s %s, want POST /v1/organizations/rbac_groups/rbac_group_1/members", w.method, w.path)
		}
		if w.body != `{"user_id":"user/7"}` {
			t.Errorf("body = %s, want {\"user_id\":\"user/7\"}", w.body)
		}
		if w.beta != "ce-user-management-2026-07-13" {
			t.Errorf("beta header = %q, want ce-user-management-2026-07-13", w.beta)
		}
	})

	t.Run("remove", func(t *testing.T) {
		doer := &recordingDoer{}
		a := NewActuator(ActuatorConfig{
			AdminKey:  "sk-ant-admin-test",
			Doer:      doer,
			Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionRemoveGroupMember, Subjects: []string{"*"}}}),
			Gate:      stubAdminGate{status: AdminApproved, echoPlan: true},
		})
		if err := a.RemoveGroupMember(ctx, "rbac_group_1", "user/7", ActionSpec{}); err != nil {
			t.Fatalf("approved remove must execute: %v", err)
		}
		w := doer.reqs[0]
		if w.method != http.MethodDelete || w.path != "/v1/organizations/rbac_groups/rbac_group_1/members/user%2F7" {
			t.Errorf("write = %s %s, want DELETE .../members/user%%2F7", w.method, w.path)
		}
		if w.beta != "ce-user-management-2026-07-13" {
			t.Errorf("beta header = %q, want ce-user-management-2026-07-13", w.beta)
		}
	})
}

// TestActuator_AddGroupMemberDualControlLever proves the defense-in-depth fix: a
// group named in DualControlGroupIDs escalates add_group_member to dual-control
// (re-verified by the connector — a single approver is refused), while a group NOT named
// stays single-HITL. Closes the asymmetry with GrantWorkspaceAdmin for high-privilege
// (org-wide-role-bound) groups.
func TestActuator_AddGroupMemberDualControlLever(t *testing.T) {
	ctx := context.Background()
	newAct := func(doer *recordingDoer, gate AdminActionGate, aud *capAdminAuditor) *Actuator {
		return NewActuator(ActuatorConfig{
			AdminKey:            "sk-ant-admin-test",
			Doer:                doer,
			Allowlist:           NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionAddGroupMember, Subjects: []string{"*"}}}),
			Gate:                gate,
			Auditor:             aud,
			DualControlGroupIDs: []string{"rbac_group_admins"},
		})
	}

	t.Run("dual-control group with one approver denies", func(t *testing.T) {
		doer := &recordingDoer{}
		aud := &capAdminAuditor{}
		a := newAct(doer, stubDualGate{status: AdminApproved, approvers: []string{"alice"}}, aud)
		err := a.AddGroupMember(ctx, "rbac_group_admins", "user_1", ActionSpec{})
		var deny *AdminDenyError
		if !errors.As(err, &deny) {
			t.Fatalf("single-approver add to a dual-control group must deny, got %v", err)
		}
		if len(doer.reqs) != 0 {
			t.Fatalf("dual-control deny must issue NO writes, got %d", len(doer.reqs))
		}
		last := aud.recs[len(aud.recs)-1]
		if last.Allowed || !strings.Contains(last.Reason, "dual-control not satisfied") {
			t.Fatalf("must audit dual-control deny, got %+v", last)
		}
	})

	t.Run("dual-control group with two approvers executes", func(t *testing.T) {
		doer := &recordingDoer{}
		a := newAct(doer, stubDualGate{status: AdminApproved, approvers: []string{"alice", "bob"}}, &capAdminAuditor{})
		if err := a.AddGroupMember(ctx, "rbac_group_admins", "user_1", ActionSpec{}); err != nil {
			t.Fatalf("two-approver add to a dual-control group must execute: %v", err)
		}
		if len(doer.reqs) != 1 {
			t.Fatalf("want exactly 1 write, got %d", len(doer.reqs))
		}
	})

	t.Run("non-listed group stays single-HITL", func(t *testing.T) {
		doer := &recordingDoer{}
		// A single-approver gate; the group is NOT in DualControlGroupIDs, so one approval executes.
		a := newAct(doer, stubDualGate{status: AdminApproved, approvers: []string{"alice"}}, &capAdminAuditor{})
		if err := a.AddGroupMember(ctx, "rbac_group_ordinary", "user_1", ActionSpec{}); err != nil {
			t.Fatalf("single-approver add to an ordinary group must execute: %v", err)
		}
		if len(doer.reqs) != 1 {
			t.Fatalf("ordinary group add must execute on one approval, got %d writes", len(doer.reqs))
		}
	})
}

// TestActuator_GroupMembershipDenyClosed proves the group writes are inert by default
// (zero governance config → deny, no write) exactly like every other governed action.
func TestActuator_GroupMembershipDenyClosed(t *testing.T) {
	ctx := context.Background()
	doer := &recordingDoer{}
	a := NewActuator(ActuatorConfig{AdminKey: "sk-ant-admin-test", Doer: doer})
	for _, call := range []func() error{
		func() error { return a.AddGroupMember(ctx, "rbac_group_1", "user_1", ActionSpec{}) },
		func() error { return a.RemoveGroupMember(ctx, "rbac_group_1", "user_1", ActionSpec{}) },
	} {
		var deny *AdminDenyError
		if err := call(); !errors.As(err, &deny) {
			t.Fatalf("zero-config group action must deny with AdminDenyError, got %v", err)
		}
	}
	if len(doer.reqs) != 0 {
		t.Fatalf("deny-closed group actions must issue NO writes, got %d", len(doer.reqs))
	}
}

// TestActuator_GroupMemberValidationDeny proves empty group/user is refused before any
// HTTP call, audited, with no write.
func TestActuator_GroupMemberValidationDeny(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		call       func(*Actuator) error
		wantReason string
	}{
		{"empty group", func(a *Actuator) error { return a.AddGroupMember(ctx, "", "user_1", ActionSpec{}) }, "group id is required"},
		{"empty user", func(a *Actuator) error { return a.AddGroupMember(ctx, "rbac_group_1", "", ActionSpec{}) }, "user id is required"},
		{"empty group remove", func(a *Actuator) error { return a.RemoveGroupMember(ctx, "", "user_1", ActionSpec{}) }, "group id is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doer := &recordingDoer{}
			a := NewActuator(ActuatorConfig{
				AdminKey:  "sk-ant-admin-test",
				Doer:      doer,
				Allowlist: NewAdminActionAllowlist([]AdminAllowRule{{Action: ActionAddGroupMember, Subjects: []string{"*"}}, {Action: ActionRemoveGroupMember, Subjects: []string{"*"}}}),
				Gate:      stubAdminGate{status: AdminApproved, echoPlan: true},
			})
			err := tc.call(a)
			var deny *AdminDenyError
			if !errors.As(err, &deny) {
				t.Fatalf("validation must return AdminDenyError, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantReason) {
				t.Fatalf("deny reason %q must contain %q", err.Error(), tc.wantReason)
			}
			if len(doer.reqs) != 0 {
				t.Fatalf("validation deny must issue NO writes, got %d", len(doer.reqs))
			}
		})
	}
}

// TestValidateOrgMemberRole_CurrencyEnum pins the role-enum currency: "managed" is
// now assignable, and the admin-tier roles (admin/owner/membership_admin/primary_owner)
// are all refused as Console-only.
func TestValidateOrgMemberRole_CurrencyEnum(t *testing.T) {
	for _, ok := range []string{"user", "developer", "billing", "claude_code_user", "managed"} {
		if reason := validateOrgMemberRole(ok); reason != "" {
			t.Errorf("role %q must be assignable, got deny %q", ok, reason)
		}
	}
	for _, refused := range []string{"admin", "owner", "membership_admin", "primary_owner"} {
		reason := validateOrgMemberRole(refused)
		if reason == "" || !strings.Contains(reason, "Console-only") {
			t.Errorf("admin-tier role %q must be refused as Console-only, got %q", refused, reason)
		}
	}
}
