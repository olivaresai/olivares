// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file is the GOVERNED ADMIN-ACTION surface (resource-mgmt actuation): the
// deny-by-default Policy-Enforcement-Point in front of the few Admin-API WRITES the
// control plane needs to ACT on an org — deactivate/archive an API key, invite or
// deprovision an org member, update member roles, revoke a pending invite, add workspace
// members, grant workspace admin, archive a workspace (the irreversible FinOps
// spend backstop), and add/remove a member to/from a Claude Enterprise RBAC group
// (the ce-user-management beta — managing group membership manages the group's custom-role
// grants). It mirrors the A2A delegation PEP (connectors/
// a2a/pep.go + delegate.go) exactly, because the security property is identical: an
// actuation is the exception to read-first (docs/SECURITY-HARDENING.md), and EVERY actuation goes
// through a deny-closed gate.
//
// The read connector (Source) stays read-only by construction; the Actuator here is a
// SEPARATE governed client the composition root (cmd, AGPL) constructs and wires — so
// no source poll can ever mutate the org. Each action passes, in order:
//
//	allowlist (deny-by-default (action, subject))
//	  → PlanHash (anti-TOCTOU binding of action+subject+params)
//	  → ApprovalGate (HITL seam, deny-closed)
//	  → execute one POST/DELETE
//	  → audit the decision (allow or deny)
//
// INERT BY DEFAULT (by design): NewActuator defaults the allowlist to
// deny-all, the gate to the deny-closed denyAdminGate, and the auditor to a no-op, so an
// Actuator built with zero governance config can NEVER act. The real bridge + the
// Admin credential are wired at cmd; in-tree the seam is deny-closed and the executor
// fires only behind an approved, plan-bound decision.
//
// Three approval groups, by blast radius:
//   - RECOVERABLE actions (deactivate/archive a key, deprovision a member, revoke an
//     invite, invite a member, update a member role, add a non-admin workspace member)
//     take SINGLE HITL approval — they can be undone.
//   - PRIVILEGE-GRANTING actions (grant workspace admin) are recoverable, but take
//     DUAL-CONTROL because workspace-admin has high blast radius.
//   - IRREVERSIBLE actions (archive a workspace — it revokes EVERY key in the workspace
//     and cannot be undone) also take DUAL-CONTROL: the gate must return ≥2 distinct
//     approvers and this connector RE-VERIFIES the quorum itself (defense in depth),
//     exactly like the RTBF content DELETE in claude-compliance. The connector never
//     trusts the gate to have enforced the second human.
//
// Boundary (LICENSING.md): Apache-2.0, /sdk + stdlib only — the ApprovalGate and Auditor
// are SEAMS the AGPL composition root binds to the real bridge + hash-chained
// ledger. Minimal data (docs/SECURITY-HARDENING.md): the PEP reasons over references (key id / user id /
// invite email / invite id / workspace id / workspace:user pair); the Admin credential is
// an out-of-band header, never in the audit record.
//
// Authority (jun-2026, verified against platform.claude.com): Admin API does NOT mint
// keys (Console-only) and has NO rotate endpoint — rotation = create-new (Console) then
// deactivate-old; the deactivate step (POST /v1/organizations/api_keys/{id}
// {status:inactive|archived}) is the part the API governs, so that is what this seam
// actuates. Org admins CANNOT be removed via the API (a 403 the executor surfaces
// honestly, never papers over). For WORKSPACES, the Admin API exposes create/get/list/
// update/archive — but there is NO endpoint to SET or CLEAR a workspace SPEND LIMIT
// (Update Workspace accepts only data_residency/external_key_id/name/tags; spend limits
// are Console-only and the Rate Limits API is read-only). So the only API-actuable
// workspace-level hard cap is ARCHIVE (POST /v1/organizations/workspaces/{id}/archive),
// which revokes all keys in the workspace — the FinOps defense-in-depth backstop
//. The Source surfaces the spend-limit gap as a posture finding
// (governance.go); this actuator never POSTs to an endpoint that does not exist.
//
// ONBOARDING writes (verified against platform.claude.com on 2026-07-05): create invite
// is POST /v1/organizations/invites with {email, role}; update user role is
// POST /v1/organizations/users/{user_id} with {role}; create workspace member is
// POST /v1/organizations/workspaces/{workspace_id}/members with {user_id,
// workspace_role}. The Admin API refuses org admin grants and workspace_billing; those
// are client-side policy denials here before any HTTP call. Although current docs examples
// show OAuth bearer transport, this connector intentionally keeps the existing
// sk-ant-admin x-api-key Admin credential transport in do().
package claudeapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// AdminAction is one governed Admin-API write. The set is deliberately small and
// least-privilege; an unknown action is never executable.
type AdminAction string

