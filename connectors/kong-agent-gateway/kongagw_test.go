// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Tests cover Kong AI Gateway config posture: uncapped AI proxy, ungoverned MCP,
// model-access drift, disabled plugin, edges — plus the decK-YAML and Admin-API-JSON
// decode paths (incl. a ref serialized as an object) and the no-secret-leak
// guarantee. Air-gapped against fixtures. Field names verified against
// developer.konghq.com (Kong AI Gateway 3.14).
package kongagw

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
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

func TestUncappedProxyAndUngovernedMCP(t *testing.T) {
	dir := configDir(t, "kong.yaml")
	fs := gather(t, map[string]string{"config_path": dir}).findings()

	if f, ok := findSubject(fs, subjectRateLimit, "route:chat-route/no-rate-limit"); !ok || f.Severity != model.SeverityMedium {
		t.Fatalf("expected Medium no-rate-limit on chat-route, got %+v ok=%v", f, ok)
	}
	if f, ok := findSubject(fs, subjectMCP, "route:mcp-route/ungoverned-mcp"); !ok || f.Severity != model.SeverityMedium {
		t.Fatalf("expected Medium ungoverned-mcp on mcp-route, got %+v ok=%v", f, ok)
	}
}

func TestDisabledPlugin(t *testing.T) {
	dir := configDir(t, "kong.yaml")
	fs := gather(t, map[string]string{"config_path": dir}).findings()
	if f, ok := findSubject(fs, subjectPlugin, "service:mcp-svc/disabled/ai-sanitizer"); !ok || f.Severity != model.SeverityLow {
		t.Fatalf("expected Low disabled ai-sanitizer, got %+v ok=%v", f, ok)
	}
}

func TestModelDriftAndEdges(t *testing.T) {
	dir := configDir(t, "kong.yaml")
	sink := gather(t, map[string]string{"config_path": dir, "approved_models": "gpt-4o"})
	fs := sink.findings()

	if f, ok := findSubject(fs, subjectModelPol, "drift/route:chat-route/anthropic/claude-sonnet-4"); !ok || f.Severity != model.SeverityHigh {
		t.Fatalf("expected High drift for claude-sonnet-4, got %+v ok=%v", f, ok)
	}
	if _, ok := findSubject(fs, subjectModelPol, "drift/route:chat-route/openai/gpt-4o"); ok {
		t.Fatal("approved model gpt-4o wrongly flagged as drift")
	}
	assertEdge(t, sink.edges(), "route chat-route", resourceModel, "openai/gpt-4o")
	assertEdge(t, sink.edges(), "route chat-route", resourceModel, "anthropic/claude-sonnet-4")

	// No policy configured => no drift findings.
	clean := gather(t, map[string]string{"config_path": dir}).findings()
	for _, f := range clean {
		if f.SubjectKind == subjectModelPol {
			t.Fatalf("model drift emitted with no policy: %q", f.SubjectRef)
		}
	}
}

func TestAdminJSONAndRefObject(t *testing.T) {
	dir := configDir(t, "kong-admin.json")
	sink := gather(t, map[string]string{"config_path": dir})
	// Plugin references its route by {"id":"r1"} object -> scope "route:r1".
	if _, ok := findSubject(sink.findings(), subjectRateLimit, "route:r1/no-rate-limit"); !ok {
		t.Fatal("Admin-API JSON with an object route ref was not decoded to route:r1")
	}
}

func TestNoSecretLeak(t *testing.T) {
	dir := configDir(t, "kong.yaml", "kong-admin.json")
	sink := gather(t, map[string]string{"config_path": dir})
	blob, err := json.Marshal(sink.obs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "SECRETVALUE") {
		t.Fatal("a secret value leaked into an observation")
	}
	// Broader than a fixed literal: no secret-shaped value survives anywhere.
	if redact.ContainsSecret(string(blob)) {
		t.Fatal("a secret-shaped value reached an observation")
	}
}

// zwsp / rlo are the zero-width-space and right-to-left-override runes an attacker can
// smuggle through a fully attacker-controlled route/service name (\u escapes so the
// source stays ASCII-clean).
const (
	zwsp = "\u200b"
	rlo  = "\u202e"
)

func TestScopeRefSanitized(t *testing.T) {
	// The scope key feeds SubjectRef; a route name is fully attacker-controlled, so a
	// Trojan-Source / zero-width payload must be sanitized on the SubjectRef too, not
	// only the Title (the edge-leak lesson).
	rawRoute := rlo + "evil" + zwsp + "route"
	yaml := "" +
		"services:\n  - name: s\n    routes:\n      - name: \"" + rawRoute + "\"\n" +
		"        plugins:\n          - name: ai-proxy\n            config:\n              model:\n                name: gpt-4o\n                provider: openai\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hostile.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := gather(t, map[string]string{"config_path": dir}).findings()
	found := false
	for _, f := range fs {
		if f.SubjectKind == subjectRateLimit && strings.HasSuffix(f.SubjectRef, "/no-rate-limit") {
			found = true
			if strings.ContainsAny(f.SubjectRef, rlo+zwsp) {
				t.Fatalf("SubjectRef %q still carries raw bidi/zero-width runes", f.SubjectRef)
			}
		}
	}
	if !found {
		t.Fatal("no no-rate-limit finding for the hostile-named route")
	}
}

func TestMultiDocYAML(t *testing.T) {
	// A concatenated multi-document decK export must decode every document, not just
	// the first.
	yaml := "" +
		"services:\n  - name: a\n    routes:\n      - name: ra\n        plugins:\n          - name: ai-proxy\n            config: {model: {name: gpt-4o, provider: openai}}\n" +
		"---\n" +
		"services:\n  - name: b\n    routes:\n      - name: rb\n        plugins:\n          - name: ai-proxy\n            config: {model: {name: gpt-4o, provider: openai}}\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "multi.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := gather(t, map[string]string{"config_path": dir}).findings()
	// Both routes (from doc 1 and doc 2) must produce a no-rate-limit finding.
	if _, ok := findSubject(fs, subjectRateLimit, "route:ra/no-rate-limit"); !ok {
		t.Fatal("route ra (doc 1) missing — multi-doc not decoded")
	}
	if _, ok := findSubject(fs, subjectRateLimit, "route:rb/no-rate-limit"); !ok {
		t.Fatal("route rb (doc 2) missing — only the first YAML document was decoded")
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
