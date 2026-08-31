// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudebatch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func fixedClock() time.Time { return time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC) }

type routeDoer struct {
	reqs    []*http.Request
	handler func(*http.Request) (int, string)
}

func (d *routeDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	st, body := d.handler(req)
	return &http.Response{StatusCode: st, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

// Batch API fixture: two batches, one active and one ended.
func batchPage1() string {
	return `{"data":[
		{"id":"msgbatch_1","type":"message_batch","processing_status":"in_progress",
		 "request_counts":{"processing":50,"succeeded":0,"errored":0,"canceled":0,"expired":0},
		 "created_at":"2026-06-25T10:00:00Z","ended_at":"","expires_at":"2026-06-26T10:00:00Z"},
		{"id":"msgbatch_2","type":"message_batch","processing_status":"ended",
		 "request_counts":{"processing":0,"succeeded":95,"errored":5,"canceled":0,"expired":0},
		 "created_at":"2026-06-24T08:00:00Z","ended_at":"2026-06-24T09:00:00Z","expires_at":"2026-07-23T09:00:00Z"}
	],"has_more":false,"first_id":"msgbatch_1","last_id":"msgbatch_2"}`
}

// Files API fixture: two files, one fresh and one old (retention candidate).
func filePage1() string {
	return `{"data":[
		{"id":"file_fresh","filename":"report.pdf","mime_type":"application/pdf",
		 "size_bytes":1048576,"purpose":"agent","downloadable":true,
		 "created_at":"2026-06-25T09:00:00Z"},
		{"id":"file_old","filename":"data-export.csv","mime_type":"text/csv",
		 "size_bytes":5242880,"purpose":"batch_input","downloadable":true,
		 "created_at":"2026-05-01T00:00:00Z"}
	],"has_more":false,"first_id":"file_fresh","last_id":"file_old"}`
}

func newTestDoer() *routeDoer {
	return &routeDoer{handler: func(req *http.Request) (int, string) {
		if req.Method != http.MethodGet {
			return http.StatusMethodNotAllowed, `{"error":"GET only"}`
		}
		switch {
		case strings.HasPrefix(req.URL.Path, batchesPath):
			return http.StatusOK, batchPage1()
		case strings.HasPrefix(req.URL.Path, filesPath):
			return http.StatusOK, filePage1()
		default:
			return http.StatusNotFound, `{}`
		}
	}}
}

// TestGatherBatchAndFileInventory verifies the connector inventories batches and
// files, emitting FindingReport observations with correct kinds and subjects.
func TestGatherBatchAndFileInventory(t *testing.T) {
	doer := newTestDoer()
	s := New()
	s.doer = doer
	s.now = fixedClock
	cfg := sdk.Config{Settings: map[string]string{
		"admin_key": "sk-ant-admin01-test",
		"org_ref":   "acme",
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}

	var batches, files, retention, posture []model.FindingReport
	for _, o := range sink.obs {
		f, ok := o.(model.FindingReport)
		if !ok {
			continue
		}
		switch f.Kind {
		case findingKindBatchInventory:
			batches = append(batches, f)
		case findingKindFileInventory:
			files = append(files, f)
		case findingKindRetentionExpired:
			retention = append(retention, f)
		case findingKindPosture:
			posture = append(posture, f)
		}
	}

	if len(batches) != 2 {
		t.Fatalf("emitted %d batch findings, want 2", len(batches))
	}
	if batches[0].SubjectRef != "msgbatch_1" || batches[0].SubjectKind != "claude_batch" {
		t.Errorf("first batch finding: ref=%q kind=%q", batches[0].SubjectRef, batches[0].SubjectKind)
	}
	if !strings.Contains(batches[0].Title, "in_progress") {
		t.Errorf("batch title should contain status: %q", batches[0].Title)
	}
	if !strings.Contains(batches[0].Title, "50 lines") {
		t.Errorf("batch title should contain line count: %q", batches[0].Title)
	}

	if len(files) != 2 {
		t.Fatalf("emitted %d file findings, want 2", len(files))
	}
	if files[0].SubjectRef != "file_fresh" || files[0].SubjectKind != "claude_file" {
		t.Errorf("first file finding: ref=%q kind=%q", files[0].SubjectRef, files[0].SubjectKind)
	}
	if files[0].Severity != model.SeverityInfo {
		t.Errorf("clean filename should be info severity, got %q", files[0].Severity)
	}

	// The old file (created 2026-05-01) is 55 days old with default TTL=30d.
	if len(retention) != 1 {
		t.Fatalf("emitted %d retention findings, want 1 (file_old)", len(retention))
	}
	if retention[0].SubjectRef != "file_old" {
		t.Errorf("retention finding should be for file_old, got %q", retention[0].SubjectRef)
	}

	if len(posture) != 1 {
		t.Fatalf("emitted %d posture findings, want 1", len(posture))
	}
	if !strings.Contains(posture[0].Title, "2 batches") || !strings.Contains(posture[0].Title, "2 files") {
		t.Errorf("posture should count batches/files: %q", posture[0].Title)
	}
}

// TestReadOnly verifies every request the connector makes is GET.
func TestReadOnly(t *testing.T) {
	doer := newTestDoer()
	s := New()
	s.doer = doer
	s.now = fixedClock
	cfg := sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin01-test"}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	for _, req := range doer.reqs {
		if req.Method != http.MethodGet {
			t.Errorf("non-GET request %s %s — connector must be read-only", req.Method, req.URL.Path)
		}
	}
}

// TestOfflineEmitsPosture verifies that with no admin_key the connector emits a
// single honest-degradation posture finding and no network I/O.
func TestOfflineEmitsPosture(t *testing.T) {
	s := New()
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"org_ref": "acme"}}); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.obs) != 1 {
		t.Fatalf("offline should emit 1 posture finding, got %d", len(sink.obs))
	}
	f := sink.obs[0].(model.FindingReport)
	if f.Kind != findingKindPosture || f.Severity != model.SeverityLow {
		t.Errorf("offline finding: kind=%q sev=%q", f.Kind, f.Severity)
	}
	if !strings.Contains(f.Title, "offline") {
		t.Errorf("offline title should say offline: %q", f.Title)
	}
}

