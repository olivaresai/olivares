// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// secRequest is one request the fake engine saw.
type secRequest struct {
	Method string
	Path   string
	Query  url.Values
	Body   map[string]any
}

// secEngine is the part of the engine the Security screens talk to: the
// approval queue, the kill switch and the audit ledger. It answers in the shapes
// of modules/governance and core/api, and records every request.
type secEngine struct {
	*httptest.Server
	mu            sync.Mutex
	approvals     []map[string]any
	estateStopped bool
	stateStatus   int
	events        []map[string]any
	log           []secRequest
	// pageCap caps the events one audit answer carries, like a scan bound does;
	// approvalsHasMore makes the queue say there are more pages.
	pageCap          int
	approvalsHasMore bool
	queueStatus      int
	// gate holds every POST until it is closed, so a test can leave while a write
	// is on its way; emptyAnswers makes the queue and the kill switch state answer
	// 200 with no body, as a proxy that drops it does.
	gate         chan struct{}
	emptyAnswers bool
}

func secApproval(id, status, reason string) map[string]any {
	return map[string]any{
		"id": id, "action": "sessions.run.launch", "policy_ref": "dlp.require-approval", "subject_kind": "workspace", "subject_ref": "workspace:ws-1",
		"requested_by": "rehema@acme.example", "status": status, "risk_tier": "high",
		"required_approvals": 1, "approve_count": 0, "reject_count": 0,
		"expires_at": time.Now().Add(23 * time.Hour).UTC().Format(time.RFC3339), "reason": reason,
	}
}

func secEvent(seq int, at time.Time, actor, action, kind, id string) map[string]any {
	return map[string]any{"seq": seq, "occurred_at": at.UTC().Format(time.RFC3339), "actor": actor,
		"action": action, "target_kind": kind, "target_id": id}
}

func newSecEngine(t *testing.T) *secEngine {
	t.Helper()
	e := &secEngine{stateStatus: http.StatusOK}
	e.Server = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.Close)
	t.Setenv("OLIVARES_SERVER_URL", e.URL)
	t.Setenv("OLIVARES_TOKEN", "test-token")
	t.Setenv("OLIVARES_TENANT", "tenant-a")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return e
}

