// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// Environment variables in Anthropic's documented credential precedence. A static
// key (tier 2) sits ABOVE the federation tiers (tier 4), so when it is present it
// silently shadows Workload Identity Federation — the documented footgun. Even an
// EMPTY ANTHROPIC_API_KEY="" wins its precedence slot, so presence (set), not a
// non-empty value, is the trigger.
const (
	envAPIKey            = "ANTHROPIC_API_KEY"
	envAuthToken         = "ANTHROPIC_AUTH_TOKEN"
	envIdentityTokenFile = "ANTHROPIC_IDENTITY_TOKEN_FILE" // a federation-in-use signal
)

// subjectFederation is the FindingReport subject for WIF governance findings.
const subjectFederation = "anthropic.federation"

// detectShadowing returns the WIF footgun finding when a static Anthropic key is
// present in the environment AND federation is in use (declared to this connector,
// or signaled by the identity-token-file env var). The static key would silently
// disable the attested federation path, so it is a high-severity governance finding.
// It returns ok=false when there is nothing to report.
//
// The detail hash records WHICH static variable shadows and WHICH federation signal
// is present, without embedding any value (a key, even masked, is never persisted).
func (s *Source) detectShadowing(at time.Time) (model.FindingReport, bool) {
	_, hasKey := s.envLookup(envAPIKey)
	_, hasAuth := s.envLookup(envAuthToken)
	if !hasKey && !hasAuth {
		return model.FindingReport{}, false // no static credential present
	}
	_, hasTokenFile := s.envLookup(envIdentityTokenFile)
	federationInUse := len(s.federation) > 0 || hasTokenFile
	if !federationInUse {
		return model.FindingReport{}, false // a static key with no federation is just a static key
	}

	shadowVar := envAPIKey
	if !hasKey {
		shadowVar = envAuthToken
	}
	signal := "declared_federation_rules"
	if len(s.federation) == 0 {
		signal = envIdentityTokenFile
	}
	return model.FindingReport{
		Kind:        "governance",
		Severity:    model.SeverityHigh,
		SubjectKind: subjectFederation,
		SubjectRef:  shadowVar,
		Title:       "Static Anthropic key shadows Workload Identity Federation",
		DetailHash:  redact.Hash(shadowVar + " present with " + signal + "; static credential takes precedence and silently disables federation"),
		OccurredAt:  at,
	}, true
}
