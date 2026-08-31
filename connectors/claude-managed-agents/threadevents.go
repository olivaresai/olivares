// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
)

// This file is the READ surface the composition root constructs for the
// claude-agents console (the dedicated request-time reader pattern of claude-api:
// the runtime-owned Gather instance never serves module routes). It reads the
// session/thread event streams STRUCTURALLY — attribution only (type, agent, peer,
// tool name, ids, time), NEVER message or tool-input content (docs/SECURITY-HARDENING.md). The
// always-on Gather observer deliberately keeps its narrower topology-only capture;
// this reader answers a human's on-demand HITL question ("which tool is this
// session waiting on?"), a different, justified posture.

// ThreadEvent is one structurally-decoded event of a managed-agent session or
// sub-agent thread. Field availability is CONNECTOR-MODELED against the live
// sessions API family (verification: {data, next_page} pagination;
// stop_reason.event_ids reference blocking events by id) — absent fields stay
// empty, never invented.
type ThreadEvent struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	AgentRef  string `json:"agent_ref"`
	PeerRef   string `json:"peer_ref"`
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
	CreatedAt string `json:"created_at"`
}

// threadEventWire is the structural projection this reader decodes per event.
// Events ALSO carry message/tool content — no content field is declared, so the
// decoder cannot read it (the sessionEvent stance, sessions.go).
type threadEventWire struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	AgentRef  string `json:"agent_ref"`
	PeerRef   string `json:"peer_ref"`
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
	CreatedAt string `json:"created_at"`
	Agent     struct {
		ID string `json:"id"`
	} `json:"agent"`
}

// threadEventPage is the event-list envelope ({data, next_page} — the sessions-API
// pagination family, threads.go).
type threadEventPage struct {
	Data     []threadEventWire `json:"data"`
	NextPage string            `json:"next_page"`
}

// ThreadEventReader is the dedicated read-only thread-event reader. It shares the
// connector's config vocabulary and client but runs OUTSIDE the runtime scheduler
// (no webhook receiver, no pollers) — module-reachable reads construct their own
// instance in the composition root.
type ThreadEventReader struct {
	cl       *client
	maxPages int
}

// NewThreadEventReader builds a reader from the SAME settings a claude-managed-
// agents source is configured with (api_key/base_url/anthropic_version/beta_header/
// max_pages; webhook keys are ignored — this instance never binds a listener). An
// absent api_key is an error: a reader that cannot read must not exist (the caller
// then leaves the console's seam unwired — the honest empty posture).
func NewThreadEventReader(cfg sdk.Config) (*ThreadEventReader, error) {
	c, err := loadConfig(cfg)
	if err != nil {
		return nil, err
	}
	if c.apiKey == "" {
		return nil, errors.New("claude-managed-agents: the thread-event reader needs api_key (read-only GET access)")
	}
	return &ThreadEventReader{cl: newClient(c, nil), maxPages: c.maxPages}, nil
}

// ThreadEvents reads a session's event stream: the session-level list (the primary
// thread, where critical events such as tool confirmations are cross-posted) plus
// every sub-agent thread's stream (the only forensic record of what a subagent
// did). DENY-CLOSED on partial reads: any upstream error returns an error, never a
// truncated list presented as complete (a missing tail could hide the very event a
// human must confirm).
func (r *ThreadEventReader) ThreadEvents(ctx context.Context, sessionID string) ([]ThreadEvent, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("claude-managed-agents: session id is required")
	}
	var out []ThreadEvent

	// 1. The session-level (primary-thread) event list.
	evs, err := r.fetchEvents(ctx, "/v1/sessions/"+url.PathEscape(sessionID)+"/events", "")
	if err != nil {
		return nil, err
	}
	out = append(out, evs...)

	// 2. Every sub-agent thread's stream (primary thread has parent_thread_id "").
	threads, err := r.cl.fetchThreads(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	for _, t := range threads {
		if t.ID == "" || t.ParentThreadID == "" {
			continue
		}
		evs, err := r.fetchEvents(ctx,
			"/v1/sessions/"+url.PathEscape(sessionID)+"/threads/"+url.PathEscape(t.ID)+"/events",
			t.Agent.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}

	// Chronological by parsed created_at — a TOTAL order (zero/unparseable times
	// sort first, equal times keep stream order via the stable sort); a partial
	// ordering predicate here would let one bad timestamp shuffle good events.
	sort.SliceStable(out, func(i, j int) bool {
		return parseTime(out[i].CreatedAt).Before(parseTime(out[j].CreatedAt))
	})
	return out, nil
}

// fetchEvents pages one event stream, mapping each event structurally.
// fallbackAgent attributes a sub-thread's events to its agent definition when the
// event itself carries no agent ref. DENY-CLOSED on the page bound: a stream
// still paginating past max_pages is an error, never a silently truncated list —
// the missing tail could hide the very event a human must confirm.
func (r *ThreadEventReader) fetchEvents(ctx context.Context, path, fallbackAgent string) ([]ThreadEvent, error) {
	var out []ThreadEvent
	page := ""
	for i := 0; i < r.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"limit": {"100"}}
		if page != "" {
			q.Set("page", page)
		}
		var p threadEventPage
		if err := r.cl.getJSON(ctx, path, q, &p); err != nil {
			return nil, err
		}
		for _, w := range p.Data {
			out = append(out, mapThreadEvent(w, fallbackAgent))
		}
		if p.NextPage == "" {
			return out, nil
		}
		page = p.NextPage
	}
	return nil, errors.New("claude-managed-agents: event stream exceeds the max_pages bound — refusing a partial result (raise max_pages)")
}

// mapThreadEvent maps one wire event to the read DTO. The tool_use_id of a
// tool_use-family event without an explicit tool_use_id field is the EVENT id —
// the same id stop_reason.event_ids blocks on (sessions.go requiresAction), so
// the human confirmation references the id the API itself uses.
func mapThreadEvent(w threadEventWire, fallbackAgent string) ThreadEvent {
	agent := w.AgentRef
	if agent == "" {
		agent = w.Agent.ID
	}
	if agent == "" {
		agent = fallbackAgent
	}
	toolUse := w.ToolUseID
	if toolUse == "" && strings.Contains(w.Type, "tool_use") {
		toolUse = w.ID
	}
	return ThreadEvent{
		ID:        redact.Clean(w.ID),
		Type:      redact.Clean(w.Type),
		AgentRef:  redact.Clean(agent),
		PeerRef:   redact.Clean(w.PeerRef),
		ToolName:  redact.Clean(w.ToolName),
		ToolUseID: redact.Clean(toolUse),
		CreatedAt: redact.Clean(w.CreatedAt),
	}
}