func (e *secEngine) reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (e *secEngine) serve(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	gate := e.gate
	e.mu.Unlock()
	if r.Method == http.MethodPost && gate != nil {
		<-gate
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	req := secRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query()}
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &req.Body)
		}
	}
	e.log = append(e.log, req)
	const gov = "/v1/m/governance"
	switch {
	case r.URL.Path == "/livez": // what `olivares ui` asks before it opens the Security screens
		e.reply(w, http.StatusOK, map[string]any{"status": "ok"})
	case r.URL.Path == "/v1/auth/whoami":
		e.reply(w, http.StatusOK, map[string]any{"display_name": "Rehema", "user_id": "u1"})
	case e.emptyAnswers && r.Method == http.MethodGet && (r.URL.Path == gov+"/approvals" || r.URL.Path == gov+"/killswitch/state"):
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && r.URL.Path == gov+"/approvals":
		items := []map[string]any{}
		for _, a := range e.approvals {
			if want := req.Query.Get("status"); want == "" || a["status"] == want {
				items = append(items, a)
			}
		}
		if e.queueStatus != 0 {
			e.reply(w, e.queueStatus, map[string]any{"error": map[string]any{"message": "the approval queue is unavailable"}})
			return
		}
		e.reply(w, http.StatusOK, map[string]any{"items": items, "has_more": e.approvalsHasMore})
	case strings.HasPrefix(r.URL.Path, gov+"/approvals/"):
		rest := strings.TrimPrefix(r.URL.Path, gov+"/approvals/")
		id, decision, _ := strings.Cut(rest, "/")
		for _, a := range e.approvals {
			if a["id"] != id {
				continue
			}
			if r.Method == http.MethodPost && decision == "decisions" {
				if a["status"] != "pending" {
					e.reply(w, http.StatusConflict, map[string]any{"error": map[string]any{"message": "approval is " + fmt.Sprint(a["status"]) + "; no decision can be recorded"}})
					return
				}
				if req.Body["decision"] == "approve" {
					a["status"], a["approve_count"] = "approved", 1
				} else {
					a["status"] = "rejected"
				}
			}
			e.reply(w, http.StatusOK, a)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case r.Method == http.MethodGet && r.URL.Path == gov+"/killswitch/state":
		if e.stateStatus != http.StatusOK {
			e.reply(w, e.stateStatus, map[string]any{"error": map[string]any{"message": "you need the kill switch read right"}})
			return
		}
		e.reply(w, http.StatusOK, map[string]any{"estate_stopped": e.estateStopped, "active": []any{}})
	case r.Method == http.MethodPost && r.URL.Path == gov+"/killswitch":
		if e.estateStopped {
			e.reply(w, http.StatusConflict, map[string]any{"error": "an active stop for this scope already exists"})
			return
		}
		e.estateStopped = true
		e.reply(w, http.StatusCreated, map[string]any{"id": "ks-1", "scope_kind": "estate", "status": "engaged", "reason": req.Body["reason"]})
	case r.Method == http.MethodGet && (r.URL.Path == "/v1/audit" || r.URL.Path == "/v1/audit/system"):
		e.reply(w, http.StatusOK, e.auditPage(req.Query))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// auditPage answers the ledger the way the API does: oldest first, from ?from,
// with the filters it supports.
func (e *secEngine) auditPage(q url.Values) map[string]any {
	from, _ := strconv.Atoi(q.Get("from"))
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= 0 {
		limit = 100
	}
	if e.pageCap > 0 && limit > e.pageCap {
		limit = e.pageCap
	}
	items := []map[string]any{}
	next := len(e.events) + 1
	more := false
	for _, ev := range e.events {
		if int(ev["seq"].(int)) < from {
			continue
		}
		if actor := q.Get("actor"); actor != "" && ev["actor"] != actor {
			continue
		}
		if action := q.Get("action"); action != "" && !strings.HasPrefix(ev["action"].(string), action) {
			continue
		}
		if text := strings.ToLower(q.Get("q")); text != "" && !strings.Contains(strings.ToLower(fmt.Sprint(ev["actor"], ev["action"], ev["target_kind"], ev["target_id"])), text) {
			continue
		}
		skip := false
		for _, ex := range q["exclude_action"] {
			skip = skip || strings.HasPrefix(ev["action"].(string), ex)
		}
		if since := q.Get("since"); since != "" {
			at, _ := time.Parse(time.RFC3339, ev["occurred_at"].(string))
			cut, err := time.Parse(time.RFC3339, since)
			skip = skip || err != nil || at.Before(cut)
		}
		if skip {
			continue
		}
		if len(items) == limit {
			more = true
			break
		}
		items = append(items, ev)
		next = ev["seq"].(int) + 1
	}
	return map[string]any{"items": items, "head_seq": len(e.events), "has_more": more, "scan_complete": !more, "next_from": next}
}

func (e *secEngine) requests(method, path string) []secRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []secRequest
	for _, r := range e.log {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// writes are the requests that change something: what the ledger would record.
func (e *secEngine) writes() []secRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []secRequest
	for _, r := range e.log {
		if r.Method == http.MethodPost {
			out = append(out, r)
		}
	}
	return out
}

// newTestScreens is the screens on the fake engine, with the commands' own
// resolution of the server, token and tenant from the environment.
func newTestScreens(t *testing.T) *securityScreens {
	t.Helper()
	cmd := &cobra.Command{Use: "ui"}
	cmd.SetContext(context.Background())
	return newSecurityScreens(cmd, &uiTools{})
}

func ledgerEngine(t *testing.T) *secEngine {
	t.Helper()
	e := newSecEngine(t)
	base := time.Now().Add(-3 * time.Hour)
	e.events = []map[string]any{
		secEvent(1, base, "rehema@acme.example", "session.started", "session", "s-1"),
		secEvent(2, base.Add(time.Hour), "johan@acme.example", "approval.requested", "approval", "apr-9"),
		secEvent(3, base.Add(90*time.Minute), "system", "checkpoint.signed", "checkpoint", "48213"),
		secEvent(4, base.Add(2*time.Hour), "rehema@acme.example", "tool.login", "tool", "codex"),
		secEvent(5, base.Add(150*time.Minute), "system", "audit.read", "audit", ""),
	}
	return e
}
