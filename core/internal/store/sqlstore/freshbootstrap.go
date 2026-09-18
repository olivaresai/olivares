// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// THE PRE-v1 ADMISSION, and the defect it replaces.
//
// `fresh-empty` used to be decided by `maxVersion == 0` plus the absence of the guard control
// plane and of the two relation families. Nothing else in the managed namespace was looked at.
// The independent review of cb1b5577 measured the consequence directly: an empty tracker beside
// `orgs(review TEXT NOT NULL)` holding a row was classified `fresh-empty`, and so was a
// deliberately malformed or an exactly precreated `orgs`. The classifier then returned, and the
// boot went on to COMMIT the rollout classification transaction and to alter the tracker before
// anything looked at the object that contradicted the whole class.
//
// The ratified frontier (an internal design note (not shipped))
// is not "the database is empty" and not "MAX(version)=0". It is:
//
//	an Olivares managed namespace with NO confirmed core migration, NO product object, and
//	zero or more expressly admitted preliminary checkpoints.
//
// The admitted checkpoints are exactly two families, because exactly two transactions can
// commit before core v1 does:
//
//	T — schema_migrations_core with ZERO rows, in the five-column shape ensureTracking
//	    creates or the historical three-column shape it expands. The three-column shape is
//	    admitted as a STRUCTURE with an expansion path, not as a demonstrated historical
//	    artifact: no release of this repository has been shown to leave one.
//	R — ALL THREE rollout relations, with the exact initial rows the first classification
//	    commits together. Never one or two of them: classifyRolloutControls creates the three
//	    and seeds every control in ONE transaction, so no interruption of it can leave a
//	    subset. A partial R is damage or a foreign schema, never a checkpoint.
//
// Everything else this build administers must be ABSENT — including when it is empty and
// including when its DDL is exactly right. A precreated `orgs` with the current contract is not
// a proven prefix of this Open; it may be a hand provision, and adopting it silently is how a
// database of unknown origin becomes an installation nobody authored.
//
// WHAT IS PRESERVED, and it is half the contract: an object that is not one of this build's
// identities is left exactly as it is. A foreign table with rows does not block an install, and
// a name that merely shares a prefix with ours proves nothing. A foreign PostgreSQL schema is
// not this engine's destination. What IS refused is an EXACT collision with a managed identity,
// even when the object is perfectly valid for its owner: the operator keeps it, and picks a
// destination this build is not going to overwrite.

// freshBootstrapAdmission is what the admission proved, carried forward so the rollout
// classification transaction can re-prove the same thing locally instead of trusting a read
// that happened in another transaction.
type freshBootstrapAdmission struct {
	// TrackerPresent and TrackerColumns describe T: absent, the three-column historical shape
	// or the five-column current one.
	TrackerPresent bool
	TrackerColumns int
	// RolloutPresent is true when all three R relations are present with their exact initial
	// rows, false when all three are absent.
	RolloutPresent bool
	// OperatorProvisioned names the identities of a RESERVED FAMILY that were found complete
	// and judged installed. It is carried so a test can assert which objects took that branch
	// rather than inferring it from a boot that did not fail.
	OperatorProvisioned []string
	// UnnamedModuleEffects are the module-registered migration statements whose durable effect
	// this build cannot determine from the statement's own form.
	//
	// They are a declared COVERAGE LIMIT of this sweep and never a refusal: a module supported
	// today is not rejected because naming its helper objects would need a registration
	// contract this correction did not change. reportIncompleteModuleInventory is what makes
	// the limit visible instead of implicit — an empty list means this inventory found nothing
	// it could not determine, NOT that the managed namespace is completely known.
	UnnamedModuleEffects []unnamedModuleEffect
}

// admittedFreshRolloutSuffixes are the only two witness details the first classification can
// have written for a control whose witness table is absent.
//
//   - `virgin` — the receipt was written while schema_migrations_core did not exist.
//   - `pre-tracker-core` — it existed. corroborateWitness asks for the tracker's EXISTENCE and
//     not for its versions, so an empty tracker really does produce this suffix today.
//
// The pair is ORDERED evidence, not two spellings of one thing. A `virgin` receipt stays valid
// after T appears (the receipt records what was observed, and is never re-derived), while a
// `pre-tracker-core` receipt WITHOUT T contradicts the sequence that could have produced it.
const (
	freshWitnessSuffixVirgin         = ":virgin"
	freshWitnessSuffixPreTrackerCore = ":pre-tracker-core"
)

// verifyFreshBootstrapAdmission proves that a max0 database really is the ratified
// `fresh-empty` frontier.
//
// IT COMMITS NOTHING, AND THAT IS NOT THE SAME AS BEING READ-ONLY SQL. It executes two kinds of
// write inside SAVEPOINTs of the caller's transaction and rolls both back: an INSERT probe on
// the tracker, and a CREATE TABLE probe on PostgreSQL that is the only version-local oracle for
// a rendered contract. Calling it "read-only" was inaccurate and is corrected here; what it
// guarantees is that no catalog or data change of this database is committed by it.
//
// It runs INSIDE the classifier's read transaction, before classifyRolloutControls opens the
// first transaction that can commit product schema and before migrate.Apply's ensureTracking
// commits the second. Every refusal it produces therefore precedes both, which is the property
// the correction exists to establish: the previous order committed R and altered T first.
//
// It is not a substitute for anything later. The rollout classification re-proves the R half in
// its own transaction, v9 re-derives its action transaction-locally, and the pre-serve
// verification still runs after the migrations. A check that arrives earlier does not make a
// later one redundant — the m4 control measured exactly that, and it stays.
func verifyFreshBootstrapAdmission(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	descs []model.EntityDescriptor,
	reg *registry,
	modulePlans []moduleFileMigrationPlan,
	fence guardEventFenceFacts,
) (freshBootstrapAdmission, error) {
	var admission freshBootstrapAdmission
	if reg == nil {
		return admission, errors.New(
			"sqlstore: fresh bootstrap admission: the managed object set cannot be derived without the closed registry")
	}
	set, err := buildManagedObjectSet(dia, descs, reg, modulePlans)
	if err != nil {
		return admission, err
	}
	admission.UnnamedModuleEffects = set.unnamedModuleEffects

	present, err := censusManagedNamespace(ctx, tx, dia, set)
	if err != nil {
		return admission, fmt.Errorf("sqlstore: fresh bootstrap admission: %w", err)
	}

	// Partition what is present into the two admitted families and everything else. The
	// "everything else" branch is the correction: it used to be unreachable.
	rolloutTables := map[string]bool{
		dialect.ControlRolloutStateTable:          true,
		dialect.ControlRolloutTransitionTable:     true,
		dialect.ControlRolloutClassificationTable: true,
	}
	var rolloutFound, unexpected []string
	reservedFound := map[string][]managedObject{}
	for _, found := range present {
		switch {
		case found.object.reservedFamily != "":
			// A RESERVED FAMILY: identities this build verifies and never creates. Their
			// presence says nothing about whether a product migration ever ran here, so
			// they are not evidence of a prior commit — but the FAMILY is the unit, and it
			// is judged below rather than excused member by member.
			reservedFound[found.object.reservedFamily] = append(
				reservedFound[found.object.reservedFamily], found.object)
		case found.object.class == managedClassRelation && found.object.name == coreTrackingTable:
			admission.TrackerPresent = true
		case found.object.class == managedClassRelation && rolloutTables[found.object.name]:
			rolloutFound = append(rolloutFound, found.object.name)
		default:
			unexpected = append(unexpected,
				fmt.Sprintf("%s (created by %s; found as %s)", found.object, found.object.origin, found.catalogKind))
		}
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		return admission, fmt.Errorf(
			"%w: this database records no core migration, and %d object(s) this build administers already exist: %s. Before core v1 the only admitted checkpoints are %s with zero rows and the complete set of three %s relations with their initial rows. An object that is merely EMPTY or whose DDL is exactly right is still not a checkpoint of this Open — it may be a hand provision or another profile's schema, and adopting it silently would attribute an installation nobody authored to this build. Nothing has been created, altered or dropped: point this deployment at a destination this build does not administer, or resolve the object deliberately",
			ErrGuardManifestNoEdge, len(unexpected), strings.Join(unexpected, ", "),
			coreTrackingTable, dialect.ControlRolloutStateTable)
	}

	// THE RESERVED FAMILY, JUDGED AS A SET AND JUDGED HERE. Before classifyRolloutControls
	// opens the first transaction that can commit product schema, so every refusal it produces
	// leaves the tracker and the rollout relations exactly as they were.
	if err := verifyReservedFamilies(ctx, tx, dia, set, reservedFound, fence, &admission); err != nil {
		return admission, err
	}

	if admission.TrackerPresent {
		columns, terr := verifyFreshTrackerAdmission(ctx, tx, dia)
		if terr != nil {
			return admission, terr
		}
		admission.TrackerColumns = columns
	}

	switch len(rolloutFound) {
	case 0:
	case len(rolloutTables):
		admission.RolloutPresent = true
	default:
		sort.Strings(rolloutFound)
		return admission, fmt.Errorf(
			"%w: %d of the %d rollout relations exist (%s) with no core migration recorded. The first classification creates all three and seeds every control in ONE transaction, so no interruption of this product can leave a subset; this is damage or a foreign schema, not a checkpoint. Nothing has been created or altered",
			ErrGuardManifestNoEdge, len(rolloutFound), len(rolloutTables), strings.Join(rolloutFound, ", "))
	}
	if admission.RolloutPresent {
		if err := verifyFreshRolloutCheckpoint(ctx, tx, dia, reg.rolloutControls(), admission.TrackerPresent); err != nil {
			return admission, err
		}
	}
	reportIncompleteModuleInventory(admission.UnnamedModuleEffects)
	return admission, nil
}

