// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/store"
)

// This module answers on its OWN mapper, not core/api's, so the 403 has to be proven here
// too — that is precisely why measured the defect on three separate paths rather than
// one. writeDepthError and writeFedRAMPError both fall through to writeStoreError, so
// mapping it once here covers every handler in the module.
func TestAddonRefusalIs403OnTheComplianceMapper(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantSubstr []string
	}{
		{
			name:       "names the add-on and the operation",
			err:        license.AddonRequired("compliance-packs", "compliance.depth.export"),
			wantSubstr: []string{"compliance-packs", "compliance.depth.export", "exporting your data are unaffected"},
		},
		{
			name:       "through the depth writer",
			err:        license.AddonRequired("regulated", "compliance.fedramp.pack"),
			wantSubstr: []string{"regulated", "compliance.fedramp.pack"},
		},
		{
			name:       "bare sentinel still refuses cleanly",
			err:        license.ErrAddonRequiresLicense,
			wantSubstr: []string{"a commercial add-on is required"},
		},
		{
			name:       "wrapped by a caller",
			err:        fmt.Errorf("depth: %w", license.AddonRequired("identity-scale", "op")),
			wantSubstr: []string{"identity-scale"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeDepthError(rec, tc.err)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, want := range tc.wantSubstr {
				if !strings.Contains(body, want) {
					t.Errorf("body %s does not mention %q", body, want)
				}
			}
			// The envelope must stay the module's own shape, not a bare string.
			var env map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("body is not the JSON error envelope: %v", err)
			}
			if _, ok := env["error"]; !ok {
				t.Fatalf("body %s has no error envelope", body)
			}
		})
	}
}

// The control: the arm must not have widened. A real fault is still a 500, and the other
// mapped cases keep their own codes.
func TestComplianceMapperStillDistinguishesRealFaults(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{errors.New("a genuine internal fault"), http.StatusInternalServerError},
		{store.ErrNotFound, http.StatusNotFound},
		{store.ErrConflict, http.StatusConflict},
	} {
		rec := httptest.NewRecorder()
		writeStoreError(rec, tc.err)
		if rec.Code != tc.want {
			t.Errorf("%v mapped to %d, want %d", tc.err, rec.Code, tc.want)
		}
	}
}