const (
	// ActionDeactivateKey sets an API key's status to inactive — the API-governable
	// step of a key rotation (creation is Console-only). Recoverable (re-activate).
	ActionDeactivateKey AdminAction = "deactivate_key"
	// ActionArchiveKey sets an API key's status to archived — the terminal lifecycle
	// state of a rotated-out key.
	ActionArchiveKey AdminAction = "archive_key"
	// ActionDeprovisionMember removes an org member (DELETE /users/{id}). Recoverable
	// via re-invite. Org admins cannot be removed via the API (the executor surfaces
	// the provider's 403 honestly).
	ActionDeprovisionMember AdminAction = "deprovision_member"
	// ActionRevokeInvite revokes a pending org invite (DELETE /invites/{id}).
	ActionRevokeInvite AdminAction = "revoke_invite"
	// ActionInviteMember creates a pending org invite. Recoverable (the invite can be
	// revoked) and therefore SINGLE HITL. The invitee email is the approval subject so
	// the human sees WHO is being invited.
	ActionInviteMember AdminAction = "invite_member"
	// ActionUpdateMemberRole updates an org member role. Recoverable (the role can be
	// changed back) and therefore SINGLE HITL. The API refuses admin grants, so the
	// maximum grantable role here is developer-tier.
	ActionUpdateMemberRole AdminAction = "update_member_role"
	// ActionAddWorkspaceMember adds a non-admin workspace member. Recoverable and
	// therefore SINGLE HITL. workspace_admin is refused here; use
	// ActionGrantWorkspaceAdmin for the privilege-granting dual-control action.
	ActionAddWorkspaceMember AdminAction = "add_workspace_member"
	// ActionGrantWorkspaceAdmin adds a workspace member with workspace_admin. This is
	// recoverable, so actionIsIrreversible deliberately stays false for it, but it is
	// privilege-granting (workspace-admin blast radius) and therefore DUAL-CONTROL
	// (≥2 distinct approvers, re-verified here).
	ActionGrantWorkspaceAdmin AdminAction = "grant_workspace_admin"
	// ActionArchiveWorkspace archives a workspace (POST /workspaces/{id}/archive). It
	// IMMEDIATELY revokes every API key scoped to that workspace and CANNOT be undone —
	// the only API-actuable workspace-level hard cap (there is no spend-limit SET
	// endpoint). Because it is irreversible it takes DUAL-CONTROL (≥2 distinct approvers,
	// re-verified here), not the single approval the recoverable actions take.
	ActionArchiveWorkspace AdminAction = "archive_workspace"
	// ActionAddGroupMember adds an org member to a Claude Enterprise RBAC group
	// (POST /v1/organizations/rbac_groups/{id}/members, ce-user-management beta). Adding
	// a member grants them the group's custom-role bindings, so it is privilege-affecting.
	// It is recoverable (remove them), so by default it takes SINGLE HITL like
	// AddWorkspaceMember. BUT an RBAC group bound to an org-wide custom role can carry
	// blast radius ≥ the workspace-scoped workspace_admin grant that IS dual-controlled;
	// because the connector cannot introspect a group's role set here, the operator names
	// such groups in ActuatorConfig.DualControlGroupIDs, and this action is then escalated
	// to DUAL-CONTROL re-verified BY THE CONNECTOR (not merely trusted to the gate) — the
	// same defense-in-depth as GrantWorkspaceAdmin. The human approves the exact
	// group:user pair..
	ActionAddGroupMember AdminAction = "add_group_member"
	// ActionRemoveGroupMember removes an org member from an RBAC group
	// (DELETE /v1/organizations/rbac_groups/{id}/members/{user_id}, ce-user-management
	// beta). It is de-privileging and recoverable (re-add), so SINGLE HITL..
	ActionRemoveGroupMember AdminAction = "remove_group_member"
)

// AdminActionGate is the governance HITL seam for an Admin-API action. The real adapter
// (cmd/olivares) bridges to the ApprovalGate (POST /v1/m/governance/approvals,
// bound to the PlanHash). The Actuator never decides — it asks and consumes. Recoverable
// actions use single approval; privilege-granting and irreversible actions carry
// dual-control evidence.
type AdminActionGate interface {
	Authorize(ctx context.Context, req AdminActionRequest) (AdminActionDecision, error)
}

// AdminActionStatus is the effective gate verdict; every value except StatusApproved
// is a DENY (mirrors the A2A/orchestration gate vocabulary so the cmd bridge maps 1:1).
type AdminActionStatus string

const (
	AdminApproved AdminActionStatus = "approved"
	AdminPending  AdminActionStatus = "pending"
	AdminRejected AdminActionStatus = "rejected"
	AdminExpired  AdminActionStatus = "expired"
	AdminNoGate   AdminActionStatus = "no_gate"
)

// AdminActionRequest is the minimal-data description of a prospective action the gate
// authorizes. PlanHash binds it to the exact (action, subject, params) tuple a human
// saw (anti-TOCTOU); RequestedBy is the audit actor. It carries NO credential.
type AdminActionRequest struct {
	Tenant      string
	Action      AdminAction
	SubjectKind string // "api_key" | "org_member" | "org_invite" | "workspace" | "workspace_member"
	SubjectRef  string // the key/user/invite/workspace ref or exact workspace:user pair being acted on
	PlanHash    string
	RequestedBy string
}

// AdminActionDecision is the gate's answer. Allowed() is the ONLY authorization; the
// zero value (empty status) is a deny.
type AdminActionDecision struct {
	ApprovalRef string
	Status      AdminActionStatus
	PlanHash    string // the plan the approval was bound to, echoed for confirmation
	// Approvers are the CREDENTIALS that approved — the audit provenance. They are NOT
	// the quorum: an audit-actor string identifies a credential, not a human, and one
	// human holding a session and a token contributes two of them.
	Approvers []string
	// ApproverPersons are the DISTINCT PEOPLE who approved — the dual-control evidence
	// an irreversible action (archive_workspace) or privilege-granting action
	// (grant_workspace_admin) requires, and the only list this connector counts. The
	// actuator re-verifies ≥2 distinct entries for such an action (defense in depth: it
	// never trusts the gate to have enforced the quorum). Single-HITL recoverable
	// actions ignore this field — a single approval (Status==approved) authorizes them.
	// A credential with no person behind it is absent from this list by construction.
	ApproverPersons []string
}

