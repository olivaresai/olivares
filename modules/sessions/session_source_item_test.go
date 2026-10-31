// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/internal/oastest"
)

func TestParseSourceItemURL(t *testing.T) {
	t.Parallel()
	accepted := map[string]sourceItemRef{
		"https://github.com/acme/widgets/issues/17":                 {Owner: "acme", Repo: "widgets", Number: 17},
		"https://github.com/acme/widgets/issues/17/":                {Owner: "acme", Repo: "widgets", Number: 17},
		"https://github.com/acme/widgets/issues/17#issuecomment-55": {Owner: "acme", Repo: "widgets", Number: 17},
		"https://github.com/acme/widgets/issues/17?notification=1":  {Owner: "acme", Repo: "widgets", Number: 17},
		"https://github.com/acme/widgets/pull/18":                   {Owner: "acme", Repo: "widgets", Number: 18, PullRequest: true},
		"https://github.com/acme/widgets/pull/18/files":             {Owner: "acme", Repo: "widgets", Number: 18, PullRequest: true},
		"  https://github.com/Acme-Co/widgets.js/issues/3\n":        {Owner: "Acme-Co", Repo: "widgets.js", Number: 3},
	}
	for raw, want := range accepted {
		got, err := parseSourceItemURL(raw)
		if err != nil || got != want {
			t.Errorf("parse(%q) = %+v, %v; want %+v", raw, got, err, want)
		}
	}
	refused := []string{
		"",
		"http://github.com/acme/widgets/issues/17",
		"https://gitlab.com/acme/widgets/issues/17",
		"https://github.com.evil.example/acme/widgets/issues/17",
		"https://user@github.com/acme/widgets/issues/17",
		"https://github.com:444/acme/widgets/issues/17",
		"https://github.com/acme/widgets",
		"https://github.com/acme/widgets/issues",
		"https://github.com/acme/widgets/issues/0",
		"https://github.com/acme/widgets/issues/-3",
		"https://github.com/acme/widgets/issues/abc",
		"https://github.com/acme/widgets/issues/17abc",
		"https://github.com/acme/widgets/issues/17/files",
		"https://github.com/acme/widgets/issues/99999999999",
		"https://github.com/acme/widgets/commit/0123456789abcdef",
		"https://github.com/acme/../issues/17",
		"https://github.com/./widgets/issues/17",
		"https://github.com/acme/wid gets/issues/17",
		"https://github.com/" + strings.Repeat("a", 2100) + "/widgets/issues/17",
	}
	for _, raw := range refused {
		if got, err := parseSourceItemURL(raw); err == nil {
			t.Errorf("parse(%q) = %+v; want a refusal", raw, got)
		}
	}
}

// The item a session starts from names its branch, implies the worktree, and
// stays on the session as a link.
func TestSessionFromAnItemWorksInAWorktreeNamedAfterIt(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	for name, c := range map[string]struct{ given, canonical, stem string }{
		"issue":        {"https://github.com/acme/widgets/issues/17#issuecomment-9", "https://github.com/acme/widgets/issues/17", "issue-17-"},
		"pull request": {"https://github.com/acme/widgets/pull/18/files", "https://github.com/acme/widgets/pull/18", "pr-18-"},
	} {
		p := h.params(false) // no worktree asked for: the item implies it
		p.SourceURL = c.given
		dto, err := createProfiledTestRun(t, h.m, ctx, h.tenant, p)
		if err != nil {
			t.Fatalf("%s: launch: %v", name, err)
		}
		branch := "olivares/" + c.stem + branchSuffix(dto.RunRef)
		dir := filepath.Join(h.root, dto.RunRef)
		if dto.WorktreeBranch != branch || worktreeGitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD") != branch {
			t.Fatalf("%s: branch = %q (checked out %q); want %q", name, dto.WorktreeBranch, worktreeGitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"), branch)
		}
		if dto.SourceURL != c.canonical {
			t.Fatalf("%s: source_url = %q; want %q", name, dto.SourceURL, c.canonical)
		}
		stored, err := h.m.getRun(ctx, h.tenant, dto.RunRef)
		if err != nil || stored.SourceURL != c.canonical {
			t.Fatalf("%s: stored source_url = %q, %v; want %q", name, stored.SourceURL, err, c.canonical)
		}
	}
}

