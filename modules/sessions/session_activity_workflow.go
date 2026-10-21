// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"time"
)

// Workflow progress is the tool's snapshot of the agents it has started, not
// an estimate of the work still to come. Several phases may run concurrently.
type activityWorkflow struct {
	RunID        string                  `json:"run_id"`
	Name         string                  `json:"name,omitempty"`
	DurationMs   int64                   `json:"duration_ms,omitempty"`
	AgentsTotal  int                     `json:"agents_total"`
	AgentsDone   int                     `json:"agents_done"`
	AgentsFailed int                     `json:"agents_failed"`
	Phases       []activityWorkflowPhase `json:"phases"`
}

type activityWorkflowPhase struct {
	Index        int    `json:"index"`
	Title        string `json:"title"`
	Detail       string `json:"detail,omitempty"`
	State        string `json:"state,omitempty"`
	AgentsTotal  int    `json:"agents_total"`
	AgentsDone   int    `json:"agents_done"`
	AgentsFailed int    `json:"agents_failed"`
}

type activityWorkflowEvent struct {
	Type       string `json:"type"`
	Index      int    `json:"index"`
	Title      string `json:"title"`
	PhaseIndex int    `json:"phaseIndex"`
	PhaseTitle string `json:"phaseTitle"`
	State      string `json:"state"`
}

// progress is already a native snapshot: replace it rather than adding counts
// on refresh. An absent snapshot is a throttled event, and changes nothing.
func (w *activityWorkflow) progress(events []activityWorkflowEvent, taskState string) bool {
	if len(events) > activityAgentsMax*2 {
		return false
	}
	phases := map[int]*activityWorkflowPhase{}
	agents := map[int]activityWorkflowEvent{}
	phase := func(index int, title string) *activityWorkflowPhase {
		if p := phases[index]; p != nil {
			return p
		}
		p := &activityWorkflowPhase{Index: index, Title: clip(title, activitySummaryMax)}
		phases[index] = p
		return p
	}
	for _, e := range events {
		switch e.Type {
		case "workflow_phase":
			if e.Index < 1 || e.Index > activityAgentsMax || e.Title == "" {
				return false
			}
			phase(e.Index, e.Title)
		case "workflow_agent":
			if e.Index < 1 || e.Index > activityAgentsMax || e.PhaseIndex < 0 || e.PhaseIndex > activityAgentsMax {
				return false
			}
			switch e.State {
			case "start", "progress", "done", "error":
			default:
				return false
			}
			agents[e.Index] = e
		}
	}
	w.AgentsTotal, w.AgentsDone, w.AgentsFailed = len(agents), 0, 0
	for _, e := range agents {
		var p *activityWorkflowPhase
		if e.PhaseIndex > 0 {
			p = phase(e.PhaseIndex, e.PhaseTitle)
			p.AgentsTotal++
		}
		switch e.State {
		case "done":
			w.AgentsDone++
			if p != nil {
				p.AgentsDone++
			}
		case "error":
			w.AgentsFailed++
			if p != nil {
				p.AgentsFailed++
			}
		default:
			if p != nil {
				p.State = activityRunning
			}
		}
	}
	w.Phases = make([]activityWorkflowPhase, 0, len(phases))
	for _, p := range phases {
		if p.State == activityRunning {
			if taskState != activityRunning {
				p.State = activityStopped
			}
		} else if p.AgentsFailed > 0 {
			p.State = activityFailed
		} else if taskState == activityCompleted {
			p.State = activityCompleted
		}
		// A completed agent can be followed by arbitrary script work. Native
		// snapshots do not report when the enclosing script phase ends.
		w.Phases = append(w.Phases, *p)
	}
	slices.SortFunc(w.Phases, func(a, b activityWorkflowPhase) int { return a.Index - b.Index })
	return true
}

// Claude Code emits task_progress to its live stream, but does not write it to
// the conversation JSONL. Reuse the attach ring, already redacted and bounded,
// so the API, CLI and a reloaded panel all see the same native snapshot.
func (f *activityFold) workflowFrames(ctx context.Context, ring *outputRing) error {
	read := ring.readFrom(1)
	if read.gap {
		f.dto.Unreadable++
	}
	for _, frame := range read.frames {
		if err := ctx.Err(); err != nil {
			return err
		}
		if frame.Stream != streamStdout {
			continue
		}
		var event struct {
			Type     string                  `json:"type"`
			Subtype  string                  `json:"subtype"`
			TaskID   string                  `json:"task_id"`
			ToolID   string                  `json:"tool_use_id"`
			Progress []activityWorkflowEvent `json:"workflow_progress"`
		}
		if len(frame.Data) > activityLineMax {
			f.dto.Unreadable++
			continue
		}
		envelope, ok := parseStreamJSON(frame.Data)
		if !ok || envelope.Type != "system" || envelope.Subtype != "task_progress" {
			continue
		}
		if json.Unmarshal(frame.Data, &event) != nil {
			f.dto.Unreadable++
			continue
		}
		if event.Progress == nil {
			continue
		}
		i, ok := f.taskIDs[event.TaskID]
		if !ok {
			continue
		}
		task := &f.dto.Tasks[i]
		if task.Kind != "workflow" || task.Workflow == nil || task.ToolUseID != event.ToolID {
			continue
		}
		if !task.Workflow.progress(event.Progress, task.State) {
			f.dto.Unreadable++
		}
	}
	return nil
}

