// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk/model"
)

// buildPushEdges maps a push webhook event to EdgeObservations. It emits
// one edge per deduplicated (origin, repo, branch, mode) tuple, checking
// commits for AI agent Co-Authored-By markers and the pusher against bot
// accounts.
func (s *Source) buildPushEdges(ev pushEvent) []model.EdgeObservation {
	branch := branchFromRef(ev.Ref)
	if branch == "" {
		return nil
	}

	// Collect all Co-Authored-By names across commits.
	var coAuthors []string
	for _, c := range ev.Commits {
		coAuthors = append(coAuthors, parseCoAuthors(c.Message)...)
	}

	// Check for AI agent markers in Co-Authored-By trailers.
	agentMarker := ""
	for _, name := range coAuthors {
		if _, ok := s.agentMarkers[strings.ToLower(name)]; ok {
			agentMarker = strings.ToLower(name)
			break
		}
	}

	originKind := identity.OriginKind
	originRef := ev.Pusher.Login
	confidence := model.ConfidenceAttributed

	if _, isBot := s.botAccounts[strings.ToLower(ev.Pusher.Login)]; isBot {
		// Bot account: attributed to the agent directly.
		originKind = "agent"
		originRef = ev.Pusher.Login
		confidence = model.ConfidenceAttributed
	} else if agentMarker != "" {
		// Co-Authored-By marker: the push involved an AI agent, but the
		// human is the pusher — attribution is approximate.
		originKind = "agent"
		originRef = agentMarker
		confidence = model.ConfidenceApproximate
	}

	return []model.EdgeObservation{{
		OriginKind:   originKind,
		OriginRef:    originRef,
		ResourceKind: "github.repo",
		ResourceRef:  ev.Repository.FullName,
		Mode:         model.ModeWrite,
		Source:       model.SignalGitHub,
		Confidence:   confidence,
		ObservedAt:   time.Now().UTC(),
		Labels:       map[string]string{"branch": branch},
	}}
}

// buildPREdges maps a pull_request webhook event to EdgeObservations. Only
// opened and merged actions emit edges.
func (s *Source) buildPREdges(ev pullRequestEvent) []model.EdgeObservation {
	switch ev.Action {
	case "opened", "closed":
		// For "closed", only emit if the PR was actually merged.
		if ev.Action == "closed" && !ev.PullRequest.Merged {
			return nil
		}
	default:
		return nil
	}

	originKind := identity.OriginKind
	originRef := ev.Sender.Login
	confidence := model.ConfidenceAttributed

	if _, isBot := s.botAccounts[strings.ToLower(ev.Sender.Login)]; isBot {
		originKind = "agent"
		confidence = model.ConfidenceAttributed
	}

	branch := ev.PullRequest.Head.Ref
	if ev.PullRequest.Merged {
		branch = ev.PullRequest.Base.Ref
	}

	return []model.EdgeObservation{{
		OriginKind:   originKind,
		OriginRef:    originRef,
		ResourceKind: "github.repo",
		ResourceRef:  ev.Repository.FullName,
		Mode:         model.ModeWrite,
		Source:       model.SignalGitHub,
		Confidence:   confidence,
		ObservedAt:   time.Now().UTC(),
		Labels:       map[string]string{"branch": branch},
	}}
}

// buildReconEdges creates a read edge for a repo/branch discovered during
// polling reconciliation. The poller cannot see who pushed, so it emits a
// repo-level read edge attributed to the org.
func (s *Source) buildReconEdges(repoFullName, branch string) []model.EdgeObservation {
	return []model.EdgeObservation{{
		OriginKind:   identity.OriginKind,
		OriginRef:    "org:" + s.org,
		ResourceKind: "github.repo",
		ResourceRef:  repoFullName,
		Mode:         model.ModeRead,
		Source:       model.SignalGitHub,
		Confidence:   model.ConfidenceApproximate,
		ObservedAt:   time.Now().UTC(),
		Labels:       map[string]string{"branch": branch},
	}}
}

// branchFromRef strips the "refs/heads/" prefix from a git ref, returning
// the branch name. Returns "" for non-branch refs (e.g. tags).
func branchFromRef(ref string) string {
	const prefix = "refs/heads/"
	if strings.HasPrefix(ref, prefix) {
		return ref[len(prefix):]
	}
	return ""
}

// parseCoAuthors extracts the human-readable names from Co-Authored-By
// trailers in a commit message. The trailer format is:
//
//	Co-Authored-By: Name <email>
//
// Only the name part is returned; emails are never emitted.
func parseCoAuthors(message string) []string {
	var names []string
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "co-authored-by:") {
			continue
		}
		// Strip the "Co-Authored-By:" prefix.
		rest := strings.TrimSpace(line[len("co-authored-by:"):])
		// Extract name before the angle bracket.
		if idx := strings.Index(rest, "<"); idx > 0 {
			name := strings.TrimSpace(rest[:idx])
			if name != "" {
				names = append(names, name)
			}
		} else if rest != "" {
			names = append(names, rest)
		}
	}
	return names
}
