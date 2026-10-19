// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// stubUpdateService records what the HTTP layer asks of the engine's update service.
// The property under test is the route: who may call it, what it answers, and that an
// apply is never run without the administrative step-up. How a release is found and
// installed belongs to `olivares upgrade` and its tests.
type stubUpdateService struct {
	status  api.UpdateStatusDTO
	applyAs auth.Principal
	applied int
	err     error
}

func (s *stubUpdateService) UpdateStatus(context.Context) (api.UpdateStatusDTO, error) {
	return s.status, s.err
}

func (s *stubUpdateService) ApplyUpdate(_ context.Context, actor auth.Principal) (api.UpdateStatusDTO, error) {
	s.applied++
	s.applyAs = actor
	if s.err != nil {
		return api.UpdateStatusDTO{}, s.err
	}
	out := s.status
	out.Restarting = true
	return out, nil
}

func availableUpdate() api.UpdateStatusDTO {
	return api.UpdateStatusDTO{
		Edition: "community", Version: "0.2", Channel: "stable",
		Available: "0.3", UpdateAvailable: true, ReleasedAt: "2026-10-16T00:00:00Z",
		Notes: "Adds the Updates page.", Security: true, Advisories: []string{"CVE-2026-0001"},
		RunningSessions: 2,
	}
}

func TestUpdatesWithoutServiceAnswer501(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/console/updates"},
		{"POST", "/v1/console/updates/apply"},
	} {
		r := h.do(tc.method, tc.path, admin, nil, nil)
		if r.code != http.StatusNotImplemented {
			t.Fatalf("%s %s with no update service = %d %s, want 501 (deny closed, not a silent success or a 404)", tc.method, tc.path, r.code, r.raw)
		}
	}
}

func TestUpdatesStatusReportsTheCheckedRelease(t *testing.T) {
	svc := &stubUpdateService{status: availableUpdate()}
	h := newHarnessOpts(t, func(o *api.Options) { o.Updates = svc })
	r := h.do("GET", "/v1/console/updates", h.adminLogin(), nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET /v1/console/updates = %d %s", r.code, r.raw)
	}
	for field, want := range map[string]any{
		"version": "0.2", "available": "0.3", "update_available": true, "notes": "Adds the Updates page.",
		"channel": "stable", "edition": "community", "security": true, "running_sessions": float64(2),
	} {
		if got := r.body[field]; got != want {
			t.Errorf("%s = %v, want %v", field, got, want)
		}
	}
	if svc.applied != 0 {
		t.Fatalf("a status read applied %d updates", svc.applied)
	}
}

func TestUpdatesApplyRunsOnceAsTheSignedInAdminAndReportsRestart(t *testing.T) {
	svc := &stubUpdateService{status: availableUpdate()}
	h := newHarnessOpts(t, func(o *api.Options) { o.Updates = svc })
	r := h.do("POST", "/v1/console/updates/apply", h.adminLogin(), nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("POST /v1/console/updates/apply = %d %s", r.code, r.raw)
	}
	if got := r.body["restarting"]; got != true {
		t.Fatalf("restarting = %v, want true so the console waits for the engine to come back", got)
	}
	if svc.applied != 1 {
		t.Fatalf("apply ran %d times, want 1", svc.applied)
	}
	if svc.applyAs.UserID == "" {
		t.Fatal("apply was not told which administrator asked, so it cannot be audited")
	}
}

func TestUpdatesApplyRequiresStepUp(t *testing.T) {
	svc := &stubUpdateService{status: availableUpdate()}
	h := newHarnessOpts(t, func(o *api.Options) { o.Updates = svc })
	admin := h.adminLogin()
	h.requirePasskeyStepUp()
	r := h.do("POST", "/v1/console/updates/apply", admin, nil, nil)
	if r.code != http.StatusForbidden || errorCode(r) != "step_up_required" || svc.applied != 0 {
		t.Fatalf("apply without the administrative step-up = %d %s (ran %d times), want 403 step_up_required; replacing the engine binary is privilege-shaped", r.code, r.raw, svc.applied)
	}
	// The read carries no secret and needs no step-up.
	if r := h.do("GET", "/v1/console/updates", admin, nil, nil); r.code != http.StatusOK {
		t.Fatalf("status read under the strictest step-up policy = %d %s", r.code, r.raw)
	}
}

func TestUpdateErrorsKeepDistinctCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
		slug string
	}{
		{"unavailable", api.ErrUpdatesUnavailable, http.StatusNotImplemented, "updates_unavailable"},
		{"refused", fmt.Errorf("%w: the install directory is not writable", api.ErrUpdateRefused), http.StatusConflict, "update_refused"},
		{"installed but not restarted", fmt.Errorf("%w: platform", api.ErrUpdateRestartUnavailable), http.StatusServiceUnavailable, "update_restart_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, slug := api.StatusForTest(tc.err)
			if code != tc.code || slug != tc.slug {
				t.Fatalf("StatusForTest = %d %q, want %d %q", code, slug, tc.code, tc.slug)
			}
		})
	}
	if code, _ := api.StatusForTest(errors.New("an unrelated fault")); code != http.StatusInternalServerError {
		t.Fatalf("an unrelated error mapped to %d, want 500", code)
	}
}

// deadlineRecorder is a ResponseWriter whose write deadline the handler can move, as the
// real connection's is.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.deadline = t
	return nil
}

func TestUpdatesApplyOutlastsTheServersWriteTimeout(t *testing.T) {
	svc := &stubUpdateService{status: availableUpdate()}
	h := newHarnessOpts(t, func(o *api.Options) { o.Updates = svc })
	admin := h.adminLogin()
	req := httptest.NewRequest("POST", "/v1/console/updates/apply", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+admin)
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	h.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply = %d %s", rec.Code, rec.Body.String())
	}
	// The server's hardened WriteTimeout is 60s; an install that downloads a release must outlast it.
	if until := time.Until(rec.deadline); until < 5*time.Minute {
		t.Fatalf("write deadline is %v away, want the reply window to outlast a download", until)
	}
}
