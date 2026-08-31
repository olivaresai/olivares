// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package recording_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/recording"
)

// fakeTimelineResolver is a test double that returns a fixed session ref and
// timeline entries so the unified handler's cross-module path is exercised.
type fakeTimelineResolver struct {
	sessionRef string
	entries    []recording.TimelineEntry
	nextCursor string
	hasMore    bool
	pages      map[string]fakeTimelinePage
	cursors    []string
	err        error
}

type fakeTimelinePage struct {
	entries    []recording.TimelineEntry
	nextCursor string
	hasMore    bool
}

func (f *fakeTimelineResolver) ResolveTimeline(_ context.Context, _ model.TenantID, _ string, _ int, cursor string) (string, []recording.TimelineEntry, string, bool, error) {
	f.cursors = append(f.cursors, cursor)
	if f.err != nil {
		return "", nil, "", false, f.err
	}
	if page, ok := f.pages[cursor]; ok {
		return f.sessionRef, page.entries, page.nextCursor, page.hasMore, nil
	}
	return f.sessionRef, f.entries, f.nextCursor, f.hasMore, nil
}

// TestUnifiedEndpoint_ReturnsSessionAndFrames verifies that the unified
// endpoint returns the session header, the expected number of frames, and a
// non-nil verify verdict for a sealed recording session.
func TestUnifiedEndpoint_ReturnsSessionAndFrames(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op@x.io", "admin")

	// Three governance requests → three recorded frames.
	for i := 0; i < 3; i++ {
		if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("governance request %d: %d %s", i, r.code, r.raw)
		}
	}

	sess := h.sessions(admin, tenant, "?status=active")
	if len(sess) == 0 {
		t.Fatal("expected at least one active session")
	}
	id := sess[0]["id"].(string)

	// Seal before calling unified so the verify verdict covers the seal anchor.
	if r := h.do("POST", "/v1/m/recording/sessions/"+id+"/seal", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("seal = %d %s", r.code, r.raw)
	}

	r := h.do("GET", "/v1/m/recording/sessions/"+id+"/unified", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("unified: expected 200, got %d: %s", r.code, r.raw)
	}

	// Decode into the exported DTO.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.raw), &raw); err != nil {
		t.Fatalf("decode top-level: %v", err)
	}

	// Schema/semconv envelope.
	var schema, semconv string
	if err := json.Unmarshal(raw["schema"], &schema); err != nil || schema != "olivares.recording/v1" {
		t.Errorf("schema = %q, want olivares.recording/v1", schema)
	}
	if err := json.Unmarshal(raw["semconv"], &semconv); err != nil || semconv != "1.41.1" {
		t.Errorf("semconv = %q, want 1.41.1", semconv)
	}

	// Session header.
	var sessMap map[string]any
	if err := json.Unmarshal(raw["session"], &sessMap); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if sessMap["id"] != id {
		t.Errorf("session id = %q, want %q", sessMap["id"], id)
	}

	// Frames: expect 3 entries.
	var framesEnvelope map[string]any
	if err := json.Unmarshal(raw["frames"], &framesEnvelope); err != nil {
		t.Fatalf("decode frames: %v", err)
	}
	items, _ := framesEnvelope["items"].([]any)
	if len(items) != 3 {
		t.Errorf("frames.items len = %d, want 3", len(items))
	}

	// Verify verdict must be non-null (and OK=true since the session is valid).
	var verify map[string]any
	if err := json.Unmarshal(raw["verify"], &verify); err != nil || verify == nil {
		t.Fatalf("expected non-null verify verdict, got %q / err=%v", string(raw["verify"]), err)
	}
	if verify["ok"] != true {
		t.Errorf("verify.ok = %v, want true", verify["ok"])
	}

	// Timeline and ledger fields must be present. The default resolver is a
	// third state (unavailable), not evidence that the activity lane was empty.
	if _, ok := raw["timeline"]; !ok {
		t.Error("missing timeline field in unified response")
	}
	var timelineEnvelope map[string]any
	if err := json.Unmarshal(raw["timeline"], &timelineEnvelope); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	if timelineEnvelope["available"] != false {
		t.Errorf("timeline.available = %v, want false for noop resolver", timelineEnvelope["available"])
	}
	if _, ok := raw["ledger"]; !ok {
		t.Error("missing ledger field in unified response")
	}
}

// TestUnifiedEndpoint_NotFound verifies that the unified endpoint returns 404
// for a session id that does not exist in the tenant.
func TestUnifiedEndpoint_NotFound(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", "/v1/m/recording/sessions/00000000-0000-0000-0000-000000000001/unified",
		admin, nil, tenantHdr(tenant))
	if r.code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", r.code, r.raw)
	}
}

