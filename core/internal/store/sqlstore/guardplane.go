// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardplane.go is the chicken-and-egg half of C4: deciding whether the control plane may be
// created, creating its rows atomically with its relations, and opening or recognizing this
// edition's rollout.
//
// THE NAME IS DELIBERATE and should not be "improved" back to the obvious one. The export
// gate (the export curation script, leak_gate rule 2) rejects the two words run together
// anywhere outside operator/, because that spelling was this product's OLD binary/command/
// image identity. This file is about the GUARD's control plane, a different thing entirely —
// but a text gate cannot tell them apart, and the right answer to a publication gate is to
// stop writing the token, not to carve it an exception. `guardplane_sqlite_test.go` already
// used this stem.
//
// The ordering it lives inside is not negotiable and is worth stating once:
//
//  1. classifyRolloutControls (#448) runs FIRST under the advisory lock. It is what decides
//     whether a deployment predates a staged control, and it must observe the schema BEFORE
//     anything this boot creates.
//  2. preflightGuardControlPlane then asks its all-or-none question about the C4 objects
//     ONLY, treating #448's two relations as permitted predecessors rather than as evidence
//     of a half-finished C4 bootstrap.
//  3. migrate.Apply applies v1-v6. v6 creates the three relations, their guards, the ACL
//     posture, the inventory activations and the bootstrap receipts, in ONE transaction with
//     its own tracking row.
//  4. The module tables are created AFTER that, which is why the rollout cannot be opened in
//     v6 — see guardunits.go.
//  5. openOrVerifyGuardRollout runs at the insertion point, where every target exists.

// ErrGuardManifestNoEdge is a database whose recorded edition differs from this binary's
// and for which this binary carries no authorized transition.
//
// It is separate from drift and from "ahead" because it is the ordinary forward case done
// wrong: a NEW epoch is not accepted merely for being larger. The binary must contain the
// exact edge (from_epoch, from_sha) -> (to_epoch, to_sha, plan), and this edition — the
// first — contains none, so every epoch change lands here.
var ErrGuardManifestNoEdge = errors.New("sqlstore: this binary declares no authorized transition from the guard edition the database records")

// guardBootstrapDisposition is what the preflight concluded.
type guardBootstrapDisposition string

const (
	// guardBootstrapAbsent means the control plane is coherently absent: no v6 tracking row
	// and none of the three relations. v6 may create it.
	guardBootstrapAbsent guardBootstrapDisposition = "absent"
	// guardBootstrapComplete means v6 is recorded AND all three relations are present. v6
	// will be skipped by migrate.Apply, and nothing may recreate anything.
	guardBootstrapComplete guardBootstrapDisposition = "complete"
)

// guardPermittedPredecessors are the relations whose presence must NOT be read as evidence
// of a partial C4 bootstrap.
//
// They are #448's, they are created by classifyRolloutControls a few statements earlier in
// this same lock, and they are therefore present on EVERY database that reaches the C4
// preflight — including a completely fresh one. A preflight that treated any pre-existing
// Olivares relation as a predecessor would refuse every boot; one that enumerates exactly
// these two says precisely what it tolerates, and a regression pins the list.
func guardPermittedPredecessors() []string {
	return []string{dialect.ControlRolloutStateTable, dialect.ControlRolloutTransitionTable}
}

// preflightGuardControlPlane decides whether the control plane may be bootstrapped, and
// refuses anything in between.
//
// The all-or-none rule is the whole of it. "A table present without v6", "v6 with a table
// missing", "a different name occupying version 6" and "only some of the three" are ONE
// named refusal, because every one of them means the durable record and the objects disagree
// about a history — and re-creating an object over that disagreement is the laundering this
// ledger exists to detect.
//
// It is necessary rather than defensive: migrate.Apply loads a SET of applied versions and
// skips any version it finds, without checking that the name or the objects match
// (core/migrate/migrate.go). So a tracking row saying 6 is enough to make Apply do nothing
// at all, and nothing else would notice that the tables are gone.
func preflightGuardControlPlane(ctx context.Context, mdb dialect.Execer, dia dialect.Dialect) (guardBootstrapDisposition, error) {
	// The supported-major refusal comes FIRST on PostgreSQL, before anything is read or
	// created. Every field of the canonical projection is a documented catalog column, and a
	// major outside the range this repository has reasoned about may carry a structural
	// field the comparator does not read — in which case two genuinely different functions
	// compare equal. Refusing is the honest posture; claiming support nobody measured is not.
	if dia.Name() == store.EnginePostgres {
		major, err := postgresServerMajor(ctx, mdb)
		if err != nil {
			return "", err
		}
		if !postgresMajorSupported(major) {
			return "", fmt.Errorf("%w: the server is PostgreSQL %d and the guard manifest's projection covers %d..%d",
				ErrGuardUnsupportedPostgresMajor, major, supportedPostgresMajorMin, supportedPostgresMajorMax)
		}
	}

	present := make(map[string]bool, 3)
	var have, missing []string
	for _, t := range dialect.GuardControlPlaneTables() {
		cols, err := dia.TableColumns(ctx, mdb, t)
		if err != nil {
			return "", fmt.Errorf("sqlstore: guard control plane preflight: probe %q: %w", t, err)
		}
		present[t] = len(cols) > 0
		if present[t] {
			have = append(have, t)
		} else {
			missing = append(missing, t)
		}
	}

	trackingCols, err := dia.TableColumns(ctx, mdb, coreTrackingTable)
	if err != nil {
		return "", fmt.Errorf("sqlstore: guard control plane preflight: probe %q: %w", coreTrackingTable, err)
	}
	trackingExists := len(trackingCols) > 0

	var recorded bool
	var name, phase string
	var revertedAt sql.NullString
	if trackingExists {
		// A <=v5 tracker can legitimately have the historical three-column
		// shape. Read only the column that existed then until a v6 row is found;
		// migrate.ensureTracking adds phase/reverted_at immediately afterwards.
		// Conversely, a recorded v6 must have the current tracker shape because
		// its own Apply already crossed that reconciliation boundary.
		row := mdb.QueryRowContext(ctx, dia.Rebind(
			"SELECT name FROM "+coreTrackingRelation(dia)+" WHERE version = ?"), guardControlPlaneVersion)
		switch err := row.Scan(&name); {
		case err == nil:
			recorded = true
		case errors.Is(err, sql.ErrNoRows):
		default:
			return "", fmt.Errorf("sqlstore: guard control plane preflight: read version %d: %w", guardControlPlaneVersion, err)
		}
		if recorded {
			if !trackingCols["phase"] || !trackingCols["reverted_at"] {
				return "", fmt.Errorf("%w: version %d is recorded in the legacy three-column migration tracker; v6 can only be recorded after phase/reverted_at exist",
					ErrGuardControlPlaneBootstrapInconsistent, guardControlPlaneVersion)
			}
			if err := mdb.QueryRowContext(ctx, dia.Rebind(
				"SELECT phase, reverted_at FROM "+coreTrackingRelation(dia)+" WHERE version = ?"), guardControlPlaneVersion).
				Scan(&phase, &revertedAt); err != nil {
				return "", fmt.Errorf("sqlstore: guard control plane preflight: read version %d state: %w",
					guardControlPlaneVersion, err)
			}
		}
	}

	switch {
	case !recorded && len(have) == 0:
		return guardBootstrapAbsent, nil
	case recorded && len(missing) == 0:
		// Recorded AND present. The NAME and the phase are checked too: a different migration
		// occupying version 6 would make Apply skip this one forever while the objects it
		// expects were never created by anything.
		//
		// AND THE SHAPE, which is the half this used to skip entirely. "Present" above means a
		// relation of that name has at least one column; it does not mean it is the relation
		// this ledger's invariants rest on. A homonymous table without UNIQUE(rollout_id,
		// event_ordinal) classified as a finished bootstrap, and everything downstream then
		// reasoned about ordinals nothing stopped from repeating.
		if serr := verifyGuardControlPlaneShape(ctx, mdb, dia); serr != nil {
			return "", serr
		}
		if name != guardControlPlaneName {
			return "", fmt.Errorf("%w: version %d is recorded as %q, not %q, so the guard control plane's migration would be skipped forever while nothing ever created its objects",
				ErrGuardControlPlaneBootstrapInconsistent, guardControlPlaneVersion, name, guardControlPlaneName)
		}
		if phase != "expand" {
			return "", fmt.Errorf("%w: version %d is recorded in phase %q; the guard control plane is additive and forward-only, so any other phase means the row was not written by this engine",
				ErrGuardControlPlaneBootstrapInconsistent, guardControlPlaneVersion, phase)
		}
		if revertedAt.Valid {
			return "", fmt.Errorf("%w: version %d is marked reverted at %q, but the guard control plane has no down statements and cannot have been reversed by this engine",
				ErrGuardControlPlaneBootstrapInconsistent, guardControlPlaneVersion, revertedAt.String)
		}
		return guardBootstrapComplete, nil
	default:
		sort.Strings(have)
		sort.Strings(missing)
		return "", fmt.Errorf("%w: version %d recorded=%v, relations present=%v, absent=%v. The record and the objects disagree, and re-creating the missing ones would launder whatever removed them. (%s and %s are permitted predecessors and are not consulted here.)",
			ErrGuardControlPlaneBootstrapInconsistent, guardControlPlaneVersion, recorded, have, missing,
			guardPermittedPredecessors()[0], guardPermittedPredecessors()[1])
	}
}

// The v6 migration's identity, spelled once so the preflight and the builder cannot
// disagree about which version means what.
const (
	guardControlPlaneVersion = 6
	guardControlPlaneName    = "guard_control_plane"
)

// verifyBootstrapFunction refuses a pre-existing mutation-blocking function that is not the
// canonical one.
//
// On a legacy database the function is already there — the v1 migration created it — and the
// bootstrap must decide between reusing it and refusing. It never runs CREATE OR REPLACE
// over one: replacing a function every existing guard already points at would change the
// behavior of objects this rollout has not even adopted yet, which is a mutation disguised
// as a bootstrap.
//
// On a genuinely pristine database the function is absent, v1 creates it, and v6 consumes
// it. That is the one pre-gate exception the design authorizes.
func verifyBootstrapFunction(ctx context.Context, q rowQuerier) error {
	want := canonicalGuardDefinition().Function
	// THE EXACT ZERO-INPUT SIGNATURE, not every overload of the name. The identity this
	// bootstrap decides about is `public.olivares_block_mutation()`; an unrelated overload of
	// that name is a different function that this build neither creates, replaces nor points a
	// trigger at, and refusing the boot over it refused a coexistence the contract ratifies.
	// See guardZeroInputFunctionProjectionSQL for the measurement and for why the narrowing is
	// confined to this caller.
	got, exists, err := projectGuardZeroInputFunction(ctx, q, want.Schema, want.Name)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if diff := guardFunctionDiff(want, got); len(diff) > 0 {
		return fmt.Errorf("%w: %s.%s differs from the declared definition (%s); it is not replaced, because every guard already installed points at it",
			ErrGuardBootstrapFunctionDivergent, want.Schema, want.Name, strings.Join(diff, "; "))
	}
	return nil
}

