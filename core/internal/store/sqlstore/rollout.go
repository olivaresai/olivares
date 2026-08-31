// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// Durable rollout state — the engine side of core/store/rollout.go.
//
// Four properties carry the design, and each one is a lesson this campaign paid
// for:
//
//  1. The classification happens ONCE, from the only moment the answer is free,
//     and is never re-derived. A later boot that re-asked the database would let a
//     deploy change the rule a live estate is running under — which is the failure
//     the unit-D blinding record exists to prevent (see resolveBlindingMode).
//
//  2. The DDL and the seed commit in ONE transaction. Unit D did not: it added the
//     column in one autocommit statement and seeded the state in another, so a crash
//     between them left a ledger the NEXT boot classified as fresh. That window is
//     fixed there too, in this same change.
//
//  3. CONTRADICTORY evidence fails the boot rather than being guessed at. Witness
//     presence is conservative evidence of lineage, not an oracle: a partial restore
//     can present a witness with no tracker entry, or a tracker claiming the witness
//     is applied while the table is gone. Guessing is worse in both directions — one
//     way invents compatibility debt, the other silently deletes it — and the
//     classification is deliberately never revisited, so a wrong guess is permanent.
//
//  4. The table is GLOBAL and UN-GUARDED, exactly like audit_spool_usage and
//     audit_meta_blinding. It is bookkeeping about the deployment, not tenant data
//     and not evidence, so it carries neither row-level security nor an append-only
//     trigger — and that is what lets it be read from the application pool with no
//     tenant bound. The alternative, inferring a deployment-wide fact from a tenant
//     table, does not merely offend layering: under FORCE row-level security a
//     cross-tenant read RAISES rather than returning zero rows, which once broke
//     every Postgres boot in this repository (see the comment on reconcileCoreData).
//
// Both tables carry CHECK constraints on every closed-vocabulary column. They are
// not decoration: this is a hand-provisionable database whose rows are read at boot
// and acted on, and a scanner that quietly accepted "any nonzero integer is true"
// or a mode string it did not recognize would interpret corruption instead of
// refusing it.

const rolloutStateDDL = "CREATE TABLE IF NOT EXISTS " + dialect.ControlRolloutStateTable + ` (
	control_key TEXT PRIMARY KEY,
	classified_mode TEXT NOT NULL CHECK (classified_mode IN ('enforced','legacy_compat')),
	current_mode TEXT NOT NULL CHECK (current_mode IN ('enforced','legacy_compat','policy_optional')),
	enforcement_committed INTEGER NOT NULL CHECK (enforcement_committed IN (0,1)),
	generation INTEGER NOT NULL CHECK (generation > 0),
	classified_at TEXT NOT NULL,
	witness_kind TEXT NOT NULL,
	witness_detail TEXT NOT NULL,
	decided_at TEXT,
	decided_by TEXT,
	decided_reason TEXT,
	CHECK ((decided_at IS NULL AND decided_by IS NULL) OR (decided_at IS NOT NULL AND decided_by IS NOT NULL))
)`

const rolloutTransitionDDL = "CREATE TABLE IF NOT EXISTS " + dialect.ControlRolloutTransitionTable + ` (
	control_key TEXT NOT NULL,
	generation INTEGER NOT NULL CHECK (generation > 0),
	from_mode TEXT NOT NULL CHECK (from_mode IN ('enforced','legacy_compat','policy_optional')),
	to_mode TEXT NOT NULL CHECK (to_mode IN ('enforced','policy_optional')),
	committed INTEGER NOT NULL CHECK (committed IN (0,1)),
	decided_at TEXT NOT NULL,
	decided_by TEXT NOT NULL,
	decided_reason TEXT NOT NULL,
	evidence TEXT NOT NULL,
	PRIMARY KEY (control_key, generation)
)`

// rolloutClassificationDDL creates the classification RECEIPT relation.
//
// It is a NEW relation rather than a widening of the transition history, and the reason is
// in that history's own CHECK constraints: `generation > 0` and
// `to_mode IN ('enforced','policy_optional')`. A generation-zero receipt whose mode may be
// legacy_compat does not fit either, and relaxing them would weaken the guarantees the
// transition log exists to make about DELIBERATE decisions. A classification is not a
// decision — it is the observation a decision would later be made against — so it gets its
// own relation instead of being squeezed into one that means something else.
//
// One row per control, PRIMARY KEY on the key: the shape itself refuses a second
// classification, so "classified twice" is not a state this table can hold.
const rolloutClassificationDDL = "CREATE TABLE IF NOT EXISTS " + dialect.ControlRolloutClassificationTable + ` (
	control_key TEXT PRIMARY KEY,
	classified_mode TEXT NOT NULL CHECK (classified_mode IN ('enforced','legacy_compat')),
	classified_at TEXT NOT NULL,
	witness_kind TEXT NOT NULL,
	witness_detail TEXT NOT NULL
)`

// Witness kinds. The kind is recorded next to the detail so a future release can
// add a stronger classifier without a stored row becoming ambiguous about which one
// produced it.
const (
	witnessKindTablePresence = "module_table_presence.v1"
)

