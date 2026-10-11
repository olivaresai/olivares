// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// TestSessionPeersChoosesWhoASessionMayMessage: the engine refuses a peer
// send to a session that is not on the sender's peers list, and only the console could
// set it, so J3 could not hand work from one session to another. `session peers` sets
// the list by name over the engine's PUT /runs/{ref}/peers and prints each peer's
// session ID, the one the agent's olivares_peer_send names.
func TestSessionPeersChoosesWhoASessionMayMessage(t *testing.T) {
	const (
		lead     = "osn_01a10199-0000-7000-8000-00000000000a"
		reviewer = "osn_01a10199-0000-7000-8000-00000000000b"
		tester   = "osn_01a10199-0000-7000-8000-00000000000c"
	)
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "01a10199-0000-7000-8000-0000000000a1", "name": "lead", "state": "running", "canonical_sid": lead, "peers": []any{}},
		{"run_ref": "01a10199-0000-7000-8000-0000000000b1", "name": "reviewer", "state": "running", "canonical_sid": reviewer},
		{"run_ref": "01a10199-0000-7000-8000-0000000000c1", "name": "tester", "state": "running", "canonical_sid": tester},
		{"run_ref": "01a10199-0000-7000-8000-0000000000d1", "name": "old", "state": "stopped"},
	}
	peersPath := sessionRunsPath + "/01a10199-0000-7000-8000-0000000000a1/peers"
	run := func(args ...string) (string, error) {
		t.Helper()
		out, _, err := execSessionCLI(t, nil, append(append([]string{"session", "peers"}, args...), sessionCreds(f.URL)...)...)
		return out, err
	}
	putCount := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.posts[peersPath])
	}
	lastBody := func() map[string]any {
		t.Helper()
		f.mu.Lock()
		defer f.mu.Unlock()
		bodies := f.posts[peersPath]
		if len(bodies) == 0 {
			t.Fatal("no PUT reached the engine")
		}
		return bodies[len(bodies)-1]
	}

	out, err := run("lead", "reviewer", tester)
	if err != nil {
		t.Fatal(err)
	}
	if got := lastBody(); !reflect.DeepEqual(got, map[string]any{"peers": []any{reviewer, tester}}) {
		t.Fatalf("PUT body = %v", got)
	}
	if want := "lead may message:\n  reviewer  " + reviewer + "\n  tester  " + tester + "\n"; out != want {
		t.Fatalf("set =\n%s\nwant\n%s", out, want)
	}
	if out, err := run("lead"); err != nil || !strings.Contains(out, "  reviewer  "+reviewer+"\n") {
		t.Fatalf("read = %q, %v", out, err)
	}
	if out, err := run("lead", "--same-template"); err != nil || out != "lead may message every running session started from its template.\n" ||
		!reflect.DeepEqual(lastBody(), map[string]any{"peers_rule": "same-template"}) {
		t.Fatalf("same-template = %q, %v, body %v", out, err, lastBody())
	}
	if out, err := run("lead", "--none"); err != nil || out != "lead may message no session.\n" ||
		!reflect.DeepEqual(lastBody(), map[string]any{"peers": []any{}}) {
		t.Fatalf("none = %q, %v, body %v", out, err, lastBody())
	}
	puts := putCount()
	_, err = run("lead", "old")
	if code := exitcode.From(err); code != exitcode.Conflict || !strings.Contains(err.Error(), "old has no session ID") {
		t.Fatalf("a peer without a session ID = %v (exit %d)", err, code)
	}
	if _, err := run("lead", "reviewer", "--none"); exitcode.From(err) != exitcode.Usage {
		t.Fatalf("peers with --none = %v", err)
	}
	if got := putCount(); got != puts {
		t.Fatalf("a refused choice still reached the engine (%d PUTs, want %d)", got, puts)
	}
}