// guardMetadataSpecs are the manifest entries for the control plane's OWN three relations.
//
// They are not registry descriptors and never become retryUnits: this migration creates them
// with their guards already in ALWAYS, so there is nothing to adopt and nothing to
// transition. They still need specs, because the bootstrap receipts attribute them and the
// verification compares them, and both need a declared canonical object to compare against.
func guardMetadataSpecs(format int64) ([]guardSpec, error) {
	def := canonicalGuardDefinition()
	out := make([]guardSpec, 0, 3)
	for _, t := range dialect.GuardControlPlaneTables() {
		spec := guardSpec{
			Key:                 guardKey{Schema: guardSchema, Relation: t, Trigger: t + guardTriggerSuffix},
			Producer:            guardProducerEngine,
			Definition:          def,
			DesiredEnableState:  guardStateAlways,
			LegacyAllowedStates: []string{guardStateAlways},
		}
		var err error
		if spec.DefinitionSHA256, err = spec.Definition.definitionDigest(spec.Key); err != nil {
			return nil, err
		}
		if spec.SpecSHA256, err = spec.specDigest(); err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	return out, nil
}

// guardBootstrapUnitID is the identity a bootstrap receipt attributes.
//
// It uses the same canonical encoding as a unit id with a DIFFERENT tag, so a bootstrap
// attribution can never collide with a unit's — and so a bootstrap receipt can never be
// mistaken for the receipt of an adoption the runner was supposed to perform.
func guardBootstrapUnitID(format int64, k guardKey) (string, error) {
	w := newCanonWriter(canonDomainEntry, format)
	w.str("bootstrap-unit")
	k.canon(w)
	d, err := w.sum()
	if err != nil {
		return "", err
	}
	return hexDigest(d), nil
}

// guardBootstrapExec is the v6 migration's row-writing half.
//
// It runs inside the migration's transaction, after the DDL and before the tracking row, so
// the relations, their guards, the inventory activations, the bootstrap receipts and the
// version record all become durable together. A crash anywhere leaves none of them, which
// is what makes the preflight's all-or-none question answerable at all.
//
// It writes NO pending-opened. The rollout is opened later, at the insertion point — see
// guardunits.go for the ordering fact that forces it.
func guardBootstrapExec(dia dialect.Dialect, m guardManifest) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		if err := requireCompleteGuardCurrentEdition(m); err != nil {
			return err
		}
		directoryInitial, err := inspectCoreDirectoryInitialDisposition(ctx, tx, dia)
		if err != nil {
			return fmt.Errorf("sqlstore: classify core directory state before guard v6 bootstrap: %w", err)
		}
		retainedSHA, err := emptyRetainedDigest()
		if err != nil {
			return err
		}
		rolloutID, err := guardRolloutID(m.Format, m.CodeEpoch, m.CodeSHA256, 0, retainedSHA)
		if err != nil {
			return err
		}

		// The inventory activations: this edition DECLARES it manages these entries. It is a
		// statement of intention, not an observation, which is why it can be written here —
		// before the module tables the entries name even exist.
		evs := make([]inventoryEvent, 0, len(m.Specs))
		for _, spec := range m.Specs {
			evs = append(evs, inventoryEvent{
				Kind:                inventoryActivate,
				Key:                 spec.Key,
				Producer:            spec.Producer,
				Format:              m.Format,
				CodeEpoch:           m.CodeEpoch,
				DefinitionSHA256:    spec.DefinitionSHA256,
				SpecSHA256:          spec.SpecSHA256,
				DesiredEnableState:  spec.DesiredEnableState,
				LegacyAllowedStates: spec.LegacyAllowedStates,
				// Activating a declared entry does not change the RETAINED set — that is the
				// history of entries this database keeps and the code no longer declares — so the
				// revision does not advance and the digest stays the empty stream's.
				RetainedRevision: 0,
				RetainedSHA256:   retainedSHA,
			})
		}
		if err := appendInventoryEvents(ctx, tx, dia, evs); err != nil {
			return err
		}

		// The bootstrap receipts: one per control-plane relation, attributing the guard this
		// migration just created in ALWAYS.
		//
		// Their prestate is CONSTRUCTED rather than projected, and the distinction is stated
		// because it is a real limit: this transaction created the objects, so a catalog
		// reading here would only echo its own DDL, and on SQLite there is no such catalog to
		// read. What makes the claim checkable is not this row — it is
		// verifyGuardControlPlaneObjects, which projects the three guards from the catalog
		// after the migration and refuses a rollout whose control plane is not what this
		// receipt says it made.
		metaSpecs, err := guardMetadataSpecs(m.Format)
		if err != nil {
			return err
		}
		rollout := guardRolloutContext{
			RolloutID: rolloutID, Format: m.Format, CodeEpoch: m.CodeEpoch,
			CodeSHA256: m.CodeSHA256, RetainedRevision: 0, RetainedSHA256: retainedSHA,
		}
		// The rows come from bootstrapReceiptFor, which is also what verifyGuardControlPlaneObjects
		// compares against. ONE construction, two callers: an expectation written separately from
		// the row it expects is free to drift, and the first symptom of that drift is a correct
		// database refusing to boot.
		//
		// Note what that construction does NOT set: from_enable_state stays NULL, because the
		// relation did not exist a statement ago and there is no state it moved FROM. 'A' there
		// would claim it was already present.
		for _, spec := range metaSpecs {
			unitID, uerr := guardBootstrapUnitID(m.Format, spec.Key)
			if uerr != nil {
				return uerr
			}
			r, rerr := bootstrapReceiptFor(rollout, spec, unitID)
			if rerr != nil {
				return rerr
			}
			if _, err := insertGuardReceipt(ctx, tx, dia, r); err != nil {
				return err
			}
		}
		// A fresh v2 already created the directory relations and needs no extra
		// evidence. An old <=v5 database did not: bind that observation to the
		// current bootstrap in THIS v6 transaction, so v7 can distinguish it from
		// somebody deleting the v7 row and its three tables later.
		if directoryInitial == coreDirectoryInitiallyAbsent {
			start, serr := guardV7Seal(m, guardV7SealStart, false)
			if serr != nil {
				return serr
			}
			if _, err := insertGuardReceipt(ctx, tx, dia, start); err != nil {
				return err
			}
		}
		return nil
	}
}

// guardBootstrapAttemptID is the attempt identity a bootstrap receipt carries.
//
// A fixed literal rather than a generated one, and deliberately so: the bootstrap is not an
// attempt that could have been retried, it is a migration that either committed or did not.
// A random value here would suggest a history of attempts that cannot exist.
const guardBootstrapAttemptID = "bootstrap"

