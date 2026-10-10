// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package githostdiff

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/sdk"
)

const (
	baseSHA = "1111111111111111111111111111111111111111"
	headSHA = "2222222222222222222222222222222222222222"
	treeSHA = "3333333333333333333333333333333333333333"
)

// fakeRoster is the source roster: the rows the engine lists, with their status.
type fakeRoster struct {
	entries []api.SourceRosterEntry
	err     error
}

func (f fakeRoster) ListSources(context.Context) ([]api.SourceRosterEntry, error) {
	return f.entries, f.err
}

// fakeResolver resolves "store:" references to "resolved:" values and records
// what it was asked to resolve.
type fakeResolver struct {
	err   error
	desc  sdk.Descriptor
	asked sdk.Config
}

func (f *fakeResolver) Resolve(_ context.Context, desc sdk.Descriptor, cfg sdk.Config) (sdk.Config, error) {
	f.desc, f.asked = desc, cfg
	if f.err != nil {
		return sdk.Config{}, f.err
	}
	out := map[string]string{}
	for k, v := range cfg.Settings {
		out[k] = strings.Replace(v, "store:", "resolved:", 1)
	}
	return sdk.Config{Settings: out}, nil
}

// fakeConn is a GitHub or GitLab connector: it records how it was opened and read.
type fakeConn struct {
	name     string
	openErr  error
	readErr  error
	opened   sdk.Config
	closed   bool
	readArgs []string
}

func (c *fakeConn) Descriptor() sdk.Descriptor { return sdk.Descriptor{Name: c.name} }

func (c *fakeConn) Open(_ context.Context, cfg sdk.Config) error {
	c.opened = cfg
	return c.openErr
}

func (c *fakeConn) Close(context.Context) error {
	c.closed = true
	return nil
}

type fakeGitHub struct {
	fakeConn
	diff gp.ContentDiff
}

func (c *fakeGitHub) ReadContentDiff(_ context.Context, repository, base, head string) (gp.ContentDiff, error) {
	c.readArgs = []string{repository, base, head}
	return c.diff, c.readErr
}

type fakeGitLab struct {
	fakeConn
	diff gp.ContentDiff
}

func (c *fakeGitLab) ReadContentDiff(_ context.Context, repository, base, head string) (gp.ContentDiff, error) {
	c.readArgs = []string{repository, base, head}
	return c.diff, c.readErr
}

// rateLimited and tooLarge carry the two conditions the route reads by interface.
type rateLimited struct{}

func (rateLimited) Error() string      { return "host rate limited" }
func (rateLimited) RetryAfter() string { return "30" }

type tooLarge struct{}

func (tooLarge) Error() string        { return "diff too large" }
func (tooLarge) GitHostDiffTooLarge() {}

func running(name, kind string, cfg map[string]string) api.SourceRosterEntry {
	return api.SourceRosterEntry{Name: name, Kind: kind, Enabled: true, Status: "running", Config: cfg}
}

// harness builds a Reader over the roster with fake connectors and counts how
// many of each were built.
type harness struct {
	r        *Reader
	resolver *fakeResolver
	gh       *fakeGitHub
	gl       *fakeGitLab
	built    int
}

func newHarness(entries ...api.SourceRosterEntry) *harness {
	h := &harness{resolver: &fakeResolver{}, gh: &fakeGitHub{fakeConn: fakeConn{name: "olivares.github"}}, gl: &fakeGitLab{fakeConn: fakeConn{name: "olivares.gitlab"}}}
	h.r = New(fakeRoster{entries: entries}, h.resolver)
	h.r.newSource = func(kind gp.TargetKind) gp.DiffSource {
		h.built++
		if kind.Name() == "github" {
			return h.gh
		}
		return h.gl
	}
	return h
}

func query(source, host string) api.GitHostDiffQuery {
	return api.GitHostDiffQuery{Source: source, Host: host, Repository: "acme/app", Base: "main", Head: "feature"}
}

