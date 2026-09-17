// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package opgate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The read-only custody cases judge a refusal the kernel produces when this process
// cannot create a file in a 0500 directory. A process with DAC override (root, or
// CAP_DAC_OVERRIDE) creates the file anyway, so those cases would judge production
// against a precondition that never held. These helpers prove the precondition and,
// when the process holds the override, re-run only the affected case in a bounded
// subprocess started through setpriv(1) with its bounding, inheritable and ambient
// capability sets requested cleared. That request is not taken as proof: setpriv can
// exit 0 without changing a set. The proof is behavioral. The child probes again and
// runs the case only if it cannot create a file in a 0500 directory; a child that still
// can fails with ENVIRONMENT_DAC_OVERRIDE. The parent's credentials are never changed.

// dacDroppedChildEnv marks the subprocess that re-runs one case through setpriv.
const dacDroppedChildEnv = "OLIVARES_TEST_DAC_DROPPED_CHILD"

// dacChildMaxBudget caps one delegated child run.
const dacChildMaxBudget = 10 * time.Minute

// dacChildDeadlineReserve is kept back from the test deadline so the parent can still
// report the child's outcome before the test binary's own timeout fires.
const dacChildDeadlineReserve = 30 * time.Second

// errDACOverrideEnvironment names an environment in which a permission-denied case
// cannot be exercised.
var errDACOverrideEnvironment = errors.New("ENVIRONMENT_DAC_OVERRIDE: this process can create files in a directory the case made read-only, so a read-only refusal cannot be exercised here")

// errDACChildBudgetExhausted refuses a delegated run that would start with no usable
// time left before the test deadline.
var errDACChildBudgetExhausted = errors.New("DAC_CHILD_BUDGET_EXHAUSTED: the test deadline leaves no time to run the case in a subprocess")

// dacChildBudget is the time one delegated child may use: min(dacChildMaxBudget,
// deadline - now - dacChildDeadlineReserve). A deadline that leaves nothing refuses.
func dacChildBudget(now, deadline time.Time, hasDeadline bool) (time.Duration, error) {
	if !hasDeadline {
		return dacChildMaxBudget, nil
	}
	remaining := deadline.Sub(now) - dacChildDeadlineReserve
	if remaining <= 0 {
		return 0, fmt.Errorf("%w (deadline in %s, reserve %s)", errDACChildBudgetExhausted, deadline.Sub(now), dacChildDeadlineReserve)
	}
	return min(dacChildMaxBudget, remaining), nil
}

// dacProbeWritable reports whether this process can create a file in dir.
func dacProbeWritable(dir string) (bool, error) {
	f, err := os.CreateTemp(dir, ".dac-probe-*")
	if err == nil {
		name := f.Name()
		_ = f.Close()
		return true, os.Remove(name)
	}
	if errors.Is(err, os.ErrPermission) {
		return false, nil
	}
	return false, err
}

// requireUnwritableDirectory proves the read-only precondition on the real directory
// before any production outcome is judged.
func requireUnwritableDirectory(t *testing.T, dir string) {
	t.Helper()
	writable, err := dacProbeWritable(dir)
	if err != nil {
		t.Fatalf("probe the read-only precondition of %s: %v", dir, err)
	}
	if writable {
		t.Fatalf("%v (directory %s, euid %d)", errDACOverrideEnvironment, dir, os.Geteuid())
	}
}

// dacChildRunPattern anchors every element of a test name so the child runs exactly
// this case and nothing else.
func dacChildRunPattern(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	return strings.Join(parts, "/")
}

