// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// sessionActivity is the part of GET /runs/{ref}/activity the text form prints.
// The JSON form prints the engine's answer as it came.
type sessionActivity struct {
	Transcript     string   `json:"transcript"`
	Model          string   `json:"model"`
	Effort         string   `json:"effort"`
	ContextTokens  int64    `json:"context_tokens"`
	ContextWindow  *int64   `json:"context_window"`
	ContextPercent *float64 `json:"context_percent"`
	Compactions    []struct {
		At string `json:"at"`
	} `json:"compactions"`
	Agents []struct {
		Type        string `json:"type"`
		Description string `json:"description"`
		State       string `json:"state"`
		DurationMs  int64  `json:"duration_ms"`
		AgentID     string `json:"agent_id"`
	} `json:"agents"`
	Tasks []struct {
		Kind        string                   `json:"kind"`
		Description string                   `json:"description"`
		Command     string                   `json:"command"`
		State       string                   `json:"state"`
		Workflow    *sessionActivityWorkflow `json:"workflow"`
	} `json:"tasks"`
	Files []struct {
		Path    string `json:"path"`
		Added   int    `json:"added"`
		Removed int    `json:"removed"`
		Created bool   `json:"created"`
	} `json:"files"`
	Todos []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	} `json:"todos"`
	Skills     []string `json:"skills"`
	MCPServers []string `json:"mcp_servers"`
	Unreadable int      `json:"unreadable"`
}

type sessionActivityWorkflow struct {
	Name        string `json:"name"`
	AgentsTotal int    `json:"agents_total"`
	AgentsDone  int    `json:"agents_done"`
	Phases      []struct {
		Title       string `json:"title"`
		Detail      string `json:"detail"`
		State       string `json:"state"`
		AgentsTotal int    `json:"agents_total"`
		AgentsDone  int    `json:"agents_done"`
	} `json:"phases"`
}

// fetchSessionActivity reads what the session's tool is doing.
func (c *agentClientConfig) fetchSessionActivity(ctx context.Context, run map[string]any) (json.RawMessage, error) {
	_, b, err := c.do(ctx, "GET", sessionRunsPath+"/"+url.PathEscape(str(run, "run_ref"))+"/activity", nil)
	return b, err
}

func activityStateRole(state string) termrender.Role {
	switch state {
	case "completed":
		return termrender.RoleOK
	case "failed":
		return termrender.RoleFail
	case "running", "scheduled":
		return termrender.RoleWarn
	}
	return termrender.RoleMuted
}

// printSessionActivity prints the activity as the console's side panel shows it:
// context first, then agents, background tasks, files and the tool's own task list.
func printSessionActivity(w io.Writer, raw json.RawMessage, folder string) error {
	var a sessionActivity
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	r := renderTo(w)
	switch a.Transcript {
	case "pending":
		r.Line("The tool has not written its transcript yet.")
	case "missing":
		r.Line("This session's transcript is not in its account home, so what its tool did cannot be shown.")
	}
	model := termSafe(a.Model)
	if a.Effort != "" {
		model += " · effort " + termSafe(a.Effort)
	}
	window := fmt.Sprintf("%d tokens", a.ContextTokens)
	knownWindow := a.ContextWindow != nil && *a.ContextWindow > 0
	if knownWindow && a.ContextPercent == nil {
		window = fmt.Sprintf("%d / %d tokens", a.ContextTokens, *a.ContextWindow)
	}
	if n := len(a.Compactions); n > 0 {
		window += fmt.Sprintf(" · compacted %d×", n)
	}
	if knownWindow && a.ContextPercent != nil {
		window += fmt.Sprintf(" · %.1f%% of %d", *a.ContextPercent, *a.ContextWindow)
	} else if !knownWindow {
		window += " · window unknown"
	}
	fields := []termrender.Field{{Key: "model", Value: model}, {Key: "context", Value: window}}
	if len(a.Skills) > 0 {
		fields = append(fields, termrender.Field{Key: "skills", Value: termSafe(strings.Join(a.Skills, ", "))})
	}
	if len(a.MCPServers) > 0 {
		fields = append(fields, termrender.Field{Key: "mcp", Value: termSafe(strings.Join(a.MCPServers, ", "))})
	}
	if a.Unreadable > 0 {
		fields = append(fields, termrender.Field{Key: "unreadable", Value: fmt.Sprintf("%d records", a.Unreadable), Role: termrender.RoleWarn})
	}
	r.Fields(fields)

	agents := termrender.Table{Header: []string{"AGENT", "STATE", "TASK", "TIME", "ID"}, Empty: "No agents."}
	for _, ag := range a.Agents {
		took := ""
		if ag.DurationMs > 0 {
			took = (time.Duration(ag.DurationMs) * time.Millisecond).Round(100 * time.Millisecond).String()
		}
		agents.Rows = append(agents.Rows, []string{termSafe(ag.Type), termSafe(ag.State), termSafe(ag.Description), took, termSafe(ag.AgentID)})
		agents.Roles = append(agents.Roles, []termrender.Role{termrender.RoleNone, activityStateRole(ag.State)})
	}
	tasks := termrender.Table{Header: []string{"TASK", "STATE", "WHAT"}, Empty: "No background tasks."}
	for _, t := range a.Tasks {
		what := t.Description
		if what == "" {
			what = t.Command
		}
		tasks.Rows = append(tasks.Rows, []string{termSafe(t.Kind), termSafe(t.State), termSafe(what)})
		tasks.Roles = append(tasks.Roles, []termrender.Role{termrender.RoleNone, activityStateRole(t.State)})
	}
	files := termrender.Table{Header: []string{"FILE", "CHANGE"}, Empty: "No files changed by the tool's edits."}
	for _, f := range a.Files {
		change := fmt.Sprintf("+%d -%d", f.Added, f.Removed)
		if f.Created {
			change += " new"
		}
		files.Rows = append(files.Rows, []string{termSafe(relativeTo(folder, f.Path)), change})
	}
	for _, t := range []termrender.Table{agents, tasks, files} {
		r.Blank()
		r.Table(t)
	}
	for _, task := range a.Tasks {
		if w := task.Workflow; w != nil {
			r.Blank()
			r.Line(termSafe(w.Name))
			if w.AgentsTotal > 0 {
				r.Line(fmt.Sprintf("%d of %d agents done", w.AgentsDone, w.AgentsTotal))
			}
			for _, phase := range w.Phases {
				line := "  " + termSafe(phase.Title)
				if phase.State != "" {
					line += " · " + termSafe(phase.State)
				}
				if phase.AgentsTotal > 0 {
					line += fmt.Sprintf(" · %d of %d agents done", phase.AgentsDone, phase.AgentsTotal)
				}
				if phase.Detail != "" {
					line += " — " + termSafe(phase.Detail)
				}
				r.Line(line)
			}
		}
	}
	if len(a.Todos) > 0 {
		r.Blank()
		for _, t := range a.Todos {
			mark := "[ ]"
			switch t.Status {
			case "completed":
				mark = "[x]"
			case "in_progress":
				mark = "[>]"
			}
			r.Line(mark + " " + termSafe(t.Content))
		}
	}
	return nil
}

// relativeTo names a file inside the session's folder by its place in it.
func relativeTo(folder, path string) string {
	if folder == "" {
		return path
	}
	if rel, err := filepath.Rel(folder, path); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		return rel
	}
	return path
}
