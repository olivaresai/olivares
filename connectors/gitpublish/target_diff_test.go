// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// Exercise the real observer through the kind interface, so moving conversion
// and error classification out of the route cannot lose host-specific facts.
func TestTargetKindDiffObserver(t *testing.T) {
	const baseSHA = "1111111111111111111111111111111111111111"
	const headSHA = "2222222222222222222222222222222222222222"
	const treeSHA = "3333333333333333333333333333333333333333"
	for _, name := range []string{"github", "gitlab"} {
		t.Run(name, func(t *testing.T) {
			var status atomic.Int32
			status.Store(http.StatusOK)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("observer sent %s", r.Method)
				}
				if code := int(status.Load()); code != http.StatusOK {
					w.Header().Set("Retry-After", "30")
					w.WriteHeader(code)
					return
				}
				p := r.URL.Path
				switch {
				case strings.Contains(p, "/git/commits/"):
					_ = json.NewEncoder(w).Encode(map[string]any{"sha": headSHA, "tree": map[string]string{"sha": treeSHA}})
				case strings.Contains(p, "/commits/"):
					sha := headSHA
					if strings.HasSuffix(p, "/main") {
						sha = baseSHA
					}
					if name == "github" {
						_, _ = fmt.Fprint(w, sha)
					} else {
						_ = json.NewEncoder(w).Encode(map[string]string{"id": sha})
					}
				default:
					if name == "github" {
						_, _ = fmt.Fprint(w, `{"files":[{"filename":"new.go","previous_filename":"old.go","status":"renamed","patch":"@@ -1 +1 @@\n-a\n+b"}]}`)
					} else {
						_, _ = fmt.Fprint(w, `{"diffs":[{"new_path":"new.go","old_path":"old.go","renamed_file":true,"diff":"@@ -1 +1 @@\n-a\n+b"}]}`)
					}
				}
			}))
			defer server.Close()
			kind, _ := LookupTargetKind(name)
			observer := kind.NewDiffSource()
			cfg := map[string]string{"org": "acme", "group": "acme", "webhook_secret": "test-webhook", "pat": "test-pat", "token": "test-token", "api_base": server.URL}
			if err := observer.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := observer.Close(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			d, err := observer.ReadContentDiff(context.Background(), "acme/tools", "main", "feature")
			wantTree := ""
			if name == "github" {
				wantTree = treeSHA
			}
			if err != nil || d.Repository != "acme/tools" || d.Base != "main" || d.Head != "feature" || d.BaseCommit != baseSHA || d.HeadCommit != headSHA || d.HeadTree != wantTree || len(d.Files) != 1 {
				t.Fatalf("diff = %+v, %v", d, err)
			}
			f := d.Files[0]
			if f.Path != "new.go" || f.PreviousPath != "old.go" || f.Status != "renamed" || f.Binary || f.Truncated || len(f.Hunks) != 1 || f.Hunks[0] != "@@ -1 +1 @@\n-a\n+b" {
				t.Fatalf("file = %+v", f)
			}
			for _, refusal := range []struct {
				status int
				want   error
			}{{404, ErrDiffUnknownRef}, {403, ErrDiffForbidden}} {
				status.Store(int32(refusal.status))
				if _, err := observer.ReadContentDiff(context.Background(), "acme/tools", "main", "feature"); !errors.Is(err, refusal.want) {
					t.Fatalf("HTTP %d = %v", refusal.status, err)
				}
			}
			status.Store(429)
			_, err = observer.ReadContentDiff(context.Background(), "acme/tools", "main", "feature")
			var limited interface{ RetryAfter() string }
			if !errors.As(err, &limited) || limited.RetryAfter() != "30" {
				t.Fatalf("rate limit = %v", err)
			}
		})
	}
}
