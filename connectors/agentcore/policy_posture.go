// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agentcore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Subject-kind constants for policy posture findings.
const (
	subjectPolicyEngine  = "agentcore.policy_engine"
	subjectPolicyGateway = "agentcore.gateway"
	subjectCedarPolicy   = "agentcore.cedar_policy"
)

// gatherPolicyPosture reads the AgentCore Policy surface — policy engines,
// their Cedar policies, and the gateways they attach to — and emits posture
// findings that give operators visibility into what AgentCore enforces.
//
// The existing Snapshot (identitysource.Graph) lists policy engines and
// policies as roster COLLECTIONS with metadata only (name, status, definition
// kind discriminator — never the Cedar source text). This sub-source adds the
// POSTURE dimension: what is ENFORCED (engine→gateway attachment +
// enforcement mode), what is ACTIVE vs CREATING/FAILED, and whether Cedar
// policies cover the gateway surface.
//
// Cedar source text is read for FINGERPRINTING (hash-based dedup in
// modules/security) but NEVER emitted or stored verbatim — it may contain
// operator-authored policy logic that is sensitive in a multi-tenant context.
// The DetailHash is over a deterministic fingerprint of the Cedar content so
// an unchanged policy dedups while a real edit surfaces a fresh finding.
func (s *Source) gatherPolicyPosture(ctx context.Context, sink sdk.Sink, c *client, at time.Time) error {
	engines, err := s.listPolicyEngines(ctx, c)
	if err != nil {
		return err
	}
	scope := s.accountScope()

	if len(engines) == 0 {
		return sink.Emit(ctx, model.FindingReport{
			Kind:        "policy_posture",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectPolicyEngine,
			SubjectRef:  redact.Clean(scope),
			Title:       "No AgentCore policy engines in region " + s.region + " — agent traffic is ungoverned",
			DetailHash:  redact.Hash("agentcore.policy_engine account=" + scope + " engines=0"),
			OccurredAt:  at,
		})
	}

	// Read gateways to build the engine→gateway enforcement map.
	gateways, err := s.listGateways(ctx, c)
	if err != nil {
		return err
	}
	engineGateways := make(map[string][]gatewayEnforcement)
	for _, gw := range gateways {
		if err := ctx.Err(); err != nil {
			return err
		}
		det, err := s.getGateway(ctx, c, gw.GatewayID)
		if err != nil {
			continue
		}
		if det.PolicyConfig != nil && det.PolicyConfig.PolicyEngineArn != "" {
			engineGateways[det.PolicyConfig.PolicyEngineArn] = append(
				engineGateways[det.PolicyConfig.PolicyEngineArn],
				gatewayEnforcement{
					gatewayArn:      gw.GatewayArn,
					gatewayName:     gw.Name,
					enforcementMode: det.PolicyConfig.EnforcementMode,
				},
			)
		}
	}

	// Emit gateway posture: gateways without policy engines are ungoverned.
	for _, gw := range gateways {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !gatewayHasPolicy(gw, engineGateways) {
			if err := sink.Emit(ctx, model.FindingReport{
				Kind:        "policy_posture",
				Severity:    model.SeverityMedium,
				SubjectKind: subjectPolicyGateway,
				SubjectRef:  redact.Clean(gw.GatewayArn),
				Title:       fmt.Sprintf("Gateway %s has no policy engine attached — agent traffic ungoverned", redact.Clean(gw.Name)),
				DetailHash:  redact.Hash(fmt.Sprintf("agentcore.gateway id=%s no_policy=true", gw.GatewayID)),
				OccurredAt:  at,
			}); err != nil {
				return err
			}
		}
	}

	// Per-engine posture: status, attached gateways, Cedar policy coverage.
	for _, eng := range engines {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherEnginePolicyPosture(ctx, sink, c, eng, engineGateways[eng.PolicyEngineArn], at); err != nil {
			return err
		}
	}
	return nil
}

// gatewayEnforcement ties a gateway to its enforcement mode for a policy engine.
type gatewayEnforcement struct {
	gatewayArn      string
	gatewayName     string
	enforcementMode string
}

// gatewayHasPolicy checks whether a gateway has any policy engine attached.
func gatewayHasPolicy(gw gatewayItem, engineGateways map[string][]gatewayEnforcement) bool {
	for _, gws := range engineGateways {
		for _, eg := range gws {
			if eg.gatewayArn == gw.GatewayArn {
				return true
			}
		}
	}
	return false
}

