// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestResolveCommitValidatesTheCompleteBody serves a commit read with each
// suffix, on the head ref and on the base ref. The object alone and the object
// followed by whitespace that keeps the whole response within maxResolveRead are
// accepted. Anything after the object other than whitespace, and whitespace that
// takes the response past maxResolveRead, is an upstream error, returned before
// any compare request.
func TestResolveCommitValidatesTheCompleteBody(t *testing.T) {
	base, head := fakeSHA("f1-base"), fakeSHA("f1-head")
	cases := []struct {
		name   string
		suffix string
		ok     bool
	}{
		{"object alone", "", true},
		{"object and bounded whitespace", " \n\t\r\n", true},
		{"object and whitespace to exactly the limit", "limit", true},
		{"object and garbage", "x", false},
		{"object and a second JSON value", "again", false},
		{"object and a second scalar", " 1", false},
		{"object and whitespace one byte past the limit", "limit+1", false},
		{"object and a mebibyte of whitespace", strings.Repeat(" ", 1<<20), false},
	}
	for _, ref := range []string{"head", "base"} {
		for _, tc := range cases {
			t.Run(ref+"/"+tc.name, func(t *testing.T) {
				// body is the commit read of id with this case's suffix.
				body := func(id string) string {
					object := `{"id":"` + id + `"}`
					switch tc.suffix {
					case "limit":
						return object + strings.Repeat(" ", maxResolveRead-len(object))
					case "limit+1":
						return object + strings.Repeat(" ", maxResolveRead-len(object)+1)
					case "again":
						return object + object
					}
					return object + tc.suffix
				}
				compares := 0
				s := openGitLabHost(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.EscapedPath() {
					case r3Project + "/repository/commits/main":
						if ref == "base" {
							_, _ = io.WriteString(w, body(base))
						} else {
							writeGitLabCommit(w, base)
						}
					case r3Project + "/repository/commits/dev":
						if ref == "head" {
							_, _ = io.WriteString(w, body(head))
						} else {
							writeGitLabCommit(w, head)
						}
					case r3Project + "/repository/compare":
						compares++
						writeGitLabFiles(w, "main.go")
					default:
						t.Errorf("unexpected request %s", r.RequestURI)
						w.WriteHeader(http.StatusNotFound)
					}
				})
				diff, err := s.ReadContentDiff(context.Background(), "mygroup/web", "main", "dev")
				if tc.ok {
					if err != nil {
						t.Fatalf("err = %v, want the diff", err)
					}
					if diff.BaseCommit != base || diff.HeadCommit != head || compares != 1 {
						t.Fatalf("base %q head %q compares %d, want %q %q 1", diff.BaseCommit, diff.HeadCommit, compares, base, head)
					}
					return
				}
				if !errors.Is(err, ErrUpstream) {
					t.Fatalf("err = %v diff = %+v, want ErrUpstream", err, diff)
				}
				if compares != 0 {
					t.Fatalf("compares = %d, want none after a refused commit read", compares)
				}
			})
		}
	}
}
