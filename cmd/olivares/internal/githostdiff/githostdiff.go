// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package githostdiff serves the console's source diff route
// (GET /v1/console/sources/diff) from the engine's own GitHub and GitLab sources.
//
// The route names a roster source and a host. The reader answers only for a source
// the roster lists as enabled and running, served by the first-party connector of
// that host, and reads through that connector opened with the source's own
// resolved configuration: the configuration the running source was opened with.
// Both connectors' Open only reads configuration and their Close releases nothing,
// so each read opens a connector for itself and closes it after.
//
// Any other source is the route's unknown_ref, never a guess: a name the roster
// does not list, another kind, the other host's connector, a source that is
// disabled or not running, or one an external plugin serves.
package githostdiff

import (
	"context"
	"errors"

	githubsrc "github.com/olivaresai/olivares/connectors/github"
	gitlabsrc "github.com/olivaresai/olivares/connectors/gitlab"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/sdk"
)

// Roster lists the engine's configured sources with the status each one runs in.
// The engine's source reconciler implements it.
type Roster interface {
	ListSources(ctx context.Context) ([]api.SourceRosterEntry, error)
}

// Resolver resolves the secret references in a source's configuration, as the
// engine does when it opens the source. The engine's secret resolver implements it.
type Resolver interface {
	Resolve(ctx context.Context, desc sdk.Descriptor, cfg sdk.Config) (sdk.Config, error)
}

// runningStatus is the roster status of a source the runtime runs.
const runningStatus = "running"

// Source kinds, as the roster names them, for the two hosts the route accepts.
const (
	kindGitHub = "github"
	kindGitLab = "gitlab"
)

// connector is what the reader needs of a GitHub or GitLab source besides its read.
type connector interface {
	Descriptor() sdk.Descriptor
	Open(ctx context.Context, cfg sdk.Config) error
	Close(ctx context.Context) error
}

type githubSource interface {
	connector
	ReadContentDiff(ctx context.Context, repository, base, head string) (githubsrc.ContentDiff, error)
}

type gitlabSource interface {
	connector
	ReadContentDiff(ctx context.Context, repository, base, head string) (gitlabsrc.ContentDiff, error)
}

var (
	_ githubSource = (*githubsrc.Source)(nil)
	_ gitlabSource = (*gitlabsrc.Source)(nil)
)

// The engine hands New its source reconciler, an api.SourceRoster, and its secret
// resolver; these assertions keep that wiring compiling where it is written.
var (
	_ Roster   = api.SourceRoster(nil)
	_ Resolver = (*secret.Resolver)(nil)
)

// Reader is the route's api.ContentDiffReader.
type Reader struct {
	roster   Roster
	resolver Resolver
	// newGitHub and newGitLab build a fresh connector; tests replace them.
	newGitHub func() githubSource
	newGitLab func() gitlabSource
}

var _ api.ContentDiffReader = (*Reader)(nil)

// New returns the reader over the engine's roster and secret resolver.
func New(roster Roster, resolver Resolver) *Reader {
	return &Reader{
		roster:    roster,
		resolver:  resolver,
		newGitHub: func() githubSource { return githubsrc.New() },
		newGitLab: func() gitlabSource { return gitlabsrc.New() },
	}
}

// ReadContentDiff reads the bounded diff of q from the source it names. The result
// carries the refs as requested, the commits they resolved to and, on GitHub, the
// head commit's tree; the route checks that identity again before it answers.
func (r *Reader) ReadContentDiff(ctx context.Context, q api.GitHostDiffQuery) (api.GitHostDiff, error) {
	entry, err := r.runningSource(ctx, q.Source)
	if err != nil {
		return api.GitHostDiff{}, err
	}
	if entry.Kind != q.Host {
		return api.GitHostDiff{}, api.ErrContentDiffUnknownRef
	}
	switch q.Host {
	case kindGitHub:
		conn := r.newGitHub()
		if err := r.open(ctx, conn, entry); err != nil {
			return api.GitHostDiff{}, err
		}
		defer func() { _ = conn.Close(ctx) }()
		d, err := conn.ReadContentDiff(ctx, q.Repository, q.Base, q.Head)
		if err != nil {
			return api.GitHostDiff{}, routeError(err, githubsrc.ErrUnknownRef, githubsrc.ErrForbidden)
		}
		return fromGitHub(d), nil
	case kindGitLab:
		conn := r.newGitLab()
		if err := r.open(ctx, conn, entry); err != nil {
			return api.GitHostDiff{}, err
		}
		defer func() { _ = conn.Close(ctx) }()
		d, err := conn.ReadContentDiff(ctx, q.Repository, q.Base, q.Head)
		if err != nil {
			return api.GitHostDiff{}, routeError(err, gitlabsrc.ErrUnknownRef, gitlabsrc.ErrForbidden)
		}
		return fromGitLab(d), nil
	}
	return api.GitHostDiff{}, api.ErrContentDiffUnknownRef
}