// freshBootstrapModuleDiagnosticLimit bounds the diagnostic. A module may ship a hundred
// migration files, and a boot log is not the place to enumerate them; the COUNT is the fact, the
// first few are the lead.
const freshBootstrapModuleDiagnosticLimit = 8

// reportIncompleteModuleInventory tells the caller, out loud, that this admission's knowledge of
// the managed namespace is INCOMPLETE — and for which namespaces.
//
// Without it the residual was carried in a struct nobody read, so an effect this build cannot
// name was indistinguishable from an effect that does not exist. "No residual recorded" is a
// statement about this inventory, never about the module; admitting the bootstrap here does not
// approve those statements' effects and does not claim to know them.
//
// IT NEVER PRINTS SQL OR DATA. Each entry contributes a namespace, a migration file name and a
// form label drawn from a closed vocabulary; the statement text is not kept anywhere it could
// reach a log (see unnamedModuleEffect).
func reportIncompleteModuleInventory(effects []unnamedModuleEffect) {
	if len(effects) == 0 {
		return
	}
	namespaces := map[string]int{}
	var sample []string
	for _, e := range effects {
		namespaces[e.namespace]++
		if len(sample) < freshBootstrapModuleDiagnosticLimit {
			sample = append(sample, e.String())
		}
	}
	sort.Strings(sample)
	if len(effects) > len(sample) {
		sample = append(sample, fmt.Sprintf("and %d more", len(effects)-len(sample)))
	}
	slog.Warn("store: the managed-object inventory of this fresh bootstrap is INCOMPLETE for module-registered migrations",
		"statements_with_undetermined_effect", len(effects),
		"namespaces", sortedMapKeys(namespaces),
		"sample", sample,
		"meaning", "these statements' durable objects are not named by this build, so their absence was NOT verified before admitting this bootstrap; nothing was rejected and no module SQL is reported here")
}

// managedObjectFound is one live catalog entry that matched a managed identity.
type managedObjectFound struct {
	object managedObject
	// catalogKind is what the catalog says the object IS, so a refusal can distinguish a
	// precreated table from a view or a foreign table wearing an index's name.
	catalogKind string
}

// censusManagedNamespace lists the objects of the ENGINE'S OWN namespace that are identities of
// this build.
//
// It is bound to the schema the engine resolves — `main` on SQLite and dialect.EngineSchema on
// PostgreSQL — and not to whatever a search path happens to reach: a foreign PostgreSQL schema
// carrying the same names is another owner's database, not a collision. Foreign objects are read
// and left alone; only a name this build administers is reported.
func censusManagedNamespace(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
	set *managedObjectSet,
) ([]managedObjectFound, error) {
	switch dia.Name() {
	case store.EngineSQLite:
		return censusManagedNamespaceSQLite(ctx, q, set)
	case store.EnginePostgres:
		return censusManagedNamespacePostgres(ctx, q, set)
	default:
		return nil, fmt.Errorf("unsupported engine %q", dia.Name())
	}
}

