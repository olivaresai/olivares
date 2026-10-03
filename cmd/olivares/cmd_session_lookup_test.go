// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// HU2-09: `session ls` cut a long name with "…" and showed no id, and `session show
// "List the files"` answered "No session is named". The id `session ls` now shows,
// the start of the name, or the cut cell as copied, names the session; a start two
// sessions share is refused with the way out.
func TestSessionIsNamedByTheStartOfItsNameOrItsID(t *testing.T) {
	const files, tests = "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", "0199a1b2-c3d4-7e5f-8a9b-ffeeddccbbaa"
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": files, "name": "List the files in this folder and say in one sentence what it is", "state": "running", "created_at": "2026-10-02T18:27:00Z"},
		{"run_ref": tests, "name": "List the tests", "state": "stopped", "created_at": "2026-10-02T18:00:00Z"},
	}
	out, errb, err := execSessionCLI(t, nil, append([]string{"session", "ls"}, sessionCreds(f.URL)...)...)
	if err != nil || !strings.Contains(out, "0c1d2e3f4a5b") || !strings.Contains(out, "ffeeddccbbaa") {
		t.Fatalf("ls: %v %s\n%s, want each session's id", err, errb, out)
	}
	for _, arg := range []string{"List the files", "List the files in this folder and say in one sentence what …", "0c1d2e3f4a5b", "…0c1d2e3f4a5b", "f4a5b"} {
		before := len(f.postsTo(sessionRunsPath + "/" + files + "/stop"))
		if _, errb, err := execSessionCLI(t, nil, append([]string{"session", "stop", arg}, sessionCreds(f.URL)...)...); err != nil {
			t.Fatalf("stop %q: %v %s", arg, err, errb)
		}
		if len(f.postsTo(sessionRunsPath+"/"+files+"/stop")) != before+1 {
			t.Fatalf("stop %q did not reach the session it names: %v", arg, f.hits)
		}
	}
	_, _, err = execSessionCLI(t, nil, append([]string{"session", "show", "List the"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "2 sessions match") || !strings.Contains(err.Error(), "by its id") {
		t.Fatalf("a shared start = %v, want the two-match refusal naming the id", err)
	}
}