// appendInventoryEvents chains a batch of inventory events, reading the head ONCE.
//
// Reading the head per event would be 2N roundtrips inside the bootstrap transaction for
// nothing: the chain is computed here, and the UNIQUE(event_ordinal) constraint is the
// backstop if the assumption that nobody else writes under this lock is ever wrong.
func appendInventoryEvents(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, evs []inventoryEvent) error {
	ordinal, prev, _, _, err := inventoryStreamHead(ctx, tx, dia)
	if err != nil {
		return err
	}
	stamp := nowRFC3339()
	q := dia.Rebind("INSERT INTO " + guardInventoryEventsTable + ` (
  event_sha256, event_ordinal, prev_event_sha256, kind,
  relation_schema, relation_name, trigger_name, producer,
  manifest_format, code_epoch, definition_sha256, spec_sha256,
  desired_enable_state, legacy_allowed_states,
  retained_revision, retained_sha256, recorded_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	for _, ev := range evs {
		ordinal++
		ev.EventOrdinal, ev.PrevEventSHA256, ev.RecordedAt = ordinal, prev, stamp
		if ev.EventSHA256, err = ev.chainDigest(); err != nil {
			return err
		}
		encodedStates, eerr := encodeUnitList(ev.LegacyAllowedStates)
		if eerr != nil {
			return eerr
		}
		if _, err := tx.ExecContext(ctx, q,
			digestBytes(ev.EventSHA256), ev.EventOrdinal, ev.PrevEventSHA256.bytes(), string(ev.Kind),
			ev.Key.Schema, ev.Key.Relation, ev.Key.Trigger, string(ev.Producer),
			ev.Format, ev.CodeEpoch, digestBytes(ev.DefinitionSHA256), digestBytes(ev.SpecSHA256),
			ev.DesiredEnableState, encodedStates,
			ev.RetainedRevision, digestBytes(ev.RetainedSHA256), ev.RecordedAt,
		); err != nil {
			return fmt.Errorf("sqlstore: append a guard inventory %s event for %s: %w", ev.Kind, ev.Key, err)
		}
		prev = someDigest(ev.EventSHA256)
	}
	return nil
}

func guardManifestAdditionEvents(edge guardManifestEdge, revision int64, retained [32]byte) []inventoryEvent {
	events := make([]inventoryEvent, 0, len(edge.Additions))
	for _, spec := range edge.Additions {
		events = append(events, inventoryEvent{
			Kind:                inventoryActivate,
			Key:                 spec.Key,
			Producer:            spec.Producer,
			Format:              edge.To.Format,
			CodeEpoch:           edge.To.CodeEpoch,
			DefinitionSHA256:    spec.DefinitionSHA256,
			SpecSHA256:          spec.SpecSHA256,
			DesiredEnableState:  spec.DesiredEnableState,
			LegacyAllowedStates: spec.LegacyAllowedStates,
			RetainedRevision:    revision,
			RetainedSHA256:      retained,
		})
	}
	return events
}

// The edition transition has one durable state machine. The three dimensions are kept
// separate only long enough to name what was read; correlateGuardEditionHistory accepts
// complete triples and no Cartesian product of individually-valid fragments.
type guardEditionReceiptState string
type guardEditionInventoryState string
type guardEditionGateState string
type guardEditionHistoryKind string

// The guardEditionPath token — the identity of the candidate history a projection
// follows — now lives in guardeditiongraph.go, because with more than one parent per
// node the start epoch stopped identifying a path and the whole crossed sequence had to.

const (
	guardEditionReceiptsCurrent            guardEditionReceiptState = "current-bootstrap"
	guardEditionReceiptsCurrentCompleted   guardEditionReceiptState = "current-bootstrap-plus-v7-completion"
	guardEditionReceiptsDirectStarted      guardEditionReceiptState = "current-bootstrap-plus-direct-v7-start"
	guardEditionReceiptsDirectCompleted    guardEditionReceiptState = "current-bootstrap-plus-direct-v7-start-and-completion"
	guardEditionReceiptsCurrentV9Completed guardEditionReceiptState = "current-bootstrap-plus-v7-and-v9-completion"
	guardEditionReceiptsDirectV9Completed  guardEditionReceiptState = "current-bootstrap-plus-direct-v7-and-v9-completion"
	guardEditionReceiptsPredecessor        guardEditionReceiptState = "predecessor-bootstrap"
	guardEditionReceiptsSealed             guardEditionReceiptState = "predecessor-bootstrap-plus-transition-seal"

	guardEditionInventoryCurrent     guardEditionInventoryState = "current-census"
	guardEditionInventoryPredecessor guardEditionInventoryState = "predecessor-census"
	guardEditionInventoryMixed       guardEditionInventoryState = "authorised-carry-forward"

	guardEditionGateAbsent      guardEditionGateState = "absent"
	guardEditionGateCurrent     guardEditionGateState = "current"
	guardEditionGatePredecessor guardEditionGateState = "predecessor-ready"

	guardEditionHistoryCurrent            guardEditionHistoryKind = "current-before-v7"
	guardEditionHistoryCurrentCompleted   guardEditionHistoryKind = "current-v7-completed"
	guardEditionHistoryDirectStarted      guardEditionHistoryKind = "direct-v7-started"
	guardEditionHistoryDirectCompleted    guardEditionHistoryKind = "direct-v7-completed"
	guardEditionHistoryCurrentV9Completed guardEditionHistoryKind = "current-v9-completed"
	guardEditionHistoryDirectV9Completed  guardEditionHistoryKind = "direct-v9-completed"
	guardEditionHistoryPredecessor        guardEditionHistoryKind = "predecessor"
	guardEditionHistoryPredecessorV7      guardEditionHistoryKind = "predecessor-v7-completed"
	guardEditionHistoryTransitioned       guardEditionHistoryKind = "transitioned"
)

type guardEditionHistory struct {
	Kind              guardEditionHistoryKind
	Revision          int64
	Retained          [32]byte
	Gate              gateProjection
	GateState         guardEditionGateState
	Path              guardEditionPath
	TerminalReceiptID [32]byte
	// ParentEpoch is the exact compiled predecessor a predecessor/transitioned history
	// came from. With one parent per node it could be re-derived; with several it is a
	// FACT about the projection that selected this history, so it is carried instead of
	// recomputed by whoever needs it next.
	ParentEpoch int64
	// CompletedV9 records that this history's node has crossed the access-evidence
	// delta: either its v9 completion seal is present, or its transition seal is over a
	// node that carries DA. Kept beside Kind because "transitioned" alone cannot say it.
	CompletedV9 bool
}

func guardEditionHistoryCompletesV7(kind guardEditionHistoryKind) bool {
	switch kind {
	case guardEditionHistoryCurrentCompleted,
		guardEditionHistoryDirectCompleted,
		guardEditionHistoryCurrentV9Completed,
		guardEditionHistoryDirectV9Completed,
		guardEditionHistoryPredecessorV7,
		guardEditionHistoryTransitioned:
		return true
	default:
		return false
	}
}

// guardEditionHistoryCompletesV9 is the predicate a later same-B DF/DK edge requires of
// its source, and the one core v9's fresh/direct actions establish.
//
// It is deliberately NOT "the epoch is 5, 6 or 7". A database whose census reaches an
// access-evidence edition but whose v9 transaction has not committed is exactly the
// pending state the migration exists to close, and treating the epoch as the witness
// would let a deleted tracker plus deleted tables read as a completed upgrade.
func guardEditionHistoryCompletesV9(history guardEditionHistory) bool {
	if !history.CompletedV9 {
		return false
	}
	return guardEditionHistoryCompletesV7(history.Kind)
}

// verifyGuardCompletedV7History is the tracked-v7 preflight selector. It performs no
// writes. Normally the current manifest verifies directly. During a chained multi-edition
// boot, however, core v7 may be durable while one or more later module editions have not
// crossed their edges yet. Walk the compiled ancestor editions, newest first, and accept
// the first exact history that carries the completed witness; the v9 migration and the
// post-module seam cross the remaining edges in order.
func verifyGuardCompletedV7History(
	ctx context.Context,
	q guardEditionQuerier,
	dia dialect.Dialect,
	current guardManifest,
) (guardEditionHistory, error) {
	graph, err := guardEditionGraphFor(current)
	if err != nil {
		return guardEditionHistory{}, err
	}
	var attempts []string
	// The rendered attempts are for a human; `causes` keeps the ERROR VALUES so the
	// summary below can still be inspected with errors.Is. Rendering a cause with
	// %v and keeping only the string severs the chain: the prose survives and the
	// type does not, and a caller can then no longer tell a broken guard chain from
	// any other reason this lineage walk gave up. The trunk's checkpoint test says
	// exactly that in its own words — without the named error "this test would pass
	// without a checkpoint".
	var causes []error
	for _, epoch := range graph.epochsDescending() {
		if epoch < 2 {
			// Epoch 1 predates the guard edition edges entirely: it has no v7
			// witness to complete, and asking for one would report a missing edge
			// rather than the missing completion this selector is about.
			continue
		}
		node, _ := graph.node(epoch)
		history, verr := verifyGuardEditionHistory(ctx, q, dia, node.Manifest)
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
	// errors.Join keeps BOTH reachable: ErrGuardManifestNoEdge, which says why the
	// walk ended, and every cause it met on the way — so the message reads the same
	// and errors.Is stops lying about what it found.
	return guardEditionHistory{}, errors.Join(append([]error{summary}, causes...)...)
}

type guardEditionMigrationAction string

const (
	guardEditionMigrationCompleteV7 guardEditionMigrationAction = "complete-v7"
	guardEditionMigrationTransition guardEditionMigrationAction = "transition"
)

// correlateGuardEditionMigrationStart binds the physical state v7 observed BEFORE
// descriptor DDL to the durable guard history it observed in the same transaction.
// Exactly three starts exist: a fresh v2 already has the relations and current bootstrap;
// a direct <=v5 upgrade has neither the relations nor anything beyond its exact start seal;
// and a K2 upgrade has neither the relations nor anything beyond the exact predecessor.
// Any completed/transitioned history with v7 still untracked is never an idempotent retry: a
// real retry rolls the whole transaction back, while a committed v7 is skipped by Apply.
func correlateGuardEditionMigrationStart(
	initial coreDirectoryInitialDisposition,
	history guardEditionHistoryKind,
) (guardEditionMigrationAction, error) {
	switch {
	case initial == coreDirectoryInitiallyPresent && history == guardEditionHistoryCurrent:
		return guardEditionMigrationCompleteV7, nil
	case initial == coreDirectoryInitiallyAbsent && history == guardEditionHistoryDirectStarted:
		return guardEditionMigrationCompleteV7, nil
	case initial == coreDirectoryInitiallyAbsent && history == guardEditionHistoryPredecessor:
		return guardEditionMigrationTransition, nil
	default:
		return "", fmt.Errorf("%w: core v7 initially found its directory relations %s while guard history was %s; no v7 transaction starts from that pair",
			ErrGuardManifestNoEdge, initial, history)
	}
}

func correlateGuardEditionHistory(
	engine store.Engine,
	receipts guardEditionReceiptState,
	inventory guardEditionInventoryState,
	gate guardEditionGateState,
) (guardEditionHistoryKind, error) {
	switch engine {
	case store.EngineSQLite:
		switch {
		case receipts == guardEditionReceiptsCurrent &&
			inventory == guardEditionInventoryCurrent && gate == guardEditionGateAbsent:
			return guardEditionHistoryCurrent, nil
		case receipts == guardEditionReceiptsCurrentCompleted &&
			inventory == guardEditionInventoryCurrent && gate == guardEditionGateAbsent:
			return guardEditionHistoryCurrentCompleted, nil
		case receipts == guardEditionReceiptsDirectStarted &&
			inventory == guardEditionInventoryCurrent && gate == guardEditionGateAbsent:
			return guardEditionHistoryDirectStarted, nil
		case receipts == guardEditionReceiptsDirectCompleted &&
			inventory == guardEditionInventoryCurrent && gate == guardEditionGateAbsent:
			return guardEditionHistoryDirectCompleted, nil
		case receipts == guardEditionReceiptsCurrentV9Completed &&
			inventory == guardEditionInventoryCurrent && gate == guardEditionGateAbsent:
			return guardEditionHistoryCurrentV9Completed, nil
		case receipts == guardEditionReceiptsDirectV9Completed &&
			inventory == guardEditionInventoryCurrent && gate == guardEditionGateAbsent:
			return guardEditionHistoryDirectV9Completed, nil
		case receipts == guardEditionReceiptsPredecessor &&
			inventory == guardEditionInventoryPredecessor && gate == guardEditionGateAbsent:
			return guardEditionHistoryPredecessor, nil
		case receipts == guardEditionReceiptsSealed &&
			inventory == guardEditionInventoryMixed && gate == guardEditionGateAbsent:
			return guardEditionHistoryTransitioned, nil
		}
	case store.EnginePostgres:
		switch {
		case receipts == guardEditionReceiptsCurrent && inventory == guardEditionInventoryCurrent &&
			gate == guardEditionGateAbsent:
			return guardEditionHistoryCurrent, nil
		case receipts == guardEditionReceiptsCurrentCompleted && inventory == guardEditionInventoryCurrent &&
			(gate == guardEditionGateAbsent || gate == guardEditionGateCurrent):
			return guardEditionHistoryCurrentCompleted, nil
		case receipts == guardEditionReceiptsDirectStarted && inventory == guardEditionInventoryCurrent &&
			gate == guardEditionGateAbsent:
			return guardEditionHistoryDirectStarted, nil
		case receipts == guardEditionReceiptsDirectCompleted && inventory == guardEditionInventoryCurrent &&
			(gate == guardEditionGateAbsent || gate == guardEditionGateCurrent):
			return guardEditionHistoryDirectCompleted, nil
		case receipts == guardEditionReceiptsCurrentV9Completed && inventory == guardEditionInventoryCurrent &&
			(gate == guardEditionGateAbsent || gate == guardEditionGateCurrent):
			return guardEditionHistoryCurrentV9Completed, nil
		case receipts == guardEditionReceiptsDirectV9Completed && inventory == guardEditionInventoryCurrent &&
			(gate == guardEditionGateAbsent || gate == guardEditionGateCurrent):
			return guardEditionHistoryDirectV9Completed, nil
		case receipts == guardEditionReceiptsPredecessor && inventory == guardEditionInventoryPredecessor &&
			gate == guardEditionGatePredecessor:
			return guardEditionHistoryPredecessor, nil
		case receipts == guardEditionReceiptsSealed && inventory == guardEditionInventoryMixed &&
			gate == guardEditionGateCurrent:
			return guardEditionHistoryTransitioned, nil
		}
	default:
		return "", fmt.Errorf("sqlstore: unsupported guard edition engine %q", engine)
	}
	return "", fmt.Errorf("%w: engine %s has bootstrap receipts %s, inventory %s and gate %s; no guard edition transition writes that combination",
		ErrGuardManifestNoEdge, engine, receipts, inventory, gate)
}

// guardEditionTwoMigrationExec is the continuation v7 must invoke after its descriptor DDL
// and before its tracking row, on the SAME *sql.Tx. On PostgreSQL it commits the two
// activations, the transition seal and pending-opened together; on SQLite it commits the
// activations and seal together and deliberately writes no gate event.
func guardEditionTwoMigrationExec(dia dialect.Dialect, current guardManifest) directoryMigrationAfter {
	return func(ctx context.Context, tx *sql.Tx, initial coreDirectoryInitialDisposition) error {
		migrationManifest, history, err := guardV7MigrationManifest(ctx, tx, dia, current)
		if err != nil {
			return err
		}
		action, err := correlateGuardEditionMigrationStart(initial, history.Kind)
		if err != nil {
			return err
		}
		switch action {
		case guardEditionMigrationCompleteV7:
			return completeV7GuardEditionInTx(ctx, tx, dia, migrationManifest, history)
		case guardEditionMigrationTransition:
		default:
			return fmt.Errorf("%w: v7 selected unrecognized migration action %q", ErrGuardManifestNoEdge, action)
		}

		// The ONLY edge core v7 crosses is the directory delta into epoch 2. It is
		// selected as the sole compiled predecessor of that node rather than as
		// "whatever parent the graph offers": v7 predates every other delta, and a v7
		// that could cross an access-evidence edge would create relations its own
		// transaction never made.
		if migrationManifest.CodeEpoch != 2 {
			return fmt.Errorf("%w: core v7 can only cross the directory edge into edition 2, not into %d",
				ErrGuardManifestNoEdge, migrationManifest.CodeEpoch)
		}
		edge, ok, err := guardManifestSoleEditionEdge(migrationManifest)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: epoch %d has no compiled edition edge",
				ErrGuardManifestNoEdge, migrationManifest.CodeEpoch)
		}
		var plans []guardUnitPlan
		openCurrent := dia.Name() == store.EnginePostgres
		if openCurrent {
			if err := verifyGuardFenceCapability(ctx, tx); err != nil {
				return err
			}
			keys := make([]guardKey, 0, len(migrationManifest.Specs))
			for _, spec := range migrationManifest.Specs {
				keys = append(keys, spec.Key)
			}
			observed, perr := projectGuardCatalogBatch(ctx, tx, keys)
			if perr != nil {
				return perr
			}
			var refusals []guardPlanRefusal
			plans, refusals, perr = buildGuardUnitPlans(migrationManifest, observed)
			if perr != nil {
				return perr
			}
			if len(refusals) != 0 {
				return fmt.Errorf("sqlstore: guard edition v7 cannot open the epoch-%d rollout: %d of %d targets have no authorized plan (first: %s)",
					migrationManifest.CodeEpoch, len(refusals), len(migrationManifest.Specs), refusals[0])
			}
		}
		return transitionGuardEditionInTx(ctx, tx, dia, migrationManifest, edge, history, plans, openCurrent)
	}
}

// guardV7MigrationManifest chooses the edition whose directory edge can actually run
// inside core v7. A fresh/direct install completes its current edition directly. A
// database whose durable history predates epoch 2 walks the compiled predecessors until
// it reaches the exact 1->2 bridge; later module editions are crossed only after their
// authored module migrations have created the relations in each delta.
func guardV7MigrationManifest(
	ctx context.Context,
	q guardEditionQuerier,
	dia dialect.Dialect,
	current guardManifest,
) (guardManifest, guardEditionHistory, error) {
	var candidate guardManifest
	var attempts []string
	// Third site of the same class in this file, confirmed as a defect by the sol max
	// contrast of 2026-08-20 (it names this function as repeating the pattern). Its
	// verification is by CLASS, not by its own red test: no caller inspects this error
	// with errors.Is today, so nothing would go red either way. It is fixed anyway
	// because leaving one of three identical defects reads as deliberate to whoever
	// finds the file next, and because a caller that starts inspecting would meet the
	// same silent flattening the other two had.
	var causes []error
	graph, gerr := guardEditionGraphFor(current)
	if gerr != nil {
		return guardManifest{}, guardEditionHistory{}, gerr
	}
	for _, epoch := range graph.epochsDescending() {
		if epoch < 2 {
			continue
		}
		node, _ := graph.node(epoch)
		candidate = node.Manifest
		history, err := verifyGuardEditionHistory(ctx, q, dia, candidate)
		if err == nil {
			// Two starts and no third. A fresh or direct install completes v7 at the
			// edition v6 bootstrapped — which is the CURRENT one; and the only edge
			// v7 itself crosses is the directory delta into epoch 2, so that is the
			// only node whose predecessor history it may act on.
			if history.Kind == guardEditionHistoryCurrent ||
				history.Kind == guardEditionHistoryDirectStarted || epoch == 2 {
				return candidate, history, nil
			}
			attempts = append(attempts, fmt.Sprintf("epoch %d: history %q cannot run core v7",
				epoch, history.Kind))
			continue
		}
		attempts = append(attempts, fmt.Sprintf("epoch %d: %v", epoch, err))
		causes = append(causes, err)
	}
	summary := fmt.Errorf("%w: core v7 history matches no edition in the compiled predecessor lineage (%s)",
		ErrGuardManifestNoEdge, strings.Join(attempts, "; "))
	if len(causes) == 0 {
		return guardManifest{}, guardEditionHistory{}, summary
	}
	return guardManifest{}, guardEditionHistory{},
		errors.Join(append([]error{summary}, causes...)...)
}

func completeV7GuardEditionInTx(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	current guardManifest,
	history guardEditionHistory,
) error {
	direct := history.Kind == guardEditionHistoryDirectStarted
	if (history.Kind != guardEditionHistoryCurrent && !direct) || history.Revision != 0 {
		return fmt.Errorf("%w: v7 completion requires the exact current bootstrap, optionally carrying its direct-upgrade start seal, at revision zero; got %q revision %d",
			ErrGuardManifestNoEdge, history.Kind, history.Revision)
	}
	completion, err := guardV7Seal(current, guardV7SealCompletion, direct)
	if err != nil {
		return err
	}
	if _, err := insertGuardReceipt(ctx, tx, dia, completion); err != nil {
		return err
	}
	post, err := verifyGuardEditionHistory(ctx, tx, dia, current)
	if err != nil {
		return fmt.Errorf("sqlstore: verify v7 completion before commit: %w", err)
	}
	want := guardEditionHistoryCurrentCompleted
	if direct {
		want = guardEditionHistoryDirectCompleted
	}
	if post.Kind != want {
		return fmt.Errorf("%w: v7 completion produced history %q, want %q",
			ErrGuardManifestNoEdge, post.Kind, want)
	}
	return nil
}

// transitionGuardEditionInTx commits ONE compiled edge.
//
// The edge is a PARAMETER rather than a derivation, and that is the DAG's central
// discipline: an epoch-6 node has two compiled parents, so a function that re-derived
// "the" edge here could commit a different transition from the one the selector proved
// the database was standing on. The caller has already correlated the durable history;
// this writes exactly that history's next step.
func transitionGuardEditionInTx(
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
	if history.ParentEpoch != edge.From.CodeEpoch {
		return fmt.Errorf("%w: the durable history stands on edition %d and the supplied edge starts at %d",
			ErrGuardManifestNoEdge, history.ParentEpoch, edge.From.CodeEpoch)
	}
	revision, retained, err := verifyInventoryChain(ctx, tx, dia, edge.From)
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
	post, err := verifyGuardEditionHistory(ctx, tx, dia, current)
	if err != nil {
		return fmt.Errorf("sqlstore: verify guard edition transition before v7 commit: %w", err)
	}
	if post.Kind != guardEditionHistoryTransitioned {
		return fmt.Errorf("%w: guard edition transition produced history %q, want %q",
			ErrGuardManifestNoEdge, post.Kind, guardEditionHistoryTransitioned)
	}
	return nil
}

func appendGuardEditionTransitionRows(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	current guardManifest,
	edge guardManifestEdge,
	revision int64,
	retained [32]byte,
	predecessorReceiptID [32]byte,
	plans []guardUnitPlan,
	openCurrent bool,
) error {
	if err := appendInventoryEvents(ctx, tx, dia, guardManifestAdditionEvents(edge, revision, retained)); err != nil {
		return err
	}
	seal, err := guardEditionTransitionSeal(edge, revision, retained, predecessorReceiptID)
	if err != nil {
		return err
	}
	if _, err := insertGuardReceipt(ctx, tx, dia, seal); err != nil {
		return err
	}
	if openCurrent {
		rolloutID, rerr := guardRolloutID(current.Format, current.CodeEpoch, current.CodeSHA256, revision, retained)
		if rerr != nil {
			return rerr
		}
		if _, err := appendGateEvent(ctx, tx, dia, gateEvent{
			RolloutID:        rolloutID,
			Kind:             gateEventPendingOpened,
			Format:           current.Format,
			CodeEpoch:        current.CodeEpoch,
			CodeSHA256:       current.CodeSHA256,
			RetainedRevision: revision,
			RetainedSHA256:   retained,
			Phase:            gatePhasePending,
			Condition:        gateConditionClean,
			ExpectedUnits:    guardPlanUnitIDs(plans),
		}); err != nil {
			return err
		}
	}
	return nil
}

// verifyInventoryChain reads the whole inventory stream, verifies every digest, INTERPRETS every
// event, and RECOMPUTES the retained pair.
//
// The three verbs are separate properties and an earlier version only had the first. Verifying
// digests says the rows were not edited; it says nothing about whether their SEQUENCE is one this
// edition can reason about, and nothing about whether the retained pair the rollout id is derived
// from is the pair the history actually produces. Taking that pair from the LAST ROW — which is
// what it used to do — means whoever wrote the last row chose the identity of every rollout that
// follows.
//
// The kinds this edition does not implement are REFUSED rather than skipped. `retain`,
// `reactivate` and `tombstone` are legal values of the column's CHECK because a later edition
// will need them without a migration; accepting them here would mean an edition with no fold for
// their semantics quietly deciding what they mean.
//
// manifest is compared against the activations: every declared entry must have exactly one, with
// the digests the manifest computes. That is what stops a gate being opened for THIS binary over
// an inventory some OTHER binary activated.
func verifyInventoryChain(ctx context.Context, q dialect.Querier, dia dialect.Dialect, manifest guardManifest) (int64, [32]byte, error) {
	return verifyInventoryChainMode(ctx, q, dia, manifest, func(activations map[guardKey]inventoryEvent) error {
		return verifyGuardActivationCensus(manifest, activations)
	})
}

func verifyInventoryChainExact(ctx context.Context, q dialect.Querier, dia dialect.Dialect, manifest guardManifest) (int64, [32]byte, error) {
	return verifyInventoryChainMode(ctx, q, dia, manifest, func(activations map[guardKey]inventoryEvent) error {
		return verifyGuardActivationsExact(manifest, activations)
	})
}

func verifyInventoryChainExpectations(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
	manifest guardManifest,
	expected []guardActivationExpectation,
) (int64, [32]byte, error) {
	return verifyInventoryChainMode(ctx, q, dia, manifest, func(activations map[guardKey]inventoryEvent) error {
		return verifyGuardActivationExpectations(expected, activations)
	})
}

func verifyInventoryChainMode(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
	manifest guardManifest,
	verifyCensus func(map[guardKey]inventoryEvent) error,
) (int64, [32]byte, error) {
	rows, err := q.QueryContext(ctx, dia.Rebind(`SELECT
  event_sha256, event_ordinal, prev_event_sha256, kind,
  relation_schema, relation_name, trigger_name, producer,
  manifest_format, code_epoch, definition_sha256, spec_sha256,
  desired_enable_state, legacy_allowed_states,
  retained_revision, retained_sha256, recorded_at
FROM `+guardOnly(dia)+guardInventoryEventsTable+` ORDER BY event_ordinal`))
	if err != nil {
		return 0, [32]byte{}, fmt.Errorf("sqlstore: read the guard inventory: %w", err)
	}
	defer rows.Close()

	var prev optDigest
	var expected int64 = 1
	revision := int64(0)
	retained, err := emptyRetainedDigest()
	if err != nil {
		return 0, [32]byte{}, err
	}
	activations := make(map[guardKey]inventoryEvent, len(manifest.Specs))
	for rows.Next() {
		var (
			ev                           inventoryEvent
			storedRaw, prevRaw           []byte
			defRaw, specRaw, retainedRaw []byte
			kind, legacyStates           string
		)
		if err := rows.Scan(&storedRaw, &ev.EventOrdinal, &prevRaw, &kind,
			&ev.Key.Schema, &ev.Key.Relation, &ev.Key.Trigger, &ev.Producer,
			&ev.Format, &ev.CodeEpoch, &defRaw, &specRaw,
			&ev.DesiredEnableState, &legacyStates,
			&ev.RetainedRevision, &retainedRaw, &ev.RecordedAt); err != nil {
			return 0, [32]byte{}, fmt.Errorf("sqlstore: read the guard inventory: %w", err)
		}
		ev.Kind = inventoryEventKind(kind)
		ev.LegacyAllowedStates = decodeUnitList(legacyStates)
		if ev.DefinitionSHA256, err = scanDigest(defRaw, "a guard inventory definition digest"); err != nil {
			return 0, [32]byte{}, err
		}
		if ev.SpecSHA256, err = scanDigest(specRaw, "a guard inventory spec digest"); err != nil {
			return 0, [32]byte{}, err
		}
		if ev.RetainedSHA256, err = scanDigest(retainedRaw, "a guard inventory retained digest"); err != nil {
			return 0, [32]byte{}, err
		}
		if ev.PrevEventSHA256, err = scanOptDigest(prevRaw, "a guard inventory predecessor digest"); err != nil {
			return 0, [32]byte{}, err
		}
		if ev.EventOrdinal != expected {
			return 0, [32]byte{}, fmt.Errorf("%w: the inventory jumps from ordinal %d to %d",
				ErrGuardGateChainBroken, expected-1, ev.EventOrdinal)
		}
		if ev.PrevEventSHA256 != prev {
			return 0, [32]byte{}, fmt.Errorf("%w: inventory event %d records predecessor %s, but its predecessor hashes to %s",
				ErrGuardGateChainBroken, ev.EventOrdinal, ev.PrevEventSHA256, prev)
		}
		recomputed, cerr := ev.chainDigest()
		if cerr != nil {
			return 0, [32]byte{}, cerr
		}
		stored, serr := scanDigest(storedRaw, "a guard inventory event digest")
		if serr != nil {
			return 0, [32]byte{}, serr
		}
		if recomputed != stored {
			return 0, [32]byte{}, fmt.Errorf("%w: inventory event %d (%s for %s) stores digest %s but hashes to %s",
				ErrGuardGateChainBroken, ev.EventOrdinal, ev.Kind, ev.Key, hexDigest(stored), hexDigest(recomputed))
		}
		prev = someDigest(stored)
		expected++

		// AND THE ENTRY DIGEST IS RECOMPUTED FROM THE EVENT'S OWN TUPLE.
		//
		// The chain digest proves the row is the row the writer chained; it proves nothing about
		// whether spec_sha256 describes the rest of the row. Since spec_sha256 IS the value the
		// manifest comparison below trusts, an event could carry the expected digest beside a
		// different producer, a different legacy policy or a different desired state and satisfy
		// every check — the digest agreeing with the manifest and the tuple agreeing with
		// nothing. Recomputing it here is what makes the comparison a comparison of MEANING.
		if ev.Format != manifest.Format {
			return 0, [32]byte{}, fmt.Errorf("%w: inventory event %d for %s declares manifest format %d and this edition is format %d",
				ErrGuardInventoryUnsupported, ev.EventOrdinal, ev.Key, ev.Format, manifest.Format)
		}
		recomputedSpec, serr2 := guardSpec{
			Key:                 ev.Key,
			Producer:            ev.Producer,
			DesiredEnableState:  ev.DesiredEnableState,
			LegacyAllowedStates: ev.LegacyAllowedStates,
			DefinitionSHA256:    ev.DefinitionSHA256,
		}.specDigest()
		if serr2 != nil {
			return 0, [32]byte{}, serr2
		}
		if recomputedSpec != ev.SpecSHA256 {
			return 0, [32]byte{}, fmt.Errorf("%w: inventory event %d for %s stores entry digest %s, and its own tuple (producer %q, desired %q, legacy %v, definition %s) hashes to %s",
				ErrGuardInventoryUnsupported, ev.EventOrdinal, ev.Key, hexDigest(ev.SpecSHA256),
				ev.Producer, ev.DesiredEnableState, ev.LegacyAllowedStates,
				hexDigest(ev.DefinitionSHA256), hexDigest(recomputedSpec))
		}

		// THE KIND IS INTERPRETED. Only `activate` has a fold in this edition.
		switch ev.Kind {
		case inventoryActivate:
			if prevEv, dup := activations[ev.Key]; dup {
				return 0, [32]byte{}, fmt.Errorf("%w: %s is activated twice (events %d and %d)",
					ErrGuardInventoryUnsupported, ev.Key, prevEv.EventOrdinal, ev.EventOrdinal)
			}
			activations[ev.Key] = ev
		default:
			return 0, [32]byte{}, fmt.Errorf("%w: event %d is a %q for %s, and this edition has no fold for that kind — accepting it would mean deciding what it means without implementing it",
				ErrGuardInventoryUnsupported, ev.EventOrdinal, ev.Kind, ev.Key)
		}
		// AND THE RETAINED PAIR IS RECOMPUTED, not read. Activating a DECLARED entry does not
		// change the retained set — that is the set of entries this database keeps and the code no
		// longer declares — so after activations only, the pair is still the empty stream's. The
		// row's own claim is compared against that rather than trusted.
		if ev.RetainedRevision != revision || ev.RetainedSHA256 != retained {
			return 0, [32]byte{}, fmt.Errorf("%w: event %d records the retained pair (%d,%s) where this history produces (%d,%s)",
				ErrGuardInventoryUnsupported, ev.EventOrdinal,
				ev.RetainedRevision, hexDigest(ev.RetainedSHA256), revision, hexDigest(retained))
		}
	}
	if err := rows.Err(); err != nil {
		return 0, [32]byte{}, fmt.Errorf("sqlstore: read the guard inventory: %w", err)
	}

	if err := verifyCensus(activations); err != nil {
		return 0, [32]byte{}, err
	}
	return revision, retained, nil
}

// verifyGuardActivationCensus accepts exactly the complete histories the compiled edge
// chain can produce: a fresh bootstrap at this manifest's epoch, or one exact predecessor
// history followed by this edge's complete delta. It never accepts an epoch independently
// per entry; doing so would admit torn or hand-spliced mixtures no transition wrote.
func verifyGuardActivationCensus(manifest guardManifest, activations map[guardKey]inventoryEvent) error {
	lineages, err := guardEditionLineagesFor(manifest)
	if err != nil {
		return err
	}
	// Fourth and last site of the class in this file, found by a differential sweep over
	// the 228 production files the chain touches rather than by reading: same shape,
	// same recipe, and the sweep is what proves the class does not live anywhere else.
	var differences []string
	var causes []error
	for _, lineage := range lineages {
		expected := guardActivationExpectationsForLineage(lineage)
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

type guardActivationExpectation struct {
	Spec  guardSpec
	Epoch int64
}

// guardEditionLineagesFor enumerates every compiled candidate history ending at a
// manifest's own edition.
func guardEditionLineagesFor(manifest guardManifest) ([]guardEditionLineage, error) {
	graph, err := guardEditionGraphFor(manifest)
	if err != nil {
		return nil, err
	}
	return graph.lineagesEndingAt(manifest.CodeEpoch)
}

// guardActivationExpectationsForLineage is the exact inventory a lineage writes: the
// start edition's whole census in manifest order, then each crossed edge's additions in
// the order the edges were crossed.
//
// The ORDER is the discriminator. Two lineages between the same endpoints activate the
// same relations, and only the sequence tells them apart — E2 -> E5 -> E6 records the
// access-evidence delta before the communication delta, E2 -> E3 -> E6 after — so an
// expectation list that sorted its entries would make two different histories look alike.
func guardActivationExpectationsForLineage(lineage guardEditionLineage) []guardActivationExpectation {
	start := lineage.start()
	expected := make([]guardActivationExpectation, 0, len(start.Manifest.Specs))
	for _, spec := range start.Manifest.Specs {
		expected = append(expected, guardActivationExpectation{Spec: spec, Epoch: start.Epoch})
	}
	for _, edge := range lineage.Edges {
		for _, spec := range edge.Additions {
			expected = append(expected, guardActivationExpectation{Spec: spec, Epoch: edge.To.CodeEpoch})
		}
	}
	return expected
}

func verifyGuardActivationsExact(manifest guardManifest, activations map[guardKey]inventoryEvent) error {
	expected := make([]guardActivationExpectation, 0, len(manifest.Specs))
	for _, spec := range manifest.Specs {
		expected = append(expected, guardActivationExpectation{Spec: spec, Epoch: manifest.CodeEpoch})
	}
	return verifyGuardActivationExpectations(expected, activations)
}

func verifyGuardActivationExpectations(expected []guardActivationExpectation, activations map[guardKey]inventoryEvent) error {
	declared := make(map[guardKey]bool, len(expected))
	for i, want := range expected {
		key := want.Spec.Key
		declared[key] = true
		ev, ok := activations[key]
		if !ok {
			return fmt.Errorf("%w: %s is declared by this edition but never activated in the inventory",
				ErrGuardInventoryUnsupported, key)
		}
		if wantOrdinal := int64(i + 1); ev.EventOrdinal != wantOrdinal {
			return fmt.Errorf("%w: the activation of %s is event %d where this edition's exact census requires event %d",
				ErrGuardInventoryUnsupported, key, ev.EventOrdinal, wantOrdinal)
		}
		spec := want.Spec
		if ev.SpecSHA256 != spec.SpecSHA256 || ev.DefinitionSHA256 != spec.DefinitionSHA256 {
			return fmt.Errorf("%w: the activation of %s records spec %s / definition %s where this edition computes %s / %s",
				ErrGuardInventoryUnsupported, spec.Key,
				hexDigest(ev.SpecSHA256), hexDigest(ev.DefinitionSHA256),
				hexDigest(spec.SpecSHA256), hexDigest(spec.DefinitionSHA256))
		}
		if ev.DesiredEnableState != spec.DesiredEnableState {
			return fmt.Errorf("%w: the activation of %s wants state %q where this edition declares %q",
				ErrGuardInventoryUnsupported, spec.Key, ev.DesiredEnableState, spec.DesiredEnableState)
		}
		if ev.Producer != spec.Producer {
			return fmt.Errorf("%w: the activation of %s was declared by %q where this edition declares %q",
				ErrGuardInventoryUnsupported, spec.Key, ev.Producer, spec.Producer)
		}
		if ev.CodeEpoch != want.Epoch {
			return fmt.Errorf("%w: the activation of %s carries code epoch %d where this history requires epoch %d",
				ErrGuardInventoryUnsupported, spec.Key, ev.CodeEpoch, want.Epoch)
		}
		if !equalStringSlices(ev.LegacyAllowedStates, spec.LegacyAllowedStates) {
			return fmt.Errorf("%w: the activation of %s allows adoption from %v where this edition allows %v",
				ErrGuardInventoryUnsupported, spec.Key, ev.LegacyAllowedStates, spec.LegacyAllowedStates)
		}
	}
	extra := make([]guardKey, 0)
	for key := range activations {
		if !declared[key] {
			extra = append(extra, key)
		}
	}
	if len(extra) > 0 {
		sort.Slice(extra, func(i, j int) bool { return extra[i].less(extra[j]) })
		return fmt.Errorf("%w: the inventory activates %s, which this edition does not declare; an activation it does not recognize means the inventory belongs to a different manifest",
			ErrGuardInventoryUnsupported, extra[0])
	}
	return nil
}

// recordedEdition is the newest edition the gate has ever opened a rollout for.
type recordedEdition struct {
	Found      bool
	RolloutID  string
	Format     int64
	CodeEpoch  int64
	CodeSHA256 [32]byte
}

// latestRecordedEdition reads it.
//
// The ordering is DETERMINISTIC and reads no clock: epoch, then retained revision, then the
// rollout id. A timestamp tiebreak would let a VM restored from a snapshot, or an NTP
// correction, decide which edition a database believes it is on.
func latestRecordedEdition(ctx context.Context, q dialect.Querier, dia dialect.Dialect) (recordedEdition, error) {
	rows, err := q.QueryContext(ctx, dia.Rebind(
		"SELECT rollout_id, manifest_format, code_epoch, code_sha256 FROM "+guardOnly(dia)+guardGateEventsTable+
			" WHERE kind = ? ORDER BY code_epoch DESC, retained_revision DESC, rollout_id DESC LIMIT 1"),
		string(gateEventPendingOpened))
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

// classifyRecordedEdition maps a recorded edition that is NOT this one onto its named
// refusal.
//
// The four outcomes are the step-2 design's start matrix, and they are separate errors
// because the operator's move differs for each: an unknown format means upgrade the binary,
// drift means find who edited an object, ahead means this binary is the old one, and no-edge
// means the transition was never authorized.
func classifyRecordedEdition(m guardManifest, rec recordedEdition) error {
	switch {
	case rec.Format > m.Format:
		return fmt.Errorf("%w: the database records manifest format %d and this binary understands %d",
			ErrGuardFormatAhead, rec.Format, m.Format)
	case rec.CodeEpoch > m.CodeEpoch:
		return fmt.Errorf("%w: the database records guard epoch %d and this binary declares %d",
			ErrGuardManifestAhead, rec.CodeEpoch, m.CodeEpoch)
	case rec.CodeEpoch == m.CodeEpoch && rec.CodeSHA256 != m.CodeSHA256:
		return fmt.Errorf("%w: epoch %d is recorded with code digest %s and this binary computes %s; an active definition changed without the edition that authorizes it",
			ErrGuardManifestDrift, rec.CodeEpoch, hexDigest(rec.CodeSHA256), hexDigest(m.CodeSHA256))
	default:
		return fmt.Errorf("%w: the database records epoch %d (%s) and this binary declares epoch %d (%s); a larger epoch is not an authorisation, the binary must carry the exact edge",
			ErrGuardManifestNoEdge, rec.CodeEpoch, hexDigest(rec.CodeSHA256), m.CodeEpoch, hexDigest(m.CodeSHA256))
	}
}

// verifyGuardPredecessorReady proves the edge starts at a completed, still-attested
// rollout. Merely finding an epoch-1 pending event is not authority to move past a
// blocked or half-applied predecessor.
func verifyGuardPredecessorReady(ctx context.Context, q dialect.Querier, dia dialect.Dialect, rec recordedEdition) error {
	gate, err := foldGateEvents(ctx, q, dia, rec.RolloutID)
	if err != nil {
		return err
	}
	if !gate.Found {
		return fmt.Errorf("%w: predecessor rollout %s is recorded but has no legible event stream",
			ErrGuardManifestNoEdge, rec.RolloutID)
	}
	if gate.Format != rec.Format || gate.CodeEpoch != rec.CodeEpoch || gate.CodeSHA256 != rec.CodeSHA256 {
		return fmt.Errorf("%w: predecessor rollout %s folds to edition %d/%d/%s, not its recorded %d/%d/%s",
			ErrGuardManifestNoEdge, rec.RolloutID,
			gate.Format, gate.CodeEpoch, hexDigest(gate.CodeSHA256),
			rec.Format, rec.CodeEpoch, hexDigest(rec.CodeSHA256))
	}
	recomputed, err := guardRolloutID(gate.Format, gate.CodeEpoch, gate.CodeSHA256,
		gate.RetainedRevision, gate.RetainedSHA256)
	if err != nil {
		return err
	}
	if recomputed != rec.RolloutID {
		return fmt.Errorf("%w: predecessor event tuple identifies rollout %s, not the recorded %s",
			ErrGuardManifestNoEdge, recomputed, rec.RolloutID)
	}
	if gate.Phase != gatePhaseReady || gate.Condition != gateConditionVerified {
		return fmt.Errorf("%w: predecessor rollout %s is %s/%s; only ready/verified may cross an edition edge",
			ErrGuardManifestNoEdge, rec.RolloutID, gate.Phase, gate.Condition)
	}
	if err := verifyGuardCheckpoint(ctx, q, dia, rec.RolloutID, gate); err != nil {
		return fmt.Errorf("%w: predecessor rollout %s no longer matches its closing checkpoint: %v",
			ErrGuardManifestNoEdge, rec.RolloutID, err)
	}
	return nil
}

type guardEditionQuerier interface {
	rowQuerier
	dialect.Querier
}

// guardEditionInventorySelection is the one candidate history the inventory stream
// matches, with everything the correlator needs from it.
type guardEditionInventorySelection struct {
	State       guardEditionInventoryState
	Path        guardEditionPath
	ParentEpoch int64
	Revision    int64
	Retained    [32]byte
}

// classifyGuardEditionInventory is selectRecordedLineage's inventory half: it evaluates
// every COMPLETE compiled candidate history and accepts exactly one.
//
// "Exactly one" is not decoration now that editions form a DAG. Two lineages between the
// same endpoints activate the same relations in a different sequence, so a single
// inventory can match at most one of them — but the moment that stops being provable by
// construction, picking the first would silently choose which upgrade a database is
// believed to have performed. An ambiguous match therefore writes nothing and joins its
// refusal to ErrGuardManifestNoEdge.
func classifyGuardEditionInventory(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
	current guardManifest,
) (guardEditionInventorySelection, error) {
	trivial := guardEditionPathOf([]guardEditionNode{{Epoch: current.CodeEpoch}})
	if revision, retained, err := verifyInventoryChainExact(ctx, q, dia, current); err == nil {
		return guardEditionInventorySelection{
			State: guardEditionInventoryCurrent, Path: trivial, Revision: revision, Retained: retained,
		}, nil
	}
	graph, err := guardEditionGraphFor(current)
	if err != nil {
		return guardEditionInventorySelection{}, err
	}
	parents := graph.parentEpochs(current.CodeEpoch)
	if len(parents) == 0 {
		_, _, currentErr := verifyInventoryChainExact(ctx, q, dia, current)
		return guardEditionInventorySelection{}, currentErr
	}

	type candidate struct {
		selection guardEditionInventorySelection
		lineage   guardEditionLineage
	}
	// Same contract as verifyGuardCompletedV7History above: the rendered lists are for
	// a human and `causes` keeps the error VALUES, so the ErrGuardGateChainBroken a
	// chain check may have produced stays reachable with errors.Is instead of being
	// flattened into ErrGuardInventoryUnsupported. Found by the sol max contrast of
	// 2026-08-20 as a fourth instance of the same class, measured with a positive
	// control on SQLite.
	var predecessorErrs, mixedErrs []string
	var causes []error
	var matches []candidate

	// PREDECESSOR: the stream stops at one of the compiled parents, so this database has
	// not crossed the last edge yet.
	for _, parent := range parents {
		lineages, lerr := graph.lineagesEndingAt(parent)
		if lerr != nil {
			return guardEditionInventorySelection{}, lerr
		}
		for _, lineage := range lineages {
			revision, retained, xerr := verifyInventoryChainExpectations(
				ctx, q, dia, current, guardActivationExpectationsForLineage(lineage))
			if xerr == nil {
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
	// CARRY-FORWARD: the stream reaches this edition through one exact sequence of edges.
	lineages, lerr := graph.lineagesEndingAt(current.CodeEpoch)
	if lerr != nil {
		return guardEditionInventorySelection{}, lerr
	}
	for _, lineage := range lineages {
		if len(lineage.Edges) == 0 {
			continue // the trivial lineage is the exact-current case tried above
		}
		revision, retained, xerr := verifyInventoryChainExpectations(
			ctx, q, dia, current, guardActivationExpectationsForLineage(lineage))
		if xerr == nil {
			last := lineage.Edges[len(lineage.Edges)-1]
			matches = append(matches, candidate{guardEditionInventorySelection{
				State: guardEditionInventoryMixed, Path: lineage.path(),
				ParentEpoch: last.From.CodeEpoch, Revision: revision, Retained: retained,
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
	default:
		paths := make([]string, 0, len(matches))
		for _, m := range matches {
			paths = append(paths, fmt.Sprintf("%s/%s", m.selection.State, m.lineage.path()))
		}
		return guardEditionInventorySelection{}, fmt.Errorf(
			"%w: the inventory matches %d compiled candidate histories (%s); an ambiguous history is not an authorisation",
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

func verifyGuardGateTuple(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
	rollout guardRolloutContext,
) (gateProjection, error) {
	gate, err := foldGateEvents(ctx, q, dia, rollout.RolloutID)
	if err != nil {
		return gateProjection{}, err
	}
	if !gate.Found {
		return gateProjection{}, fmt.Errorf("%w: recorded rollout %s has no legible gate stream",
			ErrGuardGateIllegalTransition, rollout.RolloutID)
	}
	if gate.Format != rollout.Format || gate.CodeEpoch != rollout.CodeEpoch ||
		gate.CodeSHA256 != rollout.CodeSHA256 || gate.RetainedRevision != rollout.RetainedRevision ||
		gate.RetainedSHA256 != rollout.RetainedSHA256 {
		return gateProjection{}, fmt.Errorf("%w: rollout %s folds to edition %d/%d/%s retained %d/%s, want %d/%d/%s retained %d/%s",
			ErrGuardGateIllegalTransition, rollout.RolloutID,
			gate.Format, gate.CodeEpoch, hexDigest(gate.CodeSHA256),
			gate.RetainedRevision, hexDigest(gate.RetainedSHA256),
			rollout.Format, rollout.CodeEpoch, hexDigest(rollout.CodeSHA256),
			rollout.RetainedRevision, hexDigest(rollout.RetainedSHA256))
	}
	recomputed, err := guardRolloutID(gate.Format, gate.CodeEpoch, gate.CodeSHA256,
		gate.RetainedRevision, gate.RetainedSHA256)
	if err != nil {
		return gateProjection{}, err
	}
	if recomputed != rollout.RolloutID {
		return gateProjection{}, fmt.Errorf("%w: gate tuple identifies rollout %s, not %s",
			ErrGuardGateIllegalTransition, recomputed, rollout.RolloutID)
	}
	return gate, nil
}

// verifyGuardHistoricalCheckpoint proves that the predecessor was a COMPLETE K2 close,
// not merely a syntactically-ready gate event over self-consistent log heads. The durable
// enumeration must rebuild a bijective plan for the predecessor manifest; every enumerated
// unit must have a fully-bound receipt; every terminal catalog object must still be canonical
// and ALWAYS; and only then is the historical inventory prefix/checkpoint admitted.
//
// This is used both immediately before crossing the edge and after the suffix exists. In the
// latter case the predecessor attested the prefix ending at len(manifest.Specs); its receipt
// stream remains terminal because the edition seal belongs to the target rollout.
func verifyGuardHistoricalCheckpoint(
	ctx context.Context,
	q guardEditionQuerier,
	dia dialect.Dialect,
	manifest guardManifest,
	rollout guardRolloutContext,
	gate gateProjection,
) error {
	refuse := func(what string, err error) error {
		if err == nil {
			return fmt.Errorf("%w: predecessor rollout %s %s", ErrGuardManifestNoEdge, rollout.RolloutID, what)
		}
		return fmt.Errorf("%w: predecessor rollout %s %s: %w",
			ErrGuardManifestNoEdge, rollout.RolloutID, what, err)
	}
	if gate.Phase != gatePhaseReady || gate.Condition != gateConditionVerified {
		return refuse(fmt.Sprintf("is %s/%s; only a complete ready/verified K2 attestation may cross an edition edge",
			gate.Phase, gate.Condition), nil)
	}
	if !gate.CheckpointPresent {
		return refuse("has no closing checkpoint", ErrGuardGateIllegalTransition)
	}

	keys := make([]guardKey, 0, len(manifest.Specs))
	for _, spec := range manifest.Specs {
		keys = append(keys, spec.Key)
	}
	observed, err := projectGuardCatalogBatch(ctx, q, keys)
	if err != nil {
		return refuse("cannot project its final catalog", err)
	}
	plans, err := guardPlanFromEnumeration(manifest, gate.ExpectedUnits, gate.Units, observed)
	if err != nil {
		return refuse("does not carry a bijective K2 unit enumeration", err)
	}
	receipts, err := guardRolloutReceipts(ctx, q, dia, rollout.RolloutID)
	if err != nil {
		return refuse("has an invalid receipt stream", err)
	}
	if err := verifyGuardReceiptCensus(rollout, plans, receipts); err != nil {
		return refuse("does not carry exactly the receipts its K2 plan attests", err)
	}
	summary := guardRolloutSummary{Outcomes: map[reconcileOutcome]int{}}
	if _, err := verifyGuardTerminals(ctx, q, dia, rollout, gate, plans, receipts, observed, &summary); err != nil {
		return refuse("does not re-verify every K2 terminal", err)
	}
	for _, spec := range manifest.Specs {
		row := observed[spec.Key]
		canonical, diff := row.matchesCanonical(spec)
		if !canonical || row.EnableState != guardStateAlways {
			detail := strings.Join(diff, "; ")
			if detail == "" {
				detail = fmt.Sprintf("enable state is %q, want %q", row.EnableState, guardStateAlways)
			}
			return refuse(fmt.Sprintf("does not leave %s canonical and ALWAYS (%s)", spec.Key, detail), nil)
		}
	}

	inventoryCount := int64(len(manifest.Specs))
	if gate.Checkpoint.InventoryCount != inventoryCount {
		return refuse(fmt.Sprintf("attested %d inventory events, want the exact predecessor prefix of %d",
			gate.Checkpoint.InventoryCount, inventoryCount), ErrGuardGateChainBroken)
	}
	rows, err := q.QueryContext(ctx, dia.Rebind(
		"SELECT event_sha256 FROM "+guardOnly(dia)+guardInventoryEventsTable+" WHERE event_ordinal = ?"), inventoryCount)
	if err != nil {
		return fmt.Errorf("sqlstore: read predecessor inventory checkpoint: %w", err)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("sqlstore: read predecessor inventory checkpoint: %w", err)
		}
		_ = rows.Close()
		return fmt.Errorf("%w: predecessor inventory event %d is absent", ErrGuardGateChainBroken, inventoryCount)
	}
	var raw []byte
	if err := rows.Scan(&raw); err != nil {
		_ = rows.Close()
		return fmt.Errorf("sqlstore: read predecessor inventory checkpoint: %w", err)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("sqlstore: read predecessor inventory checkpoint: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("sqlstore: read predecessor inventory checkpoint: %w", err)
	}
	head, err := scanDigest(raw, "the predecessor inventory checkpoint")
	if err != nil {
		return err
	}
	if head != gate.Checkpoint.InventoryHead {
		return refuse(fmt.Sprintf("attested inventory head %s at event %d, which now hashes to %s",
			hexDigest(gate.Checkpoint.InventoryHead), inventoryCount, hexDigest(head)), ErrGuardGateChainBroken)
	}
	_, receiptHead, err := receiptStreamHead(ctx, q, dia, rollout.RolloutID)
	if err != nil {
		return err
	}
	receiptCount, err := countStreamRows(ctx, q, dia, guardReceiptsTable, rollout.RolloutID)
	if err != nil {
		return err
	}
	if !receiptHead.Valid || receiptHead.D != gate.Checkpoint.ReceiptHead ||
		receiptCount != gate.Checkpoint.ReceiptCount {
		return refuse(fmt.Sprintf("attested %d receipts heading at %s, and it now holds %d heading at %s",
			gate.Checkpoint.ReceiptCount, hexDigest(gate.Checkpoint.ReceiptHead), receiptCount, receiptHead),
			ErrGuardGateChainBroken)
	}
	return nil
}

// guardEditionGateSelection is the gate half of the durable selector: which edition the
// gate stream says a rollout was last opened for, and — when that is a predecessor —
// exactly which compiled parent it was.
type guardEditionGateSelection struct {
	State       guardEditionGateState
	Gate        gateProjection
	ParentEpoch int64
}

func classifyGuardEditionGate(
	ctx context.Context,
	q guardEditionQuerier,
	dia dialect.Dialect,
	current guardManifest,
	graph guardEditionGraph,
	revision int64,
	retained [32]byte,
) (guardEditionGateSelection, error) {
	recorded, err := latestRecordedEdition(ctx, q, dia)
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
		// EVERY compiled parent is tried, and the one that authorizes the recorded
		// tuple is NAMED. With a chain the caller could re-derive it; with a DAG an
		// epoch-6 target has two, and "the predecessor" would be a guess.
		edges, eerr := graph.predecessorEdges(current.CodeEpoch)
		if eerr != nil {
			return guardEditionGateSelection{}, eerr
		}
		for _, edge := range edges {
			if edge.authorizes(recorded.Format, recorded.CodeEpoch, recorded.CodeSHA256) {
				state, edition, parentEpoch = guardEditionGatePredecessor, edge.From, edge.From.CodeEpoch
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

// verifyGuardEditionHistory is the sole selector for durable edition state. Receipt,
// inventory and gate projections are all exact on their own and are then admitted only as
// one of the engine-specific triples correlateGuardEditionHistory enumerates.
func verifyGuardEditionHistory(
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
	receiptVariant, _, err := classifyGuardBootstrapReceiptVariant(ctx, q, dia, current)
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

// verifyGuardHistoricalGatePath re-verifies every rollout the recorded lineage closed,
// from the edition the database currently stands on back to the one it was bootstrapped
// at.
//
// It walks the SELECTED LINEAGE rather than "every epoch between two numbers". With a
// chain those were the same walk; with the DAG an epoch-6 history could have reached its
// parent through either the communication or the access-evidence edge, and re-deriving
// the route here would verify rollouts a different upgrade would have opened.
func verifyGuardHistoricalGatePath(
	ctx context.Context,
	q guardEditionQuerier,
	dia dialect.Dialect,
	graph guardEditionGraph,
	standingOn int64,
	path guardEditionPath,
	revision int64,
	retained [32]byte,
) error {
	lineages, err := graph.lineagesEndingAt(standingOn)
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
		// The receipts named a lineage ending at the CURRENT edition (a carry-forward),
		// so the historical part of it is the prefix that ends at the parent.
		currentLineages, cerr := graph.lineagesEndingAt(graph.Current)
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

// openOrVerifyGuardRollout recognizes this edition's rollout, or opens it.
//
// It returns the immutable authorisation every unit is bound to, plus the folded gate. The
// authorisation is READ BACK from the durable event even when this call is the one that
// wrote it, and that is deliberate: a context assembled from the binary's constants would
// let a callback authorize a change no durable event ever opened.
func openOrVerifyGuardRollout(
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
		// THE CAPABILITY IS RECOMPUTED FROM THE EVENT'S OWN TUPLE, not merely copied out of it.
		//
		// Returning the fields as read meant a self-consistent event stored under this rollout's
		// key became the authorisation every callback receives — its epoch, its digests, its
		// retained pair. Requiring the tuple to reproduce the KEY it was found under is what ties
		// the two together: the id is derived from the tuple, so an event whose tuple hashes
		// elsewhere is an event filed under a rollout it does not describe.
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
	// THE HISTORIES THAT MAY OPEN A ROLLOUT FROM OUTSIDE AN EDITION TRANSACTION.
	//
	// They are the fresh and direct completions, at BOTH core boundaries: a database that
	// stopped after v7 and one that has since crossed v9 are the same shape here — an
	// edition v6 bootstrapped directly, never transitioned into, whose rollout no
	// migration has opened. Every transitioned history had its gate opened inside the
	// transaction that crossed its edge, and is handled above.
	openable := history.Kind == guardEditionHistoryCurrentCompleted ||
		history.Kind == guardEditionHistoryDirectCompleted ||
		history.Kind == guardEditionHistoryCurrentV9Completed ||
		history.Kind == guardEditionHistoryDirectV9Completed
	if history.GateState != guardEditionGateAbsent || !openable {
		return guardRolloutContext{}, gateProjection{}, false, fmt.Errorf(
			"%w: history %q with gate %q cannot open a rollout outside the v7 edition transaction",
			ErrGuardManifestNoEdge, history.Kind, history.GateState)
	}

	// NOTHING MAY BE OPENED OVER AN INCOMPLETE PLAN.
	//
	// The enumeration a `pending-opened` records IS the rollout's authorisation, and every later
	// boot verifies the units it names — so a plan that skipped a target because this boot could
	// not build a unit for it would durably authorize a rollout that never has to look at that
	// target again. Refusing here keeps the enumeration a bijection with the manifest, which is
	// what guardPlanFromEnumeration is then able to require.
	//
	// The cost is stated rather than hidden: on a database that has never opened a rollout, a
	// refusal is NOT recorded durably, because there is no rollout to record it against and
	// opening one to hold the diagnostic is precisely what must not happen. The refusal is
	// deterministic from the manifest and the catalog, so the next boot derives the same one.
	// Where a rollout DOES exist, the caller records it against that rollout as before.
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
	locked, err := verifyGuardEditionHistory(ctx, tx, dia, m)
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

	// Read it back. The projection the coordinator acts on is the DURABLE one, not the
	// struct this function just built — so a write that did not land the way it was meant to
	// cannot be acted on as though it had.
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

// verifyGuardControlPlaneObjects verifies the control plane against its OWN attribution: the
// shape of the three relations, the exact bootstrap history (current three plus its universal
// v7 completion, direct-upgrade three plus start and completion, predecessor three, or
// predecessor three plus its transition seal), and — on PostgreSQL — the three guards that
// history claims to have created and re-attested.
//
// AN EARLIER VERSION CLAIMED TO DO THIS AND DID NOT. Its comment said it compared what the
// bootstrap receipt said it made; its code read no receipt at all. It rebuilt the specs from
// the binary and compared the catalog with those, so deleting the entire receipt-writing loop
// from the migration left a first boot able to reach `ready`. A comparison between a binary
// and a catalog it just wrote is not an attribution — it is the binary agreeing with itself.
//
// What is verified now, in the order a failure is most useful in:
//
//  1. THE SHAPE. Whatever else is true, a relation that cannot hold the ledger's uniquenesses
//     makes every check after it meaningless.
//  2. THE RECEIPTS, on BOTH engines. The three control-plane bootstrap receipts, each bound
//     to the rollout and compared field by field with guardMetadataSpecs, plus only the exact
//     v7 start/completion or edition-transition witness admitted by the history state machine.
//  3. THE OBJECTS, on PostgreSQL only — SQLite has no tgenabled, and its trigger pair applies
//     to every connection unconditionally, which is the property 'A' buys on PostgreSQL. Each
//     is compared against the receipt that claims it: the receipt says it moved the guard to
//     ALWAYS from nothing, and the catalog is read back to see whether it is there.
func verifyGuardControlPlaneObjects(ctx context.Context, q guardEditionQuerier, dia dialect.Dialect, m guardManifest) error {
	if err := verifyGuardControlPlaneShape(ctx, q, dia); err != nil {
		return err
	}
	if _, _, err := verifyGuardBootstrapReceiptHistory(ctx, q, dia, m); err != nil {
		return err
	}
	return verifyGuardControlPlaneCatalog(ctx, q, dia, m.Format)
}

func verifyGuardControlPlaneCatalog(ctx context.Context, q rowQuerier, dia dialect.Dialect, format int64) error {
	if dia.Name() != store.EnginePostgres {
		return nil
	}
	specs, err := guardMetadataSpecs(format)
	if err != nil {
		return err
	}
	for _, spec := range specs {
		row, perr := projectGuardCatalogRow(ctx, q, spec.Key)
		if perr != nil {
			return perr
		}
		canonical, diff := row.matchesCanonical(spec)
		if !canonical {
			return fmt.Errorf("%w: the control plane's own guard on %s is not the declared object (%s)",
				ErrGuardControlPlaneBootstrapInconsistent, spec.Key, strings.Join(diff, "; "))
		}
		if row.EnableState != guardStateAlways {
			return fmt.Errorf("%w: the bootstrap receipt history attests state %q for %s and the catalog reports %q; at %q a logical-replication apply mutates the log in silence",
				ErrGuardBootstrapReceiptsInvalid, guardStateAlways, spec.Key, row.EnableState, guardStateOrigin)
		}
	}
	return nil
}

// bootstrapReceiptFor rebuilds the receipt the v6 migration writes for one relation.
//
// It is the SAME construction guardBootstrapExec performs, expressed once and called from
// both, because a verifier that rebuilt the expectation independently would be free to drift
// from the writer — and the first symptom of that drift is a correct database refusing to
// boot.
func bootstrapReceiptFor(rollout guardRolloutContext, spec guardSpec, unitID string) (guardReceipt, error) {
	pre := rollout.bind(prestate{
		TargetExists:          true,
		GuardPresent:          true,
		GuardEnableState:      guardStateAlways,
		GuardMatchesCanonical: true,
	}, spec)
	digest, err := prestateDigest(pre)
	if err != nil {
		return guardReceipt{}, err
	}
	r := guardReceipt{
		RolloutID:        rollout.RolloutID,
		UnitID:           unitID,
		Kind:             guardReceiptKindBootstrap,
		Intent:           guardIntentBootstrap,
		Key:              spec.Key,
		Epoch:            rollout.CodeEpoch,
		Format:           rollout.Format,
		CodeSHA256:       rollout.CodeSHA256,
		RetainedRevision: rollout.RetainedRevision,
		RetainedSHA256:   rollout.RetainedSHA256,
		SpecSHA256:       spec.SpecSHA256,
		DefinitionSHA256: spec.DefinitionSHA256,
		PrestateSHA256:   digest,
		ToEnableState:    guardStateAlways,
		AttemptID:        guardBootstrapAttemptID,
	}
	if r.ReceiptID, err = r.bodyDigest(); err != nil {
		return guardReceipt{}, err
	}
	return r, nil
}

// guardEditionSealAttemptID names the one non-bootstrap act represented with the bootstrap
// receipt kind. The edge is part of the identity: a 2->3 seal cannot be replayed as a 1->2
// seal even when every other fixed metadata field is the same.
func guardEditionSealAttemptID(edge guardManifestEdge) string {
	return fmt.Sprintf("edition-%d-to-%d", edge.From.CodeEpoch, edge.To.CodeEpoch)
}

type guardV7SealPhase string

const (
	guardV7SealStart      guardV7SealPhase = "start"
	guardV7SealCompletion guardV7SealPhase = "completion"

	guardDirectV7StartAttemptID = "direct-v7-start"
	guardV7CompletionAttemptID  = "v7-complete"
)

func guardV7SealUnitID(format int64, phase guardV7SealPhase, key guardKey) (string, error) {
	w := newCanonWriter(canonDomainEntry, format)
	w.str("v7-seal-unit")
	w.str(string(phase))
	key.canon(w)
	digest, err := w.sum()
	if err != nil {
		return "", err
	}
	return hexDigest(digest), nil
}

// guardV7Seal re-attests the first fixed metadata guard under a distinct
// unit identity and links it to the preceding durable claim. The start seal is
// linked to the ordinary current bootstrap. Universal completion is linked to
// that bootstrap on a fresh path and to start on a direct upgrade. Their bodies
// therefore cannot be transplanted between editions or reordered into a
// history no migration writes.
func guardV7Seal(current guardManifest, phase guardV7SealPhase, completionFollowsStart bool) (guardReceipt, error) {
	meta, err := guardMetadataSpecs(current.Format)
	if err != nil {
		return guardReceipt{}, err
	}
	if len(meta) == 0 || meta[0].Key.Relation != guardGateEventsTable {
		return guardReceipt{}, fmt.Errorf("sqlstore: the v7 seal requires %s as the first fixed metadata spec",
			guardGateEventsTable)
	}
	empty, err := emptyRetainedDigest()
	if err != nil {
		return guardReceipt{}, err
	}
	rollout, err := guardBootstrapRollout(current, 0, empty)
	if err != nil {
		return guardReceipt{}, err
	}
	bootstrapUnit, err := guardBootstrapUnitID(current.Format, meta[0].Key)
	if err != nil {
		return guardReceipt{}, err
	}
	anchor, err := bootstrapReceiptFor(rollout, meta[0], bootstrapUnit)
	if err != nil {
		return guardReceipt{}, err
	}
	attemptID := guardDirectV7StartAttemptID
	predecessor := anchor.ReceiptID
	if phase == guardV7SealCompletion {
		if completionFollowsStart {
			start, serr := guardV7Seal(current, guardV7SealStart, false)
			if serr != nil {
				return guardReceipt{}, serr
			}
			predecessor = start.ReceiptID
		}
		attemptID = guardV7CompletionAttemptID
	} else if phase != guardV7SealStart {
		return guardReceipt{}, fmt.Errorf("sqlstore: unrecognized v7 seal phase %q", phase)
	}
	unitID, err := guardV7SealUnitID(current.Format, phase, meta[0].Key)
	if err != nil {
		return guardReceipt{}, err
	}
	prestateSHA, err := prestateDigest(rollout.bind(prestate{
		TargetExists:          true,
		GuardPresent:          true,
		GuardEnableState:      guardStateAlways,
		GuardMatchesCanonical: true,
	}, meta[0]))
	if err != nil {
		return guardReceipt{}, err
	}
	seal := guardReceipt{
		RolloutID:            rollout.RolloutID,
		UnitID:               unitID,
		Kind:                 guardReceiptKindBootstrap,
		Intent:               guardIntentBootstrap,
		Key:                  meta[0].Key,
		Epoch:                rollout.CodeEpoch,
		Format:               rollout.Format,
		CodeSHA256:           rollout.CodeSHA256,
		RetainedRevision:     rollout.RetainedRevision,
		RetainedSHA256:       rollout.RetainedSHA256,
		SpecSHA256:           meta[0].SpecSHA256,
		DefinitionSHA256:     meta[0].DefinitionSHA256,
		PrestateSHA256:       prestateSHA,
		FromEnableState:      someText(guardStateAlways),
		ToEnableState:        guardStateAlways,
		PredecessorReceiptID: someDigest(predecessor),
		AttemptID:            attemptID,
	}
	if seal.ReceiptID, err = seal.bodyDigest(); err != nil {
		return guardReceipt{}, err
	}
	return seal, nil
}

const (
	// guardAccessEvidenceV9SealUnitDomain is the v9 completion witness's own unit-id
	// domain. A DIFFERENT tag from the bootstrap and v7-seal units, so a v9 completion
	// can never be presented as either, and neither can be replayed as one.
	guardAccessEvidenceV9SealUnitDomain = "access-evidence-v9-seal-unit"
	// guardAccessEvidenceV9AttemptID names the one act this receipt attests.
	guardAccessEvidenceV9AttemptID = "access-evidence-v9-complete"
)

func guardAccessEvidenceV9SealUnitID(format int64, key guardKey) (string, error) {
	w := newCanonWriter(canonDomainEntry, format)
	w.str(guardAccessEvidenceV9SealUnitDomain)
	key.canon(w)
	digest, err := w.sum()
	if err != nil {
		return "", err
	}
	return hexDigest(digest), nil
}

// guardAccessEvidenceV9Seal is the immutable completion witness core v9 appends on the
// fresh and direct paths.
//
// WHY THE TRACKING ROW IS NOT ENOUGH, which is the whole argument for this receipt. The
// authorized pre-v9 direct checkpoint is "current history, direct-v7-completed, no DA
// tables, no v9 row". Deleting the v9 tracking row AND the four relations after a
// successful upgrade reproduces that state exactly, so a tracker alone cannot tell a
// pending creation from a completed one that was dismantled. An append-only receipt
// bound to the previous terminal claim can: the ledger refuses deletion, and the seal
// names the exact v7 completion it follows.
//
// The guarded-transition path deliberately gets NO second seal. Its edition transition
// seal is already immutable, already binds its exact From/To pair, and a deleted v9
// tracker cannot make a transitioned history look like its source again.
func guardAccessEvidenceV9Seal(
	current guardManifest,
	predecessorReceiptID [32]byte,
	followsDirectStart bool,
) (guardReceipt, error) {
	membership, ok := guardEditionMembershipForEpoch(current.CodeEpoch)
	if !ok || !membership.has(guardDeltaAccessEvidence) {
		return guardReceipt{}, fmt.Errorf(
			"sqlstore: guard edition %d does not carry the access-evidence delta, so it has no v9 completion to seal",
			current.CodeEpoch)
	}
	if predecessorReceiptID == ([32]byte{}) {
		return guardReceipt{}, fmt.Errorf(
			"sqlstore: the v9 completion seal has no terminal v7 predecessor receipt")
	}
	meta, err := guardMetadataSpecs(current.Format)
	if err != nil {
		return guardReceipt{}, err
	}
	if len(meta) == 0 || meta[0].Key.Relation != guardGateEventsTable {
		return guardReceipt{}, fmt.Errorf("sqlstore: the v9 seal requires %s as the first fixed metadata spec",
			guardGateEventsTable)
	}
	empty, err := emptyRetainedDigest()
	if err != nil {
		return guardReceipt{}, err
	}
	rollout, err := guardBootstrapRollout(current, 0, empty)
	if err != nil {
		return guardReceipt{}, err
	}
	unitID, err := guardAccessEvidenceV9SealUnitID(current.Format, meta[0].Key)
	if err != nil {
		return guardReceipt{}, err
	}
	prestateSHA, err := prestateDigest(rollout.bind(prestate{
		TargetExists:          true,
		GuardPresent:          true,
		GuardEnableState:      guardStateAlways,
		GuardMatchesCanonical: true,
	}, meta[0]))
	if err != nil {
		return guardReceipt{}, err
	}
	// followsDirectStart is not a field of the body: it is already expressed by WHICH
	// terminal receipt this seal names, because the fresh and direct v7 completions have
	// different ids. Keeping it as a parameter makes the caller state which path it is
	// on, so a direct upgrade cannot accidentally build the fresh seal.
	_ = followsDirectStart
	seal := guardReceipt{
		RolloutID:            rollout.RolloutID,
		UnitID:               unitID,
		Kind:                 guardReceiptKindBootstrap,
		Intent:               guardIntentBootstrap,
		Key:                  meta[0].Key,
		Epoch:                rollout.CodeEpoch,
		Format:               rollout.Format,
		CodeSHA256:           rollout.CodeSHA256,
		RetainedRevision:     rollout.RetainedRevision,
		RetainedSHA256:       rollout.RetainedSHA256,
		SpecSHA256:           meta[0].SpecSHA256,
		DefinitionSHA256:     meta[0].DefinitionSHA256,
		PrestateSHA256:       prestateSHA,
		FromEnableState:      someText(guardStateAlways),
		ToEnableState:        guardStateAlways,
		PredecessorReceiptID: someDigest(predecessorReceiptID),
		AttemptID:            guardAccessEvidenceV9AttemptID,
	}
	if seal.ReceiptID, err = seal.bodyDigest(); err != nil {
		return guardReceipt{}, err
	}
	return seal, nil
}

func guardBootstrapRollout(m guardManifest, revision int64, retained [32]byte) (guardRolloutContext, error) {
	rolloutID, err := guardRolloutID(m.Format, m.CodeEpoch, m.CodeSHA256, revision, retained)
	if err != nil {
		return guardRolloutContext{}, err
	}
	return guardRolloutContext{
		RolloutID: rolloutID, Format: m.Format, CodeEpoch: m.CodeEpoch,
		CodeSHA256: m.CodeSHA256, RetainedRevision: revision, RetainedSHA256: retained,
	}, nil
}

func expectedGuardBootstrapReceipts(m guardManifest) ([]guardReceipt, error) {
	retained, err := emptyRetainedDigest()
	if err != nil {
		return nil, err
	}
	rollout, err := guardBootstrapRollout(m, 0, retained)
	if err != nil {
		return nil, err
	}
	specs, err := guardMetadataSpecs(m.Format)
	if err != nil {
		return nil, err
	}
	out := make([]guardReceipt, 0, len(specs))
	for _, spec := range specs {
		unitID, uerr := guardBootstrapUnitID(m.Format, spec.Key)
		if uerr != nil {
			return nil, uerr
		}
		receipt, rerr := bootstrapReceiptFor(rollout, spec, unitID)
		if rerr != nil {
			return nil, rerr
		}
		out = append(out, receipt)
	}
	return out, nil
}

func guardEditionTransitionSeal(
	edge guardManifestEdge,
	revision int64,
	retained [32]byte,
	predecessorReceiptID [32]byte,
) (guardReceipt, error) {
	meta, err := guardMetadataSpecs(edge.To.Format)
	if err != nil {
		return guardReceipt{}, err
	}
	if len(meta) == 0 || meta[0].Key.Relation != guardGateEventsTable {
		return guardReceipt{}, fmt.Errorf("sqlstore: the guard edition seal requires %s as the first fixed metadata spec",
			guardGateEventsTable)
	}
	unitID, err := guardBootstrapUnitID(edge.To.Format, meta[0].Key)
	if err != nil {
		return guardReceipt{}, err
	}
	if predecessorReceiptID == ([32]byte{}) {
		return guardReceipt{}, fmt.Errorf("sqlstore: guard edition %d->%d seal has no terminal predecessor receipt",
			edge.From.CodeEpoch, edge.To.CodeEpoch)
	}
	target, err := guardBootstrapRollout(edge.To, revision, retained)
	if err != nil {
		return guardReceipt{}, err
	}
	prestateSHA, err := prestateDigest(target.bind(prestate{
		TargetExists:          true,
		GuardPresent:          true,
		GuardEnableState:      guardStateAlways,
		GuardMatchesCanonical: true,
	}, meta[0]))
	if err != nil {
		return guardReceipt{}, err
	}
	seal := guardReceipt{
		RolloutID:            target.RolloutID,
		UnitID:               unitID,
		Kind:                 guardReceiptKindBootstrap,
		Intent:               guardIntentBootstrap,
		Key:                  meta[0].Key,
		Epoch:                edge.To.CodeEpoch,
		Format:               edge.To.Format,
		CodeSHA256:           edge.To.CodeSHA256,
		RetainedRevision:     revision,
		RetainedSHA256:       retained,
		SpecSHA256:           meta[0].SpecSHA256,
		DefinitionSHA256:     meta[0].DefinitionSHA256,
		PrestateSHA256:       prestateSHA,
		FromEnableState:      someText(guardStateAlways),
		ToEnableState:        guardStateAlways,
		PredecessorReceiptID: someDigest(predecessorReceiptID),
		AttemptID:            guardEditionSealAttemptID(edge),
	}
	if seal.ReceiptID, err = seal.bodyDigest(); err != nil {
		return guardReceipt{}, err
	}
	return seal, nil
}

func guardBootstrapReceiptSetDifference(got, want []guardReceipt) string {
	if len(got) != len(want) {
		return fmt.Sprintf("the ledger holds %d bootstrap receipts; this history requires exactly %d", len(got), len(want))
	}
	key := func(r guardReceipt) string { return r.RolloutID + "\x00" + r.UnitID + "\x00" + r.Kind }
	used := make([]bool, len(got))
	for _, expected := range want {
		match := -1
		// Exact durable identity first. This disambiguates the gate metadata unit in
		// a sealed history, where predecessor and target intentionally use one unit id.
		for i := range got {
			if !used[i] && key(got[i]) == key(expected) {
				match = i
				break
			}
		}
		// A forged rollout id is still the receipt for this unit. Pairing it here lets
		// receiptDifference name rollout_id rather than misdiagnosing two cardinality
		// defects. Valid sealed histories never reach this fallback for their duplicate.
		if match < 0 {
			for i := range got {
				if !used[i] && got[i].UnitID == expected.UnitID && got[i].Kind == expected.Kind {
					match = i
					break
				}
			}
		}
		if match < 0 {
			return fmt.Sprintf("%s has no bootstrap receipt (unit %s) in rollout %s",
				expected.Key, expected.UnitID, expected.RolloutID)
		}
		if diff := receiptDifference(got[match], expected); diff != "" {
			return fmt.Sprintf("receipt %s/%s differs: %s", expected.UnitID, expected.Kind, diff)
		}
		used[match] = true
	}
	for i, receipt := range got {
		if !used[i] {
			return fmt.Sprintf("unexpected receipt %s/%s under rollout %s",
				receipt.UnitID, receipt.Kind, receipt.RolloutID)
		}
	}
	return ""
}

func verifyGuardBootstrapReceiptClaims(receipts []guardReceipt, format int64) error {
	specs, err := guardMetadataSpecs(format)
	if err != nil {
		return err
	}
	byKey := make(map[guardKey]guardSpec, len(specs))
	for _, spec := range specs {
		byKey[spec.Key] = spec
	}
	for _, receipt := range receipts {
		spec, ok := byKey[receipt.Key]
		if !ok {
			return fmt.Errorf("%w: bootstrap receipt %s attributes unmanaged metadata target %s",
				ErrGuardBootstrapReceiptsInvalid, hexDigest(receipt.ReceiptID), receipt.Key)
		}
		if receipt.SpecSHA256 != spec.SpecSHA256 {
			return fmt.Errorf("%w: bootstrap receipt for %s records entry digest %s, want %s",
				ErrGuardBootstrapReceiptsInvalid, receipt.Key,
				hexDigest(receipt.SpecSHA256), hexDigest(spec.SpecSHA256))
		}
		if receipt.DefinitionSHA256 != spec.DefinitionSHA256 {
			return fmt.Errorf("%w: bootstrap receipt for %s records object digest %s, want %s",
				ErrGuardBootstrapReceiptsInvalid, receipt.Key,
				hexDigest(receipt.DefinitionSHA256), hexDigest(spec.DefinitionSHA256))
		}
		rollout := guardRolloutContext{
			RolloutID: receipt.RolloutID, Format: receipt.Format, CodeEpoch: receipt.Epoch,
			CodeSHA256: receipt.CodeSHA256, RetainedRevision: receipt.RetainedRevision,
			RetainedSHA256: receipt.RetainedSHA256,
		}
		prestateSHA, err := prestateDigest(rollout.bind(prestate{
			TargetExists:          true,
			GuardPresent:          true,
			GuardEnableState:      guardStateAlways,
			GuardMatchesCanonical: true,
		}, spec))
		if err != nil {
			return err
		}
		if receipt.PrestateSHA256 != prestateSHA {
			return fmt.Errorf("%w: bootstrap receipt for %s records prestate %s, want %s",
				ErrGuardBootstrapReceiptsInvalid, receipt.Key,
				hexDigest(receipt.PrestateSHA256), hexDigest(prestateSHA))
		}
		if receipt.ToEnableState != guardStateAlways {
			return fmt.Errorf("%w: bootstrap receipt for %s records to_enable_state %q, want %q",
				ErrGuardBootstrapReceiptsInvalid, receipt.Key,
				receipt.ToEnableState, guardStateAlways)
		}
	}
	return nil
}

// readGuardBootstrapReceiptHistory preserves the same unit across different rollouts. That
// is the representation the transition seal needs. Attribution is compared before chains
// are folded so an altered field remains the primary diagnosis; an accepted body set is
// followed immediately by verifyGuardReceiptRolloutChains.
func readGuardBootstrapReceiptHistory(ctx context.Context, q guardEditionQuerier, dia dialect.Dialect) ([]guardReceipt, error) {
	rows, err := q.QueryContext(ctx, dia.Rebind("SELECT "+guardReceiptColumns+" FROM "+guardOnly(dia)+guardReceiptsTable+
		" WHERE receipt_kind = ? ORDER BY rollout_id, event_ordinal"), guardReceiptKindBootstrap)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGuardReceiptLedgerUnavailable, err)
	}
	defer rows.Close()
	var out []guardReceipt
	for rows.Next() {
		receipt, err := scanGuardReceipt(rows.Scan)
		if err != nil {
			return nil, err
		}
		body, err := receipt.bodyDigest()
		if err != nil {
			return nil, err
		}
		if body != receipt.ReceiptID {
			return nil, fmt.Errorf("%w: bootstrap receipt %s stores an id its own contents do not produce (%s)",
				ErrGuardBootstrapReceiptsInvalid, hexDigest(receipt.ReceiptID), hexDigest(body))
		}
		out = append(out, receipt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGuardReceiptLedgerUnavailable, err)
	}
	return out, nil
}

func verifyGuardReceiptRolloutChains(ctx context.Context, q guardEditionQuerier, dia dialect.Dialect) error {
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT rollout_id FROM "+guardOnly(dia)+guardReceiptsTable+" ORDER BY rollout_id")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGuardReceiptLedgerUnavailable, err)
	}
	var rolloutIDs []string
	for rows.Next() {
		var rolloutID string
		if err := rows.Scan(&rolloutID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("%w: %v", ErrGuardReceiptLedgerUnavailable, err)
		}
		rolloutIDs = append(rolloutIDs, rolloutID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("%w: %v", ErrGuardReceiptLedgerUnavailable, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("%w: %v", ErrGuardReceiptLedgerUnavailable, err)
	}
	for _, rolloutID := range rolloutIDs {
		if _, err := guardRolloutReceipts(ctx, q, dia, rolloutID); err != nil {
			return fmt.Errorf("%w: bootstrap rollout %s does not verify: %w",
				ErrGuardBootstrapReceiptsInvalid, rolloutID, err)
		}
	}
	return nil
}

type guardBootstrapReceiptVariant struct {
	State             guardEditionReceiptState
	Receipts          []guardReceipt
	Path              guardEditionPath
	CompletedV7       bool
	CompletedV9       bool
	TerminalReceiptID [32]byte
	// Epoch is the edition this variant's history ENDS at. For a predecessor variant
	// that is the parent, not the edition being opened, and the caller needs the
	// difference to know which edge it is standing before.
	Epoch int64
}

func guardBootstrapTerminalReceipt(receipts []guardReceipt) ([32]byte, error) {
	for _, receipt := range receipts {
		if receipt.Key.Relation == guardGateEventsTable {
			return receipt.ReceiptID, nil
		}
	}
	return [32]byte{}, fmt.Errorf("sqlstore: bootstrap history has no %s metadata receipt",
		guardGateEventsTable)
}

// guardBootstrapVariantsAtNode enumerates the complete receipt histories a database can
// hold while standing at ONE edition, without any transition.
//
// These are the four the migrations write directly — the bootstrap alone, its universal
// v7 completion, the direct-upgrade start, and start plus completion — and, for an
// edition that carries the access-evidence delta, the two v9-sealed forms core v9
// appends on the fresh and direct paths.
func guardBootstrapVariantsAtNode(node guardEditionNode) ([]guardBootstrapReceiptVariant, error) {
	manifest := node.Manifest
	bootstrap, err := expectedGuardBootstrapReceipts(manifest)
	if err != nil {
		return nil, err
	}
	bootstrapTerminal, err := guardBootstrapTerminalReceipt(bootstrap)
	if err != nil {
		return nil, err
	}
	freshCompletion, err := guardV7Seal(manifest, guardV7SealCompletion, false)
	if err != nil {
		return nil, err
	}
	directStart, err := guardV7Seal(manifest, guardV7SealStart, false)
	if err != nil {
		return nil, err
	}
	directCompletion, err := guardV7Seal(manifest, guardV7SealCompletion, true)
	if err != nil {
		return nil, err
	}
	path := guardEditionPathOf([]guardEditionNode{{Epoch: node.Epoch}})
	with := func(base []guardReceipt, extra ...guardReceipt) []guardReceipt {
		return append(append([]guardReceipt(nil), base...), extra...)
	}
	out := []guardBootstrapReceiptVariant{
		{State: guardEditionReceiptsCurrent, Receipts: with(bootstrap), Path: path,
			TerminalReceiptID: bootstrapTerminal, Epoch: node.Epoch},
		{State: guardEditionReceiptsCurrentCompleted, Receipts: with(bootstrap, freshCompletion), Path: path,
			CompletedV7: true, TerminalReceiptID: freshCompletion.ReceiptID, Epoch: node.Epoch},
		{State: guardEditionReceiptsDirectStarted, Receipts: with(bootstrap, directStart), Path: path,
			TerminalReceiptID: directStart.ReceiptID, Epoch: node.Epoch},
		{State: guardEditionReceiptsDirectCompleted, Receipts: with(bootstrap, directStart, directCompletion), Path: path,
			CompletedV7: true, TerminalReceiptID: directCompletion.ReceiptID, Epoch: node.Epoch},
	}
	if !node.Membership.has(guardDeltaAccessEvidence) {
		return out, nil
	}
	// The v9 completion seal exists only on the two paths that have no DA edge to seal:
	// a fresh database, whose v2 already created the relations, and a direct <=v5
	// upgrade, whose v6 bootstrap already declared this edition. A guarded transition
	// gets its immutable witness from its edge seal instead and needs no second one.
	freshV9, err := guardAccessEvidenceV9Seal(manifest, freshCompletion.ReceiptID, false)
	if err != nil {
		return nil, err
	}
	directV9, err := guardAccessEvidenceV9Seal(manifest, directCompletion.ReceiptID, true)
	if err != nil {
		return nil, err
	}
	out = append(out,
		guardBootstrapReceiptVariant{
			State: guardEditionReceiptsCurrentV9Completed, Receipts: with(bootstrap, freshCompletion, freshV9),
			Path: path, CompletedV7: true, CompletedV9: true,
			TerminalReceiptID: freshV9.ReceiptID, Epoch: node.Epoch,
		},
		guardBootstrapReceiptVariant{
			State: guardEditionReceiptsDirectV9Completed, Receipts: with(bootstrap, directStart, directCompletion, directV9),
			Path: path, CompletedV7: true, CompletedV9: true,
			TerminalReceiptID: directV9.ReceiptID, Epoch: node.Epoch,
		},
	)
	return out, nil
}

// guardEditionSealEligible decides whether a source history may carry ONE more edge.
//
// The three answers are different questions and were one before the DAG. The directory
// edge into epoch 2 is crossed INSIDE core v7, so its source is still at the plain
// bootstrap. Every other edge needs a completed v7. And an edge leaving an edition that
// already carries the access-evidence delta additionally needs a completed v9: without
// it, a database whose v9 transaction never committed could be walked forward as though
// it had.
func guardEditionSealEligible(edge guardManifestEdge, source guardBootstrapReceiptVariant) bool {
	if edge.To.CodeEpoch == 2 {
		return source.State == guardEditionReceiptsCurrent
	}
	if membership, ok := guardEditionMembershipForEpoch(edge.From.CodeEpoch); ok &&
		membership.has(guardDeltaAccessEvidence) {
		return source.CompletedV9
	}
	return source.CompletedV7
}

// guardBootstrapReceiptVariants enumerates complete receipt histories, never individual
// receipt options. A later edge can therefore link its seal to the real terminal claim of
// a fresh completion, direct completion, v9 completion or prior transition without
// synthesizing an old bootstrap receipt that was not terminal on that path.
//
// The enumeration follows LINEAGES: one seal per crossed edge, in the order the edges
// were crossed. Two lineages between the same endpoints therefore produce different
// receipt sets, because each seal binds its own exact From/To pair and its predecessor's
// terminal receipt id.
func guardBootstrapReceiptVariants(current guardManifest) ([]guardBootstrapReceiptVariant, error) {
	graph, err := guardEditionGraphFor(current)
	if err != nil {
		return nil, err
	}
	return guardBootstrapVariantsForNode(graph, current.CodeEpoch)
}

func guardBootstrapVariantsForNode(graph guardEditionGraph, epoch int64) ([]guardBootstrapReceiptVariant, error) {
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
	for _, parent := range graph.parentEpochs(epoch) {
		edge, eerr := graph.edge(parent, epoch)
		if eerr != nil {
			return nil, eerr
		}
		sources, serr := guardBootstrapVariantsForNode(graph, parent)
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

func classifyGuardBootstrapReceiptVariant(
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
	variants, err := guardBootstrapVariantsForNode(graph, current.CodeEpoch)
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
		sources, serr := guardBootstrapVariantsForNode(graph, parent)
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

func verifyGuardBootstrapReceiptHistory(
	ctx context.Context,
	q guardEditionQuerier,
	dia dialect.Dialect,
	current guardManifest,
) (guardEditionReceiptState, []guardReceipt, error) {
	variant, actual, err := classifyGuardBootstrapReceiptVariant(ctx, q, dia, current)
	if err != nil {
		return "", nil, err
	}
	return variant.State, actual, nil
}

// readGuardBootstrapReceipts reads every bootstrap receipt in the ledger, keyed by unit.
//
// ACROSS ROLLOUTS, not within one, and that is deliberate rather than lax. The bootstrap
// receipts are written under the rollout of the edition that ran the migration; a later
// edition has a different rollout id and does NOT rewrite them. Scoping this query to the
// current rollout would therefore turn every future edition's first boot into a refusal — and
// the binding to a rollout is not lost, because the caller derives the expected rollout id and
// compares it as one of the fields.
//
// Each row's id is recomputed from its own body. A receipt whose stored id its contents do not
// produce is a row somebody wrote by hand, and it is refused before any field of it is
// compared with anything.
func readGuardBootstrapReceipts(ctx context.Context, q rowQuerier, dia dialect.Dialect) (map[string]guardReceipt, error) {
	rows, err := q.QueryContext(ctx, dia.Rebind("SELECT "+guardReceiptColumns+" FROM "+guardOnly(dia)+guardReceiptsTable+
		" WHERE receipt_kind = ? ORDER BY rollout_id, event_ordinal"), guardReceiptKindBootstrap)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGuardReceiptLedgerUnavailable, err)
	}
	defer rows.Close()
	out := map[string]guardReceipt{}
	for rows.Next() {
		r, serr := scanGuardReceipt(rows.Scan)
		if serr != nil {
			return nil, serr
		}
		body, berr := r.bodyDigest()
		if berr != nil {
			return nil, berr
		}
		if body != r.ReceiptID {
			return nil, fmt.Errorf("%w: bootstrap receipt %s stores an id its own contents do not produce (%s)",
				ErrGuardGateChainBroken, hexDigest(r.ReceiptID), hexDigest(body))
		}
		if prev, dup := out[r.UnitID]; dup {
			return nil, fmt.Errorf("%w: unit %s has two bootstrap receipts, under rollouts %s and %s",
				ErrGuardBootstrapReceiptsInvalid, r.UnitID, prev.RolloutID, r.RolloutID)
		}
		out[r.UnitID] = r
	}
	return out, rows.Err()
}
