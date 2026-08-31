// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vertex

import (
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// providerRef is the CostSample/Catalog ProviderRef every observation this connector
// emits carries. Vertex AI is a Google deployment surface, so it shares the "google"
// provider with the gemini (AI Studio) connector; the DEPLOYMENT surface is disambiguated
// by Gateway=vertex on every cost sample (model.GatewayVertex). See package doc.
const providerRef = "google" // == modelprovider.ProviderGoogle

// safetyPostureKind is the FindingReport.Kind every Model Armor safety-posture finding
// carries. It AGREES BY VALUE with connectors/bedrock, connectors/aws,
// connectors/azure-activity and modules/security across the license boundary (no shared
// import), so the GET /safety-posture roll-up treats every provider's posture uniformly.
const safetyPostureKind = "safety_posture"

// policyDriftKind is the FindingReport.Kind for declared-baseline drift. It AGREES BY
// VALUE with connectors/managedsettings, connectors/codex-managed-config and
// connectors/claude-apps-gateway across the license boundary (no shared import).
const policyDriftKind = "policy_drift"

// SubjectKind values. The posture kinds name the Model Armor config object the finding is
// about; the health kinds name the source that failed (a gap is a signal, not silence).
const (
	subjectArmorTemplate     = "vertex.model_armor_template"
	subjectArmorFloor        = "vertex.model_armor_floor"
	subjectArmorSanitization = "vertex.model_armor_sanitization"

	subjectUsage      = "vertex.usage"
	subjectCost       = "vertex.cost"
	subjectModelArmor = "vertex.model_armor"
)

// postureFinding builds a safety-posture FindingReport. The DetailHash is over a state-
// deterministic fingerprint (no timestamp) so an unchanged posture dedups in
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

// driftFinding builds one declared-baseline drift FindingReport for the Model Armor
// project floor. Like postureFinding, it hashes the deterministic detail fingerprint and
// never persists the raw detail.
func driftFinding(subjectRef, title, detail string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        policyDriftKind,
		Severity:    model.SeverityHigh,
		SubjectKind: subjectArmorFloor,
		SubjectRef:  redact.Clean(subjectRef),
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

// healthFinding reports an enabled source that could not be read. The error detail is
// hashed, never embedded, so a signed URL or message that carries a secret is not
// persisted (minimal-data). The connector emits this and continues with the next source.
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
