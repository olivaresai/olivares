// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// TestSurfacesAsOfStampsEveryRow proves the exported AsOf accessor IS the stamp every
// surface row carries — one capture date, no per-row drift.
func TestSurfacesAsOfStampsEveryRow(t *testing.T) {
	if SurfacesAsOf() == "" {
		t.Fatal("SurfacesAsOf() must not be empty")
	}
	for _, s := range AllSurfaces() {
		if s.AsOf != SurfacesAsOf() {
			t.Errorf("surface %q AsOf = %q, want the matrix stamp %q", s.Gateway, s.AsOf, SurfacesAsOf())
		}
	}
}

// TestLifecycleReferenceFamilies pins the full materialized registry: every non-exempt
// family in registry order, with its deterministic display label (the table asserts ALL
// of them — a humanizer regression must fail loudly, never relabel a model silently),
// its published deprecation date/replacement, and the to-confirm surface sets restated
// from the verified comments ("Bedrock" ⇒ both modeled bedrock gateways).
func TestLifecycleReferenceFamilies(t *testing.T) {
	bedrocks := []model.Gateway{model.GatewayBedrockLegacy, model.GatewayBedrockMantle}
	want := []struct {
		modelID      string
		displayName  string
		deprecatedOn string
		replacement  string
		perSurface   int
		toConfirm    []model.Gateway
	}{
		{"claude-opus-4-1", "Claude Opus 4.1", "2026-06-05", "claude-opus-4-8", 3, []model.Gateway{model.GatewayVertex}},
		{"claude-opus-4-2025", "Claude Opus 4", "2026-04-14", "claude-opus-4-8", 3, []model.Gateway{model.GatewayVertex}},
		{"claude-opus-4-0", "Claude Opus 4", "2026-04-14", "claude-opus-4-8", 3, []model.Gateway{model.GatewayVertex}},
		{"claude-sonnet-4", "Claude Sonnet 4", "2026-04-14", "claude-sonnet-4-6", 4, bedrocks},
		{"claude-mythos-preview", "Claude Mythos (preview)", "2026-06-09", "claude-mythos-5", 3, nil},
		{"claude-3-7-sonnet", "Claude 3.7 Sonnet", "2025-10-28", "claude-sonnet-4-6", 3, nil},
		{"claude-3-5-haiku", "Claude 3.5 Haiku", "2025-12-19", "claude-haiku-4-5-20251001", 3,
			[]model.Gateway{model.GatewayBedrockLegacy, model.GatewayBedrockMantle, model.GatewayVertex}},
		{"claude-3-haiku", "Claude 3 Haiku", "2026-02-19", "claude-haiku-4-5-20251001", 3, nil},
		{"claude-3-opus", "Claude 3 Opus", "2025-06-30", "claude-opus-4-8", 3, nil},
		{"claude-3-5-sonnet", "Claude 3.5 Sonnet", "2025-08-13", "claude-sonnet-4-6", 3, nil},
		{"claude-3-sonnet", "Claude 3 Sonnet", "2025-01-21", "claude-sonnet-4-6", 3, nil},
		{"claude-2.", "Claude 2", "", "", 3, nil},
	}

	got := LifecycleReference()
	if len(got) != len(want) {
		t.Fatalf("LifecycleReference() = %d families, want %d (registry order, exempt skipped)", len(got), len(want))
	}
	for i, w := range want {
		f := got[i]
		if f.ModelID != w.modelID {
			t.Errorf("family[%d].ModelID = %q, want %q (registry order must be preserved)", i, f.ModelID, w.modelID)
			continue
		}
		if f.DisplayName != w.displayName {
			t.Errorf("%s: DisplayName = %q, want %q", w.modelID, f.DisplayName, w.displayName)
		}
		if f.DeprecatedOn != w.deprecatedOn {
			t.Errorf("%s: DeprecatedOn = %q, want %q", w.modelID, f.DeprecatedOn, w.deprecatedOn)
		}
		if f.Replacement != w.replacement {
			t.Errorf("%s: Replacement = %q, want %q", w.modelID, f.Replacement, w.replacement)
		}
		if f.AsOf != LifecycleAsOf() {
			t.Errorf("%s: AsOf = %q, want %q", w.modelID, f.AsOf, LifecycleAsOf())
		}
		if len(f.PerSurface) != w.perSurface {
			t.Errorf("%s: %d PerSurface rows, want %d", w.modelID, len(f.PerSurface), w.perSurface)
		}
		for j := 1; j < len(f.PerSurface); j++ {
			if f.PerSurface[j-1].Surface >= f.PerSurface[j].Surface {
				t.Errorf("%s: PerSurface not sorted by surface (%q >= %q)", w.modelID, f.PerSurface[j-1].Surface, f.PerSurface[j].Surface)
			}
		}
		if len(f.ToConfirm) != len(w.toConfirm) {
			t.Errorf("%s: ToConfirm = %v, want %v", w.modelID, f.ToConfirm, w.toConfirm)
			continue
		}
		for j, g := range w.toConfirm {
			if f.ToConfirm[j] != g {
				t.Errorf("%s: ToConfirm[%d] = %q, want %q (sorted)", w.modelID, j, f.ToConfirm[j], g)
			}
		}
	}
}