func censusManagedNamespaceSQLite(
	ctx context.Context,
	q dialect.Querier,
	set *managedObjectSet,
) ([]managedObjectFound, error) {
	// sqlite_master lists tables, views, indexes and triggers together, but they do NOT all
	// collide: measured on this build's engine, a trigger and a table may share a name while a
	// table and an index may not. So the class the catalog reports decides which namespace the
	// name is looked up in (see managedObjectSet.collisionNamespace), and a foreign trigger
	// merely NAMED like one of this build's tables is left alone.
	//
	// The internal `sqlite_` names (autoindexes, sqlite_sequence, the ANALYZE statistics) are
	// the engine's own bookkeeping and are never Olivares objects.
	rows, err := q.QueryContext(ctx, `SELECT type, name FROM main.sqlite_master
WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\'
ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("read sqlite_master: %w", err)
	}
	defer rows.Close() //nolint:errcheck // joined through rows.Err below
	var out []managedObjectFound
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			return nil, fmt.Errorf("read sqlite_master: %w", err)
		}
		class := managedClassRelation
		if kind == "trigger" {
			class = managedClassTrigger
		}
		if obj, ok := set.lookup(class, name, ""); ok {
			out = append(out, managedObjectFound{object: obj, catalogKind: kind})
		}
	}
	return out, rows.Err()
}

func censusManagedNamespacePostgres(
	ctx context.Context,
	q dialect.Querier,
	set *managedObjectSet,
) ([]managedObjectFound, error) {
	var out []managedObjectFound
	// pg_class covers relations AND indexes in ONE namespace, so an index named `orgs` really
	// does collide with the table this build creates and must be looked up in the same bucket.
	// Getting this wrong was measurable: the reviewed candidate kept two buckets and missed the
	// collision entirely, reaching v2 with three rollout relations and core v1 committed.
	//
	// pg_trigger is deliberately NOT censused. A PostgreSQL trigger's identity is scoped to its
	// relation, so a foreign trigger on a foreign table is not a collision at all; this build's
	// own triggers live on relations that must themselves be absent.
	rows, err := q.QueryContext(ctx, `SELECT c.relname, c.relkind::text
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1
ORDER BY c.relname`, dialect.EngineSchema)
	if err != nil {
		return nil, fmt.Errorf("read pg_class: %w", err)
	}
	defer rows.Close() //nolint:errcheck // joined through rows.Err below
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, fmt.Errorf("read pg_class: %w", err)
		}
		if obj, ok := set.lookup(managedClassRelation, name, ""); ok {
			out = append(out, managedObjectFound{object: obj, catalogKind: "relkind " + kind})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pg_class: %w", err)
	}

	// Routines are resolved by NAME AND EXACT INPUT TYPE SIGNATURE, which is what PostgreSQL
	// itself resolves and what `CREATE OR REPLACE FUNCTION` replaces. Argument COUNT is not
	// that, and the difference was measured: a foreign `olivares_lineage_drop(boolean)` matched
	// this build's declared `olivares_lineage_drop(target_tenant text)`.
	//
	// The signature is projected from proargtypes and NOT from
	// pg_get_function_identity_arguments, which is the obvious call and the wrong one:
	// measured on PostgreSQL 16, it renders `review text` — the argument's NAME and its type —
	// and an argument name is not part of a function's identity.
	prows, err := q.QueryContext(ctx, `SELECT p.proname,
       COALESCE(pg_catalog.array_to_string(ARRAY(
         SELECT pg_catalog.format_type(t, NULL)
         FROM pg_catalog.unnest(p.proargtypes) AS t), ', '), ''),
       p.prokind::text
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1
ORDER BY p.proname`, dialect.EngineSchema)
	if err != nil {
		return nil, fmt.Errorf("read pg_proc: %w", err)
	}
	defer prows.Close() //nolint:errcheck // joined through rows.Err below
	for prows.Next() {
		var name, signature, kind string
		if err := prows.Scan(&name, &signature, &kind); err != nil {
			return nil, fmt.Errorf("read pg_proc: %w", err)
		}
		if obj, ok := set.lookup(managedClassRoutine, name, signature); ok {
			out = append(out, managedObjectFound{
				object: obj, catalogKind: fmt.Sprintf("prokind %s with signature (%s)", kind, signature)})
		}
	}
	if err := prows.Err(); err != nil {
		return nil, fmt.Errorf("read pg_proc: %w", err)
	}

	// Event triggers are database-global rather than schema-qualified, and the guard fence
	// creates two of them.
	erows, err := q.QueryContext(ctx, `SELECT evtname FROM pg_catalog.pg_event_trigger ORDER BY evtname`)
	if err != nil {
		return nil, fmt.Errorf("read pg_event_trigger: %w", err)
	}
	defer erows.Close() //nolint:errcheck // joined through rows.Err below
	for erows.Next() {
		var name string
		if err := erows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read pg_event_trigger: %w", err)
		}
		if obj, ok := set.lookup(managedClassEventTrigger, name, ""); ok {
			out = append(out, managedObjectFound{object: obj, catalogKind: "event trigger"})
		}
	}
	return out, erows.Err()
}

// verifyFreshTrackerAdmission proves T: the shape, the absence of anything that would change
// what an insert means, and cardinality zero. It returns the column count of the admitted
// shape (3 or 5).
//
// The first two legs REUSE the preflight's own helpers rather than restating them:
// coreTrackingRelationExists already refuses a view, a partitioned/inherited/foreign relation,
// row-level security, a rewrite rule and a trigger; verifyCoreTrackingRelationShape already
// refuses type drift, a lost primary key, a wrong default, a partial phase/reverted_at pair and
// extra columns. What this ADDS is the half those two do not compare, which the ratified
// frontier names explicitly: constraints, foreign keys and unique indexes that this build never
// declared, and any other undeclared behavior that could change insertion semantics.
func verifyFreshTrackerAdmission(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) (int, error) {
	exists, err := coreTrackingRelationExists(ctx, tx, dia)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: fresh bootstrap admission: inspect %s: %w", coreTrackingTable, err)
	}
	if !exists {
		return 0, fmt.Errorf(
			"sqlstore: fresh bootstrap admission: %s was listed by the catalog and is not an exact standalone table",
			coreTrackingTable)
	}
	cols, err := dia.TableColumns(ctx, tx, coreTrackingTable)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: fresh bootstrap admission: inspect %s columns: %w", coreTrackingTable, err)
	}
	current, err := verifyCoreTrackingRelationShape(ctx, tx, dia, cols)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: fresh bootstrap admission: %w", err)
	}
	columns := 3
	if current {
		columns = 5
	}
	if err := verifyFreshTrackerNoExtraConstraints(ctx, tx, dia); err != nil {
		return 0, err
	}
	if dia.Name() == store.EngineSQLite {
		if err := verifySQLiteTrackerStructuralContract(ctx, tx, current); err != nil {
			return 0, err
		}
	}
	// CARDINALITY ZERO, READ ROW BY ROW. `COALESCE(MAX(version),0)` cannot tell an empty
	// tracker from one holding a row this reader refuses, and on PostgreSQL a filtered read
	// would report zero rows for a history that is merely invisible — which is why the
	// standalone-table check above refuses row-level security outright.
	versions, err := readCanonicalCoreTrackingVersions(ctx, tx, dia)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: fresh bootstrap admission: %w", err)
	}
	if len(versions) != 0 {
		return 0, fmt.Errorf(
			"sqlstore: fresh bootstrap admission: %s holds %d version row(s) and this classification is the max0 frontier",
			coreTrackingTable, len(versions))
	}
	if err := probeFreshTrackerAcceptsAVersion(ctx, tx, dia); err != nil {
		return 0, err
	}
	return columns, nil
}

// THE ADMITTED SQLITE TRACKER CONTRACT, as normalized SQL.
//
// PostgreSQL has a constraint catalog and verifyFreshTrackerNoExtraConstraints reads it
// directly. SQLite has none: PRAGMA index_list and foreign_key_list see uniqueness and foreign
// keys, and NOTHING sees a CHECK. So on SQLite the contract is the stored CREATE statement,
// which is the only place the engine keeps that fact — normalized, and compared against exactly
// the two admitted forms.
//
// Both constants describe migrate.ensureTracking's own DDL; T3 is that DDL without the two
// columns ensureTracking adds. TestFreshBootstrapTrackerContractMatchesEnsureTracking is the
// anti-drift control: it normalizes the tracker the REAL ensureTracking creates and requires it
// to equal freshTrackerContractT5, so these strings cannot quietly stop describing the product.
//
// Measured, and it is why one text comparison serves BOTH admitted forms: SQLite REWRITES the
// stored statement when ALTER TABLE ADD COLUMN runs, so a three-column tracker expanded by the
// real ensureTracking stores byte-identical text to a freshly created five-column one.
const (
	freshTrackerContractT3 = "CREATE TABLE X (VERSION INTEGER PRIMARY KEY, NAME TEXT NOT NULL, " +
		"APPLIED_AT TEXT NOT NULL)"
	freshTrackerContractT5 = "CREATE TABLE X (VERSION INTEGER PRIMARY KEY, NAME TEXT NOT NULL, " +
		"APPLIED_AT TEXT NOT NULL, PHASE TEXT NOT NULL DEFAULT 'expand', REVERTED_AT TEXT)"
)

// verifySQLiteTrackerStructuralContract refuses an admitted-looking tracker whose stored
// statement is not one of the two admitted forms.
//
// It rejects far more than a CHECK, and deliberately: WITHOUT ROWID, STRICT, a COLLATE nobody
// declared, a generated column, an inline UNIQUE or REFERENCES — every one of them changes what
// a stored row or an insert means, and none of them is a form this build ever writes.
func verifySQLiteTrackerStructuralContract(ctx context.Context, q dialect.Querier, current bool) error {
	rows, err := q.QueryContext(ctx,
		"SELECT COALESCE(sql, '') FROM main.sqlite_master WHERE type = 'table' AND name = ?",
		coreTrackingTable)
	if err != nil {
		return fmt.Errorf("sqlstore: fresh bootstrap admission: read the stored %s statement: %w",
			coreTrackingTable, err)
	}
	var stored string
	found := false
	if rows.Next() {
		found = true
		if err := rows.Scan(&stored); err != nil {
			_ = rows.Close()
			return fmt.Errorf("sqlstore: fresh bootstrap admission: read the stored %s statement: %w",
				coreTrackingTable, err)
		}
	}
	if err := closeRows(rows, "sqlite_master "+coreTrackingTable); err != nil {
		return fmt.Errorf("sqlstore: fresh bootstrap admission: read the stored %s statement: %w",
			coreTrackingTable, err)
	}
	if !found || strings.TrimSpace(stored) == "" {
		return fmt.Errorf(
			"sqlstore: fresh bootstrap admission: %s has no stored CREATE statement, so its constraints cannot be established",
			coreTrackingTable)
	}
	want, columns := freshTrackerContractT3, 3
	if current {
		want, columns = freshTrackerContractT5, 5
	}
	if got := normalizeSQLiteTableDDL(stored, coreTrackingTable); got != want {
		return fmt.Errorf(
			"%w: %s is stored as %q where the admitted %d-column form is %q. Its columns already matched, so the difference is in what the rest of the statement declares — a CHECK, a uniqueness or collation rule, a generated column or a table option. One successful sample insert cannot establish the absence of such a predicate, so the statement itself is the contract. Nothing has been created or altered",
			ErrGuardManifestNoEdge, coreTrackingTable, got, columns, want)
	}
	return nil
}

// normalizeSQLiteTableDDL reduces a stored CREATE TABLE to the form the two contracts above are
// written in: the relation's own name replaced by X, identifiers and keywords upper-cased,
// string literals preserved verbatim, comments removed and punctuation spaced canonically.
//
// SQLite folds ASCII identifier case and does NOT fold string literals, so this is the equality
// the engine itself uses — with the one exception stated out loud: `DEFAULT 'expand'` keeps its
// literal, because that value is data rather than an identifier.
func normalizeSQLiteTableDDL(stored, relation string) string {
	var out []byte
	for i := 0; i < len(stored); i++ {
		switch c := stored[i]; c {
		case '\'':
			// A string literal, verbatim, including a doubled quote inside it.
			out = append(out, c)
			i = copySQLiteLiteral(stored, i, &out)
		case '"', '`', '[':
			// A quoted identifier: SQLite folds its case like an unquoted one, so the quotes
			// are dropped and the name is normalized with everything else.
			i = copySQLiteQuotedIdentifier(stored, i, &out)
		case '-':
			if i+1 < len(stored) && stored[i+1] == '-' {
				i, out = skipToLineEnd(stored, i), append(out, ' ')
				continue
			}
			out = append(out, c)
		case '/':
			if i+1 < len(stored) && stored[i+1] == '*' {
				i, out = skipBlockComment(stored, i), append(out, ' ')
				continue
			}
			out = append(out, c)
		default:
			out = append(out, asciiUpperByte(c))
		}
	}
	text := strings.Join(strings.Fields(string(out)), " ")
	// Canonical punctuation, applied after the whitespace join so no original spacing survives.
	text = strings.ReplaceAll(text, " ,", ",")
	text = strings.ReplaceAll(text, "( ", "(")
	text = strings.ReplaceAll(text, " )", ")")
	// The relation's own name is not part of the contract: the same statement describes the
	// tracker whatever it is called, and the census has already matched the name.
	return strings.Replace(text, asciiUpper(relation), "X", 1)
}

// copySQLiteLiteral copies a single-quoted literal verbatim and returns the index of its closing
// quote. Literal case is data and is never folded.
func copySQLiteLiteral(stored string, open int, out *[]byte) int {
	for i := open + 1; i < len(stored); i++ {
		*out = append(*out, stored[i])
		if stored[i] != '\'' {
			continue
		}
		if i+1 < len(stored) && stored[i+1] == '\'' {
			i++
			*out = append(*out, stored[i])
			continue
		}
		return i
	}
	return len(stored)
}

// copySQLiteQuotedIdentifier folds a quoted identifier into the normalized stream without its
// quoting characters, and returns the index of the closing quote.
func copySQLiteQuotedIdentifier(stored string, open int, out *[]byte) int {
	closing := byte('"')
	switch stored[open] {
	case '`':
		closing = '`'
	case '[':
		closing = ']'
	}
	for i := open + 1; i < len(stored); i++ {
		if stored[i] == closing {
			return i
		}
		*out = append(*out, asciiUpperByte(stored[i]))
	}
	return len(stored)
}

