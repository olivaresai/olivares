// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
)

// workflow_graph.go is the server-side truth about a workflow's step
// graph: the typed step vocabulary, the per-kind config contracts, and the
// whole-graph validation (acyclicity via Kahn toposort, existing refs, bounded
// fan-in/fan-out, bounded size). The editor mirrors these rules for live
// feedback, but THIS is the enforcement — a client that skips the editor meets
// the same wall.

// Step kinds — the CLOSED set of things a workflow step can do. Every kind is
// an in-plane, already-governed verb; there is deliberately NO arbitrary
// side-effect kind (HTTP/exec — out of scope by decision pack) and NO
// arbitrary event emission (a step emits the FIXED workflow.signal type only,
// so an editor-tier user can never forge a first-party event like
// edge.observed into another module's ingestion).
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

var validStepKinds = map[string]bool{
	stepScheduleFire:   true,
	stepEventingEmit:   true,
	stepNotifyTest:     true,
	stepWait:           true,
	stepApprovalGate:   true,
	stepWorkCreate:     true,
	stepWorkAssign:     true,
	stepWorkClaim:      true,
	stepSessionLaunch:  true,
	stepWorkMessage:    true,
	stepWorkWaitAck:    true,
	stepWorkHandoff:    true,
	stepWorkTransition: true,
	stepWorkCancel:     true,
	stepWorkReconcile:  true,
	stepRemotePlan:     true,
	stepRemoteTest:     true,
	stepRemoteStart:    true,
	stepRemoteObserve:  true,
	stepRemoteCancel:   true,
}

// Graph bounds. Fan-in/fan-out caps keep a graph reviewable by the human who
// approves its plan (an unbounded star is a rubber stamp, not a review);
// per-tenant workflow/step caps are composition-root-configurable (Options).
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

// stepRefPattern bounds a step ref to a short, log-safe slug (it is quoted in
// ledger results, approval subject refs and UI labels).
var stepRefPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Per-kind config contracts. Each is decoded with DisallowUnknownFields so a
// client cannot smuggle extra keys past review, and re-marshaled to its
// canonical form for storage and hashing (deterministic field order).
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

// workParticipantConfig is orchestration's neutral participant vocabulary.
// The composition root adapts it to sessions without creating a module import.
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

func canonicalConfigID(raw string) (string, bool) {
	id, err := model.ParseID(strings.TrimSpace(raw))
	return id.String(), err == nil && !id.IsZero()
}

func validWorkText(raw string, max int) bool {
	return raw != "" && len(raw) <= max && utf8.ValidString(raw)
}

func canonicalParticipant(in workParticipantConfig) (workParticipantConfig, bool) {
	in.Kind = strings.TrimSpace(in.Kind)
	in.Ref = strings.TrimSpace(in.Ref)
	return in, workParticipantKinds[in.Kind] && validWorkText(in.Ref, maxWorkRefLen)
}

// canonicalWorkItemSelector accepts a literal WorkItem, an explicit upstream
// step output, or neither (meaning the run's durable root_work_item_id). It
// never accepts two authorities for the same reference.
func canonicalWorkItemSelector(id, stepRef *string) bool {
	*id, *stepRef = strings.TrimSpace(*id), strings.TrimSpace(*stepRef)
	if *id != "" && *stepRef != "" {
		return false
	}
	if *id != "" {
		var ok bool
		*id, ok = canonicalConfigID(*id)
		return ok
	}
	return *stepRef == "" || stepRefPattern.MatchString(*stepRef)
}

func canonicalBindingSelector(id, stepRef *string) bool {
	*id, *stepRef = strings.TrimSpace(*id), strings.TrimSpace(*stepRef)
	if (*id == "") == (*stepRef == "") {
		return false
	}
	if *id != "" {
		var ok bool
		*id, ok = canonicalConfigID(*id)
		return ok
	}
	return stepRefPattern.MatchString(*stepRef)
}

func canonicalTimestamp(raw string, required bool) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", !required
	}
	ts, err := model.ParseTimestamp(raw)
	if err != nil {
		return "", false
	}
	return ts.String(), true
}

func isWorkStepKind(kind string) bool {
	switch kind {
	case stepWorkCreate, stepWorkAssign, stepWorkClaim, stepSessionLaunch,
		stepWorkMessage, stepWorkWaitAck, stepWorkHandoff, stepWorkTransition,
		stepWorkCancel, stepWorkReconcile, stepRemotePlan, stepRemoteTest,
		stepRemoteStart, stepRemoteObserve, stepRemoteCancel:
		return true
	default:
		return false
	}
}

func isRemoteStepKind(kind string) bool {
	switch kind {
	case stepRemotePlan, stepRemoteTest, stepRemoteStart, stepRemoteObserve, stepRemoteCancel:
		return true
	default:
		return false
	}
}

