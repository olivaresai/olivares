// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package reporting

import (
	"encoding/json"
	"errors"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
	"net/http"
)

var (
	permReportRead  auth.Permission = "reporting:report:read"
	permReportWrite auth.Permission = "reporting:report:write"
)

func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/reports", permReportRead, m.handleListReports)
	reg.Handle("GET", "/reports/{type}", permReportRead, m.handleGenerateReport)
	// the enterprise report engine + schedules/branding/templates.
	// Every route answers 501 until its seam is wired (enterprise build).
	m.registerEnterpriseRoutes(reg)
	m.registerSigningRoutes(reg)
}

func validReportType(rt ReportType) bool {
	switch rt {
	case ReportComplianceEvidence, ReportAuditSummary, ReportFinOps, ReportAccessReview, ReportExecutiveSummary:
		return true
	}
	return false
}

// writeResponse retains this route family's media type, nil and cache policy.
func writeResponse(w http.ResponseWriter, status int, v any) {
	if v == nil {
		v = json.RawMessage("null")
	}
	api.WriteJSON(w, status, v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeResponse(w, status, map[string]any{"error": map[string]string{"code": http.StatusText(status), "message": msg}})
}

// writeStoreError is this module's member of the product-wide error-mapper family,
// and it did not exist until 2026-08-12. Reporting classified inline, in one handler,
// and every other error path answered 500 — which had two measured consequences.
//
// THE ONE THAT REACHES A PAYING CUSTOMER. The enterprise report engine is wired
// behind an add-on gate in the closed overlay, so a lapsed entitlement returns
// license.ErrAddonRequiresLicense out of m.enterprise.*, and serveEnterpriseReport
// (enterprise.go) answered it 500 "failed to build the posture report". The operator
// is told their server is broken when in fact their license lapsed — the exact defect
// the addon_requires_license arm of core/api statusFor was written to prevent, one
// layer out. Nothing in the open tree constructs that error, so it cannot be shown
// end to end here; it is proven against the constructor in the tests beside this file.
//
// THE ONE VISIBLE FROM THE OPEN TREE. handleGenerateReport answered a withdrawn
// tenant 423 on a WARM cache (the service-state re-check above) and 500 on a COLD
// one, because the cold path let gatherData's error fall into the generic arm. Same
// tenant, same route, two answers, decided by cache warmth.
//
// The two local arms keep the wording reporting already put on the wire — the shared
// mapping agrees on both statuses and says "tenant suspended" / "residency violation"
// where these say something an operator can act on. Centralizing a mapping is not
// license to reword a response nothing in the tree tests.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrTenantSuspended), errors.Is(err, store.ErrTenantNotInService):
		writeError(w, http.StatusLocked, "tenant is not in service")
	case errors.Is(err, store.ErrResidencyViolation):
		writeError(w, http.StatusForbidden, "tenant is not resident in this region")
	default:
		status, body, _ := api.StoreErrorBody(err)
		writeResponse(w, status, body)
	}
}

// reportErr answers err and decides, in ONE place, whether it was a fault worth a log
// line. A deliberate refusal must not be logged at ERROR next to real faults: that is
// how operators learn to ignore the log, and core/api writeError already draws the
// same line for the same reason. A genuine fault keeps its event and its call-site
// sentence, because "failed to render report" and "failed to build the risk summary"
// are not interchangeable.
func (m *Module) reportErr(w http.ResponseWriter, err error, event, fallback string) {
	if _, _, refused := api.StoreErrorStatus(err); refused {
		writeStoreError(w, err)
		return
	}
	m.log.Error(event, "err", err)
	writeError(w, http.StatusInternalServerError, fallback)
}

func writeReport(w http.ResponseWriter, data []byte, format Format) {
	switch format {
	case FormatPDF:
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", "attachment; filename=report.pdf")
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	w.WriteHeader(http.StatusOK)
	// The status line is already on the wire, so a write failure here cannot change
	// the response; discarded explicitly rather than left to be read as an oversight.
	_, _ = w.Write(data)
}
