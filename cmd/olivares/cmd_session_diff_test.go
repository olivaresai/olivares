// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const (
	diffBase = "aaaaaaaaaaaabbbbbbbbbbbbccccccccccccdddd"
	diffHead = "1111111111112222222222223333333333334444"
)

// `session diff` prints what the console's branch changes show, from the same two
// engine reads: the paths a worktree session's branch changed, and each path's text at
// the base and at the branch tip, as a unified diff.
func TestSessionDiffPrintsTheBranchChangesAsAUnifiedDiff(t *testing.T) {
	const list = `{"branch":"task/fix","base":"` + diffBase + `","head":"` + diffHead + `","truncated":false,
"files":[{"path":"a.go","status":"modified"},{"path":"docs/new.md","status":"added"},{"path":"img.png","status":"modified"},{"path":"old.txt","status":"deleted"}]}`
	files := map[string]string{
		"a.go":        `{"path":"a.go","status":"modified","original":"one\ntwo\nthree\n","modified":"one\n2\nthree\n","binary":false,"truncated":false}`,
		"docs/new.md": `{"path":"docs/new.md","status":"added","original":"","modified":"# New\n\u001b]52;c;aGk=\u0007hello\n","binary":false,"truncated":false}`,
		"img.png":     `{"path":"img.png","status":"modified","original":"","modified":"","binary":true,"truncated":false}`,
		"old.txt":     `{"path":"old.txt","status":"deleted","original":"bye\n","modified":"","binary":false,"truncated":false}`,
	}
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "notes", "state": "running", "provider_driver": "claude"}}
	var read []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == sessionRunsPath+"/r1/diff":
			read = append(read, "list")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(list))
		case r.Method == http.MethodGet && r.URL.Path == sessionRunsPath+"/r1/diff/file":
			path := r.URL.Query().Get("path")
			read = append(read, path)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(files[path]))
		default:
			f.serve(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	run := func(args ...string) (string, error) {
		t.Helper()
		out, errb, err := execSessionCLI(t, nil, append(append([]string{"session", "diff", "notes"}, args...), sessionCreds(srv.URL)...)...)
		if err != nil {
			t.Logf("stderr: %s", errb)
		}
		return out, err
	}

	out, err := run()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"task/fix", diffBase[:12], diffHead[:12],
		"--- a/a.go", "+++ b/a.go", "-two", "+2", " one",
		"--- /dev/null", "+++ b/docs/new.md", "+# New", "+hello",
		"Binary file img.png differs",
		"--- a/old.txt", "+++ /dev/null", "-bye",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("diff lacks %q:\n%s", want, out)
		}
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Fatalf("what the branch holds drove the terminal: %q", out)
	}

	// One path: the listing still names the range, and only that file is read.
	read = nil
	out, err = run("a.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "docs/new.md") || !strings.Contains(out, "+2") {
		t.Fatalf("one path printed:\n%s", out)
	}
	if got := strings.Join(read, ","); got != "list,a.go" {
		t.Fatalf("one path read %q, want list,a.go", got)
	}
	if _, err = run("nothing.go"); err == nil || exitcode.From(err) != exitcode.NotFound {
		t.Fatalf("a path the branch did not change: err=%v, want exit %d", err, exitcode.NotFound)
	}

	// -o json carries the same diff, per file.
	out, err = run("-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Branch string `json:"branch"`
		Base   string `json:"base"`
		Head   string `json:"head"`
		Files  []struct {
			Path        string `json:"path"`
			Status      string `json:"status"`
			Binary      bool   `json:"binary"`
			Diff        string `json:"diff"`
			DiffOmitted string `json:"diff_omitted"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Branch != "task/fix" || got.Base != diffBase || got.Head != diffHead || len(got.Files) != 4 {
		t.Fatalf("json header/files = %+v", got)
	}
	if !strings.Contains(got.Files[0].Diff, "+2") || !got.Files[2].Binary || got.Files[2].Diff != "" {
		t.Fatalf("json files = %+v", got.Files)
	}
}
