// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/olivaresai/olivares/modules/sessions/confine"
	"github.com/olivaresai/olivares/modules/sessions/egress"
)

// A confined session child holds no capability, whatever user the engine runs
// as. A root engine's child would otherwise keep every capability inside its
// Landlock domain. Only an engine holding CAP_SETPCAP can empty the bounding
// set; without it, no_new_privs keeps any exec from regaining the cleared sets.
// The network helper empties it inside its own user namespace.
func TestProcRunnerConfinedChildHoldsNoCapabilities(t *testing.T) {
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Fatal("this security reproducer requires Landlock: " + s.Reason)
	}
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	const none = "0000000000000000"
	engine := capabilitySets(t, string(raw))
	engineBound := engine["CapBnd"]
	if effective, err := strconv.ParseUint(engine["CapEff"], 16, 64); err != nil {
		t.Fatal(err)
	} else if effective&(1<<unix.CAP_SETPCAP) != 0 {
		engineBound = none
	}
	// Only an engine that holds capabilities tells the fix apart on the
	// filesystem-only paths; the network path does so for every engine.
	t.Logf("engine euid %d: CapEff %s CapBnd %s", os.Geteuid(), engine["CapEff"], engine["CapBnd"])
	folder := t.TempDir()
	policy := func() *confine.Policy { return &confine.Policy{ReadWrite: []string{folder}} }
	for _, tc := range []struct {
		name  string
		pty   bool
		spec  func(t *testing.T) LaunchSpec
		bound string
	}{
		{"landlock", false, func(*testing.T) LaunchSpec { return LaunchSpec{Confinement: policy()} }, engineBound},
		{"landlock-pty", true, func(*testing.T) LaunchSpec { return LaunchSpec{Confinement: policy()} }, engineBound},
		{"landlock-held-folder", false, func(t *testing.T) LaunchSpec {
			held, err := confine.OpenDirectory(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = held.Close() })
			return LaunchSpec{Confinement: policy(), ConfinementFiles: []*os.File{held}}
		}, engineBound},
		{"landlock-network", false, func(*testing.T) LaunchSpec {
			return LaunchSpec{Confinement: policy(), NetworkPolicy: &egress.Policy{Providers: []string{"http://127.0.0.1:11434"}}}
		}, none},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.spec(t)
			spec.ConfinementRequired = true
			spec.Program, spec.Dir, spec.WaitDelay = "/bin/sh", folder, 2*time.Second
			spec.Args = []string{"-c", "exec /bin/grep -E '^(Cap|NoNewPrivs)' /proc/self/status"}
			runner := NewProcRunner()
			if tc.pty {
				runner = NewPTYRunner()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			proc, err := runner.Launch(ctx, spec)
			if err != nil {
				t.Fatalf("launch: %v", err)
			}
			if state, ok := confinementOf(proc); !ok || state.Mode != confine.ModeLandlock {
				t.Fatalf("confinement = %+v, %v", state, ok)
			}
			var out strings.Builder
			for f := range proc.Output() {
				out.Write(f.Data)
				out.WriteByte('\n')
			}
			if code, err := proc.Wait(); err != nil || code != 0 {
				t.Fatalf("child exited %d: %v\n%s", code, err, out.String())
			}
			got := capabilitySets(t, out.String())
			want := map[string]string{"CapInh": none, "CapPrm": none, "CapEff": none, "CapAmb": none, "CapBnd": tc.bound, "NoNewPrivs": "1"}
			for set, value := range want {
				if got[set] != value {
					t.Errorf("engine euid %d: child %s = %s, want %s", os.Geteuid(), set, got[set], value)
				}
			}
		})
	}
}

// capabilitySets parses the Cap* and NoNewPrivs lines of a /proc/<pid>/status text.
func capabilitySets(t *testing.T, status string) map[string]string {
	t.Helper()
	sets := map[string]string{}
	for _, line := range strings.Split(status, "\n") {
		if name, value, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && (strings.HasPrefix(name, "Cap") || name == "NoNewPrivs") {
			sets[name] = strings.TrimSpace(value)
		}
	}
	for _, set := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		if len(sets[set]) != 16 {
			t.Fatalf("no %s in the capability report:\n%s", set, status)
		}
	}
	return sets
}
