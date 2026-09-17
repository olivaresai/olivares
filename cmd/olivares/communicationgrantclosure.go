// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

const channelGrantClosureEvidenceRef = "sessions.channel_grant_subjects.v1"

// communicationGrantClosureResolver computes, server-side and under the
// directory epoch fence, the complete set of ChannelGrant subjects an
// authenticated principal may match: itself plus every current indirect
// subject (user groups for a User, agent groups for an Agent, and for a
// communication session its own session subject plus the agent it acts for).
// A request body never supplies any of it.
//
// The closure is a DENIAL when the principal is not a current directory
// principal (missing, inactive, non-member, retired, or a session whose exact
// Claim tuple no longer holds), and UNKNOWN only when the directory could not
// be observed. An agent credential is its own identity: the owning user is
// never added as a subject.
type communicationGrantClosureResolver struct {
	resolver *communicationDirectoryResolver
}

func newCommunicationGrantClosureResolver(
	resolver *communicationDirectoryResolver,
) *communicationGrantClosureResolver {
	return &communicationGrantClosureResolver{resolver: resolver}
}

func (c *communicationGrantClosureResolver) ResolveChannelGrantSubjects(
	ctx context.Context,
	scope sessions.DirectoryScopeRef,
	principal sessions.CommunicationPrincipal,
) (sessions.ChannelGrantSubjectClosure, error) {
	if c == nil || c.resolver == nil || c.resolver.reads == nil {
		return sessions.ChannelGrantSubjectClosure{}, fmt.Errorf(
			"%w: channel grant closure resolver is not bound", store.ErrDirectoryUnavailable)
	}
	if err := sessions.ValidateCommunicationPrincipalForScope(principal, scope); err != nil {
		return sessions.ChannelGrantSubjectClosure{}, err
	}
	r := c.resolver
	observedAt := r.observedAt()
	closure := sessions.ChannelGrantSubjectClosure{
		Scope: scope, Principal: principal, Outcome: sessions.ReadUnknown, Code: "subjects_unresolved",
		ObservedAt: observedAt, FreshUntil: observedAt.Add(r.freshness),
		EvidenceRef: channelGrantClosureEvidenceRef,
	}
	if principal.System {
		if principal.SystemGrantAgentID.IsZero() {
			closure.Outcome, closure.Code = sessions.ReadDeny, "system_agent_binding_missing"
		}
		epoch, err := r.fenced(ctx, scope.TenantID, func(context.Context, int64) error { return nil })
		if err != nil {
			return sessions.ChannelGrantSubjectClosure{}, err
		}
		closure.DirectoryEpoch = epoch
		if closure.Outcome == sessions.ReadDeny {
			return closure, nil
		}
		closure.Outcome, closure.Code = sessions.ReadAllow, "system_grant_agent"
		closure.Subjects = []sessions.CommunicationSubjectRef{{
			Kind: sessions.SubjectAgent, Ref: principal.SystemGrantAgentID.String(),
		}}
		return closure, nil
	}

	var (
		subjects []sessions.CommunicationSubjectRef
		denial   string
	)
	epoch, err := r.fenced(ctx, scope.TenantID, func(ctx context.Context, epoch int64) error {
		subjects, denial = nil, ""
		switch {
		case principal.SessionID != "":
			recipient := sessions.RecipientRef{Kind: sessions.RecipientSession, Ref: principal.SessionID}
			resolution, err := r.resolveRecipientWithin(ctx, scope, recipient, epoch)
			if err != nil {
				return err
			}
			switch {
			case !resolution.snapshot.Eligible:
				denial = resolution.code
			case resolution.sessionFence != principal.SessionFence:
				denial = "session_claim_stale"
			case resolution.sessionRun != principal.SessionRunRef:
				denial = "session_run_mismatch"
			case principal.AgentExternalID != "" && resolution.sessionAgent != principal.AgentExternalID:
				denial = "session_agent_mismatch"
			}
			if denial != "" {
				return nil
			}
			subjects = append(subjects, sessions.CommunicationSubjectRef{
				Kind: sessions.SubjectSession, Ref: principal.SessionID,
			})
			if resolution.sessionAgent == "" {
				return nil
			}
			// The session acts for its run's agent. Its grants apply only while
			// that agent is itself a current principal of the workspace.
			identityID, found, err := r.reads.agentByExternalID(ctx, scope.TenantID, resolution.sessionAgent)
			if err != nil || !found {
				return err
			}
			agent, err := r.resolveRecipientWithin(ctx, scope,
				sessions.RecipientRef{Kind: sessions.RecipientAgent, Ref: identityID.String()}, epoch)
			if err != nil || !agent.snapshot.Eligible {
				return err
			}
			return appendAgentSubjects(ctx, r.reads, scope, identityID, &subjects)
		case principal.AgentExternalID != "":
			identityID, found, err := r.reads.agentByExternalID(ctx, scope.TenantID, principal.AgentExternalID)
			if err != nil {
				return err
			}
			if !found {
				denial = "principal_not_found"
				return nil
			}
			resolution, err := r.resolveRecipientWithin(ctx, scope,
				sessions.RecipientRef{Kind: sessions.RecipientAgent, Ref: identityID.String()}, epoch)
			if err != nil {
				return err
			}
			if !resolution.snapshot.Eligible {
				denial = resolution.code
				return nil
			}
			return appendAgentSubjects(ctx, r.reads, scope, identityID, &subjects)
		default:
			resolution, err := r.resolveRecipientWithin(ctx, scope,
				sessions.RecipientRef{Kind: sessions.RecipientUser, Ref: principal.UserID.String()}, epoch)
			if err != nil {
				return err
			}
			if !resolution.snapshot.Eligible {
				denial = resolution.code
				return nil
			}
			subjects = append(subjects, sessions.CommunicationSubjectRef{
				Kind: sessions.SubjectUser, Ref: principal.UserID.String(),
			})
			groups, err := r.reads.userGroups(ctx, scope.TenantID, principal.UserID)
			if err != nil {
				return err
			}
			for _, group := range groups {
				subjects = append(subjects, sessions.CommunicationSubjectRef{
					Kind: sessions.SubjectUserGroup, Ref: group.String(),
				})
			}
			return nil
		}
	})
	if err != nil {
		return sessions.ChannelGrantSubjectClosure{}, err
	}
	closure.DirectoryEpoch = epoch
	if denial != "" {
		closure.Outcome, closure.Code = sessions.ReadDeny, denial
		return closure, nil
	}
	closure.Outcome, closure.Code = sessions.ReadAllow, "subjects_resolved"
	closure.Subjects = uniqueSortedSubjects(subjects)
	return closure, nil
}

