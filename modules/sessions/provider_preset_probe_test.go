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
	"github.com/olivaresai/olivares/modules/sessions/egress"
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
		network                    bool
	}{
		{"native available", codexFixture{}, "default", false, false, false, false},
		{"native available under OS confinement", codexFixture{}, "default", true, false, false, false},
		{"native unavailable without OS policy", codexFixture{SandboxExit: 1}, "default", false, true, false, false},
		{"interrupted probe", codexFixture{SandboxSignal: true}, "default", true, true, false, false},
		{"read-only never widens", codexFixture{SandboxExit: 1}, "plan", true, true, false, false},
		{"fallback requires OS confinement", codexFixture{SandboxExit: 1}, "default", true, false, true, false},
		// A record-bound launch probes under its own network boundary, within the
		// probe's time limit.
		{"fallback under the network boundary", codexFixture{SandboxExit: 1}, "default", true, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.confined && confine.Probe().Mode != confine.ModeLandlock {
				t.Skip("Landlock unavailable")
			}
			if tc.network && os.Getenv("OLIVARES_TEST_EGRESS_ENFORCE") != "1" {
				t.Skip("OLIVARES_TEST_EGRESS_ENFORCE=1 is not set: the host must allow user and network namespaces")
			}
			m, _, _, profile := codexHarness(t, AuthSourceAccountHome)
			setCodexFixture(t, profile, tc.fixture)
			p := CreateRunParams{PermissionMode: tc.mode, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverCodex}}
			spec := LaunchSpec{Program: os.Args[0], Dir: t.TempDir(), Env: []EnvVar{{Name: envCodexHome, Value: profile.ConfigHome}, {Name: envUserHome, Value: profile.UserHome}}}
			if tc.confined {
				spec.Confinement = &confine.Policy{ReadWrite: []string{spec.Dir, profile.ConfigHome, profile.UserHome}, ReadOnly: []string{os.Args[0]}}
			}
			if tc.network {
				spec.NetworkPolicy = &egress.Policy{Providers: []string{"https://127.0.0.1:44399/v1"}} // no DNS lookup, no privileged port
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
	m.SessionMCP = presetMCPFailure{t}
	p, spec := CreateRunParams{ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, LaunchSpec{}
	stage, err := m.prepareSessionLaunch(t.Context(), t.Context(), model.TenantID("fixture"), "run", &p, &spec)
	if err == nil {
		t.Fatal("a session MCP setup failure did not refuse the launch")
	}
	refusal := launchFailedErr(stage, err).Error()
	if !strings.Contains(refusal, "session MCP setup") || strings.Contains(refusal, "fixture-secret") {
		t.Fatalf("unsafe or unhelpful refusal: %s", refusal)
	}
}
