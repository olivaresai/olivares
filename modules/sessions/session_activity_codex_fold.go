// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Shapes are from Codex's native rollout protocol, not the app-server's DTOs.
// Response items are durable even when the transient exec/plan events are not.
type codexActivityRecord struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexActivityItem struct {
	Type      string                         `json:"type"`
	ID        string                         `json:"id"`
	CallID    string                         `json:"call_id"`
	Name      string                         `json:"name"`
	Arguments string                         `json:"arguments"`
	Input     string                         `json:"input"`
	Output    json.RawMessage                `json:"output"`
	Model     string                         `json:"model"`
	Effort    string                         `json:"effort"`
	Command   []string                       `json:"command"`
	ProcessID string                         `json:"process_id"`
	Source    json.RawMessage                `json:"source"`
	Status    string                         `json:"status"`
	ExitCode  *int                           `json:"exit_code"`
	Success   bool                           `json:"success"`
	Changes   map[string]codexActivityChange `json:"changes"`
	Plan      []struct {
		Step   string `json:"step"`
		Status string `json:"status"`
	} `json:"plan"`
	Item              json.RawMessage            `json:"item"`
	Tool              string                     `json:"tool"`
	Prompt            string                     `json:"prompt"`
	ReceiverThreadIDs []string                   `json:"receiver_thread_ids"`
	AgentsStates      map[string]json.RawMessage `json:"agents_states"`
	Info              *struct {
		Last *struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
			Total  int64 `json:"total_tokens"`
		} `json:"last_token_usage"`
		Window *int64 `json:"model_context_window"`
	} `json:"info"`
}

type codexActivityChange struct {
	Type     string `json:"type"`
	Content  string `json:"content"`
	Diff     string `json:"unified_diff"`
	MovePath string `json:"move_path"`
}

type codexActivityArgs struct {
	Cmd       string          `json:"cmd"`
	Command   json.RawMessage `json:"command"`
	SessionID json.RawMessage `json:"session_id"`
	AgentID   string          `json:"agent_id"`
	ID        string          `json:"id"`
	Message   string          `json:"message"`
	AgentType string          `json:"agent_type"`
}

type codexActivityCall struct {
	item codexActivityItem
	args codexActivityArgs
	done bool
}

type codexActivityFold struct {
	*activityFold
	calls   map[string]codexActivityCall
	patches map[string]bool
	planAt  time.Time
}

func newCodexActivityFold(base *activityFold) *codexActivityFold {
	base.dto.Driver = providerDriverCodex
	return &codexActivityFold{activityFold: base, calls: map[string]codexActivityCall{}, patches: map[string]bool{}}
}

func (f *codexActivityFold) read(ctx context.Context, r io.Reader, sid string) error {
	br := bufio.NewReaderSize(r, 64<<10)
	pending, err := codexRolloutRoot(br, sid)
	if pending {
		f.dto.Transcript = activityTranscriptPending
		return nil
	}
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := readBoundedLine(br, activityLineMax)
		switch {
		case errors.Is(err, ErrOutputLineTooLong):
			f.dto.Unreadable++
			if err := discardLine(br); errors.Is(err, io.EOF) {
				return nil
			} else if err != nil {
				return err
			}
		case errors.Is(err, io.EOF):
			if len(line) > 0 {
				f.dto.Incomplete = true
			}
			if len(line) != 0 && json.Valid(line) {
				f.add(line)
			}
			return nil
		case err != nil:
			return err
		case len(line) != 0:
			f.add(line)
		}
	}
}

