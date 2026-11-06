// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/auth"
)

// The tool table is the whole of the agent's power (design D1): a closed list, each tool
// one existing Olivares route and the permission string that route already demands. A
// tool outside it does not exist for the model. A new tool is a code change with a row
// here and a test, never a configuration value. Never in the table, whatever the role:
// any :admin verb, and in particular deciding an approval.
const (
	askPermRunsRead       auth.Permission = "sessions:run:read"
	askPermRunsWrite      auth.Permission = "sessions:run:write"
	askPermApprovalsRead  auth.Permission = "governance:approval:read"
	askPermApprovalsWrite auth.Permission = "governance:approval:write"
)

const (
	askKindRead  = "read"
	askKindWrite = "write"
	// maxArgLen bounds an argument so the command printed for it stays one readable line.
	askMaxArgLen = 80
)

// askTool is one row of the table. Args lists the argument names the model may pass (all
// strings); build turns the validated arguments into the request body and the one
// command that does the same thing from a terminal.
type askTool struct {
	Name       string
	Kind       string
	Permission auth.Permission
	Summary    string
	Method     string
	Path       string
	Args       []string
	build      func(args map[string]string) (body json.RawMessage, command string)
}

var askToolTable = []askTool{
	{
		Name: "runs.list", Kind: askKindRead, Permission: askPermRunsRead,
		Summary: "List the agent sessions.",
		Method:  "GET", Path: "/v1/m/sessions/runs",
		build: func(map[string]string) (json.RawMessage, string) { return nil, "olivares session ls" },
	},
	{
		Name: "approvals.list", Kind: askKindRead, Permission: askPermApprovalsRead,
		Summary: "List the approval requests and their state.",
		Method:  "GET", Path: "/v1/m/governance/approvals",
		build: func(map[string]string) (json.RawMessage, string) {
			return nil, "olivares governance approvals ls"
		},
	},
	{
		Name: "routing.list", Kind: askKindRead, Permission: permRoutingRead,
		Summary: "List the model routing policies.",
		Method:  "GET", Path: "/v1/m/models/routing-policies",
		build: func(map[string]string) (json.RawMessage, string) { return nil, "olivares models routing ls" },
	},
	{
		Name: "run.start", Kind: askKindWrite, Permission: askPermRunsWrite,
		Summary: "Start an agent session. Optional argument: name.",
		Method:  "POST", Path: "/v1/m/sessions/runs", Args: []string{"name"},
		build: func(a map[string]string) (json.RawMessage, string) {
			body, _ := json.Marshal(struct {
				Name      string `json:"name,omitempty"`
				Transport string `json:"transport"`
			}{a["name"], "stream-json"})
			command := "olivares session start"
			if a["name"] != "" {
				command += " --name " + askShellQuote(a["name"])
			}
			return body, command
		},
	},
}

// Outcomes of one turn.
const (
	askOutcomeAnswer   = "answer"
	askOutcomeCall     = "call"
	askOutcomeProposal = "proposal"
	askOutcomeRefused  = "refused"
)

// askCall is one checked tool call. Body and Command come from the table and the checked
// arguments, never from the model's text, so a proposal shows the exact action. The Ask
// module runs it itself, under the narrowed Ask principal; no client runs it.
type askCall struct {
	Tool       string
	Kind       string
	Permission auth.Permission
	Method     string
	Path       string
	Args       map[string]string
	Body       json.RawMessage
	Command    string
}

// askOutcome is what askPlan decided about one model reply.
type askOutcome struct {
	Kind   string
	Text   string
	Reason string
	Tool   string
	Call   *askCall
}

// askPlan decides what a model reply means. A reply that is not a tool call is the answer.
// A tool call is checked against the closed table, the strict argument list, the
// person's permission now (holds) and the caller's per-turn allow list (nil allows all
// tools): anything else is refused, never forwarded. A read becomes a call to run; a
// write becomes a proposal, and it never runs on the model's say-so.
func askPlan(output string, holds func(auth.Permission) bool, allow map[auth.Permission]bool) askOutcome {
	req, isCall := askParseCall(output)
	if !isCall {
		return askOutcome{Kind: askOutcomeAnswer, Text: output}
	}
	refuse := func(reason string) askOutcome {
		return askOutcome{Kind: askOutcomeRefused, Tool: askTruncate(req.Tool, askMaxArgLen), Reason: reason}
	}
	found := askToolNamed(req.Tool)
	if found == nil {
		return refuse("unknown tool")
	}
	if allow != nil && !allow[found.Permission] {
		return refuse("tool not allowed for this turn")
	}
	if !holds(found.Permission) {
		return refuse("you do not hold " + string(found.Permission))
	}
	// A write only opens an approval, so it also needs the permission to open one.
	if found.Kind == askKindWrite && !holds(askPermApprovalsWrite) {
		return refuse("you do not hold " + string(askPermApprovalsWrite))
	}
	args, reason := askCheckArgs(found, req.Args)
	if reason != "" {
		return refuse(reason)
	}
	body, command := found.build(args)
	kind := askOutcomeCall
	if found.Kind == askKindWrite {
		kind = askOutcomeProposal
	}
	return askOutcome{Kind: kind, Tool: found.Name, Call: &askCall{
		Tool: found.Name, Kind: found.Kind, Permission: found.Permission,
		Method: found.Method, Path: found.Path, Args: args, Body: body, Command: command,
	}}
}

// askToolNamed is the table row named name, or nil.
func askToolNamed(name string) *askTool {
	for i := range askToolTable {
		if askToolTable[i].Name == name {
			return &askToolTable[i]
		}
	}
	return nil
}

type askCallRequest struct {
	Tool string                     `json:"tool"`
	Args map[string]json.RawMessage `json:"args"`
}

// askParseCall reports whether the reply is one JSON object naming a tool, optionally in a
// code fence. Any other text, including JSON without a tool, is an answer.
func askParseCall(output string) (askCallRequest, bool) {
	text := strings.TrimSpace(output)
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```")
		text = strings.TrimPrefix(text, "json")
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "```"))
	}
	if !strings.HasPrefix(text, "{") {
		return askCallRequest{}, false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &probe); err != nil {
		return askCallRequest{}, false
	}
	if _, ok := probe["tool"]; !ok {
		return askCallRequest{}, false
	}
	var req askCallRequest
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		// A malformed call (wrong shape, extra keys) is still a call attempt: refuse it.
		return askCallRequest{Tool: "?"}, true
	}
	return req, true
}

// askCheckArgs applies the tool's strict schema: only listed names, each a short string
// without control characters.
func askCheckArgs(t *askTool, raw map[string]json.RawMessage) (map[string]string, string) {
	args := make(map[string]string, len(raw))
	for name, value := range raw {
		listed := false
		for _, want := range t.Args {
			listed = listed || want == name
		}
		if !listed {
			return nil, "unexpected argument " + askTruncate(name, 20)
		}
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return nil, "argument " + name + " must be a string"
		}
		if utf8.RuneCountInString(s) > askMaxArgLen {
			return nil, "argument " + name + " is too long"
		}
		for _, r := range s {
			if unicode.IsControl(r) {
				return nil, "argument " + name + " has a control character"
			}
		}
		args[name] = s
	}
	return args, ""
}

// askShellQuote quotes s as one POSIX shell word so the printed command runs exactly the
// proposed action.
func askShellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-/=:@") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func askTruncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
