// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// decoyFixture records what the launch handed the source and when its cleanup ran.
type decoyFixture struct {
	calls                 int
	tenant                model.TenantID
	runRef, home, workDir string
	err                   error
	cleaned               chan struct{}
}

func (f *decoyFixture) PlantSessionDecoys(_ context.Context, tenant model.TenantID, runRef, home, workDir string) (func(), error) {
	f.calls++
	f.tenant, f.runRef, f.home, f.workDir = tenant, runRef, home, workDir
	return func() { close(f.cleaned) }, f.err
}

func decoyLaunch(t *testing.T, m *Module, runCtx context.Context, spec *LaunchSpec) (string, error) {
	t.Helper()
	p := CreateRunParams{PermissionMode: "default", ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}
	return m.prepareSessionLaunch(t.Context(), runCtx, model.TenantID("fixture"), "run-1", &p, spec)
}

// A launch with no source is the launch it was: nothing is planted and nothing is asked.
func TestSessionDecoysAbsentSourceLeavesTheLaunchAlone(t *testing.T) {
	m, _, _, _ := newRuntimeHarness(t)
	spec := LaunchSpec{Dir: t.TempDir(), Env: []EnvVar{{Name: envUserHome, Value: t.TempDir()}}}
	before := len(spec.Env)
	if stage, err := decoyLaunch(t, m, t.Context(), &spec); err != nil {
		t.Fatalf("launch refused at %q: %v", stage, err)
	}
	if len(spec.Env) != before {
		t.Fatalf("env changed with no decoy source: %d -> %d", before, len(spec.Env))
	}
}

// The source gets the run, the HOME the child will see (the last one named) and the
// working directory, and its cleanup runs when the run's context ends, not before.
func TestSessionDecoysSourceGetsHomeAndIsCleanedWithTheRun(t *testing.T) {
	m, _, _, _ := newRuntimeHarness(t)
	f := &decoyFixture{cleaned: make(chan struct{})}
	m.rt.SessionDecoys = f
	work, home := t.TempDir(), t.TempDir()
	spec := LaunchSpec{Dir: work, Env: []EnvVar{
		{Name: envUserHome, Value: "/not/this/one"},
		{Name: "OTHER", Value: "x"},
		{Name: envUserHome, Value: home},
	}}
	runCtx, endRun := context.WithCancel(t.Context())
	if stage, err := decoyLaunch(t, m, runCtx, &spec); err != nil {
		t.Fatalf("launch refused at %q: %v", stage, err)
	}
	if f.calls != 1 || f.tenant != "fixture" || f.runRef != "run-1" || f.home != home || f.workDir != work {
		t.Fatalf("source saw calls=%d tenant=%q run=%q home=%q dir=%q; want 1 fixture run-1 %q %q",
			f.calls, f.tenant, f.runRef, f.home, f.workDir, home, work)
	}
	select {
	case <-f.cleaned:
		t.Fatal("cleanup ran while the run was live")
	default:
	}
	endRun()
	select {
	case <-f.cleaned:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not run when the run ended")
	}
}

// A launch that names no HOME and is not confined (it has no private directory coming),
// or names the engine user's own, still asks the source, with an empty home: the source
// decides, so one that cannot work refuses the launch like any other.
func TestSessionDecoysNoPrivateHomeAsksWithAnEmptyHome(t *testing.T) {
	engineHome := t.TempDir()
	t.Setenv("HOME", engineHome)
	for name, env := range map[string][]EnvVar{
		"no HOME":             nil,
		"the engine's HOME":   {{Name: envUserHome, Value: engineHome}},
		"an unclean spelling": {{Name: envUserHome, Value: engineHome + "/./"}},
	} {
		t.Run(name, func(t *testing.T) {
			m, _, _, _ := newRuntimeHarness(t)
			f := &decoyFixture{cleaned: make(chan struct{})}
			m.rt.SessionDecoys = f
			spec := LaunchSpec{Dir: t.TempDir(), Env: env}
			if stage, err := decoyLaunch(t, m, t.Context(), &spec); err != nil {
				t.Fatalf("launch refused at %q: %v", stage, err)
			}
			if f.calls != 1 || f.home != "" {
				t.Fatalf("source asked %d times with home %q; want once, with an empty home", f.calls, f.home)
			}
		})
	}
}