func skipToLineEnd(stored string, from int) int {
	if i := strings.IndexByte(stored[from:], '\n'); i >= 0 {
		return from + i
	}
	return len(stored)
}

func skipBlockComment(stored string, from int) int {
	if i := strings.Index(stored[from+2:], "*/"); i >= 0 {
		return from + 2 + i + 1
	}
	return len(stored)
}

func asciiUpperByte(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - ('a' - 'A')
	}
	return c
}

func asciiUpper(s string) string {
	b := []byte(s)
	for i := range b {
		b[i] = asciiUpperByte(b[i])
	}
	return string(b)
}

// probeFreshTrackerAcceptsAVersion is SUPPLEMENTARY evidence that the tracker can still record a
// migration. It is NOT the proof that no undeclared constraint is present, and the reviewed
// candidate treated it as one.
//
// The measured defect: a tracker declared `version INTEGER PRIMARY KEY CHECK(version < 2)` was
// admitted, three rollout relations and core v1 committed, and the boot then failed recording
// v2. One successful sample insert cannot establish the absence of a predicate — a different
// predicate admits that sample and refuses the real migration, and adding more sample versions
// only moves the boundary. The structural contract above is what decides; this probe stays
// because it also catches behavior no catalog exposes, such as a rule or an unexpected default.
//
// IT COMMITS NOTHING. The insert runs inside a SAVEPOINT of the caller's transaction and is
// rolled back on both paths; the caller's transaction is itself rolled back. This is the same
// mechanism the PostgreSQL exact-shape oracle already uses, and it is the reason this
// classification is described as committing nothing rather than as SQL READ ONLY.
func probeFreshTrackerAcceptsAVersion(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) error {
	const savepoint = "olivares_fresh_tracker_probe"
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
		return fmt.Errorf("sqlstore: fresh bootstrap admission: open the tracker probe savepoint: %w", err)
	}
	relation := coreTrackingRelation(dia)
	cols, err := dia.TableColumns(ctx, tx, coreTrackingTable)
	if err != nil {
		return fmt.Errorf("sqlstore: fresh bootstrap admission: inspect %s columns: %w", coreTrackingTable, err)
	}
	columns := "(version, name, applied_at)"
	values := "(?, ?, ?)"
	args := []any{int64(1), "probe", time.Unix(0, 0).UTC().Format(time.RFC3339Nano)}
	if cols["phase"] {
		columns = "(version, name, applied_at, phase)"
		values = "(?, ?, ?, ?)"
		args = append(args, "expand")
	}
	// #nosec G202 -- relation and columns are internal constants, never user input
	_, ierr := tx.ExecContext(ctx, dia.Rebind("INSERT INTO "+relation+" "+columns+" VALUES "+values), args...)
	if _, rerr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint); rerr != nil {
		return fmt.Errorf("sqlstore: fresh bootstrap admission: roll back the tracker probe: %w", rerr)
	}
	if _, rerr := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint); rerr != nil {
		return fmt.Errorf("sqlstore: fresh bootstrap admission: release the tracker probe savepoint: %w", rerr)
	}
	if ierr != nil {
		return fmt.Errorf(
			"sqlstore: fresh bootstrap admission: %s has the admitted column shape but refuses the migration row this boot would write (%v). A tracker that cannot record a version is not a checkpoint of this product; nothing has been created or altered",
			coreTrackingTable, ierr)
	}
	return nil
}

// verifyFreshTrackerNoExtraConstraints refuses the objects attached to T that the column
// comparison cannot see.
func verifyFreshTrackerNoExtraConstraints(ctx context.Context, q dialect.Querier, dia dialect.Dialect) error {
	switch dia.Name() {
	case store.EngineSQLite:
		// `version INTEGER PRIMARY KEY` is a rowid alias, so an admitted tracker has NO
		// index at all: every entry in index_list is either a UNIQUE this build never
		// declared or an index somebody added. foreign_key_list must be empty for the same
		// reason — a tracker whose rows depend on another relation is not this one.
		rows, err := q.QueryContext(ctx, "PRAGMA main.index_list("+coreTrackingTable+")") // #nosec G202 -- internal constant
		if err != nil {
			return fmt.Errorf("sqlstore: fresh bootstrap admission: read %s indexes: %w", coreTrackingTable, err)
		}
		names, err := scanFirstStringColumn(rows, 1)
		if err != nil {
			return fmt.Errorf("sqlstore: fresh bootstrap admission: read %s indexes: %w", coreTrackingTable, err)
		}
		if len(names) > 0 {
			return fmt.Errorf(
				"sqlstore: fresh bootstrap admission: %s carries %d index/uniqueness object(s) this build never declares (%s)",
				coreTrackingTable, len(names), strings.Join(names, ", "))
		}
		frows, err := q.QueryContext(ctx, "PRAGMA main.foreign_key_list("+coreTrackingTable+")") // #nosec G202 -- internal constant
		if err != nil {
			return fmt.Errorf("sqlstore: fresh bootstrap admission: read %s foreign keys: %w", coreTrackingTable, err)
		}
		refs, err := scanFirstStringColumn(frows, 2)
		if err != nil {
			return fmt.Errorf("sqlstore: fresh bootstrap admission: read %s foreign keys: %w", coreTrackingTable, err)
		}
		if len(refs) > 0 {
			return fmt.Errorf(
				"sqlstore: fresh bootstrap admission: %s declares %d foreign key(s) (to %s) and this build declares none",
				coreTrackingTable, len(refs), strings.Join(refs, ", "))
		}
		return nil
	case store.EnginePostgres:
		rows, err := q.QueryContext(ctx, `SELECT con.conname, con.contype::text
FROM pg_catalog.pg_constraint con
JOIN pg_catalog.pg_class c ON c.oid = con.conrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2 AND con.contype <> 'p'
ORDER BY con.conname`, dialect.EngineSchema, coreTrackingTable)
		if err != nil {
			return fmt.Errorf("sqlstore: fresh bootstrap admission: read %s constraints: %w", coreTrackingTable, err)
		}
		names, err := scanFirstStringColumn(rows, 0)
		if err != nil {
			return fmt.Errorf("sqlstore: fresh bootstrap admission: read %s constraints: %w", coreTrackingTable, err)
		}
		if len(names) > 0 {
			return fmt.Errorf(
				"sqlstore: fresh bootstrap admission: %s carries %d constraint(s) beyond its primary key (%s), and this build declares none",
				coreTrackingTable, len(names), strings.Join(names, ", "))
		}
		irows, err := q.QueryContext(ctx, `SELECT i.relname
FROM pg_catalog.pg_index x
JOIN pg_catalog.pg_class c ON c.oid = x.indrelid
JOIN pg_catalog.pg_class i ON i.oid = x.indexrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2 AND NOT x.indisprimary
ORDER BY i.relname`, dialect.EngineSchema, coreTrackingTable)
		if err != nil {
			return fmt.Errorf("sqlstore: fresh bootstrap admission: read %s indexes: %w", coreTrackingTable, err)
		}
		indexes, err := scanFirstStringColumn(irows, 0)
		if err != nil {
			return fmt.Errorf("sqlstore: fresh bootstrap admission: read %s indexes: %w", coreTrackingTable, err)
		}
		if len(indexes) > 0 {
			return fmt.Errorf(
				"sqlstore: fresh bootstrap admission: %s carries %d index(es) beyond its primary key (%s), and this build declares none",
				coreTrackingTable, len(indexes), strings.Join(indexes, ", "))
		}
		return nil
	default:
		return fmt.Errorf("unsupported engine %q", dia.Name())
	}
}

// scanFirstStringColumn collects column `col` of every row as text and joins the iteration
// error, which an early-exit loop would hide.
func scanFirstStringColumn(rows *sql.Rows, col int) ([]string, error) {
	defer rows.Close() //nolint:errcheck // joined through rows.Err below
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []string
	for rows.Next() {
		cells := make([]any, len(names))
		holders := make([]sql.NullString, len(names))
		for i := range cells {
			cells[i] = &holders[i]
		}
		if err := rows.Scan(cells...); err != nil {
			return nil, err
		}
		if col < len(holders) {
			out = append(out, holders[col].String)
		}
	}
	return out, rows.Err()
}

