// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

type fakeContentDiff struct {
	diff api.GitHostDiff
	err  error
	got  api.GitHostDiffQuery
}

func (f *fakeContentDiff) ReadContentDiff(_ context.Context, q api.GitHostDiffQuery) (api.GitHostDiff, error) {
	f.got = q
	return f.diff, f.err
}

func gitHostDiffPath() string {
	q := url.Values{}
	q.Set("source", "gh-main")
	q.Set("host", "github")
	q.Set("repository", "acme/web")
	q.Set("base", "main")
	q.Set("head", "deadbeef")
	return "/v1/console/sources/diff?" + q.Encode()
}

func gitHostRoster() *stubSourceRoster {
	return &stubSourceRoster{sources: []api.SourceRosterEntry{{Name: "gh-main", Kind: "github"}}}
}

// Resolved commit identity a reader returns with a diff.
const (
	routeBaseCommit = "3d6a61029242b4d9ffd8f9c2538e0a73c4691f89"
	routeHeadCommit = "29f0015b7cd4d758df661204c3da51b135bab406"
	routeHeadTree   = "991c5fd6f41fbe3b9f7b215768cd81c5e805a34e"
)

func TestGitHostDiffRoute(t *testing.T) {
	fake := &fakeContentDiff{diff: api.GitHostDiff{
		BaseCommit: routeBaseCommit,
		HeadCommit: routeHeadCommit,
		HeadTree:   routeHeadTree,
		Files: []api.GitHostDiffFile{{
			Path:   "main.go",
			Status: "modified",
			Hunks:  []string{"@@ -1 +1 @@\n-a\n+b"},
		}},
	}}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.ContentDiff = fake
		o.SourceRoster = gitHostRoster()
	})
	admin := h.adminLogin()

	t.Run("unauthenticated", func(t *testing.T) {
		r := h.do("GET", gitHostDiffPath(), "", nil, nil)
		if r.code != http.StatusUnauthorized {
			t.Fatalf("status = %d %s", r.code, r.raw)
		}
	})

	t.Run("editor forbidden", func(t *testing.T) {
		tenant := h.createOrg(admin, "acme")
		member := h.tenantToken(admin, tenant, "ed@x.io")
		r := h.do("GET", gitHostDiffPath(), member, nil, nil)
		if r.code != http.StatusForbidden {
			t.Fatalf("status = %d %s, want 403", r.code, r.raw)
		}
	})

	t.Run("happy", func(t *testing.T) {
		r := h.do("GET", gitHostDiffPath(), admin, nil, nil)
		if r.code != http.StatusOK {
			t.Fatalf("status = %d %s", r.code, r.raw)
		}
		if fake.got.Source != "gh-main" || fake.got.Host != "github" || fake.got.Repository != "acme/web" || fake.got.Base != "main" || fake.got.Head != "deadbeef" {
			t.Fatalf("query = %+v", fake.got)
		}
		files, _ := r.body["files"].([]any)
		if len(files) != 1 {
			t.Fatalf("body = %s", r.raw)
		}
		file := files[0].(map[string]any)
		if file["path"] != "main.go" || file["binary"] != false || file["truncated"] != false {
			t.Fatalf("file = %#v", file)
		}
		if r.body["truncated"] != false || r.body["base"] != "main" || r.body["head"] != "deadbeef" {
			t.Fatalf("body = %s", r.raw)
		}
		if r.body["base_commit"] != routeBaseCommit || r.body["head_commit"] != routeHeadCommit || r.body["head_tree"] != routeHeadTree {
			t.Fatalf("identity = %s", r.raw)
		}
	})

	t.Run("unknown ref hides secret", func(t *testing.T) {
		fake.err = fmt.Errorf("token ghp_SECRET leaked in LEAKED-SECRET-VALUE: %w", api.ErrContentDiffUnknownRef)
		r := h.do("GET", gitHostDiffPath(), admin, nil, nil)
		if r.code != http.StatusNotFound {
			t.Fatalf("status = %d %s", r.code, r.raw)
		}
		if stringsContainsSecret(r.raw) {
			t.Fatalf("body echoes a secret: %s", r.raw)
		}
		errObj, _ := r.body["error"].(map[string]any)
		if errObj["code"] != "unknown_ref" {
			t.Fatalf("error = %#v", errObj)
		}
	})

	t.Run("upstream hides secret", func(t *testing.T) {
		fake.err = fmt.Errorf("Bearer ghp_SECRET LEAKED-SECRET-VALUE: %w", api.ErrContentDiffUpstream)
		r := h.do("GET", gitHostDiffPath(), admin, nil, nil)
		if r.code != http.StatusBadGateway {
			t.Fatalf("status = %d %s", r.code, r.raw)
		}
		if stringsContainsSecret(r.raw) {
			t.Fatalf("body echoes a secret: %s", r.raw)
		}
		if r.hdr.Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control = %q, want no-store", r.hdr.Get("Cache-Control"))
		}
	})

	t.Run("forbidden upstream", func(t *testing.T) {
		fake.err = api.ErrContentDiffForbidden
		r := h.do("GET", gitHostDiffPath(), admin, nil, nil)
		if r.code != http.StatusForbidden {
			t.Fatalf("status = %d %s", r.code, r.raw)
		}
		if stringsContainsSecret(r.raw) {
			t.Fatalf("body echoes a secret: %s", r.raw)
		}
	})

	t.Run("missing base", func(t *testing.T) {
		fake.err = nil
		r := h.do("GET", "/v1/console/sources/diff?source=gh-main&host=github&repository=acme/web&head=deadbeef", admin, nil, nil)
		if r.code != http.StatusBadRequest {
			t.Fatalf("status = %d %s", r.code, r.raw)
		}
	})
}

