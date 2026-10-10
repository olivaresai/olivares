// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

// Stored declarations are kept in both editions for export and account retirement.
import (
	"encoding/json"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"regexp"
	"sort"
	"time"
)

const (
	stepScheduleFire = "schedule-fire" // dispatch an EXISTING governed schedule
	stepEventingEmit = "eventing-emit" // publish a workflow.signal event (fixed type)
	stepNotifyTest   = "notify-test"   // send the synthetic test through an alert route
	stepWait         = "wait"          // pause the run for a bounded duration
	stepApprovalGate = "approval-gate" // open a HITL approval and pause until resolved

	// K4 work steps are governed verbs over the sessions work kernel. Their
	// configs carry bounded data and exact references only; execution leaves
	// through composition-root ports and never through an arbitrary HTTP/exec
	// escape hatch.
	stepWorkCreate     = "work-create"
	stepWorkAssign     = "work-assign"
	stepWorkClaim      = "work-claim"
	stepSessionLaunch  = "session-launch"
	stepWorkMessage    = "work-message"
	stepWorkWaitAck    = "work-wait-ack"
	stepWorkHandoff    = "work-handoff"
	stepWorkTransition = "work-transition"
	stepWorkCancel     = "work-cancel"
	stepWorkReconcile  = "work-reconcile"

	// K5 remote-work steps expose the complete governed lifecycle. They are
	// deliberately separate so Plan/Test remain non-effecting review points and
	// Start/Cancel carry their own durable semantic idempotency receipts.
	stepRemotePlan    = "remote-plan"
	stepRemoteTest    = "remote-test"
	stepRemoteStart   = "remote-start"
	stepRemoteObserve = "remote-observe"
	stepRemoteCancel  = "remote-cancel"
)

