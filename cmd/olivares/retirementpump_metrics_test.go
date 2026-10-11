// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// A missing declared step makes every retirement fail. Operators must see each
// failed record pass through the composition's real metrics endpoint, including
// retries; an idle pass and a repaired composition must not count as failures.
func TestRetirementPumpFailuresReachMetrics(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		server := httptest.NewServer(e.h)
		defer server.Close()
		wantFailures := func(cause string, count int) {
			t.Helper()
			resp, err := server.Client().Get(server.URL + "/metrics")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("olivares_retirement_failures_total{cause=%q} %d\n", cause, count)
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
				t.Fatalf("metrics status=%d; missing %q", resp.StatusCode, want)
			}
		}
		users := []string{"retirement-metrics-a@consent.test", "retirement-metrics-b@consent.test"}
		var ids []model.ID
		for _, email := range users {
			user := e.onboard(e.tT, email, "viewer")
			ids = append(ids, user)
			e.scimDelete(e.tT, user)
		}
		p := e.pump()
		complete := p.modules
		var partial []declaredModule
		for _, d := range complete {
			if d.name != "eventing" {
				partial = append(partial, d)
			}
		}
		p.modules = partial
		for attempt := 1; attempt <= 2; attempt++ {
			advancePumpClock(p)
			if advanced, err := p.runOnce(context.Background()); err != nil || advanced != 0 {
				t.Fatalf("failed batch advanced=%d err=%v", advanced, err)
			}
			for _, id := range ids {
				rec, found := e.record(id, e.tT)
				if !found || rec.RetirementState != model.RetirementRetiring || !strings.Contains(rec.ModuleResults, "census_incomplete:eventing") {
					t.Fatalf("missing eventing step did not produce the composition defect: %+v", rec)
				}
			}
			wantFailures("record_pass_failed", 2*attempt)
		}
		p.modules = complete
		e.runPump()
		wantFailures("record_pass_failed", 4)
		for _, email := range users {
			if r := e.readmit(e.tT, email, "viewer"); r.code != http.StatusCreated {
				t.Fatalf("repaired retirement readmission=%d %s", r.code, r.raw)
			}
		}
		wantFailures("no_declared_modules", 0)
		wantFailures("read_due_failed", 0)
		p.modules = nil
		if _, err := p.runOnce(context.Background()); err != errNoDeclaredModules {
			t.Fatalf("empty composition error=%v", err)
		}
		wantFailures("no_declared_modules", 1)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _ = p.runOnce(ctx)
		wantFailures("no_declared_modules", 1)
		p.modules = complete
		if err := e.eng.store.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := p.runOnce(context.Background()); err == nil {
			t.Fatal("closed store allowed the pump to read due records")
		}
		wantFailures("read_due_failed", 1)
		_, _ = p.runOnce(ctx)
		wantFailures("read_due_failed", 1)
	})
}