// classifyRolloutControls creates the rollout tables and seeds one row per
// registered control, in a single transaction, BEFORE any schema this boot is about
// to create exists.
//
// Ordering is the load-bearing part. The witness for the egress control is
// eventing_subscription, and applyModuleTables would create it a few statements
// later in this same lock — so a classification that ran afterwards would observe
// the table it had just created and call every fresh install an upgrade.
//
// An EXISTING row is validated and left alone; it is never restated and never
// re-derived, including — especially — when the witness table now exists because an
// earlier boot created it. A row that fails validation fails the boot: this is the
// record every later decision rests on, and a caller cannot be given a disposition
// the engine does not itself believe.
func classifyRolloutControls(ctx context.Context, mdb dialect.Execer, dia dialect.Dialect, controls []store.RolloutControl) error {
	// The tables are created even when NO control is declared, so the schema does not
	// depend on which modules a build happens to enable. Without that, reading the
	// disposition of a control this binary does not carry fails with "no such table" —
	// an engine-level error where the honest answer is store.ErrNotFound, and the
	// difference matters because ErrNotFound is the reading a caller must convert into
	// "this plane cannot say" rather than into a permit.
	tx, err := mdb.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlstore: rollout classification: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, ddl := range []string{rolloutStateDDL, rolloutTransitionDDL, rolloutClassificationDDL} {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("sqlstore: reconcile rollout tables: %w", err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	sel := dia.Rebind(rolloutSelect + " WHERE control_key = ?")
	ins := dia.Rebind("INSERT INTO " + dialect.ControlRolloutStateTable +
		" (control_key, classified_mode, current_mode, enforcement_committed, generation," +
		" classified_at, witness_kind, witness_detail) VALUES (?, ?, ?, 0, 1, ?, ?, ?)")

	for _, c := range controls {
		existing, err := scanRolloutState(tx.QueryRowContext(ctx, sel, c.Key))
		switch {
		case err == nil:
			// Already classified. Validate rather than trust: a hand-edited or partially
			// restored row must not be silently interpreted, and an ON CONFLICT DO NOTHING
			// insert would have hidden exactly that.
			if verr := validateRolloutRow(existing, c); verr != nil {
				return verr
			}
			// The state row must also agree with the history. A generation below the highest
			// recorded transition means the state was rolled back or replaced while the history
			// survived, and a state that claims transitions the history does not have means the
			// history was rewritten. Either way the record is not the one the decisions were made
			// against, and this is the ONLY place that can notice.
			if verr := reconcileRolloutHistory(ctx, tx, dia, c.Key, existing); verr != nil {
				return verr
			}
			// AND THE RECEIPT IS BACKFILLED HERE, which is what makes the new guard below
			// mean anything on a database that already exists.
			//
			// Every deployment provisioned before this relation existed has a state row and
			// no receipt. Without this leg the guard would only protect installs classified
			// from this edition onward — i.e. it would be armed exactly where the risk is
			// lowest and absent everywhere the history is long enough to have lost a row.
			// The backfill copies what the surviving state row already attests; it invents
			// nothing, and it is a no-op from the second boot on.
			if rerr := ensureClassificationReceipt(ctx, tx, dia, c.Key,
				string(existing.ClassifiedMode), existing.ClassifiedAt.UTC().Format(time.RFC3339Nano),
				existing.WitnessKind, existing.WitnessDetail); rerr != nil {
				return rerr
			}
			continue
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		// No state row. Before treating that as a FIRST ENCOUNTER, ask whether this control has
		// a decision history — because if it does, the row did not fail to be written, it was
		// LOST.
		//
		// This is the hole an adversarial review of this very unit found, and it was critical:
		// after any successful boot the witness table exists and is tracked, so a database that
		// lost only this row would be re-classified `legacy_compat` at generation 1 with the
		// commitment cleared, and every grandfathered destination an operator had deliberately
		// retired would start working again — with no transition, no record, and a green boot.
		// "Classified once and never re-derived" was false for exactly this shape.
		// THE RECEIPT ANSWERS THE SAME QUESTION FOR THE HALF THE HISTORY CANNOT REACH.
		//
		// The history check below only fires for a control somebody had already
		// TRANSITIONED. A control still in its original disposition has no transitions —
		// which is every control of every fresh install — so losing its state row read as
		// "first encounter", and by then the witness table exists because an earlier boot
		// created it, so the re-derivation lands on the LEGACY mode with the commitment
		// cleared. The row it writes is self-consistent, so the flip is permanent and every
		// later boot is green. Measured end to end: BOOT1 enforced/virgin, DELETE, BOOT2
		// legacy_compat/present:tracked, green, BOOT3 stable.
		//
		// This is the same refusal as the one below, on the evidence that covers the other
		// half of the population. Both are kept: they fail for different reasons and an
		// operator who lost one relation should be told which.
		classified, cerr := classificationReceiptExists(ctx, tx, dia, c.Key)
		if cerr != nil {
			return cerr
		}
		if classified {
			return fmt.Errorf("sqlstore: refusing to classify rollout control %q: it has no state row but %s holds its classification receipt. The state was LOST, not never written — re-classifying now would observe a witness table that only exists because an earlier boot created it, and would silently move this control to its legacy disposition with the enforcement commitment cleared. Restore %s from the same backup as %s, or delete this control's receipt deliberately if the deployment really is new",
				c.Key, dialect.ControlRolloutClassificationTable,
				dialect.ControlRolloutStateTable, dialect.ControlRolloutClassificationTable)
		}
		hist, herr := rolloutHistoryHigh(ctx, tx, dia, c.Key)
		if herr != nil {
			return herr
		}
		if hist > 0 {
			return fmt.Errorf("sqlstore: refusing to classify rollout control %q: it has no state row but its decision history records %d transition(s). The state was lost, not never written — classifying afresh would silently undo a one-way decision. Restore %s, or delete this control's history deliberately if the deployment really is new",
				c.Key, hist, dialect.ControlRolloutStateTable)
		}
		// A genuine first encounter. Observe, corroborate, and record — all inside this
		// transaction, so the observation and the row it justifies cannot be separated by a
		// crash.
		present, err := witnessPresent(ctx, tx, dia, c.WitnessTable)
		if err != nil {
			return fmt.Errorf("sqlstore: rollout classification: probe %q for control %q: %w", c.WitnessTable, c.Key, err)
		}
		detail, cerr := corroborateWitness(ctx, tx, dia, c, present)
		if cerr != nil {
			return cerr
		}
		// Presence, deliberately, and not "presence AND holds a row". The question is
		// whether an entitlement COULD have been authored here without the gate, and an
		// estate that created a subscription, deleted it, and kept the rule its operator
		// wrote is still an estate the control must not surprise. It is also the
		// predicate this repository has already got wrong the other way: unit D shipped an
		// oracle defined by non-emptiness and it was both wrong and, over a FORCE-RLS
		// table, fatal to every Postgres boot.
		mode := c.FreshMode
		if present {
			mode = c.LegacyMode
		}
		if _, err := tx.ExecContext(ctx, ins, c.Key, string(mode), string(mode), now, witnessKindTablePresence, detail); err != nil {
			return fmt.Errorf("sqlstore: seed rollout control %q: %w", c.Key, err)
		}
		// THE RECEIPT, IN THE SAME TRANSACTION AS THE ROW IT JUSTIFIES. Written here and not
		// afterwards for the reason the surrounding comment already gives about the
		// observation and the row: two writes that can be separated by a crash are two facts
		// that can disagree, and a receipt without a state row is exactly the shape this
		// whole guard reads as "the state was lost".
		if rerr := ensureClassificationReceipt(ctx, tx, dia, c.Key, string(mode), now, witnessKindTablePresence, detail); rerr != nil {
			return rerr
		}
		// Announced once, on the boot that decides, because this is the moment the
		// deployment acquires a disposition it will keep. A silent seed would make the
		// most consequential decision in the control's life the only one with no trace
		// outside the row itself.
		slog.Info("store: rollout control classified for this deployment",
			"control", c.Key, "mode", string(mode), "witness", detail)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlstore: rollout classification: commit: %w", err)
	}
	return nil
}

// validateRolloutRow refuses a stored row this engine cannot believe.
//
// It checks the closed vocabularies (which the CHECK constraints also enforce, but
// only for rows this engine wrote — a database restored from elsewhere, or created
// before the constraints existed, has no such guarantee) and the two coherence
// rules the constraints cannot express: that the classification matches what THIS
// declaration says the two classifiable modes are, and that a retired control is
// not sitting in a mode retirement forbids.
func validateRolloutRow(st store.RolloutState, c store.RolloutControl) error {
	if !st.ClassifiedMode.ValidClassification() {
		return fmt.Errorf("sqlstore: rollout control %q holds classified mode %q, which is not a mode the engine classifies into; this row was not written by this engine", c.Key, st.ClassifiedMode)
	}
	if st.ClassifiedMode != c.FreshMode && st.ClassifiedMode != c.LegacyMode {
		return fmt.Errorf("sqlstore: rollout control %q was classified %q, which is neither of the modes this build declares for it (%q, %q); the control's meaning changed without its key changing", c.Key, st.ClassifiedMode, c.FreshMode, c.LegacyMode)
	}
	if !st.CurrentMode.Valid() {
		return fmt.Errorf("sqlstore: rollout control %q holds current mode %q, which this binary does not know", c.Key, st.CurrentMode)
	}
	if st.CurrentMode == store.RolloutLegacyCompat && st.EnforcementCommitted {
		return fmt.Errorf("sqlstore: rollout control %q is marked enforcement-committed but sits in %q; the row is internally inconsistent", c.Key, store.RolloutLegacyCompat)
	}
	// Compatibility is only ever ARRIVED AT by classification, never by a transition, so a row
	// sitting in it must be the row the classification wrote: same mode, first generation. A
	// hand-edited or partially restored row that says otherwise is claiming a history the state
	// machine cannot produce, and accepting it would serve compatibility — with its whole
	// grandfathered set — to a deployment that was never classified into it.
	if st.CurrentMode == store.RolloutLegacyCompat {
		if st.ClassifiedMode != store.RolloutLegacyCompat {
			return fmt.Errorf("sqlstore: rollout control %q sits in %q but was classified %q; no transition can enter compatibility mode, so this row cannot have been produced by this engine", c.Key, store.RolloutLegacyCompat, st.ClassifiedMode)
		}
		if st.Generation != 1 || !st.DecidedAt.IsZero() {
			return fmt.Errorf("sqlstore: rollout control %q sits in %q at generation %d with a recorded decision; compatibility is left once and never re-entered", c.Key, store.RolloutLegacyCompat, st.Generation)
		}
	}
	// And a mode that only a DECISION can produce must carry one.
	if st.CurrentMode == store.RolloutPolicyOptional && st.DecidedAt.IsZero() {
		return fmt.Errorf("sqlstore: rollout control %q sits in %q with no recorded decision; the engine never classifies into that mode, so the row is inconsistent", c.Key, store.RolloutPolicyOptional)
	}
	if st.EnforcementCommitted && st.CurrentMode == store.RolloutPolicyOptional {
		return fmt.Errorf("sqlstore: rollout control %q is marked enforcement-committed but sits in %q, which that commitment forbids; the row is internally inconsistent", c.Key, store.RolloutPolicyOptional)
	}
	if st.Generation < 1 {
		return fmt.Errorf("sqlstore: rollout control %q holds generation %d", c.Key, st.Generation)
	}
	if st.ClassifiedAt.IsZero() {
		return fmt.Errorf("sqlstore: rollout control %q has no classification timestamp", c.Key)
	}
	return nil
}

// witnessPresent reports whether the witness table exists in the ENGINE'S schema.
//
// It goes through the dialect's introspection, which this repository pins to the
// fixed engine schema rather than to whatever the search path resolves to — the
// engine's tables live in a literal named schema (dialect.EngineSchema) and every
// other catalog check in this package binds it. A non-existent table yields an
// empty column set on both engines rather than an error, which is what makes this a
// portable existence test rather than a per-engine catalog query.
func witnessPresent(ctx context.Context, q dialect.Querier, dia dialect.Dialect, table string) (bool, error) {
	cols, err := dia.TableColumns(ctx, q, table)
	if err != nil {
		return false, err
	}
	return len(cols) > 0, nil
}

// corroborateWitness cross-checks the witness observation against the rest of the
// schema and returns the detail to record — or an error, when the evidence
// contradicts itself.
//
// This is the part an earlier draft of this design got wrong by calling table
// presence "ground truth". It is not: a restore can produce a database whose parts
// disagree, and the two disagreements below are exactly the ones that would
// otherwise be resolved by inventing history.
//
//   - The witness is ABSENT but the module tracker says it was applied. Something
//     removed the table, or the dump omitted it. Classifying "fresh" here would
//     silently DELETE this deployment's compatibility debt: every destination it was
//     using stops being grandfathered, with no record that anything was decided.
//
//   - The witness is PRESENT but the tracker does not know it. Module tables and
//     their tracker rows commit together, so this shape is restore damage rather
//     than a normal upgrade — and it is also the shape a later boot would trip over
//     when ordinary module creation finds the table already there.
//
// A pre-tracker release is the one benign case of the second shape, so it is
// distinguished: if the tracker table itself does not exist yet, there is nothing to
// disagree with and the witness stands alone.
func corroborateWitness(ctx context.Context, q dialect.Querier, dia dialect.Dialect, c store.RolloutControl, present bool) (string, error) {
	trackerExists, err := witnessPresent(ctx, q, dia, moduleTablesTracking)
	if err != nil {
		return "", fmt.Errorf("sqlstore: rollout classification: probe %q: %w", moduleTablesTracking, err)
	}
	detail := "table:" + c.WitnessTable
	if present {
		detail += ":present"
	} else {
		detail += ":absent"
	}
	if !trackerExists {
		// The module tracker is absent, which means one of two very different things: this
		// database predates the tracker, or it is brand new. An earlier revision recorded
		// ":no-tracker" and classified on the witness alone, which is a guess in the shape
		// this unit's own design says must fail — so it corroborates against what IS there.
		coreHistory, cerr := witnessPresent(ctx, q, dia, coreTrackingTable)
		if cerr != nil {
			return "", fmt.Errorf("sqlstore: rollout classification: probe %q: %w", coreTrackingTable, cerr)
		}
		siblings, serr := siblingTablesPresent(ctx, q, dia, c)
		if serr != nil {
			return "", serr
		}
		switch {
		case !present && siblings != "":
			// The witness is gone but its module's other tables are here. A module's tables are
			// created together, so this is a restore that dropped one of them — and classifying
			// "fresh" would silently retire every entitlement the deployment had.
			return "", fmt.Errorf("sqlstore: refusing to classify rollout control %q: %q is absent but %s is present, so this module ran here and its witness was lost rather than never created. Restore it, or resolve the contradiction deliberately",
				c.Key, c.WitnessTable, siblings)
		case !present && coreHistory:
			// Core history with no module tracker and no sibling of this module: a database from a
			// release that predates the tracker, on which this module never ran. Fresh, and the
			// record says which evidence said so.
			return detail + ":pre-tracker-core", nil
		case !present:
			// Nothing at all: a genuinely new database.
			return detail + ":virgin", nil
		default:
			// The witness is here and the tracker is not — a pre-tracker release that ran this
			// module. Legacy, corroborated by the witness's own siblings where they exist.
			return detail + ":pre-tracker", nil
		}
	}
	tracked, err := moduleTableTracked(ctx, q, dia, c.WitnessTable)
	if err != nil {
		return "", err
	}
	switch {
	case present && tracked:
		return detail + ":tracked", nil
	case !present && !tracked:
		return detail + ":untracked", nil
	case !present && tracked:
		return "", fmt.Errorf("sqlstore: refusing to classify rollout control %q: the module tracker says %q was applied but the table is not there. Classifying this deployment as new would silently retire every entitlement it already had. Restore the table, or resolve the contradiction deliberately", c.Key, c.WitnessTable)
	default: // present && !tracked
		return "", fmt.Errorf("sqlstore: refusing to classify rollout control %q: %q exists but the module tracker does not record it as applied. Module tables and their tracking rows are created together, so this is restore or out-of-band damage rather than an upgrade; resolve it deliberately", c.Key, c.WitnessTable)
	}
}

// rolloutHistoryHigh returns the highest generation recorded in the decision history for a
// control, or 0 when it has none.
//
// It is the evidence that distinguishes "never classified" from "the state row was lost". The
// history table is created by the same transaction that would seed the state, so its absence is
// not a case this has to handle: by the time this runs, both tables exist.
// reconcileRolloutEvidenceGuards puts the append-only guard on the relations that record
// one-way decisions, on every boot.
//
// WHY IT IS NOT IN THE CREATION DDL, which is where every other guard lives: these
// relations exist on every deployment already, and their creation statement will never run
// again there. A guard emitted only at creation would arrive on new installs and never on
// the ones with a history long enough to have decisions worth protecting. The sweep that
// found this shelved the fix on the grounds that "it is one-shot DDL, it would not
// converge" — which is false: rollout.go's DDL loop re-runs under the migration lock on
// every boot, and so does this.
//
// WHY IT RUNS HERE AND NOT IN classifyRolloutControls: the trigger function
// olivares_block_mutation is created by the core schema, which migrate.Apply installs, and
// classification runs BEFORE that on purpose (it must observe the witness table before
// applyModuleTables creates it). On a fresh install the function does not exist yet at that
// point, so the CREATE TRIGGER would fail on the very first boot.
//
// control_rollout_state is deliberately ABSENT from this list. It takes UPDATEs in
// production — a transition rewrites the current mode — so guarding it would refuse the
// operation it exists to record. The two properties that were fused in one sentence for a
// long time ("un-guarded for the same reason as the state table") are separated here in
// code as well as in prose: no RLS, for the reason the state table gives; an immutability
// trigger, because this IS evidence.
func reconcileRolloutEvidenceGuards(ctx context.Context, mdb dialect.Execer, dia dialect.Dialect) error {
	tables := []string{
		dialect.ControlRolloutTransitionTable,
		dialect.ControlRolloutClassificationTable,
	}
	if dia.Name() == store.EnginePostgres {
		// The scope inventory is a Postgres-only relation (SQLite has no role layer and no
		// catalog-derived scope), and an inventory of what must not be erased is itself
		// something that must not be erased.
		if err := ensureAppendOnlyScopeTable(ctx, mdb, dia); err != nil {
			return err
		}
		tables = append(tables, dialect.ControlAppendOnlyScopeTable)
	}
	for _, t := range tables {
		for _, stmt := range dia.AppendOnlyGuardStmts(t) {
			if _, err := mdb.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("sqlstore: append-only guard for %q: %w", t, err)
			}
		}
	}
	return nil
}

// classificationReceiptExists reports whether this control was EVER classified on this
// database, independently of whether its state row survived.
//
// That independence is the whole point: every other durable fact about a classification
// lives in the row the guard is trying to decide about, so a lost row took the evidence of
// its own existence with it.
func classificationReceiptExists(ctx context.Context, q dialect.Querier, dia dialect.Dialect, key string) (bool, error) {
	rows, err := q.QueryContext(ctx, dia.Rebind(
		"SELECT 1 FROM "+dialect.ControlRolloutClassificationTable+" WHERE control_key = ?"), key)
	if err != nil {
		return false, fmt.Errorf("sqlstore: read rollout classification receipt for %q: %w", key, err)
	}
	defer rows.Close()
	found := rows.Next()
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("sqlstore: read rollout classification receipt for %q: %w", key, err)
	}
	return found, nil
}

// ensureClassificationReceipt writes the receipt if it is absent and leaves it alone if it
// is present.
//
// It is deliberately NOT an upsert-with-update. The receipt is append-only — it carries its
// own immutability guard from reconcileRolloutEvidenceGuards — so an UPDATE would be
// refused by the database anyway, and writing one here would encode the belief that a
// classification can be restated. It cannot: that is the property the whole record exists
// to hold.
//
// The absence check and the insert are in the caller's transaction, which is the one that
// holds the migration lock, so there is no window for a second boot to interleave.
func ensureClassificationReceipt(ctx context.Context, tx dialect.Querier, dia dialect.Dialect,
	key, mode, classifiedAt, witnessKind, witnessDetail string) error {
	present, err := classificationReceiptExists(ctx, tx, dia, key)
	if err != nil {
		return err
	}
	if present {
		return nil
	}
	if _, err := tx.ExecContext(ctx, dia.Rebind(
		"INSERT INTO "+dialect.ControlRolloutClassificationTable+
			" (control_key, classified_mode, classified_at, witness_kind, witness_detail) VALUES (?, ?, ?, ?, ?)"),
		key, mode, classifiedAt, witnessKind, witnessDetail); err != nil {
		return fmt.Errorf("sqlstore: record the classification receipt for %q: %w", key, err)
	}
	return nil
}

func rolloutHistoryHigh(ctx context.Context, q dialect.Querier, dia dialect.Dialect, key string) (int64, error) {
	rows, err := q.QueryContext(ctx, dia.Rebind(
		"SELECT generation FROM "+dialect.ControlRolloutTransitionTable+
			" WHERE control_key = ? ORDER BY generation DESC"), key)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: read rollout history for %q: %w", key, err)
	}
	defer rows.Close()
	var high int64
	if rows.Next() {
		if err := rows.Scan(&high); err != nil {
			return 0, fmt.Errorf("sqlstore: read rollout history for %q: %w", key, err)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("sqlstore: read rollout history for %q: %w", key, err)
	}
	return high, nil
}

// reconcileRolloutHistory refuses a state row that disagrees with its own decision history.
//
// The two tables are written in one transaction by every path this engine has, so on a healthy
// database the state's generation is exactly the highest recorded transition — or 1 with no
// history at all, for a classification nobody has decided anything about yet. Anything else means
// one of the two was replaced or edited independently.
//
// This check is the substitute for an append-only trigger on the history, and it is worth being
// exact about what it does and does not give: it detects a history that was TRUNCATED or a state
// that was ROLLED BACK, because those break the relationship. It does not detect an edit that
// changes a recorded reason or actor while preserving the generations. The history is therefore
// append-only by CONVENTION and by this cross-check, not by a database guarantee, and the claim
// elsewhere that it is append-only was overstated until this said so.
func reconcileRolloutHistory(ctx context.Context, q dialect.Querier, dia dialect.Dialect, key string, st store.RolloutState) error {
	high, err := rolloutHistoryHigh(ctx, q, dia, key)
	if err != nil {
		return err
	}
	switch {
	case high == 0 && st.Generation == 1 && st.DecidedAt.IsZero():
		return nil // classified, never decided
	case high == st.Generation:
		return nil
	case high > st.Generation:
		return fmt.Errorf("sqlstore: refusing to use rollout control %q: its state is at generation %d but its decision history records generation %d. The state was rolled back or replaced while the history survived, so it is not the record the decisions were made against",
			key, st.Generation, high)
	default:
		return fmt.Errorf("sqlstore: refusing to use rollout control %q: its state is at generation %d but its decision history only reaches %d. The history was truncated or rewritten, so this control's decisions can no longer be accounted for",
			key, st.Generation, high)
	}
}

// siblingTablesPresent reports a table belonging to the witness's module OTHER than the witness
// itself, or "" when none is present.
//
// The module's namespace is the witness table's leading segment by this repository's naming rule
// (docs/contracts: a module table is "<namespace>_<entity>"), so a sibling is any registered
// module table sharing that prefix. It is evidence about the same module's history, which is exactly
// what makes it able to contradict the witness.
func siblingTablesPresent(ctx context.Context, q dialect.Querier, dia dialect.Dialect, c store.RolloutControl) (string, error) {
	prefix := c.WitnessTable
	if i := strings.IndexByte(prefix, '_'); i > 0 {
		prefix = prefix[:i+1]
	}
	for _, candidate := range siblingProbeSuffixes {
		table := prefix + candidate
		if table == c.WitnessTable {
			continue
		}
		present, err := witnessPresent(ctx, q, dia, table)
		if err != nil {
			return "", fmt.Errorf("sqlstore: rollout classification: probe %q: %w", table, err)
		}
		if present {
			return table, nil
		}
	}
	return "", nil
}

// siblingProbeSuffixes are the entity names a module of this repository is overwhelmingly likely to
// have if it has any table at all. The list is a HEURISTIC and deliberately additive: a missing
// suffix means a contradiction goes unnoticed and the classification falls back to the witness
// alone, which is the direction that was already shipping. It is not a claim of completeness.
var siblingProbeSuffixes = []string{"event", "delivery", "cursor", "revision", "run", "result"}

// moduleTableTracked reports whether the module tracker records this table.
func moduleTableTracked(ctx context.Context, q dialect.Querier, dia dialect.Dialect, table string) (bool, error) {
	rows, err := q.QueryContext(ctx, dia.Rebind("SELECT 1 FROM "+moduleTablesTracking+" WHERE table_name = ?"), table)
	if err != nil {
		return false, fmt.Errorf("sqlstore: rollout classification: read %q: %w", moduleTablesTracking, err)
	}
	defer rows.Close()
	found := rows.Next()
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("sqlstore: rollout classification: read %q: %w", moduleTablesTracking, err)
	}
	return found, nil
}

