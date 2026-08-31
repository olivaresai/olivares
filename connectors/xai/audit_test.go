// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Tests in this file cover xAI audit wire currency verified against docs.x.ai on
// 2026-07-04, including the live nextPageToken shape and the absence of eventType.
package xai

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestGatherAuditEvents_Success(t *testing.T) {
	doer := &fixtureDoer{t: t}
	s := newSource(t, doer, map[string]string{"manage_keys": "false", "billing": "false"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	// The fixture has 2 events; each becomes an external_activity finding.
	var audits []model.FindingReport
	for _, f := range sink.findings() {
		if f.Kind == "external_activity" && f.SubjectKind == subjectAuditEvent {
			audits = append(audits, f)
		}
	}
	if len(audits) != 2 {
		t.Fatalf("audit findings = %d, want 2 (%+v)", len(audits), sink.findings())
	}
	// First event.
	if audits[0].SubjectRef != "evt_001" {
		t.Fatalf("first audit SubjectRef = %q, want evt_001", audits[0].SubjectRef)
	}
	if audits[0].Severity != model.SeverityInfo {
		t.Fatalf("first audit Severity = %v, want Info", audits[0].Severity)
	}
	if !strings.Contains(audits[0].Title, "API key created") {
		t.Fatalf("first audit Title = %q, want contains 'API key created'", audits[0].Title)
	}
	if !strings.HasPrefix(audits[0].Title, "xAI audit: ") {
		t.Fatalf("audit Title missing prefix: %q", audits[0].Title)
	}
	// OccurredAt should parse from the event's eventTime.
	wantTime := time.Date(2026, 6, 18, 10, 30, 0, 0, time.UTC)
	if !audits[0].OccurredAt.Equal(wantTime) {
		t.Fatalf("first audit OccurredAt = %v, want %v", audits[0].OccurredAt, wantTime)
	}
	// DetailHash must be a sha-256 hex (64 chars) and must NOT contain raw PII.
	for _, f := range audits {
		if len(f.DetailHash) != 64 {
			t.Fatalf("audit DetailHash not sha-256 hex: %q", f.DetailHash)
		}
		if strings.Contains(f.Title, "alice@") || strings.Contains(f.Title, "bob@") {
			t.Fatalf("audit title leaked actor email: %q", f.Title)
		}
	}
	// Second event.
	if audits[1].SubjectRef != "evt_002" || !strings.Contains(audits[1].Title, "Team member added") {
		t.Fatalf("second audit = %+v", audits[1])
	}
}

func TestGatherAuditEvents_LiveShapeV2NextPageToken(t *testing.T) {
	doer := &auditV2Doer{t: t}
	s := openWithDoer(t, doer, map[string]string{"manage_keys": "false", "billing": "false"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var audits []model.FindingReport
	for _, f := range sink.findings() {
		if f.Kind == "external_activity" && f.SubjectKind == subjectAuditEvent {
			audits = append(audits, f)
		}
	}
	if len(audits) != 2 {
		t.Fatalf("live audit findings = %d, want 2 (%+v)", len(audits), sink.findings())
	}
	if len(doer.auditReqs) != 2 {
		t.Fatalf("audit requests = %d, want 2 (nextPageToken followed)", len(doer.auditReqs))
	}
	q2 := doer.auditReqs[1].URL.Query()
	if got := q2.Get("pageToken"); got != "audit_page_2" {
		t.Fatalf("page-2 pageToken = %q, want audit_page_2", got)
	}
	if got := q2.Get("cursor"); got != "audit_page_2" {
		t.Fatalf("page-2 legacy cursor = %q, want audit_page_2", got)
	}
	for _, r := range doer.auditReqs {
		q := r.URL.Query()
		if q.Get("eventFilter.userId") != "" || q.Get("eventFilter.query") != "" ||
			q.Get("eventFilter.eventId") != "" || q.Get("orderBy") != "" {
			t.Fatalf("audit request used server-side filtering/orderBy despite full-window policy: %s", r.URL.RawQuery)
		}
	}
	for _, f := range audits {
		for _, pii := range []string{"carol@example.com", "diego@example.com", "Carol", "Rivera", "Diego", "Nguyen", "profile_"} {
			if strings.Contains(f.Title, pii) {
				t.Fatalf("live audit title leaked user identity %q: %q", pii, f.Title)
			}
		}
		if len(f.DetailHash) != 64 {
			t.Fatalf("live audit DetailHash not sha-256 hex: %q", f.DetailHash)
		}
	}
	if audits[0].SubjectRef != "evt_live_001" || !strings.Contains(audits[0].Title, "API key rotated") {
		t.Fatalf("first live audit = %+v", audits[0])
	}
}

func TestGatherAuditEvents_403Degrades(t *testing.T) {
	doer := &fixtureDoer{t: t, unavailable: map[string]bool{"/audit/teams/": true}}
	s := newSource(t, doer, map[string]string{"manage_keys": "false", "billing": "false"})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather must NOT fail on a 403/404 audit surface; got %v", err)
	}
	fs := sink.findings()
	// Should get exactly one posture finding about audit unavailability.
	var auditPosture []model.FindingReport
	for _, f := range fs {
		if f.SubjectKind == subjectAuditEvent {
			auditPosture = append(auditPosture, f)
		}
	}
	if len(auditPosture) != 1 {
		t.Fatalf("want 1 audit-unavailable posture finding, got %d (%+v)", len(auditPosture), fs)
	}
	f := auditPosture[0]
	if f.Kind != "posture" || f.Severity != model.SeverityMedium {
		t.Fatalf("audit unavailable finding = %+v", f)
	}
	if !strings.Contains(f.Title, "unavailable") {
		t.Fatalf("audit unavailable Title = %q", f.Title)
	}
}

func TestGatherAuditEvents_NoManagementKey(t *testing.T) {
	s := New()
	s.doer = &fixtureDoer{t: t}
	// Inference key only — no management key means Gather returns nil immediately.
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"api_key": "inf-test"}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	// No management key → Gather returns immediately, no audit calls at all.
	if len(sink.obs) != 0 {
		t.Fatalf("no-management-key gather emitted %d observations, want 0", len(sink.obs))
	}
}

func TestGatherAuditEvents_Pagination(t *testing.T) {
	// Page 1 returns has_more=true + cursor; page 2 closes.
	doer := &pagingAuditDoer{t: t}
	s := openWithDoer(t, doer, map[string]string{"manage_keys": "false", "billing": "false"})
	sink := &captureSink{}
	// Resolve team via the fixture (needs validation path).
	s.teamID = "team_default"
	if err := s.gatherAuditEvents(context.Background(), sink, "team_default"); err != nil {
		t.Fatalf("gatherAuditEvents: %v", err)
	}
	var audits []model.FindingReport
	for _, f := range sink.findings() {
		if f.SubjectKind == subjectAuditEvent {
			audits = append(audits, f)
		}
	}
	if len(audits) != 2 {
		t.Fatalf("paginated audit findings = %d, want 2 (one per page)", len(audits))
	}
	if len(doer.reqs) != 2 {
		t.Fatalf("requests = %d, want 2 (one per page)", len(doer.reqs))
	}
	// Second request must carry the cursor from page 1.
	if got := doer.reqs[1].URL.Query().Get("cursor"); got != "page2cursor" {
		t.Fatalf("page-2 cursor = %q, want page2cursor", got)
	}
}

// pagingAuditDoer synthesizes a cursor-paginated /audit/teams/.../events response:
// page 1 carries has_more=true + cursor, page 2 closes.
type pagingAuditDoer struct {
	t    *testing.T
	reqs []*http.Request
}

type auditV2Doer struct {
	t         *testing.T
	auditReqs []*http.Request
}

func (d *auditV2Doer) Do(req *http.Request) (*http.Response, error) {
	switch {
	case req.URL.Path == validationPath:
		return d.fixture("validation.json"), nil
	case strings.Contains(req.URL.Path, "/audit/teams/") && strings.HasSuffix(req.URL.Path, "/events"):
		d.auditReqs = append(d.auditReqs, req)
		if len(d.auditReqs) == 1 {
			return d.fixture("audit_events_v2.json"), nil
		}
		if req.URL.Query().Get("pageToken") != "audit_page_2" {
			d.t.Fatalf("unexpected live pageToken on page 2: %q", req.URL.RawQuery)
		}
		return resp(200, `{"events":[]}`), nil
	default:
		d.t.Fatalf("unexpected path %q", req.URL.Path)
		return nil, nil
	}
}

func (d *auditV2Doer) fixture(name string) *http.Response {
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		d.t.Fatalf("read fixture %s: %v", name, err)
	}
	return resp(200, string(body))
}

func (d *pagingAuditDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	if !strings.Contains(req.URL.Path, "/audit/teams/") {
		d.t.Fatalf("unexpected path %q", req.URL.Path)
	}
	cursor := req.URL.Query().Get("cursor")
	if cursor == "" {
		return resp(200, `{"events":[{"eventId":"p1","eventTime":"2026-06-18T00:00:00Z","description":"page1 event","user":{"userId":"u1","email":"u1@x.com","givenName":"U","familyName":"1"}}],"has_more":true,"cursor":"page2cursor"}`), nil
	}
	return resp(200, `{"events":[{"eventId":"p2","eventTime":"2026-06-19T00:00:00Z","description":"page2 event","user":{"userId":"u2","email":"u2@x.com","givenName":"U","familyName":"2"}}],"has_more":false,"cursor":""}`), nil
}