// TestPolicyMaxLines verifies that a batch exceeding max_lines emits a policy
// violation finding AND an EdgeObservation.
func TestPolicyMaxLines(t *testing.T) {
	doer := newTestDoer()
	s := New()
	s.doer = doer
	s.now = fixedClock
	cfg := sdk.Config{Settings: map[string]string{
		"admin_key":    "sk-ant-admin01-test",
		"batch_policy": `{"max_lines":40}`,
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}

	var violations []model.FindingReport
	var edges []model.EdgeObservation
	for _, o := range sink.obs {
		switch v := o.(type) {
		case model.FindingReport:
			if v.Kind == findingKindPolicyViolation {
				violations = append(violations, v)
			}
		case model.EdgeObservation:
			edges = append(edges, v)
		}
	}

	// msgbatch_1 has 50 lines (exceeds 40); msgbatch_2 has 100 lines (exceeds 40).
	if len(violations) != 2 {
		t.Fatalf("expected 2 policy violations (both batches exceed 40 lines), got %d", len(violations))
	}
	if len(edges) != 2 {
		t.Fatalf("expected 2 edge observations (PEP-ready), got %d", len(edges))
	}
	for _, v := range violations {
		if v.Severity != model.SeverityMedium {
			t.Errorf("max_lines violation severity should be medium, got %q", v.Severity)
		}
		if !strings.Contains(v.Title, "max_lines") {
			t.Errorf("violation title should mention max_lines: %q", v.Title)
		}
	}
	for _, e := range edges {
		if e.ResourceKind != "anthropic.batch_policy" || e.ResourceRef != "max_lines" {
			t.Errorf("edge resource: kind=%q ref=%q", e.ResourceKind, e.ResourceRef)
		}
		if e.Source != model.SignalPolicy {
			t.Errorf("edge source should be policy, got %q", e.Source)
		}
	}
}

// TestPolicyMalformedFails verifies that a malformed batch_policy fails Open.
func TestPolicyMalformedFails(t *testing.T) {
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		"admin_key":    "sk-ant-admin01-test",
		"batch_policy": `{INVALID`,
	}}
	err := s.Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("malformed batch_policy should fail Open")
	}
	if !strings.Contains(err.Error(), "batch_policy") {
		t.Errorf("error should mention batch_policy: %v", err)
	}
}