// Allowed reports whether the decision authorizes the action — true ONLY for an
// explicit approval. The empty/zero value is a deny. (For dual-control actions the
// actuator ALSO requires HasDualControl(); see actuate.)
func (d AdminActionDecision) Allowed() bool { return d.Status == AdminApproved }

// distinctApprovers counts the distinct, non-empty approving PEOPLE — never the
// credentials. Duplicates and blanks never inflate the count, and counting Approvers
// here would let one human with two credentials satisfy two-person control, which is the
// whole thing this quorum exists to prevent.
func (d AdminActionDecision) distinctApprovers() int {
	seen := make(map[string]struct{}, len(d.ApproverPersons))
	for _, a := range d.ApproverPersons {
		a = strings.TrimSpace(a)
		if a != "" {
			seen[a] = struct{}{}
		}
	}
	return len(seen)
}

// HasDualControl reports whether the decision satisfies two-person control (≥2 distinct
// approvers). It is independent of Status so the audit can distinguish "not approved"
// from "approved but only one approver".
func (d AdminActionDecision) HasDualControl() bool { return d.distinctApprovers() >= 2 }

// denyAdminGate is the deny-closed default: with no gate wired, every action is denied
// with an explicit no_gate decision (never a silent no-op).
type denyAdminGate struct{}

func (denyAdminGate) Authorize(_ context.Context, req AdminActionRequest) (AdminActionDecision, error) {
	return AdminActionDecision{ApprovalRef: "no-gate:" + req.PlanHash, Status: AdminNoGate, PlanHash: req.PlanHash}, nil
}

// AdminAllowRule grants the right to perform one AdminAction, restricted to the listed
// Subjects. Subjects are matched EXACTLY (you must name the key/user/invite or exact
// workspace:user pair you act on — the core of least-privilege); "*" grants any subject
// of that action's kind. An empty Subjects set authorizes NOTHING (deny-by-default down to
// the subject dimension).
type AdminAllowRule struct {
	Action   AdminAction `json:"action"`
	Subjects []string    `json:"subjects"`
}

// AdminActionAllowlist is the deny-by-default set of permitted (action, subject) tuples.
// An empty/nil allowlist denies everything — there is no "allow all" mode.
type AdminActionAllowlist struct {
	rules []AdminAllowRule
}

// NewAdminActionAllowlist builds an allowlist from operator rules (a copy is taken so
// the caller cannot mutate policy after construction).
func NewAdminActionAllowlist(rules []AdminAllowRule) *AdminActionAllowlist {
	cp := make([]AdminAllowRule, len(rules))
	copy(cp, rules)
	return &AdminActionAllowlist{rules: cp}
}

// Allowed reports whether performing action on subjectRef is permitted. Deny-by-default:
// true only when some rule matches the action AND the subject (exactly or via "*"). A
// nil allowlist denies everything.
func (a *AdminActionAllowlist) Allowed(action AdminAction, subjectRef string) bool {
	if a == nil {
		return false
	}
	subjectRef = strings.TrimSpace(subjectRef)
	for _, r := range a.rules {
		if r.Action != action {
			continue
		}
		for _, s := range r.Subjects {
			s = strings.TrimSpace(s)
			if s == "*" || (s != "" && s == subjectRef) {
				return true
			}
		}
	}
	return false
}

// AdminActionRecord is the minimal-data audit record of one action attempt. It carries
// references, the bound plan, the gate verdict and the outcome — NEVER a credential.
type AdminActionRecord struct {
	Tenant      string
	Action      AdminAction
	SubjectKind string
	SubjectRef  string
	PlanHash    string
	Allowed     bool
	// DualControl reports whether the gate decision carried a ≥2-distinct-approver quorum
	// (the dual-control evidence). False for single-approval actions and for every
	// pre-gate deny. ApproverCount is the exact distinct count.
	DualControl   bool
	ApproverCount int
	Reason        string // short, non-sensitive (e.g. "allowlist deny", "gate not approved", "executed")
	ApprovalRef   string
	RequestedBy   string
	At            time.Time
}

// AdminActionAuditor records each action decision (allow or deny) for the ledger + an
// OTel span. It is a seam: the connector emits a minimal-data record; the composition
// root writes it to the hash-chained audit ledger. The default is a no-op.
type AdminActionAuditor interface {
	Record(ctx context.Context, rec AdminActionRecord)
}

type nopAdminAuditor struct{}

func (nopAdminAuditor) Record(context.Context, AdminActionRecord) {}

// ActuatorConfig configures a governed Actuator. BaseURL/Version/AdminKey/Doer are the
// write transport (the Admin credential is presented as the out-of-band x-api-key
// header). Allowlist + Gate are the PEP (a nil Allowlist denies every action; a nil Gate
// denies every action). Auditor is the ledger/OTel seam (nil ⇒ no-op). Clock is
// injectable for tests.
type ActuatorConfig struct {
	BaseURL   string
	Version   string
	AdminKey  string
	Doer      modelprovider.Doer
	Allowlist *AdminActionAllowlist
	Gate      AdminActionGate
	Auditor   AdminActionAuditor
	Clock     func() time.Time
	// DualControlGroupIDs names the RBAC groups whose membership add is
	// privilege-critical enough to demand DUAL-CONTROL (≥2 distinct approvers,
	// re-verified by the connector), not single HITL. It is the operator's
	// connector-level lever for the defense-in-depth gap: an RBAC group bound to an
	// org-wide custom role can grant blast radius ≥ workspace_admin (which is already
	// dual-controlled), so a group listed here is treated like GrantWorkspaceAdmin. An
	// empty set keeps every group's membership add single-HITL (recoverable). Only
	// ActionAddGroupMember (privilege-granting) consults it; RemoveGroupMember is
	// de-privileging and always single-HITL.
	DualControlGroupIDs []string
}

