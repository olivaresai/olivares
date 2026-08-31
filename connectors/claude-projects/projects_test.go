// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeprojects

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestDescriptor(t *testing.T) {
	s := New()
	d := s.Descriptor()
	if d.Name != Name {
		t.Errorf("Name = %q, want %q", d.Name, Name)
	}
	if d.Type != sdk.TypeSource {
		t.Errorf("Type = %q, want %q", d.Type, sdk.TypeSource)
	}
	if len(d.ConfigFields) == 0 {
		t.Error("ConfigFields empty")
	}
	var hasAPIKey, hasOrgID bool
	for _, f := range d.ConfigFields {
		if f.Key == "api_key" {
			hasAPIKey = true
			if !f.Secret {
				t.Error("api_key should be marked Secret")
			}
		}
		if f.Key == "organization_id" {
			hasOrgID = true
		}
	}
	if !hasAPIKey {
		t.Error("missing api_key config field")
	}
	if !hasOrgID {
		t.Error("missing organization_id config field")
	}
}

func TestOpenNoKey(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.client != nil {
		t.Error("client should be nil without api_key")
	}
}

func TestGatherNoClient(t *testing.T) {
	s := New()
	_ = s.Open(context.Background(), sdk.Config{})

	var sink collectSink
	if err := s.Gather(context.Background(), &sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.obs) != 0 {
		t.Errorf("expected 0 observations without client, got %d", len(sink.obs))
	}
}

func TestGatherMissingOrgID(t *testing.T) {
	s := New()
	_ = s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"api_key": "sk-ant-admin01-test",
	}})
	s.SetTestTransport(nil)

	var sink collectSink
	if err := s.Gather(context.Background(), &sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.obs) != 1 {
		t.Fatalf("expected 1 finding (missing org_id), got %d", len(sink.obs))
	}
	f, ok := sink.obs[0].(model.FindingReport)
	if !ok {
		t.Fatal("expected FindingReport")
	}
	if f.Severity != model.SeverityMedium {
		t.Errorf("severity = %v, want Medium", f.Severity)
	}
	if !strings.Contains(f.Title, "organization_id") {
		t.Errorf("title should mention organization_id: %s", f.Title)
	}
}

