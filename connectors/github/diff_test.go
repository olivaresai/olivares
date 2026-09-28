// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// Contract bounds. Literals, not the production constants, so a raised or
// removed bound cannot agree with the test by construction.
const (
	contractMaxFiles = 100
	contractMaxBytes = 256 << 10
)

// fakeSHA is a stable 40-hex object id derived from a name.
func fakeSHA(name string) string {
	sum := sha1.Sum([]byte(name))
	return hex.EncodeToString(sum[:])
}

// openGitHubDiff serves the commit reads of acme-corp/web-app from a fake and
// passes every other request to h. A ref resolves to fakeSHA(ref) and a commit
// object carries the tree fakeSHA("tree:"+sha). A commit read for any other
// repository fails the test.
func openGitHubDiff(t *testing.T, h http.HandlerFunc) *Source {
	t.Helper()
	return openGitHubHost(t, func(w http.ResponseWriter, r *http.Request) {
		if serveGitHubCommitReads(t, w, r) {
			return
		}
		h(w, r)
	})
}

func serveGitHubCommitReads(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	t.Helper()
	const repo = "/repos/acme-corp/web-app"
	p := r.URL.Path
	if !strings.Contains(p, "/commits/") {
		return false
	}
	switch {
	case strings.HasPrefix(p, repo+"/git/commits/"):
		sha := strings.TrimPrefix(p, repo+"/git/commits/")
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": sha, "tree": map[string]any{"sha": fakeSHA("tree:" + sha)}})
	case strings.HasPrefix(p, repo+"/commits/"):
		_, _ = io.WriteString(w, fakeSHA(strings.TrimPrefix(p, repo+"/commits/")))
	default:
		t.Errorf("commit read outside the test repository: %s", r.RequestURI)
		w.WriteHeader(http.StatusNotFound)
	}
	return true
}

// openGitHubHost passes every request to h.
func openGitHubHost(t *testing.T, h http.HandlerFunc) *Source {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	cfg := validConfig()
	cfg["api_base"] = ts.URL
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatal(err)
	}
	s.client = ts.Client()
	return s
}

func assertNoGitHubSecret(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, leak := range []string{"ghp_test_token", "LEAKED-SECRET-VALUE", "Bearer "} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error echoes %q", leak)
		}
	}
}

func TestGitHostDiffHappyPath(t *testing.T) {
	hunk1 := "@@ -1,1 +1,1 @@\n-old\n+new"
	hunk2 := "@@ -8,1 +8,1 @@ func F\n-a\n+b"
	patch := hunk1 + "\n" + hunk2
	var gotMethod, gotURI, gotAuth string
	s := openGitHubDiff(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotURI, gotAuth = r.Method, r.RequestURI, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"files": []map[string]any{{
				"filename": "main.go",
				"status":   "modified",
				"patch":    patch,
			}},
		})
	})

	diff, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %s, want GET", gotMethod)
	}
	if want := "/repos/acme-corp/web-app/compare/" + fakeSHA("main") + "..." + fakeSHA("deadbeef"); gotURI != want {
		t.Fatalf("uri = %s, want %s", gotURI, want)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Fatal("missing bearer token")
	}
	if diff.Repository != "acme-corp/web-app" || diff.Base != "main" || diff.Head != "deadbeef" {
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
		s := openGitHubDiff(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("method = %s", r.Method)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{{
					"filename": "big.txt",
					"status":   "modified",
					"patch":    patch,
				}},
			})
		})
		diff, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "deadbeef")
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
			t.Fatal("retained hunk is not a prefix of the upstream patch")
		}
	})

	t.Run("files", func(t *testing.T) {
		files := make([]map[string]any, contractMaxFiles+1)
		for i := range files {
			files[i] = map[string]any{
				"filename": "f.txt",
				"status":   "added",
				"patch":    "@@\n+x",
			}
		}
		s := openGitHubDiff(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
		})
		diff, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "deadbeef")
		if err != nil {
			t.Fatal(err)
		}
		if !diff.Truncated || len(diff.Files) != contractMaxFiles {
			t.Fatalf("files = %d truncated = %v", len(diff.Files), diff.Truncated)
		}
	})
}

func TestGitHostDiffBinaryFile(t *testing.T) {
	s := openGitHubDiff(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		_, _ = io.WriteString(w, `{"files":[{"filename":"logo.bin","status":"added"}]}`)
	})
	diff, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "deadbeef")
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
	s := openGitHubDiff(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"No commit found for SHA: deadbeef","leak":"LEAKED-SECRET-VALUE","token":"ghp_test_token"}`)
	})
	_, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "deadbeef")
	assertNoGitHubSecret(t, err)
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
			s := openGitHubDiff(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method = %s", r.Method)
				}
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, `{"message":"nope","leak":"LEAKED-SECRET-VALUE","token":"ghp_test_token"}`)
			})
			_, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "nope", "deadbeef")
			assertNoGitHubSecret(t, err)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
