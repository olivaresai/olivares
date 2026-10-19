// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
	"strconv"
	"strings"
)

// Post-module admission extends the closed graph with a completed Community
// predecessor. Historical migration callbacks keep their original declarations
// and transitive source hashes; canonical encoders and SQL writers are shared.
func guardAdmissionTransitionInTx(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	current guardManifest,
	edge guardManifestEdge,
	history guardEditionHistory,
	plans []guardUnitPlan,
	openCurrent bool,
) error {
	want := guardEditionHistoryPredecessor
	if edge.From.CodeEpoch >= 2 {
		want = guardEditionHistoryPredecessorV7
	}
	if history.Kind != want {
		return fmt.Errorf("%w: guard edition %d transition requires predecessor history %q, got %q",
			ErrGuardManifestNoEdge, current.CodeEpoch, want, history.Kind)
	}
	if edge.To.CodeEpoch != current.CodeEpoch || edge.To.CodeSHA256 != current.CodeSHA256 {
		return fmt.Errorf("%w: the supplied edge targets edition %d/%s, not the %d/%s being transitioned",
			ErrGuardManifestNoEdge, edge.To.CodeEpoch, hexDigest(edge.To.CodeSHA256),
			current.CodeEpoch, hexDigest(current.CodeSHA256))
	}
	if history.ParentEpoch != guardAdmissionEdgeSource(edge) {
		return fmt.Errorf("%w: the durable history stands on edition %d and the supplied edge starts at %d",
			ErrGuardManifestNoEdge, history.ParentEpoch, guardAdmissionEdgeSource(edge))
	}
	revision, retained, err := guardAdmissionVerifyInventoryChain(ctx, tx, dia, edge.From)
	if err != nil {
		return err
	}
	if revision != history.Revision || retained != history.Retained {
		return fmt.Errorf("%w: predecessor inventory changed between classification and transition",
			ErrGuardManifestNoEdge)
	}
	target, err := guardBootstrapRollout(current, revision, retained)
	if err != nil {
		return err
	}
	targetReceipts, err := guardRolloutReceipts(ctx, tx, dia, target.RolloutID)
	if err != nil {
		return fmt.Errorf("%w: target epoch receipt stream does not verify before transition: %w",
			ErrGuardManifestNoEdge, err)
	}
	if len(targetReceipts) != 0 {
		return fmt.Errorf("%w: target epoch rollout %s already has %d receipts before its edition transition",
			ErrGuardManifestNoEdge, target.RolloutID, len(targetReceipts))
	}
	if err := appendGuardEditionTransitionRows(ctx, tx, dia, current, edge, revision, retained,
		history.TerminalReceiptID, plans, openCurrent); err != nil {
		return err
	}
	post, err := guardAdmissionVerifyHistory(ctx, tx, dia, current)
	if err != nil {
		return fmt.Errorf("sqlstore: verify guard edition transition before v7 commit: %w", err)
	}
	if post.Kind != guardEditionHistoryTransitioned {
		return fmt.Errorf("%w: guard edition transition produced history %q, want %q",
			ErrGuardManifestNoEdge, post.Kind, guardEditionHistoryTransitioned)
	}
	return nil
}

func guardAdmissionVerifyInventoryChain(ctx context.Context, q dialect.Querier, dia dialect.Dialect, manifest guardManifest) (int64, [32]byte, error) {
	return verifyInventoryChainMode(ctx, q, dia, manifest, func(activations map[guardKey]inventoryEvent) error {
		return guardAdmissionVerifyActivations(manifest, activations)
	})
}

