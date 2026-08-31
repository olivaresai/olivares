// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureactivity

import (
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// signalAzure marks an Azure Resource Graph INVENTORY edge: a discovery of the
// estate topology (tenant owns subscription, subscription owns resource) via the
// read-only Resource Graph / management APIs. It is distinct from the Activity
// Log feed (signalAzureActivity) so the consumer can separate inventory
// topology from observed control-plane access (the split connectors/aws draws
// between signal "aws" and "cloudtrail").
const signalAzure = model.SignalSource("azure")

// signalAzureActivity marks an Azure Monitor Activity Log ACTIVITY edge: a caller
// performed a control-plane operation. It is a NEW open SignalSource, distinct
// from the service-level azure_diagnostic (azurekeyvault) and azureblob_audit
// signals — the Activity Log is the subscription management plane, a different
// source from a single resource's diagnostic logs.
const signalAzureActivity = model.SignalSource("azure_activity")

// Origin and resource kinds emitted by this connector. Inventory edges are
// containment/topology (tenant ⊳ subscription ⊳ resource); activity edges are
// control-plane (a caller performed a management operation). The "azure.api"
// namespace keeps Activity Log management operations disjoint from the
// per-resource data plane own (azureblob.object, azure.keyvault.secret).
const (
	originTenant       = "azure.tenant"
	originSubscription = "azure.subscription"

	resSubscription = "azure.subscription"
	resResource     = "azure.resource"
	resAzureAPI     = "azure.api"
)

// SubjectKind values used in health findings.
const (
	subjectSubscriptions = "azure.subscriptions"
	subjectInventory     = "azure.inventory"
	subjectActivity      = "azure.activity"
	subjectRAI           = "azure.rai" // RAI posture read health
)

// safetyPostureKind is the FindingReport.Kind every provider AI-safety-posture
// finding carries. modules/security persists it (any severity, deduped) and
// the GET /safety-posture view aggregates on it. The value is shared by VALUE with
// the other provider connectors and the security module across the license boundary
// (no shared import); see modules/security findingKindSafetyPosture.
const safetyPostureKind = "safety_posture"

// SubjectKind values for the Azure RAI safety-posture findings: a Responsible-AI
// content-filter policy, a model deployment's binding to one, and the honest note
// that per-request content-filter decisions are not management-API readable.
const (
	subjectRAIPolicy     = "azure.rai_policy"
	subjectDeployment    = "azure.deployment"
	subjectContentFilter = "azure.content_filter"
)

// inventoryEdge builds one Resource Graph topology edge. A containment edge is
// NOT an access: Mode is unknown and Confidence attributed (we observed it
// directly via the API). Refs are scrubbed defensively and the resource id is
// lower-cased for stable convergence (ARM ids are case-insensitive).
func inventoryEdge(originKind, originRef, resKind, resRef string, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   originKind,
		OriginRef:    redact.Clean(originRef),
		ResourceKind: resKind,
		ResourceRef:  redact.Clean(resRef),
		Mode:         model.ModeUnknown,
		Source:       signalAzure,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
	}
}

// activityEdge builds one Activity Log control-plane edge: a caller performed an
// operation. The resource is the operationName (e.g.
// "Microsoft.Compute/virtualMachines/write") mirroring the AWS
// eventSource:eventName shape, and the tool is the resource provider. OriginKind
// is always "identity" (the log attributes a credential — objectId/appId/UPN —
// never a resolved agent; identity↔agent is module VI).
func activityEdge(caller, resRef, toolRef string, mode model.AccessMode, conf model.Confidence, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   identity.OriginKind,
		OriginRef:    redact.Clean(caller),
		ResourceKind: resAzureAPI,
		ResourceRef:  redact.Clean(resRef),
		Mode:         mode,
		Source:       signalAzureActivity,
		Confidence:   conf,
		ToolRef:      redact.Clean(toolRef),
		ObservedAt:   at,
	}
}

// healthFinding reports an enabled service that could not be reached. The error
// detail is hashed, never embedded, so a token or URL in an error string is not
// persisted (minimal-data).
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

// coverageFinding reports that a paginated read stopped at the max_pages bound
// while the API still had more data — an HONEST signal that the result is
// partial (raise max_pages for full coverage), never a silent truncation
// (docs/SECURITY-HARDENING.md; the "no silent caps" invariant). The title carries the
// non-sensitive, displayable detail; there is nothing sensitive to hash.
func coverageFinding(subjectKind, subjectRef, title string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "health",
		Severity:    model.SeverityLow,
		SubjectKind: subjectKind,
		SubjectRef:  subjectRef,
		Title:       title,
		OccurredAt:  at,
	}
}

// classifyOperation maps an Activity Log operationName to an access mode by its
// final RBAC action segment, verbatim from Azure's own vocabulary (ARCHITECTURE.md).
// "Microsoft.Compute/virtualMachines/write" → write; ".../read" → read;
// ".../delete" → write. The generic "action" suffix (a POST operation such as
// listKeys/action or restart/action) is genuinely ambiguous — it can read or
// write — so it is ModeUnknown, never guessed. Anything else is ModeUnknown too.
func classifyOperation(operationName string) model.AccessMode {
	seg := operationName
	if i := strings.LastIndexByte(operationName, '/'); i >= 0 {
		seg = operationName[i+1:]
	}
	switch strings.ToLower(strings.TrimSpace(seg)) {
	case "read":
		return model.ModeRead
	case "write":
		return model.ModeWrite
	case "delete":
		return model.ModeWrite
	default:
		return model.ModeUnknown
	}
}

// providerOf returns the Azure resource provider for an operationName, used as
// the activity edge's ToolRef. It prefers the explicit resourceProviderName from
// the event and falls back to the first segment of the operationName
// ("Microsoft.Compute/virtualMachines/write" → "Microsoft.Compute").
func providerOf(provider, operationName string) string {
	if p := strings.TrimSpace(provider); p != "" {
		return p
	}
	if i := strings.IndexByte(operationName, '/'); i >= 0 {
		return operationName[:i]
	}
	return operationName
}

// classifyShared is the connector's shared-set adapter. It is identical to the
// other connectors' use of identity.SharedSet: a caller declared shared/pooled
// drops to approximate confidence (the raw caller is still emitted).
func confidenceFor(shared identity.SharedSet, caller string) model.Confidence {
	return shared.ConfidenceFor(caller)
}

// statusSucceeded reports whether an Activity Log event's terminal status denotes
// a completed operation. The Activity Log emits a Started event and a terminal
// Succeeded/Failed event per operation; keeping only "Succeeded" yields exactly
// one edge per effective access and drops blocked/failed attempts (a Failed
// operation changed nothing — like a GCP policy-denied entry, it is not an
// observed access). Long-running operations that never reach Succeeded inside the
// window are not emitted (an honest under-count, documented in doc.go).
func statusSucceeded(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "Succeeded")
}
