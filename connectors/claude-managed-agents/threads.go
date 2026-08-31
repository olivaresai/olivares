// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// Thread is one multi-agent session thread (sthr_..., type "session_thread" — live
// schema, verified 2026-06-10): a sub-agent run a coordinator session spawned (max 25
// concurrent, depth 1; the roster lives on Session.agent.multiagent). The connector
// reads the run topology — which agent definition runs in which thread under which
// session — never thread events or message content (docs/SECURITY-HARDENING.md).
type Thread struct {
	ID             string `json:"id"`
	Status         string `json:"status"` // running|idle|rescheduling|terminated
	ParentThreadID string `json:"parent_thread_id"`
	SessionID      string `json:"session_id"`
	Agent          struct {
		ID string `json:"id"`
	} `json:"agent"`
	ArchivedAt string `json:"archived_at"`
	CreatedAt  string `json:"created_at"`
}

// threadPage is the thread-list envelope ({data, next_page} family — the sessions-API
// pagination, not the data/has_more/last_id of the older CMA list endpoints).
type threadPage struct {
	Data     []Thread `json:"data"`
	NextPage string   `json:"next_page"`
}

var errThreadPageLimit = errors.New("claude-managed-agents: thread list exceeds the max_pages bound — refusing a partial result (raise max_pages)")

// fetchThreads lists a session's threads (includes the primary thread, whose
// parent_thread_id is null). Bounded by maxPages.
func (c *client) fetchThreads(ctx context.Context, sessionID string) ([]Thread, error) {
	var out []Thread
	page := ""
	path := "/v1/sessions/" + sessionID + "/threads"
	for i := 0; i < c.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		q := url.Values{"limit": {"100"}}
		if page != "" {
			q.Set("page", page)
		}
		var p threadPage
		if err := c.getJSON(ctx, path, q, &p); err != nil {
			return out, err
		}
		out = append(out, p.Data...)
		if p.NextPage == "" {
			return out, nil
		}
		page = p.NextPage
	}
	return nil, errThreadPageLimit
}

// threadEdge maps a SUB-thread to the session → thread execution edge: the coordinator
// session runs this sub-agent thread (the OBSERVED counterpart of the roster's
// PERMITTED rosterEdges). The thread's agent definition rides ToolRef so the diff can
// relate the spawn to the declared roster. The PRIMARY thread (parent_thread_id empty)
// is the session itself — no edge (a self-loop adds nothing). ok is false for the
// primary thread or a thread with no id. ObservedAt is the thread's own created_at for
// stable cross-poll de-dup.
func threadEdge(t Thread, fallbackAt time.Time) (model.EdgeObservation, bool) {
	if t.ID == "" || t.ParentThreadID == "" {
		return model.EdgeObservation{}, false
	}
	at := parseTime(t.CreatedAt)
	if at.IsZero() {
		at = fallbackAt
	}
	return model.EdgeObservation{
		OriginKind:   originSession,
		OriginRef:    redact.Clean(t.SessionID),
		ResourceKind: kindManagedAgent,
		ResourceRef:  redact.Clean(t.ID),
		Mode:         model.ModeReadWrite,
		Source:       model.SignalCMA,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      redact.Clean(t.Agent.ID),
		ObservedAt:   at,
	}, true
}
