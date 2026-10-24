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
	"testing/synctest"
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

// A credential broker binds its helpers to the first process OnStart reports. The
// probe is a copy of the session's spec, but it is not the session: reporting it
// would bind the helpers to a process that exits at once and leave the real child
// refused by every one of them.
func TestCredentials_TheCodexSandboxProbeDoesNotBindTheHelpers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux native sandbox")
	}
	m, _, _, profile := codexHarness(t, AuthSourceAccountHome)
	p := CreateRunParams{PermissionMode: "default", ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverCodex}}
	// A stand-in codex that leaves a mark when it is run, so the test knows the probe launched.
	dir := t.TempDir()
	ran := dir + "/probe-ran"
	program := dir + "/codex"
	if err := os.WriteFile(program, []byte("#!/bin/sh\n: > "+ran+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var told []int
	spec := LaunchSpec{
		Program: program, Dir: dir,
		Env:     []EnvVar{{Name: envCodexHome, Value: profile.ConfigHome}, {Name: envUserHome, Value: profile.UserHome}},
		OnStart: func(pid int) { told = append(told, pid) },
	}
	if err := m.prepareCodexSandbox(t.Context(), &p, &spec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ran); err != nil {
		t.Fatalf("the probe did not run, so the test proves nothing: %v", err)
	}
	if len(told) != 0 {
		t.Fatalf("the Codex sandbox probe told the broker its own process %v before the run's child started", told)
	}
	if spec.OnStart == nil {
		t.Fatal("the session's own launch lost its OnStart: the broker would never learn the child")
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

// queuedMCP is a session MCP source whose store read waits its turn behind the
// other launches on the engine's one store connection, then succeeds.
type queuedMCP struct{ wait time.Duration }

func (f queuedMCP) ConfigureSessionMCP(ctx context.Context, _ model.TenantID, _, _ string, _ *LaunchSpec) (func(), error) {
	select {
	case <-time.After(f.wait):
		return func() {}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// A start that queues behind a burst of other starts is slow, not refused: the
// store read of a setup waited 4 s on a disk under sync load, and a burst can
// wait longer than the 5 s this deadline used to be.
func TestProviderLaunchSetupWaitsBehindOtherLaunches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := New()
		m.SessionMCP = queuedMCP{wait: 20 * time.Second}
		p, spec := CreateRunParams{ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, LaunchSpec{}
		stage, err := m.prepareSessionLaunch(t.Context(), t.Context(), model.TenantID("fixture"), "run", &p, &spec)
		if err != nil {
			t.Fatalf("a setup that queued 20 s was refused at %q: %v", stage, err)
		}
	})
}

// The wait stays bounded: a setup that never answers is refused at the deadline.
func TestProviderLaunchSetupStillRefusesAtTheDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := New()
		m.SessionMCP = queuedMCP{wait: 24 * time.Hour}
		p, spec := CreateRunParams{ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, LaunchSpec{}
		start := time.Now()
		stage, err := m.prepareSessionLaunch(t.Context(), t.Context(), model.TenantID("fixture"), "run", &p, &spec)
		if !errors.Is(err, context.DeadlineExceeded) || stage != "session MCP setup" || time.Since(start) != sessionLaunchSetupTimeout {
			t.Fatalf("stage=%q err=%v after %v, want a refusal at %v", stage, err, time.Since(start), sessionLaunchSetupTimeout)
		}
	})
}

// The deadline derives from the create request: a request the client abandoned
// stops the setup at once, not at the deadline.
func TestProviderLaunchSetupStopsWhenTheRequestIsCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := New()
		m.SessionMCP = queuedMCP{wait: 24 * time.Hour}
		p, spec := CreateRunParams{ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, LaunchSpec{}
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(10*time.Second, cancel)
		start := time.Now()
		stage, err := m.prepareSessionLaunch(ctx, t.Context(), model.TenantID("fixture"), "run", &p, &spec)
		if !errors.Is(err, context.Canceled) || stage != "session MCP setup" || time.Since(start) != 10*time.Second {
			t.Fatalf("stage=%q err=%v after %v, want a cancelled setup at 10s", stage, err, time.Since(start))
		}
	})
}
