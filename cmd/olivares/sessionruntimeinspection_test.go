// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
)

// The composition root's half of the launch-readiness read.
//
// ⛔ THE SEAM IS OPTIONAL, WHICH IS EXACTLY WHY THIS TEST EXISTS. A Runner that
// does not implement it answers `unknown`, honestly and forever — so if the
// composition root ever wired a Runner without it, the console would degrade to
// "check incomplete" on every profile in production and nothing would be red.
// The module's own batteries cannot notice: they inject their own runner.
func TestSessionRuntimeCompositionWiresAnInspectableRunner(t *testing.T) {
	t.Parallel()
	opts := buildSessionRuntimeOptions(func(string) string { return "" }, nil, nil)
	if m := sessions.New(opts...); !m.LaunchInspectionAvailable() {
		t.Fatal("the runner the composition root wires cannot be inspected without launching, so launch " +
			"readiness would answer unknown for the runner and the program on every profile in production")
	}
	// A module with NO runner is the control: without it, the assertion above
	// could pass on a predicate that answers true unconditionally.
	if sessions.New().LaunchInspectionAvailable() {
		t.Fatal("an unwired module claims an inspectable runner")
	}
	inspector, ok := sessions.NewProcRunner().(sessions.RunnerInspector)
	if !ok {
		t.Fatal("the native runner does not implement the inspection seam")
	}

	// And it answers about REAL files, without running them: the fixture below is
	// a script that would leave a mark if anything executed it.
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "EXECUTED")
	program := filepath.Join(dir, "provider-cli")
	if err := os.WriteFile(program, []byte("#!/bin/sh\ntouch "+sentinel+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	obs, err := inspector.InspectLaunch(context.Background(), sessions.RunnerInspection{
		Program: program, Isolation: sessions.IsolationNative,
	})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if obs.Program != sessions.RunnerProgramExecutable || obs.Isolation != sessions.RunnerIsolationSupported {
		t.Fatalf("observation = %+v, want an executable program under a supported native posture", obs)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("the composed runner EXECUTED the program while inspecting it")
	}
	// The postures this runner refuses are reported as refused, not as supported:
	// running a native child under a row that claims containment is the accident
	// procRunner.Launch already refuses, and the read must agree with it.
	for _, isolation := range []sessions.Isolation{sessions.IsolationContainer, sessions.IsolationSandbox} {
		obs, err := inspector.InspectLaunch(context.Background(), sessions.RunnerInspection{
			Program: program, Isolation: isolation,
		})
		if err != nil || obs.Isolation != sessions.RunnerIsolationUnsupported {
			t.Fatalf("isolation %q = %+v (%v), want unsupported", isolation, obs, err)
		}
	}
}

// TestSessionRuntimeCompositionRegistersDriversIndependently pins the property
// the readiness read reports per driver: pinning ONE official binary makes ONE
// driver operable, and the Claude path is unaffected by either.
func TestSessionRuntimeCompositionRegistersDriversIndependently(t *testing.T) {
	t.Parallel()
	env := func(values map[string]string) func(string) string {
		return func(k string) string { return values[k] }
	}
	cases := []struct {
		name string
		vars map[string]string
		want []string
	}{
		{"nothing pinned", nil, []string{}},
		{"codex only", map[string]string{envSessionCodexBin: "/opt/codex"}, []string{"codex"}},
		{"grok only", map[string]string{envSessionGrokBin: "/opt/grok"}, []string{"grok"}},
		{"opencode only", map[string]string{envSessionOpenCodeBin: "/opt/opencode"}, []string{"opencode"}},
		{"both", map[string]string{envSessionCodexBin: "/opt/codex", envSessionGrokBin: "/opt/grok"}, []string{"codex", "grok"}},
		{"all three", map[string]string{
			envSessionCodexBin: "/opt/codex", envSessionGrokBin: "/opt/grok", envSessionOpenCodeBin: "/opt/opencode",
		}, []string{"codex", "grok", "opencode"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := sessions.New(buildSessionRuntimeOptions(env(tc.vars), nil, nil)...)
			got := m.OperableProviderDrivers()
			if len(got) != len(tc.want) {
				t.Fatalf("operable drivers = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("operable drivers = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