// stepDTO is one node of the step graph as stored, hashed and served: the step
// ref (unique in the workflow), its kind, the kind's canonical config and the
// refs it depends on.
type stepDTO struct {
	Ref       string          `json:"ref"`
	Kind      string          `json:"kind"`
	Config    json.RawMessage `json:"config"`
	DependsOn []string        `json:"depends_on"`
}

// graphError is a structured validation failure: which step (empty for a
// whole-graph failure) and why. The editor surfaces it verbatim next to the
// offending node.
type graphError struct {
	StepRef string `json:"step_ref,omitempty"`
	Message string `json:"message"`
}

func (e graphError) Error() string {
	if e.StepRef == "" {
		return e.Message
	}
	return "step " + e.StepRef + ": " + e.Message
}

// canonicalStepConfig decodes raw against kind's typed contract and returns the
// canonical (re-marshaled) form. It is the ONLY path a config enters the store
// through, so stored configs are always shaped, bounded and canonical.
func canonicalStepConfig(kind string, raw json.RawMessage) (json.RawMessage, *graphError) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > maxStepConfig {
		return nil, &graphError{Message: fmt.Sprintf("config exceeds %d bytes", maxStepConfig)}
	}
	strict := func(v any) *graphError {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			return &graphError{Message: "invalid config for kind " + kind}
		}
		return nil
	}
	switch kind {
	case stepScheduleFire:
		var c scheduleFireConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		if strings.TrimSpace(c.ScheduleID) == "" {
			return nil, &graphError{Message: "schedule-fire requires config.schedule_id"}
		}
		return mustJSON(c), nil
	case stepEventingEmit:
		var c eventingEmitConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		if strings.TrimSpace(c.Label) == "" {
			return nil, &graphError{Message: "eventing-emit requires config.label"}
		}
		c.Label = clamp(c.Label, maxEmitLabelLen)
		return mustJSON(c), nil
	case stepNotifyTest:
		var c notifyTestConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		if strings.TrimSpace(c.RouteID) == "" {
			return nil, &graphError{Message: "notify-test requires config.route_id"}
		}
		return mustJSON(c), nil
	case stepWait:
		var c waitConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		if c.Seconds < 1 || c.Seconds > maxWaitSeconds {
			return nil, &graphError{Message: fmt.Sprintf("wait requires config.seconds between 1 and %d", maxWaitSeconds)}
		}
		return mustJSON(c), nil
	case stepApprovalGate:
		var c approvalGateConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		c.Reason = clamp(c.Reason, maxGateReasonLen)
		return mustJSON(c), nil
	case stepWorkCreate:
		var c workCreateConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		var ok bool
		if c.WorkspaceID, ok = canonicalConfigID(c.WorkspaceID); !ok {
			return nil, &graphError{Message: "work-create requires a valid config.workspace_id"}
		}
		c.WorkKind, c.Title = strings.TrimSpace(c.WorkKind), strings.TrimSpace(c.Title)
		c.BriefRef, c.Priority = strings.TrimSpace(c.BriefRef), strings.TrimSpace(c.Priority)
		if !stepRefPattern.MatchString(c.WorkKind) || !validWorkText(c.Title, 256) ||
			!workPriorities[c.Priority] || (c.BriefMD == "") == (c.BriefRef == "") ||
			(c.BriefMD != "" && !validWorkText(c.BriefMD, maxWorkTextLen)) ||
			(c.BriefRef != "" && !validWorkText(c.BriefRef, maxWorkRefLen)) {
			return nil, &graphError{Message: "invalid config for kind work-create"}
		}
		if c.Owner, ok = canonicalParticipant(c.Owner); !ok {
			return nil, &graphError{Message: "work-create requires a valid config.owner"}
		}
		c.Provenance.Kind = strings.TrimSpace(c.Provenance.Kind)
		c.Provenance.Ref = strings.TrimSpace(c.Provenance.Ref)
		c.Provenance.Hash = strings.TrimSpace(c.Provenance.Hash)
		if !workProvenanceKinds[c.Provenance.Kind] ||
			!validWorkText(c.Provenance.Ref, maxWorkRefLen) ||
			(c.Provenance.Hash != "" && !validWorkText(c.Provenance.Hash, 128)) {
			return nil, &graphError{Message: "work-create requires valid config.provenance"}
		}
		if c.DueAt, ok = canonicalTimestamp(c.DueAt, false); !ok {
			return nil, &graphError{Message: "work-create config.due_at must be canonical"}
		}
		if len(c.Criteria) == 0 || len(c.Criteria) > maxWorkCriteria {
			return nil, &graphError{Message: fmt.Sprintf("work-create requires 1..%d criteria", maxWorkCriteria)}
		}
		keys, ordinals, required := map[string]bool{}, map[int64]bool{}, 0
		for i := range c.Criteria {
			criterion := &c.Criteria[i]
			criterion.Key = strings.TrimSpace(criterion.Key)
			criterion.Statement = strings.TrimSpace(criterion.Statement)
			if !stepRefPattern.MatchString(criterion.Key) || criterion.Ordinal < 1 ||
				!validWorkText(criterion.Statement, 1024) || keys[criterion.Key] || ordinals[criterion.Ordinal] {
				return nil, &graphError{Message: "work-create contains an invalid or duplicate criterion"}
			}
			keys[criterion.Key], ordinals[criterion.Ordinal] = true, true
			if criterion.Required {
				required++
			}
		}
		if required == 0 {
			return nil, &graphError{Message: "work-create requires at least one required criterion"}
		}
		return mustJSON(c), nil
	case stepWorkAssign:
		var c workAssignConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		var ok bool
		if !canonicalWorkItemSelector(&c.WorkItemID, &c.WorkItemStepRef) || c.ExpectedOwnerEpoch < 1 {
			return nil, &graphError{Message: "work-assign requires a work selector and positive expected_owner_epoch"}
		}
		if c.Target, ok = canonicalParticipant(c.Target); !ok {
			return nil, &graphError{Message: "work-assign requires a valid config.target"}
		}
		c.ChannelID, c.ContextRef = strings.TrimSpace(c.ChannelID), strings.TrimSpace(c.ContextRef)
		if c.RequireAck {
			if c.ChannelID, ok = canonicalConfigID(c.ChannelID); !ok ||
				(c.Context == "") == (c.ContextRef == "") ||
				(c.Context != "" && !validWorkText(c.Context, maxWorkTextLen)) ||
				(c.ContextRef != "" && !validWorkText(c.ContextRef, maxWorkRefLen)) {
				return nil, &graphError{Message: "work-assign require_ack needs channel_id and exactly one context or context_ref"}
			}
			if c.AckDeadline, ok = canonicalTimestamp(c.AckDeadline, true); !ok {
				return nil, &graphError{Message: "work-assign require_ack needs a canonical ack_deadline"}
			}
		} else if c.ChannelID != "" || c.Context != "" || c.ContextRef != "" || c.AckDeadline != "" {
			return nil, &graphError{Message: "work-assign handoff fields require require_ack=true"}
		}
		return mustJSON(c), nil
	case stepWorkClaim:
		var c workClaimConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		c.SID = strings.TrimSpace(c.SID)
		if !canonicalWorkItemSelector(&c.WorkItemID, &c.WorkItemStepRef) ||
			!validWorkText(c.SID, maxWorkRefLen) || c.TTLSeconds < 1 || c.TTLSeconds > maxWorkTTL {
			return nil, &graphError{Message: fmt.Sprintf("work-claim requires a work selector, sid and ttl_seconds between 1 and %d", maxWorkTTL)}
		}
		return mustJSON(c), nil
	case stepSessionLaunch:
		var c sessionLaunchConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		c.RuntimeProfileRef = strings.TrimSpace(c.RuntimeProfileRef)
		c.AttemptKind = strings.TrimSpace(c.AttemptKind)
		c.FenceStepRef = strings.TrimSpace(c.FenceStepRef)
		if c.AttemptKind == "" {
			c.AttemptKind = "lease-bind"
		}
		if !canonicalWorkItemSelector(&c.WorkItemID, &c.WorkItemStepRef) || c.OwnerEpoch < 0 ||
			c.Fence < 0 || (c.Fence > 0 && c.FenceStepRef != "") ||
			(c.FenceStepRef != "" && !stepRefPattern.MatchString(c.FenceStepRef)) ||
			!validWorkText(c.RuntimeProfileRef, maxWorkRefLen) ||
			!stepRefPattern.MatchString(c.AttemptKind) {
			return nil, &graphError{Message: "session-launch requires a work selector, optional single fence precondition, runtime_profile_ref and attempt_kind"}
		}
		return mustJSON(c), nil
	case stepWorkMessage:
		var c workMessageConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		var ok bool
		if !canonicalWorkItemSelector(&c.WorkItemID, &c.WorkItemStepRef) {
			return nil, &graphError{Message: "work-message requires a valid work selector"}
		}
		if c.ChannelID, ok = canonicalConfigID(c.ChannelID); !ok {
			return nil, &graphError{Message: "work-message requires a valid config.channel_id"}
		}
		if c.Recipient, ok = canonicalParticipant(c.Recipient); !ok {
			return nil, &graphError{Message: "work-message requires an exact config.recipient"}
		}
		c.BodyRef, c.Urgency = strings.TrimSpace(c.BodyRef), strings.TrimSpace(c.Urgency)
		if (c.Body == "") == (c.BodyRef == "") ||
			(c.Body != "" && !validWorkText(c.Body, maxWorkTextLen)) ||
			(c.BodyRef != "" && !validWorkText(c.BodyRef, maxWorkRefLen)) ||
			(c.Urgency != "" && c.Urgency != "normal" && c.Urgency != "high" && c.Urgency != "critical") {
			return nil, &graphError{Message: "invalid config for kind work-message"}
		}
		if c.AckDueAt, ok = canonicalTimestamp(c.AckDueAt, false); !ok {
			return nil, &graphError{Message: "work-message config.ack_due_at must be canonical"}
		}
		return mustJSON(c), nil
	case stepWorkWaitAck:
		var c workWaitAckConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		var ok bool
		c.TargetKind, c.TargetStepRef = strings.TrimSpace(c.TargetKind), strings.TrimSpace(c.TargetStepRef)
		if (c.TargetID == "") == (c.TargetStepRef == "") ||
			(c.TargetStepRef != "" && !stepRefPattern.MatchString(c.TargetStepRef)) ||
			(c.TargetKind != "message" && c.TargetKind != "handoff") || c.AfterEventSeq < 0 {
			return nil, &graphError{Message: "work-wait-ack requires message/handoff target and non-negative after_event_seq"}
		}
		if c.TargetID != "" {
			if c.TargetID, ok = canonicalConfigID(c.TargetID); !ok {
				return nil, &graphError{Message: "work-wait-ack config.target_id must be valid"}
			}
		}
		if c.Deadline, ok = canonicalTimestamp(c.Deadline, true); !ok {
			return nil, &graphError{Message: "work-wait-ack requires canonical config.deadline"}
		}
		return mustJSON(c), nil
	case stepWorkHandoff:
		var c workHandoffConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		var ok bool
		if !canonicalWorkItemSelector(&c.WorkItemID, &c.WorkItemStepRef) {
			return nil, &graphError{Message: "work-handoff requires a valid work selector"}
		}
		if c.ChannelID, ok = canonicalConfigID(c.ChannelID); !ok {
			return nil, &graphError{Message: "work-handoff requires a valid config.channel_id"}
		}
		if c.Target, ok = canonicalParticipant(c.Target); !ok {
			return nil, &graphError{Message: "work-handoff requires a valid config.target"}
		}
		c.ContextRef = strings.TrimSpace(c.ContextRef)
		if (c.Context == "") == (c.ContextRef == "") ||
			(c.Context != "" && !validWorkText(c.Context, maxWorkTextLen)) ||
			(c.ContextRef != "" && !validWorkText(c.ContextRef, maxWorkRefLen)) {
			return nil, &graphError{Message: "work-handoff requires exactly one context or context_ref"}
		}
		if c.AckDeadline, ok = canonicalTimestamp(c.AckDeadline, true); !ok {
			return nil, &graphError{Message: "work-handoff requires canonical config.ack_deadline"}
		}
		return mustJSON(c), nil
	case stepWorkTransition:
		var c workTransitionConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		c.TargetState, c.EvidenceRef, c.Reason = strings.TrimSpace(c.TargetState), strings.TrimSpace(c.EvidenceRef), strings.TrimSpace(c.Reason)
		if !canonicalWorkItemSelector(&c.WorkItemID, &c.WorkItemStepRef) || !workTransitionStates[c.TargetState] ||
			(c.EvidenceRef != "" && !validWorkText(c.EvidenceRef, maxWorkRefLen)) ||
			(workTransitionReasonRequired[c.TargetState] && !validWorkText(c.Reason, maxWorkTextLen)) ||
			(!workTransitionReasonRequired[c.TargetState] && c.Reason != "" && !validWorkText(c.Reason, maxWorkTextLen)) {
			return nil, &graphError{Message: "invalid config for kind work-transition"}
		}
		return mustJSON(c), nil
	case stepWorkCancel:
		var c workCancelConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		var ok bool
		c.Reason = strings.TrimSpace(c.Reason)
		if !canonicalWorkItemSelector(&c.WorkItemID, &c.WorkItemStepRef) || !validWorkText(c.Reason, maxWorkTextLen) {
			return nil, &graphError{Message: "work-cancel requires a work selector and reason"}
		}
		if c.BindingID != "" {
			if c.BindingID, ok = canonicalConfigID(c.BindingID); !ok {
				return nil, &graphError{Message: "work-cancel config.binding_id must be valid"}
			}
		}
		return mustJSON(c), nil
	case stepWorkReconcile:
		var c workReconcileConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		var ok bool
		if c.BindingID, ok = canonicalConfigID(c.BindingID); !ok {
			return nil, &graphError{Message: "work-reconcile requires a valid config.binding_id"}
		}
		return mustJSON(c), nil
	case stepRemotePlan:
		var c remotePlanConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		var ok bool
		if c.WorkspaceID, ok = canonicalConfigID(c.WorkspaceID); !ok ||
			!canonicalWorkItemSelector(&c.WorkItemID, &c.WorkItemStepRef) {
			return nil, &graphError{Message: "remote-plan requires a workspace and work selector"}
		}
		if c.BindingSpecID, ok = canonicalConfigID(c.BindingSpecID); !ok || c.BindingSpecGeneration < 1 {
			return nil, &graphError{Message: "remote-plan requires a pinned binding spec generation"}
		}
		c.Protocol = strings.TrimSpace(c.Protocol)
		c.ProtocolVersion = strings.TrimSpace(c.ProtocolVersion)
		c.Authority = strings.TrimSpace(c.Authority)
		c.AgentRef = strings.TrimSpace(c.AgentRef)
		c.Skill = strings.TrimSpace(c.Skill)
		c.Scope = strings.TrimSpace(c.Scope)
		c.BriefHash = strings.TrimSpace(c.BriefHash)
		if (c.Protocol != "a2a" && c.Protocol != "mcp") ||
			!validWorkText(c.ProtocolVersion, 64) || !validWorkText(c.Authority, maxWorkRefLen) ||
			!validWorkText(c.AgentRef, maxWorkRefLen) || !validWorkText(c.Skill, maxWorkRefLen) ||
			!validWorkText(c.Scope, maxWorkRefLen) || !validWorkText(c.BriefHash, 128) ||
			c.OwnerEpoch < 1 || c.LeaseFence < 1 || c.CriteriaRevision < 1 {
			return nil, &graphError{Message: "remote-plan requires a pinned protocol, peer, work tuple and content revisions"}
		}
		return mustJSON(c), nil
	case stepRemoteTest:
		var c remoteTestConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		c.PlanStepRef = strings.TrimSpace(c.PlanStepRef)
		if !stepRefPattern.MatchString(c.PlanStepRef) {
			return nil, &graphError{Message: "remote-test requires config.plan_step_ref"}
		}
		return mustJSON(c), nil
	case stepRemoteStart:
		var c remoteStartConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		c.PlanStepRef = strings.TrimSpace(c.PlanStepRef)
		if !stepRefPattern.MatchString(c.PlanStepRef) {
			return nil, &graphError{Message: "remote-start requires config.plan_step_ref"}
		}
		return mustJSON(c), nil
	case stepRemoteObserve:
		var c remoteBindingConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		if !canonicalBindingSelector(&c.BindingID, &c.BindingStepRef) {
			return nil, &graphError{Message: "remote-observe requires exactly one binding_id or binding_step_ref"}
		}
		return mustJSON(c), nil
	case stepRemoteCancel:
		var c remoteCancelConfig
		if ge := strict(&c); ge != nil {
			return nil, ge
		}
		c.Reason = strings.TrimSpace(c.Reason)
		if !canonicalBindingSelector(&c.BindingID, &c.BindingStepRef) ||
			!canonicalWorkItemSelector(&c.WorkItemID, &c.WorkItemStepRef) ||
			!validWorkText(c.Reason, maxWorkTextLen) {
			return nil, &graphError{Message: "remote-cancel requires binding/work selectors and a reason"}
		}
		return mustJSON(c), nil
	default:
		return nil, &graphError{Message: "unknown step kind " + kind}
	}
}

