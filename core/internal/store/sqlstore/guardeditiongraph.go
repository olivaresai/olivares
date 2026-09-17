// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// guardeditiongraph.go turns the guard editions from a CHAIN into a finite DAG.
//
// WHY IT STOPPED BEING A CHAIN. Epochs 2, 3 and 4 add relations that arrive in one
// order because each belongs to a later module slice: directory (D2) is core, the
// communication (DF) and protocol (DK) deltas are sessions. A core-only build carries
// D2 and nothing else, a sessions build carries all three, and a chain expressed the
// whole truth: removing "the delta this epoch adds" named exactly one predecessor.
//
// The access-evidence delta (DA) is different in kind, and that difference is the whole
// reason this file exists: it is CORE, so every current build carries it, while DF/DK
// remain conditional on which modules were registered. A database can therefore be
// upgraded to a DA-bearing edition from three different starting points — a core-only
// E2, a Slice-F E3 or a K5 E4 — and each of those has to reach a DIFFERENT target,
// because the target must keep the module relations the source already had. One
// predecessor per node cannot express that, so the node keeps every compiled parent and
// the selector proves exactly one recorded history matches.
//
// WHAT DID NOT CHANGE, and must not. An edge still exists only between two manifests
// built by the same canonical encoder from the same BASE census; its additions are still
// a complete, nonempty, exactly-named delta; every carried spec is still byte-identical;
// and the durable identity of a node is still its stored (format, code_epoch,
// code_sha256) tuple, never a label. The graph is derived from the CLOSED registry and
// from nothing a database supplied.

// guardEditionDelta is one closed set of append-only relations that exactly one class of
// edge adds. The values are bits so a node's membership is a set rather than a level:
// "has DA" and "has DF" are independent facts, which is precisely what the chain could
// not say.
type guardEditionDelta uint8

const (
	guardDeltaDirectory      guardEditionDelta = 1 << iota // D2, core directory tombstones
	guardDeltaCommunication                                // DF, Slice F sessions evidence
	guardDeltaProtocol                                     // DK, K5 sessions replay/subscription
	guardDeltaAccessEvidence                               // DA, core access-evidence records
)

// guardEditionMembership is the set of deltas a node carries above its base census.
type guardEditionMembership uint8

func (m guardEditionMembership) has(d guardEditionDelta) bool {
	return m&guardEditionMembership(d) != 0
}

func (m guardEditionMembership) with(d guardEditionDelta) guardEditionMembership {
	return m | guardEditionMembership(d)
}

func (m guardEditionMembership) without(d guardEditionDelta) guardEditionMembership {
	return m &^ guardEditionMembership(d)
}

// guardEditionDeltaOrder is the order deltas are named in diagnostics and the order
// parent edges are enumerated in. It is fixed rather than derived from the bit values so
// a future delta cannot silently reorder an existing message.
var guardEditionDeltaOrder = [...]guardEditionDelta{
	guardDeltaDirectory, guardDeltaCommunication, guardDeltaProtocol, guardDeltaAccessEvidence,
}

func (d guardEditionDelta) String() string {
	switch d {
	case guardDeltaDirectory:
		return "D2/directory"
	case guardDeltaCommunication:
		return "DF/communication"
	case guardDeltaProtocol:
		return "DK/protocol"
	case guardDeltaAccessEvidence:
		return "DA/access-evidence"
	default:
		return "unknown-delta"
	}
}

// guardAccessEvidenceTables is the complete DA delta: the four append-only
// access-evidence relations core registers unconditionally.
//
// They are the SAME constants the descriptors use rather than a second spelling, because
// a manifest that named a relation the descriptors do not create would describe objects
// nothing installs — and the first symptom would be a correct database refusing to boot.
var guardAccessEvidenceTables = [...]string{
	policyArtifactTable,
	authorityTransitionTable,
	actionObservationTable,
	authorizationDecisionTable,
}

// guardEditionDeltaTables returns the complete, exact relation set of one delta.
func guardEditionDeltaTables(d guardEditionDelta) []string {
	switch d {
	case guardDeltaDirectory:
		// Sorted, so the "missing" diagnostics of every delta read the same way.
		return []string{guardEpoch2DirectoryTombstoneTable, guardEpoch2UserTombstoneTable}
	case guardDeltaCommunication:
		return guardEpoch3CommunicationTables[:]
	case guardDeltaProtocol:
		return guardEpoch4ProtocolTables[:]
	case guardDeltaAccessEvidence:
		return guardAccessEvidenceTables[:]
	default:
		return nil
	}
}