// What a start from an item cannot honestly do is refused before anything is made.
func TestSessionFromAnItemRefusals(t *testing.T) {
	t.Parallel()
	h, ctx := newWorktreeHarness(t)
	branchesBefore := h.branches()
	for name, c := range map[string]struct {
		edit    func(*CreateRunParams)
		status  int
		mention string
	}{
		"not an item page": {func(p *CreateRunParams) { p.SourceURL = "https://github.com/acme/widgets" }, http.StatusBadRequest, "source_url"},
		"another host":     {func(p *CreateRunParams) { p.SourceURL = "https://gitlab.com/acme/widgets/issues/1" }, http.StatusBadRequest, "source_url"},
		"with a repository url": {func(p *CreateRunParams) {
			p.SourceURL, p.RepositoryURL = "https://github.com/acme/widgets/issues/1", "https://github.com/acme/widgets"
		}, http.StatusBadRequest, "source_url"},
		"with no workspace": {func(p *CreateRunParams) { p.SourceURL, p.WorkspaceRef = "https://github.com/acme/widgets/issues/1", "" }, http.StatusUnprocessableEntity, "workspace"},
	} {
		p := h.params(false)
		c.edit(&p)
		if _, err := createProfiledTestRun(t, h.m, ctx, h.tenant, p); err == nil {
			t.Fatalf("%s: launch was accepted", name)
		} else {
			wantRunErr(t, err, c.status, c.mention)
		}
	}
	if got := h.branches(); got != branchesBefore {
		t.Fatalf("a refused start left branches behind: %q -> %q", branchesBefore, got)
	}
}

// fakeItemSource is a git-read custody that also reads items.
type fakeItemSource struct {
	*fakeGitRead
	callsMu sync.Mutex
	item    SourceItem
	err     error
	calls   []string // workspace|owner/repo#number
}

func (f *fakeItemSource) ReadSessionItem(_ context.Context, _ model.TenantID, workspace model.ID, owner, repo string, number int) (SourceItem, error) {
	f.callsMu.Lock()
	defer f.callsMu.Unlock()
	f.calls = append(f.calls, workspace.String()+"|"+owner+"/"+repo+"#"+strconv.Itoa(number))
	return f.item, f.err
}

func (f *fakeItemSource) called() []string {
	f.callsMu.Lock()
	defer f.callsMu.Unlock()
	return append([]string(nil), f.calls...)
}

