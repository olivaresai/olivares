// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"context"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Coverage tiers (ARCHITECTURE.md, §91). The R/RW map declares the fidelity of each
// source rather than hiding the gradation: a clean SQL/object/warehouse audit is
// authoritative; a lossy store (Mongo, vector DBs) yields coarse edges; a store
// with no passive R/RW signal at all (Redis, SQLite, D1) is opaque to capture.
const (
	tierClean  = "clean"  // SQL tables, object stores, warehouses, kernel ground-truth
	tierLossy  = "lossy"  // document/vector stores: edges exist but coarse
	tierOpaque = "opaque" // no passive R/RW signal (Redis, SQLite, D1)
	tierMixed  = "mixed"  // cooperative/tool signals not tied to one store class
)

// coverageTier classifies a resource kind into its capture-fidelity tier, so the
// graph can show honestly how much it can know about an edge (ARCHITECTURE.md). It is
// prefix-based on the ResourceKind the connectors emit.
func coverageTier(resourceKind string) string {
	rk := strings.ToLower(resourceKind)
	switch {
	case hasAnyPrefix(rk, "postgres.", "mysql.", "mssql.", "oracle.", "snowflake.", "bigquery.", "redshift.", "s3.", "gcs.", "azureblob.", "file", "net."):
		return tierClean
	case hasAnyPrefix(rk, "mongo.", "vector.", "pinecone.", "weaviate.", "elasticsearch.", "dynamodb."):
		return tierLossy
	case hasAnyPrefix(rk, "redis.", "sqlite.", "d1.", "memcached."):
		return tierOpaque
	default:
		// MCP tools, HTTP APIs, shell, agent tasks: cooperative/tool signals not
		// tied to one store class.
		return tierMixed
	}
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// existingEdge reads the current edge on a new edge's natural key
// (origin_kind, origin_id, resource_id, mode), or ok=false if none exists yet.
// It runs inside the caller's Mutate transaction, so the read and the
// subsequent Upsert are consistent.
func existingEdge(ctx context.Context, sc store.Scope, e model.AccessEdge) (model.AccessEdge, bool, error) {
	list, _, err := sc.AccessEdges().List(ctx, model.Query{
		Filters: []model.Filter{
			eq("origin_kind", e.OriginKind),
			eq("origin_id", e.OriginID.String()),
			eq("resource_id", e.ResourceID.String()),
			eq("mode", string(e.Mode)),
		},
		Limit: 1,
	})
	if err != nil {
		return model.AccessEdge{}, false, err
	}
	if len(list) == 0 {
		return model.AccessEdge{}, false, nil
	}
	return list[0], true, nil
}

// fuse reconciles an incoming edge against the previously stored one on the same
// natural key (ARCHITECTURE.md — multi-signal fusion with honest confidence). The
// store's Upsert OR-merges observed/permitted and REPLACES confidence,
// signal_source and metadata with the incoming values, so the reconciliation
// must be pre-computed here:
//
//   - Confidence never DOWNGRADES: it rises to attributed if either signal is
//     attributed. An untrusted/approximate signal (e.g. an MCP annotation, an
//     eBPF kernel edge with an ambiguous process) cannot lower an attributed
//     edge, and an annotation alone never raises an edge to attributed.
//   - The set of contributing signal sources accumulates in metadata, so a fused
//     edge shows every signal that corroborated it (the provenance, ARCHITECTURE.md).
//   - bridged stays true once it has ever held; first_seen keeps the earliest.
func fuse(in *model.AccessEdge, prev model.AccessEdge) {
	in.Confidence = bestConfidence(prev.Confidence, in.Confidence)

	if prevFS := prev.FirstSeen.String(); prevFS != "" {
		if inFS := in.FirstSeen.String(); inFS == "" || prevFS < inFS {
			in.FirstSeen = prev.FirstSeen
		}
	}

	sources := sourceSet(prev.Metadata)
	if prev.SignalSource != "" {
		sources[string(prev.SignalSource)] = struct{}{}
	}
	if in.SignalSource != "" {
		sources[string(in.SignalSource)] = struct{}{}
	}
	if in.Metadata == nil {
		in.Metadata = map[string]any{}
	}
	in.Metadata["signal_sources"] = joinSet(sources)

	if wasBridged(prev.Metadata) {
		in.Metadata["bridged"] = true
	}

	// attribution_tier never DOWNGRADES on fusion, mirroring confidence: a later,
	// weaker signal (e.g. an eBPF backstop arriving after a cooperative otel that
	// firmly named the agent) must not lower a firmly-attributed edge to approximate
	// (ARCHITECTURE.md G8). The reason follows whichever tier wins.
	inTier, _ := in.Metadata["attribution_tier"].(string)
	if prevTier, _ := prev.Metadata["attribution_tier"].(string); strongerTier(prevTier, inTier) == prevTier && prevTier != inTier {
		in.Metadata["attribution_tier"] = prevTier
		if r, ok := prev.Metadata["attribution_tier_reason"].(string); ok {
			in.Metadata["attribution_tier_reason"] = r
		}
	}
}

// bestConfidence returns the stronger of two confidences: attributed beats
// approximate. Anything unknown is treated as approximate (never silently firm).
func bestConfidence(a, b sdkmodel.Confidence) sdkmodel.Confidence {
	if a == sdkmodel.ConfidenceAttributed || b == sdkmodel.ConfidenceAttributed {
		return sdkmodel.ConfidenceAttributed
	}
	return sdkmodel.ConfidenceApproximate
}

// sourceSet extracts the accumulated signal-source set from an edge's metadata.
func sourceSet(meta map[string]any) map[string]struct{} {
	out := map[string]struct{}{}
	if meta == nil {
		return out
	}
	if s, ok := meta["signal_sources"].(string); ok && s != "" {
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out[p] = struct{}{}
			}
		}
	}
	return out
}

// joinSet renders a set as a stable, comma-joined string (sorted for determinism).
func joinSet(set map[string]struct{}) string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// wasBridged reports whether a stored edge's metadata recorded a successful bridge.
func wasBridged(meta map[string]any) bool {
	if meta == nil {
		return false
	}
	b, _ := meta["bridged"].(bool)
	return b
}