func guardAdmissionVerifyActivations(manifest guardManifest, activations map[guardKey]inventoryEvent) error {
	lineages, err := guardAdmissionLineagesFor(manifest)
	if err != nil {
		return err
	}
	var differences []string
	var causes []error
	for _, lineage := range lineages {
		expected := guardAdmissionActivations(lineage)
		if xerr := verifyGuardActivationExpectations(expected, activations); xerr == nil {
			return nil
		} else {
			differences = append(differences, fmt.Sprintf("path %s: %v", lineage.path(), xerr))
			causes = append(causes, xerr)
		}
	}
	summary := fmt.Errorf("%w: the inventory is not any complete compiled lineage ending at epoch %d (%s)",
		ErrGuardInventoryUnsupported, manifest.CodeEpoch, strings.Join(differences, "; "))
	if len(causes) == 0 {
		return summary
	}
	return errors.Join(append([]error{summary}, causes...)...)
}

func guardAdmissionLineagesFor(manifest guardManifest) ([]guardEditionLineage, error) {
	graph, err := guardAdmissionGraphFor(manifest)
	if err != nil {
		return nil, err
	}
	return guardAdmissionLineagesEndingAt(graph, manifest.CodeEpoch)
}

func guardAdmissionActivations(lineage guardEditionLineage) []guardActivationExpectation {
	expected := guardActivationExpectationsForLineage(lineage)
	start := lineage.start()
	for i := range start.Manifest.Specs {
		expected[i].Epoch = start.Manifest.CodeEpoch
	}
	return expected
}

