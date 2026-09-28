// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

// Host-method tests; the rebase test is adopted from the construction read.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestAdoptedGitLabRebaseMethodNotSilentlyWeakened(t *testing.T) {
	var body map[string]any
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			_, _ = io.WriteString(w, `{"iid":4,"state":"merged","sha":"c1","merge_commit_sha":"m9"}`)
			return
		}
		http.NotFound(w, r)
	})
	g, err := NewGitLab(GitLabConfig{APIBase: "https://gitlab.com", ProjectPath: "acme/widgets", Token: NewSecret("glpat-x")}, d)
	if err != nil {
		t.Fatal(err)
	}
	_, res := g.MergeChange(context.Background(), Token{secret: NewSecret("glpat-x")}, 4, "c1", "rebase")
	t.Logf("rebase request body = %v, result = %+v", body, res)
	if res.Class == Applied {
		t.Fatalf("method=rebase was sent as %v and reported applied: the requested method was silently weakened", body)
	}
}

func TestPathsChangedCoversGitLabCIConfig(t *testing.T) {
	f := newGitFixture(t)
	dir := t.TempDir()
	work := filepath.Join(dir, "w")
	run(t, dir, "clone", "-q", f.server, work)
	run(t, work, "checkout", "-q", f.commit)
	if err := os.WriteFile(filepath.Join(work, ".gitlab-ci.yml"), []byte("job: {script: [env]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, work, "add", ".gitlab-ci.yml")
	run(t, work, "commit", "-q", "-m", "ci")
	ci := run(t, work, "rev-parse", "HEAD")
	run(t, work, "push", "-q", f.server, "HEAD:refs/heads/ci")
	if changed, err := f.x.PathsChanged(context.Background(), f.server, f.commit, ci, []string{".gitlab-ci.yml"}); err != nil || !changed {
		t.Fatalf("CI config change = %v %v", changed, err)
	}
	if changed, err := f.x.PathsChanged(context.Background(), f.server, f.base, f.commit, []string{".gitlab-ci.yml", ".github/workflows"}); err != nil || changed {
		t.Fatalf("no CI change = %v %v", changed, err)
	}
}