// The route reads one item through the custody for a registered workspace, for a
// tenant administrator only, and says in a sentence why when it cannot.
func TestSourceItemRoute(t *testing.T) {
	repo := gitRepoFixture(t)
	src := &fakeItemSource{
		fakeGitRead: newFakeGitRead("https://github.com/acme/widgets.git"),
		item:        SourceItem{Number: 17, Title: "Crash on save", Body: "Steps:\n1. save", URL: "https://github.com/acme/widgets/issues/17"},
	}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	m.GitRead = src
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "item-http")
	editor := h.userToken(admin, tenant, "editor@acme.invalid", auth.RoleEditor)
	wr := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": repo, "name": "repo"}, tenantHdr(tenant))
	if wr.code != http.StatusCreated {
		t.Fatalf("workspace = %d %s", wr.code, wr.raw)
	}
	ws, _ := wr.body["workspace_ref"].(string)
	get := func(token, workspace, page string) resp {
		q := url.Values{"workspace_ref": {workspace}, "url": {page}}
		return h.do("GET", "/v1/m/sessions/source-items?"+q.Encode(), token, tenantHdr(tenant))
	}

	r := get(admin, ws, "https://github.com/acme/widgets/issues/17#issuecomment-1")
	if r.code != http.StatusOK || r.body["kind"] != "issue" || r.body["number"] != float64(17) || r.body["title"] != "Crash on save" ||
		r.body["body"] != "Steps:\n1. save" || r.body["url"] != "https://github.com/acme/widgets/issues/17" {
		t.Fatalf("issue = %d %s", r.code, r.raw)
	}
	if calls := src.called(); len(calls) != 1 || !strings.HasSuffix(calls[0], "|acme/widgets#17") || strings.HasPrefix(calls[0], "|") {
		t.Fatalf("custody calls = %v; want one read of acme/widgets#17 for the workspace", calls)
	}
	src.item = SourceItem{Number: 18, PullRequest: true, Title: "Fix save", URL: "https://github.com/acme/widgets/pull/18"}
	if r := get(admin, ws, "https://github.com/acme/widgets/pull/18/files"); r.code != http.StatusOK || r.body["kind"] != "pull_request" {
		t.Fatalf("pull request = %d %s", r.code, r.raw)
	}

	for name, c := range map[string]struct {
		token, workspace, page string
		err                    error
		status                 int
		mention                string
	}{
		"not an item page":     {admin, ws, "https://github.com/acme/widgets", nil, http.StatusBadRequest, "source_url"},
		"an unknown workspace": {admin, "ws-nope", "https://github.com/acme/widgets/issues/17", nil, http.StatusBadRequest, "workspace"},
		"no approved binding":  {admin, ws, "https://github.com/acme/widgets/issues/17", ErrGitReadNotApproved, http.StatusUnprocessableEntity, "acme/widgets"},
		"a host without reads": {admin, ws, "https://github.com/acme/widgets/issues/17", ErrGitReadUnsupported, http.StatusUnprocessableEntity, "GitHub"},
		"no such item":         {admin, ws, "https://github.com/acme/widgets/issues/99", ErrSourceItemNotFound, http.StatusNotFound, "#99"},
		"a host that failed":   {admin, ws, "https://github.com/acme/widgets/issues/17", errors.New("dial tcp 10.0.0.1: SECRET-DETAIL"), http.StatusServiceUnavailable, "could not be read"},
		"not a tenant admin":   {editor, ws, "https://github.com/acme/widgets/issues/17", nil, http.StatusForbidden, "tenant administration"},
		"no token at all":      {"", ws, "https://github.com/acme/widgets/issues/17", nil, http.StatusUnauthorized, ""},
	} {
		before := len(src.called())
		src.err = c.err
		r := get(c.token, c.workspace, c.page)
		if r.code != c.status || !strings.Contains(r.raw, c.mention) {
			t.Errorf("%s: %d %s; want %d mentioning %q", name, r.code, r.raw, c.status, c.mention)
		}
		if strings.Contains(r.raw, "SECRET-DETAIL") {
			t.Errorf("%s: the host's error text reached the answer: %s", name, r.raw)
		}
		if c.status != http.StatusUnprocessableEntity && c.status != http.StatusNotFound && c.status != http.StatusServiceUnavailable && len(src.called()) != before {
			t.Errorf("%s: the custody was asked although the request was refused first", name)
		}
	}

	m.GitRead = nil
	src.err = nil
	if r := get(admin, ws, "https://github.com/acme/widgets/issues/17"); r.code != http.StatusServiceUnavailable || !strings.Contains(r.raw, "custody") {
		t.Errorf("no custody wired = %d %s; want 503 naming the custody", r.code, r.raw)
	}
}

// The create body declares source_url, and it stays optional.
func TestSessionsSourceURLIsDeclaredAndOptional(t *testing.T) {
	t.Parallel()
	create := sessionsCreateRunSchema()
	if branches, ok := create["anyOf"].([]any); ok {
		create, _ = branches[0].(map[string]any)
	}
	props, _ := create["properties"].(map[string]any)
	if _, ok := props["source_url"]; !ok {
		t.Fatal("the run create schema does not declare the source_url the decoder accepts")
	}
	for _, name := range oastest.Strings(create["required"]) {
		if name == "source_url" {
			t.Fatal("source_url must stay optional: a launch without it is the launch it always was")
		}
	}
}