// Actuator is the governed Admin-API action client. Construct it with NewActuator; it is
// safe for concurrent use.
type Actuator struct {
	baseURL   string
	version   string
	adminKey  string
	doer      modelprovider.Doer
	allowlist *AdminActionAllowlist
	gate      AdminActionGate
	auditor   AdminActionAuditor
	now       func() time.Time
	// dualControlGroups is the set of RBAC group ids whose membership add requires
	// dual-control (from ActuatorConfig.DualControlGroupIDs). Read-only after
	// construction (a copy is taken), so policy cannot be mutated after the fact.
	dualControlGroups map[string]struct{}
}

// NewActuator builds a governed Actuator, defaulting every governance seam to its
// deny-closed / no-op safe value: a nil Allowlist becomes deny-all, a nil Gate becomes
// denyAdminGate, a nil Auditor becomes the no-op. So an Actuator built with zero
// governance config can NEVER act — the safe default (the "inert executor" design).
// A nil Doer uses http.DefaultClient; an empty base URL falls back to the
// first-party Anthropic API.
func NewActuator(cfg ActuatorConfig) *Actuator {
	a := &Actuator{
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		version:   cfg.Version,
		adminKey:  cfg.AdminKey,
		doer:      cfg.Doer,
		allowlist: cfg.Allowlist,
		gate:      cfg.Gate,
		auditor:   cfg.Auditor,
		now:       cfg.Clock,
	}
	if a.baseURL == "" {
		a.baseURL = defaultBaseURL
	}
	if a.version == "" {
		a.version = defaultAnthropicVersion
	}
	if a.doer == nil {
		a.doer = http.DefaultClient
	}
	if a.allowlist == nil {
		a.allowlist = NewAdminActionAllowlist(nil) // deny-all
	}
	if a.gate == nil {
		a.gate = denyAdminGate{} // deny-closed
	}
	if a.auditor == nil {
		a.auditor = nopAdminAuditor{}
	}
	if a.now == nil {
		a.now = time.Now
	}
	if len(cfg.DualControlGroupIDs) > 0 {
		a.dualControlGroups = make(map[string]struct{}, len(cfg.DualControlGroupIDs))
		for _, id := range cfg.DualControlGroupIDs {
			if id = strings.TrimSpace(id); id != "" {
				a.dualControlGroups[id] = struct{}{}
			}
		}
	}
	return a
}

// isDualControlGroup reports whether adding a member to groupID is configured to demand
// dual-control (the operator lever). Deny-safe: an empty/unconfigured set returns
// false (single-HITL), matching the recoverable-action default.
func (a *Actuator) isDualControlGroup(groupID string) bool {
	if a.dualControlGroups == nil {
		return false
	}
	_, ok := a.dualControlGroups[strings.TrimSpace(groupID)]
	return ok
}

// ActionSpec is the attribution for one governed action: who is asking and in which
// tenant. It carries no credential and no payload.
type ActionSpec struct {
	Tenant      string
	RequestedBy string
}

// AdminDenyError is the typed error an action returns when the PEP refuses it (an
// unlisted (action, subject), or a gate that did not return Allowed()). It lets a caller
// distinguish a POLICY denial from a transport failure, and always carries the bound
// plan. An AdminDenyError is never a transport error.
type AdminDenyError struct {
	Reason   string
	PlanHash string
}

func (e *AdminDenyError) Error() string {
	if e.PlanHash == "" {
		return "claude-admin: action denied: " + e.Reason
	}
	return "claude-admin: action denied (" + e.Reason + ") plan=" + e.PlanHash
}

// DeactivateKey deactivates (ActionDeactivateKey) or archives (ActionArchiveKey) an API
// key — the API-governable step of a key rotation. action MUST be one of those two;
// anything else is refused. It is the full PEP: allowlist → PlanHash → gate → POST.
func (a *Actuator) DeactivateKey(ctx context.Context, action AdminAction, keyID string, spec ActionSpec) error {
	status, ok := keyStatusFor(action)
	if !ok {
		return &AdminDenyError{Reason: "unsupported key action " + string(action)}
	}
	return a.actuate(ctx, governedAction{
		action:      action,
		subjectKind: "api_key",
		subjectRef:  keyID,
		spec:        spec,
		paramsHash:  hashAdminParams("status=" + status),
		exec: func(ctx context.Context) error {
			body, _ := json.Marshal(map[string]string{"status": status})
			return a.do(ctx, http.MethodPost, "/v1/organizations/api_keys/"+pathEscape(keyID), body)
		},
	})
}

// DeprovisionMember removes an org member (DELETE /v1/organizations/users/{id}). Org
// admins cannot be removed via the API — the provider returns 403 and the executor
// surfaces it honestly (the action is still audited as attempted-then-failed).
func (a *Actuator) DeprovisionMember(ctx context.Context, userID string, spec ActionSpec) error {
	return a.actuate(ctx, governedAction{
		action:      ActionDeprovisionMember,
		subjectKind: "org_member",
		subjectRef:  userID,
		spec:        spec,
		paramsHash:  hashAdminParams("delete_user"),
		exec: func(ctx context.Context) error {
			return a.do(ctx, http.MethodDelete, "/v1/organizations/users/"+pathEscape(userID), nil)
		},
	})
}

