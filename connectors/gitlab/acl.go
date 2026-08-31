// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// syncACL lists all projects in the group and their members, emitting
// permitted edges based on GitLab access levels.
func (s *Source) syncACL(ctx context.Context, sink sdk.Sink) error {
	projects, err := s.listProjects(ctx)
	if err != nil {
		return err
	}

	// Emit edges for project-level members.
	for _, p := range projects {
		members, err := s.listProjectMembers(ctx, p.ID)
		if err != nil {
			return err
		}
		for _, m := range members {
			edge := buildACLEdge(m.Username, p.PathWithNamespace, m.AccessLevel)
			if err := sink.Emit(ctx, edge); err != nil {
				return err
			}
		}
	}

	// Emit edges for group-level members across all projects.
	groupMembers, err := s.listGroupMembers(ctx)
	if err != nil {
		return err
	}
	for _, m := range groupMembers {
		for _, p := range projects {
			edge := buildACLEdge(m.Username, p.PathWithNamespace, m.AccessLevel)
			if err := sink.Emit(ctx, edge); err != nil {
				return err
			}
		}
	}

	return nil
}

func buildACLEdge(username, pathWithNamespace string, accessLevel int) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    "user:" + username,
		ResourceKind: "gitlab.project",
		ResourceRef:  pathWithNamespace,
		Mode:         accessLevelToMode(accessLevel),
		Source:       model.SignalPolicy,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   time.Now().UTC(),
	}
}

// accessLevelToMode maps GitLab access levels to the access-map's R/RW model.
func accessLevelToMode(level int) model.AccessMode {
	switch {
	case level >= accessMaintainer:
		return model.ModeReadWrite
	case level >= accessDeveloper:
		return model.ModeWrite
	case level >= accessReporter:
		return model.ModeRead
	default:
		return model.ModeRead
	}
}
