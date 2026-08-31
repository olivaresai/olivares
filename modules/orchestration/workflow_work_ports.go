// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
)

// WorkActor is the authenticated workflow initiator projected into the work
// ports. It is provenance only; adapters construct the neighbor module's
// principal from trusted composition state, never from workflow config.
type WorkActor struct {
	Kind              string
	Ref               string
	Admin             bool
	UserIdentity      model.ID
	AgentIdentity     string
	SessionIdentity   string
	SessionRunRef     string
	SessionFence      int64
	PurposeRestricted bool
}

type WorkParticipant struct {
	Kind string
	Ref  string
}

type WorkCriterion struct {
	Key       string
	Ordinal   int64
	Statement string
	Required  bool
}

type WorkProvenance struct {
	Kind string
	Ref  string
	Hash string
}

type WorkCreateRequest struct {
	RunRef         string
	StepRef        string
	Actor          WorkActor
	IdempotencyKey string
	WorkspaceID    model.ID
	WorkKind       string
	Title          string
	BriefMD        string
	BriefRef       string
	Priority       string
	Owner          WorkParticipant
	Criteria       []WorkCriterion
	Provenance     WorkProvenance
	DueAt          string
}

type WorkAssignRequest struct {
	RunRef             string
	StepRef            string
	Actor              WorkActor
	IdempotencyKey     string
	WorkItemID         model.ID
	ExpectedOwnerEpoch int64
	Target             WorkParticipant
	RequireAck         bool
}

type WorkClaimRequest struct {
	RunRef         string
	StepRef        string
	Actor          WorkActor
	IdempotencyKey string
	WorkItemID     model.ID
	SID            string
	TTLSeconds     int64
}

type WorkTransitionRequest struct {
	RunRef         string
	StepRef        string
	Actor          WorkActor
	IdempotencyKey string
	WorkItemID     model.ID
	TargetState    string
	EvidenceRef    string
	Reason         string
}

type WorkCancelRequest struct {
	RunRef         string
	StepRef        string
	Actor          WorkActor
	IdempotencyKey string
	WorkItemID     model.ID
	BindingID      model.ID
	Reason         string
}

// WorkCommandResult is the common durable projection returned by local work
// commands. EventSeq is the aggregate cursor after the command, not an
// in-memory delivery counter.
type WorkCommandResult struct {
	WorkItemID model.ID
	CommandID  model.ID
	EventID    model.ID
	EventSeq   int64
	OutputKind string
	OutputID   string
	Version    int64
	OwnerEpoch int64
	LeaseFence int64
	State      string
}

type WorkflowWorkControl interface {
	Create(context.Context, model.TenantID, WorkCreateRequest) (WorkCommandResult, error)
	Assign(context.Context, model.TenantID, WorkAssignRequest) (WorkCommandResult, error)
	Claim(context.Context, model.TenantID, WorkClaimRequest) (WorkCommandResult, error)
	Transition(context.Context, model.TenantID, WorkTransitionRequest) (WorkCommandResult, error)
	Cancel(context.Context, model.TenantID, WorkCancelRequest) (WorkCommandResult, error)
}

type WorkLaunchRequest struct {
	RunRef            string
	StepRef           string
	Actor             WorkActor
	IdempotencyKey    string
	WorkItemID        model.ID
	OwnerEpoch        int64
	Fence             int64
	RuntimeProfileRef string
	AttemptKind       string
}

type ManagedWorkRun struct {
	WorkItemID  model.ID
	OwnerEpoch  int64
	LeaseFence  int64
	RunRef      string
	SID         string
	DispatchKey string
}

type WorkflowRuntimeControl interface {
	LaunchForWork(context.Context, model.TenantID, WorkLaunchRequest) (ManagedWorkRun, error)
}

type WorkMessageRequest struct {
	RunRef         string
	StepRef        string
	Actor          WorkActor
	IdempotencyKey string
	WorkItemID     model.ID
	ChannelID      model.ID
	Recipient      WorkParticipant
	Body           string
	BodyRef        string
	AckDueAt       string
	Urgency        string
}

type WorkMessageResult struct {
	WorkItemID model.ID
	MessageID  model.ID
	CommandID  model.ID
	EventID    model.ID
	EventSeq   int64
}

type WorkflowMessageControl interface {
	SendWorkMessage(context.Context, model.TenantID, WorkMessageRequest) (WorkMessageResult, error)
}

type WorkHandoffRequest struct {
	RunRef             string
	StepRef            string
	Actor              WorkActor
	IdempotencyKey     string
	WorkItemID         model.ID
	ExpectedOwnerEpoch int64
	ChannelID          model.ID
	Target             WorkParticipant
	Context            string
	ContextRef         string
	AckDeadline        string
}

type WorkHandoffResult struct {
	WorkItemID model.ID
	HandoffID  model.ID
	MessageID  model.ID
	CommandID  model.ID
	EventID    model.ID
	EventSeq   int64
	OwnerEpoch int64
}