// mustJSON marshals a config struct that cannot fail (plain strings/ints).
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		// Unreachable for the typed configs; fail loud in tests if it ever isn't.
		panic(err)
	}
	return b
}

// validateGraph checks the whole step set and returns the canonicalized steps
// (configs re-marshaled, depends_on sorted) in TOPOLOGICAL order plus the
// first validation failure, if any. maxSteps is the module's configured cap.
func validateGraph(steps []stepDTO, maxSteps int) ([]stepDTO, *graphError) {
	if len(steps) == 0 {
		return []stepDTO{}, nil
	}
	if len(steps) > maxSteps {
		return nil, &graphError{Message: fmt.Sprintf("workflow exceeds %d steps", maxSteps)}
	}

	byRef := make(map[string]int, len(steps))
	canon := make([]stepDTO, len(steps))
	for i, s := range steps {
		if !stepRefPattern.MatchString(s.Ref) {
			return nil, &graphError{StepRef: s.Ref, Message: "ref must match [a-z0-9][a-z0-9_-]{0,63}"}
		}
		if _, dup := byRef[s.Ref]; dup {
			return nil, &graphError{StepRef: s.Ref, Message: "duplicate step ref"}
		}
		if !validStepKinds[s.Kind] {
			return nil, &graphError{StepRef: s.Ref, Message: "unknown step kind " + s.Kind}
		}
		cfg, ge := canonicalStepConfig(s.Kind, s.Config)
		if ge != nil {
			ge.StepRef = s.Ref
			return nil, ge
		}
		deps := append([]string(nil), s.DependsOn...)
		sort.Strings(deps)
		byRef[s.Ref] = i
		canon[i] = stepDTO{Ref: s.Ref, Kind: s.Kind, Config: cfg, DependsOn: deps}
	}

	// Edges: dep must exist, no self-dep, no duplicate dep, bounded fan-in/out.
	fanOut := make(map[string]int, len(steps))
	for _, s := range canon {
		if len(s.DependsOn) > maxFanIn {
			return nil, &graphError{StepRef: s.Ref, Message: fmt.Sprintf("fan-in exceeds %d", maxFanIn)}
		}
		seen := map[string]bool{}
		for _, d := range s.DependsOn {
			if d == s.Ref {
				return nil, &graphError{StepRef: s.Ref, Message: "step depends on itself"}
			}
			if _, ok := byRef[d]; !ok {
				return nil, &graphError{StepRef: s.Ref, Message: "depends_on references unknown step " + d}
			}
			if seen[d] {
				return nil, &graphError{StepRef: s.Ref, Message: "duplicate dependency " + d}
			}
			seen[d] = true
			fanOut[d]++
		}
	}
	// Report the offending nodes in ref order: ranging a map would pin the
	// failure to a different node run to run for the same graph, which reads as
	// a flaky validator to whoever is trying to fix their workflow.
	overFanOut := make([]string, 0, len(fanOut))
	for ref, n := range fanOut {
		if n > maxFanOut {
			overFanOut = append(overFanOut, ref)
		}
	}
	if len(overFanOut) > 0 {
		sort.Strings(overFanOut)
		return nil, &graphError{StepRef: overFanOut[0], Message: fmt.Sprintf("fan-out exceeds %d", maxFanOut)}
	}
	if ge := validateK4StepReferences(canon, byRef); ge != nil {
		return nil, ge
	}

	ordered, ok := topoOrder(canon, byRef)
	if !ok {
		return nil, &graphError{Message: "the step graph contains a cycle"}
	}
	return ordered, nil
}

