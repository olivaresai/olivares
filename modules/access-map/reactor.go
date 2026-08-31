// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"context"
	"errors"
	"path"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// errNoData is returned when a method is used before the engine (or a test)
// wired the data handle via UseData — a programming error surfaced explicitly
// rather than as a nil-dereference panic.
var errNoData = errors.New("access-map: module has no data handle; call UseData first")

// Ingest materializes one connector access observation into the canonical R/RW
// graph: it resolves the origin (the identity bridge, bridge.go), resolves the
// resource, fuses against any existing edge on the same natural key (fusion.go)
// and upserts the AccessEdge — the OBSERVED side of the permitted-vs-observed
// diff, and the PERMITTED side when the source is a policy grant (ARCHITECTURE.md).
//
// The upsert is idempotent and monotonic by natural key, so an at-least-once
// redelivery accumulates the occurrence count rather than duplicating the edge.
//
// Minimal data (docs/SECURITY-HARDENING.md): it persists ONLY the edge and the connector's
// already-redacted natural references — never a SQL statement, payload, secret
// or PII. The EdgeObservation wire type carries no such field, and the metadata
// is the bounded, allow-listed set in edgeMetadata.
//
// It returns the persisted edge (zero value when the observation was honestly
// skipped: a non-tenant reference, an unresolved origin, or no nameable
// resource).
func (m *Module) Ingest(ctx context.Context, tenantRef string, edge sdkmodel.EdgeObservation) (model.AccessEdge, error) {
	if m.data == nil {
		return model.AccessEdge{}, errNoData
	}
	tenant, ok := tenantOf(tenantRef)
	if !ok {
		return model.AccessEdge{}, nil
	}
	at := edge.ObservedAt
	if at.IsZero() {
		at = m.clock.Now().Time()
	}

	var out model.AccessEdge
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		attr, err := resolveOrigin(ctx, sc, edge)
		if err != nil {
			return err
		}
		if attr.OriginID.IsZero() {
			return nil // origin could not be attributed — honest skip, never guessed
		}
		// Classify how firmly the resolved origin ties to a concrete agent/NHI
		// (firm/approximate/unknown), consuming the per-agent identity signals
		// Expose. Deny-closed: no firm signal stays approximate; an opaque
		// store floors to unknown (G8). The lookup shares this Mutate
		// transaction, so it sees exactly what the bridge resolved.
		attr.Tier, attr.TierReason, err = attributionTier(ctx, sc, attr, coverageTier(edge.ResourceKind))
		if err != nil {
			return err
		}
		resID, err := resolveResource(ctx, sc, edge)
		if err != nil {
			return err
		}
		if resID.IsZero() {
			return nil // nothing nameable to point the edge at
		}
		observed, permitted := deriveObservedPermitted(edge.Source)
		var sessionID model.ID
		if attr.OriginKind == originSession {
			sessionID = attr.OriginID
		}
		newEdge := model.AccessEdge{
			OriginKind:      attr.OriginKind,
			OriginID:        attr.OriginID,
			ResourceID:      resID,
			Mode:            edge.Mode,
			SignalSource:    edge.Source,
			Confidence:      attr.Confidence,
			Observed:        observed,
			Permitted:       permitted,
			SessionID:       sessionID,
			FirstSeen:       model.NewTimestamp(at),
			LastSeen:        model.NewTimestamp(at),
			OccurrenceCount: 1,
			Metadata:        edgeMetadata(edge, attr),
		}
		// Multi-signal fusion: reconcile against any existing edge on the same
		// natural key so a later, weaker signal never downgrades a stronger one,
		// and an untrusted annotation never alone raises confidence (fusion.go).
		// The read and the Upsert share this one transaction.
		if prev, ok, err := existingEdge(ctx, sc, newEdge); err != nil {
			return err
		} else if ok {
			fuse(&newEdge, prev)
		}
		out, err = sc.AccessEdges().Upsert(ctx, newEdge)
		return err
	})
	return out, err
}

