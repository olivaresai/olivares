// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// TestStandardContextWindow pins the verified standard windows per family (primary
// docs, 2026-06-15/2026-07-03): current Opus 4.6/4.7/4.8, Sonnet 4.6 and Sonnet 5
// are 1M; Opus 4.5 and Sonnet 4.5 (pre-1M-GA) are 200K; Haiku 4.5 is 200K;
// Fable/Mythos 5 are 1M. An unlisted id has no declared standard (falls back to the
// coarse family window).
func TestStandardContextWindow(t *testing.T) {
	cases := []struct {
		id      string
		want    int64
		wantOK  bool
		comment string
	}{
		{"claude-opus-4-8", 1_000_000, true, "current Opus = 1M"},
		{"claude-opus-4-8-20260601", 1_000_000, true, "dated id matches the prefix"},
		{"claude-opus-4-7", 1_000_000, true, ""},
		{"claude-opus-4-6", 1_000_000, true, ""},
		{"claude-opus-4-5", 200_000, true, "Opus 4.5 predates the 1M GA"},
		{"claude-sonnet-5", 1_000_000, true, ""},
		{"claude-sonnet-4-6", 1_000_000, true, ""},
		{"claude-sonnet-4-5", 200_000, true, ""},
		{"claude-haiku-4-5", 200_000, true, "Haiku 4.5 = 200K"},
		{"claude-fable-5", 1_000_000, true, ""},
		{"claude-mythos-5", 1_000_000, true, "shares Fable 5 specs"},
		{"claude-mythos-preview", 0, false, "preview window unverified — no entry"},
		{"claude-opus-4-1", 0, false, "deprecated, no overlay entry (coarse family floor applies)"},
		{"gpt-4o", 0, false, "non-Claude — no entry"},
		{"", 0, false, ""},
	}
	for _, c := range cases {
		got, ok := StandardContextWindow(c.id)
		if ok != c.wantOK || got != c.want {
			t.Errorf("StandardContextWindow(%q) = %d,%v want %d,%v %s", c.id, got, ok, c.want, c.wantOK, c.comment)
		}
	}
}

// TestSurfaceContextWindow pins the headline per-surface divergence: Opus 4.8 is 1M on
// every surface EXCEPT Microsoft Foundry, where it is 200K. Opus 4.7/4.6, Sonnet 5,
// Sonnet 4.6, Fable 5 and Mythos 5 are 1M everywhere incl. Foundry; Haiku 4.5 is
// 200K everywhere.
func TestSurfaceContextWindow(t *testing.T) {
	cases := []struct {
		surface model.Gateway
		id      string
		want    int64
		wantOK  bool
	}{
		// Opus 4.8: 1M standard, 200K only on Foundry.
		{model.GatewayDirect, "claude-opus-4-8", 1_000_000, true},
		{model.GatewayClaudePlatformAWS, "claude-opus-4-8", 1_000_000, true},
		{model.GatewayBedrockMantle, "claude-opus-4-8", 1_000_000, true},
		{model.GatewayBedrockLegacy, "claude-opus-4-8", 1_000_000, true},
		{model.GatewayVertex, "claude-opus-4-8", 1_000_000, true},
		{model.GatewayFoundry, "claude-opus-4-8", 200_000, true}, // the divergence
		// Older Opus / current Sonnet / Fable are full 1M on Foundry.
		{model.GatewayFoundry, "claude-opus-4-7", 1_000_000, true},
		{model.GatewayFoundry, "claude-opus-4-6", 1_000_000, true},
		{model.GatewayDirect, "claude-sonnet-5", 1_000_000, true},
		{model.GatewayFoundry, "claude-sonnet-5", 1_000_000, true},
		{model.GatewayFoundry, "claude-sonnet-4-6", 1_000_000, true},
		{model.GatewayFoundry, "claude-fable-5", 1_000_000, true},
		{model.GatewayFoundry, "claude-mythos-5", 1_000_000, true},
		// 200K-native models: no divergence (200K on every surface incl. Foundry).
		{model.GatewayFoundry, "claude-haiku-4-5", 200_000, true},
		{model.GatewayDirect, "claude-haiku-4-5", 200_000, true},
		{model.GatewayFoundry, "claude-sonnet-4-5", 200_000, true},
		// Unknown model: honest unknown.
		{model.GatewayFoundry, "claude-mythos-preview", 0, false},
		{model.GatewayDirect, "gpt-4o", 0, false},
	}
	for _, c := range cases {
		got, ok := SurfaceContextWindow(c.surface, c.id)
		if ok != c.wantOK || got != c.want {
			t.Errorf("SurfaceContextWindow(%s,%q) = %d,%v want %d,%v", c.surface, c.id, got, ok, c.want, c.wantOK)
		}
	}
}

