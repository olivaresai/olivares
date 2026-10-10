// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"errors"

	githubsrc "github.com/olivaresai/olivares/connectors/github"
	gitlabsrc "github.com/olivaresai/olivares/connectors/gitlab"
	"github.com/olivaresai/olivares/sdk"
)

// DiffSource is a read-only observer of a target kind. It opens the observer's
// configuration, independently of publication rows and their credentials.
type DiffSource interface {
	Descriptor() sdk.Descriptor
	Open(context.Context, sdk.Config) error
	Close(context.Context) error
	ReadContentDiff(context.Context, string, string, string) (ContentDiff, error)
}

// ContentDiff retains the observer's bounded result without engine types.
type ContentDiff struct {
	Repository, Base, Head, BaseCommit, HeadCommit, HeadTree string
	Truncated                                                bool
	Files                                                    []DiffFile
}
type DiffFile struct {
	Path         string
	Status       string
	Binary       bool
	Truncated    bool
	PreviousPath string
	Hunks        []string
}

var (
	ErrDiffUnknownRef = errors.New("gitpublish: diff unknown ref")
	ErrDiffForbidden  = errors.New("gitpublish: diff forbidden")
)

type githubDiffSource struct{ *githubsrc.Source }
type gitlabDiffSource struct{ *gitlabsrc.Source }

func (s githubDiffSource) ReadContentDiff(ctx context.Context, repo, base, head string) (ContentDiff, error) {
	d, err := s.Source.ReadContentDiff(ctx, repo, base, head)
	if err != nil {
		return ContentDiff{}, diffError(err, githubsrc.ErrUnknownRef, githubsrc.ErrForbidden)
	}
	files := make([]DiffFile, len(d.Files))
	for i, f := range d.Files {
		files[i] = DiffFile(f)
	}
	return ContentDiff{d.Repository, d.Base, d.Head, d.BaseCommit, d.HeadCommit, d.HeadTree, d.Truncated, files}, nil
}
func (s gitlabDiffSource) ReadContentDiff(ctx context.Context, repo, base, head string) (ContentDiff, error) {
	d, err := s.Source.ReadContentDiff(ctx, repo, base, head)
	if err != nil {
		return ContentDiff{}, diffError(err, gitlabsrc.ErrUnknownRef, gitlabsrc.ErrForbidden)
	}
	files := make([]DiffFile, len(d.Files))
	for i, f := range d.Files {
		files[i] = DiffFile(f)
	}
	return ContentDiff{d.Repository, d.Base, d.Head, d.BaseCommit, d.HeadCommit, d.HeadTree, d.Truncated, files}, nil
}
func diffError(err, unknownRef, forbidden error) error {
	switch {
	case errors.Is(err, unknownRef):
		return ErrDiffUnknownRef
	case errors.Is(err, forbidden):
		return ErrDiffForbidden
	}
	return err // rate-limit and read-cap interfaces stay intact
}
