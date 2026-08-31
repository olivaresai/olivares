// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ExecutionMode is the mandatory command phase. Validate and plan are
// observational; apply revalidates against the same transaction it mutates.
type ExecutionMode string

const (
	ModeValidate ExecutionMode = "validate"
	ModePlan     ExecutionMode = "plan"
	ModeApply    ExecutionMode = "apply"
)

// AssessmentVerdict deliberately has three outcomes. Unknown evidence never
// collapses into success and a known invariant failure is not reported as an
// infrastructure outage.
type AssessmentVerdict string

const (
	VerdictClean   AssessmentVerdict = "LIMPIO"
	VerdictBroken  AssessmentVerdict = "ROTO"
	VerdictUnknown AssessmentVerdict = "NO_HE_PODIDO_MIRAR"
)

type WorkCheck struct {
	Name        string            `json:"name"`
	Verdict     AssessmentVerdict `json:"verdict"`
	EvidenceRef string            `json:"evidence_ref,omitempty"`
}

type Assessment struct {
	Verdict    AssessmentVerdict `json:"verdict"`
	Code       string            `json:"code"`
	ObservedAt string            `json:"observed_at"`
	Checks     []WorkCheck       `json:"checks"`
	PlanHash   string            `json:"plan_hash"`
	Resource   any               `json:"resource,omitempty"`
}

type Plan struct {
	Assessment
	Command      string   `json:"command"`
	ExpectedETag string   `json:"expected_etag,omitempty"`
	RowEffects   []string `json:"row_effects"`
	EventType    string   `json:"event_type"`
	// EventTypes is present only when one command will append more than the
	// primary EventType. Its order is the required aggregate delivery order.
	EventTypes    []string `json:"event_types,omitempty"`
	AuditAction   string   `json:"audit_action"`
	Permission    string   `json:"permission"`
	ExternalCalls []string `json:"external_calls"`
}

type CommandResult struct {
	Verdict    AssessmentVerdict `json:"verdict"`
	Code       string            `json:"code"`
	CommandID  model.ID          `json:"command_id"`
	ResultKind string            `json:"result_kind"`
	ResultID   model.ID          `json:"result_id,omitempty"`
	Version    int64             `json:"version,omitempty"`
	Status     string            `json:"status,omitempty"`
	EventID    model.ID          `json:"event_id"`
	EventSeq   int64             `json:"event_seq"`
	OwnerEpoch int64             `json:"owner_epoch"`
	LeaseFence int64             `json:"lease_fence,omitempty"`
	PlanHash   string            `json:"plan_hash"`
	AuditSeq   int64             `json:"audit_seq"`
	// Replayed is transport metadata. The persisted and replayed response body
	// stays byte-identical; REST exposes replay only through its response header.
	Replayed bool `json:"-"`
}

// WorkOutboxReplay reports the durable requeue of one dead-lettered event. The
// event identity never changes; attempts remain monotonic across admin replay.
type WorkOutboxReplay struct {
	Verdict       AssessmentVerdict `json:"verdict"`
	Code          string            `json:"code"`
	CommandID     model.ID          `json:"command_id"`
	OutboxID      model.ID          `json:"outbox_id"`
	EventID       model.ID          `json:"event_id"`
	AggregateKind string            `json:"aggregate_kind"`
	AggregateID   model.ID          `json:"aggregate_id"`
	// WorkItemID is the additive K1/K2 response alias. It is populated only for
	// sessions.work_item; a Message ID must never appear under this name.
	WorkItemID   model.ID `json:"work_item_id,omitempty"`
	State        string   `json:"state"`
	Version      int64    `json:"version"`
	Attempts     int64    `json:"attempts"`
	PriorState   string   `json:"prior_state"`
	PriorVersion int64    `json:"prior_version"`
	PlanHash     string   `json:"plan_hash"`
	AuditSeq     int64    `json:"audit_seq"`
	Replayed     bool     `json:"-"`
	responseJSON string
}

// WorkOutboxReplayCommand is the server-filled command envelope for an admin
// replay. EventID is path-derived on HTTP; the remaining private fields bind an
// exact apply retry and cannot be asserted as domain state by a request body.
type WorkOutboxReplayCommand struct {
	Command          string   `json:"command"`
	EventID          model.ID `json:"event_id"`
	PlanHash         string   `json:"plan_hash,omitempty"`
	ExpectedVersion  int64    `json:"-"`
	ExpectedPlanHash string   `json:"-"`
	IdempotencyKey   string   `json:"-"`
	CommandScope     string   `json:"-"`
	HTTPMethod       string   `json:"-"`
}

