// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func openGitLabWebhook(t *testing.T) *Source {
	t.Helper()
	s := New()
	if err := s.Open(context.Background(), validConfig()); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *collectSink) samples() []model.MetricSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.MetricSample
	for _, o := range s.obs {
		if m, ok := o.(model.MetricSample); ok {
			out = append(out, m)
		}
	}
	return out
}

func postGitLabEvent(t *testing.T, event, token string, payload string) (*collectSink, int) {
	t.Helper()
	src := openGitLabWebhook(t)
	sink := &collectSink{}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload))
	req.Header.Set("X-Gitlab-Token", token)
	req.Header.Set("X-Gitlab-Event", event)
	w := httptest.NewRecorder()
	src.handleWebhook(sink)(w, req)
	return sink, w.Code
}

func assertEvidence(t *testing.T, m model.MetricSample, repo, head, name, status, conclusion, event string) {
	t.Helper()
	if m.Name != "git.host.evidence" {
		t.Errorf("metric name = %q", m.Name)
	}
	if m.SubjectRef != repo {
		t.Errorf("subject = %q", m.SubjectRef)
	}
	got := map[string]string{
		"repository": m.Dimensions["repository"],
		"head_sha":   m.Dimensions["head_sha"],
		"name":       m.Dimensions["name"],
		"status":     m.Dimensions["status"],
		"conclusion": m.Dimensions["conclusion"],
		"event":      m.Dimensions["event"],
	}
	want := map[string]string{
		"repository": repo,
		"head_sha":   head,
		"name":       name,
		"status":     status,
		"conclusion": conclusion,
		"event":      event,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("dimension %s = %q, want %q", k, got[k], v)
		}
	}
}

func TestGitHostEvidencePipeline(t *testing.T) {
	payload := `{"object_kind":"pipeline","object_attributes":{"ref":"main","sha":"abc123","status":"success"},"project":{"path_with_namespace":"mygroup/web"}}`
	sink, code := postGitLabEvent(t, "Pipeline Hook", "whsec-test-secret", payload)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(sink.edges()) != 0 {
		t.Fatal("pipeline evidence must not emit an access edge")
	}
	samples := sink.samples()
	if len(samples) != 1 {
		t.Fatalf("samples = %d", len(samples))
	}
	assertEvidence(t, samples[0], "mygroup/web", "abc123", "pipeline", "success", "success", "pipeline")
}

func TestGitHostEvidenceJob(t *testing.T) {
	payload := `{"object_kind":"build","ref":"main","sha":"def456","build_name":"rspec","build_status":"running","project":{"path_with_namespace":"mygroup/web"}}`
	sink, code := postGitLabEvent(t, "Job Hook", "whsec-test-secret", payload)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	samples := sink.samples()
	if len(samples) != 1 {
		t.Fatalf("samples = %d", len(samples))
	}
	assertEvidence(t, samples[0], "mygroup/web", "def456", "rspec", "running", "", "job")
}

func TestGitHostEvidenceBadSignature(t *testing.T) {
	payload := `{"object_kind":"pipeline","object_attributes":{"sha":"abc123","status":"success"},"project":{"path_with_namespace":"mygroup/web"}}`
	sink, code := postGitLabEvent(t, "Pipeline Hook", "wrong-token", payload)
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", code)
	}
	if len(sink.samples()) != 0 || len(sink.edges()) != 0 {
		t.Fatal("bad token emitted an observation")
	}
}

func TestGitHostEvidenceUnhandledEvent(t *testing.T) {
	sink, code := postGitLabEvent(t, "Wiki Page Hook", "whsec-test-secret", `{"object_kind":"wiki_page"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(sink.samples()) != 0 || len(sink.edges()) != 0 {
		t.Fatal("unhandled event emitted an observation")
	}
}
