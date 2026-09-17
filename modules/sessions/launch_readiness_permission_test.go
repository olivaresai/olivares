// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The permanent regression for the FALSE READY an independent review reproduced.
//
// ⛔ THE CASE, IN ONE SENTENCE: a file the effective user owns at mode 0401 has
// an execute bit — for others — is perfectly statable, and is unambiguously
// denied to the very identity that would run it. The inspector's own predicate
// ("some 0111 bit is set") answered `executable`, so a real GET over real HTTP
// on a real profile returned 200 `ready` / `program_present` for a program that
// cannot start. On the bare-name path the same file came back as `missing`,
// which trades a false ready for a false absence.
//
// ⛔ AND NOTHING HERE RUNS ANYTHING. `exec.LookPath` is stat plus an
// effective-identity access check (faccessat with AT_EACCESS); the fixture
// program is a script that would leave a sentinel if it were ever executed, and
// the wired Runner fails the test on Launch.

// requireEffectiveExecutionDenial builds the negative fixture and proves, with
// the standard library, that this identity really is denied. It reports the
// fixture as NOT EXERCISED rather than passing vacuously when the running
// identity cannot express the denial — root ignores the bits, and so would a
// filesystem mounted with unusual semantics.
func requireEffectiveExecutionDenial(t *testing.T, program string) {
	t.Helper()
	if err := os.Chmod(program, 0o401); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(program); err != nil {
		t.Fatalf("the negative fixture must stay STATABLE — otherwise it proves "+
			"'could not examine', which is a different verdict: %v", err)
	}
	if _, err := exec.LookPath(program); !errors.Is(err, fs.ErrPermission) {
		t.Skipf("effective-execution denial NOT exercised on this run: euid %d resolves mode 0401 "+
			"as %v instead of a permission denial (running as root, or a filesystem that does not "+
			"apply these bits). The positive control above did run.", os.Geteuid(), err)
	}
}

// TestLaunchReadiness_EffectiveExecutionPermission is the causal pair: the SAME
// profile, the SAME wiring, one owned file, and the only change is its mode.
func TestLaunchReadiness_EffectiveExecutionPermission(t *testing.T) {
	for _, be := range readinessEngines(t) {
		t.Run(be.name, func(t *testing.T) {
			bins := newReadinessProgramFixtures(t)
			runner := newInspectingRunner(t)
			// Everything else is ready on purpose: the program is the only thing that
			// may move the verdict, so a change of aggregate cannot be attributed to
			// anything else.
			m := New(WithRunner(runner), WithProgram(bins.present),
				WithLaunchGate(refusingLaunchGate{t}), WithStopGate(refusingStopGate{t}))
			m.UseExecutionEnvironmentRef(testEnvRef)
			f := newReadinessFixture(t, be, m)
			cfgHome, userHome := readinessHomes(t)
			ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)

			// --- positive control: 0700, executable BY THIS IDENTITY ---------------
			if _, err := exec.LookPath(bins.present); err != nil {
				t.Fatalf("positive control is not executable to this identity: %v", err)
			}
			doc, r := f.readiness(ref, "")
			if r.code != http.StatusOK || doc.ConfigurationState != ReadinessReady {
				t.Fatalf("mode 0700 = %d state=%q %s", r.code, doc.ConfigurationState, r.raw)
			}
			doc.wantCheck(t, CheckProgram, ReadinessReady, codeProgramPresent)

			// --- negative control: 0401, denied BY THIS IDENTITY --------------------
			requireEffectiveExecutionDenial(t, bins.present)
			doc, r = f.readiness(ref, "")
			if r.code != http.StatusOK {
				t.Fatalf("mode 0401 = %d %s", r.code, r.raw)
			}
			// Known denial is a nameable, fixable cause — never ready, and never an
			// absence: the file is right there.
			doc.wantCheck(t, CheckProgram, ReadinessNotConfigured, codeProgramNotExecutable)
			if doc.ConfigurationState != ReadinessNotConfigured {
				t.Fatalf("configuration_state = %q while the program cannot be executed by this "+
					"identity: %s", doc.ConfigurationState, r.raw)
			}
			if got := doc.check(t, CheckProgram).Remediation; got != remediationConfigureProgram {
				t.Fatalf("remediation = %q", got)
			}
			if runner.inspections() == 0 {
				t.Fatal("the Runner was never asked, so this verdict came from somewhere else")
			}
			// The path never leaves the server, even when it IS the cause.
			if containsAnyPath(r.raw, bins.dir) {
				t.Fatalf("the response carries a filesystem path: %s", r.raw)
			}
		})
	}
}