// WorkPrincipal is constructed from authenticated state, never from a request
// body. ActorRef is canonical and safe to persist in semantic evidence.
type WorkPrincipal struct {
	ActorKind string `json:"-"`
	ActorRef  string `json:"-"`
	Actor     string `json:"-"`
	Admin     bool   `json:"-"`
	SessionID string `json:"-"`
	// SessionRunRef is the exact runtime generation authenticated by a
	// purpose-restricted work-session credential. Ordinary principals leave it
	// empty; request input can never populate it.
	SessionRunRef string `json:"-"`
	// SessionFence is the authenticated Claim generation for SessionID.
	SessionFence int64 `json:"-"`
	// PurposeRestricted marks a server-issued work-session capability. Its
	// work:write permission is route-shared, so domain validation must keep
	// block/fail/evaluate on the exact active execution lease.
	PurposeRestricted bool `json:"-"`
}

type ContextRef struct {
	Kind string `json:"kind" yaml:"kind"`
	Ref  string `json:"ref" yaml:"ref"`
	Hash string `json:"hash,omitempty" yaml:"hash,omitempty"`
}

type AcceptanceInput struct {
	ID               model.ID `json:"id,omitempty" yaml:"id,omitempty"`
	Key              string   `json:"criterion_key" yaml:"criterion_key"`
	Ordinal          int64    `json:"ordinal" yaml:"ordinal"`
	Statement        string   `json:"statement" yaml:"statement"`
	Required         bool     `json:"required" yaml:"required"`
	State            string   `json:"state,omitempty" yaml:"state,omitempty"`
	EvidenceRef      string   `json:"evidence_ref,omitempty" yaml:"evidence_ref,omitempty"`
	EvidenceHash     string   `json:"evidence_hash,omitempty" yaml:"evidence_hash,omitempty"`
	WaiverDecisionID model.ID `json:"waiver_decision_id,omitempty" yaml:"waiver_decision_id,omitempty"`
}