func validateK4StepReferences(steps []stepDTO, byRef map[string]int) *graphError {
	directDependency := func(step stepDTO, ref string) bool {
		for _, dep := range step.DependsOn {
			if dep == ref {
				return true
			}
		}
		return false
	}
	for _, step := range steps {
		if ref := stepWorkItemStepRef(step); ref != "" {
			i, ok := byRef[ref]
			if !ok || !directDependency(step, ref) || !stepProducesWorkItem(steps[i].Kind) {
				return &graphError{StepRef: step.Ref, Message: "work_item_step_ref must name a direct dependency that produces a WorkItem"}
			}
		}
		switch step.Kind {
		case stepSessionLaunch:
			var cfg sessionLaunchConfig
			_ = json.Unmarshal(step.Config, &cfg)
			if cfg.FenceStepRef != "" {
				i, ok := byRef[cfg.FenceStepRef]
				if !ok || !directDependency(step, cfg.FenceStepRef) || steps[i].Kind != stepWorkClaim {
					return &graphError{StepRef: step.Ref, Message: "fence_step_ref must name a direct work-claim dependency"}
				}
			}
		case stepWorkWaitAck:
			var cfg workWaitAckConfig
			_ = json.Unmarshal(step.Config, &cfg)
			if cfg.TargetStepRef == "" {
				continue
			}
			i, ok := byRef[cfg.TargetStepRef]
			wantKind := stepWorkMessage
			if cfg.TargetKind == "handoff" {
				wantKind = stepWorkHandoff
			}
			if !ok || !directDependency(step, cfg.TargetStepRef) || steps[i].Kind != wantKind {
				return &graphError{StepRef: step.Ref, Message: "target_step_ref must name a direct dependency with the matching target kind"}
			}
		case stepRemoteTest:
			var cfg remoteTestConfig
			_ = json.Unmarshal(step.Config, &cfg)
			i, ok := byRef[cfg.PlanStepRef]
			if !ok || !directDependency(step, cfg.PlanStepRef) || steps[i].Kind != stepRemotePlan {
				return &graphError{StepRef: step.Ref, Message: "plan_step_ref must name a direct remote-plan dependency"}
			}
		case stepRemoteStart:
			var cfg remoteStartConfig
			_ = json.Unmarshal(step.Config, &cfg)
			i, ok := byRef[cfg.PlanStepRef]
			if !ok || !directDependency(step, cfg.PlanStepRef) || steps[i].Kind != stepRemotePlan {
				return &graphError{StepRef: step.Ref, Message: "plan_step_ref must name a direct remote-plan dependency"}
			}
		case stepRemoteObserve:
			var cfg remoteBindingConfig
			_ = json.Unmarshal(step.Config, &cfg)
			if cfg.BindingStepRef != "" {
				i, ok := byRef[cfg.BindingStepRef]
				if !ok || !directDependency(step, cfg.BindingStepRef) || !stepProducesRemoteBinding(steps[i].Kind) {
					return &graphError{StepRef: step.Ref, Message: "binding_step_ref must name a direct dependency that produces a remote binding"}
				}
			}
		case stepRemoteCancel:
			var cfg remoteCancelConfig
			_ = json.Unmarshal(step.Config, &cfg)
			if cfg.BindingStepRef != "" {
				i, ok := byRef[cfg.BindingStepRef]
				if !ok || !directDependency(step, cfg.BindingStepRef) || !stepProducesRemoteBinding(steps[i].Kind) {
					return &graphError{StepRef: step.Ref, Message: "binding_step_ref must name a direct dependency that produces a remote binding"}
				}
			}
		}
	}
	return nil
}

