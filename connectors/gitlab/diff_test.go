// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const (
	contractMaxFiles = 100
	contractMaxBytes = 256 << 10
)

// fakeSHA is a stable 40-hex object id derived from a name.
func fakeSHA(name string) string {
	sum := sha1.Sum([]byte(name))
	return hex.EncodeToString(sum[:])
}

// openGitLabDiff serves the commit reads of mygroup/web from a fake and passes
// every other request to h. A ref resolves to fakeSHA(ref). A commit read for
// any other project fails the test.
func openGitLabDiff(t *testing.T, h http.HandlerFunc) *Source {
	t.Helper()
	return openGitLabHost(t, func(w http.ResponseWriter, r *http.Request) {
		if serveGitLabCommitReads(t, w, r) {
			return
		}
		h(w, r)
	})
}

func serveGitLabCommitReads(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	t.Helper()
	const project = "/api/v4/projects/mygroup%2Fweb"
	p := r.URL.EscapedPath()
	if !strings.Contains(p, "/repository/commits/") {
		return false
	}
	if !strings.HasPrefix(p, project+"/repository/commits/") {
		t.Errorf("commit read outside the test project: %s", r.RequestURI)
		w.WriteHeader(http.StatusNotFound)
		return true
	}
	ref, err := url.PathUnescape(strings.TrimPrefix(p, project+"/repository/commits/"))
	if err != nil {
		t.Errorf("ref escape: %v", err)
	}
	sha := fakeSHA(ref)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": sha, "short_id": sha[:8], "title": "t", "parent_ids": []string{}})
	return true
}

// openGitLabHost passes every request to h.
func openGitLabHost(t *testing.T, h http.HandlerFunc) *Source {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	cfg := validConfig()
	cfg.Settings["api_base"] = ts.URL
	s := New()
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	s.client = ts.Client()
	return s
}

func assertNoGitLabSecret(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, leak := range []string{"glpat-xxxxxxxxxxxxxxxxxxxx", "LEAKED-SECRET-VALUE", "PRIVATE-TOKEN"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error echoes %q", leak)
		}
	}
}

func TestGitHostDiffHappyPath(t *testing.T) {
	hunk1 := "@@ -1,1 +1,1 @@\n-old\n+new"
	hunk2 := "@@ -8,1 +8,1 @@ func F\n-a\n+b"
	patch := hunk1 + "\n" + hunk2
	var gotMethod, gotURI, gotToken string
	s := openGitLabDiff(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotURI = r.RequestURI
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"diffs": []map[string]any{{
				"old_path": "main.go",
				"new_path": "main.go",
				"diff":     patch,
			}},
		})
	})

	diff, err := s.ReadContentDiff(context.Background(), "mygroup/web", "main", "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %s, want GET", gotMethod)
	}
	if !strings.Contains(gotURI, "/api/v4/projects/") || !strings.Contains(gotURI, "/repository/compare") {
		t.Fatalf("uri = %s", gotURI)
	}
	if !strings.HasSuffix(gotURI, "?from="+fakeSHA("main")+"&to="+fakeSHA("deadbeef")) {
		t.Fatalf("uri = %s, want the resolved commits", gotURI)
	}
	if gotToken == "" {
		t.Fatal("missing private token")
	}
	if diff.Repository != "mygroup/web" || diff.Base != "main" || diff.Head != "deadbeef" {
		t.Fatalf("identity = %+v", diff)
	}
	if diff.Truncated || len(diff.Files) != 1 {
		t.Fatalf("diff = %+v", diff)
	}
	f := diff.Files[0]
	if f.Path != "main.go" || f.Status != "modified" || f.Binary || f.Truncated {
		t.Fatalf("file = %+v", f)
	}
	if len(f.Hunks) != 2 || f.Hunks[0] != hunk1 || f.Hunks[1] != hunk2 {
		t.Fatalf("hunks = %#v", f.Hunks)
	}
}

