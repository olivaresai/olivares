// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/sdk"
)

const durableTaskBootstrapPageSize = 200

func publicTaskOwner(owner taskOwner, delegated bool) TaskOwner {
	return TaskOwner{
		Tenant: owner.Tenant, Issuer: owner.Issuer, Subject: owner.Subject,
		ActAs: owner.ActAs, ClientID: owner.ClientID, IsDelegated: delegated,
	}
}

func internalTaskOwner(owner TaskOwner) taskOwner {
	return taskOwner{
		Tenant: owner.Tenant, Issuer: owner.Issuer, Subject: owner.Subject,
		ActAs: owner.ActAs, ClientID: owner.ClientID,
	}
}

func durableGenerationToken(generation int64) string {
	return "durable:" + strconv.FormatInt(generation, 10)
}

func durableIntentFromRecord(rs *ResourceServer, rec TaskRecord) DurableTaskIntent {
	intent := DurableTaskIntent{
		Owner:                publicTaskOwner(rec.owner(), rec.IsDelegated),
		TaskID:               rec.TaskID,
		Tool:                 rec.Tool,
		RequiredScope:        rec.RequiredScope,
		Destructive:          rec.Destructive,
		CreatedAt:            rec.CreatedAt,
		TTLMs:                cloneInt64(rec.TTLMs),
		PollIntervalMs:       cloneInt64(rec.PollIntervalMs),
		InitialStatus:        rec.Status,
		InitialStatusReason:  rec.StatusReason,
		UpstreamDescriptor:   rs.upstreamDescriptor,
		ProtocolVersion:      rs.upstreamRevision,
		InitialInputRequests: cloneDurableTaskInputRefs(rec.InputRequests),
	}
	if rec.Origin.Valid() {
		intent.OriginOperationID = string(rec.Origin.OperationID)
		intent.OriginEffectDigest = string(rec.Origin.EffectDigest)
	}
	return intent
}

func durableViewRecord(view DurableTaskView, assumeRelayed bool) (TaskRecord, error) {
	if err := validateDurableTaskRef(view.Ref, view.Intent.TaskID); err != nil {
		return TaskRecord{}, err
	}
	if !view.Intent.Owner.complete() {
		return TaskRecord{}, fmt.Errorf("durable task owner is incomplete")
	}
	if view.Intent.CreatedAt.IsZero() {
		return TaskRecord{}, fmt.Errorf("durable task created_at is required")
	}
	if strings.TrimSpace(view.Intent.Tool) == "" {
		return TaskRecord{}, fmt.Errorf("durable task tool is required")
	}
	if view.Observation.TaskID != "" && view.Observation.TaskID != view.Ref.TaskID {
		return TaskRecord{}, fmt.Errorf("durable task observation returned another task identifier")
	}
	if view.Observation.Generation != 0 && view.Observation.Generation != view.Ref.Generation {
		return TaskRecord{}, fmt.Errorf("durable task observation returned another generation")
	}

	status := view.Intent.InitialStatus
	reason := view.Intent.InitialStatusReason
	if view.Observation.Status != "" {
		status = view.Observation.Status
		reason = view.Observation.StatusReason
	}
	if status == "" {
		status = taskStatusWorking
	}
	ttl := cloneInt64(view.Intent.TTLMs)
	poll := cloneInt64(view.Intent.PollIntervalMs)
	if view.Observation.Kind == DurableTaskObservationGet {
		ttl = cloneInt64(view.Observation.TTLMs)
		poll = cloneInt64(view.Observation.PollIntervalMs)
	}
	rec := TaskRecord{
		TaskID: view.Ref.TaskID, Tool: view.Intent.Tool,
		Subject: view.Intent.Owner.Subject, IsDelegated: view.Intent.Owner.IsDelegated,
		ActAs: view.Intent.Owner.ActAs, Issuer: view.Intent.Owner.Issuer,
		ClientID: view.Intent.Owner.ClientID, Tenant: view.Intent.Owner.Tenant,
		RequiredScope: view.Intent.RequiredScope, Destructive: view.Intent.Destructive,
		CreatedAt: view.Intent.CreatedAt, TTLMs: ttl, PollIntervalMs: poll,
		Status: status, StatusReason: reason,
		InputRequests:   cloneDurableTaskInputRefs(view.Intent.InitialInputRequests),
		Generation:      durableGenerationToken(view.Ref.Generation),
		DurableRef:      view.Ref,
		DurableVerdict:  view.Observation.Verdict,
		DurableObserved: view.Observation.ObservedAt,
		HandleRelayed:   assumeRelayed,
	}
	if view.Intent.OriginOperationID != "" || view.Intent.OriginEffectDigest != "" {
		rec.Origin = sdk.EvidenceBinding{
			OperationID:  sdk.OperationID(view.Intent.OriginOperationID),
			EffectDigest: sdk.EffectDigest(view.Intent.OriginEffectDigest),
		}
		if !rec.Origin.Valid() {
			return TaskRecord{}, fmt.Errorf("durable task origin binding is incomplete")
		}
	}
	if view.Observation.CancelRequested && !taskStatusTerminal(status) {
		rec.CancelUnconfirmed = true
		if status == taskStatusWorking || status == "" {
			rec.Status = taskCancelRequestedStatus
		}
	}
	if taskStatusTerminal(status) && view.Observation.Kind != DurableTaskObservationGet {
		rec.TerminalUnconfirmed = true
	}
	return rec, nil
}

