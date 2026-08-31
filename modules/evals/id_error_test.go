// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestEveryEvalsRouteIDRejectionNamesItsField(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "id-errors")

	for _, row := range []struct {
		name, method, path, field string
	}{
		{"get suite", "GET", "/v1/m/evals/suites/not-an-id", "suite_id"},
		{"archive suite", "POST", "/v1/m/evals/suites/not-an-id/archive", "suite_id"},
		{"add suite case", "POST", "/v1/m/evals/suites/not-an-id/cases", "suite_id"},
		{"list suite cases", "GET", "/v1/m/evals/suites/not-an-id/cases", "suite_id"},
		{"get run", "GET", "/v1/m/evals/runs/not-an-id", "run_id"},
		{"list run results", "GET", "/v1/m/evals/runs/not-an-id/results", "run_id"},
		{"stream run", "GET", "/v1/m/evals/runs/not-an-id/stream", "run_id"},
		{"get gate", "GET", "/v1/m/evals/gate/not-an-id", "gate_id"},
		{"override gate", "POST", "/v1/m/evals/gate/not-an-id/override", "gate_id"},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			r := h.do(row.method, row.path, admin, nil, tenantHdr(tenant))
			if r.code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", r.code, r.raw)
			}
			if !strings.Contains(r.raw, row.field) {
				t.Fatalf("body = %s, want the caller field %q", r.raw, row.field)
			}
		})
	}
}

// TestCanonicalButAbsentEvalsIDsRemainNotFound is the non-firing direction for
// the path validator. Rejecting every identifier would make the malformed rows
// above green, but would turn a valid lookup into "your field is invalid". A
// canonical, absent UUID is a lookup decision and remains 404.
func TestCanonicalButAbsentEvalsIDsRemainNotFound(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "id-not-found")
	id := model.NewID().String()

	for _, path := range []string{
		"/v1/m/evals/suites/" + id,
		"/v1/m/evals/runs/" + id,
		"/v1/m/evals/gate/" + id,
	} {
		r := h.do(http.MethodGet, path, admin, nil, tenantHdr(tenant))
		if r.code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404; body = %s", path, r.code, r.raw)
		}
	}
}

// TestEvalsRouteIDErrorCoverage declares the exact census behind the regression test.
// The predicate is a production call that answers an invalid route ID through
// errorBody. The four files and all nine sites are named here so narrowing the
// protected surface, removing a validation, or adding another site costs a visible diff.
func TestEvalsRouteIDErrorCoverage(t *testing.T) {
	t.Parallel()

	files := map[string]int{
		"gate.go":   2,
		"runs.go":   2,
		"stream.go": 1,
		"suites.go": 4,
	}
	namedID := regexp.MustCompile(`errorBody\("invalid (?:gate|run|suite)_id"\)`)
	total := 0
	for file, want := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read declared census file %s: %v", file, err)
		}
		if n := strings.Count(string(src), `errorBody("invalid id")`); n != 0 {
			t.Errorf("%s has %d route-ID rejections that do not name the field", file, n)
		}
		if got := len(namedID.FindAll(src, -1)); got != want {
			t.Errorf("%s named route-ID rejections = %d, want declared count %d", file, got, want)
		}
		total += want
	}
	if len(files) != 4 || total != 9 {
		t.Fatalf("declared census = %d files/%d sites, want 4 files/9 sites", len(files), total)
	}
}