// codexRolloutRoot reads a rollout's first record and proves it identifies the root
// thread sid, before any record of it is displayed: a planted rollout-shaped name must
// not expose another conversation. A rollout the tool has not written a record to yet
// is pending.
func codexRolloutRoot(br *bufio.Reader, sid string) (pending bool, err error) {
	line, err := readBoundedLine(br, activityLineMax)
	if errors.Is(err, io.EOF) && !json.Valid(line) {
		return true, nil
	}
	var first codexActivityRecord
	var meta struct {
		ID     string          `json:"id"`
		Source json.RawMessage `json:"source"`
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return false, errClaudeStateSpecial
	}
	var source map[string]json.RawMessage
	if json.Unmarshal(line, &first) != nil || first.Type != "session_meta" || json.Unmarshal(first.Payload, &meta) != nil || meta.ID != sid {
		return false, errClaudeStateSpecial
	}
	_ = json.Unmarshal(meta.Source, &source)
	if _, child := source["subagent"]; child {
		return false, errClaudeStateSpecial
	}
	return false, nil
}

func (f *codexActivityFold) add(line []byte) {
	line = f.redact.apply(line)
	var rec codexActivityRecord
	var p codexActivityItem
	if len(line) == 0 || json.Unmarshal(line, &rec) != nil || json.Unmarshal(rec.Payload, &p) != nil {
		f.dto.Unreadable++
		return
	}
	f.dto.UpdatedAt = rec.Timestamp
	switch rec.Type {
	case "turn_context":
		if p.Model != f.dto.Model {
			f.dto.ContextTokens, f.dto.ContextWindow = 0, nil
		}
		f.dto.Model, f.dto.Effort = p.Model, p.Effort
	case "response_item":
		f.response(p, rec.Timestamp)
	case "event_msg":
		f.event(p, rec.Timestamp)
	case "compacted":
		f.dto.Compactions = append(f.dto.Compactions, activityCompaction{At: rec.Timestamp})
		f.dto.ContextTokens = 0
	}
}

func (f *codexActivityFold) response(p codexActivityItem, at string) {
	switch p.Type {
	case "function_call", "custom_tool_call":
		if p.CallID == "" {
			f.dto.Unreadable++
			return
		}
		if _, seen := f.calls[p.CallID]; seen {
			return
		}
		p.Name = strings.TrimPrefix(p.Name, "functions.")
		call := codexActivityCall{item: p}
		if p.Type == "function_call" && json.Unmarshal([]byte(p.Arguments), &call.args) != nil {
			f.dto.Unreadable++
			return
		}
		f.calls[p.CallID] = call
		switch p.Name {
		case "exec_command", "shell_command", "shell":
			command := cmpOr(call.args.Cmd, blockText(call.args.Command))
			if p.Name == "shell" {
				var args struct {
					Command []string `json:"command"`
				}
				if json.Unmarshal([]byte(p.Arguments), &args) == nil {
					command = strings.Join(args.Command, " ")
				}
			}
			f.command(p.CallID, command, "", at)
		case "spawn_agent":
			i, ok := f.agents[p.CallID]
			if !ok {
				i = len(f.dto.Agents)
				f.agents[p.CallID] = i
				f.dto.Agents = append(f.dto.Agents, activityAgent{ToolUseID: p.CallID, State: activityRunning, Background: true, StartedAt: at})
			}
			f.dto.Agents[i].Description = clip(call.args.Message, activitySummaryMax)
			f.dto.Agents[i].Type = call.args.AgentType
		}
	case "function_call_output", "custom_tool_call_output":
		call, ok := f.calls[p.CallID]
		if !ok || call.done {
			return
		}
		call.done = true
		f.calls[p.CallID] = call
		text := blockText(p.Output)
		switch call.item.Name {
		case "update_plan":
			if text == "Plan updated" {
				var plan codexActivityItem
				if json.Unmarshal([]byte(call.item.Arguments), &plan) != nil {
					f.dto.Unreadable++
					return
				}
				f.plan(plan)
			}
		case "exec_command", "shell_command", "shell":
			f.commandResult(p.CallID, text, at)
		case "write_stdin":
			if i, ok := f.taskIDs[codexActivityID(call.args.SessionID)]; ok {
				f.commandResult(f.dto.Tasks[i].ToolUseID, text, at)
			} else {
				f.dto.Unreadable++
			}
		case "apply_patch":
			if !f.patches[p.CallID] && strings.HasPrefix(text, "Success. Updated the following files:") {
				f.patchInput(p.CallID, call.item.Input, at)
			}
		case "spawn_agent":
			var result struct {
				AgentID string `json:"agent_id"`
			}
			if json.Unmarshal([]byte(text), &result) != nil || result.AgentID == "" {
				f.dto.Unreadable++
				f.dto.Agents[f.agents[p.CallID]].State = activityFailed
				return
			}
			i := f.agents[p.CallID]
			f.dto.Agents[i].AgentID = result.AgentID
			f.agentIDs[result.AgentID] = i
		case "wait", "wait_agent":
			var result struct {
				Status map[string]json.RawMessage `json:"status"`
			}
			if json.Unmarshal([]byte(text), &result) != nil {
				f.dto.Unreadable++
				return
			}
			for id, status := range result.Status {
				f.agentState(id, status, at)
			}
		case "close_agent":
			if i, ok := f.agentIDs[cmpOr(call.args.ID, call.args.AgentID)]; ok {
				var result struct {
					Previous json.RawMessage `json:"previous_status"`
				}
				if json.Unmarshal([]byte(text), &result) != nil || len(result.Previous) == 0 || string(result.Previous) == "null" {
					f.dto.Unreadable++
					return
				}
				before, unreadable := f.dto.Agents[i], f.dto.Unreadable
				f.agentState(before.AgentID, result.Previous, at)
				if f.dto.Unreadable != unreadable {
					f.dto.Agents[i] = before // An unreadable result cannot decide the agent's state.
					return
				}
				if f.dto.Agents[i].State == activityRunning {
					f.dto.Agents[i].State, f.dto.Agents[i].EndedAt = activityStopped, at
				}
			}
		}
	}
}