// RevokeInvite revokes a pending org invite (DELETE /v1/organizations/invites/{id}).
func (a *Actuator) RevokeInvite(ctx context.Context, inviteID string, spec ActionSpec) error {
	return a.actuate(ctx, governedAction{
		action:      ActionRevokeInvite,
		subjectKind: "org_invite",
		subjectRef:  inviteID,
		spec:        spec,
		paramsHash:  hashAdminParams("delete_invite"),
		exec: func(ctx context.Context) error {
			return a.do(ctx, http.MethodDelete, "/v1/organizations/invites/"+pathEscape(inviteID), nil)
		},
	})
}

// InviteMember creates a pending org invite (POST /v1/organizations/invites). The
// approval subject is the normalized invitee email (trimmed/lowercased), because the
// human must approve WHO is being invited. The API refuses admin invites; this client
// denies them before any HTTP call as a policy refusal.
func (a *Actuator) InviteMember(ctx context.Context, email, role string, spec ActionSpec) error {
	email = normalizeInviteEmail(email)
	role = strings.TrimSpace(role)
	g := governedAction{
		action:      ActionInviteMember,
		subjectKind: "org_invite",
		subjectRef:  email,
		spec:        spec,
		paramsHash:  hashAdminParams("invite_role=" + role),
		exec: func(ctx context.Context) error {
			body, _ := json.Marshal(struct {
				Email string `json:"email"`
				Role  string `json:"role"`
			}{Email: email, Role: role})
			return a.do(ctx, http.MethodPost, "/v1/organizations/invites", body)
		},
	}
	if reason := validateInviteEmail(email); reason != "" {
		return a.validationDeny(g, reason)
	}
	if reason := validateOrgMemberRole(role); reason != "" {
		return a.validationDeny(g, reason)
	}
	return a.actuate(ctx, g)
}

// UpdateMemberRole updates an org member's role (POST /v1/organizations/users/{user_id}).
// It is recoverable and single-HITL. The API refuses org admin grants; this client denies
// them before any HTTP call as a policy refusal.
func (a *Actuator) UpdateMemberRole(ctx context.Context, userID, role string, spec ActionSpec) error {
	userID = strings.TrimSpace(userID)
	role = strings.TrimSpace(role)
	g := governedAction{
		action:      ActionUpdateMemberRole,
		subjectKind: "org_member",
		subjectRef:  userID,
		spec:        spec,
		paramsHash:  hashAdminParams("set_role=" + role),
		exec: func(ctx context.Context) error {
			body, _ := json.Marshal(struct {
				Role string `json:"role"`
			}{Role: role})
			return a.do(ctx, http.MethodPost, "/v1/organizations/users/"+pathEscape(userID), body)
		},
	}
	if reason := validateOrgMemberRole(role); reason != "" {
		return a.validationDeny(g, reason)
	}
	return a.actuate(ctx, g)
}

// AddWorkspaceMember adds a non-admin workspace member
// (POST /v1/organizations/workspaces/{workspace_id}/members). Allowlist subjects name the
// exact workspace:user pair (workspaceID + ":" + userID) or "*". workspace_admin is
// deliberately refused here; use GrantWorkspaceAdmin, whose action identity enforces
// dual-control. workspace_billing is API-refused and denied before any HTTP call.
func (a *Actuator) AddWorkspaceMember(ctx context.Context, workspaceID, userID, role string, spec ActionSpec) error {
	workspaceID = strings.TrimSpace(workspaceID)
	userID = strings.TrimSpace(userID)
	role = strings.TrimSpace(role)
	subjectRef := workspaceMemberSubjectRef(workspaceID, userID)
	g := governedAction{
		action:      ActionAddWorkspaceMember,
		subjectKind: "workspace_member",
		subjectRef:  subjectRef,
		spec:        spec,
		paramsHash:  hashAdminParams("workspace_role=" + role),
		exec: func(ctx context.Context) error {
			body, _ := json.Marshal(struct {
				UserID        string `json:"user_id"`
				WorkspaceRole string `json:"workspace_role"`
			}{UserID: userID, WorkspaceRole: role})
			return a.do(ctx, http.MethodPost, "/v1/organizations/workspaces/"+pathEscape(workspaceID)+"/members", body)
		},
	}
	if reason := validateWorkspaceMemberSubject(workspaceID, userID); reason != "" {
		return a.validationDeny(g, reason)
	}
	if reason := validateAddWorkspaceMemberRole(role); reason != "" {
		return a.validationDeny(g, reason)
	}
	return a.actuate(ctx, g)
}

// GrantWorkspaceAdmin adds a workspace member with workspace_role=workspace_admin. The
// action is recoverable, but privilege-granting; dual-control is enforced from the action
// identity by actionRequiresDualControl, not by a caller-supplied flag.
func (a *Actuator) GrantWorkspaceAdmin(ctx context.Context, workspaceID, userID string, spec ActionSpec) error {
	workspaceID = strings.TrimSpace(workspaceID)
	userID = strings.TrimSpace(userID)
	const role = "workspace_admin"
	subjectRef := workspaceMemberSubjectRef(workspaceID, userID)
	g := governedAction{
		action:      ActionGrantWorkspaceAdmin,
		subjectKind: "workspace_member",
		subjectRef:  subjectRef,
		spec:        spec,
		paramsHash:  hashAdminParams("workspace_role=" + role),
		exec: func(ctx context.Context) error {
			body, _ := json.Marshal(struct {
				UserID        string `json:"user_id"`
				WorkspaceRole string `json:"workspace_role"`
			}{UserID: userID, WorkspaceRole: role})
			return a.do(ctx, http.MethodPost, "/v1/organizations/workspaces/"+pathEscape(workspaceID)+"/members", body)
		},
	}
	if reason := validateWorkspaceMemberSubject(workspaceID, userID); reason != "" {
		return a.validationDeny(g, reason)
	}
	return a.actuate(ctx, g)
}