func TestGitHubSourceMapsEveryFieldAndReadsWithItsResolvedConfig(t *testing.T) {
	h := newHarness(running("gh", "github", map[string]string{"org": "acme", "pat": "store:gh-pat"}))
	h.gh.diff = gp.ContentDiff{
		Repository: "acme/app", Base: "main", Head: "feature",
		BaseCommit: baseSHA, HeadCommit: headSHA, HeadTree: treeSHA, Truncated: true,
		Files: []gp.DiffFile{
			{Path: "a.go", Status: "modified", Hunks: []string{"@@ -1 +1 @@\n-a\n+b"}},
			{Path: "new.go", PreviousPath: "old.go", Status: "renamed", Truncated: true, Hunks: []string{}},
			{Path: "logo.png", Status: "added", Binary: true},
		},
	}
	got, err := h.r.ReadContentDiff(context.Background(), query("gh", "github"))
	if err != nil {
		t.Fatalf("read = %v", err)
	}
	want := api.GitHostDiff{
		Repository: "acme/app", Base: "main", Head: "feature",
		BaseCommit: baseSHA, HeadCommit: headSHA, HeadTree: treeSHA, Truncated: true,
		Files: []api.GitHostDiffFile{
			{Path: "a.go", Status: "modified", Hunks: []string{"@@ -1 +1 @@\n-a\n+b"}},
			{Path: "new.go", PreviousPath: "old.go", Status: "renamed", Truncated: true, Hunks: []string{}},
			{Path: "logo.png", Status: "added", Binary: true},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diff = %+v\nwant %+v", got, want)
	}
	if h.resolver.desc.Name != "olivares.github" || h.resolver.asked.Get("pat") != "store:gh-pat" {
		t.Fatalf("resolver asked with %+v %+v, want the connector's descriptor and the row's config", h.resolver.desc, h.resolver.asked)
	}
	if h.gh.opened.Get("pat") != "resolved:gh-pat" || h.gh.opened.Get("org") != "acme" {
		t.Fatalf("connector opened with %+v, want the resolved row config", h.gh.opened.Settings)
	}
	if !reflect.DeepEqual(h.gh.readArgs, []string{"acme/app", "main", "feature"}) {
		t.Fatalf("read args = %v", h.gh.readArgs)
	}
	if !h.gh.closed {
		t.Fatal("the connector was not closed after the read")
	}
}

func TestGitLabSourceKeepsTheResolvedCommitsWithoutAHeadTree(t *testing.T) {
	h := newHarness(running("gl", "gitlab", map[string]string{"group": "acme", "token": "store:gl"}))
	h.gl.diff = gp.ContentDiff{
		Repository: "acme/app", Base: "main", Head: "feature", BaseCommit: baseSHA, HeadCommit: headSHA,
		Files: []gp.DiffFile{{Path: "a.go", Status: "modified", Hunks: []string{"@@ -1 +1 @@"}}},
	}
	got, err := h.r.ReadContentDiff(context.Background(), query("gl", "gitlab"))
	if err != nil {
		t.Fatalf("read = %v", err)
	}
	if got.BaseCommit != baseSHA || got.HeadCommit != headSHA || got.HeadTree != "" {
		t.Fatalf("identity = %q %q %q, want the two commits and no tree", got.BaseCommit, got.HeadCommit, got.HeadTree)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "a.go" || got.Files[0].Hunks[0] != "@@ -1 +1 @@" {
		t.Fatalf("files = %+v", got.Files)
	}
	if h.gl.opened.Get("token") != "resolved:gl" || !h.gl.closed {
		t.Fatalf("gitlab connector opened with %+v closed=%v", h.gl.opened.Settings, h.gl.closed)
	}
}

// A source the roster does not name, of another kind, not running, disabled or served
// by an external plugin is never guessed at: the answer is unknown_ref and no
// connector is built.
func TestOnlyARunningFirstPartySourceOfTheRequestedHostIsRead(t *testing.T) {
	cases := []struct {
		name  string
		entry api.SourceRosterEntry
		q     api.GitHostDiffQuery
	}{
		{"unknown source", running("gh", "github", nil), query("other", "github")},
		{"github source asked as gitlab", running("gh", "github", nil), query("gh", "gitlab")},
		{"gitlab source asked as github", running("gl", "gitlab", nil), query("gl", "github")},
		{"another kind", running("mcp1", "mcp", nil), query("mcp1", "github")},
		{"disabled", api.SourceRosterEntry{Name: "gh", Kind: "github", Enabled: false, Status: "disabled"}, query("gh", "github")},
		{"not running", api.SourceRosterEntry{Name: "gh", Kind: "github", Enabled: true, Status: "failed"}, query("gh", "github")},
		{"external plugin", api.SourceRosterEntry{Name: "gh", Kind: "github", Enabled: true, Status: "running", Plugin: &api.SourcePluginInput{Path: "/opt/gh"}}, query("gh", "github")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(tc.entry)
			_, err := h.r.ReadContentDiff(context.Background(), tc.q)
			if !errors.Is(err, api.ErrContentDiffUnknownRef) {
				t.Fatalf("err = %v, want unknown ref", err)
			}
			if h.built != 0 {
				t.Fatalf("%d connector(s) built for a source that is not read", h.built)
			}
		})
	}
}

func TestConnectorErrorsBecomeTheRouteErrors(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		readErr error
		check   func(error) bool
	}{
		{"github unknown ref", "github", gp.ErrDiffUnknownRef, func(e error) bool { return errors.Is(e, api.ErrContentDiffUnknownRef) }},
		{"github forbidden", "github", gp.ErrDiffForbidden, func(e error) bool { return errors.Is(e, api.ErrContentDiffForbidden) }},
		{"github upstream", "github", errors.New("github upstream"), func(e error) bool { return errors.Is(e, api.ErrContentDiffUpstream) }},
		{"gitlab unknown ref", "gitlab", gp.ErrDiffUnknownRef, func(e error) bool { return errors.Is(e, api.ErrContentDiffUnknownRef) }},
		{"gitlab forbidden", "gitlab", gp.ErrDiffForbidden, func(e error) bool { return errors.Is(e, api.ErrContentDiffForbidden) }},
		{"gitlab upstream", "gitlab", errors.New("gitlab upstream"), func(e error) bool { return errors.Is(e, api.ErrContentDiffUpstream) }},
		{"too large", "github", tooLarge{}, func(e error) bool { var x interface{ GitHostDiffTooLarge() }; return errors.As(e, &x) }},
		{"rate limited", "gitlab", rateLimited{}, func(e error) bool {
			var x interface{ RetryAfter() string }
			return errors.As(e, &x) && x.RetryAfter() == "30"
		}},
		{"anything else", "github", errors.New("connection reset"), func(e error) bool { return errors.Is(e, api.ErrContentDiffUpstream) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(running("gh", "github", nil), running("gl", "gitlab", nil))
			h.gh.readErr, h.gl.readErr = tc.readErr, tc.readErr
			src := map[string]string{"github": "gh", "gitlab": "gl"}[tc.host]
			_, err := h.r.ReadContentDiff(context.Background(), query(src, tc.host))
			if err == nil || !tc.check(err) {
				t.Fatalf("err = %v, not the route's error for %s", err, tc.name)
			}
		})
	}
}