// guardEditionDeltaOf reports which delta a relation belongs to, if any. A relation that
// belongs to none is part of the base census B.
func guardEditionDeltaOf(table string) (guardEditionDelta, bool) {
	for _, d := range guardEditionDeltaOrder {
		for _, member := range guardEditionDeltaTables(d) {
			if table == member {
				return d, true
			}
		}
	}
	return 0, false
}

// guardEditionNodes is the CLOSED list of editions this binary knows, as (epoch,
// membership) pairs. It is a literal table rather than "every subset" because most
// subsets are states no release ever produced: there is no edition with the protocol
// delta and not the communication delta it extends, and none with access evidence and
// not the directory relations core has carried since v7.
//
// Epochs 1-4 are the historical chain, unchanged. 5, 6 and 7 are the access-evidence
// editions over each of the three shapes a deployed database can be in.
var guardEditionNodes = []struct {
	Epoch      int64
	Membership guardEditionMembership
}{
	{1, 0},
	{2, guardEditionMembership(guardDeltaDirectory)},
	{3, guardEditionMembership(guardDeltaDirectory | guardDeltaCommunication)},
	{4, guardEditionMembership(guardDeltaDirectory | guardDeltaCommunication | guardDeltaProtocol)},
	{5, guardEditionMembership(guardDeltaDirectory | guardDeltaAccessEvidence)},
	{6, guardEditionMembership(guardDeltaDirectory | guardDeltaCommunication | guardDeltaAccessEvidence)},
	{7, guardEditionMembership(guardDeltaDirectory | guardDeltaCommunication | guardDeltaProtocol | guardDeltaAccessEvidence)},
}

func guardEditionMembershipForEpoch(epoch int64) (guardEditionMembership, bool) {
	for _, n := range guardEditionNodes {
		if n.Epoch == epoch {
			return n.Membership, true
		}
	}
	return 0, false
}

func guardEditionEpochForMembership(m guardEditionMembership) (int64, bool) {
	for _, n := range guardEditionNodes {
		if n.Membership == m {
			return n.Epoch, true
		}
	}
	return 0, false
}

// guardEditionDeltasAddedByEpoch is the set of deltas a node carries. It is what
// completeness is judged against: a binary that declares epoch 6 must carry D2, DF and
// DA in full, and adding one of them later under the same epoch would be manifest drift.
func guardEditionDeltasOfEpoch(epoch int64) []guardEditionDelta {
	membership, ok := guardEditionMembershipForEpoch(epoch)
	if !ok {
		return nil
	}
	out := make([]guardEditionDelta, 0, len(guardEditionDeltaOrder))
	for _, d := range guardEditionDeltaOrder {
		if membership.has(d) {
			out = append(out, d)
		}
	}
	return out
}

// guardEditionNode is one node of the graph: the manifest, its delta membership and the
// fingerprint of the base census it shares with every other node of the same profile.
type guardEditionNode struct {
	Epoch      int64
	Membership guardEditionMembership
	Manifest   guardManifest
	BaseSHA256 [32]byte
}

// guardEditionGraph is the ancestor sub-DAG of one current edition.
//
// ONLY ANCESTORS, and only by SUBTRACTION. A graph that also constructed descendants
// would have to INVENT relations the closed registry never declared — a core-only build
// has no sessions_message_ack to put in an E6 node — and a manifest naming relations
// nothing installs is exactly the drift this whole subsystem refuses. Every node here is
// the current census minus a subset of its own deltas.
type guardEditionGraph struct {
	// Base is the sorted base census B: every append-only relation left after
	// subtracting D2, DF, DK and DA.
	Base []string
	// BaseSHA256 is BID. It is an in-memory comparison aid — "are these two nodes the
	// same product profile?" — and never replaces a stored manifest digest.
	BaseSHA256 [32]byte
	Current    int64
	nodes      map[int64]guardEditionNode
	order      []int64
}

// guardBaseCensusDigest fingerprints B through the same canonical encoder every other
// digest uses, under its own domain.
func guardBaseCensusDigest(base []guardSpec) ([32]byte, error) {
	w := newCanonWriter(canonDomainBaseCensus, guardManifestFormat)
	w.list(len(base))
	for _, s := range base {
		s.Key.canon(w)
		w.bytes32(s.SpecSHA256)
	}
	return w.sum()
}

// buildGuardEditionGraph derives the graph from a CLOSED census.
//
// It is the constructor R2 §4 names. Nodes are built only through
// buildGuardManifestAtEpoch and exact set subtraction, so a node's bytes cannot differ
// from what the ordinary constructor would produce for the same tables.
func buildGuardEditionGraph(tables []string) (guardEditionGraph, error) {
	current, err := buildGuardManifest(tables)
	if err != nil {
		return guardEditionGraph{}, err
	}
	return guardEditionGraphFor(current)
}

