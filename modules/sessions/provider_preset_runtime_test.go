// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

func TestProviderSessionPresetsPersistThroughNativeLaunch(t *testing.T) {
	for _, driver := range []string{providerDriverCodex, providerDriverGrok, providerDriverOpenCode} {
		for _, mode := range []string{"plan", "default", "acceptEdits"} {
			t.Run(driver+"/"+mode, func(t *testing.T) {
				harness := func(confined bool) (*Module, model.TenantID, ProviderProfile) {
					var m *Module
					var tenant model.TenantID
					var profile ProviderProfile
					switch driver {
					case providerDriverCodex:
						m, _, tenant, profile = codexHarness(t, AuthSourceAccountHome)
						setCodexFixture(t, profile, codexFixture{ThreadID: "preset-thread", Account: "apikey"})
					case providerDriverGrok:
						m, _, tenant, profile = grokHarness(t, AuthSourceAccountHome)
						setGrokFixture(t, profile, grokFixture{SessionID: "preset-grok"})
					case providerDriverOpenCode:
						m, _, tenant, profile = openCodeHarness(t, AuthSourceAccountHome)
						setOpenCodeFixture(t, profile, openCodeFixture{SessionID: "preset-opencode"})
					}
					if confined {
						WithConfinement([]string{t.TempDir()}, false)(m)
					}
					return m, tenant, profile
				}
				params := func(profile ProviderProfile) CreateRunParams {
					return CreateRunParams{
						Actor: "user:u1", ActorKind: model.ActorUser,
						Transport: TransportStreamJSON, Isolation: IsolationNative,
						ProviderProfileRef: profile.Ref, PermissionMode: mode,
					}
				}
				if mode == "plan" {
					// A read-only launch needs enforceable confinement: on a node with
					// none wired it is refused before anything starts (SR2 report 193,
					// "including absent policy"), and it starts where it is confined.
					m, tenant, profile := harness(false)
					if _, err := m.createRun(t.Context(), tenant, params(profile)); err == nil || !strings.Contains(err.Error(), "was not started") {
						t.Fatalf("read-only launch on a node with no confinement = %v, want a refusal before start", err)
					}
					if confine.Probe().Mode != confine.ModeLandlock {
						return
					}
				}
				m, tenant, profile := harness(mode == "plan")
				run, err := m.createRun(t.Context(), tenant, params(profile))
				if err != nil {
					t.Fatalf("%s preset could not start: %v", mode, err)
				}
				defer func() {
					if _, err := m.stopRun(t.Context(), tenant, run.RunRef, "user:u1", model.ActorUser); err != nil {
						t.Error(err)
					}
				}()
				stored, err := m.getRun(t.Context(), tenant, run.RunRef)
				if err != nil || run.PermissionMode != mode || stored.PermissionMode != mode {
					t.Fatalf("chosen native mode was not preserved: create=%q get=%q error=%v", run.PermissionMode, stored.PermissionMode, err)
				}
			})
		}
	}
}
