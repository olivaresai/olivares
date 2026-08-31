// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// publishSafety emits a provider safety-posture finding on the bus from a connector
// source, with an explicit DetailHash so a test can simulate both a re-pull (same
// hash ⇒ dedup) and a posture change (new hash ⇒ a fresh row).
func (h *harness) publishSafety(tenant model.TenantID, subjectKind, subjectRef, detailHash, title string, sev sdkmodel.Severity) {
	h.t.Helper()
	_ = h.bus.Publish(context.Background(), event.FromObservation(tenant.String(), "olivares.openai", sdkmodel.FindingReport{
		Kind:        "safety_posture",
		Severity:    sev,
		SubjectKind: subjectKind,
		SubjectRef:  subjectRef,
		Title:       title,
		DetailHash:  detailHash,
		OccurredAt:  time.Now(),
	}))
}

// TestSafetyPosturePersistsAndDedups proves the carve-out: a provider
// safety_posture finding persists at Info/Low/Medium (the HIGH+ rule would otherwise
// drop it), an unchanged re-pull dedups on the deterministic detail hash, and a real
// posture change (a new hash for the same subject) records a fresh row.
func TestSafetyPosturePersistsAndDedups(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	list := func(query string) []any {
		r := h.do("GET", "/v1/m/security/findings?"+query, admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("findings = %d %s", r.code, r.raw)
		}
		items, _ := r.body["items"].([]any)
		return items
	}
	moderation := func() []any { return list("kind=safety_posture&subject_kind=openai.moderation") }

	// 1) An Info posture finding must persist (the carve-out, not the HIGH+ rule).
	h.publishSafety(tenant, "openai.moderation", "organization", "h1", "OpenAI Moderation API in use", sdkmodel.SeverityInfo)
	var mod []any
	for i := 0; i < 200; i++ {
		if mod = moderation(); len(mod) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(mod) != 1 {
		t.Fatalf("moderation posture = %d rows, want 1 (Info must persist via the carve-out)", len(mod))
	}

	// 2) A re-pull (same hash) dedups; a sentinel in another surface proves the dup was
	// processed once the sentinel appears (a fixed sleep would pass vacuously).
	h.publishSafety(tenant, "openai.moderation", "organization", "h1", "OpenAI Moderation API in use", sdkmodel.SeverityInfo)
	h.publishSafety(tenant, "bedrock.guardrail", "123/us-east-1", "g1", "No Bedrock guardrails configured", sdkmodel.SeverityMedium)
	var all []any
	for i := 0; i < 200; i++ {
		if all = list("kind=safety_posture"); len(all) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(moderation()) != 1 {
		t.Fatalf("moderation posture after re-pull = %d, want 1 (dedup on the deterministic hash)", len(moderation()))
	}

	// 3) A posture CHANGE (new hash, same subject) records a fresh row.
	h.publishSafety(tenant, "openai.moderation", "organization", "h2", "No OpenAI Moderation API usage observed", sdkmodel.SeverityLow)
	for i := 0; i < 200; i++ {
		if len(moderation()) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(moderation()) != 2 {
		t.Fatalf("moderation posture after state change = %d, want 2 (a new hash must persist)", len(moderation()))
	}
}

// TestSafetyPostureView proves GET /safety-posture aggregates the posture findings into
// a per-provider-surface roll-up.
func TestSafetyPostureView(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	h.publishSafety(tenant, "openai.moderation", "organization", "h1", "OpenAI Moderation API in use", sdkmodel.SeverityInfo)
	h.publishSafety(tenant, "bedrock.guardrail", "123/us-east-1", "g1", "Bedrock guardrail prod active", sdkmodel.SeverityInfo)
	h.publishSafety(tenant, "bedrock.guardrail", "123/us-east-1/weak", "g2", "Bedrock guardrail has gaps", sdkmodel.SeverityMedium)

	// Wait until all three posture findings have persisted, so the roll-up is complete.
	for i := 0; i < 400; i++ {
		r := h.do("GET", "/v1/m/security/findings?kind=safety_posture", admin, nil, tenantHdr(tenant))
		if r.code == http.StatusOK {
			if items, _ := r.body["items"].([]any); len(items) >= 3 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}

	r := h.do("GET", "/v1/m/security/safety-posture", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("safety-posture = %d %s", r.code, r.raw)
	}
	providers, _ := r.body["providers"].([]any)
	if len(providers) != 2 {
		t.Fatalf("safety-posture providers = %d, want 2 (openai.moderation + bedrock.guardrail)", len(providers))
	}

	bySurface := map[string]map[string]any{}
	for _, p := range providers {
		pm := p.(map[string]any)
		bySurface[pm["subject_kind"].(string)] = pm
	}
	bg, ok := bySurface["bedrock.guardrail"]
	if !ok || bg["total"].(float64) != 2 {
		t.Fatalf("bedrock.guardrail roll-up = %v, want total 2", bg)
	}
	// Core severity has no "info"; the connector's Info posture collapses to "low"
	// when persisted (sevToCore). So the surface shows low 1 (the Info finding) +
	// medium 1 (the gap finding).
	bySev, _ := bg["by_severity"].(map[string]any)
	if bySev["medium"].(float64) != 1 || bySev["low"].(float64) != 1 {
		t.Fatalf("bedrock.guardrail severity breakdown = %v, want low 1 + medium 1", bySev)
	}
	if _, ok := bySurface["openai.moderation"]; !ok {
		t.Fatal("openai.moderation surface missing from the safety-posture view")
	}
}
