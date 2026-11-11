// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/license"
)

// remedyEdition reads the edition a remedy sends a person to: «Available in Olivares Business: <url>».
var remedyEdition = regexp.MustCompile(`^Available in Olivares (\w+):`)

// TestEveryEditionRefusalNamesItsEdition: one refusal shape. A code that sends a person to an
// edition (its remedy says «Available in Olivares Business») carries that edition in the body
// too, whichever of the two builders wrote it, so a client reads `edition` from every refusal and
// never parses a sentence. A code that is no edition refusal carries none.
func TestEveryEditionRefusalNamesItsEdition(t *testing.T) {
	named := 0
	for code, remedy := range errorRemedy {
		want := ""
		if m := remedyEdition.FindStringSubmatch(remedy); m != nil {
			want = strings.ToLower(m[1])
		}
		if code == "addon_requires_license" {
			want = license.EditionBusiness // an add-on is held by Business and above
		}
		if got := errorEdition[code]; got != want {
			t.Errorf("errorEdition[%q] = %q, want %q (remedy %q)", code, got, want, remedy)
		}
		if want == "" {
			continue
		}
		named++
		built, _ := ErrorBody(code, "message")["error"].(map[string]string)
		if built["edition"] != want {
			t.Errorf("ErrorBody(%q) carries edition %q, want %q", code, built["edition"], want)
		}
		var typed errorBody
		typed.Error.Code = code
		raw, err := json.Marshal(typed)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Error struct{ Edition string } `json:"error"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil || wire.Error.Edition != want {
			t.Errorf("errorBody{%q} wrote %s, want edition %q", code, raw, want)
		}
	}
	if named < 5 {
		t.Fatalf("only %d codes name an edition; the remedy table was not read", named)
	}
	for code := range errorEdition {
		if _, ok := errorRemedy[code]; !ok {
			t.Errorf("errorEdition lists %q, which has no row in errorRemedy", code)
		}
	}
	if built, _ := ErrorBody("forbidden", "no")["error"].(map[string]string); built["edition"] != "" {
		t.Errorf("a role refusal names edition %q", built["edition"])
	}
}

// TestRefusalSentencesAgree: «<Title> is part of Olivares AI <Edition>.» with a title that takes
// «is»: a plural title («Skills is part of») is the grammar slip this guards.
func TestRefusalSentencesAgree(t *testing.T) {
	checked := 0
	for namespace, spec := range placedModules {
		checked++
		err := EditionRouteRefusal(license.EditionCommunity, http.MethodPost, "/v1/m/"+namespace+"/x")
		var refusal *EditionRequiredError
		if !errors.As(err, &refusal) {
			t.Errorf("%s: POST in Community = %v, want an edition refusal", namespace, err)
			continue
		}
		want := spec.Title + " is part of Olivares AI " + editionNames[spec.Edition] + "."
		if refusal.Error() != want {
			t.Errorf("%s: sentence %q, want %q", namespace, refusal.Error(), want)
		}
		if strings.HasSuffix(strings.ToLower(spec.Title), "s") {
			t.Errorf("%s: title %q is plural and takes «are», not «is»: write a singular title", namespace, spec.Title)
		}
	}
	for _, row := range placedRoutes {
		checked++
		if strings.HasSuffix(strings.ToLower(row.title), "s") {
			t.Errorf("%s: title %q is plural and takes «are», not «is»: write a singular title", row.capability, row.title)
		}
	}
	if checked < 10 {
		t.Fatalf("only %d placed modules were checked", checked)
	}
}

// TestStoredDataCanBeRemovedBelowItsEdition: what a person stored below the edition that
// authors it stays readable, exportable and removable: GET, HEAD and DELETE answer, POST and
// PUT are refused. A module that stores nothing (reads_below false) refuses them all.
func TestStoredDataCanBeRemovedBelowItsEdition(t *testing.T) {
	refused := func(method, pattern string) bool {
		return EditionRouteRefusal(license.EditionCommunity, method, pattern) != nil
	}
	for method, want := range map[string]bool{
		http.MethodGet: false, http.MethodHead: false, http.MethodDelete: false,
		http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true,
	} {
		if got := refused(method, "/v1/m/knowledge/kbs/{id}"); got != want {
			t.Errorf("%s of a stored knowledge base in Community refused = %v, want %v", method, got, want)
		}
	}
	if !refused(http.MethodGet, "/v1/m/accessmap/graph") {
		t.Error("the access map stores nothing: its page stops, GET included")
	}
	// A stored identity provider can be removed in Community; a new one cannot be written.
	for method, want := range map[string]bool{
		http.MethodGet: false, http.MethodDelete: false, http.MethodPut: true, http.MethodPost: true,
	} {
		if got := refused(method, "/v1/console/sso/idps/{alias}"); got != want {
			t.Errorf("%s of an identity provider in Community refused = %v, want %v", method, got, want)
		}
	}
}
