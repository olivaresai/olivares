// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openai

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// asstDoer multiplexes by request path to the assistants/admin fixtures, supports
// returning 403 for selected paths, and records every request.
type asstDoer struct {
	t           *testing.T
	reqs        []*http.Request
	unavailable map[string]bool
	statuses    map[string]int
}

func (d *asstDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	if status, ok := d.statuses[req.URL.Path]; ok {
		return asstResp(status, `{"error":"unavailable"}`), nil
	}
	if d.unavailable[req.URL.Path] {
		return asstResp(403, `{"error":"forbidden"}`), nil
	}
	var file string
	switch {
	case req.URL.Path == "/v1/assistants":
		file = "assistants.json"
	case req.URL.Path == "/v1/files":
		file = "files.json"
	case req.URL.Path == "/v1/vector_stores":
		file = "vector_stores.json"
	case req.URL.Path == "/v1/organization/invites":
		file = "invites.json"
	case req.URL.Path == "/v1/organization/projects":
		file = "projects.json"
	case strings.HasSuffix(req.URL.Path, "/users") && strings.Contains(req.URL.Path, "/projects/"):
		file = "project_users.json"
	case strings.HasSuffix(req.URL.Path, "/service_accounts") && strings.Contains(req.URL.Path, "/projects/"):
		file = "project_service_accounts.json"
	case strings.HasSuffix(req.URL.Path, "/api_keys") && strings.Contains(req.URL.Path, "/projects/"):
		file = "project_api_keys.json"
	case req.URL.Path == "/v1/organization/usage/completions":
		file = "usage_completions.json"
	case req.URL.Path == "/v1/organization/usage/moderations":
		file = "usage_moderations.json"
	case req.URL.Path == "/v1/organization/users":
		file = "org_users.json"
	case req.URL.Path == "/v1/organization/audit_logs":
		file = "audit_logs.json"
	case req.URL.Path == "/v1/organization/data_retention":
		file = "data_retention.json"
	case strings.HasPrefix(req.URL.Path, "/v1/organization/projects/") && strings.HasSuffix(req.URL.Path, "/data_retention"):
		parts := strings.Split(req.URL.Path, "/")
		projID := parts[4]
		file = "data_retention_" + projID + ".json"
		body, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			body, err = os.ReadFile(filepath.Join("testdata", "data_retention.json"))
			if err != nil {
				d.t.Fatalf("read fixture: %v", err)
			}
		}
		return asstResp(200, string(body)), nil
	case req.URL.Path == "/v1/models":
		file = "models.json"
	case req.URL.Path == "/v1/organization/admin_api_keys":
		file = "admin_api_keys.json"
	case req.URL.Path == "/v1/organization/costs":
		file = "costs.json"
	default:
		d.t.Fatalf("unexpected request path %q", req.URL.Path)
	}
	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		d.t.Fatalf("read fixture %s: %v", file, err)
	}
	return asstResp(200, string(body)), nil
}