// gatherEnginePolicyPosture emits posture findings for one policy engine:
//   - engine status (ACTIVE vs error states)
//   - enforcement coverage (which gateways, what mode)
//   - per-policy: Cedar content fingerprint, policy status
func (s *Source) gatherEnginePolicyPosture(ctx context.Context, sink sdk.Sink, c *client, eng policyEngineItem, attachedGateways []gatewayEnforcement, at time.Time) error {
	status := strings.ToUpper(eng.Status)

	// Engine status finding.
	engineSev := model.SeverityInfo
	engineTitle := fmt.Sprintf("Policy engine %s is %s", redact.Clean(eng.Name), status)
	if status != "ACTIVE" {
		engineSev = model.SeverityHigh
		engineTitle = fmt.Sprintf("Policy engine %s is %s — policies are not evaluating", redact.Clean(eng.Name), status)
	}

	gwNames := make([]string, 0, len(attachedGateways))
	enforceModes := make([]string, 0, len(attachedGateways))
	for _, gw := range attachedGateways {
		gwNames = append(gwNames, redact.Clean(gw.gatewayName))
		enforceModes = append(enforceModes, gw.enforcementMode)
	}

	detail := fmt.Sprintf("agentcore.policy_engine id=%s status=%s gateways=%d gateway_names=%s enforcement_modes=%s",
		eng.PolicyEngineID, status, len(attachedGateways),
		strings.Join(gwNames, ","), strings.Join(enforceModes, ","))

	if err := sink.Emit(ctx, model.FindingReport{
		Kind:        "policy_posture",
		Severity:    engineSev,
		SubjectKind: subjectPolicyEngine,
		SubjectRef:  redact.Clean(eng.PolicyEngineArn),
		Title:       engineTitle,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}); err != nil {
		return err
	}

	// Unattached engine warning.
	if len(attachedGateways) == 0 {
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "policy_posture",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectPolicyEngine,
			SubjectRef:  redact.Clean(eng.PolicyEngineArn),
			Title:       fmt.Sprintf("Policy engine %s is not attached to any gateway — policies have no enforcement point", redact.Clean(eng.Name)),
			DetailHash:  redact.Hash(fmt.Sprintf("agentcore.policy_engine id=%s unattached=true", eng.PolicyEngineID)),
			OccurredAt:  at,
		}); err != nil {
			return err
		}
	}

	// Per-policy Cedar findings.
	policies, err := s.listEnginePolicies(ctx, c, eng.PolicyEngineID)
	if err != nil {
		return err
	}

	cedarCount := 0
	for _, pol := range policies {
		if err := ctx.Err(); err != nil {
			return err
		}
		polStatus := strings.ToUpper(pol.Status)
		defKind := pol.Definition.kind()

		polSev := model.SeverityInfo
		polTitle := fmt.Sprintf("Cedar policy %s (%s) is %s", redact.Clean(pol.Name), defKind, polStatus)

		if polStatus != "ACTIVE" && polStatus != "" {
			polSev = model.SeverityMedium
			polTitle = fmt.Sprintf("Cedar policy %s is %s — not evaluating", redact.Clean(pol.Name), polStatus)
		}

		cedarContent := s.getCedarPolicyContent(pol)
		contentFingerprint := ""
		if cedarContent != "" {
			cedarCount++
			contentFingerprint = redact.Hash(cedarContent)
		}

		polDetail := fmt.Sprintf("agentcore.cedar_policy id=%s engine=%s status=%s definition=%s cedar_hash=%s",
			pol.PolicyID, eng.PolicyEngineID, polStatus, defKind, contentFingerprint)

		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "policy_posture",
			Severity:    polSev,
			SubjectKind: subjectCedarPolicy,
			SubjectRef:  redact.Clean(pol.PolicyArn),
			Title:       polTitle,
			DetailHash:  redact.Hash(polDetail),
			OccurredAt:  at,
		}); err != nil {
			return err
		}
	}

	// Cedar coverage summary for the engine.
	if cedarCount == 0 && len(policies) > 0 {
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "policy_posture",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectPolicyEngine,
			SubjectRef:  redact.Clean(eng.PolicyEngineArn),
			Title:       fmt.Sprintf("Policy engine %s has %d policies but none are Cedar — no static policy evaluation", redact.Clean(eng.Name), len(policies)),
			DetailHash:  redact.Hash(fmt.Sprintf("agentcore.policy_engine id=%s policies=%d cedar=0", eng.PolicyEngineID, len(policies))),
			OccurredAt:  at,
		}); err != nil {
			return err
		}
	}

	return nil
}