var stepConfigs = stepKindTable{
	stepScheduleFire: model.Nested(scheduleFireConfig{}, model.ClassEvidence, model.Leaf("schedule_id", stepLeafID)),
	stepEventingEmit: model.Nested(eventingEmitConfig{}, model.ClassEvidence, model.Leaf("label", stepLeafText)),
	stepNotifyTest:   model.Nested(notifyTestConfig{}, model.ClassEvidence, model.Leaf("route_id", stepLeafID)),
	stepWait:         model.Nested(waitConfig{}, model.ClassEvidence),
	stepApprovalGate: model.Nested(approvalGateConfig{}, model.ClassEvidence, model.Leaf("reason", stepLeafText)),
	stepWorkCreate: model.Nested(workCreateConfig{}, model.ClassObligation, stepParticipantLeaves,
		model.Leaf("workspace_id", stepLeafID), model.Leaf("work_kind", stepLeafSlug),
		model.Leaf("title", stepLeafText), model.Leaf("brief_md", stepLeafText), model.Leaf("brief_ref", stepLeafText),
		model.Leaf("priority", stepLeafClosed), model.Leaf("criteria[].key", stepLeafSlug),
		model.Leaf("criteria[].statement", stepLeafText), model.Leaf("provenance.kind", stepLeafClosed),
		model.Leaf("provenance.ref", model.Scan(model.ClassEvidence)), model.Leaf("provenance.hash", stepLeafHash),
		model.Leaf("due_at", stepLeafTime)),
	stepWorkAssign: model.Nested(workAssignConfig{}, model.ClassObligation, stepParticipantLeaves,
		model.Leaf("work_item_id", stepLeafID), model.Leaf("work_item_step_ref", stepLeafSlug),
		model.Leaf("channel_id", stepLeafID), model.Leaf("context", stepLeafText), model.Leaf("context_ref", stepLeafText),
		model.Leaf("ack_deadline", stepLeafTime)),
	stepWorkClaim: model.Nested(workClaimConfig{}, model.ClassEvidence,
		model.Leaf("work_item_id", stepLeafID), model.Leaf("work_item_step_ref", stepLeafSlug),
		model.Leaf("sid", model.None("a runtime session id, bounded by validWorkText: workflow_graph.go:627"))),
	stepSessionLaunch: model.Nested(sessionLaunchConfig{}, model.ClassEvidence,
		model.Leaf("work_item_id", stepLeafID), model.Leaf("work_item_step_ref", stepLeafSlug),
		model.Leaf("fence_step_ref", stepLeafSlug), model.Leaf("runtime_profile_ref", stepLeafExternal),
		model.Leaf("attempt_kind", stepLeafSlug)),
	stepWorkMessage: model.Nested(workMessageConfig{}, model.ClassObligation, stepParticipantLeaves,
		model.Leaf("work_item_id", stepLeafID), model.Leaf("work_item_step_ref", stepLeafSlug),
		model.Leaf("channel_id", stepLeafID), model.Leaf("body", stepLeafText), model.Leaf("body_ref", stepLeafText),
		model.Leaf("ack_due_at", stepLeafTime), model.Leaf("urgency", stepLeafClosed)),
	stepWorkWaitAck: model.Nested(workWaitAckConfig{}, model.ClassEvidence,
		model.Leaf("target_kind", stepLeafClosed), model.Leaf("target_id", stepLeafID),
		model.Leaf("target_step_ref", stepLeafSlug), model.Leaf("deadline", stepLeafTime)),
	stepWorkHandoff: model.Nested(workHandoffConfig{}, model.ClassObligation, stepParticipantLeaves,
		model.Leaf("work_item_id", stepLeafID), model.Leaf("work_item_step_ref", stepLeafSlug),
		model.Leaf("channel_id", stepLeafID), model.Leaf("context", stepLeafText), model.Leaf("context_ref", stepLeafText),
		model.Leaf("ack_deadline", stepLeafTime)),
	stepWorkTransition: model.Nested(workTransitionConfig{}, model.ClassEvidence,
		model.Leaf("work_item_id", stepLeafID), model.Leaf("work_item_step_ref", stepLeafSlug),
		model.Leaf("target_state", stepLeafClosed), model.Leaf("evidence_ref", stepLeafText), model.Leaf("reason", stepLeafText)),
	stepWorkCancel: model.Nested(workCancelConfig{}, model.ClassEvidence,
		model.Leaf("work_item_id", stepLeafID), model.Leaf("work_item_step_ref", stepLeafSlug),
		model.Leaf("binding_id", stepLeafID), model.Leaf("reason", stepLeafText)),
	stepWorkReconcile: model.Nested(workReconcileConfig{}, model.ClassEvidence, model.Leaf("binding_id", stepLeafID)),
	stepRemotePlan: model.Nested(remotePlanConfig{}, model.ClassEvidence,
		model.Leaf("workspace_id", stepLeafID), model.Leaf("work_item_id", stepLeafID),
		model.Leaf("work_item_step_ref", stepLeafSlug), model.Leaf("binding_spec_id", stepLeafID),
		model.Leaf("protocol", stepLeafClosed), model.Leaf("protocol_version", stepLeafExternal),
		model.Leaf("authority", stepLeafExternal), model.Leaf("agent_ref", stepLeafExternal),
		model.Leaf("skill", stepLeafExternal), model.Leaf("scope", stepLeafExternal), model.Leaf("brief_hash", stepLeafHash)),
	stepRemoteTest:    model.Nested(remoteTestConfig{}, model.ClassEvidence, model.Leaf("plan_step_ref", stepLeafSlug)),
	stepRemoteStart:   model.Nested(remoteStartConfig{}, model.ClassEvidence, model.Leaf("plan_step_ref", stepLeafSlug)),
	stepRemoteObserve: model.Nested(remoteBindingConfig{}, model.ClassEvidence, model.Leaf("binding_id", stepLeafID), model.Leaf("binding_step_ref", stepLeafSlug)),
	stepRemoteCancel: model.Nested(remoteCancelConfig{}, model.ClassEvidence,
		model.Leaf("binding_id", stepLeafID), model.Leaf("binding_step_ref", stepLeafSlug),
		model.Leaf("work_item_id", stepLeafID), model.Leaf("work_item_step_ref", stepLeafSlug), model.Leaf("reason", stepLeafText)),
}

type stepKindTable map[string]*model.ColumnDecl

