// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"
)

// `session start --isolation container` asks the engine for a container session; a
// start without the flag is the native request it always was, and the engine, not the
// CLI, decides whether this node can run a container (it answers 422 where it cannot).
func TestSessionStartSendsTheIsolationAskedFor(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"default", nil, "native"},
		{"native", []string{"--isolation", "native"}, "native"},
		{"container", []string{"--isolation", "container"}, "container"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSessionEngine(t)
			_, errb, err := execSessionCLI(t, nil, append(append([]string{"session", "start", t.TempDir()}, tc.args...), sessionCreds(f.URL)...)...)
			if err != nil {
				t.Fatalf("start: %v\n%s", err, errb)
			}
			runs := f.postsTo(sessionRunsPath)
			if len(runs) != 1 || runs[0]["isolation"] != tc.want {
				t.Fatalf("launches = %v, want one with isolation=%s", runs, tc.want)
			}
		})
	}
}

func TestSessionStartRefusesAnIsolationItDoesNotKnowBeforeAskingTheEngine(t *testing.T) {
	f := newFakeSessionEngine(t)
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--isolation", "vm"}, sessionCreds(f.URL)...)...)
	if err == nil || !strings.Contains(err.Error(), "native or container") {
		t.Fatalf("err = %v, want one naming native or container", err)
	}
	if len(f.postsTo(sessionRunsPath)) != 0 {
		t.Fatal("a launch was sent for an isolation the CLI refuses")
	}
}