// runWithoutDACOverride returns false when this process cannot write a 0500
// directory, and the caller runs the case in place. Otherwise it re-runs exactly this
// case in a bounded subprocess through setpriv, requires that case's own PASS, and
// returns true. The child's PASS means the child itself probed a 0500 directory, could
// not write it, and then passed every assertion of the case.
func runWithoutDACOverride(t *testing.T) bool {
	t.Helper()
	probe := t.TempDir()
	if err := os.Chmod(probe, 0o500); err != nil {
		t.Fatal(err)
	}
	writable, err := dacProbeWritable(probe)
	if cerr := os.Chmod(probe, 0o700); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil {
		t.Fatalf("probe DAC override: %v", err)
	}
	if !writable {
		return false
	}
	if os.Getenv(dacDroppedChildEnv) != "" {
		t.Fatalf("%v: the setpriv subprocess can still write it (euid %d)", errDACOverrideEnvironment, os.Geteuid())
	}
	setpriv, err := exec.LookPath("setpriv")
	if err != nil {
		t.Fatalf("%v: setpriv(1) is unavailable to re-run %s in a subprocess: %v", errDACOverrideEnvironment, t.Name(), err)
	}
	deadline, hasDeadline := t.Deadline()
	budget, err := dacChildBudget(time.Now(), deadline, hasDeadline)
	if err != nil {
		t.Fatalf("%s: %v", t.Name(), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	cmd := exec.CommandContext(ctx, setpriv, "--bounding-set", "-all", "--inh-caps", "-all", "--ambient-caps", "-all",
		"--", os.Args[0], "-test.run="+dacChildRunPattern(t.Name()), "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), dacDroppedChildEnv+"=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	text := out.String()
	passed := strings.Contains(text, "--- PASS: "+t.Name()+" ")
	if runErr != nil || !passed {
		t.Fatalf("%s re-run through setpriv did not pass within %s (err %v, own PASS %t):\n%s", t.Name(), budget, runErr, passed, text)
	}
	t.Logf("%s passed in a setpriv subprocess (parent euid %d, budget %s); the child proved it could not create a file in a 0500 directory before running the case",
		t.Name(), os.Geteuid(), budget)
	return true
}

func TestDACOverrideProbeSeesAWritableDirectory(t *testing.T) {
	dir := t.TempDir()
	writable, err := dacProbeWritable(dir)
	if err != nil || !writable {
		t.Fatalf("a 0700 directory owned by this process was reported unwritable: writable=%t err=%v", writable, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("the probe left files behind: %v %v", entries, err)
	}
}

func TestDACChildRunPatternSelectsExactlyOneCase(t *testing.T) {
	pattern := dacChildRunPattern("TestA/quote-and-metacharacters")
	if pattern != "^TestA$/^quote-and-metacharacters$" {
		t.Fatalf("pattern = %q", pattern)
	}
	for _, name := range []string{"TestA", "quote-and-metacharacters"} {
		for _, elem := range strings.Split(pattern, "/") {
			if regexp.MustCompile(elem).MatchString(name+"x") || regexp.MustCompile(elem).MatchString("x"+name) {
				t.Fatalf("element %q matches a longer name than %q", elem, name)
			}
		}
	}
}

func TestDACChildBudgetIsCappedAndRefusesAnExhaustedDeadline(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		deadline    time.Time
		hasDeadline bool
		want        time.Duration
		exhausted   bool
	}{
		{name: "no-deadline-uses-the-cap", want: dacChildMaxBudget},
		{name: "far-deadline-is-capped", deadline: now.Add(time.Hour), hasDeadline: true, want: dacChildMaxBudget},
		{name: "cap-boundary", deadline: now.Add(dacChildMaxBudget + dacChildDeadlineReserve), hasDeadline: true, want: dacChildMaxBudget},
		{name: "near-deadline-keeps-the-reserve", deadline: now.Add(5 * time.Minute), hasDeadline: true, want: 5*time.Minute - dacChildDeadlineReserve},
		{name: "one-nanosecond-left", deadline: now.Add(dacChildDeadlineReserve + time.Nanosecond), hasDeadline: true, want: time.Nanosecond},
		{name: "reserve-only-is-exhausted", deadline: now.Add(dacChildDeadlineReserve), hasDeadline: true, exhausted: true},
		{name: "inside-the-reserve-is-exhausted", deadline: now.Add(10 * time.Second), hasDeadline: true, exhausted: true},
		{name: "passed-deadline-is-exhausted", deadline: now.Add(-time.Minute), hasDeadline: true, exhausted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dacChildBudget(now, tc.deadline, tc.hasDeadline)
			if tc.exhausted {
				if !errors.Is(err, errDACChildBudgetExhausted) || got != 0 {
					t.Fatalf("budget = %s, err = %v; want 0 and %v", got, err, errDACChildBudgetExhausted)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("budget = %s, err = %v; want %s", got, err, tc.want)
			}
			if got > dacChildMaxBudget {
				t.Fatalf("budget %s exceeds the cap %s", got, dacChildMaxBudget)
			}
		})
	}
}
