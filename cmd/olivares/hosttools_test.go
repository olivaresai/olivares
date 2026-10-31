// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/modules/sessions"
)

// writeHostToolCanary writes an executable that appends "ran" to marker if
// anything ever executes it.
func writeHostToolCanary(t *testing.T, path, marker string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho ran >> '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func hostToolCanaryRuns(marker string) int {
	b, _ := os.ReadFile(marker)
	return strings.Count(string(b), "ran\n")
}

// Every tool offered by the installer must also be observable without executing
// it. In particular, OpenCode must not become "unsupported" after installation.
func TestHostToolObserverRecognizesEveryInstallableDriver(t *testing.T) {
	dir := t.TempDir()
	home, pathDir := filepath.Join(dir, "home"), filepath.Join(dir, "bin")
	marker := filepath.Join(dir, "executed")
	installer := toolInstallEngine(context.Background())
	for _, driver := range installer.DriverKeys() {
		t.Run(driver, func(t *testing.T) {
			// A vendor install lands on PATH under the tool's own program name, which is
			// not the driver key for Cursor (`agent`) or Gemini CLI (`gemini`).
			facts, ok := driverfacts.Lookup(driver)
			if !ok || facts.Program == "" {
				t.Fatalf("installable driver %s declares no host program name", driver)
			}
			program := filepath.Join(pathDir, facts.Program)
			writeHostToolCanary(t, program, marker)
			observer := newHostToolObserverAt(filepath.Join(dir, "tools"), nil, home, pathDir)
			got, err := observer.ObserveHostTools(context.Background(), driver, program)
			if err != nil || got.UnsupportedDriver {
				t.Fatalf("installable driver %s is not observable: %+v, %v", driver, got, err)
			}
			want := hostToolTuple{"path", "unregistered-observed", true, "same"}
			if tallyHostTools(got)[want] != 1 {
				t.Fatalf("installed PATH tool not observed at %s: %+v, want %+v", program, got, want)
			}
			if n := observer.network.attempts.Load(); n != 0 {
				t.Fatalf("observation attempted %d network requests", n)
			}
		})
	}
	if n := hostToolCanaryRuns(marker); n != 0 {
		t.Fatalf("observation executed a tool %d times", n)
	}
}

// The scope check must examine the PATH name Detect searched, the driver's
// program, not its key. Each PATH entry is sized so that <dir>/<program> is a
// legal path that does not exist while <dir>/<key> exceeds PATH_MAX: Lstat then
// fails on the key entry alone, which shows which name was examined.
func TestHostToolScopeExaminesTheProgramDetectSearches(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the fixture relies on Linux's PATH_MAX of 4096 bytes")
	}
	const pathMax = 4096 // including the terminating NUL
	base := t.TempDir()
	installer := toolInstallEngine(context.Background())
	tested := 0
	for _, driver := range installer.DriverKeys() {
		facts, ok := driverfacts.Lookup(driver)
		if !ok || len(facts.Program) >= len(driver) {
			continue
		}
		tested++
		t.Run(driver, func(t *testing.T) {
			dir := filepath.Join(base, driver)
			for want := pathMax - 1 - len("/"+facts.Program); len(dir) < want; {
				n := min(200, want-len(dir)-1)
				if want-len(dir)-1-n == 1 {
					n-- // a lone "/" left for last would be cleaned away
				}
				dir += "/" + strings.Repeat("d", n)
			}
			if _, err := os.Lstat(filepath.Join(dir, facts.Program)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("fixture: Lstat of the program entry = %v, want not exist", err)
			}
			if _, err := os.Lstat(filepath.Join(dir, driver)); !errors.Is(err, syscall.ENAMETOOLONG) {
				t.Fatalf("fixture: Lstat of the key entry = %v, want ENAMETOOLONG", err)
			}
			observer := newHostToolObserverAt(filepath.Join(base, "tools"), nil, filepath.Join(base, "home"), dir)
			got, err := observer.ObserveHostTools(context.Background(), driver, "")
			if err != nil || len(got.Candidates) != 0 {
				t.Fatalf("scope check examined a name Detect never searched: %+v, %v", got, err)
			}
		})
	}
	if tested == 0 {
		t.Fatal("no installable driver names a program shorter than its key; the fixture shows nothing")
	}
}

type hostToolTuple struct {
	origin, match string
	executable    bool
	configured    string
}