func stepProducesWorkItem(kind string) bool {
	switch kind {
	case stepWorkCreate, stepWorkAssign, stepWorkClaim, stepSessionLaunch,
		stepWorkMessage, stepWorkHandoff, stepWorkTransition, stepWorkCancel,
		stepRemotePlan, stepRemoteTest, stepRemoteStart, stepRemoteObserve,
		stepRemoteCancel:
		return true
	default:
		return false
	}
}

func stepProducesRemoteBinding(kind string) bool {
	switch kind {
	case stepRemoteStart, stepRemoteObserve, stepRemoteCancel:
		return true
	default:
		return false
	}
}

// topoOrder returns the steps in Kahn topological order, deterministic for a
// given graph (ready steps are taken in ref order). ok=false on a cycle.
func topoOrder(steps []stepDTO, byRef map[string]int) ([]stepDTO, bool) {
	indeg := make(map[string]int, len(steps))
	dependents := make(map[string][]string, len(steps))
	for _, s := range steps {
		indeg[s.Ref] = len(s.DependsOn)
		for _, d := range s.DependsOn {
			dependents[d] = append(dependents[d], s.Ref)
		}
	}
	var ready []string
	for _, s := range steps {
		if indeg[s.Ref] == 0 {
			ready = append(ready, s.Ref)
		}
	}
	sort.Strings(ready)
	out := make([]stepDTO, 0, len(steps))
	for len(ready) > 0 {
		ref := ready[0]
		ready = ready[1:]
		out = append(out, steps[byRef[ref]])
		next := append([]string(nil), dependents[ref]...)
		sort.Strings(next)
		for _, dep := range next {
			indeg[dep]--
			if indeg[dep] == 0 {
				ready = append(ready, dep)
				sort.Strings(ready)
			}
		}
	}
	return out, len(out) == len(steps)
}

