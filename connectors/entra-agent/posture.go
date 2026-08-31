// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package entraagent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	findingAgentCAUnprotected              = "agent_ca_unprotected"
	findingAgentCANoRiskPolicy             = "agent_ca_no_risk_policy"
	findingAgentRisky                      = "agent_risky"
	findingAgentGovernanceNoAccessPackages = "agent_governance_no_access_packages"
	findingAgentNoSponsor                  = "agent_no_sponsor"
	riskyAgentStateAtRisk                  = "atRisk"
	riskyAgentStateConfirmedCompromised    = "confirmedCompromised"
	caAllAgentIDUsers                      = "AllAgentIdUsers"
	accessPackageScopeAllDirectoryAgentIDs = "allDirectoryAgentIdentities"
)

// gatherPosture emits GA-era Entra Agent ID posture findings after the existing
// static-secret drift pass. Tenant-level findings are gated on a live
// agentIdentity inventory so tenants with no agent identities do not receive
// noisy tenant posture findings; riskyAgents still runs because a deleted risky
// agent is signal.
func (s *Source) gatherPosture(ctx context.Context, sink sdk.Sink, client *httpx.Client, token string, now time.Time) error {
	needInventory := s.caPosture || s.govPosture
	var agents []agentIdentity
	if needInventory {
		var err error
		agents, err = s.liveAgentInventory(ctx, client)
		if err != nil {
			return err
		}
	}

	if len(agents) > 0 && s.caPosture {
		if err := s.gatherCAPosture(ctx, sink, client, now); err != nil && !toleratedPostureStatus(err) {
			return err
		}
	}

	if s.riskPosture {
		riskyClient, err := s.graphClientFromToken(token, map[string]string{"Prefer": "include-unknown-enum-members"})
		if err != nil {
			return err
		}
		if err := s.gatherRiskPosture(ctx, sink, riskyClient, now); err != nil && !toleratedPostureStatus(err) {
			return err
		}
	}

	if len(agents) == 0 || !s.govPosture {
		return nil
	}
	if err := s.gatherGovernanceAccessPackagePosture(ctx, sink, client, now); err != nil && !toleratedPostureStatus(err) {
		return err
	}
	if err := s.gatherSponsorlessPosture(ctx, sink, client, agents, now); err != nil {
		return err
	}
	return nil
}

func (s *Source) liveAgentInventory(ctx context.Context, client *httpx.Client) ([]agentIdentity, error) {
	query := url.Values{"$select": {"id,displayName"}}
	return collectPages[agentIdentity](ctx, client, "/v1.0/servicePrincipals/microsoft.graph.agentIdentity", query, s.maxPages)
}

// gatherCAPosture reads beta Conditional Access policies and infers only from
// live enabled policy conditions. It detects agent-user coverage via the
// documented users.includeUsers=="AllAgentIdUsers" sentinel; ordinary
// users.includeUsers=="All" policies deliberately do not count because Microsoft
// documents that all-users policies do not include agent user accounts. The
// agent-identity "all agents" sentinel and per-agent coverage math are
// undocumented, and agent CA templates have no Graph read surface; this detector
// therefore asks only whether any enabled policy targets agents and whether any
// enabled policy blocks high-risk agents. Grounding: learn.microsoft.com Entra
// Conditional Access for Agent IDs, updated 2026-07-01; Graph policy shape and
// list-policies Example 2 verified 2026-07-04.
func (s *Source) gatherCAPosture(ctx context.Context, sink sdk.Sink, client *httpx.Client, now time.Time) error {
	query := url.Values{"$select": {"id,displayName,state,conditions"}}
	policies, err := collectPages[conditionalAccessPolicy](ctx, client, "/beta/identity/conditionalAccess/policies", query, s.maxPages)
	if err != nil {
		return err
	}

	targeted := false
	highRiskPolicy := false
	for _, p := range policies {
		if p.State != "enabled" {
			continue
		}
		if caPolicyTargetsAgents(p) {
			targeted = true
		}
		for _, level := range p.Conditions.AgentIDRiskLevels {
			if level == "high" {
				highRiskPolicy = true
				break
			}
		}
	}

	if !targeted {
		return sink.Emit(ctx, model.FindingReport{
			Kind:        findingAgentCAUnprotected,
			Severity:    model.SeverityHigh,
			SubjectKind: "tenant",
			SubjectRef:  s.tenantID,
			Title:       "no enabled Conditional Access policy targets agent identities",
			OccurredAt:  now,
		})
	}
	if !highRiskPolicy {
		return sink.Emit(ctx, model.FindingReport{
			Kind:        findingAgentCANoRiskPolicy,
			Severity:    model.SeverityMedium,
			SubjectKind: "tenant",
			SubjectRef:  s.tenantID,
			Title:       "no enabled Conditional Access policy blocks high-risk agent identities",
			OccurredAt:  now,
		})
	}
	return nil
}

func caPolicyTargetsAgents(p conditionalAccessPolicy) bool {
	if p.State != "enabled" {
		return false
	}
	for _, user := range p.Conditions.Users.IncludeUsers {
		if user == caAllAgentIDUsers {
			return true
		}
	}
	apps := p.Conditions.ClientApplications
	if len(apps.IncludeAgentIDServicePrincipals) > 0 {
		return true
	}
	if apps.AgentIDServicePrincipalFilter != nil && strings.TrimSpace(apps.AgentIDServicePrincipalFilter.Rule) != "" {
		return true
	}
	return len(p.Conditions.AgentIDRiskLevels) > 0
}