// WorkCommand is the one command document shared by REST, CLI and in-process
// callers. Route metadata (idempotency, ETag and method/scope) is server-filled.
// Fields unused by a command are rejected by command-specific validation where
// their presence could change meaning.
type WorkCommand struct {
	Command          string            `json:"command" yaml:"command"`
	WorkspaceID      model.ID          `json:"workspace_id,omitempty" yaml:"workspace_id,omitempty"`
	WorkItemID       model.ID          `json:"work_item_id,omitempty" yaml:"work_item_id,omitempty"`
	TargetID         model.ID          `json:"target_id,omitempty" yaml:"target_id,omitempty"`
	WorkKind         string            `json:"work_kind,omitempty" yaml:"work_kind,omitempty"`
	Title            string            `json:"title,omitempty" yaml:"title,omitempty"`
	BriefMD          string            `json:"brief_md,omitempty" yaml:"brief_md,omitempty"`
	ContextRefs      []ContextRef      `json:"context_refs,omitempty" yaml:"context_refs,omitempty"`
	Priority         string            `json:"priority,omitempty" yaml:"priority,omitempty"`
	OwnerKind        string            `json:"owner_kind,omitempty" yaml:"owner_kind,omitempty"`
	OwnerRef         string            `json:"owner_ref,omitempty" yaml:"owner_ref,omitempty"`
	ProvenanceKind   string            `json:"provenance_kind,omitempty" yaml:"provenance_kind,omitempty"`
	ProvenanceRef    string            `json:"provenance_ref,omitempty" yaml:"provenance_ref,omitempty"`
	ProvenanceHash   string            `json:"provenance_hash,omitempty" yaml:"provenance_hash,omitempty"`
	ParentID         model.ID          `json:"parent_id,omitempty" yaml:"parent_id,omitempty"`
	SupersedesID     model.ID          `json:"supersedes_id,omitempty" yaml:"supersedes_id,omitempty"`
	DueAt            string            `json:"due_at,omitempty" yaml:"due_at,omitempty"`
	Acceptance       []AcceptanceInput `json:"acceptance,omitempty" yaml:"acceptance,omitempty"`
	Transition       string            `json:"transition,omitempty" yaml:"transition,omitempty"`
	Code             string            `json:"code,omitempty" yaml:"code,omitempty"`
	Reason           string            `json:"reason,omitempty" yaml:"reason,omitempty"`
	DependsOnID      model.ID          `json:"depends_on_id,omitempty" yaml:"depends_on_id,omitempty"`
	DependencyID     model.ID          `json:"dependency_id,omitempty" yaml:"dependency_id,omitempty"`
	CriterionID      model.ID          `json:"criterion_id,omitempty" yaml:"criterion_id,omitempty"`
	CriterionKey     string            `json:"criterion_key,omitempty" yaml:"criterion_key,omitempty"`
	Ordinal          int64             `json:"ordinal,omitempty" yaml:"ordinal,omitempty"`
	Statement        string            `json:"statement,omitempty" yaml:"statement,omitempty"`
	Required         bool              `json:"required,omitempty" yaml:"required,omitempty"`
	State            string            `json:"state,omitempty" yaml:"state,omitempty"`
	EvidenceRef      string            `json:"evidence_ref,omitempty" yaml:"evidence_ref,omitempty"`
	EvidenceHash     string            `json:"evidence_hash,omitempty" yaml:"evidence_hash,omitempty"`
	WaiverDecisionID model.ID          `json:"waiver_decision_id,omitempty" yaml:"waiver_decision_id,omitempty"`
	BlockedCode      string            `json:"blocked_code,omitempty" yaml:"blocked_code,omitempty"`
	BlockedReason    string            `json:"blocked_reason,omitempty" yaml:"blocked_reason,omitempty"`
	TerminalCode     string            `json:"terminal_code,omitempty" yaml:"terminal_code,omitempty"`
	TerminalReason   string            `json:"terminal_reason,omitempty" yaml:"terminal_reason,omitempty"`
	DecisionKey      string            `json:"decision_key,omitempty" yaml:"decision_key,omitempty"`
	SubjectKind      string            `json:"subject_kind,omitempty" yaml:"subject_kind,omitempty"`
	SubjectRef       string            `json:"subject_ref,omitempty" yaml:"subject_ref,omitempty"`
	StatementMD      string            `json:"statement_md,omitempty" yaml:"statement_md,omitempty"`
	RationaleMD      string            `json:"rationale_md,omitempty" yaml:"rationale_md,omitempty"`
	AuthorityRef     string            `json:"authority_ref,omitempty" yaml:"authority_ref,omitempty"`
	DecisionID       model.ID          `json:"decision_id,omitempty" yaml:"decision_id,omitempty"`
	HolderSID        string            `json:"holder_sid,omitempty" yaml:"holder_sid,omitempty"`
	HolderRunRef     string            `json:"holder_run_ref,omitempty" yaml:"holder_run_ref,omitempty"`
	HolderAgentRef   string            `json:"holder_agent_ref,omitempty" yaml:"holder_agent_ref,omitempty"`
	Fence            int64             `json:"fence,omitempty" yaml:"fence,omitempty"`
	TTLSeconds       int64             `json:"ttl_seconds,omitempty" yaml:"ttl_seconds,omitempty"`
	Force            bool              `json:"force,omitempty" yaml:"force,omitempty"`
	Unblock          bool              `json:"unblock,omitempty" yaml:"unblock,omitempty"`
	ChangesRequested bool              `json:"changes_requested,omitempty" yaml:"changes_requested,omitempty"`
	PlanHash         string            `json:"plan_hash,omitempty" yaml:"plan_hash,omitempty"`

	ExpectedVersion     int64  `json:"-" yaml:"-"`
	IdempotencyKey      string `json:"-" yaml:"-"`
	ExpectedPlanHash    string `json:"-" yaml:"-"`
	CommandScope        string `json:"-" yaml:"-"`
	HTTPMethod          string `json:"-" yaml:"-"`
	participantResolved bool
	leaseHolderResolved bool
	// agentAuthority is a server-only tenant-wide eligibility snapshot. Its
	// digest participates in plan hashing; the opaque token never does and is
	// only handed back to the composition adapter inside Apply.
	agentAuthority WorkAgentAuthoritySnapshot
	// holderSIDProven records that preflightIdentity ESTABLISHED, against the
	// authenticated principal, that this caller may act for HolderSID. It is
	// unexported and server-set for the same reason participantResolved is: a
	// body cannot assert it. HolderSID itself arrives in the request, so it is a
	// declaration; this flag is the difference between the two, and
	// leasePrincipalMatches reads the flag, never the bare field.
	holderSIDProven bool
	// agentOwnerProven records the server-side SessionActsForAgent proof for an
	// agent-owned item. The lease stores the owner's canonical Identity.ID even
	// though the authenticated agent principal and run row use ExternalID.
	agentOwnerProven  bool
	internal          bool
	postCommitRefusal *error
}