// planHashOfWorkflow binds an approval to the EXACT graph a human saw: the
// workflow identity, every step's canonical (ref, kind, config, deps) sorted by
// ref, AND — for the steps that actuate something — the identity of what they
// would actuate (targets).
//
// Hashing the graph alone was not enough. A schedule-fire step's config only
// names a schedule ID; the agent that actually gets run lives on the schedule
// row. The direct fire path has always hashed the schedule's own subject and
// cadence (planHashOfSchedule), so re-targeting a schedule voids an approval
// there. Without targets here, a principal holding only schedule:write —
// one tier BELOW the admin a direct fire needs — could re-point a schedule
// between the approval and the run, and the run would actuate the new target
// under the human's unchanged, still-valid "yes". Targets close that: the
// binding covers what the human was actually approving, not just its name.
//
// targets maps a step ref to the resolved identity of its target; a step with
// no entry contributes an empty binding, so an unresolvable target changes the
// hash rather than silently matching.
func planHashOfWorkflow(id, name string, steps []stepDTO, targets map[string]string) string {
	sorted := append([]stepDTO(nil), steps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Ref < sorted[j].Ref })
	// Canonical (length-prefixed) preimage: every field — ref, kind, config,
	// deps, target — is length-pinned so no controllable value can shift a
	// boundary to forge a matching hash (canonicalization).
	fields := make([]string, 0, 3+len(sorted)*5)
	fields = append(fields, id, name)
	for _, s := range sorted {
		fields = append(fields, s.Ref, s.Kind, string(s.Config), strings.Join(s.DependsOn, "\x00"), targets[s.Ref])
	}
	return canonicalHash("orchestration.workflow.plan.v2", fields...)
}

