// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The fold of a Claude Code transcript into the session's activity (session_activity.go):
// one record at a time, each item from a record the tool wrote.

// The parts of Claude Code's transcript records the activity reads. Unknown record
// types and fields are ignored, so a newer tool version adds nothing that breaks it.
type activityRecord struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	IsSidechain bool   `json:"isSidechain"`
	Timestamp   string `json:"timestamp"`
	Effort      string `json:"effort"`
	Message     struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens              int64 `json:"input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	Attachment    struct {
		Type   string          `json:"type"`
		Prompt json.RawMessage `json:"prompt"`
	} `json:"attachment"`
	CompactMetadata struct {
		Trigger    string `json:"trigger"`
		PreTokens  int64  `json:"preTokens"`
		PostTokens int64  `json:"postTokens"`
	} `json:"compactMetadata"`
}

type activityBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Text      string          `json:"text"`
	Content   json.RawMessage `json:"content"`
}

// activityInput holds the tool inputs the activity names. Each tool fills its own.
type activityInput struct {
	Description     string  `json:"description"`
	SubagentType    string  `json:"subagent_type"`
	RunInBackground bool    `json:"run_in_background"`
	Command         string  `json:"command"`
	TaskID          string  `json:"task_id"`
	ShellID         string  `json:"shell_id"`
	Skill           string  `json:"skill"`
	Reason          string  `json:"reason"`
	DelaySeconds    float64 `json:"delaySeconds"`
	TaskListID      string  `json:"taskId"`
	Status          string  `json:"status"`
	Subject         string  `json:"subject"`
	ActiveForm      string  `json:"activeForm"`
	Todos           []struct {
		Content    string `json:"content"`
		Status     string `json:"status"`
		ActiveForm string `json:"activeForm"`
	} `json:"todos"`
}

// activityResult holds the fields of the tool's own structured result the activity
// reads (toolUseResult). It is a plain string when the call failed.
type activityResult struct {
	Status            string          `json:"status"`
	AgentID           string          `json:"agentId"`
	IsAsync           bool            `json:"isAsync"`
	TotalDurationMs   int64           `json:"totalDurationMs"`
	TotalTokens       int64           `json:"totalTokens"`
	TotalToolUseCount int64           `json:"totalToolUseCount"`
	ResolvedModel     string          `json:"resolvedModel"`
	BackgroundTaskID  string          `json:"backgroundTaskId"`
	TaskID            string          `json:"taskId"`
	WorkflowRunID     string          `json:"runId"`
	WorkflowName      string          `json:"workflowName"`
	Type              string          `json:"type"`
	FilePath          string          `json:"filePath"`
	Content           json.RawMessage `json:"content"`
	StructuredPatch   []struct {
		Lines []string `json:"lines"`
	} `json:"structuredPatch"`
	Success *bool `json:"success"`
	Task    struct {
		ID string `json:"id"`
	} `json:"task"`
	StatusChange struct {
		To string `json:"to"`
	} `json:"statusChange"`
}

// activityFold reads a transcript record by record into the activity. sidechain
// selects whose records it reads: the main conversation's, or a subagent's, whose
// transcript holds only its own.
type activityFold struct {
	dto       activityDTO
	sidechain bool
	redact    *secretRedactor
	agents    map[string]int // tool_use_id -> index in dto.Agents
	tasks     map[string]int // tool_use_id -> index in dto.Tasks
	files     map[string]int // path -> index in dto.Files
	taskIDs   map[string]int // the id the tool gave a task -> index in dto.Tasks
	agentIDs  map[string]int // the id the tool gave an agent -> index in dto.Agents
	todoIDs   map[string]int // the id the tool gave a task-list item -> index in dto.Todos
	calls     map[string]activityCall
}

// activityCall is a tool call whose result has not been read yet.
type activityCall struct {
	name  string
	input activityInput
}

func newActivityFold(sidechain bool, redact *secretRedactor) *activityFold {
	return &activityFold{
		dto: activityDTO{
			Driver: providerDriverClaude, Transcript: activityTranscriptRead, Compactions: []activityCompaction{}, Agents: []activityAgent{},
			Tasks: []activityTask{}, Files: []activityFile{}, Todos: []activityTodo{}, Skills: []string{}, MCPServers: []string{},
		},
		sidechain: sidechain, redact: redact,
		agents: map[string]int{}, tasks: map[string]int{}, files: map[string]int{},
		taskIDs: map[string]int{}, agentIDs: map[string]int{}, todoIDs: map[string]int{}, calls: map[string]activityCall{},
	}
}

// read folds a whole transcript. A record longer than the bound is skipped and
// counted; a last record without its newline is one the tool is still writing,
// and the next read has it.
func (f *activityFold) read(ctx context.Context, r io.Reader) error {
	br := bufio.NewReaderSize(r, 64<<10)
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
			if len(line) > 0 && json.Valid(line) {
				f.add(line)
			}
			f.settle()
			return nil
		case err != nil:
			return err
		case len(line) > 0:
			f.add(line)
		}
	}
}

// discardLine reads past the rest of a record the bounded reader gave up on.
func discardLine(br *bufio.Reader) error {
	for {
		_, err := br.ReadSlice('\n')
		if !errors.Is(err, bufio.ErrBufferFull) {
			return err
		}
	}
}

func (f *activityFold) add(line []byte) {
	// The session's vault secrets are withheld from the record before it is read,
	// as they are from its attach stream.
	if line = f.redact.apply(line); len(line) == 0 {
		// A redactor that cannot prove a record safe withholds all of it.
		f.dto.Unreadable++
		return
	}
	var rec activityRecord
	if json.Unmarshal(line, &rec) != nil {
		f.dto.Unreadable++
		return
	}
	if rec.IsSidechain != f.sidechain && rec.Type != "attachment" {
		return
	}
	if rec.Timestamp != "" {
		f.dto.UpdatedAt = rec.Timestamp
	}
	switch rec.Type {
	case "assistant":
		f.assistant(rec)
	case "user":
		f.user(rec)
	case "attachment":
		// A background task's end reaches the conversation as a queued notification.
		var prompt string
		if !f.sidechain && rec.Attachment.Type == "queued_command" && json.Unmarshal(rec.Attachment.Prompt, &prompt) == nil {
			f.notifications(prompt, rec.Timestamp)
		}
	case "system":
		if rec.Subtype == "compact_boundary" {
			c := rec.CompactMetadata
			f.dto.Compactions = append(f.dto.Compactions, activityCompaction{
				At: rec.Timestamp, Trigger: c.Trigger, PreTokens: c.PreTokens, PostTokens: c.PostTokens,
			})
			if c.PostTokens > 0 {
				f.dto.ContextTokens = c.PostTokens
			}
		}
	}
}

func (f *activityFold) assistant(rec activityRecord) {
	// A message the tool wrote itself (an error it reports) names no real model and
	// sent nothing: it says nothing about the context.
	synthetic := strings.HasPrefix(rec.Message.Model, "<")
	if rec.Message.Model != "" && !synthetic {
		f.dto.Model = rec.Message.Model
	}
	if rec.Effort != "" {
		f.dto.Effort = rec.Effort
	}
	if u := rec.Message.Usage; u != nil && !synthetic {
		if sent := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens; sent > 0 {
			f.dto.ContextTokens = sent
		}
	}
	var blocks []activityBlock
	if json.Unmarshal(rec.Message.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		if b.Type == "tool_use" && b.ID != "" {
			f.toolUse(b, rec.Timestamp)
		}
	}
}

func (f *activityFold) toolUse(b activityBlock, at string) {
	var in activityInput
	if len(b.Input) > 0 && json.Unmarshal(b.Input, &in) != nil && isActivityTool(b.Name) {
		// An input of a shape this reader does not know names nothing, and says so.
		f.dto.Unreadable++
	}
	f.calls[b.ID] = activityCall{name: b.Name, input: in}
	switch b.Name {
	case "Agent", "Task":
		if _, seen := f.agents[b.ID]; seen {
			return
		}
		f.agents[b.ID] = len(f.dto.Agents)
		f.dto.Agents = append(f.dto.Agents, activityAgent{
			ToolUseID: b.ID, Type: in.SubagentType, Description: in.Description,
			State: activityRunning, Background: in.RunInBackground, StartedAt: at,
		})
	case "Bash":
		if in.RunInBackground {
			f.task(b.ID, "shell", in.Description, in.Command, at)
		}
	case "Monitor":
		f.task(b.ID, "monitor", in.Description, in.Command, at)
	case "Workflow":
		f.task(b.ID, "workflow", in.Description, "", at)
	case "ScheduleWakeup":
		if _, seen := f.tasks[b.ID]; seen {
			// A repeated call is the same wakeup: how it ended stays.
			return
		}
		t := f.task(b.ID, "wakeup", in.Reason, "", at)
		t.State = activityScheduled
		if ts, err := time.Parse(time.RFC3339Nano, at); err == nil && in.DelaySeconds > 0 {
			t.DueAt = ts.Add(time.Duration(in.DelaySeconds * float64(time.Second))).UTC().Format(time.RFC3339Nano)
		}
	case "Skill":
		f.dto.Skills = appendNew(f.dto.Skills, in.Skill)
	default:
		// MCP tools are named mcp__<server>__<tool>.
		if rest, ok := strings.CutPrefix(b.Name, "mcp__"); ok {
			server, _, _ := strings.Cut(rest, "__")
			f.dto.MCPServers = appendNew(f.dto.MCPServers, server)
		}
	}
}

func (f *activityFold) task(id, kind, description, command, at string) *activityTask {
	if i, ok := f.tasks[id]; ok {
		return &f.dto.Tasks[i]
	}
	f.tasks[id] = len(f.dto.Tasks)
	f.dto.Tasks = append(f.dto.Tasks, activityTask{
		ToolUseID: id, Kind: kind, Description: description, Command: command, State: activityRunning, StartedAt: at,
	})
	return &f.dto.Tasks[len(f.dto.Tasks)-1]
}

func (f *activityFold) user(rec activityRecord) {
	var text string
	if json.Unmarshal(rec.Message.Content, &text) == nil {
		f.notifications(text, rec.Timestamp)
		return
	}
	var blocks []activityBlock
	if json.Unmarshal(rec.Message.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			f.toolResult(b, rec.ToolUseResult, rec.Timestamp)
		case "text":
			f.notifications(b.Text, rec.Timestamp)
		}
	}
}

func (f *activityFold) toolResult(b activityBlock, raw json.RawMessage, at string) {
	call, ok := f.calls[b.ToolUseID]
	if !ok {
		return
	}
	delete(f.calls, b.ToolUseID)
	if b.IsError {
		summary := clip(blockText(b.Content), activitySummaryMax)
		if i, ok := f.agents[b.ToolUseID]; ok {
			a := &f.dto.Agents[i]
			a.State, a.EndedAt, a.Summary = activityFailed, at, summary
		}
		if i, ok := f.tasks[b.ToolUseID]; ok {
			t := &f.dto.Tasks[i]
			t.State, t.EndedAt, t.Summary = activityFailed, at, summary
		}
		return
	}
	var res activityResult
	if json.Unmarshal(raw, &res) != nil && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) && isActivityTool(call.name) {
		// A plain-text result carries no structure; an object this reader cannot
		// decode is counted.
		f.dto.Unreadable++
	}
	switch call.name {
	case "Agent", "Task":
		i, ok := f.agents[b.ToolUseID]
		if !ok {
			return
		}
		a := &f.dto.Agents[i]
		a.AgentID, a.Model = res.AgentID, res.ResolvedModel
		a.DurationMs, a.Tokens, a.ToolUses = res.TotalDurationMs, res.TotalTokens, res.TotalToolUseCount
		if a.AgentID != "" {
			f.agentIDs[a.AgentID] = i
		}
		if res.IsAsync || res.Status == "async_launched" || call.input.RunInBackground {
			// Launched to run on its own: a notification ends it.
			a.Background = true
			return
		}
		a.EndedAt, a.Summary = at, clip(blockText(res.Content), activitySummaryMax)
		a.State = activityCompleted
		if s := notificationState(res.Status); s != "" {
			a.State = s
		}
	case "Bash":
		if res.BackgroundTaskID != "" {
			// A command that outlived its timeout continues in the background too.
			t := f.task(b.ToolUseID, "shell", call.input.Description, call.input.Command, at)
			t.TaskID = res.BackgroundTaskID
			f.taskIDs[t.TaskID] = f.tasks[b.ToolUseID]
		}
	case "Monitor", "Workflow":
		if i, ok := f.tasks[b.ToolUseID]; ok && res.TaskID != "" {
			f.dto.Tasks[i].TaskID = res.TaskID
			f.taskIDs[res.TaskID] = i
			if call.name == "Workflow" && res.WorkflowRunID != "" {
				if !claudeSessionIDPattern.MatchString(res.WorkflowRunID) {
					f.dto.Unreadable++
					return
				}
				f.dto.Tasks[i].Workflow = &activityWorkflow{RunID: res.WorkflowRunID, Name: clip(res.WorkflowName, activitySummaryMax), Phases: []activityWorkflowPhase{}}
			}
		}
	case "TaskStop", "KillShell":
		id := call.input.TaskID
		if id == "" {
			id = call.input.ShellID
		}
		f.end(id, "", activityStopped, "", at)
	case "TodoWrite":
		// Claude Code's legacy models send their whole list each call.
		if call.input.Todos != nil {
			f.dto.Todos = make([]activityTodo, 0, len(call.input.Todos))
			clear(f.todoIDs)
			for _, t := range call.input.Todos {
				f.dto.Todos = append(f.dto.Todos, activityTodo{Content: t.Content, Status: t.Status, ActiveForm: t.ActiveForm})
			}
		}
	case "TaskCreate", "TaskUpdate":
		f.todo(call, res)
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		f.file(res, at)
	}
}

// todo keeps the task list a Claude 5 session writes one item at a time: TaskCreate
// adds an item under the id its result gives, TaskUpdate changes or deletes it. A
// create without an id or an update of an item the list lacks is counted: the list
// shown is short of what the tool keeps. A subagent's own records count nothing,
// since only the conversation's list is shown.
func (f *activityFold) todo(call activityCall, res activityResult) {
	in := call.input
	if call.name == "TaskCreate" {
		id := res.Task.ID
		if _, seen := f.todoIDs[id]; seen {
			// A repeated record is the same item.
			return
		}
		if id == "" {
			f.countMissedTodo()
			return
		}
		f.todoIDs[id] = len(f.dto.Todos)
		f.dto.Todos = append(f.dto.Todos, activityTodo{Content: in.Subject, Status: "pending", ActiveForm: in.ActiveForm})
		return
	}
	if res.Success != nil && !*res.Success {
		// The tool changed nothing.
		return
	}
	i, ok := f.todoIDs[in.TaskListID]
	if !ok {
		f.countMissedTodo()
		return
	}
	status := cmpOr(res.StatusChange.To, in.Status)
	if status == "deleted" {
		delete(f.todoIDs, in.TaskListID)
		f.dto.Todos = slices.Delete(f.dto.Todos, i, i+1)
		for id, j := range f.todoIDs {
			if j > i {
				f.todoIDs[id] = j - 1
			}
		}
		return
	}
	t := &f.dto.Todos[i]
	t.Status = cmpOr(status, t.Status)
	t.Content = cmpOr(in.Subject, t.Content)
	t.ActiveForm = cmpOr(in.ActiveForm, t.ActiveForm)
}

func (f *activityFold) countMissedTodo() {
	if !f.sidechain {
		f.dto.Unreadable++
	}
}

func (f *activityFold) file(res activityResult, at string) {
	if res.FilePath == "" {
		return
	}
	i, ok := f.files[res.FilePath]
	if !ok {
		i = len(f.dto.Files)
		f.files[res.FilePath] = i
		f.dto.Files = append(f.dto.Files, activityFile{Path: res.FilePath})
	}
	file := &f.dto.Files[i]
	file.Edits++
	file.LastAt = at
	lines := 0
	for _, hunk := range res.StructuredPatch {
		for _, l := range hunk.Lines {
			lines++
			switch {
			case strings.HasPrefix(l, "+"):
				file.Added++
			case strings.HasPrefix(l, "-"):
				file.Removed++
			}
		}
	}
	if res.Type == "create" {
		file.Created = true
		// A new file is reported with its content, not a patch.
		var content string
		if lines == 0 && json.Unmarshal(res.Content, &content) == nil && content != "" {
			file.Added += strings.Count(strings.TrimSuffix(content, "\n"), "\n") + 1
		}
	}
}

// mergeFiles adds the files a subagent changed to the session's.
func (f *activityFold) mergeFiles(files []activityFile) {
	for _, in := range files {
		i, ok := f.files[in.Path]
		if !ok {
			f.files[in.Path] = len(f.dto.Files)
			f.dto.Files = append(f.dto.Files, in)
			continue
		}
		file := &f.dto.Files[i]
		file.Added += in.Added
		file.Removed += in.Removed
		file.Edits += in.Edits
		file.Created = file.Created || in.Created
		file.LastAt = max(file.LastAt, in.LastAt)
	}
}

var taskNotificationPattern = regexp.MustCompile(`(?s)<task-notification>(.*?)</task-notification>`)

// notifications reads the ends of background work the tool reported to the
// conversation, each in a <task-notification> block naming its call and status.
func (f *activityFold) notifications(text, at string) {
	if !strings.Contains(text, "<task-notification>") {
		return
	}
	for _, m := range taskNotificationPattern.FindAllStringSubmatch(text, -1) {
		if state := notificationState(tagValue(m[1], "status")); state != "" {
			f.end(tagValue(m[1], "task-id"), tagValue(m[1], "tool-use-id"), state, tagValue(m[1], "summary"), at)
		}
	}
}

// end closes the task or agent a notification or a stop names, by its call or by
// the id the tool gave it. An item that already ended keeps how it ended.
func (f *activityFold) end(taskID, toolUseID, state, summary, at string) {
	summary = clip(summary, activitySummaryMax)
	if i, ok := lookup(f.tasks, toolUseID, f.taskIDs, taskID); ok {
		t := &f.dto.Tasks[i]
		if t.State == activityRunning || t.State == activityScheduled {
			t.State, t.EndedAt, t.Summary = state, at, cmpOr(summary, t.Summary)
		}
		return
	}
	if i, ok := lookup(f.agents, toolUseID, f.agentIDs, taskID); ok {
		a := &f.dto.Agents[i]
		if a.State == activityRunning {
			a.State, a.EndedAt, a.Summary = state, at, cmpOr(summary, a.Summary)
		}
	}
}

func lookup(byCall map[string]int, callID string, byID map[string]int, id string) (int, bool) {
	if i, ok := byCall[callID]; ok && callID != "" {
		return i, true
	}
	i, ok := byID[id]
	return i, ok && id != ""
}

// settle ends the wakeups whose time came before the tool's latest record: the
// tool woke and wrote after it.
func (f *activityFold) settle() {
	latest, err := time.Parse(time.RFC3339Nano, f.dto.UpdatedAt)
	if err != nil {
		return
	}
	for i := range f.dto.Tasks {
		t := &f.dto.Tasks[i]
		if due, err := time.Parse(time.RFC3339Nano, t.DueAt); t.State == activityScheduled && err == nil && due.Before(latest) {
			t.State, t.EndedAt = activityCompleted, t.DueAt
		}
	}
}

// close ends what a session that no longer runs left open: its tool is gone, and
// so is anything that ran under it.
func (f *activityFold) close() {
	for i := range f.dto.Agents {
		if f.dto.Agents[i].State == activityRunning {
			f.dto.Agents[i].State = activityStopped
		}
	}
	for i := range f.dto.Tasks {
		if s := f.dto.Tasks[i].State; s == activityRunning || s == activityScheduled {
			f.dto.Tasks[i].State = activityStopped
		}
		if w := f.dto.Tasks[i].Workflow; w != nil {
			for j := range w.Phases {
				if w.Phases[j].State == activityRunning {
					w.Phases[j].State = activityStopped
				}
			}
		}
	}
}

func notificationState(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "success":
		return activityCompleted
	case "failed", "error":
		return activityFailed
	case "", "running", "started", "in_progress":
		return ""
	}
	// Killed, stopped, cancelled, timed out: it ended without completing.
	return activityStopped
}

func tagValue(body, tag string) string {
	_, rest, ok := strings.Cut(body, "<"+tag+">")
	if !ok {
		return ""
	}
	value, _, _ := strings.Cut(rest, "</"+tag+">")
	return strings.TrimSpace(value)
}

// isActivityTool is whether the activity reads the tool's input or result.
func isActivityTool(name string) bool {
	switch name {
	case "Agent", "Task", "Bash", "Monitor", "Workflow", "ScheduleWakeup", "Skill",
		"TaskStop", "KillShell", "TodoWrite", "TaskCreate", "TaskUpdate", "Edit", "MultiEdit", "Write", "NotebookEdit":
		return true
	}
	return false
}

func appendNew(list []string, v string) []string {
	if v == "" || slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