func asstResp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func newAsstSource(t *testing.T, doer *asstDoer, over map[string]string) *Source {
	s := New()
	s.doer = doer
	s.now = fixedClock
	settings := map[string]string{
		"api_key":    "sk-openai-admin-test",
		"assistants": "true",
		"admin":      "true",
	}
	for k, v := range over {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// ---------- Assistants inventory ----------

func TestGatherAssistants_Success(t *testing.T) {
	doer := &asstDoer{t: t}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherAssistants(context.Background(), sink); err != nil {
		t.Fatalf("gatherAssistants: %v", err)
	}
	fs := govFindings(sink.obs)
	var inventory []model.FindingReport
	for _, f := range fs {
		if f.Kind == "inventory" {
			inventory = append(inventory, f)
		}
	}
	if len(inventory) != 3 {
		t.Fatalf("emitted %d inventory findings, want 3", len(inventory))
	}
	for _, f := range inventory {
		if f.Kind != "inventory" || f.SubjectKind != subjectAssistant {
			t.Fatalf("finding = %+v", f)
		}
		if f.Severity != model.SeverityInfo {
			t.Fatalf("severity = %q, want info", f.Severity)
		}
	}
	if inventory[0].SubjectRef != "asst_abc123" {
		t.Fatalf("first assistant = %q", inventory[0].SubjectRef)
	}
	if !strings.Contains(inventory[0].Title, "gpt-4o") {
		t.Fatalf("title missing model: %q", inventory[0].Title)
	}
	if !strings.Contains(inventory[0].Title, "code_interpreter") {
		t.Fatalf("title missing tool: %q", inventory[0].Title)
	}
}

func TestGatherAssistants_403Degrades(t *testing.T) {
	doer := &asstDoer{t: t, unavailable: map[string]bool{"/v1/assistants": true}}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherAssistants(context.Background(), sink); err != nil {
		t.Fatalf("gatherAssistants must not fail on 403; got %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 1 || fs[0].SubjectKind != subjectSurface {
		t.Fatalf("want 1 surface-unavailable finding, got %+v", fs)
	}
}

func TestGatherAssistants_BetaHeader(t *testing.T) {
	doer := &asstDoer{t: t}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherAssistants(context.Background(), sink); err != nil {
		t.Fatalf("gatherAssistants: %v", err)
	}
	var found bool
	for _, r := range doer.reqs {
		if r.URL.Path == "/v1/assistants" {
			found = true
			if r.Header.Get("OpenAI-Beta") != "assistants=v2" {
				t.Fatalf("missing OpenAI-Beta header, got %q", r.Header.Get("OpenAI-Beta"))
			}
		}
	}
	if !found {
		t.Fatal("no request to /v1/assistants")
	}
}

// ---------- Files inventory ----------

func TestGatherFiles_Success(t *testing.T) {
	doer := &asstDoer{t: t}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherFiles(context.Background(), sink); err != nil {
		t.Fatalf("gatherFiles: %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 2 {
		t.Fatalf("emitted %d findings, want 2", len(fs))
	}
	if fs[0].SubjectKind != subjectFile {
		t.Fatalf("subject kind = %q", fs[0].SubjectKind)
	}
	if !strings.Contains(fs[0].Title, "quarterly_report.pdf") {
		t.Fatalf("title = %q", fs[0].Title)
	}
	if !strings.Contains(fs[0].Title, "purpose=assistants") {
		t.Fatalf("title missing purpose: %q", fs[0].Title)
	}
}

func TestGatherFiles_403Degrades(t *testing.T) {
	doer := &asstDoer{t: t, unavailable: map[string]bool{"/v1/files": true}}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherFiles(context.Background(), sink); err != nil {
		t.Fatalf("gatherFiles must not fail on 403; got %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 1 || fs[0].SubjectKind != subjectSurface {
		t.Fatalf("want 1 surface-unavailable finding, got %+v", fs)
	}
}

// ---------- Vector Stores inventory ----------

func TestGatherVectorStores_Success(t *testing.T) {
	doer := &asstDoer{t: t}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherVectorStores(context.Background(), sink); err != nil {
		t.Fatalf("gatherVectorStores: %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 2 {
		t.Fatalf("emitted %d findings, want 2", len(fs))
	}
	kb := fs[0]
	if kb.SubjectKind != subjectVectorStore || kb.SubjectRef != "vs_abc123" {
		t.Fatalf("first store = %+v", kb)
	}
	if !strings.Contains(kb.Title, "files=10") {
		t.Fatalf("title missing file count: %q", kb.Title)
	}
	if kb.Severity != model.SeverityInfo {
		t.Fatalf("healthy store severity = %q, want info", kb.Severity)
	}
	// Second store has 2 failed files — severity should be Low.
	staging := fs[1]
	if staging.Severity != model.SeverityLow {
		t.Fatalf("failed-files store severity = %q, want low", staging.Severity)
	}
	if !strings.Contains(staging.Title, "failed=2") {
		t.Fatalf("title missing failed count: %q", staging.Title)
	}
}

func TestGatherVectorStores_403Degrades(t *testing.T) {
	doer := &asstDoer{t: t, unavailable: map[string]bool{"/v1/vector_stores": true}}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherVectorStores(context.Background(), sink); err != nil {
		t.Fatalf("gatherVectorStores must not fail on 403; got %v", err)
	}
	fs := govFindings(sink.obs)
	if len(fs) != 1 || fs[0].SubjectKind != subjectSurface {
		t.Fatalf("want 1 surface-unavailable finding, got %+v", fs)
	}
}

func TestGatherVectorStores_BetaHeader(t *testing.T) {
	doer := &asstDoer{t: t}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherVectorStores(context.Background(), sink); err != nil {
		t.Fatalf("gatherVectorStores: %v", err)
	}
	for _, r := range doer.reqs {
		if r.URL.Path == "/v1/vector_stores" {
			if r.Header.Get("OpenAI-Beta") != "assistants=v2" {
				t.Fatalf("vector stores request missing OpenAI-Beta header")
			}
		}
	}
}

// ---------- Policy enforcement ----------

func TestAssistantsPolicy_ModelViolation(t *testing.T) {
	doer := &asstDoer{t: t}
	s := newAsstSource(t, doer, map[string]string{
		"assistants_allowed_models": "gpt-4o,gpt-4o-mini",
	})
	sink := &captureSink{}
	if err := s.gatherAssistants(context.Background(), sink); err != nil {
		t.Fatalf("gatherAssistants: %v", err)
	}
	fs := govFindings(sink.obs)
	var violations []model.FindingReport
	for _, f := range fs {
		if f.Kind == "policy_violation" {
			violations = append(violations, f)
		}
	}
	// The fixture has 3 assistants: gpt-4o (allowed), gpt-4o-mini (allowed), o3-pro (blocked).
	if len(violations) != 1 {
		t.Fatalf("got %d violations, want 1", len(violations))
	}
	v := violations[0]
	if v.SubjectRef != "asst_ghi789" {
		t.Fatalf("violation ref = %q, want asst_ghi789", v.SubjectRef)
	}
	if v.Severity != model.SeverityHigh {
		t.Fatalf("violation severity = %q, want high", v.Severity)
	}
	if !strings.Contains(v.Title, "o3-pro") {
		t.Fatalf("violation title = %q", v.Title)
	}
}

func TestAssistantsPolicy_ToolViolation(t *testing.T) {
	doer := &asstDoer{t: t}
	s := newAsstSource(t, doer, map[string]string{
		"assistants_allowed_tools": "file_search",
	})
	sink := &captureSink{}
	if err := s.gatherAssistants(context.Background(), sink); err != nil {
		t.Fatalf("gatherAssistants: %v", err)
	}
	fs := govFindings(sink.obs)
	var violations []model.FindingReport
	for _, f := range fs {
		if f.Kind == "policy_violation" {
			violations = append(violations, f)
		}
	}
	// asst_abc123 has code_interpreter+file_search → 1 violation (code_interpreter)
	// asst_def456 has file_search → 0 violations
	// asst_ghi789 has code_interpreter+function → 2 violations
	if len(violations) != 3 {
		t.Fatalf("got %d tool violations, want 3", len(violations))
	}
	for _, v := range violations {
		if v.Severity != model.SeverityHigh {
			t.Fatalf("tool violation severity = %q, want high", v.Severity)
		}
	}
}

func TestAssistantsPolicy_NoPolicyNoViolations(t *testing.T) {
	doer := &asstDoer{t: t}
	// No policy fields set → no enforcement.
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherAssistants(context.Background(), sink); err != nil {
		t.Fatalf("gatherAssistants: %v", err)
	}
	for _, o := range sink.obs {
		if f, ok := o.(model.FindingReport); ok && f.Kind == "policy_violation" {
			t.Fatalf("unexpected violation with no policy: %+v", f)
		}
	}
}

// ---------- Descriptor fields ----------

func TestDescriptor_AssistantsFields(t *testing.T) {
	d := New().Descriptor()
	want := map[string]bool{"assistants": false, "admin": false, "assistants_allowed_models": false, "assistants_allowed_tools": false}
	for _, f := range d.ConfigFields {
		if _, ok := want[f.Key]; ok {
			want[f.Key] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Fatalf("Descriptor missing config field %q", k)
		}
	}
}

// ---------- Read-only constraint ----------

func TestAssistants_AllGETRequests(t *testing.T) {
	doer := &asstDoer{t: t}
	s := newAsstSource(t, doer, nil)
	sink := &captureSink{}
	if err := s.gatherAssistants(context.Background(), sink); err != nil {
		t.Fatalf("gatherAssistants: %v", err)
	}
	if err := s.gatherFiles(context.Background(), sink); err != nil {
		t.Fatalf("gatherFiles: %v", err)
	}
	if err := s.gatherVectorStores(context.Background(), sink); err != nil {
		t.Fatalf("gatherVectorStores: %v", err)
	}
	for _, r := range doer.reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("non-GET request: %s %s", r.Method, r.URL.Path)
		}
	}
}
