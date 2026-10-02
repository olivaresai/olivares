// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

func TestCodexNativeSandboxProbeKeepsOrRefusesBeforeFallback(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux native sandbox")
	}
	for _, tc := range []struct {
		name                       string
		fixture                    codexFixture
		mode                       string
		confined, refuse, fallback bool
	}{
		{"native available", codexFixture{}, "default", false, false, false},
		{"native unavailable without OS policy", codexFixture{SandboxExit: 1}, "default", false, true, false},
		{"interrupted probe", codexFixture{SandboxSignal: true}, "default", true, true, false},
		{"read-only never widens", codexFixture{SandboxExit: 1}, "plan", true, true, false},
		{"fallback requires OS confinement", codexFixture{SandboxExit: 1}, "default", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.confined && confine.Probe().Mode != confine.ModeLandlock {
				t.Skip("Landlock unavailable")
			}
			m, _, _, profile := codexHarness(t, AuthSourceAccountHome)
			setCodexFixture(t, profile, tc.fixture)
			p := CreateRunParams{PermissionMode: tc.mode, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverCodex}}
			spec := LaunchSpec{Program: os.Args[0], Dir: t.TempDir(), Env: []EnvVar{{Name: envCodexHome, Value: profile.ConfigHome}, {Name: envUserHome, Value: profile.UserHome}}}
			if tc.confined {
				spec.Confinement = &confine.Policy{ReadWrite: []string{spec.Dir, profile.ConfigHome, profile.UserHome}, ReadOnly: []string{os.Args[0]}}
			}
			err := m.prepareCodexSandbox(t.Context(), &p, &spec)
			if (err != nil) != tc.refuse || p.codexSandboxFallback != tc.fallback {
				t.Fatalf("error=%v fallback=%v", err, p.codexSandboxFallback)
			}
			if tc.fallback && (!spec.ConfinementRequired || !strings.Contains(strings.Join(spec.Args, " "), "danger-full-access")) {
				t.Fatal("fallback lost required confinement or native setting")
			}
		})
	}
}

func TestProviderPresetRetainsConfiguredCodexGranularControls(t *testing.T) {
	granular := &CodexGranularApproval{MCPElicitations: true, Rules: true, SandboxApproval: true, RequestPermissions: true, SkillApproval: true}
	d := codexDriver{policy: CodexPolicy{Approval: CodexApprovalPolicy{Granular: granular}}}
	for _, preset := range []string{PresetReadOnly, PresetAsk, PresetEditsOnly, PresetEditsAndCommands, PresetFull} {
		policy := d.launchPolicy(preset, false)
		if policy.Approval.Granular != granular || policy.Approval.Mode != "" {
			t.Fatalf("%s discarded typed policy: %+v", preset, policy)
		}
	}
}

type presetMCPFailure struct{ t *testing.T }

func (f presetMCPFailure) ConfigureSessionMCP(ctx context.Context, _ model.TenantID, _, _ string, _ *LaunchSpec) (func(), error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > sessionLaunchSetupTimeout {
		f.t.Error("MCP setup has no bounded deadline")
	}
	return nil, errors.New("fixture-secret-from-lower-trust-setup")
}

func TestProviderLaunchSetupFailureNamesSafeStageAndIsBounded(t *testing.T) {
	m := New()
	m.UseSessionMCPLaunchSource(presetMCPFailure{t})
	p, spec := CreateRunParams{}, LaunchSpec{}
	stage, err := m.prepareSessionLaunch(t.Context(), t.Context(), model.TenantID("fixture"), "run", &p, &spec)
	refusal := launchFailedErr(stage, err).Error()
	if !strings.Contains(refusal, "session MCP setup") || strings.Contains(refusal, "fixture-secret") {
		t.Fatalf("unsafe or unhelpful refusal: %s", refusal)
	}
}
