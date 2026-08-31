// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// Skill types attached to an agent (CMA agent skills[]). A custom skill is workspace-
// authored code (filesystem resources the agent runs on demand); an anthropic skill is a
// pre-built Anthropic skill (xlsx/pptx/...). Only custom skills carry a version.
const (
	skillTypeCustom    = "custom"
	skillTypeAnthropic = "anthropic"
	skillVersionLatest = "latest"
)

// SkillRef is one skill entry on an agent. version is pinned (custom skills) or "latest"
// (unpinned — a supply-chain drift signal: the agent silently picks up new skill versions).
type SkillRef struct {
	Type    string `json:"type"`
	SkillID string `json:"skill_id"`
	Version string `json:"version"`
}

// Agent is the governance-relevant subset of a CMA agent DEFINITION: its id/name, its
// attached skills, its declared tools[] (the enumerable permission grant — see
// permissions.go agentToolEdges) and its multiagent roster. It does NOT re-model the
// Agent SDK permission surface (that is connectors/claude's agentsdk.go). Tools and
// Multiagent reuse the session-snapshot types (sessions.go) — the live agent resource
// carries the same shapes (verified 2026-06-10).
type Agent struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Skills     []SkillRef        `json:"skills"`
	Tools      []AgentTool       `json:"tools"`
	Multiagent *MultiagentRoster `json:"multiagent"`
}

type agentPage struct {
	Data    []Agent `json:"data"`
	HasMore bool    `json:"has_more"`
	LastID  string  `json:"last_id"`
}

// fetchAgents lists the workspace's agents (for the skill governance dimension).
func (c *client) fetchAgents(ctx context.Context) ([]Agent, error) {
	var out []Agent
	after := ""
	for i := 0; i < c.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		var page agentPage
		if err := c.getJSON(ctx, "/v1/agents", listQuery("after_id", after), &page); err != nil {
			return out, err
		}
		out = append(out, page.Data...)
		if !page.HasMore || page.LastID == "" {
			break
		}
		after = page.LastID
	}
	return out, nil
}

// skillEdge maps an agent's attached skill to an agent → skill PERMITTED edge. A
// skills[] attachment is a DECLARED grant (which skill code the agent may pull into
// its sandbox), not an observation of use — so it travels as model.SignalPolicy and
// lands on the permitted side of the access-map diff (correction: Emitted it
// as SignalCMA, which mislabeled the grant as observed activity and made skill
// over-provisioning invisible to the unused-grant drift).
func skillEdge(agentID string, sk SkillRef, at time.Time) (model.EdgeObservation, bool) {
	if strings.TrimSpace(sk.SkillID) == "" {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   originAgent,
		OriginRef:    redact.Clean(agentID),
		ResourceKind: kindSkill,
		ResourceRef:  redact.Clean(sk.SkillID),
		Mode:         model.ModeRead,
		Source:       model.SignalPolicy,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
	}, true
}

// skillFinding flags a custom skill pinned to "latest" (or no version) as an unpinned
// supply-chain risk (ASI09): the agent silently adopts new skill code on each run, with no
// review gate. A pinned custom skill or a pre-built anthropic skill returns ok=false. The
// supply-chain weight is on custom skills because they are operator-authored executable
// resources downloaded into the sandbox.
func skillFinding(agentID string, sk SkillRef, at time.Time) (model.FindingReport, bool) {
	if sk.Type != skillTypeCustom {
		return model.FindingReport{}, false
	}
	v := strings.TrimSpace(sk.Version)
	if v != "" && v != skillVersionLatest {
		return model.FindingReport{}, false // pinned — governed
	}
	return model.FindingReport{
		Kind:        findingPosture,
		Severity:    model.SeverityLow,
		SubjectKind: kindSkill,
		SubjectRef:  redact.Clean(sk.SkillID),
		Title:       "CMA custom skill is unpinned (version 'latest') — supply-chain drift",
		DetailHash:  redact.Hash("skill=" + sk.SkillID + " agent=" + agentID + " version=" + firstNonEmpty(v, "(unset)") + " type=custom; an unpinned custom skill lets the agent adopt new executable skill code with no review gate (pin to a version) (CMA skills)"),
		OWASPASI:    []string{asiSupplyChain},
		OccurredAt:  at,
	}, true
}
