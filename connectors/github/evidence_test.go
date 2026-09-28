// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func openGitHubWebhook(t *testing.T) *Source {
	t.Helper()
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: validConfig()}); err != nil {
		t.Fatal(err)
	}
	return s
}

func (c *collectSink) samples() []model.MetricSample {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []model.MetricSample
	for _, o := range c.obs {
		if m, ok := o.(model.MetricSample); ok {
			out = append(out, m)
		}
	}
	return out
}

func postGitHubEvent(t *testing.T, event string, payload []byte, sig string) (*collectSink, int) {
	t.Helper()
	s := openGitHubWebhook(t)
	sink := &collectSink{}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", event)
	w := httptest.NewRecorder()
	s.handleWebhook(sink)(w, req)
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

func TestGitHostEvidenceCheckRun(t *testing.T) {
	payload := []byte(`{"action":"completed","check_run":{"name":"ci","status":"completed","conclusion":"success","head_sha":"abc123"},"repository":{"full_name":"acme-corp/web-app"}}`)
	sink, code := postGitHubEvent(t, "check_run", payload, signPayload(payload, "test-secret"))
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(sink.edges()) != 0 {
		t.Fatal("check evidence must not emit an access edge")
	}
	samples := sink.samples()
	if len(samples) != 1 {
		t.Fatalf("samples = %d", len(samples))
	}
	assertEvidence(t, samples[0], "acme-corp/web-app", "abc123", "ci", "completed", "success", "check_run")
}

func TestGitHostEvidenceCheckSuite(t *testing.T) {
	payload := []byte(`{"action":"completed","check_suite":{"status":"completed","conclusion":"failure","head_sha":"def456","app":{"name":"GitHub Actions"}},"repository":{"full_name":"acme-corp/web-app"}}`)
	sink, code := postGitHubEvent(t, "check_suite", payload, signPayload(payload, "test-secret"))
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	samples := sink.samples()
	if len(samples) != 1 {
		t.Fatalf("samples = %d", len(samples))
	}
	assertEvidence(t, samples[0], "acme-corp/web-app", "def456", "GitHub Actions", "completed", "failure", "check_suite")
}

func TestGitHostEvidenceWorkflowRun(t *testing.T) {
	payload := []byte(`{"action":"completed","workflow_run":{"name":"CI","status":"completed","conclusion":"cancelled","head_sha":"fff789"},"repository":{"full_name":"acme-corp/web-app"}}`)
	sink, code := postGitHubEvent(t, "workflow_run", payload, signPayload(payload, "test-secret"))
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	samples := sink.samples()
	if len(samples) != 1 {
		t.Fatalf("samples = %d", len(samples))
	}
	assertEvidence(t, samples[0], "acme-corp/web-app", "fff789", "CI", "completed", "cancelled", "workflow_run")
}

func TestGitHostEvidenceBadSignature(t *testing.T) {
	payload := []byte(`{"action":"completed","check_run":{"name":"ci","status":"completed","conclusion":"success","head_sha":"abc123"},"repository":{"full_name":"acme-corp/web-app"}}`)
	sink, code := postGitHubEvent(t, "check_run", payload, "sha256=deadbeef")
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", code)
	}
	if len(sink.samples()) != 0 || len(sink.edges()) != 0 {
		t.Fatal("bad signature emitted an observation")
	}
}

func TestGitHostEvidenceUnhandledEvent(t *testing.T) {
	payload := []byte(`{"action":"created"}`)
	sink, code := postGitHubEvent(t, "star", payload, signPayload(payload, "test-secret"))
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(sink.samples()) != 0 || len(sink.edges()) != 0 {
		t.Fatal("unhandled event emitted an observation")
	}
}