// TestLaunchReadiness_EffectiveExecutionPermissionThroughPATH is the bare-name
// half, over real HTTP: the same denied file, reached the way exec.Command
// reaches it. A denied candidate on PATH is `not_executable`, and reporting it
// as `missing` would be a claim of absence about a file that is present.
func TestLaunchReadiness_EffectiveExecutionPermissionThroughPATH(t *testing.T) {
	be := readinessEngines(t)[0]
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "EXECUTED")
	program := filepath.Join(dir, "pathdenied")
	if err := os.WriteFile(program, []byte("#!/bin/sh\ntouch "+sentinel+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(sentinel); err == nil {
			t.Error("the fixture program was EXECUTED during a readiness read")
		}
	})
	// PATH is reduced to this one directory, so the resolution has exactly one
	// candidate and the verdict cannot come from some other installed binary.
	t.Setenv("PATH", dir)

	runner := newInspectingRunner(t)
	m := New(WithRunner(runner), WithProgram("pathdenied"))
	m.UseExecutionEnvironmentRef(testEnvRef)
	f := newReadinessFixture(t, be, m)
	cfgHome, userHome := readinessHomes(t)
	ref := f.createProfile("claude", cfgHome, userHome, AuthSourceAccountHome)

	doc, r := f.readiness(ref, "")
	if r.code != http.StatusOK || doc.ConfigurationState != ReadinessReady {
		t.Fatalf("bare name at mode 0700 = %d state=%q %s", r.code, doc.ConfigurationState, r.raw)
	}
	doc.wantCheck(t, CheckProgram, ReadinessReady, codeProgramPresent)

	requireEffectiveExecutionDenial(t, program)
	// The standard library's own answer for the bare name is now "not found in
	// $PATH" — one error for two very different worlds, which is precisely why
	// the classification must not stop there.
	if _, err := exec.LookPath("pathdenied"); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("PATH control: LookPath = %v, want ErrNotFound", err)
	}
	doc, r = f.readiness(ref, "")
	if r.code != http.StatusOK {
		t.Fatalf("bare name at mode 0401 = %d %s", r.code, r.raw)
	}
	doc.wantCheck(t, CheckProgram, ReadinessNotConfigured, codeProgramNotExecutable)
	if doc.ConfigurationState != ReadinessNotConfigured {
		t.Fatalf("configuration_state = %q, want not_configured", doc.ConfigurationState)
	}
}

// TestInspectProgramMetadataDefersToTheStandardLibrary pins the classifier
// itself against the states the HTTP tables cannot reach cheaply, and states the
// rule the whole correction rests on: whatever `exec.LookPath` decides about
// execution, this inspector decides too — it never re-derives that decision from
// permission bits of its own.
func TestInspectProgramMetadataDefersToTheStandardLibrary(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	executable := write("ok-cli", 0o700)
	noBits := write("plain-cli", 0o600)
	othersOnly := write("others-cli", 0o401)
	subdir := filepath.Join(dir, "adir")
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatal(err)
	}

	// The oracle and the inspector must agree, case by case, and the test says
	// WHICH oracle: the resolution os/exec would perform for a launch.
	for _, tc := range []struct {
		name string
		path string
		want RunnerProgramState
	}{
		{"executable to this identity", executable, RunnerProgramExecutable},
		{"no execute bit at all", noBits, RunnerProgramNotExecutable},
		{"a directory is not a program", subdir, RunnerProgramNotExecutable},
		{"absent", filepath.Join(dir, "nope-cli"), RunnerProgramMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inspectProgramMetadata(tc.path); got != tc.want {
				t.Fatalf("inspectProgramMetadata = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("execute bit for others only", func(t *testing.T) {
		if _, err := exec.LookPath(othersOnly); !errors.Is(err, fs.ErrPermission) {
			t.Skipf("NOT exercised: euid %d is not denied by mode 0401 (%v)", os.Geteuid(), err)
		}
		if got := inspectProgramMetadata(othersOnly); got != RunnerProgramNotExecutable {
			t.Fatalf("inspectProgramMetadata = %q for a file this identity may not execute, "+
				"want %q — a permission bit that belongs to somebody else is not this "+
				"process's permission", got, RunnerProgramNotExecutable)
		}
	})

	t.Run("names os/exec refuses outright", func(t *testing.T) {
		// validateLookPath rejects these three. They are a wiring absence, not a
		// missing binary, and reporting them as one would blame the operator's
		// install for an empty configuration field.
		for _, name := range []string{"", "   ", ".", ".."} {
			if got := inspectProgramMetadata(name); got != RunnerProgramUnknown {
				t.Fatalf("inspectProgramMetadata(%q) = %q, want unknown", name, got)
			}
		}
	})

	t.Run("a link whose target cannot be examined stays unknown", func(t *testing.T) {
		locked := filepath.Join(dir, "locked")
		if err := os.MkdirAll(locked, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(locked, "hidden-cli")
		if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "linked-cli")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
		if _, err := os.Stat(link); err == nil {
			t.Skipf("NOT exercised: euid %d can still stat through a 0000 directory", os.Geteuid())
		}
		// ⛔ THE DISCRIMINATION THIS CASE EXISTS FOR. os/exec answers
		// fs.ErrPermission here too — the same error as a denied execute — and the
		// two must not collapse: one is a verdict about the file, the other is an
		// inspection that could not look.
		if got := inspectProgramMetadata(link); got != RunnerProgramUnknown {
			t.Fatalf("inspectProgramMetadata = %q, want unknown: an unexaminable target is "+
				"not a known denial and not an absence", got)
		}
	})
}

// containsAnyPath reports whether raw leaks a filesystem location.
func containsAnyPath(raw, dir string) bool {
	return dir != "" && strings.Contains(raw, dir)
}
