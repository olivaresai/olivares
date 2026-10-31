// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardBootVariants owns the compiled receipt histories for a boot verification.
// Migration verification continues to use the immutable fresh walk.
type guardBootVariants struct {
	graph           guardEditionGraph
	variantsByEpoch map[int64][]guardBootstrapReceiptVariant
	// admission walks the post-module graph, which also holds the completed
	// Community predecessors of a Business census.
	admission bool
	// mu guards variantsByEpoch for a boot shared by concurrent opens.
	mu sync.Mutex
}

func (b *guardBootVariants) parentEpochs(epoch int64) []int64 {
	if b.admission {
		return guardAdmissionParentEpochs(b.graph, epoch)
	}
	return b.graph.parentEpochs(epoch)
}

func (b *guardBootVariants) edge(from, to int64) (guardManifestEdge, error) {
	if b.admission {
		return guardAdmissionEdge(b.graph, from, to)
	}
	return b.graph.edge(from, to)
}

func (b *guardBootVariants) variants(epoch int64) ([]guardBootstrapReceiptVariant, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.derive(epoch)
}

func (b *guardBootVariants) derive(epoch int64) ([]guardBootstrapReceiptVariant, error) {
	if variants, ok := b.variantsByEpoch[epoch]; ok {
		return variants, nil
	}
	graph := b.graph
	node, ok := graph.node(epoch)
	if !ok {
		return nil, fmt.Errorf("%w: edition %d is not in this graph", ErrGuardManifestNoEdge, epoch)
	}
	variants, err := guardBootstrapVariantsAtNode(node)
	if err != nil {
		return nil, err
	}
	empty, err := emptyRetainedDigest()
	if err != nil {
		return nil, err
	}
	for _, parent := range b.parentEpochs(epoch) {
		edge, eerr := b.edge(parent, epoch)
		if eerr != nil {
			return nil, eerr
		}
		sources, serr := b.derive(parent)
		if serr != nil {
			return nil, serr
		}
		for _, source := range sources {
			if !guardEditionSealEligible(edge, source) {
				continue
			}
			seal, sealErr := guardEditionTransitionSeal(edge, 0, empty, source.TerminalReceiptID)
			if sealErr != nil {
				return nil, sealErr
			}
			variants = append(variants, guardBootstrapReceiptVariant{
				State:             guardEditionReceiptsSealed,
				Receipts:          append(append([]guardReceipt(nil), source.Receipts...), seal),
				Path:              source.Path + guardEditionPath(">") + guardEditionPath(strconv.FormatInt(epoch, 10)),
				CompletedV7:       true,
				CompletedV9:       source.CompletedV9 || node.Membership.has(guardDeltaAccessEvidence),
				TerminalReceiptID: seal.ReceiptID,
				Epoch:             epoch,
			})
		}
	}
	// The memo is shared: a caller that appends to the variants or to a variant's
	// receipts must copy them, so neither slice keeps spare capacity to write into.
	for i := range variants {
		variants[i].Receipts = slices.Clip(variants[i].Receipts)
	}
	variants = slices.Clip(variants)
	b.variantsByEpoch[epoch] = variants
	return variants, nil
}

// classifyGuardBootReceiptVariant keeps the migration classifier's exact checks,
// but shares the compiled variants between its current and predecessor walks.
func classifyGuardBootReceiptVariant(
	ctx context.Context,
	q guardEditionQuerier,
	dia dialect.Dialect,
	current guardManifest,
) (guardBootstrapReceiptVariant, []guardReceipt, error) {
	actual, err := readGuardBootstrapReceiptHistory(ctx, q, dia)
	if err != nil {
		return guardBootstrapReceiptVariant{}, nil, err
	}
	accept := func(variant guardBootstrapReceiptVariant) (guardBootstrapReceiptVariant, []guardReceipt, error) {
		if err := verifyGuardBootstrapReceiptClaims(actual, current.Format); err != nil {
			return guardBootstrapReceiptVariant{}, nil, err
		}
		if err := verifyGuardReceiptRolloutChains(ctx, q, dia); err != nil {
			return guardBootstrapReceiptVariant{}, nil, err
		}
		return variant, actual, nil
	}
	graph, err := guardEditionGraphFor(current)
	if err != nil {
		return guardBootstrapReceiptVariant{}, nil, err
	}
	boot := guardBootVariants{graph: graph, variantsByEpoch: make(map[int64][]guardBootstrapReceiptVariant)}
	variants, err := boot.variants(current.CodeEpoch)
	if err != nil {
		return guardBootstrapReceiptVariant{}, nil, err
	}
	var differences []string
	var matches []guardBootstrapReceiptVariant
	for _, variant := range variants {
		if diff := guardBootstrapReceiptSetDifference(actual, variant.Receipts); diff == "" {
			matches = append(matches, variant)
		} else {
			differences = append(differences, fmt.Sprintf("%s/path-%s: %s", variant.State, variant.Path, diff))
		}
	}
	// PREDECESSOR HISTORIES: the database is standing at one of the compiled parents and
	// has not crossed the last edge. Every parent is enumerated and named, because with
	// more than one of them "the predecessor" is not a fact.
	for _, parent := range graph.parentEpochs(current.CodeEpoch) {
		edge, eerr := graph.edge(parent, current.CodeEpoch)
		if eerr != nil {
			return guardBootstrapReceiptVariant{}, nil, eerr
		}
		sources, serr := boot.variants(parent)
		if serr != nil {
			return guardBootstrapReceiptVariant{}, nil, serr
		}
		for _, source := range sources {
			if !guardEditionSealEligible(edge, source) {
				continue
			}
			if diff := guardBootstrapReceiptSetDifference(actual, source.Receipts); diff == "" {
				source.State = guardEditionReceiptsPredecessor
				matches = append(matches, source)
			} else {
				differences = append(differences, fmt.Sprintf("predecessor-%d/path-%s: %s", parent, source.Path, diff))
			}
		}
	}
	switch len(matches) {
	case 1:
		return accept(matches[0])
	case 0:
		return guardBootstrapReceiptVariant{}, nil, fmt.Errorf("%w: bootstrap history is not an exact compiled current, predecessor, or transitioned variant (%s)",
			ErrGuardBootstrapReceiptsInvalid, strings.Join(differences, "; "))
	default:
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, fmt.Sprintf("%s/path-%s", m.State, m.Path))
		}
		return guardBootstrapReceiptVariant{}, nil, fmt.Errorf(
			"%w: the bootstrap receipt history matches %d compiled variants (%s); an ambiguous history is not an authorisation",
			ErrGuardManifestNoEdge, len(matches), strings.Join(names, ", "))
	}
}