func TestGitHostDiffRouteUnwired(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	r := h.do("GET", gitHostDiffPath(), admin, nil, nil)
	if r.code != http.StatusNotImplemented {
		t.Fatalf("status = %d %s, want 501", r.code, r.raw)
	}
	errObj, _ := r.body["error"].(map[string]any)
	if errObj["code"] != "git_host_diff_unavailable" {
		t.Fatalf("error = %#v", errObj)
	}
}

func stringsContainsSecret(s string) bool {
	return strings.Contains(s, "ghp_SECRET") || strings.Contains(s, "LEAKED-SECRET-VALUE")
}

func TestGitHostDiffRouteSource(t *testing.T) {
	fake := &fakeContentDiff{diff: api.GitHostDiff{
		Files: []api.GitHostDiffFile{{Path: "a.go", Status: "modified", Hunks: []string{"@@\n+a"}}},
	}}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.ContentDiff = fake
		o.SourceRoster = &stubSourceRoster{sources: []api.SourceRosterEntry{{Name: "gh-main", Kind: "github"}}}
	})
	admin := h.adminLogin()

	t.Run("missing", func(t *testing.T) {
		r := h.do("GET", "/v1/console/sources/diff?host=github&repository=acme/web&base=main&head=deadbeef", admin, nil, nil)
		if r.code != http.StatusBadRequest {
			t.Fatalf("status = %d %s, want 400", r.code, r.raw)
		}
		if fake.got.Repository != "" {
			t.Fatal("reader called without a source")
		}
	})

	t.Run("unknown", func(t *testing.T) {
		r := h.do("GET", "/v1/console/sources/diff?source=no-such&host=github&repository=acme/web&base=main&head=deadbeef", admin, nil, nil)
		if r.code != http.StatusBadRequest {
			t.Fatalf("status = %d %s, want 400", r.code, r.raw)
		}
		if fake.got.Source != "" {
			t.Fatal("reader called for an unknown source")
		}
	})
}

type gitHostTooLargeError struct{}

