// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// syncACL polls the GitHub API for collaborator and team permissions across
// all org repos and emits permitted edges on the SignalPolicy path.
func (s *Source) syncACL(ctx context.Context, sink sdk.Sink) error {
	repos, err := s.listRepos(ctx)
	if err != nil {
		return err
	}

	for _, r := range repos {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.syncRepoCollaborators(ctx, sink, r.FullName); err != nil {
			return err
		}
	}

	teams, err := s.listTeams(ctx)
	if err != nil {
		return err
	}
	for _, t := range teams {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.syncTeamRepos(ctx, sink, t.Slug); err != nil {
			return err
		}
	}

	return nil
}

// syncRepoCollaborators emits a permitted edge for each collaborator on a
// repository.
func (s *Source) syncRepoCollaborators(ctx context.Context, sink sdk.Sink, repoFullName string) error {
	collabs, err := s.listCollaborators(ctx, repoFullName)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, c := range collabs {
		edge := model.EdgeObservation{
			OriginKind:   identity.OriginKind,
			OriginRef:    "user:" + c.Login,
			ResourceKind: "github.repo",
			ResourceRef:  repoFullName,
			Mode:         permissionToMode(c.Permissions),
			Source:       model.SignalPolicy,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   now,
		}
		if err := sink.Emit(ctx, edge); err != nil {
			return err
		}
	}
	return nil
}

// syncTeamRepos emits a permitted edge for each repo a team has access to.
func (s *Source) syncTeamRepos(ctx context.Context, sink sdk.Sink, teamSlug string) error {
	repos, err := s.listTeamRepos(ctx, teamSlug)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, r := range repos {
		edge := model.EdgeObservation{
			OriginKind:   identity.OriginKind,
			OriginRef:    "team:" + teamSlug,
			ResourceKind: "github.repo",
			ResourceRef:  r.FullName,
			Mode:         permissionToMode(r.Permissions),
			Source:       model.SignalPolicy,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   now,
		}
		if err := sink.Emit(ctx, edge); err != nil {
			return err
		}
	}
	return nil
}

// permissionToMode maps a GitHub permission set to an access mode.
func permissionToMode(p permissionSet) model.AccessMode {
	if p.Admin {
		return model.ModeReadWrite
	}
	if p.Push {
		return model.ModeWrite
	}
	if p.Pull {
		return model.ModeRead
	}
	return model.ModeUnknown
}