// TestCheckContextWindowForSurface is the router/governance check: a 1M-token Opus 4.8
// request is DENIED/flagged on Foundry (Exceeds) but PERMITTED on the Claude API — the
// exact "1M against Foundry = denied/flagged, against API = allowed" the session asks
// for. It also pins Capped (structural) vs Exceeds (per-request), and the honest
// unknown-model behavior.
func TestCheckContextWindowForSurface(t *testing.T) {
	// Headline: a 1M request to Opus 4.8.
	onFoundry := CheckContextWindowForSurface(model.GatewayFoundry, "claude-opus-4-8", 1_000_000)
	if !onFoundry.Known || !onFoundry.Exceeds || !onFoundry.Capped {
		t.Fatalf("1M opus-4-8 on Foundry: want known+exceeds+capped, got %+v", onFoundry)
	}
	if onFoundry.Effective != 200_000 || onFoundry.Standard != 1_000_000 {
		t.Errorf("Foundry verdict effective/standard = %d/%d, want 200K/1M", onFoundry.Effective, onFoundry.Standard)
	}

	onAPI := CheckContextWindowForSurface(model.GatewayDirect, "claude-opus-4-8", 1_000_000)
	if !onAPI.Known || onAPI.Exceeds || onAPI.Capped {
		t.Fatalf("1M opus-4-8 on Claude API: want known, NOT exceeds, NOT capped, got %+v", onAPI)
	}
	if onAPI.Effective != 1_000_000 {
		t.Errorf("API verdict effective = %d, want 1M", onAPI.Effective)
	}

	// A 300K request: exceeds Foundry's 200K cap, fits the API's 1M.
	if v := CheckContextWindowForSurface(model.GatewayFoundry, "claude-opus-4-8", 300_000); !v.Exceeds {
		t.Errorf("300K opus-4-8 on Foundry should exceed 200K cap, got %+v", v)
	}
	if v := CheckContextWindowForSurface(model.GatewayDirect, "claude-opus-4-8", 300_000); v.Exceeds {
		t.Errorf("300K opus-4-8 on Claude API should fit 1M, got %+v", v)
	}

	// Opus 4.7 on Foundry is full 1M: a 1M request fits, no cap.
	if v := CheckContextWindowForSurface(model.GatewayFoundry, "claude-opus-4-7", 1_000_000); v.Exceeds || v.Capped {
		t.Errorf("1M opus-4-7 on Foundry should fit (1M there), got %+v", v)
	}

	// requestedTokens<=0 evaluates only the structural divergence (Capped), not Exceeds.
	if v := CheckContextWindowForSurface(model.GatewayFoundry, "claude-opus-4-8", 0); v.Exceeds || !v.Capped {
		t.Errorf("opus-4-8 on Foundry with no request: want capped, NOT exceeds, got %+v", v)
	}

	// Unknown model: honest unknown — never asserts an exceed.
	if v := CheckContextWindowForSurface(model.GatewayFoundry, "some-local-llama", 5_000_000); v.Known || v.Exceeds {
		t.Errorf("unknown model: want not-known, not-exceeds, got %+v", v)
	}
}

// TestSurfaceContextVerdictFinding checks the request-time High finding: emitted only
// when the request exceeds the surface window, with the correct kind/severity.
func TestSurfaceContextVerdictFinding(t *testing.T) {
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

	exceeds := CheckContextWindowForSurface(model.GatewayFoundry, "claude-opus-4-8", 1_000_000)
	f, ok := exceeds.Finding(now)
	if !ok {
		t.Fatal("an exceeding verdict must produce a finding")
	}
	if f.Kind != findingSurfaceCapabilityDivergence {
		t.Errorf("finding kind = %q, want %q", f.Kind, findingSurfaceCapabilityDivergence)
	}
	if f.Severity != model.SeverityHigh {
		t.Errorf("request-time finding severity = %v, want High", f.Severity)
	}
	if f.SubjectRef != "claude-opus-4-8" {
		t.Errorf("subject ref = %q, want the model id", f.SubjectRef)
	}

	fits := CheckContextWindowForSurface(model.GatewayDirect, "claude-opus-4-8", 1_000_000)
	if _, ok := fits.Finding(now); ok {
		t.Error("a fitting verdict must NOT produce a finding")
	}
}