// ArchiveWorkspace archives a workspace (POST /v1/organizations/workspaces/{id}/archive,
// no body). This IMMEDIATELY revokes every API key scoped to that workspace and CANNOT be
// undone — the only API-actuable workspace-level hard cap (the Admin API has no
// spend-limit SET endpoint). Because it is irreversible it is DUAL-CONTROL: the gate must
// return ≥2 distinct approvers and the PEP re-verifies the quorum here (defense in depth).
// It is the FinOps defense-in-depth backstop's nuclear escalation — the
// surgical, recoverable backstop is DeactivateKey on the offending key.
func (a *Actuator) ArchiveWorkspace(ctx context.Context, workspaceID string, spec ActionSpec) error {
	return a.actuate(ctx, governedAction{
		action:      ActionArchiveWorkspace,
		subjectKind: "workspace",
		subjectRef:  workspaceID,
		spec:        spec,
		paramsHash:  hashAdminParams("archive_workspace"),
		exec: func(ctx context.Context) error {
			return a.do(ctx, http.MethodPost, "/v1/organizations/workspaces/"+pathEscape(workspaceID)+"/archive", nil)
		},
	})
}

// AddGroupMember adds an org member to a Claude Enterprise RBAC group
// (POST /v1/organizations/rbac_groups/{group_id}/members with {user_id}). Allowlist
// subjects name the exact group:user pair (groupID + ":" + userID) or "*". It carries
// the ce-user-management beta header (the endpoint 404s without it). SCIM-provisioned
// groups reject writes (400) — the executor surfaces the provider's error honestly and
// never papers over it. Single-HITL (recoverable via RemoveGroupMember).
func (a *Actuator) AddGroupMember(ctx context.Context, groupID, userID string, spec ActionSpec) error {
	groupID = strings.TrimSpace(groupID)
	userID = strings.TrimSpace(userID)
	subjectRef := groupMemberSubjectRef(groupID, userID)
	g := governedAction{
		action:      ActionAddGroupMember,
		subjectKind: "rbac_group_member",
		subjectRef:  subjectRef,
		spec:        spec,
		paramsHash:  hashAdminParams("add_group_member"),
		// A group flagged in DualControlGroupIDs is privilege-critical (bound to an
		// org-wide custom role) and takes dual-control, re-verified by the connector —
		// closing the asymmetry with GrantWorkspaceAdmin.
		forceDualControl: a.isDualControlGroup(groupID),
		exec: func(ctx context.Context) error {
			body, _ := json.Marshal(struct {
				UserID string `json:"user_id"`
			}{UserID: userID})
			return a.doWithHeaders(ctx, http.MethodPost,
				"/v1/organizations/rbac_groups/"+pathEscape(groupID)+"/members", body,
				map[string]string{"anthropic-beta": betaCEUserManagement})
		},
	}
	if reason := validateGroupMemberSubject(groupID, userID); reason != "" {
		return a.validationDeny(g, reason)
	}
	return a.actuate(ctx, g)
}

// RemoveGroupMember removes an org member from an RBAC group
// (DELETE /v1/organizations/rbac_groups/{group_id}/members/{user_id}). Same beta
// header + subject shape as AddGroupMember. De-privileging and recoverable, so
// single-HITL. A user not in the group (404) or a SCIM group (400) is surfaced
// honestly by the executor.
func (a *Actuator) RemoveGroupMember(ctx context.Context, groupID, userID string, spec ActionSpec) error {
	groupID = strings.TrimSpace(groupID)
	userID = strings.TrimSpace(userID)
	subjectRef := groupMemberSubjectRef(groupID, userID)
	g := governedAction{
		action:      ActionRemoveGroupMember,
		subjectKind: "rbac_group_member",
		subjectRef:  subjectRef,
		spec:        spec,
		paramsHash:  hashAdminParams("remove_group_member"),
		exec: func(ctx context.Context) error {
			return a.doWithHeaders(ctx, http.MethodDelete,
				"/v1/organizations/rbac_groups/"+pathEscape(groupID)+"/members/"+pathEscape(userID), nil,
				map[string]string{"anthropic-beta": betaCEUserManagement})
		},
	}
	if reason := validateGroupMemberSubject(groupID, userID); reason != "" {
		return a.validationDeny(g, reason)
	}
	return a.actuate(ctx, g)
}

// governedAction bundles one action's identity + its executor closure for the shared PEP.
type governedAction struct {
	action      AdminAction
	subjectKind string
	subjectRef  string
	spec        ActionSpec
	paramsHash  string
	exec        func(ctx context.Context) error
	// forceDualControl escalates a normally-single-HITL action to dual-control for THIS
	// invocation, derived from operator config the action identity alone cannot capture
	// (an RBAC group named in DualControlGroupIDs). It can only ADD a quorum
	// requirement, never remove one — actionRequiresDualControl still applies unconditionally.
	forceDualControl bool
}

