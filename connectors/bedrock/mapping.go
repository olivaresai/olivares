// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bedrock

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// ProviderBedrock is the CostSample ProviderRef every sample this connector emits
// carries — the cost STREAM's provenance discriminator (the AWS Bedrock platform),
// consistent with how the other gateway/platform connectors tag their stream (fal,
// cursor). The model VENDOR (anthropic/amazon/meta/…) lives in ModelRef; the
// deployment SURFACE (bedrock-mantle/bedrock-legacy) lives in Gateway.
const ProviderBedrock = "bedrock"

// safetyPostureKind is the FindingReport.Kind every provider AI-safety-posture finding
// carries. modules/security persists it (any severity, deduped) and the GET
// /safety-posture view aggregates on it. The Kind, and the SubjectKind/SubjectRef values
// below, AGREE BY VALUE with connectors/aws and modules/security across the license
// boundary (no shared import) — so the consumer treats both connectors' findings
// uniformly. Note: this connector additionally encodes Automated Reasoning in the
// guardrail DETAIL, so its per-guardrail DetailHash diverges from connectors/aws — the
// two will NOT dedup against each other (the dedup key includes DetailHash). That is why
// Guardrails must be enabled on exactly ONE Bedrock connector (see guardrails.go).
const safetyPostureKind = "safety_posture"

// SubjectKind values for the Bedrock safety-posture findings: a guardrail's config
// posture, and the account-level decision-logging (auditability) posture. Identical to
// connectors/aws so the two connectors' findings are interchangeable to the consumer.
const (
	subjectBedrockGuardrail = "bedrock.guardrail"
	subjectBedrockLogging   = "bedrock.logging"
)

// SubjectKind values used in health findings, one per source, so a failed source is a
// visible signal rather than silence (a gap is a signal, not silence).
const (
	subjectUsage      = "bedrock.usage"
	subjectCost       = "bedrock.cost"
	subjectGuardrails = "bedrock.guardrails"
)

// postureFinding builds a safety-posture FindingReport. The DetailHash is over a
// state-deterministic fingerprint (no timestamp) so an unchanged posture dedups in
// modules/security while a real config change surfaces a fresh finding.
func postureFinding(sev model.Severity, subjectKind, subjectRef, title, detail string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        safetyPostureKind,
		Severity:    sev,
		SubjectKind: subjectKind,
		SubjectRef:  redact.Clean(subjectRef),
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

// healthFinding reports an enabled source that could not be read. The error detail is
// hashed, never embedded, so a signed URL or message that carries a secret is not
// persisted (minimal-data). The connector emits this and continues with the next
// source.
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

// emit forwards an observation, returning Emit's error so callers can treat it as
// fatal to the pass (per the SDK contract).
func emit(ctx context.Context, sink sdk.Sink, obs model.Observation) error {
	return sink.Emit(ctx, obs)
}