func tallyHostTools(obs sessions.HostToolObservation) map[hostToolTuple]int {
	out := map[hostToolTuple]int{}
	for _, c := range obs.Candidates {
		out[hostToolTuple{c.Origin, c.Match, c.Executable, c.Configured}]++
	}
	return out
}

// TestSessionRuntimeHostToolObserverExecutesVerifiesAndDialsNothing drives the
// PRODUCTION adapter over a real managed release (installed by the real CLI with
// the real verifier), a vendor-layout copy, a PATH copy, and gpg canaries first
// on both the captured and the process PATH.
func TestSessionRuntimeHostToolObserverExecutesVerifiesAndDialsNothing(t *testing.T) {
	f := newAgentToolFixture(t)
	f.publish("2.1.261")
	if code, stdout, stderr := f.run(f.installArgs("--version", "2.1.261", "--yes", "-o", "json")...); code != exitcode.OK {
		t.Fatalf("install: %d\n%s\n%s", code, stdout, stderr)
	}
	ranAfterInstall := f.ran()
	// The observer must never reach the CLI engine seam, which verifies signatures.
	cliEngine := toolInstallEngine
	toolInstallEngine = func(context.Context) *toolinstall.Engine {
		t.Error("host-tool observation built the CLI engine, which verifies signatures")
		return cliEngine(context.Background())
	}
	t.Cleanup(func() { toolInstallEngine = cliEngine })

	dir := toolinstalltest.ExecCapableDir(t)
	marker := filepath.Join(dir, "canary.ran")
	pathDir := filepath.Join(dir, "path")
	writeHostToolCanary(t, filepath.Join(pathDir, "claude"), marker)
	home := filepath.Join(dir, "home")
	vendorBin := filepath.Join(home, ".local", "share", "claude", "versions", "2.1.261")
	writeHostToolCanary(t, vendorBin, marker)
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(vendorBin, filepath.Join(home, ".local", "bin", "claude")); err != nil {
		t.Fatal(err)
	}
	gpgDir := filepath.Join(dir, "gpg-canary")
	for _, name := range []string{"gpg", "gpgconf", "gpg-agent", "gpgv"} {
		writeHostToolCanary(t, filepath.Join(gpgDir, name), marker)
	}
	t.Setenv("PATH", gpgDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	pinned := filepath.Join(dir, "pinned", "claude-official")
	if err := os.MkdirAll(filepath.Dir(pinned), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(pathDir, "claude"), pinned); err != nil {
		t.Fatal(err)
	}
	captured := gpgDir + string(os.PathListSeparator) + pathDir

	obs := newHostToolObserverAt(f.root, nil, home, captured)
	res, err := obs.ObserveHostTools(context.Background(), "claude", pinned)
	if err != nil || res.UnsupportedDriver {
		t.Fatalf("observe: %+v %v", res, err)
	}
	want := map[hostToolTuple]int{
		{"managed", "unverified", true, "different"}:                   1,
		{"vendor-default", "unregistered-observed", true, "different"}: 1,
		{"path", "unregistered-observed", true, "same"}:                1,
	}
	if got := tallyHostTools(res); len(res.Candidates) != 3 || !equalTally(got, want) {
		t.Fatalf("candidates %+v, want %v", res.Candidates, want)
	}

	// A bare configured name resolves on the CAPTURED PATH, like a launch would.
	res, err = obs.ObserveHostTools(context.Background(), "claude", "claude")
	if err != nil || tallyHostTools(res)[hostToolTuple{"path", "unregistered-observed", true, "same"}] != 1 {
		t.Fatalf("bare configured name: %+v %v", res, err)
	}

	// Every comparison that cannot be established is unknown, never different.
	locked := filepath.Join(dir, "locked")
	writeHostToolCanary(t, filepath.Join(locked, "claude"), marker)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	uncertain := []string{filepath.Join(dir, "missing", "claude"), "bin/claude", ""}
	if os.Geteuid() != 0 {
		uncertain = append(uncertain, filepath.Join(locked, "claude"))
	}
	for _, program := range uncertain {
		res, err := obs.ObserveHostTools(context.Background(), "claude", program)
		if err != nil || len(res.Candidates) != 3 {
			t.Fatalf("configured %q: %+v %v", program, res, err)
		}
		for _, c := range res.Candidates {
			if c.Configured != sessions.HostToolCodeUnknown {
				t.Fatalf("configured %q: candidate %+v is not unknown", program, c)
			}
		}
	}
	// A dangling PATH entry is a candidate whose identity cannot be compared.
	dangling := filepath.Join(dir, "dangling")
	if err := os.MkdirAll(dangling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dangling, "claude")); err != nil {
		t.Fatal(err)
	}
	res, err = newHostToolObserverAt(f.root, nil, filepath.Join(dir, "empty-home"), dangling).ObserveHostTools(context.Background(), "claude", pinned)
	if err != nil || tallyHostTools(res)[hostToolTuple{"path", "unregistered-observed", false, "unknown"}] != 1 {
		t.Fatalf("dangling candidate: %+v %v", res, err)
	}

	// Drivers the catalog does not support are refused as unsupported, never
	// answered with Claude's detector; a root that could not be resolved is an error.
	if res, err := obs.ObserveHostTools(context.Background(), "not-a-provider", ""); err != nil || !res.UnsupportedDriver || len(res.Candidates) != 0 {
		t.Fatalf("unsupported: %+v %v", res, err)
	}
	if res, err := obs.ObserveHostTools(context.Background(), "grok", ""); err != nil || res.UnsupportedDriver {
		t.Fatalf("grok must be a supported driver: %+v %v", res, err)
	}
	noRoot := newHostToolObserverAt("", errors.New("no data directory"), home, captured)
	if _, err := noRoot.ObserveHostTools(context.Background(), "claude", ""); err == nil {
		t.Fatal("an unresolved managed root answered as if it had been searched")
	}

	if n := hostToolCanaryRuns(marker); n != 0 {
		t.Fatalf("a CLI or gpg canary ran %d time(s)", n)
	}
	if f.ran() != ranAfterInstall {
		t.Fatal("the managed release was executed by observation")
	}
	if n := obs.network.attempts.Load(); n != 0 {
		t.Fatalf("observation attempted %d network request(s)", n)
	}
	if len(obs.admit) != 0 {
		t.Fatal("admission was not released")
	}
}