// A roster, secret or Open failure is an upstream error that carries none of the
// failure's own text: an Open error ran against the resolved configuration.
func TestSetupFailuresAreUpstreamWithoutTheirDetail(t *testing.T) {
	const secretText = "tok-SECRET-VALUE"
	t.Run("roster", func(t *testing.T) {
		h := newHarness()
		h.r = New(fakeRoster{err: errors.New("store down " + secretText)}, h.resolver)
		_, err := h.r.ReadContentDiff(context.Background(), query("gh", "github"))
		if !errors.Is(err, api.ErrContentDiffUpstream) || strings.Contains(err.Error(), secretText) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("resolver", func(t *testing.T) {
		h := newHarness(running("gh", "github", nil))
		h.resolver.err = errors.New("vault said " + secretText)
		_, err := h.r.ReadContentDiff(context.Background(), query("gh", "github"))
		if !errors.Is(err, api.ErrContentDiffUpstream) || strings.Contains(err.Error(), secretText) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("open", func(t *testing.T) {
		h := newHarness(running("gh", "github", nil))
		h.gh.openErr = errors.New("bad pat " + secretText)
		_, err := h.r.ReadContentDiff(context.Background(), query("gh", "github"))
		if !errors.Is(err, api.ErrContentDiffUpstream) || strings.Contains(err.Error(), secretText) {
			t.Fatalf("err = %v", err)
		}
		if !h.gh.closed {
			t.Fatal("a connector whose Open failed was not closed")
		}
		if h.gh.readArgs != nil {
			t.Fatal("a connector whose Open failed was read")
		}
	})
}

func TestTheRealConnectorsAreTheSeams(t *testing.T) {
	r := New(fakeRoster{}, &fakeResolver{})
	for _, name := range []string{"github", "gitlab"} {
		kind, _ := gp.LookupTargetKind(name)
		conn := r.newSource(kind)
		if conn.Descriptor().Name != "olivares."+name+"-source" {
			t.Fatalf("%s observer = %s", name, conn.Descriptor().Name)
		}
	}
	plain, _ := gp.LookupTargetKind("git")
	if r.newSource(plain) != nil {
		t.Fatal("plain git has no diff observer")
	}
	var _ api.ContentDiffReader = r
}
