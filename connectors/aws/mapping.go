// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// signalAWS marks an IAM-inventory edge: it is a discovery of a non-human
// identity (role/user/policy) via the IAM API, distinct from the CloudTrail
// activity feed (model.SignalCloudTrail). The consumer reads edges by signal
// source to separate inventory topology from observed control-plane access.
const signalAWS = model.SignalSource("aws")

// Origin and resource kinds emitted by this connector. IAM inventory edges are
// containment/topology (account owns identity, role attaches policy); CloudTrail
// edges are control-plane activity (identity called an account-level API). The
// "aws.api" namespace keeps CloudTrail management activity disjoint from the
// per-S3-object data-plane resources.
const (
	originAccount  = "aws.account"
	originIdentity = "identity"

	resIAMRole   = "iam.role"
	resIAMUser   = "iam.user"
	resIAMPolicy = "iam.policy"
	resAWSAPI    = "aws.api"

	// originIAMRole is the origin kind for a role→attached-policy edge.
	originIAMRole = "iam.role"
)

// SubjectKind values used in health findings, one per enabled service.
const (
	subjectIAM        = "aws.iam"
	subjectCloudTrail = "aws.cloudtrail"
	subjectBedrock    = "aws.bedrock"
)

// safetyPostureKind is the FindingReport.Kind every provider AI-safety-posture
// finding carries. modules/security persists it (any severity, deduped) and
// the GET /safety-posture view aggregates on it. The value is shared by VALUE with
// the other provider connectors and the security module across the license boundary
// (no shared import); see modules/security findingKindSafetyPosture.
const safetyPostureKind = "safety_posture"

// SubjectKind values for the Bedrock safety-posture findings: a guardrail's config
// posture, and the account-level decision-logging (auditability) posture.
const (
	subjectBedrockGuardrail = "bedrock.guardrail"
	subjectBedrockLogging   = "bedrock.logging"
)

// inventoryEdge builds one IAM-inventory topology edge. A containment edge is NOT
// an access: Mode is unknown and Confidence attributed (we observed it directly
// via the IAM API). The ref is scrubbed defensively; an ARN never carries a
// secret, but a scrub keeps the minimal-data invariant uniform across connectors.
func inventoryEdge(originKind, originRef, resKind, resRef string, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   originKind,
		OriginRef:    redact.Clean(originRef),
		ResourceKind: resKind,
		ResourceRef:  redact.Clean(resRef),
		Mode:         model.ModeUnknown,
		Source:       signalAWS,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
	}
}

// activityEdge builds one CloudTrail control-plane activity edge: an identity
// called an account-level API. The principal ref is scrubbed (an assumed-role ARN
// can embed a session name); the resource is the eventSource:eventName pair and
// the tool is the eventSource. Mode and Confidence are decided by the caller from
// the event's readOnly flag and the attributability of the principal.
func activityEdge(principalRef, resRef, toolRef string, mode model.AccessMode, conf model.Confidence, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   originIdentity,
		OriginRef:    redact.Clean(principalRef),
		ResourceKind: resAWSAPI,
		ResourceRef:  redact.Clean(resRef),
		Mode:         mode,
		Source:       model.SignalCloudTrail,
		Confidence:   conf,
		ToolRef:      redact.Clean(toolRef),
		ObservedAt:   at,
	}
}

// healthFinding reports an enabled service that could not be reached/listed. The
// error detail is hashed, never embedded, so a signed URL or error message that
// carries a secret is not persisted (minimal-data). A gap is a signal, not
// silence: the connector emits this and continues with the other service.
func healthFinding(subjectKind, subjectRef, title string, err error, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "health",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectKind,
		SubjectRef:  subjectRef,
		Title:       title,
		DetailHash:  redact.Hash(err.Error()),
		OccurredAt:  at,
	}
}