func cloneDurableTaskInputRefs(refs []DurableTaskInputRef) []DurableTaskInputRef {
	return append([]DurableTaskInputRef(nil), refs...)
}

// rehydrateDurableTasks rebuilds the process-local cache from the durable
// tenant inventory. Construction fails on an unreadable, malformed or
// self-inconsistent inventory: advertising Tasks with only a partial cache
// would make request routing depend on which rows happened to load.
func (rs *ResourceServer) rehydrateDurableTasks(ctx context.Context) error {
	if rs == nil || rs.durableTasks == nil {
		return nil
	}
	selector := TaskOwner{Tenant: rs.tenant}
	cursor := ""
	seen := map[string]struct{}{"": {}}
	for {
		page, err := rs.durableTasks.List(ctx, selector, cursor, durableTaskBootstrapPageSize)
		if err != nil {
			return err
		}
		for i, view := range page.Tasks {
			if view.Intent.Owner.Tenant != rs.tenant {
				return fmt.Errorf("page %q task %d belongs to another tenant", cursor, i)
			}
			rec, err := durableViewRecord(view, true)
			if err != nil {
				return fmt.Errorf("page %q task %d: %w", cursor, i, err)
			}
			if err := rs.taskLedger.restoreDurable(rec); err != nil {
				return fmt.Errorf("page %q task %d: %w", cursor, i, err)
			}
		}
		next := strings.TrimSpace(page.NextCursor)
		if next == "" {
			return nil
		}
		if _, duplicate := seen[next]; duplicate {
			return fmt.Errorf("durable task inventory repeated cursor %q", next)
		}
		seen[next] = struct{}{}
		cursor = next
	}
}

func (rs *ResourceServer) registerDurableTask(ctx context.Context, rec TaskRecord) (TaskRecord, error) {
	if rs == nil || rs.durableTasks == nil {
		return rec, errors.New("MCP Tasks persistence is not configured")
	}
	ref, err := rs.durableTasks.Register(ctx, durableIntentFromRecord(rs, rec))
	if err != nil {
		return rec, err
	}
	if err := validateDurableTaskRef(ref, rec.TaskID); err != nil {
		return rec, err
	}
	rec.Generation = durableGenerationToken(ref.Generation)
	rec.DurableRef = ref
	return rec, nil
}

// durableTaskRecord resolves one exact owned task through the durable authority
// and only then refreshes the process cache. The caller's owner, not cached
// metadata, scopes the authoritative read.
func (rs *ResourceServer) durableTaskRecord(ctx context.Context, owner TaskOwner, taskID string, generation int64) (TaskRecord, error) {
	if rs == nil || rs.durableTasks == nil {
		return TaskRecord{}, errors.New("MCP Tasks persistence is not configured")
	}
	if !owner.complete() {
		return TaskRecord{}, fmt.Errorf("durable task owner is incomplete")
	}
	view, err := rs.durableTasks.Get(ctx, owner, taskID, generation)
	if err != nil {
		return TaskRecord{}, err
	}
	if internalTaskOwner(view.Intent.Owner) != internalTaskOwner(owner) ||
		view.Intent.Owner.IsDelegated != owner.IsDelegated {
		return TaskRecord{}, ErrDurableTaskNotFound
	}
	assumeRelayed := true
	if cached, exists := rs.taskLedger.lookup(taskID); exists &&
		cached.Generation == durableGenerationToken(view.Ref.Generation) {
		assumeRelayed = false
	}
	rec, err := durableViewRecord(view, assumeRelayed)
	if err != nil {
		return TaskRecord{}, err
	}
	if err := rs.taskLedger.refreshDurable(rec); err != nil {
		return TaskRecord{}, err
	}
	return rec, nil
}

func (rs *ResourceServer) persistDurableTaskObservation(ctx context.Context, rec TaskRecord, observation DurableTaskObservation) error {
	if rs == nil || rs.durableTasks == nil {
		return errors.New("MCP Tasks persistence is not configured")
	}
	if rec.DurableRef.Generation <= 0 {
		return fmt.Errorf("task has no durable generation")
	}
	observation.TaskID = rec.TaskID
	observation.Generation = rec.DurableRef.Generation
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = rs.clock()
	}
	return rs.durableTasks.UpdateObservation(ctx,
		publicTaskOwner(rec.owner(), rec.IsDelegated), observation)
}

func (rs *ResourceServer) prepareDurableTaskInputResponses(
	ctx context.Context,
	rec TaskRecord,
	binding sdk.EvidenceBinding,
	responses map[string]json.RawMessage,
) error {
	refs := durableTaskInputRefs(responses)
	if len(refs) == 0 {
		return nil
	}
	interrupts, ok := rs.durableTasks.(DurableTaskInterruptStore)
	if !ok || interrupts == nil {
		return errors.New("MCP task input-response communication is not configured")
	}
	if rec.DurableRef.TaskID != rec.TaskID || rec.DurableRef.Generation < 1 ||
		strings.TrimSpace(string(binding.OperationID)) == "" ||
		strings.TrimSpace(string(binding.EffectDigest)) == "" {
		return errors.New("MCP task input-response binding is incomplete")
	}
	return interrupts.PrepareInputResponses(ctx,
		publicTaskOwner(rec.owner(), rec.IsDelegated),
		DurableTaskInputResponseBatch{
			TaskID: rec.TaskID, Generation: rec.DurableRef.Generation,
			OperationID: string(binding.OperationID), EffectDigest: string(binding.EffectDigest),
			Responses: refs,
		},
	)
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