type WorkflowHandoffControl interface {
	OfferWorkHandoff(context.Context, model.TenantID, WorkHandoffRequest) (WorkHandoffResult, error)
}

type WorkAckStatus string

const (
	WorkAckPending      WorkAckStatus = "pending"
	WorkAckAcknowledged WorkAckStatus = "acknowledged"
	WorkAckRejected     WorkAckStatus = "rejected"
	WorkAckExpired      WorkAckStatus = "expired"
	WorkAckUnknown      WorkAckStatus = "unknown"
)

type WorkAckQuery struct {
	Actor         WorkActor
	TargetKind    string
	TargetID      model.ID
	AfterEventSeq int64
}

type WorkAckObservation struct {
	Status   WorkAckStatus
	AckID    model.ID
	EventID  model.ID
	EventSeq int64
	Detail   string
}

type WorkflowAckReader interface {
	ObserveWorkAck(context.Context, model.TenantID, WorkAckQuery) (WorkAckObservation, error)
}

type WorkReconcileRequest struct {
	RunRef         string
	StepRef        string
	Actor          WorkActor
	IdempotencyKey string
	BindingID      model.ID
}

type WorkReconcileResult struct {
	BindingID model.ID
	CommandID model.ID
	EventID   model.ID
	EventSeq  int64
	State     string
}

type WorkflowBindingControl interface {
	ReconcileWorkBinding(context.Context, model.TenantID, WorkReconcileRequest) (WorkReconcileResult, error)
}

var (
	ErrWorkflowWorkUnwired      = errors.New("orchestration: workflow work control is not wired")
	ErrWorkflowRuntimeUnwired   = errors.New("orchestration: workflow runtime control is not wired")
	ErrWorkflowMessageUnwired   = errors.New("orchestration: workflow message control is not wired")
	ErrWorkflowHandoffUnwired   = errors.New("orchestration: workflow handoff control is not wired")
	ErrWorkflowAckReaderUnwired = errors.New("orchestration: workflow ack reader is not wired")
	ErrWorkflowBindingUnwired   = errors.New("orchestration: workflow binding control is not wired")
)

type unwiredWorkflowWorkControl struct{}

func (unwiredWorkflowWorkControl) Create(context.Context, model.TenantID, WorkCreateRequest) (WorkCommandResult, error) {
	return WorkCommandResult{}, ErrWorkflowWorkUnwired
}
func (unwiredWorkflowWorkControl) Assign(context.Context, model.TenantID, WorkAssignRequest) (WorkCommandResult, error) {
	return WorkCommandResult{}, ErrWorkflowWorkUnwired
}
func (unwiredWorkflowWorkControl) Claim(context.Context, model.TenantID, WorkClaimRequest) (WorkCommandResult, error) {
	return WorkCommandResult{}, ErrWorkflowWorkUnwired
}
func (unwiredWorkflowWorkControl) Transition(context.Context, model.TenantID, WorkTransitionRequest) (WorkCommandResult, error) {
	return WorkCommandResult{}, ErrWorkflowWorkUnwired
}
func (unwiredWorkflowWorkControl) Cancel(context.Context, model.TenantID, WorkCancelRequest) (WorkCommandResult, error) {
	return WorkCommandResult{}, ErrWorkflowWorkUnwired
}

type unwiredWorkflowRuntimeControl struct{}

func (unwiredWorkflowRuntimeControl) LaunchForWork(context.Context, model.TenantID, WorkLaunchRequest) (ManagedWorkRun, error) {
	return ManagedWorkRun{}, ErrWorkflowRuntimeUnwired
}

type unwiredWorkflowMessageControl struct{}

func (unwiredWorkflowMessageControl) SendWorkMessage(context.Context, model.TenantID, WorkMessageRequest) (WorkMessageResult, error) {
	return WorkMessageResult{}, ErrWorkflowMessageUnwired
}

type unwiredWorkflowHandoffControl struct{}

func (unwiredWorkflowHandoffControl) OfferWorkHandoff(context.Context, model.TenantID, WorkHandoffRequest) (WorkHandoffResult, error) {
	return WorkHandoffResult{}, ErrWorkflowHandoffUnwired
}

type unwiredWorkflowAckReader struct{}

func (unwiredWorkflowAckReader) ObserveWorkAck(context.Context, model.TenantID, WorkAckQuery) (WorkAckObservation, error) {
	return WorkAckObservation{}, ErrWorkflowAckReaderUnwired
}

type unwiredWorkflowBindingControl struct{}

func (unwiredWorkflowBindingControl) ReconcileWorkBinding(context.Context, model.TenantID, WorkReconcileRequest) (WorkReconcileResult, error) {
	return WorkReconcileResult{}, ErrWorkflowBindingUnwired
}
