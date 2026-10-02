// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
)

func TestDoctorBackgroundJobsGivesEachApplicableRemedy(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		for _, tc := range []struct {
			name      string
			reasons   []string
			wants     []string
			inventory bool
		}{
			{"license", []string{api.JobReasonAddonRequiresLicense}, []string{"license install", "enterprise enable"}, false},
			{"directory", []string{api.JobReasonDirectoryUnavailable}, []string{"ldap check", "ldap sync"}, false},
			{"inventory", []string{api.JobReasonNoTenantInventory}, []string{"db init", "deploy/postgres/README.md"}, true},
			{"combined", []string{api.JobReasonNoTenantInventory, api.JobReasonAddonRequiresLicense, api.JobReasonDirectoryUnavailable, api.JobReasonAddonRequiresLicense}, []string{"db init", "license install", "enterprise enable", "ldap check", "ldap sync"}, true},
		} {
			t.Run(engine+"/"+tc.name, func(t *testing.T) {
				var jobs []api.JobNotRunning
				for _, reason := range tc.reasons {
					jobs = append(jobs, api.JobNotRunning{Job: api.JobDirectorySynchronization, Reason: reason})
				}
				body, err := json.Marshal(map[string]any{"engine": engine, "jobs_not_running": jobs})
				if err != nil {
					t.Fatal(err)
				}
				deps := doctorDeps{httpGet: func(context.Context, string, string, time.Duration) (int, []byte, error) {
					return http.StatusOK, body, nil
				}}
				got := doctorBackgroundJobsCheck(context.Background(), deps, doctorOptions{server: "https://engine.test", timeout: time.Second}, "")
				if got.Required || got.Status != "warn" {
					t.Fatalf("optional job refusal=%+v", got)
				}
				for _, want := range tc.wants {
					if strings.Count(got.Remediation, want) != 1 {
						t.Fatalf("remedy must name %q once: %s", want, got.Remediation)
					}
				}
				if !tc.inventory && strings.Contains(got.Remediation, "postgres") {
					t.Fatalf("directory/license refusal prescribed PostgreSQL repair: %s", got.Remediation)
				}
				if tc.name == "directory" || tc.name == "combined" {
					if !strings.Contains(got.Remediation, "ldap detail") || !strings.Contains(got.Remediation, "--generation <observed staged generation>") || strings.Count(got.Remediation, "--reason <audit reason>") != 2 {
						t.Fatalf("directory remedy omitted required observed generation or audit reasons: %s", got.Remediation)
					}
				}
			})
		}
	}
}
