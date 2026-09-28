// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

const r3Repo = "/repos/acme-corp/web-app"

func writeGitHubFiles(w http.ResponseWriter, name string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"files": []map[string]any{{
			"filename": name,
			"status":   "added",
			"patch":    "@@ -0,0 +1 @@\n+" + name,
		}},
	})
}

func TestGitHostDiffCommitIdentity(t *testing.T) {
	base, head, tree := fakeSHA("r3-base"), fakeSHA("r3-head"), fakeSHA("r3-tree")
	var seen []string
	s := openGitHubHost(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.RequestURI)
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		switch r.RequestURI {
		case r3Repo + "/commits/main", r3Repo + "/commits/feature/login":
			if got := r.Header.Get("Accept"); got != "application/vnd.github.sha" {
				t.Errorf("resolution Accept = %q", got)
			}
			if strings.HasSuffix(r.RequestURI, "/main") {
				_, _ = io.WriteString(w, base)
			} else {
				_, _ = io.WriteString(w, head)
			}
		case r3Repo + "/git/commits/" + head:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sha":     head,
				"tree":    map[string]any{"sha": tree},
				"message": "m",
				"parents": []map[string]any{{"sha": base}},
			})
		case r3Repo + "/compare/" + base + "..." + head:
			writeGitHubFiles(w, "main.go")
		default:
			t.Errorf("unexpected request %s", r.RequestURI)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	diff, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "feature/login")
	if err != nil {
		t.Fatal(err)
	}
	if diff.BaseCommit != base || diff.HeadCommit != head || diff.HeadTree != tree {
		t.Fatalf("identity = %q %q %q, want %q %q %q", diff.BaseCommit, diff.HeadCommit, diff.HeadTree, base, head, tree)
	}
	if diff.Base != "main" || diff.Head != "feature/login" {
		t.Fatalf("requested refs = %q %q", diff.Base, diff.Head)
	}
	if len(diff.Files) != 1 || diff.Files[0].Path != "main.go" {
		t.Fatalf("files = %+v", diff.Files)
	}
	compared := 0
	for _, req := range seen {
		if strings.Contains(req, "/compare/") {
			compared++
			if req != "GET "+r3Repo+"/compare/"+base+"..."+head {
				t.Fatalf("compare = %s, want the resolved commits", req)
			}
		}
	}
	if compared != 1 {
		t.Fatalf("compares = %d, requests = %v", compared, seen)
	}
}

// The head ref moves right after it is resolved. The files must be those of
// the resolved head, and a second read sees the new head consistently.
func TestGitHostDiffHeadMovesDuringRead(t *testing.T) {
	base := fakeSHA("r3-base")
	headA, headB := fakeSHA("r3-head-a"), fakeSHA("r3-head-b")
	filesAt := map[string]string{headA: "a.go", headB: "b.go"}
	var mu sync.Mutex
	current := headA
	s := openGitHubHost(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		p := r.URL.Path
		switch {
		case p == r3Repo+"/commits/main":
			_, _ = io.WriteString(w, base)
		case p == r3Repo+"/commits/feature/login":
			_, _ = io.WriteString(w, current)
			current = headB
		case strings.HasPrefix(p, r3Repo+"/git/commits/"):
			sha := strings.TrimPrefix(p, r3Repo+"/git/commits/")
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": sha, "tree": map[string]any{"sha": fakeSHA("tree:" + sha)}})
		case strings.HasPrefix(p, r3Repo+"/compare/"):
			b, h, _ := strings.Cut(strings.TrimPrefix(p, r3Repo+"/compare/"), "...")
			// A ref names wherever it points now.
			if b == "main" {
				b = base
			}
			if h == "feature/login" {
				h = current
			}
			name, ok := filesAt[h]
			if b != base || !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeGitHubFiles(w, name)
		default:
			t.Errorf("unexpected request %s", r.RequestURI)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	first, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "feature/login")
	if err != nil {
		t.Fatal(err)
	}
	if first.HeadCommit != headA || first.HeadTree != fakeSHA("tree:"+headA) {
		t.Fatalf("first identity = %q %q, want head %q", first.HeadCommit, first.HeadTree, headA)
	}
	if len(first.Files) != 1 || first.Files[0].Path != "a.go" {
		t.Fatalf("first files = %+v, want a.go of the resolved head", first.Files)
	}

	second, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "feature/login")
	if err != nil {
		t.Fatal(err)
	}
	if second.HeadCommit != headB || second.HeadTree != fakeSHA("tree:"+headB) {
		t.Fatalf("second identity = %q %q, want head %q", second.HeadCommit, second.HeadTree, headB)
	}
	if len(second.Files) != 1 || second.Files[0].Path != "b.go" {
		t.Fatalf("second files = %+v, want b.go", second.Files)
	}
}