// TestSurfaceCapabilityDivergenceFinding checks the gather-time posture finding: a
// Foundry-configured connector emits one Medium surface_capability_divergence finding
// naming Opus 4.8; a Claude-API-configured connector emits none (it caps nothing).
func TestSurfaceCapabilityDivergenceFinding(t *testing.T) {
	at := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

	foundry := &Source{gateway: model.GatewayFoundry}
	f, ok := foundry.surfaceCapabilityDivergenceFinding(at)
	if !ok {
		t.Fatal("Foundry caps Opus 4.8 — a divergence finding must be emitted")
	}
	if f.Kind != findingSurfaceCapabilityDivergence || f.Severity != model.SeverityMedium {
		t.Errorf("posture finding kind/sev = %q/%v, want %q/Medium", f.Kind, f.Severity, findingSurfaceCapabilityDivergence)
	}
	if f.SubjectKind != subjectSurfaceCapability || f.SubjectRef != string(model.GatewayFoundry) {
		t.Errorf("posture finding subject = %q/%q, want %q/foundry", f.SubjectKind, f.SubjectRef, subjectSurfaceCapability)
	}
	if !contains(f.Title, "claude-opus-4-8") || !contains(f.Title, "200K") || !contains(f.Title, "1M") {
		t.Errorf("posture finding title omits the divergence detail: %q", f.Title)
	}

	// Every non-Foundry surface caps nothing today → no finding.
	for _, g := range []model.Gateway{
		model.GatewayDirect, model.GatewayClaudePlatformAWS, model.GatewayBedrockMantle,
		model.GatewayBedrockLegacy, model.GatewayVertex,
	} {
		s := &Source{gateway: g}
		if _, ok := s.surfaceCapabilityDivergenceFinding(at); ok {
			t.Errorf("surface %s caps nothing today and must emit no divergence finding", g)
		}
	}
}

// TestSurfaceContextWindowsFor checks the per-surface overlay attached to the catalog:
// non-nil ONLY for a model that diverges across surfaces (Opus 4.8), with one entry per
// modeled surface, Foundry=200K and the rest 1M, AsOf-stamped.
func TestSurfaceContextWindowsFor(t *testing.T) {
	got := SurfaceContextWindowsFor("claude-opus-4-8")
	if len(got) != len(AllSurfaces()) {
		t.Fatalf("opus-4-8 overlay has %d entries, want one per surface (%d)", len(got), len(AllSurfaces()))
	}
	foundrySeen := false
	for _, sc := range got {
		if sc.AsOf != contextWindowAsOf {
			t.Errorf("overlay entry %s AsOf = %q, want %q", sc.Surface, sc.AsOf, contextWindowAsOf)
		}
		want := int64(1_000_000)
		if sc.Surface == model.GatewayFoundry {
			want, foundrySeen = 200_000, true
		}
		if sc.ContextWindow != want {
			t.Errorf("opus-4-8 on %s = %d, want %d", sc.Surface, sc.ContextWindow, want)
		}
	}
	if !foundrySeen {
		t.Error("opus-4-8 overlay must include the Foundry surface")
	}

	// Uniform models (1M or 200K on every surface) carry no overlay — their single
	// ContextWindow says it all.
	for _, id := range []string{"claude-opus-4-7", "claude-sonnet-5", "claude-sonnet-4-6", "claude-haiku-4-5", "claude-fable-5", "claude-mythos-5", "claude-opus-4-1", "gpt-4o"} {
		if sc := SurfaceContextWindowsFor(id); sc != nil {
			t.Errorf("SurfaceContextWindowsFor(%q) = %v, want nil (no divergence)", id, sc)
		}
	}
}

// TestBuildModelContextWindow proves buildModel reports the precise standard window
// (1M for Opus 4.8 / Sonnet 4.6, not the coarse 200K family floor) and attaches the
// per-surface overlay where it diverges — so the offline catalog agrees with the live
// path (which reports Opus 4.8 = 1M) and with SurfaceContextWindows.
func TestBuildModelContextWindow(t *testing.T) {
	opus := buildModel("claude-opus-4-8", "Claude Opus 4.8")
	if opus.ContextWindow != 1_000_000 {
		t.Errorf("buildModel(opus-4-8).ContextWindow = %d, want 1M (standard), not the 200K family floor", opus.ContextWindow)
	}
	if len(opus.SurfaceContextWindows) == 0 {
		t.Error("opus-4-8 must carry the per-surface overlay (it diverges on Foundry)")
	}

	sonnet := buildModel("claude-sonnet-4-6", "Claude Sonnet 4.6")
	if sonnet.ContextWindow != 1_000_000 {
		t.Errorf("buildModel(sonnet-4-6).ContextWindow = %d, want 1M", sonnet.ContextWindow)
	}
	if sonnet.SurfaceContextWindows != nil {
		t.Error("sonnet-4-6 is 1M on every surface — no per-surface overlay expected")
	}

	sonnet5 := buildModel("claude-sonnet-5", "Claude Sonnet 5")
	if sonnet5.ContextWindow != 1_000_000 {
		t.Errorf("buildModel(sonnet-5).ContextWindow = %d, want 1M", sonnet5.ContextWindow)
	}
	if sonnet5.MaxOutputTokens != 128_000 {
		t.Errorf("buildModel(sonnet-5).MaxOutputTokens = %d, want 128K", sonnet5.MaxOutputTokens)
	}

	haiku := buildModel("claude-haiku-4-5", "Claude Haiku 4.5")
	if haiku.ContextWindow != 200_000 {
		t.Errorf("buildModel(haiku-4-5).ContextWindow = %d, want 200K", haiku.ContextWindow)
	}
}