func (gitHostTooLargeError) Error() string        { return "content diff: diff too large" }
func (gitHostTooLargeError) GitHostDiffTooLarge() {}

func TestGitHostDiffRouteTooLarge(t *testing.T) {
	fake := &fakeContentDiff{err: gitHostTooLargeError{}}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.ContentDiff = fake
		o.SourceRoster = gitHostRoster()
	})
	admin := h.adminLogin()
	r := h.do("GET", gitHostDiffPath(), admin, nil, nil)
	if r.code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d %s, want 422", r.code, r.raw)
	}
	errObj, _ := r.body["error"].(map[string]any)
	if errObj["code"] != "diff_too_large" {
		t.Fatalf("error = %#v", errObj)
	}
}

func TestGitHostDiffRouteNoStore(t *testing.T) {
	var buf bytes.Buffer
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Logger = slog.New(slog.NewTextHandler(&buf, nil))
	})
	admin := h.adminLogin()
	r := h.do("GET", gitHostDiffPath(), admin, nil, nil)
	if r.code != http.StatusNotImplemented {
		t.Fatalf("status = %d %s", r.code, r.raw)
	}
	if r.hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", r.hdr.Get("Cache-Control"))
	}
	if !strings.Contains(buf.String(), "refused by design") {
		t.Fatalf("log = %q", buf.String())
	}
}

func TestGitHostDiffRouteHostForbidden(t *testing.T) {
	fake := &fakeContentDiff{err: api.ErrContentDiffForbidden}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.ContentDiff = fake
		o.SourceRoster = gitHostRoster()
	})
	admin := h.adminLogin()
	r := h.do("GET", gitHostDiffPath(), admin, nil, nil)
	if r.code != http.StatusForbidden {
		t.Fatalf("status = %d %s", r.code, r.raw)
	}
	errObj, _ := r.body["error"].(map[string]any)
	if errObj["code"] != "git_host_forbidden" {
		t.Fatalf("error = %#v, want git_host_forbidden", errObj)
	}
}

type gitHostRateLimitError struct{ after string }

func (e gitHostRateLimitError) Error() string      { return "content diff: rate limited" }
func (e gitHostRateLimitError) RetryAfter() string { return e.after }

func TestGitHostDiffRouteRateLimited(t *testing.T) {
	fake := &fakeContentDiff{err: gitHostRateLimitError{after: "12"}}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.ContentDiff = fake
		o.SourceRoster = gitHostRoster()
	})
	admin := h.adminLogin()
	r := h.do("GET", gitHostDiffPath(), admin, nil, nil)
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d %s, want 503", r.code, r.raw)
	}
	if r.hdr.Get("Retry-After") != "12" {
		t.Fatalf("Retry-After = %q", r.hdr.Get("Retry-After"))
	}
	errObj, _ := r.body["error"].(map[string]any)
	if errObj["code"] != "rate_limited" {
		t.Fatalf("error = %#v", errObj)
	}
	if r.hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", r.hdr.Get("Cache-Control"))
	}
}

func gitHostDiffPathFor(host string) string {
	q := url.Values{}
	q.Set("source", "gh-main")
	q.Set("host", host)
	q.Set("repository", "acme/web")
	q.Set("base", "main")
	q.Set("head", "feature/login")
	return "/v1/console/sources/diff?" + q.Encode()
}

