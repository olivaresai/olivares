// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
)

// mcpMemoryDurableTaskStore is the explicit durable authority used by gateway
// integration fixtures that exercise MCP Tasks. Fixtures that verify the OFF
// posture continue to build the production gateway without this store.
type mcpMemoryDurableTaskStore struct {
	mu    sync.Mutex
	next  int64
	views map[string]mcpc.DurableTaskView
}

func newMCPMemoryDurableTaskStore() *mcpMemoryDurableTaskStore {
	return &mcpMemoryDurableTaskStore{views: make(map[string]mcpc.DurableTaskView)}
}

func mcpMemoryOwnerKey(owner mcpc.TaskOwner) string {
	return strings.Join([]string{
		owner.Tenant,
		owner.Issuer,
		owner.Subject,
		owner.ActAs,
		owner.ClientID,
		strconv.FormatBool(owner.IsDelegated),
	}, "\x00")
}

func mcpMemoryTaskKey(owner mcpc.TaskOwner, taskID string) string {
	return mcpMemoryOwnerKey(owner) + "\x00" + taskID
}

func (s *mcpMemoryDurableTaskStore) Register(
	_ context.Context,
	intent mcpc.DurableTaskIntent,
) (mcpc.DurableTaskRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := mcpMemoryTaskKey(intent.Owner, intent.TaskID)
	for currentKey, current := range s.views {
		if current.Intent.Owner.Tenant != intent.Owner.Tenant || current.Ref.TaskID != intent.TaskID {
			continue
		}
		if mcpMemoryOwnerKey(current.Intent.Owner) == mcpMemoryOwnerKey(intent.Owner) &&
			intent.OriginOperationID != "" &&
			current.Intent.OriginOperationID == intent.OriginOperationID &&
			current.Intent.OriginEffectDigest == intent.OriginEffectDigest {
			return current.Ref, nil
		}
		if current.Observation.Terminal {
			delete(s.views, currentKey)
			continue
		}
		return mcpc.DurableTaskRef{}, mcpc.ErrDurableTaskConflict
	}

	s.next++
	ref := mcpc.DurableTaskRef{
		TaskID:     intent.TaskID,
		Generation: s.next,
		BindingID:  "binding-" + strconv.FormatInt(s.next, 10),
		WorkItemID: "work-" + strconv.FormatInt(s.next, 10),
		SID:        "sid:mcp:test:" + strconv.FormatInt(s.next, 10),
	}
	s.views[key] = mcpc.DurableTaskView{
		Ref:    ref,
		Intent: intent,
		Observation: mcpc.DurableTaskObservation{
			TaskID:         intent.TaskID,
			Generation:     ref.Generation,
			Kind:           mcpc.DurableTaskObservationRegister,
			Status:         intent.InitialStatus,
			Verdict:        mcpc.DurableTaskVerdictClean,
			ObservedAt:     intent.CreatedAt,
			TTLMs:          intent.TTLMs,
			PollIntervalMs: intent.PollIntervalMs,
		},
	}
	return ref, nil
}

func (s *mcpMemoryDurableTaskStore) Get(
	_ context.Context,
	owner mcpc.TaskOwner,
	taskID string,
	generation int64,
) (mcpc.DurableTaskView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	view, ok := s.views[mcpMemoryTaskKey(owner, taskID)]
	if !ok || (generation != 0 && view.Ref.Generation != generation) {
		return mcpc.DurableTaskView{}, mcpc.ErrDurableTaskNotFound
	}
	return view, nil
}

func (s *mcpMemoryDurableTaskStore) UpdateObservation(
	_ context.Context,
	owner mcpc.TaskOwner,
	observation mcpc.DurableTaskObservation,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := mcpMemoryTaskKey(owner, observation.TaskID)
	view, ok := s.views[key]
	if !ok || view.Ref.Generation != observation.Generation {
		return mcpc.ErrDurableTaskNotFound
	}
	view.Observation = observation
	s.views[key] = view
	return nil
}

func (s *mcpMemoryDurableTaskStore) List(
	_ context.Context,
	owner mcpc.TaskOwner,
	cursor string,
	limit int,
) (mcpc.DurableTaskPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		return mcpc.DurableTaskPage{}, errors.New("limit must be positive")
	}
	start := 0
	if cursor != "" {
		parsed, err := strconv.Atoi(cursor)
		if err != nil || parsed < 0 {
			return mcpc.DurableTaskPage{}, errors.New("invalid cursor")
		}
		start = parsed
	}
	keys := make([]string, 0, len(s.views))
	for key, view := range s.views {
		if view.Intent.Owner.Tenant != owner.Tenant {
			continue
		}
		if owner.Issuer != "" && mcpMemoryOwnerKey(view.Intent.Owner) != mcpMemoryOwnerKey(owner) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if start > len(keys) {
		return mcpc.DurableTaskPage{}, errors.New("cursor outside inventory")
	}
	end := start + limit
	if end > len(keys) {
		end = len(keys)
	}
	page := mcpc.DurableTaskPage{Tasks: make([]mcpc.DurableTaskView, 0, end-start)}
	for _, key := range keys[start:end] {
		page.Tasks = append(page.Tasks, s.views[key])
	}
	if end < len(keys) {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}