// TestTokenLabel checks the human token labels used in non-sensitive finding titles.
func TestTokenLabel(t *testing.T) {
	cases := map[int64]string{200_000: "200K", 1_000_000: "1M", 128_000: "128K", 0: "0", 1234: "1234"}
	for n, want := range cases {
		if got := tokenLabel(n); got != want {
			t.Errorf("tokenLabel(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestGatherFoundryEmitsCapabilityDivergence proves the finding reaches the sink
// through the REAL gather pipeline on a Foundry connector in its REALISTIC config —
// gateway=foundry with NO admin_key (Foundry exposes no Admin API, so an admin key is
// meaningless there). The finding is credential-independent declared knowledge emitted
// before the offline short-circuit, so the estate it targets actually gets it. A
// Claude-API connector running the full pipeline emits no such finding.
func TestGatherFoundryEmitsCapabilityDivergence(t *testing.T) {
	// Foundry, offline (no admin_key): the declared divergence finding still fires.
	foundry := New()
	foundry.doer = &fixtureDoer{t: t}
	foundry.now = fixedClock
	if err := foundry.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"gateway": "foundry",
	}}); err != nil {
		t.Fatalf("Open(foundry): %v", err)
	}
	sink := &captureSink{}
	if err := foundry.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather(foundry): %v", err)
	}
	var got *model.FindingReport
	for _, o := range sink.obs {
		if f, ok := o.(model.FindingReport); ok && f.Kind == findingSurfaceCapabilityDivergence {
			ff := f
			got = &ff
		}
	}
	if got == nil {
		t.Fatal("Foundry Gather must emit a surface_capability_divergence finding")
	}
	if got.SubjectRef != string(model.GatewayFoundry) || !contains(got.Title, "claude-opus-4-8") {
		t.Errorf("divergence finding = subject %q, title %q", got.SubjectRef, got.Title)
	}

	// Claude API (full pipeline, via fixtures): caps nothing, so no divergence finding.
	api, _ := newLive(t)
	apiSink := &captureSink{}
	if err := api.Gather(context.Background(), apiSink); err != nil {
		t.Fatalf("Gather(direct): %v", err)
	}
	for _, o := range apiSink.obs {
		if f, ok := o.(model.FindingReport); ok && f.Kind == findingSurfaceCapabilityDivergence {
			t.Errorf("Claude API caps nothing but emitted a divergence finding: %q", f.Title)
		}
	}
}

// contains is a tiny substring helper (avoids importing strings in the test for one use).
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// --- effort defaults + 300k output beta tests ---

// TestDefaultEffortFor pins the verified per-model effort defaults and supported levels
// (VERIFIED 2026-06-27, platform.claude.com/docs/en/build-with-claude/effort).
func TestDefaultEffortFor(t *testing.T) {
	cases := []struct {
		id          string
		wantDefault string
		wantLevels  int
		wantXHigh   bool
		wantOK      bool
	}{
		{"claude-opus-4-8", "high", 5, true, true},
		{"claude-opus-4-7", "high", 5, true, true},
		{"claude-opus-4-6", "high", 4, false, true},
		{"claude-opus-4-5", "high", 4, false, true},
		{"claude-sonnet-5", "high", 5, true, true},
		{"claude-sonnet-4-6", "high", 4, false, true},
		{"claude-fable-5", "high", 5, true, true},
		{"claude-mythos-5", "high", 5, true, true},
		{"claude-haiku-4-5", "", 0, false, false},  // no effort support
		{"claude-sonnet-4-5", "", 0, false, false}, // no effort support
		{"gpt-4o", "", 0, false, false},            // non-Claude
	}
	for _, c := range cases {
		def, levels, ok := DefaultEffortFor(c.id)
		if ok != c.wantOK {
			t.Errorf("DefaultEffortFor(%q) ok = %v, want %v", c.id, ok, c.wantOK)
			continue
		}
		if def != c.wantDefault {
			t.Errorf("DefaultEffortFor(%q) default = %q, want %q", c.id, def, c.wantDefault)
		}
		if len(levels) != c.wantLevels {
			t.Errorf("DefaultEffortFor(%q) levels = %d, want %d: %v", c.id, len(levels), c.wantLevels, levels)
		}
		hasXHigh := false
		for _, l := range levels {
			if l == "xhigh" {
				hasXHigh = true
			}
		}
		if hasXHigh != c.wantXHigh {
			t.Errorf("DefaultEffortFor(%q) xhigh = %v, want %v", c.id, hasXHigh, c.wantXHigh)
		}
	}
}

