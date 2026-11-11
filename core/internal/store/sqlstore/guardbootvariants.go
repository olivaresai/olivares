// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"fmt"
	"slices"
	"strconv"
	"sync"
)

// guardBootVariants owns the compiled receipt histories of one edition-history
// verification, at boot and in migrations alike: each edition's variants are derived once.
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