// actuate is the shared deny-closed PEP every governed action passes through. It binds
// a PlanHash, enforces the deny-by-default allowlist, requires an ApprovalGate
// authorization bound to the exact plan (anti-TOCTOU), executes exactly once, and audits
// every exit path (allow or deny) with minimal data. An AdminDenyError marks a policy
// refusal; any other error is a transport/RPC failure (the decision is still audited).
func (a *Actuator) actuate(ctx context.Context, g governedAction) error {
	plan := AdminPlanHash(g.action, g.subjectKind, g.subjectRef, g.paramsHash)

	if g.subjectRef == "" {
		a.record(g, plan, false, false, 0, "empty subject", "")
		return &AdminDenyError{Reason: "empty subject ref", PlanHash: plan}
	}

	// 1) Allowlist: deny-by-default least-privilege over (action, subject).
	if !a.allowlist.Allowed(g.action, g.subjectRef) {
		a.record(g, plan, false, false, 0, "allowlist deny (action/subject not permitted)", "")
		return &AdminDenyError{Reason: "action/subject not on the admin-action allowlist", PlanHash: plan}
	}

	// 2) ApprovalGate (HITL), bound to the PlanHash. Fail closed on any error.
	dec, err := a.gate.Authorize(ctx, AdminActionRequest{
		Tenant: g.spec.Tenant, Action: g.action, SubjectKind: g.subjectKind,
		SubjectRef: g.subjectRef, PlanHash: plan, RequestedBy: g.spec.RequestedBy,
	})
	if err != nil {
		a.record(g, plan, false, false, 0, "gate error (fail-closed)", "")
		return fmt.Errorf("claude-admin: action gate error (deny): %w", err)
	}
	approvers := dec.distinctApprovers()
	dual := dec.HasDualControl()
	// Status: only an explicit approval authorizes.
	if !dec.Allowed() {
		a.record(g, plan, false, dual, approvers, "gate not approved ("+string(dec.Status)+")", dec.ApprovalRef)
		return &AdminDenyError{Reason: "action not approved by governance (" + string(dec.Status) + ")", PlanHash: plan}
	}
	// Anti-TOCTOU: the approval MUST echo the EXACT plan. An empty/absent echo means the
	// gate did not bind the action to a plan a human saw — deny. The connector never
	// trusts the gate to have bound it (defense in depth, like the dual-control re-check).
	if dec.PlanHash != plan {
		a.record(g, plan, false, dual, approvers, "plan not bound (anti-TOCTOU)", dec.ApprovalRef)
		return &AdminDenyError{Reason: "approval not bound to the action plan (anti-TOCTOU)", PlanHash: plan}
	}
	// Dual-control re-verification: an irreversible or privilege-granting action's gate
	// approval must carry ≥2 distinct approvers, RE-VERIFIED here (the connector never
	// trusts the gate to have enforced the quorum — same posture as the claude-compliance
	// RTBF DELETE). A break-glass authorization carries no approvers, so it can never
	// satisfy this. The base requirement is derived from ACTION IDENTITY, never a
	// call-site flag, so a future caller cannot silently DOWNGRADE a dual-control action.
	// g.forceDualControl can only ESCALATE (add a quorum requirement, never remove one) —
	// it is set from operator config the action identity cannot capture (an RBAC
	// group named in DualControlGroupIDs), and it is OR-ed in, so it preserves the
	// no-silent-downgrade property.
	if (actionRequiresDualControl(g.action) || g.forceDualControl) && !dual {
		a.record(g, plan, false, false, approvers, "dual-control not satisfied (need 2 distinct approvers)", dec.ApprovalRef)
		return &AdminDenyError{Reason: fmt.Sprintf("admin action requires dual-control (got %d distinct approver(s), need 2)", approvers), PlanHash: plan}
	}

	// 3) Execute exactly one write (credential out-of-band in the x-api-key header).
	if err := g.exec(ctx); err != nil {
		a.record(g, plan, true, dual, approvers, "approved; execution failed", dec.ApprovalRef)
		return err
	}
	a.record(g, plan, true, dual, approvers, "executed", dec.ApprovalRef)
	return nil
}

// record emits a minimal-data audit decision (best-effort; never blocks the result).
// dualControl/approverCount carry the quorum evidence (false/0 for a pre-gate deny or a
// recoverable single-approval action).
func (a *Actuator) record(g governedAction, plan string, allowed, dualControl bool, approverCount int, reason, approvalRef string) {
	a.auditor.Record(context.Background(), AdminActionRecord{
		Tenant: g.spec.Tenant, Action: g.action, SubjectKind: g.subjectKind, SubjectRef: g.subjectRef,
		PlanHash: plan, Allowed: allowed, DualControl: dualControl, ApproverCount: approverCount,
		Reason: reason, ApprovalRef: approvalRef, RequestedBy: g.spec.RequestedBy, At: a.now().UTC(),
	})
}

// validationDeny records a pre-transport policy refusal. The reason must stay bounded and
// non-sensitive (no credential, provider body, or invite email).
func (a *Actuator) validationDeny(g governedAction, reason string) error {
	plan := AdminPlanHash(g.action, g.subjectKind, g.subjectRef, g.paramsHash)
	a.record(g, plan, false, false, 0, "validation deny ("+reason+")", "")
	return &AdminDenyError{Reason: reason, PlanHash: plan}
}

// do issues one authenticated write (POST/DELETE) with only the standard headers. It
// is the common case; doWithHeaders adds any per-action extras (e.g. the
// ce-user-management beta header the RBAC group writes require).
func (a *Actuator) do(ctx context.Context, method, path string, body []byte) error {
	return a.doWithHeaders(ctx, method, path, body, nil)
}

