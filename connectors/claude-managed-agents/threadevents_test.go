// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// threadEventsFixture serves the session-event + thread + per-thread-event reads
// the ThreadEventReader pages ({data, next_page} envelopes — the sessions-API
// family). Events deliberately carry a content field the structural decode must
// never read.
func threadEventsFixture(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "" {
			t.Errorf("missing api key on %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/sessions/sesn_1/events":
			_, _ = w.Write([]byte(`{"data":[
				{"id":"evt_9","type":"status_idle","created_at":"2026-06-11T10:00:30Z","stop_reason":{"type":"requires_action","event_ids":["evt_2"]}},
				{"id":"evt_2","type":"tool_use","tool_name":"Bash","created_at":"2026-06-11T10:00:20Z","agent":{"id":"agdef_root"},"content":[{"type":"tool_use","input":{"command":"rm -rf /"}}]},
				{"id":"evt_1","type":"agent.thread_message","agent_ref":"agdef_root","peer_ref":"agdef_sub","created_at":"2026-06-11T10:00:10Z","content":"SECRET PAYLOAD"}
			],"next_page":""}`))
		case r.URL.Path == "/v1/sessions/sesn_1/threads":
			_, _ = w.Write([]byte(`{"data":[
				{"id":"sthr_1","status":"running","parent_thread_id":"","session_id":"sesn_1","agent":{"id":"agdef_root"},"created_at":"2026-06-11T10:00:00Z"},
				{"id":"sthr_2","status":"idle","parent_thread_id":"sthr_1","session_id":"sesn_1","agent":{"id":"agdef_sub"},"created_at":"2026-06-11T10:00:05Z"}
			],"next_page":""}`))
		case r.URL.Path == "/v1/sessions/sesn_1/threads/sthr_2/events" && r.URL.Query().Get("page") == "":
			_, _ = w.Write([]byte(`{"data":[
				{"id":"evt_s1","type":"tool_use","tool_name":"Read","created_at":"2026-06-11T10:00:15Z"}
			],"next_page":"p2"}`))
		case r.URL.Path == "/v1/sessions/sesn_1/threads/sthr_2/events" && r.URL.Query().Get("page") == "p2":
			_, _ = w.Write([]byte(`{"data":[
				{"id":"evt_s2","type":"tool_use","tool_use_id":"toolu_77","tool_name":"Write","created_at":"2026-06-11T10:00:25Z"}
			],"next_page":""}`))
		case strings.HasPrefix(r.URL.Path, "/v1/sessions/sesn_down"):
			http.Error(w, `{"error":"upstream sad"}`, http.StatusBadGateway)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

func newTestReader(t *testing.T, baseURL string) *ThreadEventReader {
	t.Helper()
	r, err := NewThreadEventReader(sdk.Config{Settings: map[string]string{
		"api_key": "k", "base_url": baseURL,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestThreadEventReaderReadsSessionAndSubThreads(t *testing.T) {
	srv := threadEventsFixture(t)
	defer srv.Close()
	r := newTestReader(t, srv.URL)

	evs, err := r.ThreadEvents(context.Background(), "sesn_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 5 {
		t.Fatalf("events = %d, want 5 (3 session + 2 sub-thread paged): %+v", len(evs), evs)
	}
	byID := map[string]ThreadEvent{}
	for _, ev := range evs {
		byID[ev.ID] = ev
		// Structural-only: nothing decoded may carry the content payload.
		for _, field := range []string{ev.Type, ev.AgentRef, ev.PeerRef, ev.ToolName, ev.ToolUseID} {
			if strings.Contains(field, "SECRET") || strings.Contains(field, "rm -rf") {
				t.Fatalf("content leaked into a structural field: %+v", ev)
			}
		}
	}
	// A tool_use event without an explicit tool_use_id carries the EVENT id (the
	// id stop_reason.event_ids blocks on).
	if got := byID["evt_2"]; got.ToolUseID != "evt_2" || got.ToolName != "Bash" || got.AgentRef != "agdef_root" {
		t.Fatalf("session tool_use mapping: %+v", got)
	}
	// An explicit tool_use_id wins over the event id.
	if got := byID["evt_s2"]; got.ToolUseID != "toolu_77" {
		t.Fatalf("explicit tool_use_id must win: %+v", got)
	}
	// Sub-thread events with no agent attribution fall back to the thread's agent.
	if got := byID["evt_s1"]; got.AgentRef != "agdef_sub" {
		t.Fatalf("sub-thread agent fallback: %+v", got)
	}
	// A2A attribution survives.
	if got := byID["evt_1"]; got.PeerRef != "agdef_sub" {
		t.Fatalf("peer_ref mapping: %+v", got)
	}
	// Chronological ordering by created_at.
	for i := 1; i < len(evs); i++ {
		if evs[i-1].CreatedAt > evs[i].CreatedAt {
			t.Fatalf("events out of order: %s then %s", evs[i-1].CreatedAt, evs[i].CreatedAt)
		}
	}
}

func TestThreadEventReaderErrorIsNeverPartial(t *testing.T) {
	srv := threadEventsFixture(t)
	defer srv.Close()
	r := newTestReader(t, srv.URL)
	evs, err := r.ThreadEvents(context.Background(), "sesn_down")
	if err == nil {
		t.Fatal("an upstream failure must return an error, never a partial list")
	}
	if evs != nil {
		t.Fatalf("no events on error, got %+v", evs)
	}
}

func TestThreadEventReaderRefusesTruncationAtMaxPages(t *testing.T) {
	// Review finding: a stream still paginating past max_pages must be an
	// ERROR, never a silently truncated list — the missing tail could hide the
	// very event a human must confirm.
	srv := threadEventsFixture(t)
	defer srv.Close()
	r, err := NewThreadEventReader(sdk.Config{Settings: map[string]string{
		"api_key": "k", "base_url": srv.URL, "max_pages": "1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	// sesn_1's sub-thread stream has 2 pages; max_pages=1 must refuse.
	if _, err := r.ThreadEvents(context.Background(), "sesn_1"); err == nil || !strings.Contains(err.Error(), "max_pages") {
		t.Fatalf("paging past the bound must error, got %v", err)
	}
}

func TestThreadEventReaderRequiresAPIKey(t *testing.T) {
	if _, err := NewThreadEventReader(sdk.Config{Settings: map[string]string{
		"webhook_secret": "whsec_x",
	}}); err == nil {
		t.Fatal("a reader without api_key must refuse to construct")
	}
	if _, err := r0(); err == nil {
		t.Fatal("an empty config must refuse to construct")
	}
}

func r0() (*ThreadEventReader, error) {
	return NewThreadEventReader(sdk.Config{Settings: map[string]string{}})
}