func codexActivityID(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

func (f *codexActivityFold) command(id, command, process, at string) int {
	i, ok := f.tasks[id]
	if !ok {
		i = len(f.dto.Tasks)
		f.tasks[id] = i
		f.dto.Tasks = append(f.dto.Tasks, activityTask{ToolUseID: id, Kind: "shell", Command: clip(command, handoffToolInputMax), State: activityRunning, StartedAt: at})
	}
	if process != "" {
		f.dto.Tasks[i].TaskID = process
		f.taskIDs[process] = i
	}
	return i
}

var (
	codexActivityExit    = regexp.MustCompile(`(?m)^(?:Process exited with code |Exit code: )(-?[0-9]+)\r?$`)
	codexActivityProcess = regexp.MustCompile(`(?m)^Process running with session ID ([A-Za-z0-9_-]+)\r?$`)
)

func (f *codexActivityFold) commandResult(id, text, at string) {
	i, ok := f.tasks[id]
	if !ok {
		f.dto.Unreadable++
		return
	}
	t := &f.dto.Tasks[i]
	if t.State != activityRunning {
		return
	}
	// Only native result metadata decides state. The command's stdout can contain
	// these same words, so exclude it before matching the exit/process fields.
	header, _, _ := strings.Cut(text, "\nFinal output:")
	header, _, _ = strings.Cut(header, "\nOutput:")
	if match := codexActivityProcess.FindStringSubmatch(header); match != nil {
		t.TaskID = match[1]
		f.taskIDs[t.TaskID] = i
		return
	}
	if match := codexActivityExit.FindStringSubmatch(header); match != nil {
		code, err := strconv.Atoi(match[1])
		if err == nil {
			f.endCommand(i, code, at)
			return
		}
	}
	// Older shell calls serialize their exit metadata as JSON in the result text.
	var result struct {
		Metadata struct {
			ExitCode *int `json:"exit_code"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(text), &result) == nil && result.Metadata.ExitCode != nil {
		f.endCommand(i, *result.Metadata.ExitCode, at)
		return
	}
	f.dto.Unreadable++
	t.State, t.EndedAt = activityStopped, at
}

func (f *codexActivityFold) endCommand(i, code int, at string) {
	t := &f.dto.Tasks[i]
	t.State, t.EndedAt, t.Summary = activityCompleted, at, fmt.Sprintf("exit code %d", code)
	if code != 0 {
		t.State = activityFailed
	}
}

func (f *codexActivityFold) plan(p codexActivityItem) {
	if p.Plan == nil {
		f.dto.Unreadable++
		return
	}
	for _, step := range p.Plan {
		if step.Step == "" || !slices.Contains([]string{"pending", "in_progress", "completed"}, step.Status) {
			f.dto.Unreadable++
			return
		}
	}
	f.dto.Todos = make([]activityTodo, 0, len(p.Plan))
	f.planAt, _ = time.Parse(time.RFC3339Nano, f.dto.UpdatedAt)
	for _, step := range p.Plan {
		f.dto.Todos = append(f.dto.Todos, activityTodo{Content: step.Step, Status: step.Status})
	}
}

func (f *codexActivityFold) event(p codexActivityItem, at string) {
	switch p.Type {
	case "token_count":
		if p.Info == nil {
			return
		}
		if p.Info.Last != nil {
			u := p.Info.Last
			if u.Total < 0 || u.Input < 0 || u.Output < 0 {
				f.dto.Unreadable++
				return
			}
			f.dto.ContextTokens = u.Total
			if u.Total == 0 {
				f.dto.ContextTokens = u.Input + u.Output
			}
		}
		if p.Info.Window != nil {
			window := max(0, *p.Info.Window)
			f.dto.ContextWindow = nil
			if window > 0 {
				f.dto.ContextWindow = &window
			}
		}
	case "plan_update":
		f.plan(p)
	case "exec_command_begin":
		f.command(p.CallID, strings.Join(p.Command, " "), p.ProcessID, at)
	case "exec_command_end":
		i := f.command(p.CallID, strings.Join(p.Command, " "), p.ProcessID, at)
		if p.ExitCode != nil {
			f.endCommand(i, *p.ExitCode, at)
		} else {
			f.dto.Unreadable++
		}
	case "patch_apply_end":
		if p.Success {
			f.patch(p.CallID, p.Changes, at, true)
		}
	case "item_completed":
		var item codexActivityItem
		if json.Unmarshal(p.Item, &item) != nil {
			f.dto.Unreadable++
			return
		}
		switch item.Type {
		case "CommandExecution":
			id := item.ID
			var source string
			_ = json.Unmarshal(item.Source, &source)
			if source == "unified_exec_interaction" {
				if i, ok := f.taskIDs[item.ProcessID]; ok {
					id = f.dto.Tasks[i].ToolUseID
				}
			}
			i := f.command(id, strings.Join(item.Command, " "), item.ProcessID, at)
			if item.Status != "in_progress" {
				if item.ExitCode != nil {
					f.endCommand(i, *item.ExitCode, at)
				} else {
					f.dto.Tasks[i].State, f.dto.Tasks[i].EndedAt = activityStopped, at
				}
			}
		case "FileChange":
			if item.Status == "completed" {
				f.patch(item.ID, item.Changes, at, true)
			}
		case "CollabAgentToolCall":
			f.collab(item, at)
		}
	}
}

func activityLineCount(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(content, "\n"), "\n") + 1
}

func (f *codexActivityFold) patch(id string, changes map[string]codexActivityChange, at string, fileHeaders bool) {
	if id == "" {
		f.dto.Unreadable++
		return
	}
	if f.patches[id] {
		return
	}
	f.patches[id] = true
	paths := make([]string, 0, len(changes))
	for path := range changes {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		change := changes[path]
		file := activityFile{Path: cmpOr(change.MovePath, path), Edits: 1, LastAt: at}
		switch change.Type {
		case "add":
			file.Added, file.Created = activityLineCount(change.Content), true
		case "delete":
			file.Removed = activityLineCount(change.Content)
		case "update":
			inHunk := false
			for _, line := range strings.Split(change.Diff, "\n") {
				if strings.HasPrefix(line, "@@") {
					inHunk = true
					continue
				}
				// Only native unified diffs carry file headers. Inside a hunk,
				// +++/--- can be ordinary added/removed source lines.
				if fileHeaders && !inHunk && (strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ")) {
					continue
				}
				if strings.HasPrefix(line, "+") {
					file.Added++
				}
				if strings.HasPrefix(line, "-") {
					file.Removed++
				}
			}
		default:
			f.dto.Unreadable++
			continue
		}
		f.mergeFiles([]activityFile{file})
	}
}

// Older rollouts retain the successful custom tool call's patch and result,
// rather than a FileChange item. Count only the patch the tool confirmed applying.
func (f *codexActivityFold) patchInput(id, patch, at string) {
	changes := map[string]codexActivityChange{}
	var path string
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			path = strings.TrimPrefix(line, "*** Add File: ")
			changes[path] = codexActivityChange{Type: "add"}
		case strings.HasPrefix(line, "*** Update File: "):
			path = strings.TrimPrefix(line, "*** Update File: ")
			changes[path] = codexActivityChange{Type: "update"}
		case strings.HasPrefix(line, "*** Delete File: "):
			path = ""
			f.dto.Unreadable++ // The patch contains no deleted file's contents.
		case strings.HasPrefix(line, "*** Move to: "):
			change := changes[path]
			change.MovePath = strings.TrimPrefix(line, "*** Move to: ")
			changes[path] = change
		case strings.HasPrefix(line, "***"):
			if line == "*** End Patch" {
				path = ""
			}
		default:
			if path == "" {
				continue
			}
			change := changes[path]
			if change.Type == "add" && strings.HasPrefix(line, "+") {
				change.Content += strings.TrimPrefix(line, "+") + "\n"
			}
			if change.Type == "update" {
				change.Diff += line + "\n"
			}
			changes[path] = change
		}
	}
	f.patch(id, changes, at, false)
}

func (f *codexActivityFold) agentState(id string, raw json.RawMessage, at string) {
	i, ok := f.agentIDs[id]
	if !ok {
		f.dto.Unreadable++
		return
	}
	a := &f.dto.Agents[i]
	var state string
	var detail map[string]json.RawMessage
	if json.Unmarshal(raw, &state) != nil {
		if json.Unmarshal(raw, &detail) != nil || len(detail) != 1 {
			f.dto.Unreadable++
			return
		}
		for s, text := range detail {
			state = s
			var summary string
			if string(text) != "null" && json.Unmarshal(text, &summary) != nil {
				f.dto.Unreadable++
				return
			}
			a.Summary = clip(summary, activitySummaryMax)
		}
	}
	switch state {
	case "pending_init", "running":
		a.State = activityRunning
	case "completed":
		a.State = activityCompleted
	case "errored":
		a.State = activityFailed
	case "shutdown", "interrupted", "not_found":
		a.State = activityStopped
	default:
		f.dto.Unreadable++
		a.State = activityStopped
	}
	if a.State != activityRunning {
		a.EndedAt = at
		start, startErr := time.Parse(time.RFC3339Nano, a.StartedAt)
		end, endErr := time.Parse(time.RFC3339Nano, at)
		if startErr == nil && endErr == nil {
			a.DurationMs = max(0, end.Sub(start).Milliseconds())
		}
	}
}

func (f *codexActivityFold) collab(item codexActivityItem, at string) {
	for _, id := range item.ReceiverThreadIDs {
		i, known := f.agentIDs[id]
		if !known && item.Tool == "spawn_agent" {
			var callKnown bool
			i, callKnown = f.agents[item.ID]
			if !callKnown || (f.dto.Agents[i].AgentID != "" && f.dto.Agents[i].AgentID != id) {
				i = len(f.dto.Agents)
				f.dto.Agents = append(f.dto.Agents, activityAgent{ToolUseID: item.ID, Description: clip(item.Prompt, activitySummaryMax), State: activityRunning, Background: true, StartedAt: at})
				if !callKnown {
					f.agents[item.ID] = i
				}
			}
			f.dto.Agents[i].AgentID, f.agentIDs[id] = id, i
			known = true
		}
		if known && item.Model != "" {
			f.dto.Agents[i].Model = item.Model
		}
	}
	for id, state := range item.AgentsStates {
		f.agentState(id, state, at)
	}
}
