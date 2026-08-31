// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Tests cover Envoy AI Gateway config posture: unauthenticated backend, MCP
// passthrough posture, model-access drift, FinOps blind spot, edges — plus the
// no-secret-leak guarantee and the YAML/JSON dual decode. Air-gapped against
// recorded fixtures. CRD shapes verified against aigateway.envoyproxy.io (v1.0).
package envoyaigw

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

func (s *captureSink) findings() []model.FindingReport {
	var out []model.FindingReport
	for _, o := range s.obs {
		if f, ok := o.(model.FindingReport); ok {
			out = append(out, f)
		}
	}
	return out
}

func (s *captureSink) edges() []model.EdgeObservation {
	var out []model.EdgeObservation
	for _, o := range s.obs {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
		}
	}
	return out
}

func configDir(t *testing.T, fixtures ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range fixtures {
		_, file, _, _ := runtime.Caller(0)
		data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func gather(t *testing.T, cfg map[string]string) *captureSink {
	t.Helper()
	s := New()
	s.now = func() time.Time { return time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink
}

func findSubject(fs []model.FindingReport, kind, ref string) (model.FindingReport, bool) {
	for _, f := range fs {
		if f.SubjectKind == kind && f.SubjectRef == ref {
			return f, true
		}
	}
	return model.FindingReport{}, false
}

func TestUnauthenticatedBackend(t *testing.T) {
	dir := configDir(t, "config.json")
	sink := gather(t, map[string]string{"config_path": dir})

	f, ok := findSubject(sink.findings(), subjectBackend, "anthropic-backend")
	if !ok || f.Severity != model.SeverityHigh {
		t.Fatalf("expected High unauthenticated anthropic-backend, got %+v ok=%v", f, ok)
	}
	// The secured backend must NOT be flagged.
	if _, ok := findSubject(sink.findings(), subjectBackend, "openai-backend"); ok {
		t.Fatal("secured openai-backend wrongly flagged unauthenticated")
	}
}

func TestMCPPosture(t *testing.T) {
	dir := configDir(t, "config.json")
	fs := gather(t, map[string]string{"config_path": dir}).findings()

	if f, ok := findSubject(fs, subjectMCP, "tools-mcp/no-security-policy"); !ok || f.Severity != model.SeverityHigh {
		t.Fatalf("expected High MCP no-security-policy, got %+v ok=%v", f, ok)
	}
	if f, ok := findSubject(fs, subjectMCP, "tools-mcp/no-tool-selector"); !ok || f.Severity != model.SeverityMedium {
		t.Fatalf("expected Medium MCP no-tool-selector, got %+v ok=%v", f, ok)
	}
}

func TestModelDrift(t *testing.T) {
	dir := configDir(t, "config.json")
	fs := gather(t, map[string]string{"config_path": dir, "approved_models": "gpt-4o"}).findings()

	if f, ok := findSubject(fs, subjectModelPol, "drift/claude-sonnet-4"); !ok || f.Severity != model.SeverityHigh {
		t.Fatalf("expected High model drift for claude-sonnet-4, got %+v ok=%v", f, ok)
	}
	// gpt-4o is approved — no drift.
	if _, ok := findSubject(fs, subjectModelPol, "drift/gpt-4o"); ok {
		t.Fatal("approved model gpt-4o wrongly flagged as drift")
	}
	// With NO policy configured, no drift findings at all.
	clean := gather(t, map[string]string{"config_path": dir}).findings()
	for _, f := range clean {
		if f.SubjectKind == subjectModelPol {
			t.Fatalf("model drift emitted with no policy: %q", f.SubjectRef)
		}
	}
}

func TestFinOpsBlindSpot(t *testing.T) {
	dir := configDir(t, "config.json")
	fs := gather(t, map[string]string{"config_path": dir}).findings()

	// cheap-route has neither llmRequestCosts nor a QuotaPolicy.
	if f, ok := findSubject(fs, subjectFinOps, "cheap-route/no-cost-or-quota"); !ok || f.Severity != model.SeverityLow {
		t.Fatalf("expected Low FinOps blind spot for cheap-route, got %+v ok=%v", f, ok)
	}
	// chat-route has llmRequestCosts (and a QuotaPolicy) — not flagged.
	if _, ok := findSubject(fs, subjectFinOps, "chat-route/no-cost-or-quota"); ok {
		t.Fatal("chat-route wrongly flagged as a FinOps blind spot")
	}
}

func TestEdges(t *testing.T) {
	dir := configDir(t, "config.json")
	edges := gather(t, map[string]string{"config_path": dir}).edges()
	assertEdge(t, edges, "chat-route", resourceBackend, "openai-backend")
	assertEdge(t, edges, "chat-route", resourceBackend, "anthropic-backend")
}

func TestYAMLAccepted(t *testing.T) {
	// A YAML multi-document export decodes through the same path as JSON.
	yaml := "" +
		"apiVersion: aigateway.envoyproxy.io/v1alpha1\n" +
		"kind: AIServiceBackend\n" +
		"metadata:\n  name: yaml-backend\n  namespace: prod\n" +
		"spec:\n  schema:\n    name: OpenAI\n" +
		"---\n" +
		"apiVersion: aigateway.envoyproxy.io/v1alpha1\n" +
		"kind: AIGatewayRoute\n" +
		"metadata:\n  name: yaml-route\n  namespace: prod\n" +
		"spec:\n  rules:\n  - backendRefs:\n    - name: yaml-backend\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cfg.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := gather(t, map[string]string{"config_path": dir})
	// yaml-backend has no BackendSecurityPolicy -> High, proving the YAML decoded.
	if _, ok := findSubject(sink.findings(), subjectBackend, "yaml-backend"); !ok {
		t.Fatal("YAML export was not decoded (no finding for yaml-backend)")
	}
	assertEdge(t, sink.edges(), "yaml-route", resourceBackend, "yaml-backend")
}

func TestNoSecretLeak(t *testing.T) {
	// The BackendSecurityPolicy fixture embeds a secretRef name; it must NEVER appear
	// in any observation (the connector reads only the policy TYPE, not key material).
	dir := configDir(t, "config.json")
	sink := gather(t, map[string]string{"config_path": dir})
	blob, err := json.Marshal(sink.obs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "SECRETVALUE") {
		t.Fatal("a secret value leaked into an observation")
	}
}

func TestOfflineNoConfig(t *testing.T) {
	sink := gather(t, map[string]string{})
	if len(sink.obs) != 0 {
		t.Fatalf("offline emitted %d observations, want 0", len(sink.obs))
	}
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Version != "0.1.0" {
		t.Fatalf("descriptor = %q %q", d.Name, d.Version)
	}
	var keys []string
	for _, f := range d.ConfigFields {
		keys = append(keys, f.Key)
	}
	for _, want := range []string{"config_path", "approved_models"} {
		if !contains(keys, want) {
			t.Fatalf("descriptor fields %v missing %s", keys, want)
		}
	}
}

func assertEdge(t *testing.T, edges []model.EdgeObservation, origin, kind, ref string) {
	t.Helper()
	for _, e := range edges {
		if e.OriginRef == origin && e.ResourceKind == kind && e.ResourceRef == ref {
			return
		}
	}
	t.Fatalf("missing edge origin=%s kind=%s ref=%s", origin, kind, ref)
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