// runningSource returns the roster row named name when it is enabled, running and
// served in-process by the connector its kind names. A roster that cannot be read
// is an upstream error; any other refusal is unknown_ref.
func (r *Reader) runningSource(ctx context.Context, name string) (api.SourceRosterEntry, error) {
	entries, err := r.roster.ListSources(ctx)
	if err != nil {
		return api.SourceRosterEntry{}, api.ErrContentDiffUpstream
	}
	for _, e := range entries {
		if e.Name != name {
			continue
		}
		if !e.Enabled || e.Status != runningStatus || e.Plugin != nil {
			return api.SourceRosterEntry{}, api.ErrContentDiffUnknownRef
		}
		return e, nil
	}
	return api.SourceRosterEntry{}, api.ErrContentDiffUnknownRef
}

// open resolves entry's configuration for conn and opens conn with it. Either
// failure is an upstream error without its text: a resolver error can name a
// secret backend, and an Open error ran against the resolved configuration.
func (r *Reader) open(ctx context.Context, conn connector, entry api.SourceRosterEntry) error {
	cfg, err := r.resolver.Resolve(ctx, conn.Descriptor(), sdk.Config{Settings: entry.Config})
	if err != nil {
		return api.ErrContentDiffUpstream
	}
	if err := conn.Open(ctx, cfg); err != nil {
		_ = conn.Close(ctx)
		return api.ErrContentDiffUpstream
	}
	return nil
}

// routeError maps a connector read error to the route's errors. A diff past the
// read cap and a rate limit keep their own error, which the route reads by
// interface; anything else is upstream.
func routeError(err, unknownRef, forbidden error) error {
	switch {
	case errors.Is(err, unknownRef):
		return api.ErrContentDiffUnknownRef
	case errors.Is(err, forbidden):
		return api.ErrContentDiffForbidden
	}
	var tooLarge interface{ GitHostDiffTooLarge() }
	if errors.As(err, &tooLarge) {
		return err
	}
	var limited interface{ RetryAfter() string }
	if errors.As(err, &limited) {
		return err
	}
	return api.ErrContentDiffUpstream
}

func fromGitHub(d githubsrc.ContentDiff) api.GitHostDiff {
	files := make([]api.GitHostDiffFile, len(d.Files))
	for i, f := range d.Files {
		files[i] = api.GitHostDiffFile{
			Path: f.Path, PreviousPath: f.PreviousPath, Status: f.Status,
			Binary: f.Binary, Truncated: f.Truncated, Hunks: f.Hunks,
		}
	}
	return api.GitHostDiff{
		Repository: d.Repository, Base: d.Base, Head: d.Head,
		BaseCommit: d.BaseCommit, HeadCommit: d.HeadCommit, HeadTree: d.HeadTree,
		Truncated: d.Truncated, Files: files,
	}
}

func fromGitLab(d gitlabsrc.ContentDiff) api.GitHostDiff {
	files := make([]api.GitHostDiffFile, len(d.Files))
	for i, f := range d.Files {
		files[i] = api.GitHostDiffFile{
			Path: f.Path, PreviousPath: f.PreviousPath, Status: f.Status,
			Binary: f.Binary, Truncated: f.Truncated, Hunks: f.Hunks,
		}
	}
	return api.GitHostDiff{
		Repository: d.Repository, Base: d.Base, Head: d.Head,
		BaseCommit: d.BaseCommit, HeadCommit: d.HeadCommit, HeadTree: d.HeadTree,
		Truncated: d.Truncated, Files: files,
	}
}
