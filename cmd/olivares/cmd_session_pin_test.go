// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// pinEngine serves three sessions newest first, none of them pinned.
func pinEngine(t *testing.T) *fakeSessionEngine {
	t.Helper()
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "r-new", "name": "newest", "state": "running"},
		{"run_ref": "r-mid", "name": "middle", "state": "running"},
		{"run_ref": "r-old", "name": "oldest", "state": "stopped"},
	}
	return f
}

// `session pin` and `unpin` name the session by its name, make one call each and say
// so in one sentence; the JSON answer is the engine's pin state.
func TestSessionPinAndUnpinMakeOneCallEachAndSaySo(t *testing.T) {
	f := pinEngine(t)
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "pin", "newest"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if out != "Pinned newest.\n" {
		t.Fatalf("pin output = %q", out)
	}
	out, _, err = execSessionCLI(t, nil, append([]string{"session", "unpin", "newest"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if out != "Unpinned newest.\n" {
		t.Fatalf("unpin output = %q", out)
	}
	hits := strings.Join(f.hits, "\n")
	for _, want := range []string{"POST " + sessionRunsPath + "/r-new/pin", "DELETE " + sessionRunsPath + "/r-new/pin"} {
		if strings.Count(hits, want) != 1 {
			t.Fatalf("want exactly one %q in hits:\n%s", want, hits)
		}
	}
	js, _, err := execSessionCLI(t, nil, append([]string{"session", "pin", "newest", "-o", "json"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeSessionBody(t, js)
	if got["run_ref"] != "r-new" || got["pinned"] != true {
		t.Fatalf("json pin state = %v", got)
	}
}

// `session ls` puts the caller's pinned sessions first, in the order they were
// pinned (not the list's order), then the rest as the engine sent them; an engine
// that sends no pin state leaves the order alone. Text and JSON agree.
func TestSessionListPutsPinnedFirstInPinOrder(t *testing.T) {
	cases := []struct {
		name string
		pins map[string]string // run_ref -> pinned_at
		want []string
	}{
		{"nothing pinned", nil, []string{"r-new", "r-mid", "r-old"}},
		{"oldest pinned before newest", map[string]string{"r-old": "2026-10-14T10:00:05Z", "r-new": "2026-10-14T10:00:09Z"},
			[]string{"r-old", "r-new", "r-mid"}},
		{"newest pinned before oldest", map[string]string{"r-new": "2026-10-14T10:00:03Z", "r-old": "2026-10-14T10:00:07Z"},
			[]string{"r-new", "r-old", "r-mid"}},
		{"one pinned in the middle", map[string]string{"r-mid": "2026-10-14T10:00:01Z"}, []string{"r-mid", "r-new", "r-old"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := pinEngine(t)
			for _, run := range f.runs {
				if at, ok := tc.pins[run["run_ref"].(string)]; ok {
					run["pinned"], run["pinned_at"] = true, at
				}
			}
			js, _, err := execSessionCLI(t, nil, append([]string{"session", "ls", "-o", "json"}, sessionCreds(f.URL)...)...)
			if err != nil {
				t.Fatal(err)
			}
			var rows []map[string]any
			if err := json.Unmarshal([]byte(js), &rows); err != nil {
				t.Fatalf("json = %q: %v", js, err)
			}
			var refs []string
			for _, r := range rows {
				refs = append(refs, r["run_ref"].(string))
			}
			if !slices.Equal(refs, tc.want) {
				t.Fatalf("json order = %v, want %v", refs, tc.want)
			}
			text, _, err := execSessionCLI(t, nil, append([]string{"session", "ls"}, sessionCreds(f.URL)...)...)
			if err != nil {
				t.Fatal(err)
			}
			names := map[string]string{"r-new": "newest", "r-mid": "middle", "r-old": "oldest"}
			last := -1
			for _, ref := range tc.want {
				at := strings.Index(text, names[ref])
				if at <= last {
					t.Fatalf("text order differs from %v:\n%s", tc.want, text)
				}
				last = at
			}
		})
	}
}
