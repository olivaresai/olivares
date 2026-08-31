// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Gather runs the batch observation pass over the enabled sub-sources:
//
//  1. NHI long-lived-credential drift (API-key credential providers) — always
//     on when online.
//  2. AgentCore Registry inventory (enable_registry) — edges for approved
//     agent records, posture findings for non-healthy records.
//  3. Cedar policy posture (enable_policy_posture) — engine status, gateway
//     enforcement mode, Cedar coverage.
//  4. Export drift (enable_export_drift) — out-of-band edits and failed
//     async applies for Olivares-managed AgentCore policies.
//  5. Evaluations + guardrail posture (enable_eval_posture) — unhealthy custom
//     evaluators, online-evaluation failures, and gateways lacking guardrails.
//
// Each sub-source that fails emits a health finding and the pass continues.
// Offline (no credential/region) it returns nil immediately.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.offline() {
		return nil
	}
	c := s.newClient()
	now := s.clock().UTC()

	// 1. NHI long-lived-credential drift.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.gatherDrift(ctx, sink, c, now); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e := sink.Emit(ctx, healthFinding("agentcore.drift", s.accountScope(),
			"AgentCore credential drift scan failed", err, now)); e != nil {
			return e
		}
	}

	// 2. Registry inventory.
	if s.enableRegistry {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherRegistry(ctx, sink, c, now); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := sink.Emit(ctx, healthFinding("agentcore.registry", s.accountScope(),
				"AgentCore registry scan failed", err, now)); e != nil {
				return e
			}
		}
	}

	// 3. Cedar policy posture.
	if s.enablePolicyPosture {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherPolicyPosture(ctx, sink, c, now); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := sink.Emit(ctx, healthFinding("agentcore.policy_posture", s.accountScope(),
				"AgentCore policy posture scan failed", err, now)); e != nil {
				return e
			}
		}
	}

	// 4. Export drift and async apply failures. Out-of-band deletion is
	// statelessly undetectable here; the next export plan is the deletion detector
	// because it compares desired state to the remote managed-policy set.
	if s.enableExportDrift {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherExportDrift(ctx, sink, c, now); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := sink.Emit(ctx, healthFinding("agentcore.export_drift", s.accountScope(),
				"AgentCore export drift scan failed", err, now)); e != nil {
				return e
			}
		}
	}

	// 5. Evaluations and guardrail-coverage posture.
	if s.enableEvalPosture {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherEvalPosture(ctx, sink, c, now); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := sink.Emit(ctx, healthFinding("agentcore.eval_posture", s.accountScope(),
				"AgentCore evaluations posture scan failed", err, now)); e != nil {
				return e
			}
		}
	}
	return nil
}

// gatherDrift emits one nhi_longlived_credential drift finding per API-key
// credential provider: an API key in the token vault is the canonical static,
// long-lived secret the Five Eyes joint guidance "Careful adoption of agentic
// AI services" (2026-05-01) says to replace with ephemeral credentials.
func (s *Source) gatherDrift(ctx context.Context, sink sdk.Sink, c *client, at time.Time) error {
	providers, err := s.listAPIKeyProviders(ctx, c)
	if err != nil {
		return err
	}
	for _, p := range providers {
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        identitysource.FindingLongLivedCredential,
			Severity:    model.SeverityMedium,
			SubjectKind: "identity",
			SubjectRef:  p.CredentialProviderArn,
			Title:       "static API-key credential provider for agent outbound auth",
			DetailHash:  redact.Hash(identitysource.FindingLongLivedCredential + "|agentcore|" + p.CredentialProviderArn),
			OccurredAt:  at,
		}); err != nil {
			return err
		}
	}
	return nil
}

// healthFinding builds a health-class FindingReport for a failed sub-source,
// following the Bedrock connector pattern (a gap is a signal, not silence).
func healthFinding(subjectKind, subjectRef, title string, err error, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "health",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectKind,
		SubjectRef:  redact.Clean(subjectRef),
		Title:       title,
		DetailHash:  redact.Hash(err.Error()),
		OccurredAt:  at,
	}
}

