// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource || len(d.ConfigFields) == 0 {
		t.Errorf("descriptor = %+v", d)
	}
}

func TestOpenNoServersFails(t *testing.T) {
	if err := New().Open(t.Context(), sdk.Config{}); err == nil {
		t.Error("Open with no servers must fail")
	}
}

func inlineServers(t *testing.T, specs ...serverSpec) sdk.Config {
	t.Helper()
	raw, err := json.Marshal(specs)
	if err != nil {
		t.Fatal(err)
	}
	return sdk.Config{Settings: map[string]string{cfgServers: string(raw)}}
}

// withLegacyMode returns a copy of cfg with next_revision_preview explicitly
// disabled, so tests exercising the legacy 2025-11-25 Initialize path are not
// broken by the default-ON change.
func withLegacyMode(cfg sdk.Config) sdk.Config {
	settings := make(map[string]string, len(cfg.Settings)+1)
	for k, v := range cfg.Settings {
		settings[k] = v
	}
	settings[cfgNextRevisionPreview] = "false"
	return sdk.Config{Settings: settings}
}

func TestGatherEmitsCapabilityEdges(t *testing.T) {
	s := New()
	// Use legacy mode: the helper stdio server speaks 2025-11-25 Initialize, not the
	// 2026-07-28 stateless path. Explicitly disable next_revision_preview (default ON
	// since) so introspectOne uses the legacy path for this test.
	if err := s.Open(t.Context(), withLegacyMode(inlineServers(t, helperSpec("helper")))); err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &fakeSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	edges := sink.edges()
	// 2 tools + 1 resource + 1 template + 1 prompt.
	if len(edges) != 5 {
		t.Fatalf("want 5 capability edges, got %d (%+v)", len(edges), edges)
	}
	// AIP-01: a healthy introspection surfaces the negotiated protocol revision per
	// server. The helper advertises the prior revision (2025-06-18), so there is a Low
	// "stale revision" flag naming the current baseline — proving the connector
	// advertises 2026-07-28 AND records/flags the server's revision. AIP-10: a
	// healthy, CLEAN helper also gets exactly one posture-SCORE summary (grade A, no
	// issues) and no posture issue findings.
	var rev, posture []model.FindingReport
	for _, f := range sink.findings() {
		switch f.Kind {
		case findingRevision:
			rev = append(rev, f)
		case findingPosture:
			posture = append(posture, f)
		default:
			t.Errorf("unexpected finding kind for a clean server: %+v", f)
		}
	}
	if len(rev) != 1 || rev[0].Severity != model.SeverityLow {
		t.Fatalf("want exactly one Low mcp_revision finding, got %+v", rev)
	}
	if !strings.Contains(rev[0].Title, revision20250618) || !strings.Contains(rev[0].Title, currentRevision) {
		t.Errorf("revision finding should name the stale (%s) and current (%s) revisions: %q", revision20250618, currentRevision, rev[0].Title)
	}
	if len(posture) != 1 || posture[0].Severity != model.SeverityInfo || !strings.Contains(posture[0].Title, "grade A") {
		t.Fatalf("want exactly one clean (grade A, Info) posture summary, got %+v", posture)
	}
}

func TestGatherFailedServerEmitsFinding(t *testing.T) {
	s := New()
	bad := serverSpec{Name: "broken", Transport: transportStdio, Command: "/nonexistent/mcp-server-binary"}
	if err := s.Open(t.Context(), inlineServers(t, bad)); err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &fakeSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather should not fail on a bad server: %v", err)
	}
	if len(sink.edges()) != 0 {
		t.Errorf("a broken server should produce no edges")
	}
	f := sink.findings()
	if len(f) != 1 || f[0].Kind != "health" || f[0].SubjectRef != "broken" {
		t.Errorf("expected one health finding, got %+v", f)
	}
	// The error detail (which can carry a path/host) is hashed, never embedded.
	blob := fmt.Sprintf("%+v", f[0])
	if strings.Contains(blob, "/nonexistent/mcp-server-binary") {
		t.Errorf("failure finding leaked the command path: %q", blob)
	}
	if len(f[0].DetailHash) != 64 {
		t.Errorf("DetailHash is not a SHA-256 hex: %q", f[0].DetailHash)
	}
}

func TestTransportForErrors(t *testing.T) {
	// A spec with a URL-style transport but no URL, and one with neither command
	// nor URL, cannot determine a transport.
	if _, err := transportFor(t.Context(), serverSpec{Name: "x", Transport: transportSSE}); err == nil {
		t.Error("sse transport with no url must error")
	}
	if _, err := transportFor(t.Context(), serverSpec{Name: "y"}); err == nil {
		t.Error("a spec with neither command nor url must error")
	}
}

func TestGatherHonorsCanceledContext(t *testing.T) {
	s := New()
	if err := s.Open(t.Context(), inlineServers(t, helperSpec("helper"))); err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Gather(ctx, &fakeSink{}); err == nil {
		t.Error("Gather with a canceled context should return its error")
	}
}