func equalTally(a, b map[hostToolTuple]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestSessionRuntimeHostToolObserverAdmissionIsCancelable(t *testing.T) {
	dir := t.TempDir()
	obs := newHostToolObserverAt(filepath.Join(dir, "tools"), nil, dir, dir)

	obs.admit <- struct{}{} // another observation holds admission
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := obs.ObserveHostTools(ctx, "claude", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting for admission ignored the context: %v", err)
	}
	<-obs.admit
	if _, err := obs.ObserveHostTools(context.Background(), "claude", ""); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if len(obs.admit) != 0 {
		t.Fatal("a completed observation kept admission")
	}
	pre, cancelPre := context.WithCancel(context.Background())
	cancelPre()
	if _, err := obs.ObserveHostTools(pre, "claude", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("a canceled context was observed anyway: %v", err)
	}
	if len(obs.admit) != 0 {
		t.Fatal("a canceled observation kept admission")
	}
}

func TestSessionRuntimeHostToolObserverCapturesLocationsAtComposition(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OLIVARES_DATA_DIR", filepath.Join(dir, "data-a"))
	t.Setenv("HOME", filepath.Join(dir, "home-a"))
	cliRoot, err := resolveAgentToolRoot("")
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"PATH": "/captured/path-a"}
	obs := newHostToolObserver(func(k string) string { return env[k] })

	env["PATH"] = "/changed/path-b"
	t.Setenv("OLIVARES_DATA_DIR", filepath.Join(dir, "data-b"))
	t.Setenv("HOME", filepath.Join(dir, "home-b"))
	if obs.rootErr != nil || obs.root != cliRoot || obs.root != filepath.Join(dir, "data-a", "tools") {
		t.Fatalf("root %q (%v), want the CLI's %q", obs.root, obs.rootErr, cliRoot)
	}
	if obs.pathEnv != "/captured/path-a" || obs.home != filepath.Join(dir, "home-a") {
		t.Fatalf("captured path=%q home=%q", obs.pathEnv, obs.home)
	}
	// Composition detected nothing and created nothing.
	if _, err := os.Lstat(filepath.Join(dir, "data-a")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("constructing the observer touched the data directory: %v", err)
	}
}

func TestSessionRuntimeCompositionWiresTheHostToolObserver(t *testing.T) {
	opts := buildSessionRuntimeOptions(func(string) string { return "" }, nil, "", nil)
	if !sessions.New(opts...).HostToolObservationAvailable() {
		t.Fatal("the composition root does not wire the host-tools observer, so the read would answer unknown on every profile")
	}
	if sessions.New().HostToolObservationAvailable() {
		t.Fatal("an unwired module claims a host-tools observer")
	}
}