// Compile-time proof the store exposes the rollout capability the composition root
// forwards to the modules that own staged controls.
var _ store.RolloutStater = (*sqlStore)(nil)

const rolloutSelect = "SELECT control_key, classified_mode, current_mode, enforcement_committed," +
	" generation, classified_at, witness_kind, witness_detail, decided_at, decided_by, decided_reason" +
	" FROM " + dialect.ControlRolloutStateTable

// RolloutState reads a control's durable state.
//
// A missing row returns store.ErrNotFound and the caller must treat that as
// UNAVAILABLE rather than as permissive. For a registered control the row cannot be
// missing on a healthy database — the classification seeds it under the migration
// lock — so its absence means either that this binary carries a control the database
// was never classified for, or that the row was deleted. Both are conditions where
// the honest answer is "this plane cannot say", and the one reading that must never
// be produced is "then everything is allowed".
func (s *sqlStore) RolloutState(ctx context.Context, key string) (store.RolloutState, error) {
	return scanRolloutState(s.db.QueryRowContext(ctx, s.dia.Rebind(rolloutSelect+" WHERE control_key = ?"), key))
}

func scanRolloutState(row *sql.Row) (store.RolloutState, error) {
	var (
		st                             store.RolloutState
		classified, current            string
		committed, generation          int64
		classifiedAt                   string
		decidedAt, decidedBy, decidedR sql.NullString
	)
	if err := row.Scan(&st.Key, &classified, &current, &committed, &generation,
		&classifiedAt, &st.WitnessKind, &st.WitnessDetail, &decidedAt, &decidedBy, &decidedR); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.RolloutState{}, store.ErrNotFound
		}
		return store.RolloutState{}, fmt.Errorf("sqlstore: read rollout state: %w", err)
	}
	st.ClassifiedMode = store.RolloutMode(classified)
	st.CurrentMode = store.RolloutMode(current)
	st.EnforcementCommitted = committed != 0
	st.Generation = generation
	// A timestamp that does not parse is an ERROR and not a zero value. Silently
	// zeroing it would turn a corrupt row into a plausible one, and this row is read
	// at boot and acted on.
	t, terr := time.Parse(time.RFC3339Nano, classifiedAt)
	if terr != nil {
		return store.RolloutState{}, fmt.Errorf("sqlstore: rollout state %q holds an unparseable classification timestamp %q: %w", st.Key, classifiedAt, terr)
	}
	st.ClassifiedAt = t
	if decidedAt.Valid {
		dt, derr := time.Parse(time.RFC3339Nano, decidedAt.String)
		if derr != nil {
			return store.RolloutState{}, fmt.Errorf("sqlstore: rollout state %q holds an unparseable decision timestamp %q: %w", st.Key, decidedAt.String, derr)
		}
		st.DecidedAt = dt
	}
	st.DecidedBy = decidedBy.String
	st.DecidedReason = decidedR.String
	return st, nil
}

