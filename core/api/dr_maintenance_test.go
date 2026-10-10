// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRestoreMaintenanceNeverAdvisesRestartDuringKeyRecovery(t *testing.T) {
	svc := newDRService(DRConfig{})
	job := svc.jobs.create(drJobRestore, "test")
	svc.jobs.update(job.ID, func(j *drJob) {
		j.Status = drJobFailed
		j.Phase = "restoring_store"
		j.Error = "write key audit-signing.key: denied; the restored database is already in place, so do not restart until all custody keys from the bundle are restored, including keys not yet attempted"
	})
	svc.beginMaintenance(job.ID)
	s := &Server{drSvc: svc}
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	s.restoreMaintenance(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("maintenance permitted serving") })).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(strings.ToLower(rec.Body.String()), "do not restart") || !strings.Contains(rec.Body.String(), "all custody keys") {
		t.Fatalf("unsafe key-recovery guidance: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRestoreMaintenanceReceiptIsBoundToItsJobAndCredential(t *testing.T) {
	for _, cookie := range []bool{false, true} {
		name := "bearer"
		if cookie {
			name = "cookie"
		}
		t.Run(name, func(t *testing.T) {
			svc := newDRService(DRConfig{})
			s := &Server{drSvc: svc}
			job := svc.jobs.create(drJobRestore, "test")
			applied := httptest.NewRequest(http.MethodPost, "https://example.test/v1/console/dr/restore/test/apply", nil)
			applied.Header.Set("Authorization", "Bearer receipt-test-credential")
			s.rememberRestoreReceipt(job.ID, applied, "user:test")
			svc.jobs.update(job.ID, func(j *drJob) { j.Status = drJobCompleted; j.Phase = "restart_required" })
			svc.beginMaintenance(job.ID)
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("closed store was reached") })
			for _, tc := range []struct {
				name, path, credential string
				code                   int
			}{
				{"list", "/v1/console/dr/jobs", "receipt-test-credential", 200},
				{"late stream", "/v1/console/dr/jobs/" + job.ID + "/stream", "receipt-test-credential", 200},
				{"unrelated job", "/v1/console/dr/jobs/other/stream", "receipt-test-credential", 503},
				{"unrelated credential", "/v1/console/dr/jobs", "other-credential", 503},
				{"anonymous", "/v1/console/dr/jobs", "", 503},
				{"write", "/v1/auth/logout", "receipt-test-credential", 503},
			} {
				t.Run(tc.name, func(t *testing.T) {
					req := httptest.NewRequest(http.MethodGet, "https://example.test"+tc.path, nil)
					if cookie {
						req.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: tc.credential})
						req.Header.Set("X-Olivares-Session", "cookie")
					} else if tc.credential != "" {
						req.Header.Set("Authorization", "Bearer "+tc.credential)
					}
					if tc.name == "write" {
						req.Method = http.MethodPost
						if cookie {
							req.Header.Set("X-CSRF-Token", browserCSRF(tc.credential))
						}
					}
					rec := httptest.NewRecorder()
					s.restoreMaintenance(next).ServeHTTP(rec, req)
					if rec.Code != tc.code {
						t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
					}
					if tc.code == 200 && rec.Header().Get("Cache-Control") != "no-store" {
						t.Fatal("receipt is cacheable")
					}
					if tc.name == "list" {
						var got struct {
							Items []map[string]any `json:"items"`
						}
						if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
							t.Fatal(err)
						}
						if len(got.Items) != 1 || got.Items[0]["id"] != job.ID {
							t.Fatalf("unexpected jobs: %v", got.Items)
						}
						if strings.Contains(rec.Body.String(), "receipt") {
							t.Fatal("receipt credential data was serialized")
						}
					}
				})
			}
			for _, tc := range []struct {
				name, header, value string
				code                int
			}{
				{"foreign origin", "Origin", "https://attacker.test", 403},
				{"cross-site fetch", "Sec-Fetch-Site", "cross-site", 403},
				{"malformed bearer with valid cookie", "Authorization", "Basic ignored", 401},
				{"wrong bearer with valid cookie", "Authorization", "Bearer wrong", 503},
			} {
				t.Run(tc.name, func(t *testing.T) {
					req := httptest.NewRequest(http.MethodGet, "https://example.test/v1/console/dr/jobs", nil)
					req.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: "receipt-test-credential"})
					req.Header.Set("X-Olivares-Session", "cookie")
					req.Header.Set(tc.header, tc.value)
					rec := httptest.NewRecorder()
					s.restoreMaintenance(next).ServeHTTP(rec, req)
					if rec.Code != tc.code {
						t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
					}
				})
			}
			svc.jobs.update(job.ID, func(j *drJob) { j.receiptUntil = time.Now().Add(-time.Second) })
			expired := applied.Clone(applied.Context())
			expired.Method = http.MethodGet
			expired.URL.Path = "/v1/console/dr/jobs"
			rec := httptest.NewRecorder()
			s.restoreMaintenance(next).ServeHTTP(rec, expired)
			if rec.Code != 503 {
				t.Fatal("expired receipt allowed a request")
			}
		})
	}
}

func TestRestoreMaintenanceAdviceFollowsTheRestoreOutcome(t *testing.T) {
	for _, tc := range []struct {
		status string
		safe   bool
		want   string
	}{
		{drJobRunning, false, "Wait for its result"},
		{drJobCompleted, false, "Restart the engine"},
		{drJobFailed, true, "Restart the engine"},
		{drJobFailed, false, "Do not restart"},
	} {
		t.Run(tc.status+tc.want, func(t *testing.T) {
			svc := newDRService(DRConfig{})
			job := svc.jobs.create(drJobRestore, "test")
			svc.jobs.update(job.ID, func(j *drJob) { j.Status = tc.status; j.restartSafe = tc.safe })
			svc.beginMaintenance(job.ID)
			s := &Server{drSvc: svc}
			rec := httptest.NewRecorder()
			s.restoreMaintenance(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("served while stopped") })).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if rec.Code != 503 || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("guidance: %s", rec.Body.String())
			}
		})
	}
}

func TestRestoreMaintenancePreservesLivenessWithoutReadiness(t *testing.T) {
	for _, status := range []string{drJobRunning, drJobCompleted, drJobFailed} {
		t.Run(status, func(t *testing.T) {
			svc := newDRService(DRConfig{})
			job := svc.jobs.create(drJobRestore, "test")
			svc.jobs.update(job.ID, func(j *drJob) { j.Status = status })
			svc.beginMaintenance(job.ID)
			s := &Server{drSvc: svc}
			for _, path := range []string{"/livez", "/healthz", "/readyz", "/pod-readyz"} {
				rec := httptest.NewRecorder()
				s.restoreMaintenance(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("stopped store was reached") })).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
				want := 503
				if path == "/livez" || path == "/healthz" {
					want = 200
				}
				if rec.Code != want {
					t.Fatalf("%s status=%d, want %d", path, rec.Code, want)
				}
			}
		})
	}
}