// TestLifecycleReferenceSkipsExempt proves the carve-out entries never surface as
// families — an exempt hit means "no schedule", and materializing one would fabricate
// a lifecycle for a still-active id.
func TestLifecycleReferenceSkipsExempt(t *testing.T) {
	for _, f := range LifecycleReference() {
		if f.ModelID == "claude-sonnet-4-5" || f.ModelID == "claude-sonnet-4-6" {
			t.Errorf("exempt carve-out %q materialized as a lifecycle family", f.ModelID)
		}
	}
}

// TestLifecycleReferenceSonnet4Detail pins the one family with verified per-surface
// divergence end-to-end: the Anthropic-operated 2026-06-15 dates, the later Vertex
// date, and the two explicit bedrock to-confirm surfaces (date NOT published — the
// matrix must render "to-confirm", never "never retires" and never a fabricated date).
func TestLifecycleReferenceSonnet4Detail(t *testing.T) {
	var fam LifecycleFamily
	for _, f := range LifecycleReference() {
		if f.ModelID == "claude-sonnet-4" {
			fam = f
			break
		}
	}
	if fam.ModelID == "" {
		t.Fatal("claude-sonnet-4 family missing from LifecycleReference()")
	}
	wantDates := map[model.Gateway]string{
		model.GatewayClaudePlatformAWS: "2026-06-15",
		model.GatewayDirect:            "2026-06-15",
		model.GatewayFoundry:           "2026-06-15",
		model.GatewayVertex:            "2026-09-14",
	}
	if len(fam.PerSurface) != len(wantDates) {
		t.Fatalf("sonnet-4 PerSurface = %v, want the 4 verified surfaces", fam.PerSurface)
	}
	for _, sd := range fam.PerSurface {
		if wantDates[sd.Surface] != sd.RetiresOn {
			t.Errorf("sonnet-4 %q retires_on = %q, want %q", sd.Surface, sd.RetiresOn, wantDates[sd.Surface])
		}
	}
	// The bedrock surfaces must be to-confirm, NOT in PerSurface (no date exists).
	for _, sd := range fam.PerSurface {
		if sd.Surface == model.GatewayBedrockLegacy || sd.Surface == model.GatewayBedrockMantle {
			t.Errorf("sonnet-4 bedrock surface %q has a PerSurface date %q — the authority published none", sd.Surface, sd.RetiresOn)
		}
	}
}

// TestLifecycleReferenceReturnsFreshSlices proves a caller mutating the returned
// slices cannot corrupt the registry for the next reader.
func TestLifecycleReferenceReturnsFreshSlices(t *testing.T) {
	a := LifecycleReference()
	a[0].PerSurface[0].RetiresOn = "9999-01-01"
	if len(a[0].ToConfirm) > 0 {
		a[0].ToConfirm[0] = model.Gateway("mutated")
	}
	b := LifecycleReference()
	if b[0].PerSurface[0].RetiresOn == "9999-01-01" {
		t.Error("mutating a returned PerSurface leaked into the registry")
	}
	if len(b[0].ToConfirm) > 0 && b[0].ToConfirm[0] == model.Gateway("mutated") {
		t.Error("mutating a returned ToConfirm leaked into the registry")
	}
}

// TestParamDeprecationReference pins the declared descriptor against the enforcement
// truth (RejectsSamplingParams): params/status verbatim, the Affected vocabulary of
// the finding title, and the lifecycle AsOf stamp.
func TestParamDeprecationReference(t *testing.T) {
	ref := ParamDeprecationReference()
	wantParams := []string{"temperature", "top_p", "top_k"}
	if len(ref.Params) != len(wantParams) {
		t.Fatalf("Params = %v, want %v", ref.Params, wantParams)
	}
	for i, p := range wantParams {
		if ref.Params[i] != p {
			t.Errorf("Params[%d] = %q, want %q", i, ref.Params[i], p)
		}
	}
	if ref.Affected != "Opus 4.7+, Fable/Mythos 5" {
		t.Errorf("Affected = %q, want the finding-title vocabulary", ref.Affected)
	}
	if ref.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400", ref.HTTPStatus)
	}
	if ref.AsOf != LifecycleAsOf() {
		t.Errorf("AsOf = %q, want %q", ref.AsOf, LifecycleAsOf())
	}
	// Coherence with the per-id evaluator: each named generation actually rejects.
	for _, id := range []string{"claude-opus-4-7", "claude-fable-5", "claude-mythos-5"} {
		if !RejectsSamplingParams(id) {
			t.Errorf("RejectsSamplingParams(%q) = false; the descriptor names it as affected", id)
		}
	}
}
