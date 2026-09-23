// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/license"
)

func TestOSCALPOAMExportOutcomes(t *testing.T) {
	const secret = "private-poam-diagnostic-marker"
	tests := []struct {
		name       string
		noBuilder  bool
		model      map[string]any
		err        error
		reason     string
		disclaimer string
	}{
		{
			name:       "wrapped_license_refusal",
			err:        fmt.Errorf("%s: %w", secret, license.AddonRequired(secret, secret)),
			reason:     "addon_requires_license",
			disclaimer: " POA&M section omitted: the required add-on license is unavailable (addon_requires_license).",
		},
		{
			name:       "license_sentinel",
			err:        fmt.Errorf("%s: %w", secret, license.ErrAddonRequiresLicense),
			reason:     "addon_requires_license",
			disclaimer: " POA&M section omitted: the required add-on license is unavailable (addon_requires_license).",
		},
		{
			name:       "render_failure",
			model:      map[string]any{"incomplete": secret},
			err:        errors.New(secret),
			reason:     "render_failed",
			disclaimer: " POA&M section omitted: rendering failed (render_failed).",
		},
		{
			name:  "attached",
			model: map[string]any{"uuid": "poam-1", "poam-items": []any{}},
		},
		{name: "nothing_to_plan"},
		{name: "no_builder", noBuilder: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := &stubPOAMBuilder{poam: tt.model, err: tt.err}
			var opts []Option
			if !tt.noBuilder {
				opts = append(opts, WithPOAMBuilder(builder))
			}
			h := newHarness(t, opts...)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			owner := h.roleToken(admin, tenant, "o@x.io", "owner")
			id := sealEUAIAct(t, h, owner, tenant)
			var logs bytes.Buffer
			h.mod.SetLogger(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			path := "/v1/m/compliance/evidence/" + id + "/export?format=oscal"
			exported := h.do("GET", path, owner, nil, tenantHdr(tenant))
			if exported.code != http.StatusOK {
				t.Fatalf("export status = %d, want 200", exported.code)
			}
			meta := h.auditMetaFor(tenant, "compliance.evidence.export")
			if meta == nil {
				t.Fatal("export must commit an audit event")
			}
			auditJSON, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			for surface, text := range map[string]string{
				"response": exported.raw,
				"audit":    string(auditJSON),
				"logs":     logs.String(),
			} {
				if strings.Contains(text, secret) {
					t.Errorf("%s discloses a private builder diagnostic", surface)
				}
			}
			wantMeta := map[string]any{"framework": "eu_ai_act", "format": "oscal"}
			wantAttached := tt.model != nil && tt.err == nil
			if wantAttached {
				wantMeta["oscal_poam"] = true
			} else if tt.reason != "" {
				wantMeta["oscal_poam"] = false
				wantMeta["oscal_poam_omission_reason"] = tt.reason
			}
			if !reflect.DeepEqual(meta, wantMeta) {
				t.Errorf("export audit metadata = %v, want %v", meta, wantMeta)
			}
			poam, attached := exported.body["plan-of-action-and-milestones"]
			if attached != wantAttached || (attached && !reflect.DeepEqual(poam, tt.model)) {
				t.Errorf("POA&M attachment = %v, want attached=%t", poam, wantAttached)
			}
			if tt.reason == "" {
				if logs.Len() != 0 {
					t.Error("successful or absent POA&M must not log an omission")
				}
			} else {
				var entry map[string]any
				if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
					t.Errorf("expected one structured omission warning: %v", err)
				} else {
					delete(entry, "time")
					want := map[string]any{
						"level": "WARN", "msg": "compliance: OSCAL POA&M omitted",
						"package_id": id, "framework": "eu_ai_act", "reason": tt.reason,
					}
					if !reflect.DeepEqual(entry, want) {
						t.Errorf("omission warning = %v, want %v", entry, want)
					}
				}
			}

			// Render the same sealed package with nothing to plan as the open-model baseline.
			builder.poam, builder.err = nil, nil
			baseline := h.do("GET", path, owner, nil, tenantHdr(tenant))
			if baseline.code != http.StatusOK {
				t.Fatalf("baseline export status = %d, want 200", baseline.code)
			}
			for _, key := range []string{"component-definition", "assessment-results", "control-mapping", "manifest"} {
				model, ok := exported.body[key].(map[string]any)
				if !ok || len(model) == 0 || !reflect.DeepEqual(model, baseline.body[key]) {
					t.Errorf("export must preserve the complete %s", key)
				}
			}
			baseDisclaimer, ok := baseline.body["disclaimer"].(string)
			if !ok || baseDisclaimer == "" {
				t.Fatal("baseline disclaimer is missing")
			}
			if got := exported.body["disclaimer"]; got != baseDisclaimer+tt.disclaimer {
				t.Errorf("disclaimer = %v, want the baseline plus %q", got, tt.disclaimer)
			}
			delete(exported.body, "plan-of-action-and-milestones")
			exported.body["disclaimer"] = baseDisclaimer
			if !reflect.DeepEqual(exported.body, baseline.body) {
				t.Error("POA&M outcome changed the open export outside its attachment and disclaimer")
			}
		})
	}
}
