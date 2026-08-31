// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/seed"
)

// This file proves the seams through the PRODUCTION wiring (buildModules →
// sessionsSampleAdapter / sessionsHistoryAdapter → the sessions module's read
// surface), over the estate the seed source emitted through the real bus. It is
// also the regression guard for the monitor's sample-OUTSIDE-tx property: the e2e
// store is single-connection SQLite (SetMaxOpenConns(1)), so a re-nested
// SessionSource.Sample inside the monitor's write transaction would block forever
// on pool acquisition — the bounded request context below turns that hang into a
// fast, explicit failure.

// TestE2E_S161_MonitorSamplesRealSessions drives POST /v1/m/evals/monitor through
// the production adapter and asserts the module-II signals score honestly:
// the live session is in flight (never a clean pass) and the silent-evasion
// session always fails.
func TestE2E_S161_MonitorSamplesRealSessions(t *testing.T) {
	h := newHarness(t)

	// A bounded context: a sample-inside-tx regression deadlocks on the single
	// SQLite connection; this fails the request in 30s instead of hanging go test.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"suite": "e2e-live"})
	r := httptest.NewRequest("POST", "/v1/m/evals/monitor", bytes.NewReader(body)).WithContext(ctx)
	r.RemoteAddr = "10.0.0.1:4321"
	r.Header.Set("Authorization", "Bearer "+h.adminToken)
	r.Header.Set("X-Olivares-Tenant", h.tenantA)
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("monitor = %d: %s", rec.Code, rec.Body.String())
	}

	var mon struct {
		Total   int `json:"total"`
		Samples []struct {
			SessionRef string  `json:"session_ref"`
			State      string  `json:"state"`
			Score      float64 `json:"score"`
			Passed     bool    `json:"passed"`
		} `json:"samples"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &mon); err != nil {
		t.Fatalf("decode monitor: %v", err)
	}
	if mon.Total < 2 {
		t.Fatalf("monitor total = %d, want >=2 (the seeded live + evade sessions)", mon.Total)
	}
	found := map[string]bool{}
	for _, s := range mon.Samples {
		switch s.SessionRef {
		case seed.SessionLive:
			found[s.SessionRef] = true
			// In flight (active/idle): never judged a clean pass.
			if s.State != "active" && s.State != "idle" {
				t.Errorf("live session state = %q, want active|idle", s.State)
			}
			if s.Passed {
				t.Errorf("live (in-flight) session scored as a pass")
			}
		case seed.SessionEvade:
			found[s.SessionRef] = true
			if s.State != "silent_evasion" {
				t.Errorf("evade session state = %q, want silent_evasion", s.State)
			}
			if s.Passed || s.Score != 0 {
				t.Errorf("silent_evasion scored %v/passed=%v, want 0.0/false (never a pass)", s.Score, s.Passed)
			}
		}
	}
	if !found[seed.SessionLive] || !found[seed.SessionEvade] {
		t.Fatalf("monitor missed seeded sessions (found %v of %d samples)", found, len(mon.Samples))
	}
}

// TestE2E_S161_ReplayFromSeededTimeline replays a seeded session through the
// production history adapter: ordered, zero-padded steps from the real module-II
// timeline, and an honestly DEGRADED replay for a session that never existed.
func TestE2E_S161_ReplayFromSeededTimeline(t *testing.T) {
	h := newHarness(t)

	var run struct {
		ID         string `json:"id"`
		Status     string `json:"status"`
		StepsTotal int    `json:"steps_total"`
	}
	if code := h.reqInto("POST", "/v1/m/sandbox/replay", h.adminToken, h.tenantA,
		map[string]any{"session_ref": seed.SessionLive}, &run); code != http.StatusCreated {
		t.Fatalf("replay = %d", code)
	}
	if run.Status != "completed" || run.StepsTotal < 1 {
		t.Fatalf("replay = %s/%d steps, want completed with the seeded actions", run.Status, run.StepsTotal)
	}
	outs := h.getJSON(h.adminToken, h.tenantA, "/v1/m/sandbox/runs/"+run.ID+"/outputs")
	outItems := items(outs)
	if len(outItems) != run.StepsTotal {
		t.Fatalf("outputs = %d, want %d", len(outItems), run.StepsTotal)
	}
	first, _ := outItems[0]["step_key"].(string)
	if !strings.HasPrefix(first, "00001 ") {
		t.Errorf("first step key = %q, want the zero-padded production format", first)
	}

	var ghost struct {
		Status     string `json:"status"`
		StepsTotal int    `json:"steps_total"`
	}
	if code := h.reqInto("POST", "/v1/m/sandbox/replay", h.adminToken, h.tenantA,
		map[string]any{"session_ref": "never-existed"}, &ghost); code != http.StatusCreated {
		t.Fatalf("ghost replay = %d", code)
	}
	if ghost.Status != "degraded" || ghost.StepsTotal != 0 {
		t.Errorf("ghost replay = %s/%d, want degraded/0 (honest, never fabricated)", ghost.Status, ghost.StepsTotal)
	}
}
