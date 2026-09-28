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
	"testing"
	"unicode/utf8"
)

func TestGitHostDiffOutsideOrg(t *testing.T) {
	called := false
	s := openGitHubDiff(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.WriteString(w, `{"files":[{"filename":"secret.txt","status":"modified","patch":"@@ -1 +1 @@\n-a\n+b"}]}`)
	})
	_, err := s.ReadContentDiff(context.Background(), "other-org/private-repo", "main", "dev")
	if called {
		t.Fatal("request sent before the scope check")
	}
	if !errors.Is(err, ErrUnknownRef) {
		t.Fatalf("err = %v, want ErrUnknownRef", err)
	}
}

func TestGitHostDiffDotSegments(t *testing.T) {
	called := false
	s := openGitHubDiff(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.WriteString(w, `{}`)
	})
	_, err := s.ReadContentDiff(context.Background(), "acme-corp/../../orgs/x", "main", "dev")
	if called {
		t.Fatal("dot segments were sent upstream")
	}
	if !errors.Is(err, ErrUnknownRef) {
		t.Fatalf("err = %v, want ErrUnknownRef", err)
	}
}

func TestGitHostDiffOverTwoMiBTruncates(t *testing.T) {
	patch := "@@ -1 +1 @@\n" + strings.Repeat("x", (2<<20)+64)
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
	if err != nil || !diff.Truncated {
		t.Fatalf("over 2 MiB: err=%v truncated=%v, want nil and truncated", err, diff.Truncated)
	}
	got := 0
	for _, f := range diff.Files {
		for _, h := range f.Hunks {
			got += len(h)
		}
	}
	if got > contractMaxBytes {
		t.Fatalf("retained %d bytes", got)
	}
}

func TestGitHostDiffHardReadCap(t *testing.T) {
	patch := "@@\n" + strings.Repeat("y", (8<<20)+4096)
	s := openGitHubDiff(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"files": []map[string]any{{
				"filename": "huge.txt",
				"status":   "modified",
				"patch":    patch,
			}},
		})
	})
	_, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "deadbeef")
	if err == nil || errors.Is(err, ErrUpstream) {
		t.Fatalf("past hard cap = %v, want a dedicated too-large error", err)
	}
	if _, ok := err.(interface{ GitHostDiffTooLarge() }); !ok {
		t.Fatalf("err = %T %v, want GitHostDiffTooLarge", err, err)
	}
}

func TestGitHostDiffRenameNotBinary(t *testing.T) {
	s := openGitHubDiff(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"files":[{"filename":"b.txt","previous_filename":"a.txt","status":"renamed","additions":0,"deletions":0,"changes":0}]}`)
	})
	diff, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Files) != 1 {
		t.Fatalf("files = %d", len(diff.Files))
	}
	f := diff.Files[0]
	if f.Binary || f.Truncated || f.Path != "b.txt" || f.PreviousPath != "a.txt" || f.Status != "renamed" {
		t.Fatalf("rename = %+v", f)
	}
}

func TestGitHostDiffOmittedPatchTruncates(t *testing.T) {
	s := openGitHubDiff(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"files":[{"filename":"gen.txt","status":"modified","additions":90000,"deletions":80000,"changes":170000}]}`)
	})
	diff, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "dev")
	if err != nil {
		t.Fatal(err)
	}
	f := diff.Files[0]
	if f.Binary || !f.Truncated {
		t.Fatalf("omitted patch: binary=%v truncated=%v", f.Binary, f.Truncated)
	}
}

func TestGitHostDiffRateLimited(t *testing.T) {
	cases := []struct {
		name   string
		code   int
		remain string
		retry  string
	}{
		{"forbidden remaining zero", http.StatusForbidden, "0", "7"},
		{"too many requests", http.StatusTooManyRequests, "", "11"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openGitHubDiff(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.remain != "" {
					w.Header().Set("X-RateLimit-Remaining", tc.remain)
				}
				if tc.retry != "" {
					w.Header().Set("Retry-After", tc.retry)
				}
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, `{"message":"LEAKED-SECRET-VALUE","token":"ghp_test_token"}`)
			})
			_, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "dev")
			assertNoGitHubSecret(t, err)
			if err == nil || errors.Is(err, ErrForbidden) || errors.Is(err, ErrUpstream) {
				t.Fatalf("rate limit collapsed to %v", err)
			}
			ra, ok := err.(interface{ RetryAfter() string })
			if !ok || ra.RetryAfter() != tc.retry {
				t.Fatalf("retry = %v ok=%v, want %s", err, ok, tc.retry)
			}
		})
	}
}

func TestGitHostDiffRuneBound(t *testing.T) {
	euro := "€"
	hunk := strings.Repeat("a", contractMaxBytes-1) + euro + "tail"
	s := openGitHubDiff(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"files": []map[string]any{{
				"filename": "u.txt",
				"status":   "modified",
				"patch":    hunk,
			}},
		})
	})
	diff, err := s.ReadContentDiff(context.Background(), "acme-corp/web-app", "main", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if !diff.Truncated || len(diff.Files) != 1 {
		t.Fatalf("truncated=%v files=%d", diff.Truncated, len(diff.Files))
	}
	got := strings.Join(diff.Files[0].Hunks, "")
	if len(got) > contractMaxBytes || !utf8.ValidString(got) || !strings.HasPrefix(hunk, got) {
		t.Fatalf("kept %d bytes valid=%v", len(got), utf8.ValidString(got))
	}
	if len(got) < len(hunk) && !utf8.RuneStart(hunk[len(got)]) {
		t.Fatal("cut inside a multi-byte rune")
	}
}