func (t stepKindTable) Kinds() []string {
	out := make([]string, 0, len(t))
	for kind := range t {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}
func (t stepKindTable) Variant(kind string) (*model.ColumnDecl, bool) {
	decl, ok := t[kind]
	return decl, ok && decl != nil
}

var _ model.KindTable = stepKindTable{}

var (
	stepLeafID       = model.None("an id of a non-principal row, parsed by canonicalConfigID: workflow_graph.go:373")
	stepLeafSlug     = model.None("a step ref or slug, matched by stepRefPattern: workflow_graph.go:183")
	stepLeafText     = model.None("operator text, bounded by validWorkText and rendered only: workflow_graph.go:378")
	stepLeafClosed   = model.None("a closed value set checked by the step validator: workflow_graph.go:358-368,669,685,781")
	stepLeafTime     = model.None("a canonical timestamp, parsed by canonicalTimestamp: workflow_graph.go:417")
	stepLeafHash     = model.None("a content digest bounded and compared for integrity: workflow_graph.go:566,784")
	stepLeafExternal = model.None("a remote peer, skill or runtime profile, never an account: workflow_graph.go:781-784")
)

var stepParticipantLeaves = model.TypeLeaves(workParticipantConfig{},
	model.Leaf("kind", model.None("a closed participant kind: workflow_graph.go:358")),
	model.Leaf("ref", model.KindRef("kind", "")),
)

const (
	maxFanIn         = 8
	maxFanOut        = 8
	maxStepConfig    = 4096         // bytes of one step's config JSON
	maxWaitSeconds   = 24 * 60 * 60 // a wait is a pacing device, not a scheduler
	defaultMaxWfs    = 200          // workflows per tenant
	defaultMaxSteps  = 50           // steps per workflow
	maxWfDescLen     = 2000
	maxGateReasonLen = 200
	maxEmitLabelLen  = 200
	maxWorkRefLen    = 512
	maxWorkTextLen   = 2048
	maxWorkCriteria  = 16
	maxWorkTTL       = 24 * 60 * 60
)

var stepRefPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type scheduleFireConfig struct {
	ScheduleID string `json:"schedule_id"`
}

type eventingEmitConfig struct {
	Label string `json:"label"`
}

type notifyTestConfig struct {
	RouteID string `json:"route_id"`
}

type waitConfig struct {
	Seconds int64 `json:"seconds"`
}

type approvalGateConfig struct {
	Reason string `json:"reason,omitempty"`
}

type workParticipantConfig struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

type workCriterionConfig struct {
	Key       string `json:"key"`
	Ordinal   int64  `json:"ordinal"`
	Statement string `json:"statement"`
	Required  bool   `json:"required"`
}

type workProvenanceConfig struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Hash string `json:"hash,omitempty"`
}

type workCreateConfig struct {
	WorkspaceID string                `json:"workspace_id"`
	WorkKind    string                `json:"work_kind"`
	Title       string                `json:"title"`
	BriefMD     string                `json:"brief_md,omitempty"`
	BriefRef    string                `json:"brief_ref,omitempty"`
	Priority    string                `json:"priority"`
	Owner       workParticipantConfig `json:"owner"`
	Criteria    []workCriterionConfig `json:"criteria"`
	Provenance  workProvenanceConfig  `json:"provenance"`
	DueAt       string                `json:"due_at,omitempty"`
}

type workAssignConfig struct {
	WorkItemID         string                `json:"work_item_id,omitempty"`
	WorkItemStepRef    string                `json:"work_item_step_ref,omitempty"`
	ExpectedOwnerEpoch int64                 `json:"expected_owner_epoch"`
	Target             workParticipantConfig `json:"target"`
	RequireAck         bool                  `json:"require_ack"`
	ChannelID          string                `json:"channel_id,omitempty"`
	Context            string                `json:"context,omitempty"`
	ContextRef         string                `json:"context_ref,omitempty"`
	AckDeadline        string                `json:"ack_deadline,omitempty"`
}

type workClaimConfig struct {
	WorkItemID      string `json:"work_item_id,omitempty"`
	WorkItemStepRef string `json:"work_item_step_ref,omitempty"`
	SID             string `json:"sid"`
	TTLSeconds      int64  `json:"ttl_seconds"`
}

type sessionLaunchConfig struct {
	WorkItemID        string `json:"work_item_id,omitempty"`
	WorkItemStepRef   string `json:"work_item_step_ref,omitempty"`
	OwnerEpoch        int64  `json:"owner_epoch,omitempty"`
	Fence             int64  `json:"fence,omitempty"`
	FenceStepRef      string `json:"fence_step_ref,omitempty"`
	RuntimeProfileRef string `json:"runtime_profile_ref"`
	AttemptKind       string `json:"attempt_kind,omitempty"`
}

