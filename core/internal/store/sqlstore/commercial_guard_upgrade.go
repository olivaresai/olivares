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
	census := append(append([]string(nil), registered...), guardCommercialTables[:]...)
	sort.Strings(census)
	wide, wideGraph, err := build(census)
	if err != nil {
		return nil, guardManifest{}, guardEditionGraph{}, err
	}
	widened := guardGraphDigests(wideGraph)
	// Every receipt must be a digest one of the two censuses compiles: a stray or unknown one
	// leaves the registry census, which refuses the history.
	if slices.ContainsFunc(receipts, func(r guardReceipt) bool { return !own[r.CodeSHA256] && !widened[r.CodeSHA256] }) {
		return registered, manifest, graph, nil
	}
	return census, wide, wideGraph, nil
}

// guardGraphDigests is the set of code digests a graph's compiled editions carry.
func guardGraphDigests(graph guardEditionGraph) map[[32]byte]bool {
	digests := make(map[[32]byte]bool, len(graph.nodes))
	for _, node := range graph.nodes {
		digests[node.Manifest.CodeSHA256] = true
	}
	return digests
}
