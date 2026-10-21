// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"slices"
	"time"
)

// turn/plan/updated is already delivered to the owned bridge. In Code Mode it
// can be the only native record of the plan's arguments; retain the latest one
// in the live session so opening the panel or reloading a browser loses nothing.
func (s *codexSession) observeActivityPlan(params json.RawMessage) {
	var n struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Plan     []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"plan"`
	}
	if json.Unmarshal(params, &n) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n.ThreadID == "" || n.ThreadID != s.threadID || n.TurnID == "" || (s.turnID != "" && n.TurnID != s.turnID) {
		return
	}
	if slices.Contains(s.finished, n.TurnID) {
		return // A late plan cannot replace the last plan of a finished turn.
	}
	if n.Plan == nil {
		s.activityPlanUnreadable++
		return
	}
	plan := make([]activityTodo, 0, len(n.Plan))
	for _, step := range n.Plan {
		status := step.Status
		if status == "inProgress" {
			status = "in_progress"
		}
		if step.Step == "" || !slices.Contains([]string{"pending", "in_progress", "completed"}, status) {
			s.activityPlanUnreadable++
			return
		}
		plan = append(plan, activityTodo{Content: step.Step, Status: status})
	}
	s.activityPlan, s.activityPlanAt, s.activityPlanSeen = plan, time.Now(), true
}

func (s *codexSession) activityPlanSnapshot(threadID string) ([]activityTodo, time.Time, bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threadID != threadID {
		return nil, time.Time{}, false, 0
	}
	return slices.Clone(s.activityPlan), s.activityPlanAt, s.activityPlanSeen, s.activityPlanUnreadable
}