// guardEditionGraphFor derives the ancestor graph of an already-built manifest.
//
// Taking the manifest rather than the raw table list is what lets a HISTORICAL edition
// have its own graph: the predecessor manifests this file constructs are ordinary
// manifests, and their own ancestors are derived the same way.
func guardEditionGraphFor(current guardManifest) (guardEditionGraph, error) {
	if current.Format != guardManifestFormat {
		return guardEditionGraph{}, fmt.Errorf(
			"sqlstore: guard edition graph needs manifest format %d, got %d",
			guardManifestFormat, current.Format)
	}
	membership, ok := guardEditionMembershipForEpoch(current.CodeEpoch)
	if !ok {
		return guardEditionGraph{}, fmt.Errorf(
			"sqlstore: guard epoch %d is not a compiled edition of this binary", current.CodeEpoch)
	}
	base := make([]string, 0, len(current.Specs))
	baseSpecs := make([]guardSpec, 0, len(current.Specs))
	for _, spec := range current.Specs {
		delta, isDelta := guardEditionDeltaOf(spec.Key.Relation)
		if isDelta && membership.has(delta) {
			continue
		}
		if isDelta {
			// A relation of a delta this edition does NOT declare is not base census:
			// it is a partial delta, and admitting it into B would make two different
			// products compare equal.
			return guardEditionGraph{}, fmt.Errorf(
				"sqlstore: guard edition %d declares %s, which belongs to the %s delta this edition does not carry",
				current.CodeEpoch, spec.Key.Relation, delta)
		}
		base = append(base, spec.Key.Relation)
		baseSpecs = append(baseSpecs, spec)
	}
	sort.Strings(base)
	// The base specs come out of the current manifest already sorted by key, and their
	// per-entry digests do not depend on the epoch, so this is byte-for-byte what the E1
	// node's own census hashes to. Deriving it here rather than from that node is what
	// lets an INCOMPLETE build still have a base fingerprint.
	bid, err := guardBaseCensusDigest(baseSpecs)
	if err != nil {
		return guardEditionGraph{}, err
	}

	g := guardEditionGraph{Base: base, BaseSHA256: bid, Current: current.CodeEpoch, nodes: map[int64]guardEditionNode{}}

	// AN INCOMPLETE BUILD HAS NO ANCESTORS, and saying so here rather than refusing is
	// deliberate. Its own node still exists — callers that only enumerate this edition's
	// activations must keep working — but it has no edges, so requireCompleteGuardEdition
	// refuses it in the one place that refusal belongs, with the missing relations named.
	// Constructing ancestors from a census that is missing part of its own delta would
	// mean inventing manifests for editions this build cannot represent.
	if missing := missingGuardTablesForEpoch(current, current.CodeEpoch); len(missing) != 0 {
		g.nodes[current.CodeEpoch] = guardEditionNode{
			Epoch: current.CodeEpoch, Membership: membership, Manifest: current, BaseSHA256: bid,
		}
		g.order = []int64{current.CodeEpoch}
		return g, nil
	}
	for _, candidate := range guardEditionNodes {
		if candidate.Membership&^membership != 0 {
			// Not a subset of this edition's membership: not an ancestor, and its
			// relations are not in this census.
			continue
		}
		manifest := current
		if candidate.Epoch != current.CodeEpoch {
			tables := make([]string, 0, len(current.Specs))
			tables = append(tables, base...)
			for _, d := range guardEditionDeltaOrder {
				if candidate.Membership.has(d) {
					tables = append(tables, guardEditionDeltaTables(d)...)
				}
			}
			built, berr := buildGuardManifestAtEpoch(tables, candidate.Epoch)
			if berr != nil {
				return guardEditionGraph{}, berr
			}
			manifest = built
		}
		g.nodes[candidate.Epoch] = guardEditionNode{
			Epoch: candidate.Epoch, Membership: candidate.Membership, Manifest: manifest,
		}
		g.order = append(g.order, candidate.Epoch)
	}
	sort.Slice(g.order, func(i, j int) bool { return g.order[i] < g.order[j] })
	for epoch, node := range g.nodes {
		node.BaseSHA256 = bid
		g.nodes[epoch] = node
	}
	// The E1 node IS the base census by construction, so its own specs must hash to the
	// fingerprint derived above. Checking it turns "these two derivations agree" from an
	// argument into a measurement.
	if root, present := g.nodes[1]; present {
		rootDigest, rerr := guardBaseCensusDigest(root.Manifest.Specs)
		if rerr != nil {
			return guardEditionGraph{}, rerr
		}
		if rootDigest != bid {
			return guardEditionGraph{}, fmt.Errorf(
				"sqlstore: the base census subtracted from edition %d hashes to %s while its edition-1 node hashes to %s",
				current.CodeEpoch, hexDigest(bid), hexDigest(rootDigest))
		}
	}
	return g, nil
}