// verifyGuardBootEditionHistory is the boot selector. The migration selector in
// guardplane.go stays immutable; both selectors still re-read every durable fact.
func verifyGuardBootEditionHistory(
	ctx context.Context,
	q guardEditionQuerier,
	dia dialect.Dialect,
	current guardManifest,
) (guardEditionHistory, error) {
	if err := requireCompleteGuardCurrentEdition(current); err != nil {
		return guardEditionHistory{}, err
	}
	graph, err := guardEditionGraphFor(current)
	if err != nil {
		return guardEditionHistory{}, err
	}
	if err := verifyGuardControlPlaneShape(ctx, q, dia); err != nil {
		return guardEditionHistory{}, err
	}
	receiptVariant, _, err := classifyGuardBootReceiptVariant(ctx, q, dia, current)
	if err != nil {
		return guardEditionHistory{}, err
	}
	if err := verifyGuardControlPlaneCatalog(ctx, q, dia, current.Format); err != nil {
		return guardEditionHistory{}, err
	}
	inventory, err := classifyGuardEditionInventory(ctx, q, dia, current)
	if err != nil {
		return guardEditionHistory{}, err
	}
	if len(graph.parentEpochs(current.CodeEpoch)) == 0 {
		return guardEditionHistory{}, fmt.Errorf("%w: epoch %d has no compiled edition edge",
			ErrGuardManifestNoEdge, current.CodeEpoch)
	}
	gateSelection, err := classifyGuardEditionGate(ctx, q, dia, current, graph, inventory.Revision, inventory.Retained)
	if err != nil {
		return guardEditionHistory{}, err
	}
	kind, err := correlateGuardEditionHistory(dia.Name(), receiptVariant.State, inventory.State, gateSelection.State)
	if err != nil {
		return guardEditionHistory{}, err
	}
	if receiptVariant.Path != inventory.Path {
		return guardEditionHistory{}, fmt.Errorf("%w: receipt history follows lineage %s while inventory follows lineage %s; no transition writes that cross-product",
			ErrGuardManifestNoEdge, receiptVariant.Path, inventory.Path)
	}
	// The three projections must also agree about WHICH compiled parent they stand on.
	// Path equality already implies it for receipts and inventory; the gate is read from
	// a different stream, so its parent is compared explicitly rather than assumed.
	parentEpoch := inventory.ParentEpoch
	if gateSelection.State == guardEditionGatePredecessor && gateSelection.ParentEpoch != parentEpoch {
		return guardEditionHistory{}, fmt.Errorf("%w: the gate stream stands on edition %d while receipts and inventory stand on %d",
			ErrGuardManifestNoEdge, gateSelection.ParentEpoch, parentEpoch)
	}
	if kind == guardEditionHistoryPredecessor && receiptVariant.CompletedV7 {
		kind = guardEditionHistoryPredecessorV7
	}
	if dia.Name() == store.EnginePostgres &&
		(kind == guardEditionHistoryPredecessor || kind == guardEditionHistoryPredecessorV7 ||
			kind == guardEditionHistoryTransitioned) {
		if err := verifyGuardHistoricalGatePath(ctx, q, dia, graph, parentEpoch,
			receiptVariant.Path, inventory.Revision, inventory.Retained); err != nil {
			return guardEditionHistory{}, err
		}
	}
	return guardEditionHistory{
		Kind: kind, Revision: inventory.Revision, Retained: inventory.Retained,
		Gate: gateSelection.Gate, GateState: gateSelection.State,
		Path: receiptVariant.Path, TerminalReceiptID: receiptVariant.TerminalReceiptID,
		ParentEpoch: parentEpoch, CompletedV9: receiptVariant.CompletedV9,
	}, nil
}
