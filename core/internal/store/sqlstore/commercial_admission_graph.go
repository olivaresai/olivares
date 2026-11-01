// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"
)

// guardAdmissionGraphFor is the post-module graph of a manifest. The graph and the
// receipt histories compiled over it are a pure function of the manifest, which a binary
// derives from its own registry. Admission asks for them at every unit batch, rollout
// opening, fence close and serving check of one open, and each derivation hashes every
// compiled edition again, so the process derives them once per manifest and releases them
// when opens stop (guardAdmissionKeep). Nothing read from a database is cached: only the
// compiled editions the database is compared against. Callers must not modify what they get.
func guardAdmissionGraphFor(current guardManifest) (guardEditionGraph, error) {
	compiled, err := guardAdmissionCompiled.get(current)
	if err != nil {
		return guardEditionGraph{}, err
	}
	return compiled.graph, nil
}

// guardAdmissionKeep is how long compiled editions outlive their last use. The
// receipt histories of a Business census hold about 100 MiB; an open needs them for
// seconds, so a long-running process gives the memory back, while consecutive opens
// (a restart in the same process, a suite booting estates) keep reusing them.
const guardAdmissionKeep = time.Minute

// guardAdmissionEntry is the compiled editions of one manifest, and the copy of that
// manifest they were compiled from: no caller's backing array is part of an entry. The
// cache finds it by manifestDigest, which hashes each spec's stored SpecSHA256 and not the
// spec, so the copy decides whether an entry is a manifest's.
type guardAdmissionEntry struct {
	manifest guardManifest
	boot     *guardBootVariants
}

// guardAdmissionCache holds the compiled editions of the manifests a process asks about:
// the binary's own, and the compiled ancestors an admission walk checks one by one. They are
// bounded by the compiled editions, not by any input, and released with the idle timer.
type guardAdmissionCache struct {
	keep     time.Duration
	mu       sync.Mutex
	byDigest map[[32]byte]guardAdmissionEntry
	idle     *time.Timer
}

var guardAdmissionCompiled = &guardAdmissionCache{keep: guardAdmissionKeep}

// get returns the compiled editions of current. A manifest that differs from the entry
// under its digest in any field (an edited or hand-built one carrying a stale digest) gets
// editions compiled from itself, and the entry stays as it was.
func (c *guardAdmissionCache) get(current guardManifest) (*guardBootVariants, error) {
	digest, err := current.manifestDigest()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	cached, ok := c.byDigest[digest]
	c.touch()
	c.mu.Unlock()
	if ok && reflect.DeepEqual(cached.manifest, current) {
		return cached.boot, nil
	}
	own := current
	own.Specs = cloneGuardSpecs(current.Specs)
	graph, err := buildGuardAdmissionGraph(own)
	if err != nil {
		return nil, err
	}
	entry := guardAdmissionEntry{manifest: own, boot: &guardBootVariants{
		graph: graph, variantsByEpoch: make(map[int64][]guardBootstrapReceiptVariant), admission: true,
	}}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.byDigest[digest]; ok {
		if reflect.DeepEqual(cached.manifest, current) {
			return cached.boot, nil
		}
		return entry.boot, nil
	}
	if c.byDigest == nil {
		c.byDigest = make(map[[32]byte]guardAdmissionEntry)
	}
	c.byDigest[digest] = entry
	c.touch()
	return entry.boot, nil
}

// touch re-arms the release; the caller holds mu.
func (c *guardAdmissionCache) touch() {
	if c.idle == nil {
		c.idle = time.AfterFunc(c.keep, c.release)
		return
	}
	c.idle.Reset(c.keep)
}

// release drops every entry. A caller that still holds one keeps it, and the next
// get derives it again, so releasing is never a correctness event.
func (c *guardAdmissionCache) release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.byDigest)
}

// Post-module admission extends the closed graph with a completed Community
// predecessor. Historical migration callbacks keep their original declarations
// and transitive source hashes; canonical encoders and SQL writers are shared.
func buildGuardAdmissionGraph(current guardManifest) (guardEditionGraph, error) {
	g, err := guardEditionGraphFor(current)
	if err != nil {
		return g, err
	}
	if len(missingGuardTablesForEpoch(current, current.CodeEpoch)) != 0 {
		return g, nil
	}
	return addGuardCommunityPredecessors(g, current)
}

func guardAdmissionDeltaTables(target guardManifest, d guardEditionDelta) []string {
	if d == guardDeltaCommercial {
		return guardProfileTables(target)
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
			parent = epoch + guardNodeEpoch(epoch) - parent
		}
		if _, present := g.nodes[parent]; present {
			out = append(out, parent)
		}
	}
	membership, ok := guardEditionMembershipForEpoch(guardNodeEpoch(epoch))
	if ok && membership.has(guardDeltaAccessEvidence) {
		parent := guardProfileParent(epoch)
		if _, present := g.nodes[parent]; present {
			out = append(out, parent)
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
	source, target := from, to
	if guardProfileParent(to) == from {
		source, target = -guardNodeEpoch(from), guardNodeEpoch(to)
	}
	delta, err := guardAdmissionDelta(source, target)
	if err != nil {
		return guardManifestEdge{}, err
	}
	want := map[string]bool{}
	for _, table := range guardAdmissionDeltaTables(toNode.Manifest, delta) {
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