func (g guardEditionGraph) node(epoch int64) (guardEditionNode, bool) {
	n, ok := g.nodes[epoch]
	return n, ok
}

func (g guardEditionGraph) currentNode() (guardEditionNode, bool) { return g.node(g.Current) }

// epochsDescending lists every node of the graph, newest first. It is the deterministic
// order every "try each compiled edition" walk uses.
func (g guardEditionGraph) epochsDescending() []int64 {
	out := append([]int64(nil), g.order...)
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

// parentEpochs returns every compiled parent of a node, ascending.
//
// A parent is this node's membership with exactly one delta removed, when that smaller
// membership is itself a compiled node. That derivation is what makes the edge set
// closed: nothing here can invent an edge whose additions are not exactly one named
// delta, and nothing can produce an edge with empty additions.
func (g guardEditionGraph) parentEpochs(epoch int64) []int64 {
	membership, ok := guardEditionMembershipForEpoch(epoch)
	if !ok {
		return nil
	}
	var out []int64
	for _, d := range guardEditionDeltaOrder {
		if !membership.has(d) {
			continue
		}
		parent, valid := guardEditionEpochForMembership(membership.without(d))
		if !valid {
			continue
		}
		if _, present := g.nodes[parent]; !present {
			continue
		}
		out = append(out, parent)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// edgeDelta names the single delta an edge adds, and refuses any pair that is not one of
// the compiled edges.
func guardEditionEdgeDelta(from, to int64) (guardEditionDelta, error) {
	fromM, okFrom := guardEditionMembershipForEpoch(from)
	toM, okTo := guardEditionMembershipForEpoch(to)
	if !okFrom || !okTo {
		return 0, fmt.Errorf("%w: %d -> %d is not a pair of compiled editions", ErrGuardManifestNoEdge, from, to)
	}
	added := toM &^ fromM
	if toM&fromM != fromM {
		return 0, fmt.Errorf("%w: edition %d drops a delta edition %d carries", ErrGuardManifestNoEdge, to, from)
	}
	for _, d := range guardEditionDeltaOrder {
		if added == guardEditionMembership(d) {
			return d, nil
		}
	}
	return 0, fmt.Errorf("%w: %d -> %d does not add exactly one compiled delta", ErrGuardManifestNoEdge, from, to)
}

// edge builds the compiled edge between two nodes of this graph.
//
// Every property the chain enforced is enforced here, per edge: the additions are the
// complete named delta, they are nonempty, removals are empty, and every carried spec is
// byte-identical. Rebuilding both ends from one constructor should make the last check
// tautological; it stays because a future epoch-specific policy change must fail at
// construction rather than silently authorize a changed old entry.
func (g guardEditionGraph) edge(from, to int64) (guardManifestEdge, error) {
	fromNode, okFrom := g.node(from)
	toNode, okTo := g.node(to)
	if !okFrom || !okTo {
		return guardManifestEdge{}, fmt.Errorf("%w: edition %d -> %d is not in this graph",
			ErrGuardManifestNoEdge, from, to)
	}
	delta, err := guardEditionEdgeDelta(from, to)
	if err != nil {
		return guardManifestEdge{}, err
	}
	want := map[string]bool{}
	for _, table := range guardEditionDeltaTables(delta) {
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

// guardEditionLineage is ONE complete candidate history: a start edition and the exact
// ordered edges crossed from it.
//
// The order is load-bearing rather than descriptive. Two different paths between the
// same endpoints activate the same relations in a DIFFERENT sequence — E2 -> E5 -> E6
// records the access-evidence delta before the communication delta, E2 -> E3 -> E6
// records it after — and the inventory stream preserves that sequence. That is what lets
// exactly one candidate match a database, and it is why a lineage is a path and not a
// pair.
type guardEditionLineage struct {
	Nodes []guardEditionNode
	Edges []guardManifestEdge
}

func (l guardEditionLineage) start() guardEditionNode { return l.Nodes[0] }
func (l guardEditionLineage) end() guardEditionNode   { return l.Nodes[len(l.Nodes)-1] }

// guardEditionPath is a lineage's identity, as the epochs it crossed.
//
// It is the whole path rather than only the start epoch, and that changed with the DAG:
// with one parent per node the start determined the path, and with two it does not. A
// receipt history and an inventory that agree on the start but disagree on the ORDER of
// the deltas are a cross-product no transaction wrote, and comparing full paths is what
// keeps refusing it.
type guardEditionPath string

func guardEditionPathOf(nodes []guardEditionNode) guardEditionPath {
	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		parts = append(parts, strconv.FormatInt(n.Epoch, 10))
	}
	return guardEditionPath(strings.Join(parts, ">"))
}

func (l guardEditionLineage) path() guardEditionPath { return guardEditionPathOf(l.Nodes) }

func (l guardEditionLineage) extend(node guardEditionNode, edge guardManifestEdge) guardEditionLineage {
	out := guardEditionLineage{
		Nodes: append(append([]guardEditionNode(nil), l.Nodes...), node),
		Edges: append(append([]guardManifestEdge(nil), l.Edges...), edge),
	}
	return out
}

// lineagesEndingAt enumerates every compiled candidate history whose last node is epoch,
// including the trivial one-node lineage (a database bootstrapped directly at that
// edition and never upgraded).
//
// The order is deterministic and is part of the selector's contract: shortest first —
// the trivial lineage, then one edge, then two — and within a length, by path. Callers
// that accept the first match therefore prefer the history with the FEWEST transitions,
// and the selector that requires uniqueness reports every collision rather than
// depending on this order.
func (g guardEditionGraph) lineagesEndingAt(epoch int64) ([]guardEditionLineage, error) {
	node, ok := g.node(epoch)
	if !ok {
		return nil, fmt.Errorf("%w: edition %d is not in this graph", ErrGuardManifestNoEdge, epoch)
	}
	out := []guardEditionLineage{{Nodes: []guardEditionNode{node}}}
	for _, parent := range g.parentEpochs(epoch) {
		edge, err := g.edge(parent, epoch)
		if err != nil {
			return nil, err
		}
		prefixes, err := g.lineagesEndingAt(parent)
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

// predecessorEdges returns every compiled parent edge of a node, which is the DAG's
// replacement for "the" predecessor. It never derives an edge from database-supplied
// additions: both ends are rebuilt from the closed census.
func (g guardEditionGraph) predecessorEdges(epoch int64) ([]guardManifestEdge, error) {
	var out []guardManifestEdge
	for _, parent := range g.parentEpochs(epoch) {
		edge, err := g.edge(parent, epoch)
		if err != nil {
			return nil, err
		}
		out = append(out, edge)
	}
	return out, nil
}

// guardManifestEditionEdges is the manifest-level entry point: every authorized forward
// transition INTO this edition.
//
// It replaces guardManifestEditionEdge, whose signature could only ever answer with one
// parent. Callers that genuinely need a unique answer say so by asking for it (see
// guardManifestSoleEditionEdge) rather than by taking the first of a list.
func guardManifestEditionEdges(current guardManifest) ([]guardManifestEdge, error) {
	if current.Format != guardManifestFormat || current.CodeEpoch < 2 || current.CodeEpoch > guardCodeEpoch {
		return nil, nil
	}
	if _, ok := guardEditionMembershipForEpoch(current.CodeEpoch); !ok {
		return nil, nil
	}
	if missing := missingGuardTablesForEpoch(current, current.CodeEpoch); len(missing) != 0 {
		// An incomplete build has no edge at all. In particular, it must not be able
		// to persist an epoch with an empty delta and later call the real additions
		// same-epoch drift.
		return nil, nil
	}
	g, err := guardEditionGraphFor(current)
	if err != nil {
		return nil, err
	}
	return g.predecessorEdges(current.CodeEpoch)
}

// guardManifestSoleEditionEdge is the compatibility shape for the places where exactly
// one parent is the only sound answer — the E1 -> E2 bridge core v7 runs, and the
// completeness check that asks whether an edition has any compiled predecessor at all.
//
// It REFUSES ambiguity instead of picking, because "the first parent" is not a fact
// about the database.
func guardManifestSoleEditionEdge(current guardManifest) (guardManifestEdge, bool, error) {
	edges, err := guardManifestEditionEdges(current)
	if err != nil {
		return guardManifestEdge{}, false, err
	}
	switch len(edges) {
	case 0:
		return guardManifestEdge{}, false, nil
	case 1:
		return edges[0], true, nil
	default:
		return guardManifestEdge{}, false, fmt.Errorf(
			"%w: edition %d has %d compiled predecessors, so no single one is 'the' predecessor",
			ErrGuardManifestNoEdge, current.CodeEpoch, len(edges))
	}
}