// RolloutHistory returns the append-only decision history, oldest first.
func (s *sqlStore) RolloutHistory(ctx context.Context, key string) ([]store.RolloutTransitionRecord, error) {
	q := s.dia.Rebind("SELECT control_key, generation, from_mode, to_mode, committed, decided_at, decided_by, decided_reason, evidence" +
		" FROM " + dialect.ControlRolloutTransitionTable + " WHERE control_key = ? ORDER BY generation")
	rows, err := s.db.QueryContext(ctx, q, key)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: read rollout history: %w", err)
	}
	defer rows.Close()
	var out []store.RolloutTransitionRecord
	for rows.Next() {
		var (
			rec          store.RolloutTransitionRecord
			from, to, at string
			committed    int64
		)
		if err := rows.Scan(&rec.Key, &rec.Generation, &from, &to, &committed, &at, &rec.Actor, &rec.Reason, &rec.Evidence); err != nil {
			return nil, fmt.Errorf("sqlstore: read rollout history: %w", err)
		}
		rec.FromMode, rec.ToMode, rec.Committed = store.RolloutMode(from), store.RolloutMode(to), committed != 0
		t, terr := time.Parse(time.RFC3339Nano, at)
		if terr != nil {
			return nil, fmt.Errorf("sqlstore: rollout history for %q holds an unparseable timestamp %q: %w", key, at, terr)
		}
		rec.At = t
		out = append(out, rec)
	}
	return out, rows.Err()
}