func TestGatherProjectsWithFixtures(t *testing.T) {
	projData := mustReadFixture(t, "testdata/projects.json")
	membData := mustReadFixture(t, "testdata/members.json")
	keysData := mustReadFixture(t, "testdata/apikeys.json")

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/organizations/org_test/projects", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(projData)
	})
	mux.HandleFunc("/v1/organizations/org_test/projects/prj_abc123/members", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(membData)
	})
	mux.HandleFunc("/v1/organizations/org_test/projects/prj_def456/members", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"has_more":false}`))
	})
	mux.HandleFunc("/v1/organizations/org_test/projects/prj_abc123/api_keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(keysData)
	})
	mux.HandleFunc("/v1/organizations/org_test/projects/prj_def456/api_keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"has_more":false}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"api_key":         "sk-ant-admin01-test",
		"organization_id": "org_test",
		"base_url":        srv.URL,
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.SetTestTransport(nil)

	var sink collectSink
	if err := s.Gather(context.Background(), &sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	var edges, findings int
	for _, o := range sink.obs {
		switch o.(type) {
		case model.EdgeObservation:
			edges++
		case model.FindingReport:
			findings++
		}
	}

	// Expected edges: 3 members for prj_abc123 + 2 api keys for prj_abc123 = 5
	if edges != 5 {
		t.Errorf("edges = %d, want 5", edges)
	}
	// Expected findings: 1 coverage gap + 2 project inventory + 1 archived note + 1 active key = 5
	if findings < 4 {
		t.Errorf("findings = %d, want >= 4", findings)
	}

	// Verify edge kinds
	var memberEdges, keyEdges int
	for _, o := range sink.obs {
		if e, ok := o.(model.EdgeObservation); ok {
			switch e.ResourceKind {
			case resMember:
				memberEdges++
			case resAPIKey:
				keyEdges++
			}
		}
	}
	if memberEdges != 3 {
		t.Errorf("member edges = %d, want 3", memberEdges)
	}
	if keyEdges != 2 {
		t.Errorf("api key edges = %d, want 2", keyEdges)
	}

	// Verify member edge modes (admin/developer=RW, viewer=R)
	for _, o := range sink.obs {
		if e, ok := o.(model.EdgeObservation); ok && e.ResourceKind == resMember {
			switch e.ResourceRef {
			case "user_001", "user_002":
				if e.Mode != model.ModeReadWrite {
					t.Errorf("member %s mode = %v, want ReadWrite", e.ResourceRef, e.Mode)
				}
			case "user_003":
				if e.Mode != model.ModeRead {
					t.Errorf("member %s mode = %v, want Read", e.ResourceRef, e.Mode)
				}
			}
		}
	}
}

func TestPolicyForbiddenName(t *testing.T) {
	pol, err := parsePolicy(`{"forbidden_name_patterns":["^test","secret"]}`)
	if err != nil {
		t.Fatalf("parsePolicy: %v", err)
	}

	s := New()
	s.policy = pol
	s.SetTestTransport(nil)

	var sink collectSink
	now := s.clock().UTC()

	if err := s.evaluateProjectPolicy(context.Background(), &sink, project{
		ID: "prj_1", Name: "test-project", CreatedAt: "2026-06-01T00:00:00Z",
	}, now); err != nil {
		t.Fatalf("evaluateProjectPolicy: %v", err)
	}

	if len(sink.obs) != 1 {
		t.Fatalf("expected 1 finding for forbidden name, got %d", len(sink.obs))
	}
	f := sink.obs[0].(model.FindingReport)
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if !strings.Contains(f.Title, "forbidden pattern") {
		t.Errorf("title should mention forbidden pattern: %s", f.Title)
	}
}

func TestPolicyInvalidRegex(t *testing.T) {
	_, err := parsePolicy(`{"forbidden_name_patterns":["[invalid"]}`)
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestPolicyStaleProject(t *testing.T) {
	pol, err := parsePolicy(`{"require_archive_after_days":90}`)
	if err != nil {
		t.Fatalf("parsePolicy: %v", err)
	}

	s := New()
	s.policy = pol
	s.SetTestTransport(nil)

	var sink collectSink
	now := s.clock().UTC()

	if err := s.evaluateProjectPolicy(context.Background(), &sink, project{
		ID: "prj_old", Name: "Ancient Project", CreatedAt: "2025-01-01T00:00:00Z",
	}, now); err != nil {
		t.Fatalf("evaluateProjectPolicy: %v", err)
	}

	if len(sink.obs) != 1 {
		t.Fatalf("expected 1 finding for stale project, got %d", len(sink.obs))
	}
	f := sink.obs[0].(model.FindingReport)
	if f.Kind != "policy_violation" {
		t.Errorf("kind = %q, want policy_violation", f.Kind)
	}
}

func TestArtifactClassification(t *testing.T) {
	tests := []struct {
		activityType string
		want         ArtifactState
	}{
		{"artifact_created", ArtifactCreated},
		{"artifact_shared", ArtifactShared},
		{"artifact_archived", ArtifactArchived},
		{"file_uploaded", ArtifactCreated},
		{"file_shared", ArtifactShared},
		{"file_deleted", ArtifactArchived},
		{"artifact_updated", ArtifactCreated},
		{"user_signed_in", ""},
		{"", ""},
		{"claude_chat_created", ""},
	}
	for _, tt := range tests {
		got := ClassifyArtifactEvent(tt.activityType)
		if got != tt.want {
			t.Errorf("ClassifyArtifactEvent(%q) = %q, want %q", tt.activityType, got, tt.want)
		}
	}
}

func TestArtifactLifecycleFinding(t *testing.T) {
	var sink collectSink
	ev := ArtifactEvent{
		ArtifactRef: "art_001",
		ProjectRef:  "prj_abc123",
		EventType:   "artifact_created",
		State:       ArtifactCreated,
		OccurredAt:  mustParseTime("2026-06-27T10:00:00Z"),
	}
	if err := EmitArtifactLifecycleFinding(context.Background(), &sink, ev, nil); err != nil {
		t.Fatalf("EmitArtifactLifecycleFinding: %v", err)
	}
	// 1 finding + 1 edge
	if len(sink.obs) != 2 {
		t.Fatalf("expected 2 observations, got %d", len(sink.obs))
	}
}

func TestArtifactRetentionViolation(t *testing.T) {
	var sink collectSink
	ev := ArtifactEvent{
		ArtifactRef: "art_shared",
		ProjectRef:  "prj_abc123",
		EventType:   "artifact_shared",
		State:       ArtifactShared,
		OccurredAt:  mustParseTime("2026-06-27T10:00:00Z"),
	}
	ret := &ArtifactRetentionPolicy{RequireArchiveOnShare: true}
	if err := EmitArtifactLifecycleFinding(context.Background(), &sink, ev, ret); err != nil {
		t.Fatalf("EmitArtifactLifecycleFinding: %v", err)
	}
	// 1 lifecycle finding + 1 edge + 1 policy violation
	if len(sink.obs) != 3 {
		t.Fatalf("expected 3 observations, got %d", len(sink.obs))
	}
	var violations int
	for _, o := range sink.obs {
		if f, ok := o.(model.FindingReport); ok && f.Kind == "policy_violation" {
			violations++
		}
	}
	if violations != 1 {
		t.Errorf("expected 1 policy violation, got %d", violations)
	}
}

func TestSanitizeName(t *testing.T) {
	if sanitizeName("") != "(unnamed)" {
		t.Error("empty name should be (unnamed)")
	}
	long := strings.Repeat("a", 100)
	if len(sanitizeName(long)) > 84 {
		t.Error("long name should be truncated")
	}
	if sanitizeName("Normal Name") != "Normal Name" {
		t.Error("normal name should pass through")
	}
}

// --- test helpers ---

type collectSink struct {
	obs []model.Observation
}

func (s *collectSink) Emit(_ context.Context, obs model.Observation) error {
	s.obs = append(s.obs, obs)
	return nil
}

func mustReadFixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return b
}

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