func (s *Source) gatherExportDrift(ctx context.Context, sink sdk.Sink, c *client, at time.Time) error {
	engines, err := s.listPolicyEngines(ctx, c)
	if err != nil {
		return err
	}
	for _, eng := range engines {
		if err := ctx.Err(); err != nil {
			return err
		}
		policies, err := s.listEnginePolicies(ctx, c, eng.PolicyEngineID)
		if err != nil {
			return err
		}
		for _, pol := range policies {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !strings.HasPrefix(pol.Name, "olv_") {
				continue
			}
			markerTenant, expected, ok := parseExportMarker(pol.Description)
			if !ok {
				continue
			}
			actual := ""
			if pol.Definition.kind() == "cedar" {
				actual = sha256Hex(s.getCedarPolicyContent(pol))
			}
			if actual != expected {
				if err := sink.Emit(ctx, model.FindingReport{
					Kind:        "export_drift",
					Severity:    model.SeverityMedium,
					SubjectKind: subjectCedarPolicy,
					SubjectRef:  redact.Clean(pol.PolicyArn),
					Title:       fmt.Sprintf("Exported AgentCore policy %s drifted from its Olivares marker", redact.Clean(pol.Name)),
					DetailHash: redact.Hash(fmt.Sprintf("agentcore.export_drift engine=%s policy=%s tenant=%s expected=%s actual=%s",
						eng.PolicyEngineID, pol.PolicyID, markerTenant, hashPrefix(expected), hashPrefix(actual))),
					OccurredAt: at,
				}); err != nil {
					return err
				}
			}
			status := strings.ToUpper(strings.TrimSpace(pol.Status))
			if status == "CREATE_FAILED" || status == "UPDATE_FAILED" || status == "DELETE_FAILED" {
				if err := sink.Emit(ctx, model.FindingReport{
					Kind:        "export_apply_failed",
					Severity:    model.SeverityHigh,
					SubjectKind: subjectCedarPolicy,
					SubjectRef:  redact.Clean(pol.PolicyArn),
					Title:       fmt.Sprintf("AgentCore export apply for policy %s is %s", redact.Clean(pol.Name), status),
					DetailHash: redact.Hash(fmt.Sprintf("agentcore.export_apply_failed engine=%s policy=%s tenant=%s status=%s reasons=%s",
						eng.PolicyEngineID, pol.PolicyID, markerTenant, status, strings.Join(pol.StatusReasons, "|"))),
					OccurredAt: at,
				}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Source) gatherEvalPosture(ctx context.Context, sink sdk.Sink, c *client, at time.Time) error {
	if err := s.gatherEvaluatorPosture(ctx, sink, c, at); err != nil {
		return err
	}
	if err := s.gatherOnlineEvaluationPosture(ctx, sink, c, at); err != nil {
		return err
	}
	return s.gatherGuardrailCoverage(ctx, sink, c, at)
}

func (s *Source) gatherEvaluatorPosture(ctx context.Context, sink sdk.Sink, c *client, at time.Time) error {
	evaluators, err := s.listEvaluators(ctx, c)
	if err != nil {
		return err
	}
	for _, ev := range evaluators {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !evaluatorTypeIsCustom(ev.EvaluatorType) {
			continue
		}
		status := strings.ToUpper(strings.TrimSpace(ev.Status))
		if status == "" || status == "ACTIVE" {
			continue
		}
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "evaluator_unhealthy",
			Severity:    model.SeverityInfo,
			SubjectKind: "agentcore.evaluator",
			SubjectRef:  redact.Clean(firstNonEmpty(ev.EvaluatorArn, ev.EvaluatorID, ev.EvaluatorName)),
			Title:       fmt.Sprintf("Custom AgentCore evaluator %s is %s", redact.Clean(ev.EvaluatorName), status),
			DetailHash: redact.Hash(fmt.Sprintf("agentcore.evaluator id=%s type=%s status=%s",
				ev.EvaluatorID, ev.EvaluatorType, status)),
			OccurredAt: at,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) gatherOnlineEvaluationPosture(ctx context.Context, sink sdk.Sink, c *client, at time.Time) error {
	configs, err := s.listOnlineEvaluationConfigs(ctx, c)
	if err != nil {
		return err
	}
	for _, cfg := range configs {
		if err := ctx.Err(); err != nil {
			return err
		}
		executionStatus := strings.ToUpper(strings.TrimSpace(cfg.ExecutionStatus))
		failureReason := strings.TrimSpace(cfg.FailureReason)
		if failureReason == "" && !strings.Contains(executionStatus, "FAIL") {
			continue
		}
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "online_evaluation_failed",
			Severity:    model.SeverityMedium,
			SubjectKind: "agentcore.online_evaluation_config",
			SubjectRef:  redact.Clean(firstNonEmpty(cfg.OnlineEvaluationConfigArn, cfg.OnlineEvaluationConfigID, cfg.OnlineEvaluationConfigName)),
			Title:       fmt.Sprintf("AgentCore online evaluation config %s is failing", redact.Clean(cfg.OnlineEvaluationConfigName)),
			DetailHash: redact.Hash(fmt.Sprintf("agentcore.online_evaluation_config id=%s status=%s execution=%s failure=%s",
				cfg.OnlineEvaluationConfigID, cfg.Status, executionStatus, failureReason)),
			OccurredAt: at,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) gatherGuardrailCoverage(ctx context.Context, sink sdk.Sink, c *client, at time.Time) error {
	if !guardrailsSupportedRegion(s.region) {
		return nil
	}
	engines, err := s.listPolicyEngines(ctx, c)
	if err != nil {
		return err
	}
	engineByARN := make(map[string]policyEngineItem, len(engines))
	for _, eng := range engines {
		engineByARN[eng.PolicyEngineArn] = eng
	}
	gateways, err := s.listGateways(ctx, c)
	if err != nil {
		return err
	}
	guardrailCache := make(map[string]bool)
	for _, gw := range gateways {
		if err := ctx.Err(); err != nil {
			return err
		}
		det, err := s.getGateway(ctx, c, gw.GatewayID)
		if err != nil {
			continue
		}
		if det.PolicyConfig == nil || det.PolicyConfig.PolicyEngineArn == "" {
			continue
		}
		eng, ok := engineByARN[det.PolicyConfig.PolicyEngineArn]
		if !ok {
			continue
		}
		targets, err := s.listGatewayTargets(ctx, c, gw.GatewayID)
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			continue
		}
		hasGuardrail, ok := guardrailCache[eng.PolicyEngineID]
		if !ok {
			hasGuardrail, err = s.engineHasGuardrailPolicy(ctx, c, eng.PolicyEngineID)
			if err != nil {
				return err
			}
			guardrailCache[eng.PolicyEngineID] = hasGuardrail
		}
		if hasGuardrail {
			continue
		}
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "gateway_without_guardrails",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectPolicyGateway,
			SubjectRef:  redact.Clean(gw.GatewayArn),
			Title:       fmt.Sprintf("Gateway %s has targets but no guardrail-bearing policy", redact.Clean(gw.Name)),
			DetailHash: redact.Hash(fmt.Sprintf("agentcore.gateway_without_guardrails gateway=%s engine=%s targets=%d",
				gw.GatewayID, eng.PolicyEngineID, len(targets))),
			OccurredAt: at,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) engineHasGuardrailPolicy(ctx context.Context, c *client, engineID string) (bool, error) {
	policies, err := s.listEnginePolicies(ctx, c, engineID)
	if err != nil {
		return false, err
	}
	for _, pol := range policies {
		// Guardrail syntax (`when guardrails`, suppressOutput) is NOT standard
		// Cedar, so a guardrail-bearing definition may live in the June-GA
		// "policy" union arm rather than "cedar" — scan BOTH text arms or a
		// covered gateway would be falsely flagged. The text is scanned in
		// memory and discarded (D7), never emitted.
		statement := policyStatementText(pol)
		if strings.Contains(statement, "when guardrails") || strings.Contains(statement, "BedrockGuardrails::") {
			return true, nil
		}
	}
	return false, nil
}

// policyStatementText extracts the statement from whichever text-bearing union
// arm the definition uses ("cedar" or the June-GA "policy" arm); generated
// definitions carry no statement text and yield "".
func policyStatementText(p policyItem) string {
	var raw json.RawMessage
	switch p.Definition.kind() {
	case "cedar":
		raw = p.Definition.Cedar
	case "policy":
		raw = p.Definition.Policy
	default:
		return ""
	}
	var body cedarPolicyBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return ""
	}
	return body.Statement
}

func evaluatorTypeIsCustom(kind string) bool {
	kind = strings.ToUpper(strings.TrimSpace(kind))
	return strings.Contains(kind, "CUSTOM")
}

func hashPrefix(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

// Guardrail policy support region snapshot, taken from the upstream devguide
// table (2026-07). Upstream is expected to expand; re-verify this allowlist
// before broadening guardrail-coverage findings.
const (
	guardrailRegionUSEast1      = "us-east-1"
	guardrailRegionEUWest2      = "eu-west-2"
	guardrailRegionEUNorth1     = "eu-north-1"
	guardrailRegionAPSoutheast2 = "ap-southeast-2"
	guardrailRegionAPNortheast1 = "ap-northeast-1"
	guardrailRegionUSGovWest1   = "us-gov-west-1"
)

func guardrailsSupportedRegion(region string) bool {
	switch strings.TrimSpace(region) {
	case guardrailRegionUSEast1,
		guardrailRegionEUWest2,
		guardrailRegionEUNorth1,
		guardrailRegionAPSoutheast2,
		guardrailRegionAPNortheast1,
		guardrailRegionUSGovWest1:
		return true
	default:
		return false
	}
}
