// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

func gitlabFor(t *testing.T, d Doer) *GitLab {
	t.Helper()
	g, err := NewGitLab(GitLabConfig{
		APIBase:      "https://gitlab.com",
		ProjectPath:  "acme/tools/widgets",
		Token:        NewSecret("glpat-bot"),
		AllowedHosts: nil,
	}, d)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGitLabNonBotTokenRefused(t *testing.T) {
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/user" {
			if r.Header.Get("PRIVATE-TOKEN") != "glpat-bot" {
				t.Errorf("token header missing")
			}
			_, _ = io.WriteString(w, `{"id":5,"bot":false}`)
			return
		}
		t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
	})
	g := gitlabFor(t, d)
	if _, err := g.Mint(context.Background(), EffectPush); !errors.Is(err, ErrCredentialRefused) {
		t.Fatalf("err = %v, want ErrCredentialRefused", err)
	}
}

func TestGitLabMergeCarriesSHAAndNeverAutoMerges(t *testing.T) {
	status := 200
	var body map[string]any
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v4/user":
			_, _ = io.WriteString(w, `{"id":5,"bot":true}`)
		case r.Method == http.MethodPut && r.URL.EscapedPath() == "/api/v4/projects/acme%2Ftools%2Fwidgets/merge_requests/4/merge":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			w.Header().Set("X-Request-Id", "gl-rid")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"iid":4,"state":"merged","sha":"c1","merge_commit_sha":"m9"}`)
		default:
			http.NotFound(w, r)
		}
	})
	g := gitlabFor(t, d)
	tok, err := g.Mint(context.Background(), EffectMerge)
	if err != nil {
		t.Fatal(err)
	}
	m, res := g.MergeChange(context.Background(), tok, 4, "c1", "merge")
	if res.Class != Applied || m.MergeCommitSHA != "m9" {
		t.Fatalf("merge = %+v %+v", m, res)
	}
	if body["sha"] != "c1" {
		t.Fatalf("sha = %v", body["sha"])
	}
	for _, k := range []string{"auto_merge", "merge_when_pipeline_succeeds"} {
		if _, ok := body[k]; ok {
			t.Fatalf("%s must never be sent", k)
		}
	}
	for s, reason := range map[int]string{409: "head_mismatch", 405: "not_mergeable", 422: "not_mergeable"} {
		status = s
		_, res := g.MergeChange(context.Background(), tok, 4, "c1", "merge")
		if res.Class != Rejected || res.Reason != reason || res.Host.RequestID != "gl-rid" {
			t.Fatalf("merge %d = %+v", s, res)
		}
	}
	status = 503
	if _, res := g.MergeChange(context.Background(), tok, 4, "c1", "merge"); res.Class != Ambiguous {
		t.Fatalf("503 = %+v", res)
	}
}

func TestGitLabOpenChangesAndPushTarget(t *testing.T) {
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != "opened" || q.Get("source_branch") != "olivares/x" || q.Get("target_branch") != "main" {
			t.Errorf("query = %v", q)
		}
		_, _ = io.WriteString(w, `[{"iid":7,"state":"opened","source_branch":"olivares/x","target_branch":"main","sha":"c7"}]`)
	})
	g := gitlabFor(t, d)
	tok := Token{secret: NewSecret("glpat-bot")}
	ch, err := g.OpenChanges(context.Background(), tok, "olivares/x", "main")
	if err != nil || len(ch) != 1 || ch[0].Number != 7 || ch[0].HeadSHA != "c7" || !ch[0].Open {
		t.Fatalf("changes = %+v %v", ch, err)
	}
	u, scheme, hdr := g.PushTarget(tok)
	if u != "https://gitlab.com/acme/tools/widgets.git" || scheme != "https" {
		t.Fatalf("push target %s %s", u, scheme)
	}
	if hdr.Reveal() != "Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte("oauth2:glpat-bot")) {
		t.Fatalf("header = %q", hdr.Reveal())
	}
}

func TestGitLabBranchReportsDefaultAndProtection(t *testing.T) {
	d := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/acme%2Ftools%2Fwidgets":
			_, _ = io.WriteString(w, `{"default_branch":"main"}`)
		case "/api/v4/projects/acme%2Ftools%2Fwidgets/repository/branches/olivares%2Frel":
			_, _ = io.WriteString(w, `{"name":"olivares/rel","protected":true,"default":false}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	g := gitlabFor(t, d)
	tok := Token{secret: NewSecret("glpat-bot")}
	if b, err := g.Branch(context.Background(), tok, "olivares/rel"); err != nil || b.Default != "main" || !b.Protected {
		t.Fatalf("protected = %+v %v", b, err)
	}
	if b, err := g.Branch(context.Background(), tok, "olivares/new"); err != nil || b.Exists || b.Protected {
		t.Fatalf("new = %+v %v", b, err)
	}
}
