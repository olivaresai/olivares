// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/modulespec"
)

// EditionRequiredError refuses a route whose capability is placed in a higher edition than the
// running one. Capability is the stable key a client translates (a module namespace or a row
// of placedRoutes); the message names the edition for a person.
type EditionRequiredError struct {
	Edition, Capability, title string
}

func (e *EditionRequiredError) Error() string {
	return e.title + " is part of Olivares AI " + editionNames[e.Edition] + "."
}

var editionNames = map[string]string{
	license.EditionEngineer:   "Engineer",
	license.EditionBusiness:   "Business",
	license.EditionEnterprise: "Enterprise",
}

// placedRoutes places the routes that belong to no module (modules carry their own
// placement in modules.json). Methods lists the verbs placed; the others stay open.
var placedRoutes = []struct {
	prefix, methods, edition, capability, title string
}{
	// One OIDC or SAML identity provider: a stored one keeps signing people in, and an
	// administrator can still read or remove it.
	{"/v1/console/sso", "PUT POST", license.EditionBusiness, "sso", "Single sign-on"},
}

// EditionRouteRefusal answers the route method pattern (as the router registered it under
// /v1) at the running edition: nil when the edition holds it, else an *EditionRequiredError.
// A module placed with reads_below keeps GET and HEAD open, so stored data stays readable
// and exportable below its edition.
func EditionRouteRefusal(edition, method, pattern string) error {
	minimum, capability, title := routeMinimumEdition(method, pattern)
	if license.EditionCovers(edition, minimum) {
		return nil
	}
	return &EditionRequiredError{Edition: minimum, Capability: capability, title: title}
}

// placedModules are the modules.json rows with a title: the route gate places them. A row
// without one keeps the refusal its module answers itself.
var placedModules = func() map[string]modulespec.Spec {
	placed := map[string]modulespec.Spec{}
	for _, spec := range modulespec.All() {
		if spec.Title != "" {
			placed[spec.Namespace] = spec
		}
	}
	return placed
}()

func routeMinimumEdition(method, pattern string) (edition, capability, title string) {
	if rest, ok := strings.CutPrefix(pattern, "/v1/m/"); ok {
		namespace, _, _ := strings.Cut(rest, "/")
		spec, placed := placedModules[namespace]
		if !placed || spec.ReadsBelow && (method == http.MethodGet || method == http.MethodHead) {
			return license.EditionCommunity, "", ""
		}
		return spec.Edition, spec.Namespace, spec.Title
	}
	for _, r := range placedRoutes {
		if (pattern == r.prefix || strings.HasPrefix(pattern, r.prefix+"/")) && strings.Contains(" "+r.methods+" ", " "+method+" ") {
			return r.edition, r.capability, r.title
		}
	}
	return license.EditionCommunity, "", ""
}
