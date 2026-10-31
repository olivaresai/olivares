// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"

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

// Negative IDs name profile predecessors, not durable code epochs. Each additional
// layer has its own range so Business can also compile pre-AI-terms Community histories.
const guardProfileNodeStride int64 = 1000

func guardNodeEpoch(node int64) int64 {
	if node < 0 {
		return -node % guardProfileNodeStride
	}
	return node
}

func guardProfileParent(node int64) int64 {
	if node > 0 {
		return -node
	}
	return node - guardProfileNodeStride
}

func guardProfileTables(current guardManifest) []string {
	present := make(map[string]bool, len(current.Specs))
	for _, spec := range current.Specs {
		present[spec.Key.Relation] = true
	}
	set := guardAITermsTables[:]
	if slices.ContainsFunc(guardCommercialTables[:], func(table string) bool { return present[table] }) {
		set = guardCommercialTables[:]
	}
	if slices.ContainsFunc(set, func(table string) bool { return !present[table] }) {
		return nil
	}
	return set
}

func addGuardCommunityPredecessors(g guardEditionGraph, current guardManifest) (guardEditionGraph, error) {
	profile := guardProfileTables(current)
	if profile == nil {
		return g, nil
	}
	tables := make([]string, 0, len(current.Specs))
	for _, spec := range current.Specs {
		if !slices.Contains(profile, spec.Key.Relation) {
			tables = append(tables, spec.Key.Relation)
		}
	}
	community, err := buildGuardManifestAtEpoch(tables, current.CodeEpoch)
	if err != nil {
		return g, err
	}
	ancestors, err := buildGuardAdmissionGraph(community)
	if err != nil {
		return g, err
	}
	for _, epoch := range ancestors.order {
		node, _ := ancestors.node(epoch)
		node.Epoch = guardProfileParent(epoch)
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

// bootGuardCensus builds the guard manifest and edition graph this boot verifies and governs.
// Normally the census is the registry's own. A directory a Business build wrote records
// bootstrap receipts bound to Business census digests, which no Community variant carries
// (Business X.Y-1 -> Community X.Y -> Business X.Y, #1883, #2741): when a receipt is bound to
// a digest only the registry plus the three Business relations produces, Community verifies
// and keeps guarding that wider census, and the Business admission graph (with its Community
// predecessor) does the exact verification. The receipt digest only selects the census; an
// unknown history matches neither census, and any ledger read that fails leaves the registry's
// own census, whose verification reports the same failure with its own words.
func bootGuardCensus(ctx context.Context, db guardEditionQuerier, dia dialect.Dialect, registered []string) ([]string, guardManifest, guardEditionGraph, error) {
	build := func(tables []string) (guardManifest, guardEditionGraph, error) {
		manifest, err := buildGuardManifest(tables)
		if err != nil {
			return guardManifest{}, guardEditionGraph{}, err
		}
		if err := requireCompleteGuardCurrentEdition(manifest); err != nil {
			return guardManifest{}, guardEditionGraph{}, fmt.Errorf("sqlstore: guard manifest: %w", err)
		}
		graph, err := guardEditionGraphFor(manifest)
		if err != nil {
			return guardManifest{}, guardEditionGraph{}, fmt.Errorf("sqlstore: guard edition graph: %w", err)
		}
		return manifest, graph, nil
	}
	manifest, graph, err := build(registered)
	if err != nil {
		return nil, guardManifest{}, guardEditionGraph{}, err
	}
	// A Business registry already holds these relations: its census is the wider one.
	for _, table := range registered {
		if slices.Contains(guardCommercialTables[:], table) {
			return registered, manifest, graph, nil
		}
	}
	receipts, rerr := readGuardBootstrapReceiptHistory(ctx, db, dia)
	if rerr != nil {
		// No ledger yet (a fresh database) or an unreadable one is the expected, quiet case; a
		// ledger whose contents fail their own digest is worth a line. Either way the registry
		// census stays and its admission refuses with the same error in its own words.
		level := slog.LevelWarn
		if errors.Is(rerr, ErrGuardReceiptLedgerUnavailable) {
			level = slog.LevelDebug
		}
		slog.Log(ctx, level, "sqlstore: guard receipts not read for the census; using the registry census", "error", rerr)
		return registered, manifest, graph, nil
	}
	own := guardGraphDigests(graph)
	foreign := slices.ContainsFunc(receipts, func(r guardReceipt) bool { return !own[r.CodeSHA256] })
	if !foreign {
		return registered, manifest, graph, nil
	}
	// A Business-written directory retains its wider census. The AI terms
	// predecessor is now admitted by the current census's reviewed profile edge.
	candidates := [][]string{append(append([]string(nil), registered...), guardCommercialTables[:]...)}
	for _, census := range candidates {
		sort.Strings(census)
		other, otherGraph, err := build(census)
		if err != nil {
			return nil, guardManifest{}, guardEditionGraph{}, err
		}
		known := guardGraphDigests(otherGraph)
		// Every receipt must be a digest one of the two censuses compiles: a stray or unknown one
		// leaves the registry census, which refuses the history. This only selects a census. A
		// ledger that mixes the two passes it, and the exact history verification of the selected
		// census is what refuses it: that verification must stay exact.
		if !slices.ContainsFunc(receipts, func(r guardReceipt) bool { return !own[r.CodeSHA256] && !known[r.CodeSHA256] }) {
			return census, other, otherGraph, nil
		}
	}
	return registered, manifest, graph, nil
}

// Community has registered the two append-only AI terms relations since 0.5 (modules/aiterms), at
// the code epoch a directory an earlier Community release wrote already records without them.
var guardAITermsTables = [...]string{
	"aiterms_acceptance",
	"aiterms_policy",
}

// guardGraphDigests is the set of code digests a graph's compiled editions carry.
func guardGraphDigests(graph guardEditionGraph) map[[32]byte]bool {
	digests := make(map[[32]byte]bool, len(graph.nodes))
	for _, node := range graph.nodes {
		digests[node.Manifest.CodeSHA256] = true
	}
	return digests
}
