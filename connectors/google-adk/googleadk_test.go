// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Tests cover the ADK 2.0 exported-session governance: agent inventory, tool-policy
// drift, execution errors, access-map edges and Vertex correlation — all air-gapped
// against recorded fixtures (no network, no live ADK deployment). Session shape
// verified against google.github.io/adk-docs (2.0).
package googleadk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/textscan"
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

// sessionDirWith copies the named testdata fixtures into a fresh temp dir and
// returns it, so each test controls exactly which exports are present.
func sessionDirWith(t *testing.T, fixtures ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range fixtures {
		data, err := os.ReadFile(testdataPath(name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func testdataPath(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", name)
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

func findWithSubject(fs []model.FindingReport, kind, ref string) (model.FindingReport, bool) {
	for _, f := range fs {
		if f.SubjectKind == kind && f.SubjectRef == ref {
			return f, true
		}
	}
	return model.FindingReport{}, false
}

func TestAgentInventory(t *testing.T) {
	dir := sessionDirWith(t, "session-research.json")
	sink := gather(t, map[string]string{"session_dir": dir})

	inv, ok := findWithSubject(sink.findings(), subjectInventory, "research-assistant")
	if !ok {
		t.Fatal("no inventory finding for research-assistant")
	}
	for _, want := range []string{"agents=2", "users=1", "sessions=1", "events=3", "tools=2", "transfers=1", "state_writes=1", "errors=1", "escalations=1"} {
		if !strings.Contains(inv.Title, want) {
			t.Fatalf("inventory title %q missing %q", inv.Title, want)
		}
	}
}

func TestUnapprovedToolDrift(t *testing.T) {
	dir := sessionDirWith(t, "session-research.json")
	sink := gather(t, map[string]string{"session_dir": dir, "approved_tools": "google_search"})

	drift, ok := findWithSubject(sink.findings(), subjectPolicy, "research-assistant/unapproved-tools")
	if !ok || drift.Severity != model.SeverityHigh {
		t.Fatalf("expected High unapproved-tools drift, got %+v ok=%v", drift, ok)
	}
	// With no tool policy, no drift finding is emitted.
	clean := gather(t, map[string]string{"session_dir": dir})
	if _, ok := findWithSubject(clean.findings(), subjectPolicy, "research-assistant/unapproved-tools"); ok {
		t.Fatal("drift finding emitted with no tool policy configured")
	}
}

func TestExecutionErrorsAndEdges(t *testing.T) {
	dir := sessionDirWith(t, "session-research.json")
	sink := gather(t, map[string]string{"session_dir": dir})

	if _, ok := findWithSubject(sink.findings(), subjectExecution, "research-assistant/errors"); !ok {
		t.Fatal("no execution-errors finding")
	}
	edges := sink.edges()
	assertEdge(t, edges, "research-assistant", resourceTool, "google_search")
	assertEdge(t, edges, "research-assistant", resourceTool, "shell_exec")
	assertEdge(t, edges, "research-assistant", resourceAgent, "researcher") // transfer edge
}

func TestVertexCorrelation(t *testing.T) {
	dir := sessionDirWith(t, "session-research.json")
	sink := gather(t, map[string]string{
		"session_dir":              dir,
		"vertex_reasoning_engines": "research-assistant=projects/p/locations/us/reasoningEngines/123",
	})
	if _, ok := findWithSubject(sink.findings(), subjectInventory, "research-assistant/vertex"); !ok {
		t.Fatal("no Vertex correlation finding")
	}
}

func TestArrayFileMultiSession(t *testing.T) {
	dir := sessionDirWith(t, "sessions-support.json")
	sink := gather(t, map[string]string{"session_dir": dir})
	inv, ok := findWithSubject(sink.findings(), subjectInventory, "support-bot")
	if !ok {
		t.Fatal("no inventory for support-bot (array file)")
	}
	// Two sessions, three events, two distinct tools (lookup_order, issue_refund).
	for _, want := range []string{"sessions=2", "events=3", "tools=2", "users=2"} {
		if !strings.Contains(inv.Title, want) {
			t.Fatalf("support-bot inventory %q missing %q", inv.Title, want)
		}
	}
}

func TestOfflineNoSessionDir(t *testing.T) {
	sink := gather(t, map[string]string{})
	if len(sink.obs) != 0 {
		t.Fatalf("offline emitted %d observations, want 0", len(sink.obs))
	}
}

func TestMalformedExportSkipped(t *testing.T) {
	dir := sessionDirWith(t, "session-research.json")
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := gather(t, map[string]string{"session_dir": dir})
	// The good export is still processed despite the broken sibling.
	if _, ok := findWithSubject(sink.findings(), subjectInventory, "research-assistant"); !ok {
		t.Fatal("a malformed sibling aborted the scan")
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
	for _, want := range []string{"session_dir", "approved_tools", "vertex_reasoning_engines"} {
		if !contains(keys, want) {
			t.Fatalf("descriptor fields %v missing %s", keys, want)
		}
	}
}

// zwsp / rlo are the zero-width-space and right-to-left-override runes an attacker
// can smuggle through a fully attacker-controlled app_name (kept as \u escapes so
// the source file itself stays ASCII-clean and reviewable).
const (
	zwsp = "\u200b"
	rlo  = "\u202e"
)

func TestEdgeOriginSanitized(t *testing.T) {
	// app_name is fully attacker-controlled; a Trojan-Source / zero-width payload must
	// be sanitized on EVERY observation, including edge OriginRef — not just findings.
	rawApp := rlo + "evil" + zwsp + "agent"
	sess := `{"id":"s","app_name":"` + rawApp + `","user_id":"u",` +
		`"events":[{"id":"e1","author":"` + rawApp + `","content":{"role":"model","parts":[{"function_call":{"name":"shell_exec"}}]},` +
		`"actions":{"transfer_to_agent":"worker"}}]}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s.json"), []byte(sess), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := gather(t, map[string]string{"session_dir": dir})

	want := textscan.SanitizeDisplay(rawApp)
	if want == rawApp {
		t.Fatal("test precondition: SanitizeDisplay left the hostile app_name unchanged")
	}
	edges := sink.edges()
	if len(edges) == 0 {
		t.Fatal("no edges emitted")
	}
	for _, e := range edges {
		if e.OriginRef != want {
			t.Fatalf("edge OriginRef = %q, want sanitized %q (raw must not pass through)", e.OriginRef, want)
		}
		if strings.ContainsAny(e.OriginRef, rlo+zwsp) {
			t.Fatalf("edge OriginRef %q still carries raw bidi/zero-width runes", e.OriginRef)
		}
	}
}

func TestCardinalityCapTruncates(t *testing.T) {
	// One app with more distinct tools than the per-dimension cap: enumeration stops
	// at the cap AND a visible truncation finding is emitted (the ceiling is never silent).
	var b strings.Builder
	b.WriteString(`{"id":"s","app_name":"big","user_id":"u","events":[`)
	n := maxDistinctKeys + 50
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"e%d","author":"big","content":{"role":"model","parts":[{"function_call":{"name":"tool_%d"}}]}}`, i, i)
	}
	b.WriteString(`]}`)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.json"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := gather(t, map[string]string{"session_dir": dir})

	if _, ok := findWithSubject(sink.findings(), subjectInventory, "big/truncated"); !ok {
		t.Fatal("no per-app truncation finding despite exceeding the cardinality cap")
	}
	toolEdges := 0
	for _, e := range sink.edges() {
		if e.ResourceKind == resourceTool {
			toolEdges++
		}
	}
	if toolEdges != maxDistinctKeys {
		t.Fatalf("emitted %d tool edges, want the cap %d", toolEdges, maxDistinctKeys)
	}
}

func TestAppsCapTruncates(t *testing.T) {
	// More distinct app_names than the app cap: a scan-level truncation finding is
	// raised and the number of enumerated apps never exceeds the cap.
	var b strings.Builder
	b.WriteByte('[')
	n := maxDistinctApps + 40
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"s%d","app_name":"app_%d","user_id":"u"}`, i, i)
	}
	b.WriteByte(']')
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "many.json"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := gather(t, map[string]string{"session_dir": dir})

	if _, ok := findWithSubject(sink.findings(), subjectInventory, "(scan)/apps-truncated"); !ok {
		t.Fatal("no scan-level apps-truncated finding despite exceeding the app cap")
	}
	inv := 0
	for _, f := range sink.findings() {
		if f.Kind == "inventory" && f.SubjectKind == subjectInventory {
			inv++
		}
	}
	if inv != maxDistinctApps {
		t.Fatalf("enumerated %d apps, want exactly the cap %d", inv, maxDistinctApps)
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