func TestGitHostDiffSizeBoundTruncates(t *testing.T) {
	t.Run("bytes", func(t *testing.T) {
		patch := "@@ -1 +1 @@\n" + strings.Repeat("x", contractMaxBytes)
		s := openGitLabDiff(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("method = %s", r.Method)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"diffs": []map[string]any{{
					"old_path": "big.txt",
					"new_path": "big.txt",
					"diff":     patch,
				}},
			})
		})
		diff, err := s.ReadContentDiff(context.Background(), "mygroup/web", "main", "deadbeef")
		if err != nil {
			t.Fatal(err)
		}
		if !diff.Truncated || len(diff.Files) != 1 || !diff.Files[0].Truncated {
			t.Fatalf("want truncated file, got %+v", diff)
		}
		got := strings.Join(diff.Files[0].Hunks, "")
		if len(got) > contractMaxBytes {
			t.Fatalf("retained %d bytes, bound %d", len(got), contractMaxBytes)
		}
		if !strings.HasPrefix(patch, got) {
			t.Fatal("retained hunk is not a prefix of the upstream diff")
		}
	})

	t.Run("files", func(t *testing.T) {
		files := make([]map[string]any, contractMaxFiles+1)
		for i := range files {
			files[i] = map[string]any{
				"old_path": "f.txt",
				"new_path": "f.txt",
				"diff":     "@@\n+x",
			}
		}
		s := openGitLabDiff(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"diffs": files})
		})
		diff, err := s.ReadContentDiff(context.Background(), "mygroup/web", "main", "deadbeef")
		if err != nil {
			t.Fatal(err)
		}
		if !diff.Truncated || len(diff.Files) != contractMaxFiles {
			t.Fatalf("files = %d truncated = %v", len(diff.Files), diff.Truncated)
		}
	})
}

func TestGitHostDiffBinaryFile(t *testing.T) {
	s := openGitLabDiff(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		_, _ = io.WriteString(w, `{"diffs":[{"old_path":"logo.bin","new_path":"logo.bin","new_file":true,"diff":"Binary files a/logo.bin and b/logo.bin differ"}]}`)
	})
	diff, err := s.ReadContentDiff(context.Background(), "mygroup/web", "main", "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if diff.Truncated || len(diff.Files) != 1 {
		t.Fatalf("diff = %+v", diff)
	}
	f := diff.Files[0]
	if f.Path != "logo.bin" || f.Status != "added" || !f.Binary || f.Truncated || len(f.Hunks) != 0 {
		t.Fatalf("file = %+v", f)
	}
}

func TestGitHostDiffUnknownRef(t *testing.T) {
	s := openGitLabDiff(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"404 Commit Not Found","leak":"LEAKED-SECRET-VALUE","token":"glpat-xxxxxxxxxxxxxxxxxxxx"}`)
	})
	_, err := s.ReadContentDiff(context.Background(), "mygroup/web", "main", "deadbeef")
	assertNoGitLabSecret(t, err)
	if !errors.Is(err, ErrUnknownRef) {
		t.Fatalf("err = %v, want ErrUnknownRef", err)
	}
}

func TestGitHostDiffUpstreamStatuses(t *testing.T) {
	cases := []struct {
		name string
		code int
		want error
	}{
		{"404", http.StatusNotFound, ErrUnknownRef},
		{"403", http.StatusForbidden, ErrForbidden},
		{"500", http.StatusInternalServerError, ErrUpstream},
		{"502", http.StatusBadGateway, ErrUpstream},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openGitLabDiff(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method = %s", r.Method)
				}
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, `{"message":"nope","leak":"LEAKED-SECRET-VALUE","token":"glpat-xxxxxxxxxxxxxxxxxxxx"}`)
			})
			_, err := s.ReadContentDiff(context.Background(), "mygroup/web", "nope", "deadbeef")
			assertNoGitLabSecret(t, err)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
