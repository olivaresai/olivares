// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const r3Project = "/api/v4/projects/mygroup%2Fweb"

func writeGitLabCommit(w http.ResponseWriter, id string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "short_id": "0123abcd", "title": "t", "parent_ids": []string{}})
}

func writeGitLabFiles(w http.ResponseWriter, name string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"diffs": []map[string]any{{
			"old_path": name,
			"new_path": name,
			"new_file": true,
			"diff":     "@@ -0,0 +1 @@\n+" + name,
		}},
	})
}

func TestGitHostDiffCommitIdentity(t *testing.T) {
	base, head := fakeSHA("r3-base"), fakeSHA("r3-head")
	var seen []string
	s := openGitLabHost(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.RequestURI)
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		switch r.RequestURI {
		case r3Project + "/repository/commits/main":
			writeGitLabCommit(w, base)
		case r3Project + "/repository/commits/feature%2Flogin":
			writeGitLabCommit(w, head)
		case r3Project + "/repository/compare?from=" + base + "&to=" + head:
			writeGitLabFiles(w, "main.go")
		default:
			t.Errorf("unexpected request %s", r.RequestURI)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	diff, err := s.ReadContentDiff(context.Background(), "mygroup/web", "main", "feature/login")
	if err != nil {
		t.Fatal(err)
	}
	if diff.BaseCommit != base || diff.HeadCommit != head {
		t.Fatalf("identity = %q %q, want %q %q", diff.BaseCommit, diff.HeadCommit, base, head)
	}
	// GitLab's commit read exposes no tree id, so head_tree is omitted.
	if diff.HeadTree != "" {
		t.Fatalf("head tree = %q, want omitted on GitLab", diff.HeadTree)
	}
	if diff.Base != "main" || diff.Head != "feature/login" {
		t.Fatalf("requested refs = %q %q", diff.Base, diff.Head)
	}
	if len(diff.Files) != 1 || diff.Files[0].Path != "main.go" {
		t.Fatalf("files = %+v", diff.Files)
	}
	if len(seen) != 3 {
		t.Fatalf("requests = %v, want two commit reads and one compare", seen)
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
	s := openGitLabHost(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		p := r.URL.EscapedPath()
		switch {
		case p == r3Project+"/repository/commits/main":
			writeGitLabCommit(w, base)
		case p == r3Project+"/repository/commits/feature%2Flogin":
			writeGitLabCommit(w, current)
			current = headB
		case p == r3Project+"/repository/compare":
			from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
			// A ref names wherever it points now.
			if from == "main" {
				from = base
			}
			if to == "feature/login" {
				to = current
			}
			name, ok := filesAt[to]
			if from != base || !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeGitLabFiles(w, name)
		default:
			t.Errorf("unexpected request %s", r.RequestURI)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	first, err := s.ReadContentDiff(context.Background(), "mygroup/web", "main", "feature/login")
	if err != nil {
		t.Fatal(err)
	}
	if first.HeadCommit != headA {
		t.Fatalf("first head = %q, want %q", first.HeadCommit, headA)
	}
	if len(first.Files) != 1 || first.Files[0].Path != "a.go" {
		t.Fatalf("first files = %+v, want a.go of the resolved head", first.Files)
	}

	second, err := s.ReadContentDiff(context.Background(), "mygroup/web", "main", "feature/login")
	if err != nil {
		t.Fatal(err)
	}
	if second.HeadCommit != headB || len(second.Files) != 1 || second.Files[0].Path != "b.go" {
		t.Fatalf("second = %q %+v, want %q and b.go", second.HeadCommit, second.Files, headB)
	}
}

func TestGitHostDiffInvalidCommitIdentity(t *testing.T) {
	head := fakeSHA("r3-head")
	commit := func(id string) string {
		b, _ := json.Marshal(map[string]any{"id": id, "title": "t"})
		return string(b)
	}
	cases := []struct {
		name, base, head string
	}{
		{"base upper case", commit(strings.ToUpper(fakeSHA("r3-base"))), commit(head)},
		{"head short", commit(fakeSHA("r3-base")), commit(head[:39])},
		{"head sha256", commit(fakeSHA("r3-base")), commit(head + head[:24])},
		{"head not hex", commit(fakeSHA("r3-base")), commit("g" + head[1:])},
		{"head id missing", commit(fakeSHA("r3-base")), `{"title":"t"}`},
		{"head not json", commit(fakeSHA("r3-base")), "not json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openGitLabHost(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.EscapedPath() {
				case r3Project + "/repository/commits/main":
					_, _ = io.WriteString(w, tc.base)
				case r3Project + "/repository/commits/dev":
					_, _ = io.WriteString(w, tc.head)
				default:
					t.Errorf("request after an invalid identity: %s", r.RequestURI)
					writeGitLabFiles(w, "x.go")
				}
			})
			diff, err := s.ReadContentDiff(context.Background(), "mygroup/web", "main", "dev")
			if !errors.Is(err, ErrUpstream) {
				t.Fatalf("err = %v diff = %+v, want ErrUpstream", err, diff)
			}
		})
	}
}

func TestGitHostDiffResolutionStatuses(t *testing.T) {
	cases := []struct {
		name  string
		ref   string
		code  int
		retry string
		want  error
	}{
		{"base 404", "main", http.StatusNotFound, "", ErrUnknownRef},
		{"head 404", "dev", http.StatusNotFound, "", ErrUnknownRef},
		{"head 403", "dev", http.StatusForbidden, "", ErrForbidden},
		{"head 401", "dev", http.StatusUnauthorized, "", ErrForbidden},
		{"head 500", "dev", http.StatusInternalServerError, "", ErrUpstream},
		{"head 429", "dev", http.StatusTooManyRequests, "5", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openGitLabDiff(t, func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("request after a failed resolution: %s", r.RequestURI)
				writeGitLabFiles(w, "x.go")
			})
			inner := s.client.Transport
			s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.EscapedPath() == r3Project+"/repository/commits/"+url.PathEscape(tc.ref) {
					h := http.Header{}
					if tc.retry != "" {
						h.Set("Retry-After", tc.retry)
					}
					return &http.Response{
						StatusCode: tc.code,
						Header:     h,
						Body:       io.NopCloser(strings.NewReader(`{"message":"404 Commit Not Found LEAKED-SECRET-VALUE glpat-xxxxxxxxxxxxxxxxxxxx"}`)),
						Request:    r,
					}, nil
				}
				return inner.RoundTrip(r)
			})
			_, err := s.ReadContentDiff(context.Background(), "mygroup/web", "main", "dev")
			assertNoGitLabSecret(t, err)
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
			s := openGitLabHost(t, func(w http.ResponseWriter, r *http.Request) {
				called = true
				writeGitLabCommit(w, fakeSHA(ref))
			})
			_, errHead := s.ReadContentDiff(context.Background(), "mygroup/web", "main", ref)
			_, errBase := s.ReadContentDiff(context.Background(), "mygroup/web", ref, "main")
			if called {
				t.Fatal("a ref with an empty or dot segment was sent upstream")
			}
			if !errors.Is(errHead, ErrUnknownRef) || !errors.Is(errBase, ErrUnknownRef) {
				t.Fatalf("errs = %v, %v, want ErrUnknownRef", errHead, errBase)
			}
		})
	}
}