func (f *activityFold) workflowFiles(ctx context.Context, dir *os.Root) error {
	if dir == nil {
		return nil
	}
	read := 0
	remaining := int64(activityFileMax)
	for i := range f.dto.Tasks {
		if err := ctx.Err(); err != nil {
			return err
		}
		task := &f.dto.Tasks[i]
		w := task.Workflow
		if w == nil {
			continue
		}
		if read++; read > activityAgentsMax {
			f.dto.Unreadable++
			continue
		}
		changed, err := readActivityState(ctx, dir, w.RunID+".json", func(ctx context.Context, r io.Reader) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			limit := min(int64(activityLineMax), remaining)
			b, err := io.ReadAll(io.LimitReader(r, limit+1))
			remaining = max(0, remaining-int64(len(b)))
			if err := ctx.Err(); err != nil {
				return err
			}
			if err != nil {
				return err
			}
			if int64(len(b)) > limit {
				return errActivityTooLarge
			}
			var record struct {
				RunID      string                  `json:"runId"`
				TaskID     string                  `json:"taskId"`
				Name       string                  `json:"workflowName"`
				Status     string                  `json:"status"`
				DurationMs int64                   `json:"durationMs"`
				Timestamp  string                  `json:"timestamp"`
				Summary    string                  `json:"summary"`
				AgentCount int                     `json:"agentCount"`
				Progress   []activityWorkflowEvent `json:"workflowProgress"`
				Phases     []struct {
					Title  string `json:"title"`
					Detail string `json:"detail"`
				} `json:"phases"`
			}
			if err := json.Unmarshal(f.redact.apply(b), &record); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if record.Timestamp != "" {
				// RFC3339 permits arbitrary fractional precision; bound the raw
				// timestamp too, while preserving the native representation.
				if len(record.Timestamp) > 64 {
					return errors.New("workflow metadata has oversized timestamp")
				}
				if _, err := time.Parse(time.RFC3339, record.Timestamp); err != nil {
					return errors.New("workflow metadata has invalid timestamp")
				}
			}
			state := ""
			switch record.Status {
			case "completed", "success", "failed", "error", "killed", "stopped", "cancelled", "canceled", "timed_out", "timeout":
				state = notificationState(record.Status)
			default:
				return errors.New("workflow file does not report a known terminal state")
			}
			if record.RunID != w.RunID || record.TaskID != task.TaskID || state == "" || state == activityRunning || record.DurationMs < 0 || record.AgentCount < 0 || len(record.Phases) > activityAgentsMax {
				return errors.New("workflow metadata does not identify this task's completed run")
			}
			final := activityWorkflow{RunID: w.RunID, Name: clip(cmpOr(record.Name, w.Name), activitySummaryMax), DurationMs: record.DurationMs}
			if !final.progress(record.Progress, state) {
				return errors.New("workflow metadata has invalid progress")
			}
			final.AgentsTotal = max(final.AgentsTotal, record.AgentCount)
			for i, declared := range record.Phases {
				found := false
				for j := range final.Phases {
					p := &final.Phases[j]
					if p.Index == i+1 {
						p.Detail = clip(declared.Detail, activitySummaryMax)
						found = true
						break
					}
				}
				if !found {
					final.Phases = append(final.Phases, activityWorkflowPhase{Index: i + 1, Title: clip(declared.Title, activitySummaryMax), Detail: clip(declared.Detail, activitySummaryMax)})
				}
			}
			*w = final
			if task.State == activityRunning {
				task.State = state
				task.EndedAt = record.Timestamp
				task.Summary = cmpOr(task.Summary, clip(record.Summary, activitySummaryMax))
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			f.dto.Unreadable++
		}
		if changed {
			// A run record that moved during the read is not verified as final.
			f.dto.Incomplete = true
		}
	}
	return nil
}
