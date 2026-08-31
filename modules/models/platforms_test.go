// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/models"
)

// stubPlatforms is a wired declared-reference provider.
type stubPlatforms struct {
	ref models.PlatformsReference
	err error
}

func (s stubPlatforms) Platforms(context.Context) (models.PlatformsReference, error) {
	return s.ref, s.err
}

// platformsFixture mirrors the adapter's output shape: one surface row, one
// lifecycle family carrying BOTH a confirmed dated row and the explicit to-confirm
// rows (empty date, no deprecated_on), and the param-deprecation card.
func platformsFixture() models.PlatformsReference {
	return models.PlatformsReference{
		Surfaces: []models.PlatformSurface{{
			Gateway:            "direct",
			DisplayName:        "Anthropic API (first-party)",
			Operator:           "Anthropic",
			OperatorDataAccess: "Anthropic-operated",
			BaseURLPattern:     "https://api.anthropic.com",
			AuthScheme:         "x-api-key",
			ModelIDForm:        "bare model id",
			APIs:               models.PlatformAPISupport{Messages: true, Admin: true, Compliance: true, Models: true, Batches: true, MCPConnector: true},
			Billing:            "Anthropic invoice",
			HIPAA:              "to-confirm",
			HIPAAStatus:        "to-confirm",
			ZDR:                "on-request",
			Residency:          "per-request inference_geo",
			AsOf:               "2026-06-06",
			Notes:              "full API set",
		}},
		SurfacesAsOf:   "2026-06-06",
		SurfacesSource: "connectors/claude-api/surfaces.go",
		Lifecycles: []models.PlatformLifecycle{{
			ModelID:     "claude-sonnet-4",
			DisplayName: "Claude Sonnet 4",
			Retirements: []models.PlatformRetirement{
				{Surface: "bedrock-legacy", RetiresOn: "", Status: "to-confirm", ReplacementRef: "claude-sonnet-4-6", AsOf: "2026-06-09"},
				{Surface: "bedrock-mantle", RetiresOn: "", Status: "to-confirm", ReplacementRef: "claude-sonnet-4-6", AsOf: "2026-06-09"},
				{Surface: "direct", RetiresOn: "2026-06-15", Status: "confirmed", ReplacementRef: "claude-sonnet-4-6", DeprecatedOn: "2026-04-14", AsOf: "2026-06-09"},
				{Surface: "vertex", RetiresOn: "2026-09-14", Status: "confirmed", ReplacementRef: "claude-sonnet-4-6", DeprecatedOn: "2026-04-14", AsOf: "2026-06-09"},
			},
		}},
		LifecycleAsOf:   "2026-06-09",
		LifecycleSource: "connectors/claude-api/lifecycle.go",
		ParamDeprecation: models.PlatformParamDeprecation{
			Params: []string{"temperature", "top_p", "top_k"}, Affected: "Opus 4.7+, Fable/Mythos 5", HTTPStatus: 400,
		},
	}
}

// TestPlatformsDegradesWithoutProvider proves GET /platforms never 500s with no
// provider wired: 200, available=false with a reason, and EMPTY (not null)
// collections so the response shape stays stable for the web consumer.
func TestPlatformsDegradesWithoutProvider(t *testing.T) {
	m := models.New() // no platforms provider
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	r := h.do("GET", "/v1/m/models/platforms", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("platforms (unwired) = %d, want 200 (degrade, never 500) — %s", r.code, r.raw)
	}
	if r.body["available"] != false || r.body["reason"] == "" {
		t.Errorf("unwired reference must be available=false with a reason: %v", r.body)
	}
	if surfaces, ok := r.body["surfaces"].([]any); !ok || len(surfaces) != 0 {
		t.Errorf("unwired surfaces must be an empty array, got %v", r.body["surfaces"])
	}
	if lifecycles, ok := r.body["lifecycles"].([]any); !ok || len(lifecycles) != 0 {
		t.Errorf("unwired lifecycles must be an empty array, got %v", r.body["lifecycles"])
	}
	pd, _ := r.body["param_deprecation"].(map[string]any)
	if pd == nil {
		t.Fatal("param_deprecation must be present (empty) even when degraded")
	}
	if params, ok := pd["params"].([]any); !ok || len(params) != 0 {
		t.Errorf("degraded param_deprecation.params must be an empty array, got %v", pd["params"])
	}
}