// TestUnifiedEndpoint_WithTimelineResolver verifies that a wired TimelineResolver
// populates the live correlation and timeline in the unified response.
func TestUnifiedEndpoint_WithTimelineResolver(t *testing.T) {
	resolver := &fakeTimelineResolver{
		sessionRef: "sessions-ref-abc",
		entries: []recording.TimelineEntry{
			{Kind: "tool_call", ToolRef: "bash", Title: "ran command"},
		},
	}
	h := newHarness(t, recording.WithTimelineResolver(resolver))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "op2@x.io", "admin")

	if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("governance request: %d %s", r.code, r.raw)
	}

	sess := h.sessions(admin, tenant, "?status=active")
	if len(sess) == 0 {
		t.Fatal("expected at least one active session")
	}
	id := sess[0]["id"].(string)

	r := h.do("GET", "/v1/m/recording/sessions/"+id+"/unified", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("unified: expected 200, got %d: %s", r.code, r.raw)
	}

	// live must be set to the session_ref returned by the resolver.
	live, _ := r.body["live"].(map[string]any)
	if live == nil {
		t.Fatal("expected live correlation in unified response")
	}
	if live["session_ref"] != "sessions-ref-abc" {
		t.Errorf("live.session_ref = %v, want sessions-ref-abc", live["session_ref"])
	}

	// timeline.items must carry the one entry from the resolver.
	tl, _ := r.body["timeline"].(map[string]any)
	if tl == nil {
		t.Fatal("expected timeline in unified response")
	}
	if tl["available"] != true {
		t.Errorf("timeline.available = %v, want true for wired resolver", tl["available"])
	}
	tlItems, _ := tl["items"].([]any)
	if len(tlItems) != 1 {
		t.Errorf("timeline.items len = %d, want 1", len(tlItems))
	} else {
		entry, _ := tlItems[0].(map[string]any)
		if entry["kind"] != "tool_call" || entry["tool_ref"] != "bash" {
			t.Errorf("timeline entry unexpected: %v", entry)
		}
	}

	// A resolver that is wired but cannot answer is unavailable, not an empty
	// successful timeline. The recording evidence response itself remains usable.
	resolver.err = errors.New("timeline backend unavailable")
	r = h.do("GET", "/v1/m/recording/sessions/"+id+"/unified", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("unified with failed timeline: expected 200, got %d: %s", r.code, r.raw)
	}
	tl, _ = r.body["timeline"].(map[string]any)
	if tl["available"] != false {
		t.Errorf("timeline.available = %v, want false after resolver failure", tl["available"])
	}
	if failedItems, _ := tl["items"].([]any); len(failedItems) != 0 {
		t.Errorf("timeline.items len = %d, want 0 after resolver failure", len(failedItems))
	}
}

// TestUnifiedEndpoint_TimelineCursorPagination proves the timeline cursor is
// independent from frame pagination and is passed through as an opaque value.
func TestUnifiedEndpoint_TimelineCursorPagination(t *testing.T) {
	resolver := &fakeTimelineResolver{
		sessionRef: "sessions-ref-paged",
		pages: map[string]fakeTimelinePage{
			"": {
				entries: []recording.TimelineEntry{
					{Kind: "tool", ToolRef: "Read", ResourceRef: "/first"},
				},
				nextCursor: "opaque-page-2",
				hasMore:    true,
			},
			"opaque-page-2": {
				entries: []recording.TimelineEntry{
					{Kind: "tool", ToolRef: "Edit", ResourceRef: "/second"},
				},
			},
		},
	}
	h := newHarness(t, recording.WithTimelineResolver(resolver))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, op := h.roleUser(admin, tenant, "paged@x.io", "admin")

	if r := h.do("GET", "/v1/m/governance/things", op, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("governance request: %d %s", r.code, r.raw)
	}
	sess := h.sessions(admin, tenant, "?status=active")
	if len(sess) == 0 {
		t.Fatal("expected at least one active session")
	}
	id := sess[0]["id"].(string)

	first := h.do("GET", "/v1/m/recording/sessions/"+id+"/unified?timeline_limit=1",
		admin, nil, tenantHdr(tenant))
	if first.code != http.StatusOK {
		t.Fatalf("first page: expected 200, got %d: %s", first.code, first.raw)
	}
	firstTimeline, _ := first.body["timeline"].(map[string]any)
	firstItems, _ := firstTimeline["items"].([]any)
	if len(firstItems) != 1 || firstItems[0].(map[string]any)["resource_ref"] != "/first" {
		t.Fatalf("first timeline page = %v, want /first", firstTimeline)
	}
	if firstTimeline["cursor"] != "opaque-page-2" || firstTimeline["has_more"] != true {
		t.Fatalf("first timeline pagination = %v, want cursor + has_more", firstTimeline)
	}

	second := h.do("GET", "/v1/m/recording/sessions/"+id+"/unified?timeline_limit=1&timeline_cursor=opaque-page-2",
		admin, nil, tenantHdr(tenant))
	if second.code != http.StatusOK {
		t.Fatalf("second page: expected 200, got %d: %s", second.code, second.raw)
	}
	secondTimeline, _ := second.body["timeline"].(map[string]any)
	secondItems, _ := secondTimeline["items"].([]any)
	if len(secondItems) != 1 || secondItems[0].(map[string]any)["resource_ref"] != "/second" {
		t.Fatalf("second timeline page = %v, want /second", secondTimeline)
	}
	if secondTimeline["has_more"] != false {
		t.Fatalf("second timeline has_more = %v, want false", secondTimeline["has_more"])
	}
	if _, ok := secondTimeline["cursor"]; ok {
		t.Fatalf("second timeline cursor must be omitted at the end: %v", secondTimeline)
	}
	if len(resolver.cursors) != 2 || resolver.cursors[0] != "" || resolver.cursors[1] != "opaque-page-2" {
		t.Fatalf("resolver cursors = %v, want [empty opaque-page-2]", resolver.cursors)
	}
}