func TestGitHostDiffInvalidCommitIdentity(t *testing.T) {
	head := fakeSHA("r3-head")
	object := func(sha, tree string) string {
		b, _ := json.Marshal(map[string]any{"sha": sha, "tree": map[string]any{"sha": tree}})
		return string(b)
	}
	cases := []struct {
		name, base, head, object string
	}{
		{"base upper case", strings.ToUpper(fakeSHA("r3-base")), head, object(head, fakeSHA("t"))},
		{"head short", fakeSHA("r3-base"), head[:39], object(head[:39], fakeSHA("t"))},
		{"head sha256", fakeSHA("r3-base"), head + head[:24], object(head+head[:24], fakeSHA("t"))},
		{"head not hex", fakeSHA("r3-base"), "g" + head[1:], object("g"+head[1:], fakeSHA("t"))},
		{"head empty", fakeSHA("r3-base"), "", object("", fakeSHA("t"))},
		{"tree upper case", fakeSHA("r3-base"), head, object(head, strings.ToUpper(fakeSHA("t")))},
		{"tree missing", fakeSHA("r3-base"), head, `{"sha":"` + head + `"}`},
		{"object of another commit", fakeSHA("r3-base"), head, object(fakeSHA("other"), fakeSHA("t"))},
		{"object not json", fakeSHA("r3-base"), head, "not json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openGitHubHost(t, func(w http.ResponseWriter, r *http.Request) {
				p := r.URL.Path
				switch {
				case p == r3Repo+"/commits/main":
					_, _ = io.WriteString(w, tc.base)
				case p == r3Repo+"/commits/dev":
					_, _ = io.WriteString(w, tc.head)
				case strings.HasPrefix(p, r3Repo+"/git/commits/"):
					_, _ = io.WriteString(w, tc.object)
				default:
					t.Errorf("request after an invalid identity: %s", r.RequestURI)
					writeGitHubFiles(w, "x.go")
				}
			})
			diff, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "dev")
			if !errors.Is(err, ErrUpstream) {
				t.Fatalf("err = %v diff = %+v, want ErrUpstream", err, diff)
			}
		})
	}
}

func TestGitHostDiffResolutionStatuses(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		code  int
		retry string
		want  error
	}{
		{"base 404", "/commits/main", http.StatusNotFound, "", ErrUnknownRef},
		{"head 422", "/commits/dev", http.StatusUnprocessableEntity, "", ErrUnknownRef},
		{"head 403", "/commits/dev", http.StatusForbidden, "", ErrForbidden},
		{"head 401", "/commits/dev", http.StatusUnauthorized, "", ErrForbidden},
		{"head 409", "/commits/dev", http.StatusConflict, "", ErrUpstream},
		{"head 500", "/commits/dev", http.StatusInternalServerError, "", ErrUpstream},
		{"head 429", "/commits/dev", http.StatusTooManyRequests, "9", nil},
		{"object 403", "/git/commits/", http.StatusForbidden, "", ErrForbidden},
		{"object 500", "/git/commits/", http.StatusInternalServerError, "", ErrUpstream},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openGitHubDiff(t, func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("request after a failed resolution: %s", r.RequestURI)
				writeGitHubFiles(w, "x.go")
			})
			inner := s.client.Transport
			s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, r3Repo+tc.path) {
					h := http.Header{}
					if tc.retry != "" {
						h.Set("Retry-After", tc.retry)
					}
					return &http.Response{
						StatusCode: tc.code,
						Header:     h,
						Body:       io.NopCloser(strings.NewReader(`{"message":"No commit found for SHA: LEAKED-SECRET-VALUE","token":"ghp_test_token"}`)),
						Request:    r,
					}, nil
				}
				return inner.RoundTrip(r)
			})
			_, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "dev")
			assertNoGitHubSecret(t, err)
			if tc.want == nil {
				ra, ok := err.(interface{ RetryAfter() string })
				if !ok || ra.RetryAfter() != tc.retry {
					t.Fatalf("err = %v, want rate limit with Retry-After %s", err, tc.retry)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHostDiffRefDotSegments(t *testing.T) {
	for _, ref := range []string{".", "..", "a/../b", "./main", "feature//x", "/main", "main/"} {
		t.Run(ref, func(t *testing.T) {
			called := false
			s := openGitHubHost(t, func(w http.ResponseWriter, r *http.Request) {
				called = true
				_, _ = io.WriteString(w, fakeSHA(ref))
			})
			_, errHead := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", ref)
			_, errBase := s.ReadContentDiff(context.Background(), "acme-corp/web-app", ref, "main")
			if called {
				t.Fatal("a ref with an empty or dot segment was sent upstream")
			}
			if !errors.Is(errHead, ErrUnknownRef) || !errors.Is(errBase, ErrUnknownRef) {
				t.Fatalf("errs = %v, %v, want ErrUnknownRef", errHead, errBase)
			}
		})
	}
}