// TestSurfaceMaxOutputsFor pins the 300k-output beta per (model, surface): Opus 4.8/4.7/
// 4.6 and Sonnet 4.6 get 300K on Anthropic API surfaces (direct + claude-platform-aws);
// other models and other surfaces get nil (standard limit applies).
func TestSurfaceMaxOutputsFor(t *testing.T) {
	// Supported model: should have entries for direct + claude-platform-aws.
	out := SurfaceMaxOutputsFor("claude-opus-4-8")
	if len(out) != 2 {
		t.Fatalf("opus-4-8 300k beta: want 2 entries, got %d", len(out))
	}
	for _, e := range out {
		if e.MaxOutputTokens != 300_000 {
			t.Errorf("opus-4-8 on %s: max output = %d, want 300K", e.Surface, e.MaxOutputTokens)
		}
		if e.Beta != "output-300k-2026-03-24" {
			t.Errorf("opus-4-8 on %s: beta = %q, want output-300k-2026-03-24", e.Surface, e.Beta)
		}
	}

	// Other supported models.
	for _, id := range []string{"claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-5", "claude-sonnet-4-6"} {
		if o := SurfaceMaxOutputsFor(id); len(o) != 2 {
			t.Errorf("SurfaceMaxOutputsFor(%q) = %d entries, want 2", id, len(o))
		}
	}
	for _, e := range SurfaceMaxOutputsFor("claude-sonnet-5") {
		if e.AsOf != "2026-07-03" {
			t.Errorf("sonnet-5 output beta on %s AsOf = %q, want 2026-07-03", e.Surface, e.AsOf)
		}
	}

	// Unsupported models: no output beta.
	for _, id := range []string{"claude-haiku-4-5", "claude-fable-5", "claude-mythos-5", "claude-opus-4-5", "gpt-4o"} {
		if o := SurfaceMaxOutputsFor(id); o != nil {
			t.Errorf("SurfaceMaxOutputsFor(%q) = %v, want nil (not eligible for 300k beta)", id, o)
		}
	}
}

// TestBuildModelEffortAndOutputBeta proves buildModel wires the effort default and
// the 300k output beta into the Model struct.
func TestBuildModelEffortAndOutputBeta(t *testing.T) {
	opus := buildModel("claude-opus-4-8", "Claude Opus 4.8")
	if opus.DefaultEffort != "high" {
		t.Errorf("opus-4-8 DefaultEffort = %q, want high", opus.DefaultEffort)
	}
	if len(opus.SurfaceMaxOutputs) == 0 {
		t.Error("opus-4-8 must carry SurfaceMaxOutputs (300k beta)")
	}
	for _, o := range opus.SurfaceMaxOutputs {
		if o.MaxOutputTokens != 300_000 {
			t.Errorf("opus-4-8 SurfaceMaxOutput on %s = %d, want 300K", o.Surface, o.MaxOutputTokens)
		}
	}

	sonnet5 := buildModel("claude-sonnet-5", "Claude Sonnet 5")
	if sonnet5.DefaultEffort != "high" {
		t.Errorf("sonnet-5 DefaultEffort = %q, want high", sonnet5.DefaultEffort)
	}
	if len(sonnet5.SurfaceMaxOutputs) == 0 {
		t.Error("sonnet-5 must carry SurfaceMaxOutputs (300k beta)")
	}

	haiku := buildModel("claude-haiku-4-5", "Claude Haiku 4.5")
	if haiku.DefaultEffort != "" {
		t.Errorf("haiku-4-5 DefaultEffort = %q, want empty (no effort support)", haiku.DefaultEffort)
	}
	if haiku.SurfaceMaxOutputs != nil {
		t.Error("haiku-4-5 must NOT carry SurfaceMaxOutputs")
	}
}
