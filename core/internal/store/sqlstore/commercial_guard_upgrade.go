// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
)

// This is an admission delta, not a feature implementation or schema. Business
// registers these three additional engine-guarded relations together. Subtracting
// exactly this compiled set reconstructs Community; an arbitrary smaller census
// must never become an authorized predecessor.
var guardCommercialTables = [...]string{
	"ldap_revision",
	"session_cockpit_console_audit_index",
	"session_cockpit_runtime_session_original",
}

// The profile edge does not advance the published code epoch. Historical Business
// manifests already used that epoch with their larger base census, and changing
// their canonical bytes would strand existing installations. Negative node IDs
// distinguish Community nodes inside a Business graph; only Manifest.CodeEpoch
// goes into durable events and receipts.
const guardDeltaCommercial guardEditionDelta = 1 << 7

func guardNodeEpoch(node int64) int64 {
	if node < 0 {
		return -node
	}
	return node
}

func addGuardCommunityPredecessors(g guardEditionGraph, current guardManifest) (guardEditionGraph, error) {
	commercial := make(map[string]bool, len(guardCommercialTables))
	for _, table := range guardCommercialTables {
		commercial[table] = true
	}
	tables := make([]string, 0, len(current.Specs))
	found := 0
	for _, spec := range current.Specs {
		if commercial[spec.Key.Relation] {
			found++
		} else {
			tables = append(tables, spec.Key.Relation)
		}
	}
	if found != len(commercial) {
		return g, nil
	}
	community, err := buildGuardManifestAtEpoch(tables, current.CodeEpoch)
	if err != nil {
		return g, err
	}
	ancestors, err := guardEditionGraphFor(community)
	if err != nil {
		return g, err
	}
	for _, epoch := range ancestors.order {
		node, _ := ancestors.node(epoch)
		node.Epoch = -epoch
		g.nodes[node.Epoch] = node
		g.order = append(g.order, node.Epoch)
	}
	return g, nil
}

func runGuardAdmissionUnits(ctx context.Context, mdb dialect.Execer, dia dialect.Dialect, registered []string, observerDSN string, hardened bool, session func(context.Context) (rowQuerier, func(), error)) (guardRolloutSummary, error) {
	m, err := buildGuardManifest(registered)
	if err != nil {
		return guardRolloutSummary{}, err
	}
	graph, err := guardAdmissionGraphFor(m)
	if err != nil {
		return guardRolloutSummary{}, err
	}
	if _, commercial := graph.nodes[-m.CodeEpoch]; !commercial {
		return runAppendOnlyGuardUnits(ctx, mdb, dia, registered, observerDSN, hardened, session)
	}
	return guardAdmissionRunUnits(ctx, mdb, dia, registered, observerDSN, hardened, session)
}