// A source that cannot plant refuses the launch at a named stage, and anything it
// planted before failing is still cleaned with the run.
func TestSessionDecoysFailureRefusesTheLaunchAndStillCleans(t *testing.T) {
	m, _, _, _ := newRuntimeHarness(t)
	boom := errors.New("disk full")
	f := &decoyFixture{cleaned: make(chan struct{}), err: boom}
	m.rt.SessionDecoys = f
	spec := LaunchSpec{Dir: t.TempDir(), Env: []EnvVar{{Name: envUserHome, Value: t.TempDir()}}}
	runCtx, endRun := context.WithCancel(t.Context())
	stage, err := decoyLaunch(t, m, runCtx, &spec)
	if !errors.Is(err, boom) || stage != "decoy credentials" {
		t.Fatalf("stage=%q err=%v; want the source's error at stage %q", stage, err, "decoy credentials")
	}
	endRun()
	select {
	case <-f.cleaned:
	case <-time.After(5 * time.Second):
		t.Fatal("a failed plant left its files: cleanup never ran")
	}
}

// A confined child that names no HOME runs in the private directory the runner makes, which
// does not exist yet: the source is not asked at preparation, the launch carries the hook
// that asks it with that directory, and the cleanup is tied to the run like any other.
func TestSessionDecoysScratchHomeIsPlantedWhenTheRunnerMakesIt(t *testing.T) {
	m, _, _, _ := newRuntimeHarness(t)
	f := &decoyFixture{cleaned: make(chan struct{})}
	m.rt.SessionDecoys = f
	work, scratch := t.TempDir(), t.TempDir()
	spec := LaunchSpec{Dir: work, Confinement: &confine.Policy{ReadWrite: []string{work}}}
	runCtx, endRun := context.WithCancel(t.Context())
	if stage, err := decoyLaunch(t, m, runCtx, &spec); err != nil {
		t.Fatalf("launch refused at %q: %v", stage, err)
	}
	if f.calls != 0 {
		t.Fatalf("the source was asked %d times before the runner made the home", f.calls)
	}
	if spec.ScratchHome == nil {
		t.Fatal("the launch carries no hook for the runner's private home")
	}
	if err := spec.ScratchHome(scratch); err != nil {
		t.Fatalf("ScratchHome: %v", err)
	}
	if f.calls != 1 || f.runRef != "run-1" || f.home != scratch || f.workDir != work {
		t.Fatalf("source saw calls=%d run=%q home=%q dir=%q; want 1 run-1 %q %q", f.calls, f.runRef, f.home, f.workDir, scratch, work)
	}
	select {
	case <-f.cleaned:
		t.Fatal("cleanup ran while the run was live")
	default:
	}
	endRun()
	select {
	case <-f.cleaned:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not run when the run ended")
	}
}

// A named HOME is planted at preparation, confined or not: the runner makes no directory
// for it and the launch carries no hook.
func TestSessionDecoysNamedHomeNeedsNoRunnerHook(t *testing.T) {
	m, _, _, _ := newRuntimeHarness(t)
	f := &decoyFixture{cleaned: make(chan struct{})}
	m.rt.SessionDecoys = f
	work, home := t.TempDir(), t.TempDir()
	spec := LaunchSpec{Dir: work, Env: []EnvVar{{Name: envUserHome, Value: home}}, Confinement: &confine.Policy{ReadWrite: []string{work}}}
	if stage, err := decoyLaunch(t, m, t.Context(), &spec); err != nil {
		t.Fatalf("launch refused at %q: %v", stage, err)
	}
	if spec.ScratchHome != nil || f.calls != 1 || f.home != home {
		t.Fatalf("hook=%v calls=%d home=%q; want no hook and one call with %q", spec.ScratchHome != nil, f.calls, f.home, home)
	}
}

// The refusal of a source that cannot plant in the runner's private home reaches the launch.
func TestSessionDecoysScratchHomeFailureIsTheSourcesError(t *testing.T) {
	m, _, _, _ := newRuntimeHarness(t)
	boom := errors.New("disk full")
	f := &decoyFixture{cleaned: make(chan struct{}), err: boom}
	m.rt.SessionDecoys = f
	work := t.TempDir()
	spec := LaunchSpec{Dir: work, Confinement: &confine.Policy{ReadWrite: []string{work}}}
	if stage, err := decoyLaunch(t, m, t.Context(), &spec); err != nil {
		t.Fatalf("preparation refused at %q: %v", stage, err)
	}
	if err := spec.ScratchHome(t.TempDir()); !errors.Is(err, boom) {
		t.Fatalf("ScratchHome err = %v, want the source's error", err)
	}
}