func TestGitHostDiffRouteCommitIdentity(t *testing.T) {
	fake := &fakeContentDiff{}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.ContentDiff = fake
		o.SourceRoster = gitHostRoster()
	})
	admin := h.adminLogin()

	t.Run("github", func(t *testing.T) {
		fake.diff = api.GitHostDiff{BaseCommit: routeBaseCommit, HeadCommit: routeHeadCommit, HeadTree: routeHeadTree}
		r := h.do("GET", gitHostDiffPathFor("github"), admin, nil, nil)
		if r.code != http.StatusOK {
			t.Fatalf("status = %d %s", r.code, r.raw)
		}
		if r.body["base_commit"] != routeBaseCommit || r.body["head_commit"] != routeHeadCommit || r.body["head_tree"] != routeHeadTree {
			t.Fatalf("identity = %s", r.raw)
		}
		if r.body["base"] != "main" || r.body["head"] != "feature/login" {
			t.Fatalf("requested refs = %s", r.raw)
		}
	})

	t.Run("gitlab omits head_tree", func(t *testing.T) {
		fake.diff = api.GitHostDiff{BaseCommit: routeBaseCommit, HeadCommit: routeHeadCommit}
		r := h.do("GET", gitHostDiffPathFor("gitlab"), admin, nil, nil)
		if r.code != http.StatusOK {
			t.Fatalf("status = %d %s", r.code, r.raw)
		}
		if r.body["base_commit"] != routeBaseCommit || r.body["head_commit"] != routeHeadCommit {
			t.Fatalf("identity = %s", r.raw)
		}
		if _, ok := r.body["head_tree"]; ok {
			t.Fatalf("head_tree present on gitlab: %s", r.raw)
		}
	})
}

func TestGitHostDiffRouteInvalidCommitIdentity(t *testing.T) {
	bad := strings.ToUpper(routeHeadCommit)
	cases := []struct {
		name string
		host string
		diff api.GitHostDiff
	}{
		{"base missing", "github", api.GitHostDiff{HeadCommit: routeHeadCommit, HeadTree: routeHeadTree}},
		{"base upper case", "github", api.GitHostDiff{BaseCommit: bad, HeadCommit: routeHeadCommit, HeadTree: routeHeadTree}},
		{"head missing", "gitlab", api.GitHostDiff{BaseCommit: routeBaseCommit}},
		{"head short", "gitlab", api.GitHostDiff{BaseCommit: routeBaseCommit, HeadCommit: routeHeadCommit[:39]}},
		{"head sha256", "gitlab", api.GitHostDiff{BaseCommit: routeBaseCommit, HeadCommit: routeHeadCommit + routeHeadTree[:24]}},
		{"head not hex", "github", api.GitHostDiff{BaseCommit: routeBaseCommit, HeadCommit: "g" + routeHeadCommit[1:], HeadTree: routeHeadTree}},
		{"tree upper case", "github", api.GitHostDiff{BaseCommit: routeBaseCommit, HeadCommit: routeHeadCommit, HeadTree: strings.ToUpper(routeHeadTree)}},
		{"tree missing on github", "github", api.GitHostDiff{BaseCommit: routeBaseCommit, HeadCommit: routeHeadCommit}},
		{"tree not hex on gitlab", "gitlab", api.GitHostDiff{BaseCommit: routeBaseCommit, HeadCommit: routeHeadCommit, HeadTree: "tree"}},
	}
	fake := &fakeContentDiff{}
	h := newHarnessOpts(t, func(o *api.Options) {
		o.ContentDiff = fake
		o.SourceRoster = gitHostRoster()
	})
	admin := h.adminLogin()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake.diff = tc.diff
			r := h.do("GET", gitHostDiffPathFor(tc.host), admin, nil, nil)
			if r.code != http.StatusBadGateway {
				t.Fatalf("status = %d %s, want 502", r.code, r.raw)
			}
			errObj, _ := r.body["error"].(map[string]any)
			if errObj["code"] != "upstream" {
				t.Fatalf("error = %#v", errObj)
			}
			for _, v := range []string{bad, strings.ToUpper(routeHeadTree), routeHeadCommit[:39], "g" + routeHeadCommit[1:]} {
				if strings.Contains(r.raw, v) {
					t.Fatalf("body passes a host value through: %s", r.raw)
				}
			}
			if r.hdr.Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", r.hdr.Get("Cache-Control"))
			}
		})
	}
}