// TestRetentionTTL verifies that files older than upload_ttl emit a retention
// finding, and fresh files do not.
func TestRetentionTTL(t *testing.T) {
	doer := newTestDoer()
	s := New()
	s.doer = doer
	s.now = fixedClock
	cfg := sdk.Config{Settings: map[string]string{
		"admin_key":  "sk-ant-admin01-test",
		"upload_ttl": "168h", // 7 days
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}

	var retention []model.FindingReport
	for _, o := range sink.obs {
		f, ok := o.(model.FindingReport)
		if !ok {
			continue
		}
		if f.Kind == findingKindRetentionExpired {
			retention = append(retention, f)
		}
	}

	// file_old is 55 days old, file_fresh is 3 hours old. With TTL=7d, only file_old expires.
	if len(retention) != 1 {
		t.Fatalf("expected 1 retention finding (file_old), got %d", len(retention))
	}
	if retention[0].SubjectRef != "file_old" {
		t.Errorf("retention finding should be for file_old, got %q", retention[0].SubjectRef)
	}
	if retention[0].Severity != model.SeverityLow {
		t.Errorf("retention severity should be low, got %q", retention[0].Severity)
	}
}

// TestFilenamePIIScan verifies that a filename containing a secret shape elevates
// the file inventory finding to Medium severity.
func TestFilenamePIIScan(t *testing.T) {
	secretFilename := "export-sk-ant-api01-AAAA_BBBB_CCCC_DDDD_EEEE.csv"
	doer := &routeDoer{handler: func(req *http.Request) (int, string) {
		switch {
		case strings.HasPrefix(req.URL.Path, batchesPath):
			return http.StatusOK, `{"data":[],"has_more":false}`
		case strings.HasPrefix(req.URL.Path, filesPath):
			return http.StatusOK, `{"data":[
				{"id":"file_pii","filename":"` + secretFilename + `","mime_type":"text/csv",
				 "size_bytes":1024,"purpose":"batch_input","downloadable":true,
				 "created_at":"2026-06-25T11:00:00Z"}
			],"has_more":false,"first_id":"file_pii","last_id":"file_pii"}`
		default:
			return http.StatusNotFound, `{}`
		}
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	cfg := sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin01-test"}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}

	var fileFindings []model.FindingReport
	for _, o := range sink.obs {
		f, ok := o.(model.FindingReport)
		if !ok {
			continue
		}
		if f.Kind == findingKindFileInventory {
			fileFindings = append(fileFindings, f)
		}
	}
	if len(fileFindings) != 1 {
		t.Fatalf("expected 1 file finding, got %d", len(fileFindings))
	}
	if fileFindings[0].Severity != model.SeverityMedium {
		t.Errorf("PII filename should elevate severity to medium, got %q", fileFindings[0].Severity)
	}
	if strings.Contains(fileFindings[0].Title, secretFilename) {
		t.Error("raw secret filename leaked into finding title — should be redacted")
	}
}

// TestPagination verifies the connector paginates the batch endpoint (after_id).
func TestPagination(t *testing.T) {
	callCount := 0
	doer := &routeDoer{handler: func(req *http.Request) (int, string) {
		switch {
		case strings.HasPrefix(req.URL.Path, batchesPath):
			callCount++
			if req.URL.Query().Get("after_id") == "" {
				return http.StatusOK, `{"data":[
					{"id":"b1","type":"message_batch","processing_status":"ended",
					 "request_counts":{"processing":0,"succeeded":10,"errored":0,"canceled":0,"expired":0},
					 "created_at":"2026-06-25T10:00:00Z"}
				],"has_more":true,"first_id":"b1","last_id":"b1"}`
			}
			return http.StatusOK, `{"data":[
				{"id":"b2","type":"message_batch","processing_status":"ended",
				 "request_counts":{"processing":0,"succeeded":5,"errored":0,"canceled":0,"expired":0},
				 "created_at":"2026-06-25T11:00:00Z"}
			],"has_more":false,"first_id":"b2","last_id":"b2"}`
		case strings.HasPrefix(req.URL.Path, filesPath):
			return http.StatusOK, `{"data":[],"has_more":false}`
		default:
			return http.StatusNotFound, `{}`
		}
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	cfg := sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin01-test"}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}

	var batches []model.FindingReport
	for _, o := range sink.obs {
		f, ok := o.(model.FindingReport)
		if !ok {
			continue
		}
		if f.Kind == findingKindBatchInventory {
			batches = append(batches, f)
		}
	}
	if len(batches) != 2 {
		t.Fatalf("pagination should yield 2 batches, got %d", len(batches))
	}
	if callCount != 2 {
		t.Errorf("expected 2 batch API calls (page1 + page2), got %d", callCount)
	}
}

// TestDescriptor verifies the connector descriptor is well-formed.
func TestDescriptor(t *testing.T) {
	s := New()
	d := s.Descriptor()
	if d.Name != Name {
		t.Errorf("Name = %q, want %q", d.Name, Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want source", d.Type)
	}
	if len(d.ConfigFields) == 0 {
		t.Error("ConfigFields is empty")
	}
}