type workMessageConfig struct {
	WorkItemID      string                `json:"work_item_id,omitempty"`
	WorkItemStepRef string                `json:"work_item_step_ref,omitempty"`
	ChannelID       string                `json:"channel_id"`
	Recipient       workParticipantConfig `json:"recipient"`
	Body            string                `json:"body,omitempty"`
	BodyRef         string                `json:"body_ref,omitempty"`
	AckDueAt        string                `json:"ack_due_at,omitempty"`
	Urgency         string                `json:"urgency,omitempty"`
}

type workWaitAckConfig struct {
	TargetKind    string `json:"target_kind"`
	TargetID      string `json:"target_id,omitempty"`
	TargetStepRef string `json:"target_step_ref,omitempty"`
	Deadline      string `json:"deadline"`
	AfterEventSeq int64  `json:"after_event_seq,omitempty"`
}

type workHandoffConfig struct {
	WorkItemID      string                `json:"work_item_id,omitempty"`
	WorkItemStepRef string                `json:"work_item_step_ref,omitempty"`
	ChannelID       string                `json:"channel_id"`
	Target          workParticipantConfig `json:"target"`
	Context         string                `json:"context,omitempty"`
	ContextRef      string                `json:"context_ref,omitempty"`
	AckDeadline     string                `json:"ack_deadline"`
}