// TestPlatformsMaterializesReference proves a wired provider materializes the full
// envelope: AsOf/source stamps, the surface attributes, and the lifecycle rows with
// the to-confirm cells VERBATIM (empty date, status to-confirm, family-wide
// replacement_ref, NO deprecated_on — the partner authority published neither).
func TestPlatformsMaterializesReference(t *testing.T) {
	m := models.New(models.WithPlatformsProvider(stubPlatforms{ref: platformsFixture()}))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	r := h.do("GET", "/v1/m/models/platforms", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["available"] != true {
		t.Fatalf("platforms (wired) = %d available=%v — %s", r.code, r.body["available"], r.raw)
	}
	if r.body["surfaces_as_of"] != "2026-06-06" || r.body["surfaces_source"] != "connectors/claude-api/surfaces.go" {
		t.Errorf("surfaces provenance = %v / %v", r.body["surfaces_as_of"], r.body["surfaces_source"])
	}
	if r.body["lifecycle_as_of"] != "2026-06-09" || r.body["lifecycle_source"] != "connectors/claude-api/lifecycle.go" {
		t.Errorf("lifecycle provenance = %v / %v", r.body["lifecycle_as_of"], r.body["lifecycle_source"])
	}

	surfaces, _ := r.body["surfaces"].([]any)
	if len(surfaces) != 1 {
		t.Fatalf("surfaces rows = %d, want 1", len(surfaces))
	}
	s0, _ := surfaces[0].(map[string]any)
	if s0["gateway"] != "direct" || s0["hipaa_status"] != "to-confirm" {
		t.Errorf("surface row = %v, want gateway=direct hipaa_status=to-confirm", s0)
	}
	apis, _ := s0["apis"].(map[string]any)
	if apis == nil || apis["mcp_connector"] != true {
		t.Errorf("surface apis = %v, want the nested mcp_connector flag", s0["apis"])
	}

	lifecycles, _ := r.body["lifecycles"].([]any)
	if len(lifecycles) != 1 {
		t.Fatalf("lifecycle families = %d, want 1", len(lifecycles))
	}
	fam, _ := lifecycles[0].(map[string]any)
	if fam["model_id"] != "claude-sonnet-4" || fam["display_name"] != "Claude Sonnet 4" {
		t.Errorf("family = %v", fam)
	}
	rows, _ := fam["retirements"].([]any)
	if len(rows) != 4 {
		t.Fatalf("retirement rows = %d, want 4", len(rows))
	}
	first, _ := rows[0].(map[string]any)
	if first["surface"] != "bedrock-legacy" || first["status"] != "to-confirm" || first["retires_on"] != "" {
		t.Errorf("to-confirm row = %v, want bedrock-legacy/to-confirm/empty date", first)
	}
	if first["replacement_ref"] != "claude-sonnet-4-6" {
		t.Errorf("to-confirm row must carry the family-wide replacement_ref, got %v", first["replacement_ref"])
	}
	if _, has := first["deprecated_on"]; has {
		t.Errorf("to-confirm row must omit deprecated_on (unpublished), got %v", first["deprecated_on"])
	}
	confirmed, _ := rows[2].(map[string]any)
	if confirmed["surface"] != "direct" || confirmed["retires_on"] != "2026-06-15" || confirmed["deprecated_on"] != "2026-04-14" {
		t.Errorf("confirmed row = %v", confirmed)
	}

	pd, _ := r.body["param_deprecation"].(map[string]any)
	if pd == nil || pd["affected"] != "Opus 4.7+, Fable/Mythos 5" || pd["http_status"] != float64(400) {
		t.Errorf("param_deprecation = %v", pd)
	}
}

// TestPlatformsTransientErrorDegrades proves a provider error degrades to
// available=false with a generic reason — never a 500 and never the leaked error.
func TestPlatformsTransientErrorDegrades(t *testing.T) {
	m := models.New(models.WithPlatformsProvider(stubPlatforms{err: context.DeadlineExceeded}))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)
	r := h.do("GET", "/v1/m/models/platforms", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["available"] != false {
		t.Fatalf("transient error must degrade to 200/available=false, got %d %v", r.code, r.body)
	}
	if reason, _ := r.body["reason"].(string); reason == "" || reason == context.DeadlineExceeded.Error() {
		t.Errorf("reason must be a generic non-leaking string, got %q", reason)
	}
}

// TestPlatformsRBAC proves the route is read-tier (a viewer reads it — non-sensitive
// declared reference) and authenticated (no token → 401).
func TestPlatformsRBAC(t *testing.T) {
	m := models.New(models.WithPlatformsProvider(stubPlatforms{ref: platformsFixture()}))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	if r := h.do("GET", "/v1/m/models/platforms", "", nil, tenantHdr(tenant)); r.code != http.StatusUnauthorized {
		t.Errorf("unauthenticated platforms = %d, want 401", r.code)
	}
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)
	if r := h.do("GET", "/v1/m/models/platforms", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("viewer platforms = %d, want 200 (read-tier)", r.code)
	}
}