// gatherRiskPosture reads beta ID Protection riskyAgents with
// Prefer: include-unknown-enum-members so agentIdentityBlueprintPrincipal rows
// are not masked as unknownFutureValue. Rows with actuation states atRisk or
// confirmedCompromised become findings, including deleted risky agents. Actuation
// endpoints (dismiss, confirmCompromised, confirmSafe) are declined by design:
// this plane is read-only toward identity providers; kill/contain for a
// risky Microsoft agent must happen in Entra, and this finding is the signal.
// ID Protection for agents may be gated by Agent 365 licensing ("starting soon"
// per Microsoft); a 403 is a tolerated skip. Grounding: learn.microsoft.com
// risky agents, updated 2026-06-17; Graph shape verified 2026-07-04.
func (s *Source) gatherRiskPosture(ctx context.Context, sink sdk.Sink, client *httpx.Client, now time.Time) error {
	rows, err := collectPages[riskyAgent](ctx, client, "/beta/identityProtection/riskyAgents", nil, s.maxPages)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.RiskState != riskyAgentStateAtRisk && r.RiskState != riskyAgentStateConfirmedCompromised {
			continue
		}
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        findingAgentRisky,
			Severity:    riskyAgentSeverity(r),
			SubjectKind: "identity",
			SubjectRef:  r.ID,
			Title:       fmt.Sprintf("agent at risk: %s (%s/%s)", r.AgentDisplayName, r.RiskState, r.RiskLevel),
			DetailHash:  redact.Hash("agent_risky|entra-agent|" + r.ID + "|" + r.RiskState + "|" + r.RiskLevel),
			OccurredAt:  now,
		}); err != nil {
			return err
		}
	}
	return nil
}

func riskyAgentSeverity(r riskyAgent) model.Severity {
	if r.RiskState == riskyAgentStateConfirmedCompromised {
		return model.SeverityHigh
	}
	switch r.RiskLevel {
	case "high":
		return model.SeverityHigh
	case "medium":
		return model.SeverityMedium
	default:
		return model.SeverityLow
	}
}

// gatherGovernanceAccessPackagePosture reads v1.0 entitlement-management
// assignment policies and sees only the documented
// allowedTargetScope=="allDirectoryAgentIdentities" signal. Agent-specific
// subjects inside specificAllowedTargets are not documented and therefore not
// detectable; lifecycle-workflow agent-sponsor task GUIDs are likewise
// unverified, so LCW posture is deliberately not read. Graph resource updated
// 2026-06-20 and verified 2026-07-04.
func (s *Source) gatherGovernanceAccessPackagePosture(ctx context.Context, sink sdk.Sink, client *httpx.Client, now time.Time) error {
	query := url.Values{"$select": {"id,displayName,allowedTargetScope"}}
	policies, err := collectPages[accessPackageAssignmentPolicy](ctx, client, "/v1.0/identityGovernance/entitlementManagement/assignmentPolicies", query, s.maxPages)
	if err != nil {
		return err
	}
	for _, p := range policies {
		if p.AllowedTargetScope == accessPackageScopeAllDirectoryAgentIDs {
			return nil
		}
	}
	return sink.Emit(ctx, model.FindingReport{
		Kind:        findingAgentGovernanceNoAccessPackages,
		Severity:    model.SeverityLow,
		SubjectKind: "tenant",
		SubjectRef:  s.tenantID,
		Title:       "no entitlement-management policy targets agent identities",
		OccurredAt:  now,
	})
}

// gatherSponsorlessPosture checks live agent identities for an accountable-human
// gap: neither a first user owner nor a first user sponsor. Microsoft's sponsor
// lifecycle exists to prevent orphaned agents; this complements the NHI
// lifecycle registry_orphaned signal, which marks blueprint-orphans at roster
// sync time. Grounding: learn.microsoft.com Entra agent ID governance overview
// and agent sponsor tasks (updated 2026-06-24).
func (s *Source) gatherSponsorlessPosture(ctx context.Context, sink sdk.Sink, client *httpx.Client, agents []agentIdentity, now time.Time) error {
	bound := s.maxPages * graphPageSize
	for i, a := range agents {
		if i >= bound {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		ownerRef, ownerDenied, err := s.firstUserRef(ctx, client, a.ID, "owners")
		if err != nil {
			return err
		}
		sponsorRef, sponsorDenied, err := s.firstUserRef(ctx, client, a.ID, "sponsors")
		if err != nil {
			return err
		}
		if ownerDenied || sponsorDenied || ownerRef != "" || sponsorRef != "" {
			continue
		}
		name := a.DisplayName
		if name == "" {
			name = a.ID
		}
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        findingAgentNoSponsor,
			Severity:    model.SeverityMedium,
			SubjectKind: "identity",
			SubjectRef:  a.ID,
			Title:       fmt.Sprintf("agent identity has no user owner or sponsor: %s", name),
			DetailHash:  redact.Hash("agent_no_sponsor|entra-agent|" + a.ID),
			OccurredAt:  now,
		}); err != nil {
			return err
		}
	}
	return nil
}

func toleratedPostureStatus(err error) bool {
	var se *httpx.StatusError
	return errors.As(err, &se) && (se.Status == http.StatusForbidden || se.Status == http.StatusNotFound)
}
