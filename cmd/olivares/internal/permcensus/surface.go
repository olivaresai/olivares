// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package permcensus

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"

	"github.com/olivaresai/olivares/core/api"
)

// Business reports the compiled edition. It starts true and the !enterprise half
// (surface_community.go) clears it in init, so the Business build needs no tagged
// file. Read it at run time, never in a package-level initializer.
var Business = true

// editionRouteSurfaces starts as the Business table: these routes are live there.
// Community replaces them with its refusals in surface_community.go.
var editionRouteSurfaces = map[string]string{
	// Seat denominators are ingested by the enterprise collector over HTTP.
	"finops POST /seats": "api-only",
	// The Business bridge consumes grants; the console grants and revokes them.
	"governance POST /breakglass/consume": "api-only",
}

// Surface describes a route's console obligation, not its authorization. Every
// route still contributes its permission and the engine's unchanged role grants.
// Unknown routes require a console surface. Only explicit edition seams and the
// retained portability/ingest contracts below are exempt from that obligation.
func routeSurface(namespace, method, pattern string, h api.ModuleHandler) string {
	surface := editionRouteSurfaces[namespace+" "+method+" "+pattern]
	// Cost ingest is a provider/API producer in both editions, not a console act.
	if namespace == "finops" && method == "POST" && pattern == "/cost" {
		surface = "api-only"
	}
	// This exact compiled handler is the no-IDS seam, including base Business.
	if h != nil && runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name() ==
		"github.com/olivaresai/olivares/modules/orchestration.unavailable" {
		surface = "edition-refusal"
	}
	if surface == "" {
		return "console"
	}
	if surface == "edition-refusal" {
		// Execute the registered seam. A renamed, newly live or broken handler must
		// stop inventory production rather than silently hide a real orphan route.
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(method, pattern, nil), api.ModuleContext{})
		if w.Code != http.StatusNotImplemented {
			panic(fmt.Sprintf("permission census: %s %s %s edition refusal returned %d, want 501", namespace, method, pattern, w.Code))
		}
	}
	return surface
}
