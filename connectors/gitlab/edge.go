// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk/model"
)

// buildPushEdges creates EdgeObservation records from a push webhook event.
// It applies three-layer correlation: commit markers, bot accounts, then
// human identity fallback.
func (s *Source) buildPushEdges(ev pushHook) []model.EdgeObservation {
	branch := branchFromRef(ev.Ref)
	var edges []model.EdgeObservation

	// Collect agent markers from commit messages.
	var markers []string
	for _, c := range ev.Commits {
		markers = append(markers, parseCoAuthors(c.Message)...)
	}

	// Check for known agent markers (case-insensitive).
	for _, m := range markers {
		lower := strings.ToLower(m)
		if _, ok := s.agentMarkers[lower]; ok {
			edges = append(edges, model.EdgeObservation{
				OriginKind:   "agent",
				OriginRef:    lower,
				ResourceKind: "gitlab.project",
				ResourceRef:  ev.Project.PathWithNamespace,
				Mode:         model.ModeWrite,
				Source:       model.SignalGitLab,
				Confidence:   model.ConfidenceApproximate,
				ObservedAt:   time.Now().UTC(),
				Labels:       map[string]string{"branch": branch},
			})
		}
	}

	// Bot account attribution.
	if _, ok := s.botAccounts[strings.ToLower(ev.UserLogin)]; ok {
		edges = append(edges, model.EdgeObservation{
			OriginKind:   "agent",
			OriginRef:    ev.UserLogin,
			ResourceKind: "gitlab.project",
			ResourceRef:  ev.Project.PathWithNamespace,
			Mode:         model.ModeWrite,
			Source:       model.SignalGitLab,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   time.Now().UTC(),
			Labels:       map[string]string{"branch": branch},
		})
		return edges
	}

	// Human identity fallback.
	if ev.UserLogin != "" {
		edges = append(edges, model.EdgeObservation{
			OriginKind:   identity.OriginKind,
			OriginRef:    ev.UserLogin,
			ResourceKind: "gitlab.project",
			ResourceRef:  ev.Project.PathWithNamespace,
			Mode:         model.ModeWrite,
			Source:       model.SignalGitLab,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   time.Now().UTC(),
			Labels:       map[string]string{"branch": branch},
		})
	}

	return edges
}

// buildMREdges creates EdgeObservation records from a merge request webhook event.
func (s *Source) buildMREdges(ev mergeRequestHook) []model.EdgeObservation {
	// Only emit edges for actionable MR events.
	switch ev.ObjectAttributes.Action {
	case "open", "merge", "close", "update":
	default:
		return nil
	}

	mode := model.ModeRead
	if ev.ObjectAttributes.Action == "merge" || ev.ObjectAttributes.Action == "open" {
		mode = model.ModeWrite
	}

	originKind := identity.OriginKind
	originRef := ev.User.Username
	confidence := model.ConfidenceAttributed

	if _, ok := s.botAccounts[strings.ToLower(ev.User.Username)]; ok {
		originKind = "agent"
		confidence = model.ConfidenceAttributed
	}

	branch := ev.ObjectAttributes.TargetBranch
	if branch == "" {
		branch = ev.ObjectAttributes.SourceBranch
	}

	return []model.EdgeObservation{{
		OriginKind:   originKind,
		OriginRef:    originRef,
		ResourceKind: "gitlab.project",
		ResourceRef:  ev.Project.PathWithNamespace,
		Mode:         mode,
		Source:       model.SignalGitLab,
		Confidence:   confidence,
		ObservedAt:   time.Now().UTC(),
		Labels:       map[string]string{"branch": branch},
	}}
}

// buildPollEdge creates a read-mode edge for a project+branch seen during
// periodic reconciliation polling.
func buildPollEdge(pathWithNamespace, branch string) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    "poll",
		ResourceKind: "gitlab.project",
		ResourceRef:  pathWithNamespace,
		Mode:         model.ModeRead,
		Source:       model.SignalGitLab,
		Confidence:   model.ConfidenceApproximate,
		ObservedAt:   time.Now().UTC(),
		Labels:       map[string]string{"branch": branch},
	}
}

// branchFromRef extracts the branch name from a Git ref (e.g. "refs/heads/main" → "main").
func branchFromRef(ref string) string {
	const prefix = "refs/heads/"
	if strings.HasPrefix(ref, prefix) {
		return ref[len(prefix):]
	}
	const tagPrefix = "refs/tags/"
	if strings.HasPrefix(ref, tagPrefix) {
		return ref[len(tagPrefix):]
	}
	return ref
}

// parseCoAuthors extracts names from Co-Authored-By trailers in a commit message.
// It returns the first word of each Co-Authored-By value (the tool/agent name).
func parseCoAuthors(msg string) []string {
	var names []string
	for _, line := range strings.Split(msg, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "co-authored-by:") {
			continue
		}
		// "Co-Authored-By: Name <email>" → extract Name
		val := strings.TrimSpace(line[len("co-authored-by:"):])
		// Take the first word as the agent/tool name.
		name := strings.Fields(val)
		if len(name) > 0 {
			names = append(names, name[0])
		}
	}
	return names
}
