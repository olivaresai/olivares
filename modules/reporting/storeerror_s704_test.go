// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coreapi "github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// refusingEngine is the enterprise report engine as the closed overlay builds it
// when the entitlement has lapsed: constructed behind an add-on gate, so every
// method refuses with license.ErrAddonRequiresLicense.
//
// THIS IS THE ONLY WAY TO SHOW THE DEFECT FROM THE OPEN TREE, and saying so is part
// of the test rather than a note somewhere else. license.AddonRequired
// (core/license/entitlement.go:61) has no production caller in this clone — the
// addonGate that calls it lives in cmd-overlay, which is not here — so the refusal is
// injected at the seam the overlay fills (WithEnterpriseReports) instead of being
// provoked end to end.
type refusingEngine struct{ err error }

func (e refusingEngine) PostureReport(context.Context, model.TenantID) (any, error) {
	return nil, e.err
}
func (e refusingEngine) RiskSummary(context.Context, model.TenantID) (any, error) {
	return nil, e.err
}
func (e refusingEngine) EvidenceBundle(context.Context, model.TenantID) (any, error) {
	return nil, e.err
}
func (e refusingEngine) DueDigests(time.Time, map[string]time.Time) []DigestDue { return nil }

// A LAPSED ADD-ON IS 403, NOT 500. Before 2026-08-12 all three enterprise report
// routes answered 500 "failed to build the …" for this, which tells a customer who
// has paid for everything except this add-on that the server is broken.
func TestEnterpriseReportRoutesAnswerAnAddonRefusal403(t *testing.T) {
	m := &Module{log: slog.New(slog.DiscardHandler),
		enterprise: refusingEngine{err: license.AddonRequired("reporting", "reporting.posture")}}
	for _, tc := range []struct {
		name  string
		serve func(http.ResponseWriter, *http.Request)
	}{
		{"posture", func(w http.ResponseWriter, r *http.Request) { m.handleEnterprisePosture(w, r, coreapi.ModuleContext{}) }},
		{"risk", func(w http.ResponseWriter, r *http.Request) { m.handleEnterpriseRisk(w, r, coreapi.ModuleContext{}) }},
		{"bundle", func(w http.ResponseWriter, r *http.Request) { m.handleEnterpriseBundle(w, r, coreapi.ModuleContext{}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.serve(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
			}
			msg := reportingMessage(t, rec)
			// It must name the add-on, or the operator cannot tell which entitlement
			// lapsed, and it must say the refusal is not about reading their own data.
			for _, want := range []string{"reporting", "exporting your data are unaffected"} {
				if !strings.Contains(msg, want) {
					t.Errorf("message %q does not mention %q", msg, want)
				}
			}
		})
	}
}

// THE NON-FIRING DIRECTION on the same route. A real engine fault is still a 500 with
// the route's own sentence — the arm above must not have swallowed everything.
func TestEnterpriseReportRoutesStillCallARealFaultAFault(t *testing.T) {
	m := &Module{log: slog.New(slog.DiscardHandler),
		enterprise: refusingEngine{err: fmt.Errorf("the evidence store caught fire")}}
	rec := httptest.NewRecorder()
	m.handleEnterprisePosture(rec, httptest.NewRequest(http.MethodGet, "/x", nil), coreapi.ModuleContext{})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	if got := reportingMessage(t, rec); got != "failed to build the posture report" {
		t.Fatalf("message = %q, want the route's own sentence", got)
	}
}

// A WITHDRAWN TENANT GETS THE SAME ANSWER COLD AND WARM. It did not: the warm-cache
// branch re-entered the guarded store and answered 423, while a cold request let the
// gather error fall into the generic arm and answered 500. Same tenant, same route,
// the answer decided by whether an entry happened to be in memory.
func TestReportingAnswersServiceWithdrawalTheSameWayOnEveryPath(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{"suspended", store.ErrTenantSuspended, http.StatusLocked, "tenant is not in service"},
		{"not in service", store.ErrTenantNotInService, http.StatusLocked, "tenant is not in service"},
		{"residency", store.ErrResidencyViolation, http.StatusForbidden, "tenant is not resident in this region"},
		// Reached the generic 500 on every path before the shared mapping.
		{"not leader", store.ErrNotLeader, http.StatusServiceUnavailable, "not leader"},
		{"not found", store.ErrNotFound, http.StatusNotFound, "not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Module{log: slog.New(slog.DiscardHandler)}
			rec := httptest.NewRecorder()
			m.reportErr(rec, fmt.Errorf("gather: %w", tc.err), "gather report data", "failed to gather report data")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := reportingMessage(t, rec); got != tc.wantMsg {
				t.Fatalf("message = %q, want %q", got, tc.wantMsg)
			}
		})
	}
}

// This module's envelope carries a code field, unlike the other module mappers.
// Delegating the mapping must not have changed its shape.
func TestReportingKeepsItsOwnEnvelopeShape(t *testing.T) {
	m := &Module{log: slog.New(slog.DiscardHandler)}
	rec := httptest.NewRecorder()
	m.reportErr(rec, store.ErrTenantSuspended, "x", "y")
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body %q is not the module envelope: %v", rec.Body.String(), err)
	}
	if env.Error.Code != http.StatusText(http.StatusLocked) {
		t.Fatalf("code = %q, want %q — the envelope's own code field must survive", env.Error.Code, http.StatusText(http.StatusLocked))
	}
}

func reportingMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body %q is not the module error envelope: %v", rec.Body.String(), err)
	}
	return env.Error.Message
}