type WorkItem struct {
	ID                 model.ID        `json:"id"`
	WorkspaceID        model.ID        `json:"workspace_id"`
	Version            int64           `json:"version"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
	WorkKind           string          `json:"work_kind"`
	Title              string          `json:"title"`
	BriefMD            string          `json:"brief_md"`
	BriefHash          string          `json:"brief_hash"`
	ContextRefs        json.RawMessage `json:"context_refs"`
	Status             string          `json:"status"`
	Priority           string          `json:"priority"`
	OwnerKind          string          `json:"owner_kind"`
	OwnerRef           string          `json:"owner_ref"`
	OwnerEpoch         int64           `json:"owner_epoch"`
	ProvenanceKind     string          `json:"provenance_kind"`
	ProvenanceRef      string          `json:"provenance_ref"`
	ProvenanceHash     string          `json:"provenance_hash,omitempty"`
	ParentID           model.ID        `json:"parent_id,omitempty"`
	SupersedesID       model.ID        `json:"supersedes_id,omitempty"`
	AcceptanceRevision int64           `json:"acceptance_revision"`
	BlockedCode        string          `json:"blocked_code,omitempty"`
	BlockedReason      string          `json:"blocked_reason,omitempty"`
	TerminalCode       string          `json:"terminal_code,omitempty"`
	TerminalReason     string          `json:"terminal_reason,omitempty"`
	DueAt              string          `json:"due_at,omitempty"`
	ReadyAt            string          `json:"ready_at,omitempty"`
	StartedAt          string          `json:"started_at,omitempty"`
	ReviewAt           string          `json:"review_at,omitempty"`
	TerminalAt         string          `json:"terminal_at,omitempty"`
	ArchivedAt         string          `json:"archived_at,omitempty"`
	LastEventSeq       int64           `json:"last_event_seq"`
	DependencyBlocked  bool            `json:"dependency_blocked"`
	Claimable          bool            `json:"claimable"`
	Leased             bool            `json:"leased"`
	Orphaned           bool            `json:"orphaned"`
	// Lease is retained for in-process derivation/tests only. Public WorkItem
	// projections expose claimable/leased/orphaned; the holder generation and
	// operator-authored end reason require the dedicated lease:read surface.
	Lease *WorkLease `json:"-"`
}

type WorkSnapshot struct {
	Item         WorkItem                       `json:"item"`
	Acceptance   api.JSONArray[json.RawMessage] `json:"acceptance"`
	Dependencies api.JSONArray[json.RawMessage] `json:"dependencies"`
}

// WorkLease is durable execution authority over one WorkItem. HolderSID uses
// the canonical sessions vocabulary (osn_<uuid>), so it is text by design.
type WorkLease struct {
	ID              model.ID          `json:"id,omitempty"`
	WorkspaceID     model.ID          `json:"workspace_id"`
	WorkItemID      model.ID          `json:"work_item_id"`
	Version         int64             `json:"version,omitempty"`
	HolderSID       string            `json:"holder_sid,omitempty"`
	HolderRunRef    string            `json:"holder_run_ref,omitempty"`
	HolderAgentRef  string            `json:"holder_agent_ref,omitempty"`
	Fence           int64             `json:"fence"`
	State           string            `json:"state"`
	AcquiredAt      string            `json:"acquired_at,omitempty"`
	RenewedAt       string            `json:"renewed_at,omitempty"`
	ExpiresAt       string            `json:"expires_at,omitempty"`
	EndedAt         string            `json:"ended_at,omitempty"`
	EndReason       string            `json:"end_reason,omitempty"`
	RenewalCount    int64             `json:"renewal_count"`
	Live            bool              `json:"live"`
	LivenessVerdict AssessmentVerdict `json:"liveness_verdict"`
	LivenessCode    string            `json:"liveness_code"`
}

type WorkLeaseQuery struct {
	Limit   int
	Cursor  string
	Filters map[string]string
}

type WorkLeasePage struct {
	Items      api.JSONArray[WorkLease] `json:"items"`
	NextCursor string                   `json:"next_cursor"`
	HasMore    bool                     `json:"has_more"`
}

type WorkQuery struct {
	Limit   int
	Cursor  string
	Filters map[string]string
}

type WorkPage struct {
	Items      api.JSONArray[WorkItem] `json:"items"`
	NextCursor string                  `json:"next_cursor"`
	HasMore    bool                    `json:"has_more"`
}

type WorkEventEnvelope struct {
	TenantID      model.TenantID  `json:"tenant_id"`
	WorkspaceID   model.ID        `json:"workspace_id"`
	EventID       model.ID        `json:"event_id"`
	AggregateKind string          `json:"aggregate_kind"`
	AggregateID   model.ID        `json:"aggregate_id"`
	Sequence      int64           `json:"sequence"`
	Type          string          `json:"type"`
	OccurredAt    string          `json:"occurred_at"`
	Payload       json.RawMessage `json:"payload"`
}

type Participant struct {
	Kind              string
	CanonicalRef      string
	Active            bool
	WorkspaceEligible bool
}

type ContentDecision struct {
	Allowed bool
	Code    string
}

type WorkIdentityResolver interface {
	ResolveParticipant(context.Context, model.TenantID, model.ID, string, string) (Participant, error)
	SessionActsForAgent(context.Context, model.TenantID, string, string) (bool, error)
}

// WorkAuthenticatedAgentMatcher is the optional, narrower identity seam used
// when an authenticated agent owner reports failure after its execution lease
// has ended. WorkItem owner_ref is the canonical Identity.ID, while the
// principal carries the independently authenticated ExternalID; comparing the
// strings directly is never a valid substitute for this server-side lookup.
// A resolver that cannot answer must leave this interface unimplemented so the
// command returns UNKNOWN instead of falling back to caller-provided spelling.
type WorkAuthenticatedAgentMatcher interface {
	AuthenticatedAgentMatches(
		context.Context, model.TenantID, string, string,
	) (bool, error)
}

// WorkAgentAuthoritySnapshot is opaque server-owned evidence that an agent
// owner is currently eligible. Digest is safe to bind into a plan; it must not
// disclose sponsor or lifecycle identifiers. Implementations retain any exact
// fact references privately and use Token only within the same request.
type WorkAgentAuthoritySnapshot struct {
	Eligible bool
	Digest   string
	Token    any
}

// WorkAgentEligibilityInScope is the optional transaction-bound identity seam
// used immediately before a mutation consumes or renews agent authority.
// ObserveAgentWorkAuthority reads the tenant-wide authority plane without
// borrowing the request's workspace-confined data handle. LockAgentWorkAuthority
// then pins that exact snapshot on the supplied WorkItem transaction without
// returning tenant-wide rows. Returning a cached answer without locking its
// fact versions does not satisfy this contract.
type WorkAgentEligibilityInScope interface {
	ObserveAgentWorkAuthority(
		context.Context, model.TenantID, model.ID, string, string,
	) (WorkAgentAuthoritySnapshot, error)
	LockAgentWorkAuthority(
		context.Context, store.Scope, WorkAgentAuthoritySnapshot,
	) error
}

type WorkContentGuard interface {
	Inspect(context.Context, model.TenantID, model.ID, string, []byte) (ContentDecision, error)
}

type WorkEventSink interface {
	IngestDurable(context.Context, WorkEventEnvelope) error
}

// WorkAuthorizer is the narrow seam needed for command-dependent privilege
// checks on a shared REST route (for example archive versus ordinary state
// transitions). The concrete core authorizer stays in the composition root.
type WorkAuthorizer interface {
	Authorize(context.Context, auth.Request) auth.Decision
}

type WorkKernel interface {
	Validate(context.Context, model.TenantID, WorkPrincipal, WorkCommand) (Assessment, error)
	Plan(context.Context, model.TenantID, WorkPrincipal, WorkCommand) (Plan, error)
	Apply(context.Context, model.TenantID, WorkPrincipal, WorkCommand) (CommandResult, error)
	Get(context.Context, model.TenantID, WorkPrincipal, model.ID) (WorkSnapshot, error)
	List(context.Context, model.TenantID, WorkPrincipal, WorkQuery) (WorkPage, error)
	GetLease(context.Context, model.TenantID, WorkPrincipal, model.ID) (WorkLease, error)
	ListLeases(context.Context, model.TenantID, WorkPrincipal, WorkLeaseQuery) (WorkLeasePage, error)
	OwnerDied(context.Context, model.TenantID, string, string, string) error
}

var _ WorkKernel = (*Module)(nil)