// verifyFreshRolloutCheckpoint proves R: the three shapes, and the exact correlated rows the
// FIRST classification of THIS registration profile commits.
//
// Why the rows and not only the tables: classifyRolloutControls validates an existing state row
// but ensureClassificationReceipt RETURNS EARLY when a receipt exists, without comparing its
// fields. Calling the mutating classifier and treating its success as proof of this correlation
// would therefore prove less than it appears to — so the correlation is established HERE, before
// that transaction opens, and re-established inside it.
func verifyFreshRolloutCheckpoint(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
	controls []store.RolloutControl,
	trackerPresent bool,
) error {
	// The shapes first: a SELECT succeeding proves nothing about the columns, checks and keys
	// the later reads and writes depend on.
	if err := verifyFreshRolloutShapes(ctx, q, dia); err != nil {
		return err
	}

	// No transition can precede the first complete Open. A transition is an operator DECISION
	// about a control that has been classified, and this database has not finished being
	// created.
	transitions, err := countTableRows(ctx, q, dialect.ControlRolloutTransitionTable)
	if err != nil {
		return fmt.Errorf("sqlstore: fresh bootstrap admission: %w", err)
	}
	if transitions != 0 {
		return fmt.Errorf(
			"%w: %s holds %d decision(s) before any core migration is recorded. A transition is a decision about a classified control, and no complete Open has happened here",
			ErrGuardManifestNoEdge, dialect.ControlRolloutTransitionTable, transitions)
	}

	states, err := readFreshRolloutStates(ctx, q, dia)
	if err != nil {
		return err
	}
	receipts, err := readFreshRolloutReceipts(ctx, q, dia)
	if err != nil {
		return err
	}

	declared := map[string]store.RolloutControl{}
	var declaredKeys []string
	for _, c := range controls {
		declared[c.Key] = c
		declaredKeys = append(declaredKeys, c.Key)
	}
	sort.Strings(declaredKeys)
	if err := freshRolloutKeySetsAgree(declaredKeys, states, receipts); err != nil {
		return err
	}

	var batchAt, batchKey string
	for _, key := range declaredKeys {
		control := declared[key]
		state := states[key]
		receipt := receipts[key]
		if state.ClassifiedMode != control.FreshMode || state.CurrentMode != control.FreshMode {
			return fmt.Errorf(
				"%w: rollout control %q is recorded as classified=%q current=%q where the fresh disposition of this build is %q. With no core migration recorded, this build's witness table cannot exist, so the only classification reachable here is the fresh one",
				ErrGuardManifestNoEdge, key, state.ClassifiedMode, state.CurrentMode, control.FreshMode)
		}
		if allNull, set := state.decisionColumnsAreNull(); !allNull {
			return fmt.Errorf(
				"%w: rollout control %q has %s set before any core migration is recorded. The seed of a first classification leaves all three decision columns SQL NULL, and an empty string is not NULL: it is a value this engine never writes",
				ErrGuardManifestNoEdge, key, strings.Join(set, " and "))
		}
		if state.EnforcementCommitted || state.Generation != 1 {
			return fmt.Errorf(
				"%w: rollout control %q carries generation %d and enforcement-committed=%t before any core migration is recorded. The seed of a first classification is generation 1 with no commitment",
				ErrGuardManifestNoEdge, key, state.Generation, state.EnforcementCommitted)
		}
		if state.WitnessKind != witnessKindTablePresence {
			return fmt.Errorf(
				"%w: rollout control %q records witness kind %q where this build writes %q",
				ErrGuardManifestNoEdge, key, state.WitnessKind, witnessKindTablePresence)
		}
		if err := verifyFreshWitnessDetail(key, control, state.WitnessDetail, trackerPresent); err != nil {
			return err
		}
		// FIELD BY FIELD, because the receipt is the record that makes the state row's loss
		// detectable. A receipt that disagrees with its state row is two facts about one
		// classification, and neither can be preferred without inventing which boot was
		// right.
		if receipt.mode != string(state.ClassifiedMode) ||
			receipt.classifiedAt != state.classifiedAtText ||
			receipt.witnessKind != state.WitnessKind ||
			receipt.witnessDetail != state.WitnessDetail {
			return fmt.Errorf(
				"%w: the classification receipt for rollout control %q does not match its state row: receipt(mode=%q at=%q kind=%q detail=%q) state(mode=%q at=%q kind=%q detail=%q). Both are written by one transaction, so a disagreement is damage; no receipt is rewritten to make this state admissible",
				ErrGuardManifestNoEdge, key,
				receipt.mode, receipt.classifiedAt, receipt.witnessKind, receipt.witnessDetail,
				state.ClassifiedMode, state.classifiedAtText, state.WitnessKind, state.WitnessDetail)
		}
		if err := verifyCanonicalClassificationTimestamp(key, state.classifiedAtText); err != nil {
			return err
		}
		// ONE TIMESTAMP FOR THE WHOLE BATCH. classifyRolloutControls takes `now` once and
		// writes it to every control it seeds, in one transaction, so a first classification
		// cannot have produced two. Two of them means either two classification transactions
		// — which is the mid-bootstrap profile change §3.4 does not ratify — or an edited
		// row; either way the checkpoint is not the one this build would have written.
		if batchAt == "" {
			batchAt = state.classifiedAtText
			batchKey = key
		} else if state.classifiedAtText != batchAt {
			return fmt.Errorf(
				"%w: rollout controls %q and %q were classified at different instants (%q and %q). The first classification writes every control it seeds in ONE transaction with ONE timestamp, so this checkpoint was not produced by a single first classification of this profile. It is preserved and nothing is rewritten to reconcile it",
				ErrGuardManifestNoEdge, batchKey, key, batchAt, state.classifiedAtText)
		}
	}
	return nil
}

// verifyCanonicalClassificationTimestamp requires the exact representation the writer produces.
//
// classifyRolloutControls stores `time.Now().UTC().Format(time.RFC3339Nano)`, so the stored text
// is UTC with a `Z` designator and no trailing zeros in its fraction. Merely PARSING the value is
// not that test, and the difference was measured: `2026-01-01T01:00:00+01:00` parses, is not what
// any writer of this engine stores, and was admitted. The value is never rewritten into canonical
// form — a witness that has to be normalized to be admissible is not the witness it claims to be.
func verifyCanonicalClassificationTimestamp(key, stored string) error {
	parsed, err := time.Parse(time.RFC3339Nano, stored)
	if err != nil {
		return fmt.Errorf(
			"%w: rollout control %q holds an unparseable classification timestamp %q",
			ErrGuardManifestNoEdge, key, stored)
	}
	if canonical := parsed.UTC().Format(time.RFC3339Nano); canonical != stored {
		return fmt.Errorf(
			"%w: rollout control %q holds the classification timestamp %q, which is not the representation this engine writes; the same instant in the writer's own form is %q. Nothing is rewritten to make it admissible",
			ErrGuardManifestNoEdge, key, stored, canonical)
	}
	return nil
}

// verifyFreshWitnessDetail admits exactly the two details corroborateWitness can have written
// for an absent witness, and binds the second to the sequence that produces it.
func verifyFreshWitnessDetail(
	key string,
	control store.RolloutControl,
	detail string,
	trackerPresent bool,
) error {
	base := "table:" + control.WitnessTable + ":absent"
	switch detail {
	case base + freshWitnessSuffixVirgin:
		// Still valid after T appears: the receipt records what was observed and is never
		// re-derived. Rewriting it to `pre-tracker-core` because the tracker exists NOW
		// would replace an observation with a deduction.
		return nil
	case base + freshWitnessSuffixPreTrackerCore:
		if !trackerPresent {
			return fmt.Errorf(
				"%w: rollout control %q records the witness detail %q, which is written only when %s already exists, and it does not. The sequence that would produce this receipt did not happen; %s is not created to justify it",
				ErrGuardManifestNoEdge, key, detail, coreTrackingTable, coreTrackingTable)
		}
		return nil
	default:
		return fmt.Errorf(
			"%w: rollout control %q records the witness detail %q. With no core migration recorded this build's witness table %q cannot exist, so the only details reachable are %q and %q",
			ErrGuardManifestNoEdge, key, detail, control.WitnessTable,
			base+freshWitnessSuffixVirgin, base+freshWitnessSuffixPreTrackerCore)
	}
}

func freshRolloutKeySetsAgree(
	declared []string,
	states map[string]freshRolloutState,
	receipts map[string]freshRolloutReceipt,
) error {
	describe := func(name string, keys []string) string {
		if len(keys) == 0 {
			return name + "=none"
		}
		return name + "=" + strings.Join(keys, ",")
	}
	stateKeys, receiptKeys := sortedMapKeys(states), sortedMapKeys(receipts)
	if strings.Join(stateKeys, ",") == strings.Join(declared, ",") &&
		strings.Join(receiptKeys, ",") == strings.Join(declared, ",") {
		return nil
	}
	return fmt.Errorf(
		"%w: the rollout checkpoint does not describe this registration profile: %s, %s, %s. The first classification writes one state row AND one receipt for every declared control in a single transaction, so a subset, an orphan or an unknown key is not an interrupted commit. It may be a perfectly valid checkpoint of ANOTHER profile — which is exactly why this build will not guess: the state is preserved and no receipt is written to make it fit",
		ErrGuardManifestNoEdge,
		describe("declared", declared), describe("state rows", stateKeys), describe("receipts", receiptKeys))
}

func sortedMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// freshRolloutState is one state row AS STORED.
//
// It keeps the classification timestamp as TEXT so the receipt comparison is byte-for-byte
// rather than through a parse that normalizes, and it keeps the three decision columns as
// sql.NullString because SQL NULL and the empty string are different facts about them. The
// reviewed candidate discarded `Valid` for the reason column, so a row with
// `decided_reason = ”` — a value no writer of this engine produces, and one the table's own
// CHECK does not forbid because that CHECK only pairs decided_at with decided_by — was admitted
// as an untouched first classification.
type freshRolloutState struct {
	store.RolloutState
	classifiedAtText string
	decidedAt        sql.NullString
	decidedBy        sql.NullString
	decidedReason    sql.NullString
}

// decisionColumnsAreNull reports whether all three decision columns are SQL NULL, and names the
// ones that are not.
func (st freshRolloutState) decisionColumnsAreNull() (bool, []string) {
	var set []string
	for _, c := range []struct {
		name  string
		value sql.NullString
	}{
		{"decided_at", st.decidedAt},
		{"decided_by", st.decidedBy},
		{"decided_reason", st.decidedReason},
	} {
		if c.value.Valid {
			set = append(set, c.name)
		}
	}
	return len(set) == 0, set
}

type freshRolloutReceipt struct {
	mode          string
	classifiedAt  string
	witnessKind   string
	witnessDetail string
}

func readFreshRolloutStates(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
) (map[string]freshRolloutState, error) {
	rows, err := q.QueryContext(ctx, "SELECT control_key, classified_mode, current_mode,"+
		" enforcement_committed, generation, classified_at, witness_kind, witness_detail,"+
		" decided_at, decided_by, decided_reason FROM "+dialect.ControlRolloutStateTable)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: fresh bootstrap admission: read %s: %w",
			dialect.ControlRolloutStateTable, err)
	}
	defer rows.Close() //nolint:errcheck // joined through rows.Err below
	out := map[string]freshRolloutState{}
	for rows.Next() {
		var (
			st                    freshRolloutState
			classified, current   string
			committed, generation int64
		)
		if err := rows.Scan(&st.Key, &classified, &current, &committed, &generation,
			&st.classifiedAtText, &st.WitnessKind, &st.WitnessDetail,
			&st.decidedAt, &st.decidedBy, &st.decidedReason); err != nil {
			return nil, fmt.Errorf("sqlstore: fresh bootstrap admission: read %s: %w",
				dialect.ControlRolloutStateTable, err)
		}
		st.ClassifiedMode, st.CurrentMode = store.RolloutMode(classified), store.RolloutMode(current)
		st.EnforcementCommitted, st.Generation = committed != 0, generation
		if _, duplicate := out[st.Key]; duplicate {
			return nil, fmt.Errorf("sqlstore: fresh bootstrap admission: %s holds two rows for %q",
				dialect.ControlRolloutStateTable, st.Key)
		}
		out[st.Key] = st
	}
	return out, rows.Err()
}

func readFreshRolloutReceipts(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
) (map[string]freshRolloutReceipt, error) {
	rows, err := q.QueryContext(ctx, "SELECT control_key, classified_mode, classified_at,"+
		" witness_kind, witness_detail FROM "+dialect.ControlRolloutClassificationTable)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: fresh bootstrap admission: read %s: %w",
			dialect.ControlRolloutClassificationTable, err)
	}
	defer rows.Close() //nolint:errcheck // joined through rows.Err below
	out := map[string]freshRolloutReceipt{}
	for rows.Next() {
		var key string
		var r freshRolloutReceipt
		if err := rows.Scan(&key, &r.mode, &r.classifiedAt, &r.witnessKind, &r.witnessDetail); err != nil {
			return nil, fmt.Errorf("sqlstore: fresh bootstrap admission: read %s: %w",
				dialect.ControlRolloutClassificationTable, err)
		}
		if _, duplicate := out[key]; duplicate {
			return nil, fmt.Errorf("sqlstore: fresh bootstrap admission: %s holds two receipts for %q",
				dialect.ControlRolloutClassificationTable, key)
		}
		out[key] = r
	}
	return out, rows.Err()
}

func countTableRows(ctx context.Context, q dialect.Querier, table string) (int64, error) {
	rows, err := q.QueryContext(ctx, "SELECT COUNT(*) FROM "+table) // #nosec G202 -- internal constant
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", table, err)
	}
	defer rows.Close() //nolint:errcheck // joined through rows.Err below
	var count int64
	if rows.Next() {
		if err := rows.Scan(&count); err != nil {
			return 0, fmt.Errorf("read %s: %w", table, err)
		}
	}
	return count, rows.Err()
}

// verifyFreshRolloutShapes compares the three live relations against the DDL THIS BUILD
// renders, rather than against a hand-written expectation that would drift from it.
//
// A working SELECT is not the property that matters. These rows are read at boot and acted on:
// a widened CHECK, a lost primary key or an extra column changes what a stored row can mean,
// and the classification is deliberately never re-derived, so a wrong reading here is permanent.
func verifyFreshRolloutShapes(ctx context.Context, q dialect.Querier, dia dialect.Dialect) error {
	rendered := []struct {
		table string
		ddl   string
	}{
		{dialect.ControlRolloutStateTable, rolloutStateDDL},
		{dialect.ControlRolloutTransitionTable, rolloutTransitionDDL},
		{dialect.ControlRolloutClassificationTable, rolloutClassificationDDL},
	}
	switch dia.Name() {
	case store.EngineSQLite:
		// SQLite stores the CREATE statement VERBATIM, so the comparison is the statement
		// itself — the same oracle verifyGuardShapeSQLite uses for the guard control plane.
		stored, attached, err := sqliteRolloutObjects(ctx, q)
		if err != nil {
			return fmt.Errorf("sqlstore: fresh bootstrap admission: %w", err)
		}
		for _, r := range rendered {
			text, present := stored[r.table]
			if !present {
				return fmt.Errorf(
					"sqlstore: fresh bootstrap admission: %s was listed by the catalog and is not an ordinary table", r.table)
			}
			// SQLite stores the statement it PARSED, and it drops `IF NOT EXISTS` from the
			// text it keeps. The rendered constant carries the clause because the DDL loop
			// re-runs on every boot, so the two differ by exactly that phrase and nothing
			// else — removing it from the rendered side is a normalization of a documented
			// engine behavior, not a relaxation of the comparison.
			if want := strings.Replace(r.ddl, "IF NOT EXISTS ", "", 1); text != want {
				return fmt.Errorf(
					"%w: %s is stored as %q where this build renders %q",
					ErrGuardManifestNoEdge, r.table, text, want)
			}
		}
		if len(attached) > 0 {
			sort.Strings(attached)
			return fmt.Errorf(
				"%w: %d object(s) are attached to the rollout relations that this build does not create at this point (%s). The append-only guards on the decision and receipt logs are installed AFTER the migrations, so at a pre-v1 checkpoint there is nothing attached to these tables at all",
				ErrGuardManifestNoEdge, len(attached), strings.Join(attached, ", "))
		}
		return nil
	case store.EnginePostgres:
		for _, r := range rendered {
			if err := verifyPostgresRolloutShape(ctx, q, r.table, r.ddl); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported engine %q", dia.Name())
	}
}

// sqliteRolloutObjects returns the stored CREATE text of each rollout table and the names of
// any other object attached to one of them.
func sqliteRolloutObjects(ctx context.Context, q dialect.Querier) (map[string]string, []string, error) {
	tables := []string{
		dialect.ControlRolloutStateTable,
		dialect.ControlRolloutTransitionTable,
		dialect.ControlRolloutClassificationTable,
	}
	list, args := sqliteNameList(tables)
	// #nosec G202 -- `list` is sqliteNameList's output: ONLY "?,?,…" placeholders
	rows, err := q.QueryContext(ctx, `SELECT type, name, tbl_name, sql FROM main.sqlite_master
WHERE tbl_name IN (`+list+`) AND name NOT LIKE 'sqlite\_%' ESCAPE '\'
ORDER BY name`, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("read sqlite_master: %w", err)
	}
	defer rows.Close() //nolint:errcheck // joined through rows.Err below
	stored := map[string]string{}
	var attached []string
	for rows.Next() {
		var kind, name, table string
		var text sql.NullString
		if err := rows.Scan(&kind, &name, &table, &text); err != nil {
			return nil, nil, fmt.Errorf("read sqlite_master: %w", err)
		}
		if kind == "table" && name == table {
			stored[name] = text.String
			continue
		}
		attached = append(attached, kind+" "+name+" on "+table)
	}
	return stored, attached, rows.Err()
}

// verifyPostgresRolloutShape compares a live relation with a PROBE created from the rendered
// DDL on the same server, inside a savepoint that is rolled back.
//
// The probe is the only version-local oracle for a rendered contract on PostgreSQL — the same
// reason core v7's fresh branch and the access-evidence exact-shape proof already use one — and
// it is what keeps this comparison honest as the DDL changes: an expectation written out by hand
// here would keep passing after the DDL moved.
//
// ITS IDENTITY IS PROVED, NOT ASSUMED, and the reviewed candidate showed why. It used the
// predictable name `olivares_fresh_probe_<table>` and kept the rendered `CREATE TABLE IF NOT
// EXISTS`, so an unrelated relation of that name — a name outside the managed set, which another
// application may legitimately own — was silently adopted AS THE ORACLE, and a perfectly valid R
// was refused for a contract mismatch that belonged to somebody else's table. Now the probe gets
// a random identity, is proved absent before it is created, is created WITHOUT `IF NOT EXISTS`
// so that creating it is the proof, and is censused BY OID rather than by name.
func verifyPostgresRolloutShape(ctx context.Context, q dialect.Querier, table, ddl string) error {
	liveOID, err := pgRelationOID(ctx, q, table)
	if err != nil {
		return fmt.Errorf("sqlstore: fresh bootstrap admission: resolve %s: %w", table, err)
	}
	live, err := pgRelationShape(ctx, q, liveOID)
	if err != nil {
		return fmt.Errorf("sqlstore: fresh bootstrap admission: census %s: %w", table, err)
	}
	if live.kind != "r" || live.persistence != "p" || live.partition || live.rowSecurity ||
		live.forceRowSecurity || live.inherits || live.inherited || live.policies || live.rules ||
		live.triggers {
		return fmt.Errorf(
			"%w: %s is not the ordinary standalone table this build creates: kind=%q persistence=%q partition=%t rls=%t force_rls=%t parent=%t child=%t policy=%t rule=%t trigger=%t. The append-only guards on these relations are installed AFTER the migrations, so a pre-v1 checkpoint carries none",
			ErrGuardManifestNoEdge, table, live.kind, live.persistence, live.partition,
			live.rowSecurity, live.forceRowSecurity, live.inherits, live.inherited,
			live.policies, live.rules, live.triggers)
	}

	probe, err := freshProbeRelationName()
	if err != nil {
		return fmt.Errorf("sqlstore: fresh bootstrap admission: name the %s shape probe: %w", table, err)
	}
	const savepoint = "olivares_fresh_rollout_probe"
	if _, err := q.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
		return fmt.Errorf("sqlstore: fresh bootstrap admission: open the %s probe savepoint: %w", table, err)
	}
	probeShape, perr := func() (pgRelationShapeCensus, error) {
		// COLLISION CHECK FIRST. A random name that already exists is not a name this
		// transaction may take, and adopting it is precisely the defect being corrected.
		existing, err := pgRelationOID(ctx, q, probe)
		if err == nil {
			return pgRelationShapeCensus{}, fmt.Errorf(
				"the shape probe identity is already taken by relation oid %d; refusing to read an existing object as this build's oracle", existing)
		}
		if !errors.Is(err, errPGRelationAbsent) {
			return pgRelationShapeCensus{}, err
		}
		// The DDL names its own table, and its `IF NOT EXISTS` is removed on purpose: the
		// probe must be CREATED here, so a name that turned out to be taken fails loudly
		// instead of yielding somebody else's relation.
		stmt := strings.Replace(strings.Replace(ddl, "IF NOT EXISTS ", "", 1), table, probe, 1)
		if _, err := q.ExecContext(ctx, stmt); err != nil { // #nosec G202 -- internal constant DDL with a generated identifier
			return pgRelationShapeCensus{}, fmt.Errorf("create the shape probe: %w", err)
		}
		oid, err := pgRelationOID(ctx, q, probe)
		if err != nil {
			return pgRelationShapeCensus{}, fmt.Errorf("resolve the shape probe just created: %w", err)
		}
		if oid == liveOID {
			return pgRelationShapeCensus{}, fmt.Errorf(
				"the shape probe resolved to the relation under test (oid %d); it would be comparing %s with itself", oid, table)
		}
		return pgRelationShape(ctx, q, oid)
	}()
	if _, rerr := q.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint); rerr != nil {
		return fmt.Errorf("sqlstore: fresh bootstrap admission: roll back the %s probe: %w", table, rerr)
	}
	if _, rerr := q.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint); rerr != nil {
		return fmt.Errorf("sqlstore: fresh bootstrap admission: release the %s probe savepoint: %w", table, rerr)
	}
	if perr != nil {
		return fmt.Errorf("sqlstore: fresh bootstrap admission: %s: %w", table, perr)
	}
	// The probe's own name appears inside its constraint and index definitions, so it is
	// mapped back before the comparison.
	want := strings.ReplaceAll(probeShape.contract(), probe, table)
	if got := live.contract(); got != want {
		return fmt.Errorf(
			"%w: %s does not have the contract this build renders.\n  stored:   %s\n  rendered: %s",
			ErrGuardManifestNoEdge, table, got, want)
	}
	return nil
}

