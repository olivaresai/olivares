// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// Session is the governance-relevant subset of a CMA session resource, matching the
// LIVE GetSession schema (platform.claude.com/docs/en/api/beta/sessions/retrieve,
// verified 2026-06-10). The connector reads run state, the resolved agent snapshot
// (tool permission policies, multi-agent roster), the attached resources (memory-store
// mounts with their access mode), vault references and outcome-evaluation verdicts —
// never message content, tool inputs or deliverables (docs/SECURITY-HARDENING.md).
//
// Correction to the model: the session resource carries NO stop_reason
// field (verified ABSENT from the live schema) — stop_reason{type,event_ids} exists
// only on session.status_idle events. The requires_action HITL signal is therefore
// recovered from the session's event list (fetchSessionEvents), not from this resource.
type Session struct {
	ID            string              `json:"id"`
	Status        string              `json:"status"` // rescheduling|running|idle|terminated
	Agent         SessionAgent        `json:"agent"`  // resolved snapshot at session creation
	EnvironmentID string              `json:"environment_id"`
	Resources     []SessionResource   `json:"resources"`
	VaultIDs      []string            `json:"vault_ids"`
	Outcomes      []OutcomeEvaluation `json:"outcome_evaluations"`
	CreatedAt     string              `json:"created_at"`
	ArchivedAt    string              `json:"archived_at"`
}

// SessionAgent is the resolved agent snapshot embedded in a session (the session↔agent
// link is agent.id + agent.version; there is no bare agent_id field).
type SessionAgent struct {
	ID         string            `json:"id"`
	Version    int64             `json:"version"`
	Tools      []AgentTool       `json:"tools"`
	Skills     []SkillRef        `json:"skills"`
	Multiagent *MultiagentRoster `json:"multiagent"`
}

// AgentTool is one tools[] entry on an agent / session-agent snapshot. Three variants
// (verbatim, live schema): agent_toolset_20260401 (built-in tools bash|edit|read|write|
// glob|grep|web_fetch|web_search), mcp_toolset (an MCP server's tools), and custom.
// The permission surface is the permission_policy union {type: always_allow|always_ask}
// on default_config and per-tool configs[] (there is NO always_deny; a deny is expressed
// at confirmation time).
type AgentTool struct {
	Type          string            `json:"type"`
	Name          string            `json:"name"`            // custom variant only
	MCPServerName string            `json:"mcp_server_name"` // mcp_toolset variant only
	DefaultConfig ToolConfig        `json:"default_config"`
	Configs       []NamedToolConfig `json:"configs"`
}

// ToolConfig is a toolset's default per-tool configuration. Enabled is a pointer so
// "absent" (toolset present ⇒ available) is distinguishable from an explicit false.
type ToolConfig struct {
	Enabled          *bool            `json:"enabled"`
	PermissionPolicy PermissionPolicy `json:"permission_policy"`
}

// NamedToolConfig is a per-tool override inside a toolset's configs[].
type NamedToolConfig struct {
	Name             string           `json:"name"`
	Enabled          *bool            `json:"enabled"`
	PermissionPolicy PermissionPolicy `json:"permission_policy"`
}

// PermissionPolicy is the verbatim policy union: {"type": "always_allow"} or
// {"type": "always_ask"}.
type PermissionPolicy struct {
	Type string `json:"type"`
}

// Verbatim permission-policy types (live docs: agent toolset defaults to always_allow;
// MCP toolsets default to always_ask).
const (
	policyAlwaysAllow = "always_allow"
	policyAlwaysAsk   = "always_ask"
)

// agentToolsetBuiltins is the verbatim configs[].name enum of agent_toolset_20260401
// (live schema, verified 2026-06-10). It is the enumerable permitted set the toolset
// grants when present — the per-tool PERMITTED edges expand over it.
var agentToolsetBuiltins = []string{"bash", "edit", "read", "write", "glob", "grep", "web_fetch", "web_search"}

// Toolset type discriminators (verbatim).
const (
	toolsetBuiltin = "agent_toolset_20260401"
	toolsetMCP     = "mcp_toolset"
	toolsetCustom  = "custom"
)

// MultiagentRoster is the agent's top-level multiagent coordinator declaration: the
// roster of sub-agents the coordinator may spawn as threads (max 20 unique agents,
// 25 concurrent threads, depth 1 — live multi-agent docs).
type MultiagentRoster struct {
	Type   string        `json:"type"` // "coordinator"
	Agents []RosterEntry `json:"agents"`
}

