// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package googleadk

// session.go holds the ADK 2.0 Session/Event wire shapes the connector reads from
// exported session JSON, and the aggregation that turns them into a governance view.
// Only the minimal-data fields are declared and mapped — agent/app identity, tool
// (function-call) names, transfers, state-change and error COUNTS — never message
// text, prompts, completions or tool arguments (docs/SECURITY-HARDENING.md).
//
// Verification tier: VERIFIED-SHAPE against the ADK 2.0 Session/Event schema
// (google.github.io/adk-docs/sessions/session, /events; adk-docs/docs/2.0). A
// Session is {id, app_name, user_id, state, events[]}; an Event is {id,
// invocation_id, author, timestamp, content{parts[{function_call{name},
// function_response{name}}]}, actions{state_delta, transfer_to_agent, escalate},
// error_code, error_message} plus ADK-2.0 node_info/output. Unknown fields are
// ignored; the connector degrades gracefully on a partial export.

import (
	"sort"
	"strings"
)

// adkSession is one exported ADK conversation thread.
type adkSession struct {
	ID      string         `json:"id"`
	AppName string         `json:"app_name"`
	UserID  string         `json:"user_id"`
	State   map[string]any `json:"state"`
	Events  []adkEvent     `json:"events"`
}

type adkEvent struct {
	ID           string      `json:"id"`
	InvocationID string      `json:"invocation_id"`
	Author       string      `json:"author"`
	Content      *adkContent `json:"content"`
	Actions      *adkActions `json:"actions"`
	ErrorCode    string      `json:"error_code"`
}

type adkContent struct {
	Role  string    `json:"role"`
	Parts []adkPart `json:"parts"`
}

// adkPart: only the function-call/response NAMES are read (governance signal). The
// text field is deliberately NOT mapped — the connector never reads message content.
type adkPart struct {
	FunctionCall     *adkFunctionRef `json:"function_call"`
	FunctionResponse *adkFunctionRef `json:"function_response"`
}

type adkFunctionRef struct {
	Name string `json:"name"`
}

type adkActions struct {
	StateDelta      map[string]any `json:"state_delta"`
	TransferToAgent string         `json:"transfer_to_agent"`
	Escalate        bool           `json:"escalate"`
}

// appFacts is the aggregated governance view of one ADK application (app_name),
// treated as a first-class agent for inventory.
type appFacts struct {
	AppName        string
	Agents         map[string]struct{} // distinct event authors (agents/nodes)
	Users          map[string]struct{}
	Sessions       int
	Events         int
	ToolCalls      map[string]int // tool name -> call count
	Transfers      map[string]int // transfer_to_agent -> count
	StateWrites    int
	Errors         int
	Escalations    int
	unapprovedTool map[string]struct{} // tools used that are not on the allowlist
	truncated      map[string]struct{} // dimensions whose distinct-key cap was hit
}

func newAppFacts(name string) *appFacts {
	return &appFacts{
		AppName:        name,
		Agents:         map[string]struct{}{},
		Users:          map[string]struct{}{},
		ToolCalls:      map[string]int{},
		Transfers:      map[string]int{},
		unapprovedTool: map[string]struct{}{},
		truncated:      map[string]struct{}{},
	}
}

// markTruncated records that a dimension hit its distinct-key cap for this app.
func (af *appFacts) markTruncated(dim string) { af.truncated[dim] = struct{}{} }

// addSetCapped adds key to a set, capped at cap distinct entries. It returns false
// only when a NEW key is dropped because the cap is already reached (an existing key
// is always a no-op success).
func addSetCapped(m map[string]struct{}, key string, cap int) bool {
	if _, ok := m[key]; ok {
		return true
	}
	if len(m) >= cap {
		return false
	}
	m[key] = struct{}{}
	return true
}

// addCountCapped increments a counter map, capped at cap distinct keys. An existing
// key is always incremented; a NEW key past the cap is dropped and returns false so
// the counter (and the edge it drives) never grows without bound.
func addCountCapped(m map[string]int, key string, cap int) bool {
	if _, ok := m[key]; ok {
		m[key]++
		return true
	}
	if len(m) >= cap {
		return false
	}
	m[key]++
	return true
}

// aggregate folds a set of parsed sessions into per-app facts, applying the tool
// allowlist. approvedTools nil => no tool policy (inventory only). Both the number of
// distinct apps and the distinct keys per app are capped (see maxDistinctApps /
// maxDistinctKeys); appsTruncated is true when a new app_name was dropped at the app
// cap, so the caller can surface a scan-level truncation finding.
func aggregate(sessions []adkSession, approvedTools map[string]struct{}) (apps map[string]*appFacts, appsTruncated bool) {
	apps = map[string]*appFacts{}
	for _, s := range sessions {
		app := strings.TrimSpace(s.AppName)
		if app == "" {
			app = "unknown"
		}
		af := apps[app]
		if af == nil {
			if len(apps) >= maxDistinctApps {
				appsTruncated = true
				continue // drop this app: enumerating it would breach the app cap
			}
			af = newAppFacts(app)
			apps[app] = af
		}
		af.Sessions++
		if u := strings.TrimSpace(s.UserID); u != "" {
			if !addSetCapped(af.Users, u, maxDistinctKeys) {
				af.markTruncated("users")
			}
		}
		for _, e := range s.Events {
			af.Events++
			if a := strings.TrimSpace(e.Author); a != "" {
				if !addSetCapped(af.Agents, a, maxDistinctKeys) {
					af.markTruncated("agents")
				}
			}
			if e.ErrorCode != "" {
				af.Errors++
			}
			if e.Content != nil {
				for _, p := range e.Content.Parts {
					if p.FunctionCall != nil {
						if name := strings.TrimSpace(p.FunctionCall.Name); name != "" {
							if !addCountCapped(af.ToolCalls, name, maxDistinctKeys) {
								af.markTruncated("tools")
							} else if approvedTools != nil {
								if _, ok := approvedTools[strings.ToLower(name)]; !ok {
									if !addSetCapped(af.unapprovedTool, name, maxDistinctKeys) {
										af.markTruncated("unapproved-tools")
									}
								}
							}
						}
					}
				}
			}
			if e.Actions != nil {
				if len(e.Actions.StateDelta) > 0 {
					af.StateWrites++
				}
				if t := strings.TrimSpace(e.Actions.TransferToAgent); t != "" {
					if !addCountCapped(af.Transfers, t, maxDistinctKeys) {
						af.markTruncated("transfers")
					}
				}
				if e.Actions.Escalate {
					af.Escalations++
				}
			}
		}
	}
	return apps, appsTruncated
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedCountKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