// errPGRelationAbsent distinguishes "this name is free" from "the catalog could not be read",
// because the probe's collision check must not read a failure as an absence.
var errPGRelationAbsent = errors.New("sqlstore: relation is absent from the engine schema")

// freshProbeRelationName mints an identity no other application can be holding on purpose.
//
// 128 random bits, so the name is not derivable from the table under test and cannot be
// pre-created by anybody aiming at this check. It is still checked for collision before use: a
// name being improbable is not the same as a name being free, and this build does not read an
// object it did not create as its own oracle.
func freshProbeRelationName() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "olivares_fresh_probe_" + hex.EncodeToString(raw[:]), nil
}

func pgRelationOID(ctx context.Context, q dialect.Querier, relname string) (int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.oid::int8
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, dialect.EngineSchema, relname)
	if err != nil {
		return 0, err
	}
	var oid int64
	found := false
	if rows.Next() {
		found = true
		if err := rows.Scan(&oid); err != nil {
			_ = rows.Close()
			return 0, err
		}
	}
	if rows.Next() {
		_ = rows.Close()
		return 0, fmt.Errorf("relation %q resolves to more than one object in %q", relname, dialect.EngineSchema)
	}
	if err := closeRows(rows, "pg_class "+relname); err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("%w: %q", errPGRelationAbsent, relname)
	}
	return oid, nil
}

// pgRelationShapeCensus is the comparable contract of one PostgreSQL relation.
type pgRelationShapeCensus struct {
	kind, persistence                     string
	partition, rowSecurity                bool
	forceRowSecurity, inherits, inherited bool
	policies, rules, triggers             bool
	columns                               []string
	constraints                           []string
	indexes                               []string
}

// contract renders the parts that must be IDENTICAL between the live relation and the probe.
// The catalog flags are compared separately, because their diagnosis is more useful named.
func (c pgRelationShapeCensus) contract() string {
	return "columns[" + strings.Join(c.columns, "; ") + "] constraints[" +
		strings.Join(c.constraints, "; ") + "] indexes[" + strings.Join(c.indexes, "; ") + "]"
}

// pgRelationShape censuses ONE relation BY OID.
//
// By oid and not by name, so the object compared is the object resolved — a probe created in
// this transaction cannot be replaced under its own name between the CREATE and the read, and
// the comparison cannot silently change subject.
func pgRelationShape(ctx context.Context, q dialect.Querier, oid int64) (pgRelationShapeCensus, error) {
	var out pgRelationShapeCensus
	rows, err := q.QueryContext(ctx, `SELECT c.relkind::text, c.relpersistence::text, c.relispartition,
       c.relrowsecurity, c.relforcerowsecurity,
       EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhrelid = c.oid),
       EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhparent = c.oid),
       EXISTS (SELECT 1 FROM pg_catalog.pg_policy p WHERE p.polrelid = c.oid),
       EXISTS (SELECT 1 FROM pg_catalog.pg_rewrite r WHERE r.ev_class = c.oid),
       EXISTS (SELECT 1 FROM pg_catalog.pg_trigger t WHERE t.tgrelid = c.oid AND NOT t.tgisinternal)
FROM pg_catalog.pg_class c
WHERE c.oid = $1`, oid)
	if err != nil {
		return out, err
	}
	found := false
	if rows.Next() {
		found = true
		if err := rows.Scan(&out.kind, &out.persistence, &out.partition, &out.rowSecurity,
			&out.forceRowSecurity, &out.inherits, &out.inherited, &out.policies, &out.rules,
			&out.triggers); err != nil {
			_ = rows.Close()
			return out, err
		}
	}
	if err := closeRows(rows, fmt.Sprintf("pg_class oid %d", oid)); err != nil {
		return out, err
	}
	if !found {
		return out, fmt.Errorf("relation oid %d is absent", oid)
	}

	crows, err := q.QueryContext(ctx, `SELECT a.attname, pg_catalog.format_type(a.atttypid, a.atttypmod),
       a.attnotnull, COALESCE(pg_catalog.pg_get_expr(d.adbin, d.adrelid), '')
FROM pg_catalog.pg_attribute a
LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`, oid)
	if err != nil {
		return out, err
	}
	for crows.Next() {
		var name, typ, def string
		var notNull bool
		if err := crows.Scan(&name, &typ, &notNull, &def); err != nil {
			_ = crows.Close()
			return out, err
		}
		out.columns = append(out.columns, fmt.Sprintf("%s %s notnull=%t default=%q", name, typ, notNull, def))
	}
	if err := closeRows(crows, fmt.Sprintf("pg_attribute oid %d", oid)); err != nil {
		return out, err
	}

	// Constraint DEFINITIONS rather than names: PostgreSQL derives a CHECK constraint's name
	// from its relation, so a probe would never match by name and the comparison would be
	// vacuous. The definitions are sorted because their catalog order is not meaningful.
	korows, err := q.QueryContext(ctx, `SELECT pg_catalog.pg_get_constraintdef(con.oid)
FROM pg_catalog.pg_constraint con WHERE con.conrelid = $1`, oid)
	if err != nil {
		return out, err
	}
	for korows.Next() {
		var def string
		if err := korows.Scan(&def); err != nil {
			_ = korows.Close()
			return out, err
		}
		out.constraints = append(out.constraints, def)
	}
	if err := closeRows(korows, fmt.Sprintf("pg_constraint oid %d", oid)); err != nil {
		return out, err
	}
	sort.Strings(out.constraints)

	irows, err := q.QueryContext(ctx, `SELECT pg_catalog.pg_get_indexdef(x.indexrelid)
FROM pg_catalog.pg_index x WHERE x.indrelid = $1`, oid)
	if err != nil {
		return out, err
	}
	for irows.Next() {
		var def string
		if err := irows.Scan(&def); err != nil {
			_ = irows.Close()
			return out, err
		}
		out.indexes = append(out.indexes, def)
	}
	if err := closeRows(irows, fmt.Sprintf("pg_index oid %d", oid)); err != nil {
		return out, err
	}
	sort.Strings(out.indexes)
	return out, nil
}

