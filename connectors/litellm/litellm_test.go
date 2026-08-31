// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Tests cover LiteLLM governance: identity-correlation edges, unbounded budget,
// budget drift vs the declared budget, model-access drift, unattributed and blocked
// keys, the no-CostSample (no double-count) guarantee, and no-secret-leak.
// Air-gapped against fixtures. Fields verified against docs.litellm.ai.
package litellm

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

func exportDir(t *testing.T, fixtures ...string) string {
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

func TestIdentityEdges(t *testing.T) {
	dir := exportDir(t, "export.json")
	edges := gather(t, map[string]string{"path": dir}).edges()
	assertEdge(t, edges, "alice", resourceKey, "alice-key")
}

func TestNoBudgetCap(t *testing.T) {
	dir := exportDir(t, "export.json")
	fs := gather(t, map[string]string{"path": dir}).findings()
	if f, ok := findSubject(fs, subjectBudget, "key/nobudget-key/no-budget"); !ok || f.Severity != model.SeverityMedium {
		t.Fatalf("expected Medium no-budget for nobudget-key, got %+v ok=%v", f, ok)
	}
	// A key WITH a budget is not flagged.
	if _, ok := findSubject(fs, subjectBudget, "key/alice-key/no-budget"); ok {
		t.Fatal("alice-key (budgeted) wrongly flagged no-budget")
	}
}

func TestUnattributedKey(t *testing.T) {
	dir := exportDir(t, "export.json")
	fs := gather(t, map[string]string{"path": dir}).findings()
	// The orphan key has only a token; its id is tok:<hash>.
	found := false
	for _, f := range fs {
		if f.SubjectKind == subjectKey && strings.HasPrefix(f.SubjectRef, "tok:") && strings.HasSuffix(f.SubjectRef, "/unattributed") {
			found = true
		}
	}
	if !found {
		t.Fatal("no unattributed finding for the orphan token-only key")
	}
}

func TestBlockedRetained(t *testing.T) {
	dir := exportDir(t, "export.json")
	fs := gather(t, map[string]string{"path": dir}).findings()
	if f, ok := findSubject(fs, subjectKey, "blocked-key/blocked"); !ok || f.Severity != model.SeverityInfo {
		t.Fatalf("expected Info blocked-key, got %+v ok=%v", f, ok)
	}
}

func TestBudgetDrift(t *testing.T) {
	dir := exportDir(t, "export.json")
	fs := gather(t, map[string]string{"path": dir, "declared_budgets": "alice-key=200"}).findings()
	if f, ok := findSubject(fs, subjectBudget, "drift/key/alice-key"); !ok || f.Severity != model.SeverityHigh {
		t.Fatalf("expected High budget drift for alice-key, got %+v ok=%v", f, ok)
	}
	if !strings.Contains(mustFind(t, fs, subjectBudget, "drift/key/alice-key").Title, "100") {
		t.Fatal("budget drift title should carry the LiteLLM value 100")
	}
	// With no declared budget, no drift.
	clean := gather(t, map[string]string{"path": dir}).findings()
	for _, f := range clean {
		if f.SubjectKind == subjectBudget && strings.HasPrefix(f.SubjectRef, "drift/") {
			t.Fatalf("budget drift emitted with no declared_budgets: %q", f.SubjectRef)
		}
	}
}

func TestModelDrift(t *testing.T) {
	dir := exportDir(t, "export.json")
	fs := gather(t, map[string]string{"path": dir, "approved_models": "gpt-4o"}).findings()
	// alice-key reaches claude-sonnet-4, outside the allowlist.
	if f, ok := findSubject(fs, subjectModelPol, "drift/key/alice-key/claude-sonnet-4"); !ok || f.Severity != model.SeverityHigh {
		t.Fatalf("expected High model drift for claude-sonnet-4, got %+v ok=%v", f, ok)
	}
	// blocked-key has an empty models list -> all-models access under a policy.
	if f, ok := findSubject(fs, subjectModelPol, "key/blocked-key/all-models"); !ok || f.Severity != model.SeverityHigh {
		t.Fatalf("expected High all-models for blocked-key, got %+v ok=%v", f, ok)
	}
}

func TestPartialDecodeToleratesBadElement(t *testing.T) {
	// One key with a type-mismatched field must skip only that element, never discard
	// the whole file (which would fail-open to zero data, indistinguishable from offline).
	blob := `{"keys":[
		{"key_alias":"good1","user_id":"u1","max_budget":10.0,"models":["gpt-4o"]},
		{"key_alias":"bad","max_budget":"unlimited"},
		{"key_alias":"good2","user_id":"u2","max_budget":20.0,"models":["gpt-4o"]}
	]}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keys.json"), []byte(blob), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := gather(t, map[string]string{"path": dir})
	// Both well-formed keys still produce their identity edges despite the bad sibling.
	assertEdge(t, sink.edges(), "u1", resourceKey, "good1")
	assertEdge(t, sink.edges(), "u2", resourceKey, "good2")
}

func TestNoCostSampleEmitted(t *testing.T) {
	dir := exportDir(t, "export.json")
	sink := gather(t, map[string]string{"path": dir})
	for _, o := range sink.obs {
		if _, ok := o.(model.CostSample); ok {
			t.Fatal("litellm emitted a CostSample — spend must never be double-counted as cost")
		}
	}
}

func TestNoSecretLeak(t *testing.T) {
	dir := exportDir(t, "export.json")
	sink := gather(t, map[string]string{"path": dir})
	blob, err := json.Marshal(sink.obs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "SECRETVALUE") {
		t.Fatal("a raw virtual-key secret leaked into an observation")
	}
}

func TestOffline(t *testing.T) {
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
	for _, want := range []string{"path", "approved_models", "declared_budgets"} {
		if !contains(keys, want) {
			t.Fatalf("descriptor fields %v missing %s", keys, want)
		}
	}
}

func mustFind(t *testing.T, fs []model.FindingReport, kind, ref string) model.FindingReport {
	t.Helper()
	f, ok := findSubject(fs, kind, ref)
	if !ok {
		t.Fatalf("missing finding %s/%s", kind, ref)
	}
	return f
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
