// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudflare

import (
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// signalCloudflare is this connector's provenance tag on every observation it
// emits, so the consumer can build the inventory by signal_source and keep
// Cloudflare-derived topology distinct from other collectors (ARCHITECTURE.md).
const signalCloudflare = model.SignalSource("cloudflare")

// Origin and resource kinds emitted by this connector. The origin of an
// account-scoped edge is the Cloudflare account; the origin of a route edge is
// the zone. The resource is the discovered serverless/object/audit entity. The
// consumer module materializes entities from these refs.
const (
	originAccount = "cf.account"
	originZone    = "cf.zone"

	resWorker      = "cf.worker"
	resWorkerRoute = "cf.worker_route"
	resR2Bucket    = "r2.bucket"
	resLogpushJob  = "cf.logpush_job"
)

// inventoryEdge builds one containment/topology edge with the shared Cloudflare
// provenance. A containment edge is NOT an access: Mode is always ModeUnknown and
// Confidence is ConfidenceAttributed (we observed the membership directly via the
// API). This mirrors the mcp_annotation precedent — an edge that is topology,
// not an R/RW access (the consumer derives observed/permitted, not the connector).
func inventoryEdge(originKind, originRef, resKind, resRef, toolRef string, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   originKind,
		OriginRef:    originRef,
		ResourceKind: resKind,
		ResourceRef:  resRef,
		Mode:         model.ModeUnknown,
		Source:       signalCloudflare,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      toolRef,
		ObservedAt:   at,
	}
}

// healthFinding reports a target that is configured but could not be listed. The
// error detail is hashed, never embedded, so a token or URL inside an error
// message is never persisted (docs/SECURITY-HARDENING.md). A gap is a signal, not silence: the
// pass emits this and continues with the other targets.
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
