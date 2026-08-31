// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// RemoteWorkOutcome is the connector-independent three-way outcome of a
// remote-work operation. UNKNOWN is deliberately distinct from BROKEN: it
// means the peer may have acted or could not be observed conclusively, so a
// caller must reconcile and must never blindly repeat an effect.
type RemoteWorkOutcome string

const (
	RemoteWorkClean   RemoteWorkOutcome = "CLEAN"
	RemoteWorkBroken  RemoteWorkOutcome = "BROKEN"
	RemoteWorkUnknown RemoteWorkOutcome = "UNKNOWN"
)

// RemoteWorkResultKind preserves the A2A SendMessage response union without
// importing connector DTOs into the AGPL orchestration module. A direct
// message result never fabricates an external task identifier.
type RemoteWorkResultKind string

const (
	RemoteWorkResultTask    RemoteWorkResultKind = "task"
	RemoteWorkResultMessage RemoteWorkResultKind = "message"
)

// RemoteWorkCheck is one bounded, non-payload-bearing check behind a remote
// outcome. EvidenceRef is an opaque digest/reference, never a credential or
// remote response body.
type RemoteWorkCheck struct {
	Name        string
	Outcome     RemoteWorkOutcome
	EvidenceRef string
}

// RemoteWorkResult is the durable projection shared by Plan, Test, Start,
// Observe and Cancel. CommandID/EventID/EventSeq identify the adapter's
// durable ProtocolBinding receipt; they are required for effecting operations
// and observations used by a workflow. WireHash and DetailHash anchor data
// retained outside orchestration without copying protocol payloads here.
type RemoteWorkResult struct {
	Outcome     RemoteWorkOutcome
	Code        string
	ObservedAt  string
	Checks      []RemoteWorkCheck
	PlanHash    string
	ApprovalRef string

	BindingID             model.ID
	BindingSpecID         model.ID
	BindingSpecGeneration int64
	WorkItemID            model.ID
	AttemptID             model.ID
	Generation            int64
	SyntheticSID          string

	OwnerEpoch int64
	LeaseFence int64

	ResultKind        RemoteWorkResultKind
	ExternalTaskID    string
	ExternalContextID string
	ExternalMessageID string
	RemoteState       string
	RemoteRevision    string
	Terminal          bool
	WireHash          string
	DetailHash        string

	CommandID model.ID
	EventID   model.ID
	EventSeq  int64
	WorkState string
	Detail    string
}

// RemoteWorkPlanRequest contains every local dimension which the outbound
// plan must commit. Authority and AgentRef are server-side configuration keys;
// endpoints and credentials are intentionally absent from workflow DTOs.
type RemoteWorkPlanRequest struct {
	RunRef  string
	StepRef string
	Actor   WorkActor

	WorkspaceID           model.ID
	WorkItemID            model.ID
	BindingID             model.ID
	BindingSpecID         model.ID
	BindingSpecGeneration int64
	Protocol              string
	ProtocolVersion       string
	Authority             string
	AgentRef              string
	Skill                 string
	Scope                 string

	OwnerEpoch       int64
	LeaseFence       int64
	BriefHash        string
	CriteriaRevision int64
}

// RemoteWorkTestRequest probes discovery/auth/capability for a configured
// peer or observes a known binding without creating, canceling or sending work.
type RemoteWorkTestRequest struct {
	RunRef   string
	StepRef  string
	Actor    WorkActor
	Plan     RemoteWorkPlanRequest
	PlanHash string
}

// RemoteWorkStartRequest executes an already approved plan. The executor must
// persist the binding attempt and idempotency intent before transmission. An
// exact replay returns the same receipt; a reused key, changed plan or changed
// approval is a definitive conflict. A post-transmit ambiguity returns UNKNOWN
// and is never emitted again under this attempt.
type RemoteWorkStartRequest struct {
	RunRef              string
	StepRef             string
	Actor               WorkActor
	IdempotencyKey      string
	Plan                RemoteWorkPlanRequest
	PlanHash            string
	ApprovalRef         string
	ApprovalPlanHash    string
	ApprovalAction      string
	ApprovalSubjectKind string
	ApprovalSubjectRef  string
}

// RemoteWorkObserveRequest reconciles one exact binding generation. Observe
// is allowed to poll the peer, but the translated observation and any legal
// local WorkItem transition must be committed behind the semantic key before
// the method returns.
type RemoteWorkObserveRequest struct {
	RunRef         string
	StepRef        string
	Actor          WorkActor
	IdempotencyKey string
	BindingID      model.ID
}

// RemoteWorkCancelRequest first records cancellation intent, then asks the
// remote executor to cancel. A successful RPC is not equivalent to a canceled
// WorkItem: only a CLEAN terminal canceled observation may close local work.
type RemoteWorkCancelRequest struct {
	RunRef         string
	StepRef        string
	Actor          WorkActor
	IdempotencyKey string
	BindingID      model.ID
	WorkItemID     model.ID
	Reason         string
}

// RemoteWorkExecutor is the governed, durable remote-work seam. Implementations
// live in the composition root and translate these neutral DTOs to a complete
// protocol client (for example the A2A Delegator) plus the WorkKernel and
// ProtocolBinding store. Plan and Test have no effects. Start and Cancel are
// at-most-once after transmit; Observe is exact-generation reconciliation.
type RemoteWorkExecutor interface {
	Plan(context.Context, model.TenantID, RemoteWorkPlanRequest) (RemoteWorkResult, error)
	Test(context.Context, model.TenantID, RemoteWorkTestRequest) (RemoteWorkResult, error)
	Start(context.Context, model.TenantID, RemoteWorkStartRequest) (RemoteWorkResult, error)
	Observe(context.Context, model.TenantID, RemoteWorkObserveRequest) (RemoteWorkResult, error)
	Cancel(context.Context, model.TenantID, RemoteWorkCancelRequest) (RemoteWorkResult, error)
}