// SetRolloutMode applies a deliberate transition.
//
// The refusals are rules rather than validations:
//
//   - RolloutLegacyCompat is never a target. It is a CLASSIFICATION, and it honors
//     an unbounded set of grandfathered entitlements collected from the deployment's
//     own history; entering it deliberately would grant all of them at once, and
//     re-entering it after retirement would grant them back.
//
//   - RolloutPolicyOptional is unreachable once compatibility is retired. After a
//     deployment has committed to the control being authoritative, the way to permit
//     a destination is to author it — scoped, per tenant, auditable — not to relax the
//     control globally. This is the property that makes "enforcement does not
//     regress" true rather than aspirational.
//
//   - A stale generation loses, so two operators who both read the same diff and both
//     decided cannot both apply; the second is told to re-read, which is the only way
//     the diff they were shown still describes what they are approving.
//
//   - An empty actor or reason loses. A rollout decision with neither is precisely the
//     ownerless state this mechanism exists to eliminate.
//
// The state change and its history entry commit TOGETHER. What this cannot do is
// commit a tenant audit event with them — it has no tenant bound, and the ledger is
// tenant-guarded — so it does not pretend to: see the interface comment.
func (s *sqlStore) SetRolloutMode(ctx context.Context, t store.RolloutTransition) (store.RolloutState, error) {
	if !t.Mode.Valid() {
		return store.RolloutState{}, fmt.Errorf("sqlstore: rollout mode %q is not one of %q, %q, %q",
			t.Mode, store.RolloutEnforced, store.RolloutLegacyCompat, store.RolloutPolicyOptional)
	}
	if t.Mode == store.RolloutLegacyCompat {
		return store.RolloutState{}, fmt.Errorf("sqlstore: %q is a classification, not a transition target: it honors every entitlement a deployment had before the control existed, so entering it deliberately would grant all of them at once", store.RolloutLegacyCompat)
	}
	if strings.TrimSpace(t.Actor) == "" {
		return store.RolloutState{}, errors.New("sqlstore: a rollout transition needs an actor")
	}
	if strings.TrimSpace(t.Reason) == "" {
		return store.RolloutState{}, errors.New("sqlstore: a rollout transition needs a recorded reason")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return store.RolloutState{}, fmt.Errorf("sqlstore: rollout transition: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	sel := s.dia.Rebind(rolloutSelect + " WHERE control_key = ?")
	cur, err := scanRolloutState(tx.QueryRowContext(ctx, sel, t.Key))
	if err != nil {
		return store.RolloutState{}, err
	}
	if cur.EnforcementCommitted && t.Mode == store.RolloutPolicyOptional {
		return store.RolloutState{}, fmt.Errorf("sqlstore: control %q has committed to enforcement, so it cannot move to %q: after a deployment decides a control is authoritative, a destination is permitted by authoring it rather than by relaxing the control globally",
			t.Key, store.RolloutPolicyOptional)
	}
	now := s.clock.Now().Time().UTC()
	// The CAS carries the generation AND the invariants, so a concurrent writer that
	// slipped between the read and the write cannot land a transition this one already
	// decided was illegal.
	upd := s.dia.Rebind("UPDATE " + dialect.ControlRolloutStateTable +
		" SET current_mode = ?, enforcement_committed = ?, generation = generation + 1," +
		" decided_at = ?, decided_by = ?, decided_reason = ?" +
		" WHERE control_key = ? AND generation = ? AND enforcement_committed = ?")
	// The commitment is made by deciding to ENFORCE, and by nothing else. Setting it on
	// any deliberate decision would make the rules contradict each other: choosing the
	// policy-optional posture would forbid the posture that had just been chosen.
	committed := cur.EnforcementCommitted || t.Mode == store.RolloutEnforced
	committedInt, curCommittedInt := 0, 0
	if committed {
		committedInt = 1
	}
	if cur.EnforcementCommitted {
		curCommittedInt = 1
	}
	res, err := tx.ExecContext(ctx, upd, string(t.Mode), committedInt, now.Format(time.RFC3339Nano),
		t.Actor, t.Reason, t.Key, t.ExpectGeneration, curCommittedInt)
	if err != nil {
		return store.RolloutState{}, fmt.Errorf("sqlstore: rollout transition %q: %w", t.Key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return store.RolloutState{}, fmt.Errorf("sqlstore: rollout transition %q: rows affected: %w", t.Key, err)
	}
	if n == 0 {
		return store.RolloutState{}, fmt.Errorf("sqlstore: rollout transition %q expected generation %d but the current generation is %d: re-read the state and confirm the change against what it says now",
			t.Key, t.ExpectGeneration, cur.Generation)
	}
	hist := s.dia.Rebind("INSERT INTO " + dialect.ControlRolloutTransitionTable +
		" (control_key, generation, from_mode, to_mode, committed, decided_at, decided_by, decided_reason, evidence)" +
		" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)")
	if _, err := tx.ExecContext(ctx, hist, t.Key, cur.Generation+1, string(cur.CurrentMode), string(t.Mode),
		committedInt, now.Format(time.RFC3339Nano), t.Actor, t.Reason, t.Evidence); err != nil {
		return store.RolloutState{}, fmt.Errorf("sqlstore: record rollout transition %q: %w", t.Key, err)
	}
	out, err := scanRolloutState(tx.QueryRowContext(ctx, sel, t.Key))
	if err != nil {
		return store.RolloutState{}, err
	}
	if err := tx.Commit(); err != nil {
		return store.RolloutState{}, fmt.Errorf("sqlstore: rollout transition %q: commit: %w", t.Key, err)
	}
	slog.Warn("store: rollout control moved by a deliberate decision",
		"control", t.Key, "from", string(cur.CurrentMode), "to", string(t.Mode),
		"generation", out.Generation, "by", t.Actor, "enforcement_committed", out.EnforcementCommitted)
	return out, nil
}