type workTransitionConfig struct {
	WorkItemID      string `json:"work_item_id,omitempty"`
	WorkItemStepRef string `json:"work_item_step_ref,omitempty"`
	TargetState     string `json:"target_state"`
	EvidenceRef     string `json:"evidence_ref,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

type workCancelConfig struct {
	WorkItemID      string `json:"work_item_id,omitempty"`
	WorkItemStepRef string `json:"work_item_step_ref,omitempty"`
	BindingID       string `json:"binding_id,omitempty"`
	Reason          string `json:"reason"`
}

type workReconcileConfig struct {
	BindingID string `json:"binding_id"`
}

type remotePlanConfig struct {
	WorkspaceID           string `json:"workspace_id"`
	WorkItemID            string `json:"work_item_id,omitempty"`
	WorkItemStepRef       string `json:"work_item_step_ref,omitempty"`
	BindingSpecID         string `json:"binding_spec_id"`
	BindingSpecGeneration int64  `json:"binding_spec_generation"`
	Protocol              string `json:"protocol"`
	ProtocolVersion       string `json:"protocol_version"`
	Authority             string `json:"authority"`
	AgentRef              string `json:"agent_ref"`
	Skill                 string `json:"skill"`
	Scope                 string `json:"scope"`
	OwnerEpoch            int64  `json:"owner_epoch"`
	LeaseFence            int64  `json:"lease_fence"`
	BriefHash             string `json:"brief_hash"`
	CriteriaRevision      int64  `json:"criteria_revision"`
}

type remoteTestConfig struct {
	PlanStepRef string `json:"plan_step_ref"`
}

type remoteStartConfig struct {
	PlanStepRef string `json:"plan_step_ref"`
}

type remoteBindingConfig struct {
	BindingID      string `json:"binding_id,omitempty"`
	BindingStepRef string `json:"binding_step_ref,omitempty"`
}

type remoteCancelConfig struct {
	BindingID       string `json:"binding_id,omitempty"`
	BindingStepRef  string `json:"binding_step_ref,omitempty"`
	WorkItemID      string `json:"work_item_id,omitempty"`
	WorkItemStepRef string `json:"work_item_step_ref,omitempty"`
	Reason          string `json:"reason"`
}

var (
	workParticipantKinds = map[string]bool{"user": true, "agent": true, "session": true}
	workPriorities       = map[string]bool{"p0": true, "p1": true, "p2": true, "p3": true}
	workProvenanceKinds  = map[string]bool{
		"human": true, "workflow": true, "a2a": true, "mcp": true,
		"migration": true, "system": true,
	}
	workTransitionStates = map[string]bool{
		"ready": true, "blocked": true, "review": true, "completed": true,
		"failed": true, "canceled": true,
	}
	workTransitionReasonRequired = map[string]bool{
		"blocked": true, "failed": true, "canceled": true,
	}
)

type stepDTO struct {
	Ref       string          `json:"ref"`
	Kind      string          `json:"kind"`
	Config    json.RawMessage `json:"config"`
	DependsOn []string        `json:"depends_on"`
}

var workflowStepsDecl = model.Nested([]stepDTO(nil), model.ClassObligation,
	model.Leaf("[].ref", stepLeafSlug),
	model.Leaf("[].kind", stepLeafKind),
	model.Leaf("[].config", model.Union("kind", stepConfigs)),
	model.Leaf("[].depends_on[]", stepLeafSlug),
)

var stepLeafKind = model.None("a step kind, refused unless stepConfigs has it: workflow_graph.go:878")

type graphError struct {
	StepRef string `json:"step_ref,omitempty"`
	Message string `json:"message"`
}

type countedColumn struct {
	name string
	decl *model.ColumnDecl
}

var (
	workflowCountedColumns = []countedColumn{{colWfSteps, workflowStepsDecl}}
	runCountedColumns      = []countedColumn{
		{colWrActor, pdeclRunActor}, {colWrUserIdentity, pdeclRunUserIdentity}, {colWrSteps, runStepsDecl},
	}
	scheduleCountedColumns = []countedColumn{{colOwnerActor, pdeclScheduleOwnerActor}, {colOwnerUserRef, pdeclScheduleOwnerUser}}
)

type scheduleDTO struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	SubjectKind   string `json:"subject_kind"`
	SubjectRef    string `json:"subject_ref"`
	TriggerKind   string `json:"trigger_kind"`
	CadenceSpec   string `json:"cadence_spec,omitempty"`
	ExpectedIvl   int64  `json:"expected_interval_seconds"`
	GraceFactor   int64  `json:"grace_factor"`
	DesiredStatus string `json:"desired_status"`
	OwnerActor    string `json:"owner_actor"`
	LastFiredAt   string `json:"last_fired_at,omitempty"`
	LastObserved  string `json:"last_observed_at,omitempty"`
	MissedAt      string `json:"missed_at,omitempty"`
	Health        string `json:"health"` // active | paused | retired | stalled
	CreatedAt     string `json:"created_at"`
}

const (
	schedRevisionKind  model.Kind = "orchestration.schedule_revision"
	schedRevisionTable            = "orchestration_schedule_revision"

	colRevSubject  = "subject_id" // the schedule this revision belongs to
	colRevOp       = "op"         // create | update | restore
	colRevSnapshot = "snapshot"   // scheduleDTO JSON
	colRevActor    = "actor"
	colRevActorK   = "actor_kind"

	revOpCreate  = "create"
	revOpUpdate  = "update"
	revOpRestore = "restore"
)

func schedRevisionDescriptor() model.EntityDescriptor {
	return model.EntityDescriptor{
		Kind:       schedRevisionKind,
		Table:      schedRevisionTable,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colRevSubject, Kind: model.KindText, Indexed: true, Principal: pdeclNoneRevisionOf},
			{Name: colRevOp, Kind: model.KindText, Principal: pdeclNoneRevisionOp},
			{Name: colRevSnapshot, Kind: model.KindText, Principal: pdeclScheduleSnapshot},
			{Name: colRevActor, Kind: model.KindText, Principal: pdeclActorEvidence},
			{Name: colRevActorK, Kind: model.KindText, Principal: pdeclNoneActorKind},
		},
	}
}

const Name = "olivares.orchestration"

const Namespace = "orchestration"

const (
	permGraphRead     auth.Permission = "orchestration:graph:read"
	permScheduleRead  auth.Permission = "orchestration:schedule:read"
	permScheduleWrite auth.Permission = "orchestration:schedule:write"
	permScheduleAdmin auth.Permission = "orchestration:schedule:admin"
	permWorkflowRead  auth.Permission = "orchestration:workflow:read"
	permWorkflowWrite auth.Permission = "orchestration:workflow:write"
	permWorkflowAdmin auth.Permission = "orchestration:workflow:admin"
)

func eq(col string, value any) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: value}
}

type runStepState struct {
	Ref       string          `json:"ref"`
	Kind      string          `json:"kind"`
	Config    json.RawMessage `json:"config"`
	DependsOn []string        `json:"depends_on"`
	Status    string          `json:"status"`
	// K4 work lineage is part of the durable step snapshot. It is populated
	// before actuation when the graph carries a literal WorkItem and completed
	// by the executor when work-create produces the root dynamically.
	WorkItemID      string `json:"work_item_id,omitempty"`
	CommandID       string `json:"command_id,omitempty"`
	EventSeq        int64  `json:"event_seq,omitempty"`
	OutputKind      string `json:"output_kind,omitempty"`
	OutputID        string `json:"output_id,omitempty"`
	OwnerEpoch      int64  `json:"owner_epoch,omitempty"`
	LeaseFence      int64  `json:"lease_fence,omitempty"`
	AttemptSemantic string `json:"attempt_semantic,omitempty"`
	// K5 remote-work lineage is the bounded durable projection needed to
	// reconcile after restart. It stores identities, hashes and verdicts only;
	// protocol payloads remain outside the workflow snapshot.
	RemoteOutcome               string `json:"remote_outcome,omitempty"`
	RemoteCode                  string `json:"remote_code,omitempty"`
	RemoteObservedAt            string `json:"remote_observed_at,omitempty"`
	RemotePlanHash              string `json:"remote_plan_hash,omitempty"`
	RemoteApprovalRef           string `json:"remote_approval_ref,omitempty"`
	RemoteBindingID             string `json:"remote_binding_id,omitempty"`
	RemoteBindingSpecID         string `json:"remote_binding_spec_id,omitempty"`
	RemoteBindingSpecGeneration int64  `json:"remote_binding_spec_generation,omitempty"`
	RemoteAttemptID             string `json:"remote_attempt_id,omitempty"`
	RemoteGeneration            int64  `json:"remote_generation,omitempty"`
	RemoteSyntheticSID          string `json:"remote_synthetic_sid,omitempty"`
	RemoteResultKind            string `json:"remote_result_kind,omitempty"`
	RemoteTaskID                string `json:"remote_task_id,omitempty"`
	RemoteContextID             string `json:"remote_context_id,omitempty"`
	RemoteMessageID             string `json:"remote_message_id,omitempty"`
	RemoteState                 string `json:"remote_state,omitempty"`
	RemoteRevision              string `json:"remote_revision,omitempty"`
	RemoteTerminal              bool   `json:"remote_terminal,omitempty"`
	RemoteWireHash              string `json:"remote_wire_hash,omitempty"`
	RemoteDetailHash            string `json:"remote_detail_hash,omitempty"`
	RemoteCommandID             string `json:"remote_command_id,omitempty"`
	RemoteEventID               string `json:"remote_event_id,omitempty"`
	RemoteEventSeq              int64  `json:"remote_event_seq,omitempty"`
	RemoteWorkState             string `json:"remote_work_state,omitempty"`
	// A work-wait-ack step persists its exact resume cursor and target. The
	// worker can therefore reconstruct the wait after restart without an
	// in-memory timer or subscription.
	WaitingTargetKind    string `json:"waiting_target_kind,omitempty"`
	WaitingTargetID      string `json:"waiting_target_id,omitempty"`
	WaitingAfterEventSeq int64  `json:"waiting_after_event_seq,omitempty"`
	WaitingDeadline      string `json:"waiting_deadline,omitempty"`
	Detail               string `json:"detail,omitempty"`
	ApprovalRef          string `json:"approval_ref,omitempty"`
	DispatchRef          string `json:"dispatch_ref,omitempty"`
	NotBefore            string `json:"not_before,omitempty"`
	At                   string `json:"at,omitempty"` // last transition instant
	// D-06: the APPROVED target binding frozen at run creation. Execution
	// recomputes the fingerprint against the CURRENT config and BLOCKS on any
	// change (a re-pointed schedule/route, a rotated secret). An acting step with
	// an empty BindProfile could not be bound (no HMAC key) and is BLOCKED — a
	// target that cannot be verified is never actuated. Opaque only: no URL/
	// command/header/secret is ever stored here.
	BindProfile    string `json:"bind_profile,omitempty"`
	ApprovedTarget string `json:"approved_target,omitempty"`
	MacKeyID       string `json:"mac_key_id,omitempty"`
	Generation     string `json:"generation,omitempty"`
	// RouteFp is the notify route's OWN opaque fingerprint (pre-HMAC) frozen at
	// approval, handed to the atomic notify seam so it can refuse a re-pointed
	// route from a SINGLE read that also delivers (hole c1). Empty for
	// non-notify steps.
	RouteFp string `json:"route_fp,omitempty"`
	// ReauthResume is, for a reauthentication_required step, the status it
	// resumes into once the run is reauthorized: pending for a refused claim,
	// waiting_ack for a refused acknowledgement poll.
	ReauthResume string `json:"reauth_resume,omitempty"`
}

var runStepsDecl = model.Nested([]runStepState(nil), model.ClassObligation,
	model.Leaf("[].ref", stepLeafSlug), model.Leaf("[].kind", stepLeafKind),
	model.Leaf("[].config", model.Union("kind", stepConfigs)), model.Leaf("[].depends_on[]", stepLeafSlug),
	model.Leaf("[].status", runLeafClosed), model.Leaf("[].work_item_id", runLeafLineage),
	model.Leaf("[].command_id", runLeafLineage), model.Leaf("[].output_kind", runLeafClosed),
	model.Leaf("[].output_id", runLeafLineage), model.Leaf("[].attempt_semantic", runLeafClosed),
	model.Leaf("[].remote_outcome", runLeafRemote), model.Leaf("[].remote_code", runLeafRemote),
	model.Leaf("[].remote_observed_at", runLeafRemote), model.Leaf("[].remote_plan_hash", runLeafRemote),
	model.Leaf("[].remote_approval_ref", runLeafRemote), model.Leaf("[].remote_binding_id", runLeafRemote),
	model.Leaf("[].remote_binding_spec_id", runLeafRemote), model.Leaf("[].remote_attempt_id", runLeafRemote),
	model.Leaf("[].remote_synthetic_sid", runLeafRemote), model.Leaf("[].remote_result_kind", runLeafRemote),
	model.Leaf("[].remote_task_id", runLeafRemote), model.Leaf("[].remote_context_id", runLeafRemote),
	model.Leaf("[].remote_message_id", runLeafRemote), model.Leaf("[].remote_state", runLeafRemote),
	model.Leaf("[].remote_revision", runLeafRemote), model.Leaf("[].remote_wire_hash", runLeafRemote),
	model.Leaf("[].remote_detail_hash", runLeafRemote), model.Leaf("[].remote_command_id", runLeafRemote),
	model.Leaf("[].remote_event_id", runLeafRemote), model.Leaf("[].remote_work_state", runLeafRemote),
	model.Leaf("[].waiting_target_kind", stepLeafClosed), model.Leaf("[].waiting_target_id", stepLeafID),
	model.Leaf("[].waiting_deadline", stepLeafTime), model.Leaf("[].detail", runLeafDetail),
	model.Leaf("[].approval_ref", runLeafLineage), model.Leaf("[].dispatch_ref", runLeafLineage),
	model.Leaf("[].not_before", runLeafLineage), model.Leaf("[].at", runLeafLineage),
	model.Leaf("[].bind_profile", runLeafBinding), model.Leaf("[].approved_target", runLeafBinding),
	model.Leaf("[].mac_key_id", runLeafBinding), model.Leaf("[].generation", runLeafBinding),
	model.Leaf("[].route_fp", runLeafBinding), model.Leaf("[].reauth_resume", runLeafClosed),
)

const listCap = 1000

const (
	maxNameLen  = 200
	maxRefLen   = 1024
	maxReqBytes = 1 << 22 // 4 MiB cap on a request body
)

const (
	defaultActiveWindow = 2 * time.Minute  // activity within → active
	defaultIdleWindow   = 30 * time.Minute // within → idle; beyond → ended/completed
)

const (
	stateActive    = "active"
	stateIdle      = "idle"
	stateCompleted = "completed" // a flow/worker that went silent (finished — honest)
	stateStalled   = "stalled"   // an active recurring schedule overdue vs its cadence (anti-evasion)
)

var (
	runLeafClosed  = model.None("a closed status, output or attempt value set by the executor: workflow_run.go:65,754")
	runLeafLineage = model.None("an id, timestamp or ref of the run's own work, recorded by the executor: workflow_run.go:1508-1512,1556-1567")
	runLeafRemote  = model.None("the remote protocol's bounded projection, ids, hashes and verdicts only: workflow_run.go:1515-1537")
	runLeafDetail  = model.None("an operator-facing status line, clamped and rendered only: workflow_run.go:1558")
	runLeafBinding = model.None("an opaque target-binding fingerprint and key id: workflow_run.go:782-783")
)