// doWithHeaders issues one authenticated write (POST/DELETE) with the standard headers
// plus any extras, and fails on any non-2xx status. A bounded slice of an error body is
// surfaced for diagnostics (provider error messages, no PII); the credential never
// appears in any error. Extra headers cannot override x-api-key/anthropic-version/
// content-type — those are set AFTER the extras so a caller can never strip auth.
func (a *Actuator) doWithHeaders(ctx context.Context, method, path string, body []byte, extra map[string]string) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, rdr)
	if err != nil {
		return err
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	if a.adminKey != "" {
		req.Header.Set("x-api-key", a.adminKey)
	}
	req.Header.Set("anthropic-version", a.version)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	resp, err := a.doer.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slice, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return fmt.Errorf("claude-admin: %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(slice)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

// actionIsIrreversible reports whether an action cannot be undone and therefore ALWAYS
// requires dual-control, independent of any call-site flag — the irreversible
// classification is keyed off the action identity so it cannot be lost when a new caller
// constructs a governedAction. Today only workspace archive (it revokes every key in the
// workspace and cannot be undone).
func actionIsIrreversible(action AdminAction) bool {
	return action == ActionArchiveWorkspace
}

// actionRequiresDualControl reports whether an action's identity demands two-person
// approval evidence. Workspace-admin grant is reversible, but privilege-critical; archive
// remains the only irreversible action.
func actionRequiresDualControl(action AdminAction) bool {
	return actionIsIrreversible(action) || action == ActionGrantWorkspaceAdmin
}

// keyStatusFor maps a key action to the status value it sets, and whether the action is
// a key action at all.
func keyStatusFor(action AdminAction) (string, bool) {
	switch action {
	case ActionDeactivateKey:
		return "inactive", true
	case ActionArchiveKey:
		return "archived", true
	default:
		return "", false
	}
}

const maxInviteEmailLen = 320

func normalizeInviteEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validateInviteEmail(email string) string {
	switch {
	case email == "":
		return "invite email is required"
	case len(email) > maxInviteEmailLen:
		return "invite email exceeds maximum length"
	case !strings.Contains(email, "@"):
		return "invite email must contain @"
	default:
		return ""
	}
}

// validateOrgMemberRole gates org-member role assignment. VERIFIED 2026-07-20 against
// platform.claude.com: the org role enum grew (managed, membership_admin, owner,
// primary_owner) for Claude Enterprise. The API can ASSIGN only the non-admin tiers —
// the Console-org roles (user/developer/billing/claude_code_user) and the CE-assignable
// "managed" role. The admin-tier roles (admin, owner, membership_admin, primary_owner)
// are Console-only privilege grants the API refuses, so this client denies them before
// any HTTP call (a policy refusal, never a papered-over provider 400).
func validateOrgMemberRole(role string) string {
	switch role {
	case "user", "developer", "billing", "claude_code_user", "managed":
		return ""
	case "admin", "owner", "membership_admin", "primary_owner":
		return "org role " + role + " is a Console-only admin-tier grant and cannot be assigned by the Claude Admin API"
	case "":
		return "org role is required"
	default:
		return "unsupported org role"
	}
}

// groupMemberSubjectRef is the canonical (group, user) subject the group-membership PEP
// reasons over — the exact pair a human must approve. Mirrors workspaceMemberSubjectRef.
func groupMemberSubjectRef(groupID, userID string) string {
	return groupID + ":" + userID
}

// validateGroupMemberSubject rejects an empty group or user before any HTTP call.
func validateGroupMemberSubject(groupID, userID string) string {
	switch {
	case groupID == "":
		return "group id is required"
	case userID == "":
		return "user id is required"
	default:
		return ""
	}
}

func validateAddWorkspaceMemberRole(role string) string {
	switch role {
	case "workspace_user", "workspace_developer", "workspace_restricted_developer":
		return ""
	case "workspace_admin":
		return "workspace_admin requires action " + string(ActionGrantWorkspaceAdmin)
	case "workspace_billing":
		return "workspace_billing is not supported by the Claude Admin API"
	case "":
		return "workspace role is required"
	default:
		return "unsupported workspace role"
	}
}

func validateWorkspaceMemberSubject(workspaceID, userID string) string {
	switch {
	case workspaceID == "":
		return "workspace id is required"
	case userID == "":
		return "user id is required"
	default:
		return ""
	}
}

func workspaceMemberSubjectRef(workspaceID, userID string) string {
	return workspaceID + ":" + userID
}

// adminPlanHashVersion namespaces the canonical admin-action plan-hash so a future
// change to the tuple shape cannot collide with an existing bound approval.
const adminPlanHashVersion = "admin-action-v1"

// AdminPlanHash computes the canonical, anti-TOCTOU binding for an admin action: a
// stable SHA-256 over the normalized (action, subjectKind, subjectRef, paramsHash)
// tuple. Any re-target (different subject), re-action, or changed params changes the
// hash and voids a stale approval. The fields are length-prefixed so no separator
// collision can forge a matching plan.
func AdminPlanHash(action AdminAction, subjectKind, subjectRef, paramsHash string) string {
	h := sha256.New()
	for _, part := range []string{
		adminPlanHashVersion,
		string(action),
		strings.TrimSpace(subjectKind),
		strings.TrimSpace(subjectRef),
		strings.TrimSpace(paramsHash),
	} {
		var lenbuf [8]byte
		n := len(part)
		for i := 0; i < 8; i++ {
			lenbuf[i] = byte(n >> (8 * (7 - i)))
		}
		_, _ = h.Write(lenbuf[:])
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hashAdminParams is a stable digest of an action's minimal-data parameters (the
// status/operation label), so the PlanHash binds WHAT the human approved without
// persisting anything sensitive (there is nothing sensitive here — only operation labels).
func hashAdminParams(label string) string {
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:])
}

// pathEscape escapes a path segment (key/user/invite id) for safe URL composition.
func pathEscape(seg string) string { return url.PathEscape(seg) }
