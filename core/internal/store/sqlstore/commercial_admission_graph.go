// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"fmt"
	"sort"
)

// Post-module admission extends the closed graph with a completed Community
// predecessor. Historical migration callbacks keep their original declarations
// and transitive source hashes; canonical encoders and SQL writers are shared.
func guardAdmissionGraphFor(current guardManifest) (guardEditionGraph, error) {
	g, err := guardEditionGraphFor(current)
	if err != nil {
		return g, err
	}
	if len(missingGuardTablesForEpoch(current, current.CodeEpoch)) != 0 {
		return g, nil
	}
	return addGuardCommunityPredecessors(g, current)
}

func guardAdmissionDeltaTables(d guardEditionDelta) []string {
	if d == guardDeltaCommercial {
		return guardCommercialTables[:]
	}
	return guardEditionDeltaTables(d)
}

func guardAdmissionModuleDelta(d guardEditionDelta) bool {
	return d == guardDeltaCommercial || guardEditionModuleDelta(d)
}

func guardAdmissionDelta(from, to int64) (guardEditionDelta, error) {
	if from < 0 && to > 0 {
		membership, ok := guardEditionMembershipForEpoch(to)
		if -from == to && ok && membership.has(guardDeltaAccessEvidence) {
			return guardDeltaCommercial, nil
		}
		return 0, fmt.Errorf("%w: %d -> %d is not a compiled profile edge", ErrGuardManifestNoEdge, from, to)
	}
	if from > 0 && to < 0 {
		return 0, fmt.Errorf("%w: a Business node cannot drop its profile delta", ErrGuardManifestNoEdge)
	}
	return guardEditionEdgeDelta(guardNodeEpoch(from), guardNodeEpoch(to))
}

func guardAdmissionParentEpochs(g guardEditionGraph, epoch int64) []int64 {
	original := g.parentEpochs(guardNodeEpoch(epoch))
	out := make([]int64, 0, len(original)+1)
	for _, parent := range original {
		if epoch < 0 {
			parent = -parent
		}
		if _, present := g.nodes[parent]; present {
			out = append(out, parent)
		}
	}
	membership, ok := guardEditionMembershipForEpoch(epoch)
	if epoch > 0 && ok && membership.has(guardDeltaAccessEvidence) {
		if _, present := g.nodes[-epoch]; present {
			out = append(out, -epoch)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func guardAdmissionEdge(g guardEditionGraph, from, to int64) (guardManifestEdge, error) {
	fromNode, okFrom := g.node(from)
	toNode, okTo := g.node(to)
	if !okFrom || !okTo {
		return guardManifestEdge{}, fmt.Errorf("%w: edition %d -> %d is not in this graph",
			ErrGuardManifestNoEdge, from, to)
	}
	delta, err := guardAdmissionDelta(from, to)
	if err != nil {
		return guardManifestEdge{}, err
	}
	want := map[string]bool{}
	for _, table := range guardAdmissionDeltaTables(delta) {
		want[table] = true
	}
	additions := make([]guardSpec, 0, len(want))
	for _, spec := range toNode.Manifest.Specs {
		if want[spec.Key.Relation] {
			additions = append(additions, spec)
			continue
		}
		old, present := fromNode.Manifest.lookup(spec.Key)
		if !present {
			return guardManifestEdge{}, fmt.Errorf(
				"sqlstore: guard edition %d -> %d carries %s, which its predecessor does not declare",
				from, to, spec.Key)
		}
		if !guardSpecsByteIdentical(old, spec) {
			return guardManifestEdge{}, fmt.Errorf(
				"sqlstore: guard edition %d changes predecessor entry %s instead of carrying it forward byte-identically",
				to, spec.Key)
		}
	}
	if len(additions) != len(want) {
		return guardManifestEdge{}, fmt.Errorf(
			"sqlstore: guard edition %d -> %d adds %d of the %d relations the %s delta names",
			from, to, len(additions), len(want), delta)
	}
	for _, old := range fromNode.Manifest.Specs {
		if _, present := toNode.Manifest.lookup(old.Key); !present {
			return guardManifestEdge{}, fmt.Errorf(
				"sqlstore: guard edition %d drops predecessor entry %s without a retention transition",
				to, old.Key)
		}
	}
	return guardManifestEdge{From: fromNode.Manifest, To: toNode.Manifest, Additions: additions}, nil
}

func guardAdmissionLineagesEndingAt(g guardEditionGraph, epoch int64) ([]guardEditionLineage, error) {
	node, ok := g.node(epoch)
	if !ok {
		return nil, fmt.Errorf("%w: edition %d is not in this graph", ErrGuardManifestNoEdge, epoch)
	}
	out := []guardEditionLineage{{Nodes: []guardEditionNode{node}}}
	for _, parent := range guardAdmissionParentEpochs(g, epoch) {
		edge, err := guardAdmissionEdge(g, parent, epoch)
		if err != nil {
			return nil, err
		}
		prefixes, err := guardAdmissionLineagesEndingAt(g, parent)
		if err != nil {
			return nil, err
		}
		for _, prefix := range prefixes {
			out = append(out, prefix.extend(node, edge))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].Nodes) != len(out[j].Nodes) {
			return len(out[i].Nodes) < len(out[j].Nodes)
		}
		return out[i].path() < out[j].path()
	})
	return out, nil
}

func guardAdmissionPredecessorEdges(g guardEditionGraph, epoch int64) ([]guardManifestEdge, error) {
	var out []guardManifestEdge
	for _, parent := range guardAdmissionParentEpochs(g, epoch) {
		edge, err := guardAdmissionEdge(g, parent, epoch)
		if err != nil {
			return nil, err
		}
		out = append(out, edge)
	}
	return out, nil
}

func guardAdmissionEdgeSource(edge guardManifestEdge) int64 {
	if edge.From.CodeEpoch == edge.To.CodeEpoch {
		return -edge.From.CodeEpoch
	}
	return edge.From.CodeEpoch
}