// resolveResource find-or-creates the Resource the edge names — and only the
// resource (the table, bucket or API). The reference is the connector's
// already-redacted natural ref.
func resolveResource(ctx context.Context, sc store.Scope, edge sdkmodel.EdgeObservation) (model.ID, error) {
	if edge.ResourceRef == "" || edge.ResourceKind == "" {
		return "", nil
	}
	return foResource(ctx, sc, edge.ResourceKind, edge.ResourceRef, resourceName(edge.ResourceKind, edge.ResourceRef))
}

// deriveObservedPermitted maps a signal source to the observed/permitted flags
// of the AccessEdge (ARCHITECTURE.md; the §4 derivation also used). A policy
// grant is the PERMITTED side of the diff; an MCP annotation is an UNTRUSTED
// declared capability (neither observed nor permitted, so it can never alone
// make an edge observed or raise it to attributed); every real observation
// source (otel, pgAudit, CloudTrail, eBPF) is observed.
func deriveObservedPermitted(source sdkmodel.SignalSource) (observed, permitted bool) {
	switch source {
	case sdkmodel.SignalPolicy, sdkmodel.SignalScopedGrant:
		// SignalScopedGrant: a FASE X source→scope binding projected as a
		// PERMITTED edge — like an IdP-derived policy grant it is declared (not
		// observed) and populates the permitted side of the drift. Adding it here (a
		// new permitted SignalSource that OR-merges into the same natural-key edge) is
		// the minimal, non-breaking change; Permitted stays "known-to-be-permitted"
		// (ARCHITECTURE.md) and a scoped FORBID is NEVER represented here.
		return false, true
	case sdkmodel.SignalMCPAnnotation:
		return false, false
	default:
		return true, false
	}
}

// edgeMetadata captures the bounded, allow-listed, already-redacted display
// references plus the bridge provenance and coverage tier, so the topology view
// and least-privilege findings are self-describing. It stores no payloads — only
// the classification, the connector's redacted natural references and the
// attribution decision (docs/SECURITY-HARDENING.md). The key set is closed: nothing a connector
// could smuggle into a free field reaches storage through here.
func edgeMetadata(edge sdkmodel.EdgeObservation, attr attribution) map[string]any {
	meta := map[string]any{
		"raw_origin_kind":         edge.OriginKind,
		"raw_confidence":          string(edge.Confidence),
		"attribution_reason":      attr.Reason,
		"bridged":                 attr.Bridged,
		"canonical_confidence":    string(attr.Confidence),
		"coverage_tier":           coverageTier(edge.ResourceKind),
		"attribution_tier":        attr.Tier,
		"attribution_tier_reason": attr.TierReason,
		"signal_sources":          string(edge.Source),
	}
	if edge.OriginRef != "" {
		meta["origin_ref"] = edge.OriginRef
	}
	if edge.ResourceKind != "" {
		meta["resource_kind"] = edge.ResourceKind
	}
	if edge.ResourceRef != "" {
		meta["resource_ref"] = edge.ResourceRef
	}
	if edge.ToolRef != "" {
		meta["tool_ref"] = edge.ToolRef
	}
	return meta
}

// resourceName derives a short, non-sensitive display name from a resource's
// kind and (already redacted) reference: the basename for a file path, else the
// reference itself, else the kind when the reference is empty.
func resourceName(kind, ref string) string {
	if ref == "" {
		return kind
	}
	if kind == "file" || kind == "file.path" {
		if b := path.Base(ref); b != "" && b != "." && b != "/" {
			return b
		}
	}
	return ref
}

// tenantOf resolves an event's string tenant reference to a usable business
// tenant, or false for a placeholder/system reference (the module never writes
// to the system partition).
func tenantOf(ref string) (model.TenantID, bool) {
	t, err := model.ParseTenantID(ref)
	if err != nil || t.IsZero() || t.IsSystem() {
		return "", false
	}
	return t, true
}