// RosterEntry is one roster member: {type:"agent", id[, version]} or {type:"self"}.
type RosterEntry struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

// SessionResource is one resources[] entry. Only the memory_store variant is read
// (file/github_repository mounts carry no governed-memory dimension here); its access
// field is the read_only|read_write mount mode, enforced at the filesystem level and
// defaulting to read_write (live memory docs, verified 2026-06-10).
type SessionResource struct {
	Type          string `json:"type"`
	MemoryStoreID string `json:"memory_store_id"`
	Access        string `json:"access"` // "read_write" | "read_only"; empty = read_write (documented default)
}

// Memory-mount access modes (verbatim).
const (
	accessReadWrite = "read_write"
	accessReadOnly  = "read_only"
)

// OutcomeEvaluation is one grader verdict on an outcome-oriented session, matching the
// live outcome_evaluations[] schema (verified 2026-06-10): {type, outcome_id,
// description, explanation, iteration, result, completed_at}. There is NO usage field
// on the evaluation (the model carried one — fabricated vs the live schema; the
// grader's separate context-window cost is not attributable from this resource).
// result states: pending|running|evaluating are non-terminal; satisfied|
// max_iterations_reached|failed|interrupted are terminal (completed_at set).
type OutcomeEvaluation struct {
	OutcomeID   string `json:"outcome_id"`
	Result      string `json:"result"`
	Iteration   int    `json:"iteration"`
	Explanation string `json:"explanation"`
	CompletedAt string `json:"completed_at"`
}

// terminal reports whether the evaluation reached a terminal verdict. completed_at is
// the documented terminal marker; the result enum is checked as a defensive AND so an
// unexpectedly-stamped non-terminal row never emits a verdict finding.
func (ev OutcomeEvaluation) terminal() bool {
	if strings.TrimSpace(ev.CompletedAt) == "" {
		return false
	}
	switch ev.Result {
	case "pending", "running", "evaluating":
		return false
	default:
		return true
	}
}

// sessionPage is the sessions LIST envelope: {data, next_page} with an opaque `page`
// cursor (live ListSessions schema — NOT the data/has_more/last_id envelope the other
// CMA list endpoints use).
type sessionPage struct {
	Data     []Session `json:"data"`
	NextPage string    `json:"next_page"`
}

// fetchSession reads a single session's governance state. It is the GET-back a thin
// webhook delivery requires (a webhook carries only {type,id}; the resource is fetched
// by id to avoid stale data on retries).
func (c *client) fetchSession(ctx context.Context, sessionID string) (Session, error) {
	var s Session
	err := c.getJSON(ctx, "/v1/sessions/"+sessionID, nil, &s)
	return s, err
}

// fetchActiveSessions lists the workspace's non-terminated sessions (the active
// governance surface: an idle session may be awaiting a tool confirmation; a running
// or rescheduling one holds live mounts/vault references — the verified status enum is
// rescheduling|running|idle|terminated). Terminated sessions are observed event-driven
// via webhooks, not re-listed every poll. Pagination is the verified {data, next_page}
// envelope walked with the opaque `page` cursor, bounded by maxPages.
func (c *client) fetchActiveSessions(ctx context.Context) ([]Session, error) {
	var out []Session
	page := ""
	for i := 0; i < c.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		q := url.Values{"limit": {"100"}, "statuses": {"idle", "running", "rescheduling"}}
		if page != "" {
			q.Set("page", page)
		}
		var p sessionPage
		if err := c.getJSON(ctx, "/v1/sessions", q, &p); err != nil {
			return out, err
		}
		out = append(out, p.Data...)
		if p.NextPage == "" {
			break
		}
		page = p.NextPage
	}
	return out, nil
}

// fetchSessionsByMemoryStore lists sessions whose resources include the given memory
// store (the verified memory_store_id list filter) — the attach-observation probe the
// Dreams output-store admission check runs. A single bounded page: the check needs
// existence + attribution, not an exhaustive census.
func (c *client) fetchSessionsByMemoryStore(ctx context.Context, storeID string) ([]Session, error) {
	var p sessionPage
	q := url.Values{"limit": {"100"}, "memory_store_id": {storeID}}
	if err := c.getJSON(ctx, "/v1/sessions", q, &p); err != nil {
		return nil, err
	}
	return p.Data, nil
}

