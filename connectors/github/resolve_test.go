// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestCommitTreeValidatesTheCompleteBody serves the head commit object with each
// suffix. The object alone and the object followed by whitespace that keeps the
// whole response within maxResolveRead are accepted. Anything after the object
// other than whitespace, and whitespace that takes the response past
// maxResolveRead, is an upstream error, returned before any compare request.
func TestCommitTreeValidatesTheCompleteBody(t *testing.T) {
	base, head, tree := fakeSHA("f1-base"), fakeSHA("f1-head"), fakeSHA("f1-tree")
	object := `{"sha":"` + head + `","tree":{"sha":"` + tree + `"}}`
	toLimit := maxResolveRead - len(object)
	cases := []struct {
		name   string
		suffix string
		ok     bool
	}{
		{"object alone", "", true},
		{"object and bounded whitespace", " \n\t\r\n", true},
		{"object and whitespace to exactly the limit", strings.Repeat(" ", toLimit), true},
		{"object and garbage", "x", false},
		{"object and a second JSON value", object, false},
		{"object and a second scalar", " 1", false},
		{"object and whitespace one byte past the limit", strings.Repeat(" ", toLimit+1), false},
		{"object and a mebibyte of whitespace", strings.Repeat(" ", 1<<20), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			compares := 0
			s := openGitHubHost(t, func(w http.ResponseWriter, r *http.Request) {
				switch p := r.URL.Path; {
				case p == r3Repo+"/commits/main":
					_, _ = io.WriteString(w, base)
				case p == r3Repo+"/commits/dev":
					_, _ = io.WriteString(w, head)
				case p == r3Repo+"/git/commits/"+head:
					_, _ = io.WriteString(w, object+tc.suffix)
				case p == r3Repo+"/compare/"+base+"..."+head:
					compares++
					writeGitHubFiles(w, "main.go")
				default:
					t.Errorf("unexpected request %s", r.RequestURI)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			diff, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "dev")
			if tc.ok {
				if err != nil {
					t.Fatalf("err = %v, want the diff", err)
				}
				if diff.HeadTree != tree || diff.HeadCommit != head || compares != 1 {
					t.Fatalf("tree %q head %q compares %d, want %q %q 1", diff.HeadTree, diff.HeadCommit, compares, tree, head)
				}
				return
			}
			if !errors.Is(err, ErrUpstream) {
				t.Fatalf("err = %v diff = %+v, want ErrUpstream", err, diff)
			}
			if compares != 0 {
				t.Fatalf("compares = %d, want none after a refused commit object", compares)
			}
		})
	}
}