// gatePlanHash derives the hash a MID-GRAPH approval-gate step's approval is
// bound to. It must differ from the run's own plan hash: both approvals are
// opened against the same workflow, and runPhaseDecide accepts any approved ref
// whose bound hash matches, without inspecting the approval's action. Sharing
// one hash therefore let a human's answer to an in-run checkpoint be replayed
// as the authorization to START a whole new run. Deriving a per-step hash keeps
// the two decisions separate: answering the checkpoint authorizes the
// checkpoint, and nothing else.
func gatePlanHash(runPlanHash, stepRef string) string {
	return canonicalHash("orchestration.workflow-gate.v2", runPlanHash, stepRef)
}

// decodeSteps parses a stored steps JSON document. A malformed stored document
// is a corruption, surfaced as an error rather than silently treated as empty.
func decodeSteps(raw string) ([]stepDTO, error) {
	if strings.TrimSpace(raw) == "" {
		return []stepDTO{}, nil
	}
	var steps []stepDTO
	if err := json.Unmarshal([]byte(raw), &steps); err != nil {
		return nil, fmt.Errorf("orchestration: stored workflow steps are malformed: %w", err)
	}
	return steps, nil
}

// encodeSteps serializes a canonical step set for storage.
func encodeSteps(steps []stepDTO) string {
	if steps == nil {
		steps = []stepDTO{}
	}
	return string(mustJSON(steps))
}