// sessionEvent is the minimal structural projection of one session event: its type and
// the stop_reason a status_idle event carries. Events ALSO carry message/tool content —
// the decoder reads none of it (only these two fields are declared), upholding the
// structural-only posture (docs/SECURITY-HARDENING.md).
type sessionEvent struct {
	Type       string `json:"type"`
	StopReason struct {
		Type     string   `json:"type"`
		EventIDs []string `json:"event_ids"`
	} `json:"stop_reason"`
}

// sessionEventPage is the session event-list envelope ({data, next_page} family).
type sessionEventPage struct {
	Data     []sessionEvent `json:"data"`
	NextPage string         `json:"next_page"`
}

// requiresAction is the stop_reason.type that signals an always_ask permission policy
// is awaiting a human tool-confirmation (user.tool_confirmation); the blocking event
// ids ride stop_reason.event_ids (live events-and-streaming docs, verified 2026-06-10).
const requiresAction = "requires_action"

// fetchAwaitingConfirmation reads the session's event list (one bounded page — the
// session-level view is the primary thread, where critical events such as tool
// confirmations are cross-posted) and reports whether the LATEST status_idle-family
// event paused on requires_action, plus how many event ids are blocking. The decode is
// structural-only (sessionEvent declares no content field).
//
// Ordering: the CMA list family is newest-first (the convention every sibling list in
// this package documents), so the FIRST status_idle in the page is the latest idle and
// decides; older idles are superseded. The probe runs event-driven off the
// session.status_idled webhook, so the just-fired idle sits at the head of the page.
func (c *client) fetchAwaitingConfirmation(ctx context.Context, sessionID string) (blocking int, awaiting bool, err error) {
	var p sessionEventPage
	if err := c.getJSON(ctx, "/v1/sessions/"+sessionID+"/events", nil, &p); err != nil {
		return 0, false, err
	}
	for _, ev := range p.Data {
		if !strings.Contains(ev.Type, "status_idle") {
			continue
		}
		if ev.StopReason.Type == requiresAction && len(ev.StopReason.EventIDs) > 0 {
			return len(ev.StopReason.EventIDs), true, nil
		}
		return 0, false, nil // the latest idle did not pause on requires_action
	}
	return 0, false, nil
}

// sessionObservations maps one fetched session to its governed-object observations:
// the memory-store mount edges (with the verified access mode), the read_write-mount
// poisoning posture finding (ASI06 — the write target a prompt-injected agent can
// poison), the session→vault use edges, and the terminal outcome verdict findings.
// Stable timestamps (session created_at / evaluation completed_at) make re-emission
// across polls/webhooks de-dup downstream.
func sessionObservations(s Session, fallbackAt time.Time) []model.Observation {
	at := parseTime(s.CreatedAt)
	if at.IsZero() {
		at = fallbackAt
	}
	sid := redact.Clean(s.ID)
	var out []model.Observation
	for _, r := range s.Resources {
		if r.Type != "memory_store" || r.MemoryStoreID == "" {
			continue
		}
		mode := model.ModeReadWrite // documented default when access is absent
		if r.Access == accessReadOnly {
			mode = model.ModeRead
		}
		out = append(out, model.EdgeObservation{
			OriginKind:   originSession,
			OriginRef:    sid,
			ResourceKind: kindMemoryStore,
			ResourceRef:  redact.Clean(r.MemoryStoreID),
			Mode:         mode,
			Source:       model.SignalCMA,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   at,
		})
		if mode == model.ModeReadWrite {
			out = append(out, model.FindingReport{
				Kind:        findingPosture,
				Severity:    model.SeverityLow,
				SubjectKind: kindMemoryStore,
				SubjectRef:  redact.Clean(r.MemoryStoreID),
				Title:       "CMA memory store mounted read_write — a prompt-injection write target",
				DetailHash:  redact.Hash("memory mount session=" + s.ID + " store=" + r.MemoryStoreID + " access=read_write; a writable memory mount lets a prompt-injected agent poison persistent memory other sessions consume (CMA memory)"),
				OWASPASI:    []string{asiMemoryPoison},
				OccurredAt:  at,
			})
		}
	}
	for _, v := range s.VaultIDs {
		if v == "" {
			continue
		}
		out = append(out, model.EdgeObservation{
			OriginKind:   originSession,
			OriginRef:    sid,
			ResourceKind: kindVault,
			ResourceRef:  redact.Clean(v),
			Mode:         model.ModeRead,
			Source:       model.SignalCMA,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   at,
		})
	}
	for _, ev := range s.Outcomes {
		if f, ok := outcomeFinding(s.ID, ev, at); ok {
			out = append(out, f)
		}
	}
	return out
}