func appendAgentSubjects(
	ctx context.Context,
	reads *communicationDirectoryReads,
	scope sessions.DirectoryScopeRef,
	identityID model.ID,
	subjects *[]sessions.CommunicationSubjectRef,
) error {
	*subjects = append(*subjects, sessions.CommunicationSubjectRef{
		Kind: sessions.SubjectAgent, Ref: identityID.String(),
	})
	groups, err := reads.agentGroups(ctx, scope.TenantID, scope.WorkspaceID, identityID)
	if err != nil {
		return err
	}
	for _, group := range groups {
		*subjects = append(*subjects, sessions.CommunicationSubjectRef{
			Kind: sessions.SubjectAgentGroup, Ref: group.String(),
		})
	}
	return nil
}

func uniqueSortedSubjects(subjects []sessions.CommunicationSubjectRef) []sessions.CommunicationSubjectRef {
	seen := make(map[sessions.CommunicationSubjectRef]struct{}, len(subjects))
	out := make([]sessions.CommunicationSubjectRef, 0, len(subjects))
	for _, subject := range subjects {
		if _, duplicate := seen[subject]; duplicate {
			continue
		}
		seen[subject] = struct{}{}
		out = append(out, subject)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Ref < out[j].Ref
	})
	return out
}

var _ sessions.ChannelGrantSubjectClosureResolver = (*communicationGrantClosureResolver)(nil)