// stepWorkItemID returns the literal WorkItem reference carried by a K4 step.
// work-create has no ID until execution and wait/reconcile target other
// aggregates, so those kinds intentionally return empty.
func stepWorkItemID(s stepDTO) string {
	var id string
	switch s.Kind {
	case stepWorkAssign:
		var c workAssignConfig
		_ = json.Unmarshal(s.Config, &c)
		id = c.WorkItemID
	case stepWorkClaim:
		var c workClaimConfig
		_ = json.Unmarshal(s.Config, &c)
		id = c.WorkItemID
	case stepSessionLaunch:
		var c sessionLaunchConfig
		_ = json.Unmarshal(s.Config, &c)
		id = c.WorkItemID
	case stepWorkMessage:
		var c workMessageConfig
		_ = json.Unmarshal(s.Config, &c)
		id = c.WorkItemID
	case stepWorkHandoff:
		var c workHandoffConfig
		_ = json.Unmarshal(s.Config, &c)
		id = c.WorkItemID
	case stepWorkTransition:
		var c workTransitionConfig
		_ = json.Unmarshal(s.Config, &c)
		id = c.WorkItemID
	case stepWorkCancel:
		var c workCancelConfig
		_ = json.Unmarshal(s.Config, &c)
		id = c.WorkItemID
	case stepRemotePlan:
		var c remotePlanConfig
		_ = json.Unmarshal(s.Config, &c)
		id = c.WorkItemID
	case stepRemoteCancel:
		var c remoteCancelConfig
		_ = json.Unmarshal(s.Config, &c)
		id = c.WorkItemID
	}
	return id
}

func stepWorkItemStepRef(s stepDTO) string {
	var ref string
	switch s.Kind {
	case stepWorkAssign:
		var c workAssignConfig
		_ = json.Unmarshal(s.Config, &c)
		ref = c.WorkItemStepRef
	case stepWorkClaim:
		var c workClaimConfig
		_ = json.Unmarshal(s.Config, &c)
		ref = c.WorkItemStepRef
	case stepSessionLaunch:
		var c sessionLaunchConfig
		_ = json.Unmarshal(s.Config, &c)
		ref = c.WorkItemStepRef
	case stepWorkMessage:
		var c workMessageConfig
		_ = json.Unmarshal(s.Config, &c)
		ref = c.WorkItemStepRef
	case stepWorkHandoff:
		var c workHandoffConfig
		_ = json.Unmarshal(s.Config, &c)
		ref = c.WorkItemStepRef
	case stepWorkTransition:
		var c workTransitionConfig
		_ = json.Unmarshal(s.Config, &c)
		ref = c.WorkItemStepRef
	case stepWorkCancel:
		var c workCancelConfig
		_ = json.Unmarshal(s.Config, &c)
		ref = c.WorkItemStepRef
	case stepRemotePlan:
		var c remotePlanConfig
		_ = json.Unmarshal(s.Config, &c)
		ref = c.WorkItemStepRef
	case stepRemoteCancel:
		var c remoteCancelConfig
		_ = json.Unmarshal(s.Config, &c)
		ref = c.WorkItemStepRef
	}
	return ref
}

// rootWorkItemID returns the one literal WorkItem shared by the graph. Empty
// means the root is produced dynamically by work-create, absent, or cannot be
// selected without guessing because the graph carries multiple literal IDs.
func rootWorkItemID(steps []stepDTO) string {
	root := ""
	for _, step := range steps {
		id := stepWorkItemID(step)
		if id == "" {
			continue
		}
		if root != "" && root != id {
			return ""
		}
		root = id
	}
	return root
}
