// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"
	"time"
)

// `session snooze` sends the moment the snooze ends, from a duration or a time, and
// `--undo` ends it; `session done` marks the session done and `--undo` clears it.
// Each makes one call and says so in one sentence; the JSON answer is the engine's.
func TestSessionSnoozeAndDoneMakeOneCallEachAndSaySo(t *testing.T) {
	f := pinEngine(t)
	run := func(args ...string) string {
		t.Helper()
		out, _, err := execSessionCLI(t, nil, append(append([]string{"session"}, args...), sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("session %v: %v", args, err)
		}
		return out
	}

	before := time.Now()
	if out := run("snooze", "newest", "2h"); !strings.HasPrefix(out, "Snoozed newest until ") {
		t.Fatalf("snooze output = %q", out)
	}
	posts := f.posts[sessionRunsPath+"/r-new/snooze"]
	if len(posts) != 1 {
		t.Fatalf("snooze posts = %v", posts)
	}
	until, err := time.Parse(time.RFC3339, posts[0]["until"].(string))
	if err != nil || until.Before(before.Add(2*time.Hour-time.Second)) || until.After(time.Now().Add(2*time.Hour)) {
		t.Fatalf("snooze 2h sent until = %v (%v), want two hours from now", posts[0]["until"], err)
	}
	if out := run("snooze", "newest", "2030-01-02T09:00:00Z"); out != "Snoozed newest until 2030-01-02T09:00:00Z.\n" {
		t.Fatalf("snooze until output = %q", out)
	}
	if out := run("snooze", "newest", "--undo"); out != "Unsnoozed newest.\n" {
		t.Fatalf("unsnooze output = %q", out)
	}
	if out := run("done", "newest"); out != "Marked newest done.\n" {
		t.Fatalf("done output = %q", out)
	}
	js := decodeSessionBody(t, run("done", "middle", "-o", "json"))
	if js["run_ref"] != "r-mid" || js["done"] != true {
		t.Fatalf("json done state = %v", js)
	}
	if out := run("done", "newest", "--undo"); out != "Marked newest not done.\n" {
		t.Fatalf("undone output = %q", out)
	}
	hits := strings.Join(f.hits, "\n")
	for want, n := range map[string]int{
		"POST " + sessionRunsPath + "/r-new/snooze":   2,
		"DELETE " + sessionRunsPath + "/r-new/snooze": 1,
		"POST " + sessionRunsPath + "/r-new/done":     1,
		"DELETE " + sessionRunsPath + "/r-new/done":   1,
	} {
		if got := strings.Count(hits, want); got != n {
			t.Fatalf("%d of %q in hits, want %d:\n%s", got, want, n, hits)
		}
	}
}

// A snooze needs a moment it can read, and --undo takes none: each mistake is
// refused before any call reaches the engine.
func TestSessionSnoozeRefusesAMomentItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"snooze", "newest"}, "accepts 2 arg(s)"},
		{[]string{"snooze", "newest", "soon"}, "give a duration (90m, 2h) or a time"},
		{[]string{"snooze", "newest", "0s"}, "the duration must be positive"},
		{[]string{"snooze", "newest", "2h", "--undo"}, "accepts 1 arg(s)"},
		{[]string{"done", "newest", "now"}, "accepts 1 arg(s)"},
	} {
		args := tc.args
		f := pinEngine(t)
		_, _, err := execSessionCLI(t, nil, append(append([]string{"session"}, args...), sessionCreds(f.URL)...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("session %v: err = %v, want a refusal saying %q", args, err, tc.want)
		}
		for _, hit := range f.hits {
			if strings.HasSuffix(hit, "/snooze") || strings.HasSuffix(hit, "/done") {
				t.Fatalf("session %v reached the engine: %v", args, f.hits)
			}
		}
	}
}
