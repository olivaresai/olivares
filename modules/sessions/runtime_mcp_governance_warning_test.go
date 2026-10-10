// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// Agent peers are protocol fixtures, not authenticated vendor turns. The API
// reports native MCP limits without refusing an otherwise supported session.
func TestRuntimeRunReportsMCPGovernanceByDriver(t *testing.T) {
	for _, driver := range []string{"", providerDriverClaude, providerDriverCodex, providerDriverGrok, providerDriverOpenCode} {
		name := driver
		if name == "" {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			opts := []Option{WithRunner(&fakeRunner{initSID: "warning-claude"}), WithCredentialSource(staticCred())}
			switch driver {
			case providerDriverGrok:
				opts = grokRuntimeOptions()
			case providerDriverOpenCode:
				opts = openCodeRuntimeOptions()
			case providerDriverCodex:
				opts = []Option{
					WithRunner(NewProcRunner()), WithProviderDriver(NewCodexDriver()),
					WithDriverProgram(providerDriverCodex, os.Args[0]), WithProductVersion("test"),
					WithStopWaitDelay(2 * time.Second), WithDriverTimeouts(20*time.Second, 2*time.Second),
				}
			}
			m := New(append(opts, WithSessionWorkspaceRoot(t.TempDir()))...)
			m.UseExecutionEnvironmentRef(testEnvRef)
			h := newHarness(t, m)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "mcp-warning")
			launchDriver := driver
			if launchDriver == "" {
				launchDriver = providerDriverClaude
			}
			prof := mustCreateProfile(t, m, tenant, CreateProfileInput{
				Driver: launchDriver, ConfigHome: t.TempDir(), UserHome: t.TempDir(),
				DisplayName: launchDriver + "-warning", AuthSource: AuthSourceAccountHome,
			})
			switch driver {
			case providerDriverGrok:
				setGrokFixture(t, prof, grokFixture{SessionID: "warning-grok"})
			case providerDriverOpenCode:
				setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "warning-opencode", ResumeEmptyObject: true})
			case providerDriverCodex:
				setCodexFixture(t, prof, codexFixture{ThreadID: "warning-codex", Account: "apikey"})
			}
			created := h.doJSON(http.MethodPost, "/v1/m/sessions/runs", admin, map[string]any{
				"transport": "stream-json", "permission_mode": "default", "isolation": "native",
				"provider_profile_ref": prof.Ref,
			}, tenantHdr(tenant))
			runRef, _ := created.body["run_ref"].(string)
			if created.code != http.StatusCreated || runRef == "" {
				t.Fatalf("driver launch = %d %s; must remain usable", created.code, created.raw)
			}
			t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, runRef, "user:u1", model.ActorUser) })
			if driver == "" {
				waitFor(t, "legacy fixture init", func() bool {
					run, err := m.getRun(context.Background(), tenant, runRef)
					return err == nil && run.ClaudeSessionID != ""
				})
				persistLegacyRunWithoutProfile(t, m, tenant, runRef)
				created = h.do(http.MethodGet, "/v1/m/sessions/runs/"+runRef, admin, tenantHdr(tenant))
				if created.code != http.StatusOK {
					t.Fatalf("legacy fixture read = %d %s", created.code, created.raw)
				}
			}
			assertWarning := func(body map[string]any) {
				t.Helper()
				if driver == providerDriverGrok || driver == providerDriverOpenCode {
					if body["mcp_governance_warning"] != "MCP servers configured in this tool's own settings are not governed by Olivares." {
						t.Errorf("run lacks the honest MCP limitation: %v", body["mcp_governance_warning"])
					}
				} else if _, exists := body["mcp_governance_warning"]; exists {
					t.Errorf("governed driver %s received an inapplicable MCP warning", driver)
				}
				gotDriver, _ := body["provider_driver"].(string)
				if gotDriver != driver {
					t.Errorf("driver = %v; want %s", body["provider_driver"], driver)
				}
			}
			assertWarning(created.body)
			runPath := "/v1/m/sessions/runs/" + runRef
			got := h.do(http.MethodGet, runPath, admin, tenantHdr(tenant))
			if got.code != http.StatusOK {
				t.Fatalf("run GET = %d %s", got.code, got.raw)
			}
			assertWarning(got.body)
			listed := h.do(http.MethodGet, "/v1/m/sessions/runs", admin, tenantHdr(tenant))
			items, _ := listed.body["items"].([]any)
			if listed.code != http.StatusOK || len(items) != 1 {
				t.Fatalf("runs GET = %d %s", listed.code, listed.raw)
			}
			assertWarning(items[0].(map[string]any))
			stopped := h.do(http.MethodPost, runPath+"/stop", admin, tenantHdr(tenant))
			if stopped.code != http.StatusOK {
				t.Fatalf("Stop = %d %s", stopped.code, stopped.raw)
			}
			resumed := h.do(http.MethodPost, runPath+"/resume", admin, tenantHdr(tenant))
			if driver == "" {
				if resumed.code != http.StatusConflict || launchCount(m.rt.Runner.(*fakeRunner)) != 1 {
					t.Fatalf("unproven legacy resume = %d %s", resumed.code, resumed.raw)
				}
				return
			}
			if resumed.code != http.StatusOK {
				t.Fatalf("resume = %d %s", resumed.code, resumed.raw)
			}
			assertWarning(resumed.body)
		})
	}
}