// verifyFreshRolloutPreState re-establishes the R half of the admission inside the transaction
// that is about to create it, and reports whether the checkpoint is already there.
//
// It exists because "the classifier checked" and "this transaction checked" are different
// statements. The classifier's read ran in its own transaction and, on PostgreSQL, at READ
// COMMITTED: the migration lock and the deployment fence exclude cooperating writers, and they
// do NOT prove that an owner outside this protocol committed no DDL in between. That is not a
// hypothetical this correction can close, so the answer is to re-read rather than to claim an
// exclusion this build cannot demonstrate.
//
// It is a VERIFICATION, never a repair: it creates nothing, drops nothing and rewrites no
// receipt. When the three relations are absent it returns false and the caller's own DDL runs.
func verifyFreshRolloutPreState(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	controls []store.RolloutControl,
) (bool, error) {
	tables := []string{
		dialect.ControlRolloutStateTable,
		dialect.ControlRolloutTransitionTable,
		dialect.ControlRolloutClassificationTable,
	}
	var present []string
	for _, table := range tables {
		exists, err := witnessPresent(ctx, tx, dia, table)
		if err != nil {
			return false, fmt.Errorf("sqlstore: rollout classification: probe %q: %w", table, err)
		}
		if exists {
			present = append(present, table)
		}
	}
	switch len(present) {
	case 0:
		return false, nil
	case len(tables):
	default:
		return false, fmt.Errorf(
			"%w: %d of the %d rollout relations exist (%s) at the pre-v1 frontier. They are created together in one transaction, so a subset is damage rather than an interrupted commit; nothing has been created",
			ErrGuardManifestNoEdge, len(present), len(tables), strings.Join(present, ", "))
	}
	trackerPresent, err := coreTrackingRelationExists(ctx, tx, dia)
	if err != nil {
		return false, fmt.Errorf("sqlstore: rollout classification: inspect %s: %w", coreTrackingTable, err)
	}
	if err := verifyFreshRolloutCheckpoint(ctx, tx, dia, controls, trackerPresent); err != nil {
		return false, err
	}
	return true, nil
}

// guardEventFenceFacts are the two facts the fence projection needs, RESOLVED BY Open and passed
// in rather than re-derived here.
//
// Both are read from the APPLICATION pool before the migration lock — the role this pool
// authenticates as, and this server's major — because that is the subject the fence's
// rewritability question is about. Re-reading them on the owner connection inside the lock would
// answer about a different role, and guessing either one would make an "installed" verdict a
// statement about a database nobody looked at. On SQLite they are zero and unused: the reserved
// family is empty there, because SQLite has no event triggers.
type guardEventFenceFacts struct {
	// AppRole is posture.Role from the application pool. Open guarantees it is non-empty on
	// PostgreSQL: every path that cannot resolve it refuses before reaching the lock.
	AppRole string
	// Major is the server major Open already read and already refused when unsupported.
	Major int
}

// verifyReservedFamilies judges every reserved family AS A SET, before anything can commit.
//
// THE DEFECT IT REPLACES, measured by an independent review on PostgreSQL 16.15: the three fence
// identities were each excused on their own, so a database carrying ONLY the canonical handler
// was admitted as `fresh-empty`. Under the default posture it reached core v9 and all three
// rollout relations before the late verifier refused it; under `GuardEventFenceOff` it was not
// refused at all and served.
//
// THREE ANSWERS, AND ONLY THREE:
//
//   - NONE of the family present — the ordinary case, and the only one that was ever really
//     about absence. Admitted, and nothing is projected.
//   - ALL of it present — admitted only if the EXISTING projection and judge say `installed`,
//     using the app role and major Open resolved. Any other verdict is a refusal.
//   - A PROPER SUBSET — refused. Nobody installs half a fence, so half a fence is either an
//     interrupted operator step or a removal, and neither is a prefix of an installation.
//
// IT IGNORES THE SERVICE POLICY DELIBERATELY. `GuardEventFenceOff` switches off what a RUNNING
// deployment asserts about its fence; it cannot make a partial set of reserved names into a
// checkpoint this build would have produced. The pre-serve verification keeps its own policy
// semantics untouched — this is a provenance question asked once, at max0, and it is asked in the
// classifier's own transaction on the connection that holds the migration lock.
func verifyReservedFamilies(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	set *managedObjectSet,
	found map[string][]managedObject,
	fence guardEventFenceFacts,
	admission *freshBootstrapAdmission,
) error {
	for _, family := range sortedMapKeys(found) {
		members := found[family]
		declared := set.reservedFamilySize(family)
		if len(members) != declared {
			names := make([]string, 0, len(members))
			for _, m := range members {
				names = append(names, m.String())
			}
			sort.Strings(names)
			return fmt.Errorf(
				"%w: %d of the %d identities of the reserved %s family exist (%s) with no core migration recorded. They are installed together by one operator step, so a proper subset is an interrupted provisioning or a removal — not a prefix of an installation this build can build on. Nothing has been created or altered: complete the step or remove what is left of it",
				ErrGuardManifestNoEdge, len(members), declared, family, strings.Join(names, ", "))
		}
		switch family {
		case guardEventFenceFamily:
			if err := verifyReservedGuardEventFence(ctx, tx, dia, fence); err != nil {
				return err
			}
		default:
			return fmt.Errorf(
				"sqlstore: fresh bootstrap admission: the reserved family %q has no admission verifier, and an unjudged reserved family is an exception nobody checked",
				family)
		}
		for _, m := range members {
			admission.OperatorProvisioned = append(admission.OperatorProvisioned, m.String())
		}
		sort.Strings(admission.OperatorProvisioned)
	}
	return nil
}

// verifyReservedGuardEventFence asks the EXISTING fence verification whether the complete family
// this database carries is the one this build declares.
//
// It reuses projectGuardEventFence and judgeGuardEventFence unchanged, so a fence admitted here
// is admitted by the same oracle the pre-serve leg uses and by the same reasons. What it does not
// reuse is verifyGuardEventFence's POLICY branch, and that omission is the point: `off` is a
// statement about what a running deployment asserts, not a license to treat a divergent set of
// reserved names as an authored checkpoint.
func verifyReservedGuardEventFence(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	fence guardEventFenceFacts,
) error {
	if dia.Name() != store.EnginePostgres {
		// Unreachable today — the family is PostgreSQL-only — and refused rather than passed,
		// because an engine that cannot carry the objects cannot have found them either.
		return fmt.Errorf(
			"sqlstore: fresh bootstrap admission: the %s family was found on engine %q, which has no event triggers",
			guardEventFenceFamily, dia.Name())
	}
	obs, err := projectGuardEventFence(ctx, tx, fence.AppRole, fence.Major)
	if err != nil {
		// A READING THAT FAILED IS NOT AN ABSENCE, and it is not an installation either. The
		// family is present, so its provenance is exactly what this admission cannot skip.
		return fmt.Errorf(
			"%w: the reserved %s family is present and could not be read, so whether it is the fence this build declares was not established: %v. Nothing has been created or altered",
			ErrGuardManifestNoEdge, guardEventFenceFamily, err)
	}
	status := judgeGuardEventFence(obs)
	if status.Verdict == guardEventFenceInstalled {
		return nil
	}
	return fmt.Errorf(
		"%w: the reserved %s family is complete and is not the fence this build declares (%s): %s. It is admitted before core v1 only because an operator installs it with the maintenance role, and that admission is about the fence this build verifies — not about any object wearing those names. Nothing has been created or altered",
		ErrGuardManifestNoEdge, guardEventFenceFamily, status.Verdict,
		strings.Join(status.Reasons, "; "))
}