var ErrRemoteWorkExecutorUnwired = errors.New("orchestration: remote work executor is not wired")

type unwiredRemoteWorkExecutor struct{}

func (unwiredRemoteWorkExecutor) Plan(context.Context, model.TenantID, RemoteWorkPlanRequest) (RemoteWorkResult, error) {
	return RemoteWorkResult{}, ErrRemoteWorkExecutorUnwired
}

func (unwiredRemoteWorkExecutor) Test(context.Context, model.TenantID, RemoteWorkTestRequest) (RemoteWorkResult, error) {
	return RemoteWorkResult{}, ErrRemoteWorkExecutorUnwired
}

func (unwiredRemoteWorkExecutor) Start(context.Context, model.TenantID, RemoteWorkStartRequest) (RemoteWorkResult, error) {
	return RemoteWorkResult{}, ErrRemoteWorkExecutorUnwired
}

func (unwiredRemoteWorkExecutor) Observe(context.Context, model.TenantID, RemoteWorkObserveRequest) (RemoteWorkResult, error) {
	return RemoteWorkResult{}, ErrRemoteWorkExecutorUnwired
}

func (unwiredRemoteWorkExecutor) Cancel(context.Context, model.TenantID, RemoteWorkCancelRequest) (RemoteWorkResult, error) {
	return RemoteWorkResult{}, ErrRemoteWorkExecutorUnwired
}

func validRemoteOutcome(outcome RemoteWorkOutcome) bool {
	switch outcome {
	case RemoteWorkClean, RemoteWorkBroken, RemoteWorkUnknown:
		return true
	default:
		return false
	}
}

// validateRemoteStartResult enforces the response union and the tuple the
// approved plan committed. It is connector-independent and shared by adapters'
// contract tests: a direct Message response has no Task ID, while a Task result
// has one. The binding reservation preserves WorkItem/owner and advances the
// approved lease fence exactly once when its synthetic SID acquires the lease.
func validateRemoteStartResult(req RemoteWorkStartRequest, result RemoteWorkResult) error {
	if err := validateRemoteResult("start", result, true); err != nil {
		return err
	}
	if strings.TrimSpace(req.PlanHash) == "" || strings.TrimSpace(req.ApprovalRef) == "" ||
		strings.TrimSpace(req.ApprovalPlanHash) == "" || strings.TrimSpace(req.ApprovalAction) == "" ||
		strings.TrimSpace(req.ApprovalSubjectKind) == "" || strings.TrimSpace(req.ApprovalSubjectRef) == "" {
		return errors.New("orchestration: remote start requires a bound plan and approval")
	}
	if result.PlanHash != req.PlanHash || result.ApprovalRef != req.ApprovalRef {
		return errors.New("orchestration: remote start result does not match the approved plan")
	}
	if req.Plan.LeaseFence < 0 || req.Plan.LeaseFence == 1<<63-1 {
		return errors.New("orchestration: remote start plan has an invalid lease fence")
	}
	if result.WorkItemID != req.Plan.WorkItemID || result.OwnerEpoch != req.Plan.OwnerEpoch ||
		result.LeaseFence != req.Plan.LeaseFence+1 {
		return errors.New("orchestration: remote start result changed the approved work tuple")
	}
	if result.AttemptID.IsZero() || result.Generation < 1 || strings.TrimSpace(result.SyntheticSID) == "" ||
		result.BindingSpecID != req.Plan.BindingSpecID ||
		result.BindingSpecGeneration != req.Plan.BindingSpecGeneration {
		return errors.New("orchestration: remote start returned an incomplete or mismatched binding reservation")
	}
	if result.Outcome != RemoteWorkClean && result.ResultKind == "" {
		return nil
	}
	switch result.ResultKind {
	case RemoteWorkResultTask:
		if strings.TrimSpace(result.ExternalTaskID) == "" {
			return errors.New("orchestration: remote task result has no external task id")
		}
	case RemoteWorkResultMessage:
		if strings.TrimSpace(result.ExternalMessageID) == "" || strings.TrimSpace(result.ExternalTaskID) != "" {
			return errors.New("orchestration: direct remote message result has an invalid task/message union")
		}
	default:
		return errors.New("orchestration: remote start returned an invalid result kind")
	}
	return nil
}

// validateRemoteResult verifies the bounded durable envelope. requireReceipt
// is false for pure Plan/Test and true whenever a workflow will persist and
// rely on the result after restart.
func validateRemoteResult(operation string, result RemoteWorkResult, requireReceipt bool) error {
	if !validRemoteOutcome(result.Outcome) || strings.TrimSpace(result.Code) == "" {
		return fmt.Errorf("orchestration: remote %s returned an invalid outcome", operation)
	}
	if _, err := model.ParseTimestamp(result.ObservedAt); err != nil {
		return fmt.Errorf("orchestration: remote %s returned an invalid observation time", operation)
	}
	for _, check := range result.Checks {
		if strings.TrimSpace(check.Name) == "" || !validRemoteOutcome(check.Outcome) {
			return fmt.Errorf("orchestration: remote %s returned an invalid check", operation)
		}
	}
	if requireReceipt && (result.BindingID.IsZero() || result.CommandID.IsZero() ||
		result.EventID.IsZero() || result.EventSeq < 1 || result.AttemptID.IsZero() ||
		result.Generation < 1 || strings.TrimSpace(result.SyntheticSID) == "") {
		return fmt.Errorf("orchestration: remote %s returned incomplete durable evidence", operation)
	}
	return nil
}
