// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

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

func TestGitHostDiffOutsideGroup(t *testing.T) {
	called := false
	s := openGitLabDiff(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.WriteString(w, `{"diffs":[{"old_path":"s.txt","new_path":"s.txt","diff":"@@ -1 +1 @@\n-a\n+b"}]}`)
	})
	_, err := s.ReadContentDiff(context.Background(), "othergroup/private", "main", "dev")
	if called {
		t.Fatal("request sent before the scope check")
	}
	if !errors.Is(err, ErrUnknownRef) {
		t.Fatalf("err = %v, want ErrUnknownRef", err)
	}
}

func TestGitHostDiffDotSegments(t *testing.T) {
	called := false
	s := openGitLabDiff(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.WriteString(w, `{}`)
	})
	_, err := s.ReadContentDiff(context.Background(), "mygroup/../../other", "main", "dev")
	if called {
		t.Fatal("dot segments were sent upstream")
	}
	if !errors.Is(err, ErrUnknownRef) {
		t.Fatalf("err = %v, want ErrUnknownRef", err)
	}
}

func TestGitHostDiffNumericID(t *testing.T) {
	called := false
	s := openGitLabDiff(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.WriteString(w, `{}`)
	})
	_, err := s.ReadContentDiff(context.Background(), "12345", "main", "dev")
	if called {
		t.Fatal("numeric project id was sent upstream")
	}
	if !errors.Is(err, ErrUnknownRef) {
		t.Fatalf("err = %v, want ErrUnknownRef", err)
	}
}

func TestGitHostDiffOverTwoMiBTruncates(t *testing.T) {
	patch := "@@ -1 +1 @@\n" + strings.Repeat("x", (2<<20)+64)
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
	if err != nil || !diff.Truncated {
		t.Fatalf("over 2 MiB: err=%v truncated=%v, want nil and truncated", err, diff.Truncated)
	}
}

func TestGitHostDiffHardReadCap(t *testing.T) {
	patch := "@@\n" + strings.Repeat("y", (8<<20)+4096)
	s := openGitLabDiff(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"diffs": []map[string]any{{
				"old_path": "huge.txt",
				"new_path": "huge.txt",
				"diff":     patch,
			}},
		})
	})
	_, err := s.ReadContentDiff(context.Background(), "mygroup/web", "main", "deadbeef")
	if err == nil || errors.Is(err, ErrUpstream) {
		t.Fatalf("past hard cap = %v, want a dedicated too-large error", err)
	}
	if _, ok := err.(interface{ GitHostDiffTooLarge() }); !ok {
		t.Fatalf("err = %T %v, want GitHostDiffTooLarge", err, err)
	}
}

func TestGitHostDiffRenameNotBinary(t *testing.T) {
	s := openGitLabDiff(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"diffs":[{"old_path":"a.txt","new_path":"b.txt","renamed_file":true,"diff":""}]}`)
	})
	diff, err := s.ReadContentDiff(context.Background(), "mygroup/web", "main", "dev")
	if err != nil {
		t.Fatal(err)
	}
	f := diff.Files[0]
	if f.Binary || f.Truncated || f.Path != "b.txt" || f.PreviousPath != "a.txt" || f.Status != "renamed" {
		t.Fatalf("rename = %+v", f)
	}
}

func TestGitHostDiffRateLimited(t *testing.T) {
	s := openGitLabDiff(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "9")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"message":"LEAKED-SECRET-VALUE","token":"glpat-xxxxxxxxxxxxxxxxxxxx"}`)
	})
	_, err := s.ReadContentDiff(context.Background(), "mygroup/web", "main", "dev")
	assertNoGitLabSecret(t, err)
	if err == nil || errors.Is(err, ErrForbidden) || errors.Is(err, ErrUpstream) {
		t.Fatalf("rate limit collapsed to %v", err)
	}
	ra, ok := err.(interface{ RetryAfter() string })
	if !ok || ra.RetryAfter() != "9" {
		t.Fatalf("retry = %v ok=%v", err, ok)
	}
}

func TestGitHostDiffRuneBound(t *testing.T) {
	euro := "€"
	hunk := strings.Repeat("a", contractMaxBytes-1) + euro + "tail"
	s := openGitLabDiff(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"diffs": []map[string]any{{
				"old_path": "u.txt",
				"new_path": "u.txt",
				"diff":     hunk,
			}},
		})
	})
	diff, err := s.ReadContentDiff(context.Background(), "mygroup/web", "main", "dev")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(diff.Files[0].Hunks, "")
	if !diff.Truncated || len(got) > contractMaxBytes || !utf8.ValidString(got) || !strings.HasPrefix(hunk, got) {
		t.Fatalf("kept %d truncated=%v valid=%v", len(got), diff.Truncated, utf8.ValidString(got))
	}
	if len(got) < len(hunk) && !utf8.RuneStart(hunk[len(got)]) {
		t.Fatal("cut inside a multi-byte rune")
	}
}
