// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// TestLifecycleStateFor pins the data-driven state evaluator: deprecated vs
// retired around the 2026-06-15 boundary, the Sonnet-4 Vertex divergence, the
// deny-closed "hit with no date = deprecated" rule, and the prefix-collision cases
// (a current id must never inherit an older generation's schedule).
func TestLifecycleStateFor(t *testing.T) {
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		id      string
		surface model.Gateway
		now     time.Time
		want    LifecycleState
	}{
		// Prefix collisions: current ids yield NO lifecycle hit…
		{"claude-opus-4-8", model.GatewayDirect, day(2026, 6, 10), LifecycleActive},
		{"claude-sonnet-4-6", model.GatewayDirect, day(2026, 6, 10), LifecycleActive},
		{"claude-haiku-4-5", model.GatewayDirect, day(2026, 6, 10), LifecycleActive},
		{"claude-sonnet-4-5", model.GatewayDirect, day(2026, 6, 10), LifecycleActive}, // carve-out (not on the page)
		{"claude-fable-5", model.GatewayDirect, day(2026, 6, 10), LifecycleActive},
		{"claude-mythos-5", model.GatewayDirect, day(2026, 6, 10), LifecycleActive},
		{"gpt-4o", model.GatewayDirect, day(2026, 6, 10), LifecycleActive},
		// …while the dated/aliased deprecated ids DO hit.
		{"claude-opus-4-20250514", model.GatewayDirect, day(2026, 6, 14), LifecycleDeprecated},
		{"claude-opus-4-20250514", model.GatewayDirect, day(2026, 6, 15), LifecycleRetired}, // boundary: retired ON the date
		{"claude-opus-4-20250514", model.GatewayDirect, day(2026, 6, 16), LifecycleRetired},
		{"claude-opus-4-0", model.GatewayDirect, day(2026, 6, 14), LifecycleDeprecated},
		{"claude-opus-4-1-20250805", model.GatewayDirect, day(2026, 6, 10), LifecycleDeprecated}, // retires 2026-08-05
		{"claude-opus-4-1-20250805", model.GatewayDirect, day(2026, 8, 5), LifecycleRetired},
		// Vertex divergence: Sonnet 4 is retired first-party but still deprecated on
		// Vertex until 2026-09-14.
		{"claude-sonnet-4-20250514", model.GatewayDirect, day(2026, 7, 1), LifecycleRetired},
		{"claude-sonnet-4-20250514", model.GatewayVertex, day(2026, 7, 1), LifecycleDeprecated},
		{"claude-sonnet-4-20250514", model.GatewayVertex, day(2026, 9, 14), LifecycleRetired},
		// Deny-closed: a hit with NO date for the queried surface is deprecated, never
		// a guessed retired (Bedrock's Sonnet-4 date is unpublished).
		{"claude-sonnet-4-20250514", model.GatewayBedrockMantle, day(2027, 1, 1), LifecycleDeprecated},
		// Retired generation on the Anthropic-operated surfaces.
		{"claude-3-5-sonnet-20241022", model.GatewayDirect, day(2026, 6, 10), LifecycleRetired},
		{"claude-3-5-sonnet-20240620", model.GatewayClaudePlatformAWS, day(2026, 6, 10), LifecycleRetired},
		{"claude-3-haiku-20240307", model.GatewayDirect, day(2026, 4, 19), LifecycleDeprecated},
		{"claude-3-haiku-20240307", model.GatewayDirect, day(2026, 4, 20), LifecycleRetired},
		// Announced with no published retirement date: deprecated, never retired.
		{"claude-mythos-preview", model.GatewayDirect, day(2027, 1, 1), LifecycleDeprecated},
		{"claude-2.1", model.GatewayDirect, day(2027, 1, 1), LifecycleDeprecated}, // dateless (to-confirm) — deny-closed
	}
	for _, c := range cases {
		got, _ := LifecycleStateFor(c.id, c.surface, c.now)
		if got != c.want {
			t.Errorf("LifecycleStateFor(%q, %s, %s) = %s, want %s", c.id, c.surface, c.now.Format("2006-01-02"), got, c.want)
		}
	}
}

// TestRetirementsFor_CarriesDeprecationAndReplacement proves the schedule now carries
// the published deprecation date and recommended replacement (model-deprecations.md),
// and that an announced-but-unscheduled retirement keeps an empty date.
func TestRetirementsFor_CarriesDeprecationAndReplacement(t *testing.T) {
	rs := RetirementsFor("claude-opus-4-20250514")
	if len(rs) == 0 {
		t.Fatal("claude-opus-4-20250514 has no schedule")
	}
	for _, r := range rs {
		if r.DeprecatedOn != "2026-04-14" || r.RetiresOn != "2026-06-15" || r.ReplacementRef != "claude-opus-4-8" {
			t.Errorf("opus-4 schedule on %s = dep %q ret %q repl %q, want 2026-04-14/2026-06-15/claude-opus-4-8",
				r.Surface, r.DeprecatedOn, r.RetiresOn, r.ReplacementRef)
		}
		if r.AsOf != lifecycleAsOf {
			t.Errorf("opus-4 schedule on %s missing AsOf stamp", r.Surface)
		}
	}
	// Mythos preview: retirement announced ("after claude-mythos-5 becomes
	// available"), date unpublished — entries carry an EMPTY RetiresOn, never a guess.
	prev := RetirementsFor("claude-mythos-preview")
	if len(prev) == 0 {
		t.Fatal("claude-mythos-preview has no schedule")
	}
	for _, r := range prev {
		if r.RetiresOn != "" || r.DeprecatedOn != "2026-06-09" || r.ReplacementRef != "claude-mythos-5" {
			t.Errorf("mythos-preview schedule on %s = dep %q ret %q repl %q, want 2026-06-09/<empty>/claude-mythos-5",
				r.Surface, r.DeprecatedOn, r.RetiresOn, r.ReplacementRef)
		}
	}
	// No lifecycle hit for the current generation (buildModel attaches nil).
	for _, id := range []string{"claude-fable-5", "claude-mythos-5", "claude-opus-4-8"} {
		if m := buildModel(id, id); m.Retirements != nil {
			t.Errorf("%s carries a lifecycle schedule: %+v", id, m.Retirements)
		}
	}
}