func guardAdmissionLatestRecordedEdition(ctx context.Context, q dialect.Querier, dia dialect.Dialect) (recordedEdition, error) {
	rows, err := q.QueryContext(ctx, dia.Rebind(
		"SELECT opened.rollout_id, opened.manifest_format, opened.code_epoch, opened.code_sha256 FROM "+guardOnly(dia)+guardGateEventsTable+" AS opened"+
			" WHERE opened.kind = ? AND NOT EXISTS (SELECT 1 FROM "+guardOnly(dia)+guardReceiptsTable+" AS successor"+
			" JOIN "+guardOnly(dia)+guardReceiptsTable+" AS predecessor ON successor.predecessor_receipt_id = predecessor.receipt_id"+
			" JOIN "+guardOnly(dia)+guardGateEventsTable+" AS next_opened ON next_opened.rollout_id = successor.rollout_id"+
			" WHERE predecessor.rollout_id = opened.rollout_id AND successor.rollout_id <> opened.rollout_id"+
			" AND successor.receipt_kind = ? AND successor.epoch >= opened.code_epoch AND next_opened.kind = ?)"+
			" ORDER BY opened.code_epoch DESC, opened.retained_revision DESC, opened.rollout_id DESC LIMIT 1"),
		string(gateEventPendingOpened), guardReceiptKindBootstrap, string(gateEventPendingOpened))
	if err != nil {
		return recordedEdition{}, fmt.Errorf("sqlstore: read the recorded guard edition: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return recordedEdition{}, rows.Err()
	}
	var out recordedEdition
	var raw []byte
	if err := rows.Scan(&out.RolloutID, &out.Format, &out.CodeEpoch, &raw); err != nil {
		return recordedEdition{}, fmt.Errorf("sqlstore: read the recorded guard edition: %w", err)
	}
	if out.CodeSHA256, err = scanDigest(raw, "a recorded guard edition digest"); err != nil {
		return recordedEdition{}, err
	}
	out.Found = true
	return out, rows.Err()
}

func guardAdmissionClassifyInventory(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
	current guardManifest,
	path guardEditionPath,
) (guardEditionInventorySelection, error) {
	trivial := guardEditionPathOf([]guardEditionNode{{Epoch: current.CodeEpoch}})
	var mismatchedPath guardEditionPath
	if revision, retained, err := verifyInventoryChainExact(ctx, q, dia, current); err == nil {
		if path == trivial {
			return guardEditionInventorySelection{
				State: guardEditionInventoryCurrent, Path: trivial, Revision: revision, Retained: retained,
			}, nil
		}
		mismatchedPath = trivial
	}
	graph, err := guardAdmissionGraphFor(current)
	if err != nil {
		return guardEditionInventorySelection{}, err
	}
	parents := guardAdmissionParentEpochs(graph, current.CodeEpoch)
	if len(parents) == 0 {
		_, _, currentErr := verifyInventoryChainExact(ctx, q, dia, current)
		return guardEditionInventorySelection{}, currentErr
	}

	type candidate struct {
		selection guardEditionInventorySelection
		lineage   guardEditionLineage
	}
	var predecessorErrs, mixedErrs []string
	var causes []error
	var matches []candidate

	for _, parent := range parents {
		lineages, lerr := guardAdmissionLineagesEndingAt(graph, parent)
		if lerr != nil {
			return guardEditionInventorySelection{}, lerr
		}
		for _, lineage := range lineages {
			revision, retained, xerr := verifyInventoryChainExpectations(
				ctx, q, dia, current, guardAdmissionActivations(lineage))
			if xerr == nil {
				if lineage.path() != path {
					mismatchedPath = lineage.path()
					continue
				}
				matches = append(matches, candidate{guardEditionInventorySelection{
					State: guardEditionInventoryPredecessor, Path: lineage.path(),
					ParentEpoch: parent, Revision: revision, Retained: retained,
				}, lineage})
				continue
			}
			predecessorErrs = append(predecessorErrs, fmt.Sprintf("path %s: %v", lineage.path(), xerr))
			causes = append(causes, xerr)
		}
	}
	lineages, lerr := guardAdmissionLineagesEndingAt(graph, current.CodeEpoch)
	if lerr != nil {
		return guardEditionInventorySelection{}, lerr
	}
	for _, lineage := range lineages {
		if len(lineage.Edges) == 0 {
			continue // the trivial lineage is the exact-current case tried above
		}
		revision, retained, xerr := verifyInventoryChainExpectations(
			ctx, q, dia, current, guardAdmissionActivations(lineage))
		if xerr == nil {
			if lineage.path() != path {
				mismatchedPath = lineage.path()
				continue
			}
			matches = append(matches, candidate{guardEditionInventorySelection{
				State: guardEditionInventoryMixed, Path: lineage.path(),
				ParentEpoch: lineage.Nodes[len(lineage.Nodes)-2].Epoch, Revision: revision, Retained: retained,
			}, lineage})
			continue
		}
		mixedErrs = append(mixedErrs, fmt.Sprintf("path %s: %v", lineage.path(), xerr))
		causes = append(causes, xerr)
	}

	switch len(matches) {
	case 1:
		return matches[0].selection, nil
	case 0:
		if mismatchedPath != "" {
			return guardEditionInventorySelection{}, fmt.Errorf(
				"%w: receipts select path %s but the inventory matches path %s",
				ErrGuardManifestNoEdge, path, mismatchedPath)
		}
	default:
		paths := make([]string, 0, len(matches))
		for _, m := range matches {
			paths = append(paths, fmt.Sprintf("%s/%s", m.selection.State, m.lineage.path()))
		}
		return guardEditionInventorySelection{}, fmt.Errorf(
			"%w: the inventory matches %d compiled candidate histories (%s); an ambiguous history is not an authorization",
			ErrGuardManifestNoEdge, len(matches), strings.Join(paths, ", "))
	}

	_, _, currentErr := verifyInventoryChainExact(ctx, q, dia, current)
	summary := fmt.Errorf("%w: inventory is neither current, a complete predecessor lineage, nor its authorized carry-forward (current: %v; predecessor: %s; carry-forward: %s)",
		ErrGuardInventoryUnsupported, currentErr,
		strings.Join(predecessorErrs, "; "), strings.Join(mixedErrs, "; "))
	if currentErr != nil {
		causes = append(causes, currentErr)
	}
	if len(causes) == 0 {
		return guardEditionInventorySelection{}, summary
	}
	return guardEditionInventorySelection{}, errors.Join(append([]error{summary}, causes...)...)
}

func guardAdmissionClassifyGate(
	ctx context.Context,
	q guardEditionQuerier,
	dia dialect.Dialect,
	current guardManifest,
	graph guardEditionGraph,
	revision int64,
	retained [32]byte,
) (guardEditionGateSelection, error) {
	recorded, err := guardAdmissionLatestRecordedEdition(ctx, q, dia)
	if err != nil {
		return guardEditionGateSelection{}, err
	}
	if !recorded.Found {
		return guardEditionGateSelection{State: guardEditionGateAbsent}, nil
	}
	var (
		state       guardEditionGateState
		edition     guardManifest
		parentEpoch int64
	)
	switch {
	case recorded.Format == current.Format && recorded.CodeEpoch == current.CodeEpoch &&
		recorded.CodeSHA256 == current.CodeSHA256:
		state, edition = guardEditionGateCurrent, current
	default:
		edges, eerr := guardAdmissionPredecessorEdges(graph, current.CodeEpoch)
		if eerr != nil {
			return guardEditionGateSelection{}, eerr
		}
		for _, edge := range edges {
			if edge.authorizes(recorded.Format, recorded.CodeEpoch, recorded.CodeSHA256) {
				state, edition, parentEpoch = guardEditionGatePredecessor, edge.From, guardAdmissionEdgeSource(edge)
				break
			}
		}
		if state == "" {
			return guardEditionGateSelection{}, classifyRecordedEdition(current, recorded)
		}
	}
	rollout, err := guardBootstrapRollout(edition, revision, retained)
	if err != nil {
		return guardEditionGateSelection{}, err
	}
	if recorded.RolloutID != rollout.RolloutID {
		return guardEditionGateSelection{}, fmt.Errorf("%w: latest %s edition is recorded under rollout %s, want %s for retained pair %d/%s",
			ErrGuardGateIllegalTransition, state, recorded.RolloutID, rollout.RolloutID,
			revision, hexDigest(retained))
	}
	gate, err := verifyGuardGateTuple(ctx, q, dia, rollout)
	if err != nil {
		return guardEditionGateSelection{}, err
	}
	if state == guardEditionGatePredecessor {
		if err := verifyGuardHistoricalCheckpoint(ctx, q, dia, edition, rollout, gate); err != nil {
			return guardEditionGateSelection{}, err
		}
	} else if err := verifyGuardCheckpoint(ctx, q, dia, rollout.RolloutID, gate); err != nil {
		return guardEditionGateSelection{}, err
	}
	return guardEditionGateSelection{State: state, Gate: gate, ParentEpoch: parentEpoch}, nil
}

func guardAdmissionVerifyHistory(
	ctx context.Context,
	q guardEditionQuerier,
	dia dialect.Dialect,
	current guardManifest,
) (guardEditionHistory, error) {
	if err := requireCompleteGuardCurrentEdition(current); err != nil {
		return guardEditionHistory{}, err
	}
	graph, err := guardAdmissionGraphFor(current)
	if err != nil {
		return guardEditionHistory{}, err
	}
	if _, commercial := graph.nodes[-current.CodeEpoch]; !commercial {
		return verifyGuardBootEditionHistory(ctx, q, dia, current)
	}
	if err := verifyGuardControlPlaneShape(ctx, q, dia); err != nil {
		return guardEditionHistory{}, err
	}
	receiptVariant, _, err := guardAdmissionClassifyReceipts(ctx, q, dia, current)
	if err != nil {
		return guardEditionHistory{}, err
	}
	if err := verifyGuardControlPlaneCatalog(ctx, q, dia, current.Format); err != nil {
		return guardEditionHistory{}, err
	}
	inventory, err := guardAdmissionClassifyInventory(ctx, q, dia, current, receiptVariant.Path)
	if err != nil {
		return guardEditionHistory{}, err
	}
	if len(guardAdmissionParentEpochs(graph, current.CodeEpoch)) == 0 {
		return guardEditionHistory{}, fmt.Errorf("%w: epoch %d has no compiled edition edge",
			ErrGuardManifestNoEdge, current.CodeEpoch)
	}
	gateSelection, err := guardAdmissionClassifyGate(ctx, q, dia, current, graph, inventory.Revision, inventory.Retained)
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
		if err := guardAdmissionVerifyHistoricalGates(ctx, q, dia, graph, parentEpoch,
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

func guardAdmissionVerifyHistoricalGates(
	ctx context.Context,
	q guardEditionQuerier,
	dia dialect.Dialect,
	graph guardEditionGraph,
	standingOn int64,
	path guardEditionPath,
	revision int64,
	retained [32]byte,
) error {
	lineages, err := guardAdmissionLineagesEndingAt(graph, standingOn)
	if err != nil {
		return err
	}
	var selected *guardEditionLineage
	for i := range lineages {
		if lineages[i].path() == path {
			selected = &lineages[i]
			break
		}
	}
	if selected == nil {
		currentLineages, cerr := guardAdmissionLineagesEndingAt(graph, graph.Current)
		if cerr != nil {
			return cerr
		}
		for i := range currentLineages {
			if currentLineages[i].path() != path || len(currentLineages[i].Edges) == 0 {
				continue
			}
			prefix := guardEditionLineage{
				Nodes: currentLineages[i].Nodes[:len(currentLineages[i].Nodes)-1],
				Edges: currentLineages[i].Edges[:len(currentLineages[i].Edges)-1],
			}
			if prefix.end().Epoch == standingOn {
				selected = &prefix
			}
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("%w: no compiled lineage %s ends at edition %d",
			ErrGuardManifestNoEdge, path, standingOn)
	}
	for i := len(selected.Nodes) - 1; i >= 0; i-- {
		manifest := selected.Nodes[i].Manifest
		rollout, rerr := guardBootstrapRollout(manifest, revision, retained)
		if rerr != nil {
			return rerr
		}
		gate, gerr := verifyGuardGateTuple(ctx, q, dia, rollout)
		if gerr != nil {
			return fmt.Errorf("%w: historical epoch-%d gate is absent or divergent: %v",
				ErrGuardManifestNoEdge, manifest.CodeEpoch, gerr)
		}
		if err := verifyGuardHistoricalCheckpoint(ctx, q, dia, manifest, rollout, gate); err != nil {
			return err
		}
	}
	return nil
}

func guardAdmissionOpenOrVerifyGuardRollout(
	ctx context.Context,
	mdb dialect.Execer,
	dia dialect.Dialect,
	m guardManifest,
	history guardEditionHistory,
	plans []guardUnitPlan,
	mayOpen bool,
) (guardRolloutContext, gateProjection, bool, error) {
	if err := requireCompleteGuardCurrentEdition(m); err != nil {
		return guardRolloutContext{}, gateProjection{}, false, err
	}
	if !guardEditionHistoryCompletesV7(history.Kind) {
		return guardRolloutContext{}, gateProjection{}, false, fmt.Errorf(
			"%w: epoch-%d rollout opening requires a completed v7 history; history is %q",
			ErrGuardManifestNoEdge, m.CodeEpoch, history.Kind)
	}
	rolloutID, err := guardRolloutID(m.Format, m.CodeEpoch, m.CodeSHA256, history.Revision, history.Retained)
	if err != nil {
		return guardRolloutContext{}, gateProjection{}, false, err
	}
	if history.GateState == guardEditionGateCurrent {
		proj := history.Gate
		if !proj.Found || proj.RolloutID != rolloutID {
			return guardRolloutContext{}, gateProjection{}, false, fmt.Errorf(
				"%w: correlated current gate names rollout %q, want %q",
				ErrGuardGateIllegalTransition, proj.RolloutID, rolloutID)
		}
		recomputed, rerr := guardRolloutID(proj.Format, proj.CodeEpoch, proj.CodeSHA256,
			proj.RetainedRevision, proj.RetainedSHA256)
		if rerr != nil {
			return guardRolloutContext{}, gateProjection{}, false, rerr
		}
		if recomputed != rolloutID {
			return guardRolloutContext{}, gateProjection{}, false, fmt.Errorf("%w: rollout %s holds an opening event whose own tuple (format %d, epoch %d, code %s, retained %d/%s) identifies rollout %s",
				ErrGuardGateIllegalTransition, rolloutID, proj.Format, proj.CodeEpoch,
				hexDigest(proj.CodeSHA256), proj.RetainedRevision, hexDigest(proj.RetainedSHA256), recomputed)
		}
		return guardRolloutContext{
			RolloutID: rolloutID, Format: proj.Format, CodeEpoch: proj.CodeEpoch,
			CodeSHA256: proj.CodeSHA256, RetainedRevision: proj.RetainedRevision,
			RetainedSHA256: proj.RetainedSHA256,
		}, proj, false, nil
	}
	openable := history.Kind == guardEditionHistoryCurrentCompleted ||
		history.Kind == guardEditionHistoryDirectCompleted ||
		history.Kind == guardEditionHistoryCurrentV9Completed ||
		history.Kind == guardEditionHistoryDirectV9Completed
	if history.GateState != guardEditionGateAbsent || !openable {
		return guardRolloutContext{}, gateProjection{}, false, fmt.Errorf(
			"%w: history %q with gate %q cannot open a rollout outside the v7 edition transaction",
			ErrGuardManifestNoEdge, history.Kind, history.GateState)
	}

	if !mayOpen {
		return guardRolloutContext{}, gateProjection{}, false, fmt.Errorf(
			"sqlstore: refusing to open a guard rollout for edition %d/%d: this boot cannot build a unit for every target it declares, and an enumeration that omits one would authorize every later boot to skip it",
			m.Format, m.CodeEpoch)
	}

	tx, err := mdb.BeginTx(ctx, nil)
	if err != nil {
		return guardRolloutContext{}, gateProjection{}, false, fmt.Errorf("sqlstore: open the guard rollout: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	locked, err := guardAdmissionVerifyHistory(ctx, tx, dia, m)
	if err != nil {
		return guardRolloutContext{}, gateProjection{}, false, err
	}
	if locked.Kind != history.Kind || locked.GateState != guardEditionGateAbsent ||
		locked.Revision != history.Revision || locked.Retained != history.Retained {
		return guardRolloutContext{}, gateProjection{}, false, fmt.Errorf(
			"%w: current guard history changed before pending-opened (history=%s gate=%s)",
			ErrGuardManifestNoEdge, locked.Kind, locked.GateState)
	}
	if _, err := appendGateEvent(ctx, tx, dia, gateEvent{
		RolloutID:        rolloutID,
		Kind:             gateEventPendingOpened,
		Format:           m.Format,
		CodeEpoch:        m.CodeEpoch,
		CodeSHA256:       m.CodeSHA256,
		RetainedRevision: history.Revision,
		RetainedSHA256:   history.Retained,
		Phase:            gatePhasePending,
		Condition:        gateConditionClean,
		ExpectedUnits:    guardPlanUnitIDs(plans),
	}); err != nil {
		return guardRolloutContext{}, gateProjection{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return guardRolloutContext{}, gateProjection{}, false, fmt.Errorf("sqlstore: open the guard rollout: commit: %w", err)
	}

	proj, err := foldGateEvents(ctx, mdb, dia, rolloutID)
	if err != nil {
		return guardRolloutContext{}, gateProjection{}, false, err
	}
	if !proj.Found {
		return guardRolloutContext{}, gateProjection{}, false, fmt.Errorf("sqlstore: the guard rollout %s was opened but does not read back", rolloutID)
	}
	return guardRolloutContext{
		RolloutID: rolloutID, Format: proj.Format, CodeEpoch: proj.CodeEpoch,
		CodeSHA256: proj.CodeSHA256, RetainedRevision: proj.RetainedRevision,
		RetainedSHA256: proj.RetainedSHA256,
	}, proj, true, nil
}

func guardAdmissionVerifyControlPlane(ctx context.Context, q guardEditionQuerier, dia dialect.Dialect, m guardManifest) error {
	if err := verifyGuardControlPlaneShape(ctx, q, dia); err != nil {
		return err
	}
	if _, _, err := guardAdmissionClassifyReceipts(ctx, q, dia, m); err != nil {
		return err
	}
	return verifyGuardControlPlaneCatalog(ctx, q, dia, m.Format)
}

func guardAdmissionBootstrapVariants(graph guardEditionGraph, epoch int64) ([]guardBootstrapReceiptVariant, error) {
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
	for _, parent := range guardAdmissionParentEpochs(graph, epoch) {
		edge, eerr := guardAdmissionEdge(graph, parent, epoch)
		if eerr != nil {
			return nil, eerr
		}
		sources, serr := guardAdmissionBootstrapVariants(graph, parent)
		if serr != nil {
			return nil, serr
		}
		parentNode, _ := graph.node(parent)
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
			_ = parentNode
		}
	}
	return variants, nil
}

func guardAdmissionClassifyReceipts(
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
	graph, err := guardAdmissionGraphFor(current)
	if err != nil {
		return guardBootstrapReceiptVariant{}, nil, err
	}
	variants, err := guardAdmissionBootstrapVariants(graph, current.CodeEpoch)
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
	for _, parent := range guardAdmissionParentEpochs(graph, current.CodeEpoch) {
		edge, eerr := guardAdmissionEdge(graph, parent, current.CodeEpoch)
		if eerr != nil {
			return guardBootstrapReceiptVariant{}, nil, eerr
		}
		sources, serr := guardAdmissionBootstrapVariants(graph, parent)
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
			"%w: the bootstrap receipt history matches %d compiled variants (%s); an ambiguous history is not an authorization",
			ErrGuardManifestNoEdge, len(matches), strings.Join(names, ", "))
	}
}

// Per-boot witness selection may admit a completed Community profile; historical
// migration callbacks continue using verifyGuardCompletedV7History.
func guardAdmissionVerifyCompletedV7History(
	ctx context.Context,
	q guardEditionQuerier,
	dia dialect.Dialect,
	current guardManifest,
) (guardEditionHistory, error) {
	graph, err := guardAdmissionGraphFor(current)
	if err != nil {
		return guardEditionHistory{}, err
	}
	if _, commercial := graph.nodes[-current.CodeEpoch]; !commercial {
		return verifyGuardCompletedV7History(ctx, q, dia, current)
	}
	var attempts []string
	var causes []error
	for _, epoch := range graph.epochsDescending() {
		if epoch < 2 {
			continue
		}
		node, _ := graph.node(epoch)
		history, verr := guardAdmissionVerifyHistory(ctx, q, dia, node.Manifest)
		if verr == nil && guardEditionHistoryCompletesV7(history.Kind) {
			return history, nil
		}
		if verr != nil {
			attempts = append(attempts, fmt.Sprintf("epoch %d: %v", epoch, verr))
			causes = append(causes, verr)
			continue
		}
		attempts = append(attempts, fmt.Sprintf("epoch %d: history %q has no completed v7 witness",
			epoch, history.Kind))
	}
	summary := fmt.Errorf("%w: tracked core v7 has no completed history in the compiled predecessor lineage (%s)",
		ErrGuardManifestNoEdge, strings.Join(attempts, "; "))
	if len(causes) == 0 {
		return guardEditionHistory{}, summary
	}
	return guardEditionHistory{}, errors.Join(append([]error{summary}, causes...)...)
}