// TestRejectsSamplingParams_FableMythos pins the Fable 5 / Mythos 5 extension
// (launch page 2026-06-09: sampling params rejected like Opus 4.7+) and that the
// unverified mythos-preview stays false (fail-closed).
func TestRejectsSamplingParams_FableMythos(t *testing.T) {
	cases := map[string]bool{
		"claude-fable-5":        true,
		"claude-mythos-5":       true,
		"claude-mythos-preview": false,
		"claude-haiku-4-5":      false,
		"claude-opus-4-8":       true, // unchanged Opus 4.7+ floor
	}
	for id, want := range cases {
		if got := RejectsSamplingParams(id); got != want {
			t.Errorf("RejectsSamplingParams(%q) = %v, want %v", id, got, want)
		}
	}
}

// lifecycleFixtureDoer serves the lifecycle usage fixture for the usage pull and
// falls back to the shared fixtures for every other path.
type lifecycleFixtureDoer struct{ fixtureDoer }

func (d *lifecycleFixtureDoer) Do(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/v1/organizations/usage_report/messages" {
		d.reqs = append(d.reqs, req)
		body, err := os.ReadFile(filepath.Join("testdata", "usage_report_lifecycle.json"))
		if err != nil {
			d.t.Fatalf("read fixture usage_report_lifecycle.json: %v", err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	}
	return d.fixtureDoer.Do(req)
}

// TestGather_DeprecatedModelInUseFindings proves the lifecycle check over the
// ingest: a usage report naming claude-opus-4-20250514 (deprecated, retires
// 2026-06-15 — within 30 days of the fixed 2026-06-02 clock → High),
// claude-3-5-sonnet-20241022 (retired 2025-10-28 → Critical) and claude-fable-5
// (active → no finding) yields exactly TWO deprecated_model_in_use findings, in
// sorted-model order, with the schedule tuple as a stable hash.
func TestGather_DeprecatedModelInUseFindings(t *testing.T) {
	doer := &lifecycleFixtureDoer{fixtureDoer{t: t}}
	s := New()
	s.doer = doer
	s.now = fixedClock // 2026-06-02
	// cost_report off: this test isolates the usage-pull model set.
	cfg := sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test", "cost_report": "false"}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	var lifecycle []model.FindingReport
	for _, o := range sink.obs {
		if f, ok := o.(model.FindingReport); ok && f.Kind == "deprecated_model_in_use" {
			lifecycle = append(lifecycle, f)
		}
	}
	if len(lifecycle) != 2 {
		t.Fatalf("deprecated_model_in_use findings = %d, want 2 (%+v)", len(lifecycle), lifecycle)
	}

	// Sorted model order: claude-3-5-sonnet-20241022 before claude-opus-4-20250514.
	retired, deprecated := lifecycle[0], lifecycle[1]
	if retired.SubjectRef != "claude-3-5-sonnet-20241022" || deprecated.SubjectRef != "claude-opus-4-20250514" {
		t.Fatalf("finding order = %q, %q (want sorted model ids)", retired.SubjectRef, deprecated.SubjectRef)
	}

	if retired.Severity != model.SeverityCritical {
		t.Errorf("retired severity = %s, want critical", retired.Severity)
	}
	if retired.SubjectKind != subjectModelLifecycle {
		t.Errorf("retired subject kind = %q, want %q", retired.SubjectKind, subjectModelLifecycle)
	}
	wantRetiredTitle := "Retired model still in use: claude-3-5-sonnet-20241022 retired 2025-10-28 on direct (replacement: claude-sonnet-4-6)"
	if retired.Title != wantRetiredTitle {
		t.Errorf("retired title = %q, want %q", retired.Title, wantRetiredTitle)
	}

	// 2026-06-15 is within 30 days of the 2026-06-02 clock → High, not Medium.
	if deprecated.Severity != model.SeverityHigh {
		t.Errorf("deprecated severity = %s, want high (retirement within 30 days)", deprecated.Severity)
	}
	wantDepTitle := "Deprecated model in use: claude-opus-4-20250514 retires 2026-06-15 on direct (replacement: claude-opus-4-8)"
	if deprecated.Title != wantDepTitle {
		t.Errorf("deprecated title = %q, want %q", deprecated.Title, wantDepTitle)
	}

	// The detail hash is the stable schedule tuple — reproducible, never raw detail.
	wantHash := redact.Hash("claude-opus-4-20250514|direct|2026-04-14|2026-06-15|claude-opus-4-8|deprecated")
	if deprecated.DetailHash != wantHash {
		t.Errorf("deprecated detail hash = %q, want stable tuple hash %q", deprecated.DetailHash, wantHash)
	}
	if !deprecated.OccurredAt.Equal(fixedClock().UTC()) {
		t.Errorf("deprecated OccurredAt = %v, want the connector clock", deprecated.OccurredAt)
	}

	// claude-fable-5 (active) must NOT produce a lifecycle finding.
	for _, f := range lifecycle {
		if f.SubjectRef == "claude-fable-5" {
			t.Error("active claude-fable-5 produced a lifecycle finding")
		}
	}
}
