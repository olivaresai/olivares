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
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/store"
)

// The pre-v1 admission on the engine that has the parts SQLite does not: a schema namespace, a
// role layer, routines resolved by signature, and a catalog rather than stored DDL text.
//
// Three of the frontier's clauses can only be tested here at all:
//
//   - a FOREIGN SCHEMA carrying the same names is another owner's database and not a collision,
//     because the engine resolves its own objects in dialect.EngineSchema and nowhere else;
//   - the tenancy FUNCTION is what core v1 creates on PostgreSQL (there is no _scope_tenant), and
//     `CREATE OR REPLACE FUNCTION` replaces exactly one signature — so an exact or divergent
//     zero-argument olivares_block_mutation() is a collision and a one-argument overload is not;
//   - "empty" must be cardinality zero and not a read a row-level-security policy filtered.

// pgLogicalSnapshot is the state a refused boot must leave untouched: relations, columns,
// defaults, constraints, indexes, triggers, policies, routines and every value of every ordinary
// table of the managed schema, plus the same for a foreign schema when one is present.
//
// It is LOGICAL. Page images, OIDs, transaction counters and the sequence allocator are not
// compared, and cannot be: a rolled-back probe legitimately consumes identifiers.
func pgLogicalSnapshot(t *testing.T, db *sql.DB, schemas ...string) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	for _, schema := range schemas {
		rows, err := db.QueryContext(ctx, `SELECT c.relname, c.relkind::text, c.relpersistence::text,
       c.relrowsecurity, c.relforcerowsecurity
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 ORDER BY c.relname, c.relkind`, schema)
		if err != nil {
			t.Fatal(err)
		}
		var tables []string
		for rows.Next() {
			var name, kind, persistence string
			var rls, force bool
			if err := rows.Scan(&name, &kind, &persistence, &rls, &force); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "REL\t%s\t%s\t%s\t%s\t%t\t%t\n", schema, name, kind, persistence, rls, force)
			if kind == "r" {
				tables = append(tables, name)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		_ = rows.Close()
		for _, q := range []struct{ label, sql string }{
			{"COL", `SELECT c.relname || ' ' || a.attname || ' ' || pg_catalog.format_type(a.atttypid, a.atttypmod) || ' notnull=' || a.attnotnull::text || ' default=' || COALESCE(pg_catalog.pg_get_expr(d.adbin, d.adrelid), '')
FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE n.nspname = $1 AND a.attnum > 0 AND NOT a.attisdropped`},
			{"CON", `SELECT c.relname || ' ' || con.conname || ' ' || pg_catalog.pg_get_constraintdef(con.oid)
FROM pg_catalog.pg_constraint con JOIN pg_catalog.pg_class c ON c.oid = con.conrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1`},
			{"IDX", `SELECT pg_catalog.pg_get_indexdef(x.indexrelid)
FROM pg_catalog.pg_index x JOIN pg_catalog.pg_class c ON c.oid = x.indrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1`},
			{"TRG", `SELECT c.relname || ' ' || t.tgname || ' ' || t.tgenabled::text
FROM pg_catalog.pg_trigger t JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND NOT t.tgisinternal`},
			{"POL", `SELECT c.relname || ' ' || p.polname
FROM pg_catalog.pg_policy p JOIN pg_catalog.pg_class c ON c.oid = p.polrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1`},
			{"FUN", `SELECT p.proname || '/' || p.pronargs::text || ' ' || p.prokind::text
FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1`},
		} {
			for _, line := range pgQueryStrings(t, db, q.sql, schema) {
				fmt.Fprintf(&b, "%s\t%s\t%s\n", q.label, schema, line)
			}
		}
		sort.Strings(tables)
		for _, table := range tables {
			for _, line := range pgQueryStrings(t, db,
				`SELECT (to_jsonb(x.*))::text FROM `+schema+`.`+table+` x`) {
				fmt.Fprintf(&b, "ROW\t%s.%s\t%s\n", schema, table, line)
			}
		}
	}
	return b.String()
}

// pgQueryStrings collects a single text column, sorted, so catalog order never enters the
// comparison.
func pgQueryStrings(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("snapshot query %.60q: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		out = append(out, value.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func pgRelationExists(t *testing.T, db *sql.DB, schema, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, schema, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// TestPostgresFreshBootstrapInventoryCoversTheInstalledProfile is the PostgreSQL half of the
// empirical completeness proof, and its SCOPE is stated rather than implied.
//
// WHAT IT PROVES: for the profiles it installs — core-only, and one synthetic module that
// registers an entity, a rollout control and a migration filesystem — every object a real
// installation leaves in the managed schema is named by the inventory. The census is taken from
// pg_class (relations, indexes AND sequences), pg_proc by identity signature, ordinary pg_trigger
// rows and pg_event_trigger, so a scoped object is not silently outside the sweep.
//
// WHAT IT DOES NOT PROVE, said out loud because the earlier wording ("every catalog object")
// claimed it: this is not the product's own registration callback. `core` cannot import
// `modules` — the module packages import core — so no test in this package can install the real
// enabled-module set. The nearest available evidence is
// TestFreshBootstrapInventoryReadsTheRepositoryModuleMigrations, which reads the ACTUAL on-disk
// module migration files through the real plan preparation; that is a static prepared-plan
// control and is not relabelled as a catalog control.
func TestPostgresFreshBootstrapInventoryCoversTheInstalledProfile(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register func(store.ExtensionRegistry) error
	}{
		{"core-only registrar", nil},
		{"synthetic module registrar with file migrations", registerFreshBootstrapModule},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, dia := accessEvidencePGStore(t)
			st, err := Open(ctx, cfg, tc.register)
			if err != nil {
				t.Fatalf("fresh Open: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			reg := freshBootstrapRegistry(t, tc.register)
			set, err := buildManagedObjectSet(dia, coreDescriptors(), reg, freshBootstrapPlans(t, dia, reg))
			if err != nil {
				t.Fatalf("build the managed object set: %v", err)
			}

			var missing []string
			// pg_class WITHOUT a relkind filter: sequences were excluded by the earlier
			// version, which is exactly the kind of quiet narrowing this control exists to
			// prevent.
			for _, line := range pgQueryStrings(t, owner, `SELECT c.relname || '|' || c.relkind::text
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1`, dialect.EngineSchema) {
				name, kind, _ := strings.Cut(line, "|")
				class := managedClassRelation
				if kind == "i" || kind == "I" {
					class = managedClassIndex
				}
				if _, ok := set.lookup(class, name, ""); ok {
					continue
				}
				// A constraint-backing index is not an object anybody declares: it is the
				// primary key or unique constraint of a relation already named, created by
				// the engine itself.
				if (kind == "i" || kind == "I") && pgIndexIsConstraintBacked(t, owner, name) {
					continue
				}
				missing = append(missing, kind+" "+name)
			}
			// Routines BY IDENTITY SIGNATURE, which is what the census compares.
			for _, line := range pgQueryStrings(t, owner, `SELECT p.proname || '|' || pg_catalog.pg_get_function_identity_arguments(p.oid)
FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1`, dialect.EngineSchema) {
				name, signature, _ := strings.Cut(line, "|")
				if _, ok := set.lookup(managedClassRoutine, name, signature); !ok {
					missing = append(missing, "function "+name+"("+signature+")")
				}
			}
			// Ordinary triggers. They are scoped to their relation and the ADMISSION census
			// deliberately ignores them, but the INVENTORY must still name them: a managed
			// trigger nobody declared would otherwise never be noticed here.
			for _, name := range pgQueryStrings(t, owner, `SELECT t.tgname
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND NOT t.tgisinternal`, dialect.EngineSchema) {
				if _, ok := set.lookup(managedClassTrigger, name, ""); !ok {
					missing = append(missing, "trigger "+name)
				}
			}
			for _, name := range pgQueryStrings(t, owner, `SELECT evtname FROM pg_catalog.pg_event_trigger`) {
				if _, ok := set.lookup(managedClassEventTrigger, name, ""); !ok {
					missing = append(missing, "event trigger "+name)
				}
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				t.Fatalf("a real PostgreSQL installation of this profile left %d object(s) the managed inventory does not name: %s",
					len(missing), strings.Join(missing, ", "))
			}
		})
	}
}

func pgIndexIsConstraintBacked(t *testing.T, db *sql.DB, index string) bool {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pg_catalog.pg_constraint con
JOIN pg_catalog.pg_class i ON i.oid = con.conindid
JOIN pg_catalog.pg_namespace n ON n.oid = i.relnamespace
WHERE n.nspname = $1 AND i.relname = $2`, dialect.EngineSchema, index).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// TestPostgresFreshBootstrapRefusesAPrecreatedManagedObject is the refusal on the engine whose
// exact contract is a catalog projection, with the two cases only PostgreSQL has: the tenancy
// FUNCTION core v1 creates, exact and divergent.
func TestPostgresFreshBootstrapRefusesAPrecreatedManagedObject(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed []string
		// realSeed renders the fixture from a product constructor, so an "exact" control is
		// exact by construction rather than by a body somebody typed out.
		realSeed func(dialect.Dialect) []string
	}{
		{
			name: "orgs in a foreign shape holding a row",
			seed: []string{
				"CREATE TABLE public.orgs (review text NOT NULL)",
				"INSERT INTO public.orgs (review) VALUES ('preserve-me')",
			},
		},
		{
			// The exact function core v1 creates, RENDERED BY THE REAL CONSTRUCTOR. An
			// earlier version of this case wrote the body by hand and got it wrong —
			// `RAISE EXCEPTION 'append-only'` where postgresDialect.TenancyStmts emits
			// `'table is append-only'` — so it was calling a second DIVERGENT body
			// canonical. Refused for the same reason an exactly precreated table is:
			// `CREATE OR REPLACE FUNCTION` would silently adopt a routine every installed
			// guard would then point at.
			name:     "the tenancy trigger function, exactly as core v1 renders it",
			realSeed: func(dia dialect.Dialect) []string { return dia.TenancyStmts() },
		},
		{
			name: "the same name with a different body",
			seed: []string{`CREATE OR REPLACE FUNCTION public.olivares_block_mutation() RETURNS trigger AS $$
BEGIN RETURN NEW; END $$ LANGUAGE plpgsql`},
		},
		{
			name: "a foreign index wearing the name core v4 drops unqualified",
			seed: []string{
				"CREATE TABLE public.review_unrelated (value text NOT NULL)",
				"INSERT INTO public.review_unrelated (value) VALUES ('preserve-me')",
				"CREATE INDEX federation_configs_scope_uniq ON public.review_unrelated(value)",
			},
		},
		{
			name: "a view wearing a managed name",
			seed: []string{"CREATE VIEW public.audit_events AS SELECT 1 AS review"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, dia := accessEvidencePGStore(t)
			seed := tc.seed
			if tc.realSeed != nil {
				seed = tc.realSeed(dia)
			}
			for _, stmt := range seed {
				mustExec(t, owner, stmt)
			}
			before := pgLogicalSnapshot(t, owner, dialect.EngineSchema)

			st, err := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			if err == nil {
				t.Fatal("Open admitted a managed object that predates every core migration")
			}
			if !errors.Is(err, ErrGuardManifestNoEdge) &&
				!errors.Is(err, ErrGuardControlPlaneBootstrapInconsistent) {
				t.Fatalf("Open error = %v, want the boot classification's own refusal", err)
			}
			if pgRelationExists(t, owner, dialect.EngineSchema, dialect.ControlRolloutStateTable) {
				t.Fatal("the refusal created the rollout relations: the first product commit happened anyway")
			}
			if pgRelationExists(t, owner, dialect.EngineSchema, coreTrackingTable) {
				t.Fatal("the refusal created the core tracker")
			}
			if got := pgLogicalSnapshot(t, owner, dialect.EngineSchema); got != before {
				t.Fatalf("the refusal changed durable state.\n--- before ---\n%s\n--- after ---\n%s", before, got)
			}
		})
	}
}

// TestPostgresFreshBootstrapRefusesAPrecreatedCoreV10Routine is the admission half of the
// core v10 inventory entry, and it is the reason that entry is worth adding.
//
// Both routines are SECURITY-sensitive: the lock function is SECURITY DEFINER and the
// retention function is what the H trigger executes to make User authority permanent. An
// object of either identity sitting in the schema before the first product migration is a
// foreign object wearing a name this build creates. Admitting it would leave every
// installed guard pointing at somebody else's body, so the boot must refuse before its
// first durable change — with the tracker, the rollout relations and H all still absent.
//
// The seeds are rendered FROM THE PRODUCTION CONSTANTS, so "exact twin" is exact by
// construction rather than by a body typed into a test.
func TestPostgresFreshBootstrapRefusesAPrecreatedCoreV10Routine(t *testing.T) {
	// The control comes first and on the same fixture: without the twin, this database
	// takes the ordinary path all the way through the current supported version. Without it, a refusal below would
	// not distinguish "the twin was refused" from "this fixture never boots".
	t.Run("control: no foreign twin", func(t *testing.T) {
		ctx := context.Background()
		cfg, owner, dia := accessEvidencePGStore(t)
		st, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("the empty max0 fixture refused its own intended path: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		if got := postgresMaxCoreVersion(t, owner, dia); got != coreSupportedMigrationVersion {
			t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
		}
		for _, name := range []string{
			"olivares_lock_core_user_authority", "olivares_retain_user_authority",
		} {
			if n := len(pgRoutineSignatures(t, owner, name)); n != 1 {
				t.Fatalf("the intended path left %d %s routine(s), want exactly this build's own", n, name)
			}
		}
	})

	for _, tc := range []struct {
		name     string
		routine  string
		realSeed func() []string
	}{
		{
			name:     "the User authority lock function, exactly as core v10 renders it",
			routine:  "olivares_lock_core_user_authority",
			realSeed: func() []string { return []string{postgresUserAuthorityLockDDL} },
		},
		{
			name:     "the User authority retention function, exactly as core v10 renders it",
			routine:  "olivares_retain_user_authority",
			realSeed: func() []string { return []string{postgresUserAuthorityRetentionDDL} },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, _ := accessEvidencePGStore(t)
			for _, stmt := range tc.realSeed() {
				mustExec(t, owner, stmt)
			}
			before := pgLogicalSnapshot(t, owner, dialect.EngineSchema)

			st, err := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			if err == nil {
				t.Fatal("Open admitted a foreign routine wearing a core v10 identity that predates every core migration")
			}
			// The state claims come first and do not stop the run: "refused before any
			// effect" is the property, and when it breaks the error identity is the next
			// thing worth reading rather than a reason to stop looking.
			if pgRelationExists(t, owner, dialect.EngineSchema, coreTrackingTable) {
				t.Error("the refusal created the core tracker: the boot advanced before it stopped")
			}
			if pgRelationExists(t, owner, dialect.EngineSchema, dialect.ControlRolloutStateTable) {
				t.Error("the refusal created the rollout relations: the first product commit happened anyway")
			}
			if pgRelationExists(t, owner, dialect.EngineSchema, userAuthorityDescriptor.Table) {
				t.Error("the refusal created H")
			}
			if !errors.Is(err, ErrGuardManifestNoEdge) &&
				!errors.Is(err, ErrGuardControlPlaneBootstrapInconsistent) {
				t.Errorf("Open error = %v, want the boot classification's own refusal", err)
			}
			if !strings.Contains(err.Error(), tc.routine) {
				t.Errorf("the refusal does not name the object it refused: %v", err)
			}
			if got := pgLogicalSnapshot(t, owner, dialect.EngineSchema); got != before {
				t.Errorf("the refusal changed durable state.\n--- before ---\n%s\n--- after ---\n%s", before, got)
			}
		})
	}
}

// TestPostgresFreshBootstrapDoesNotSeizeForeignCoreV10Signatures is what keeps the two
// entries above from being a name ban.
//
// A routine's identity is its name AND its input signature. Naming two more identities must
// not turn the census into a prefix sweep: another product's
// olivares_retain_user_authority(text) is not an object this build creates, so the managed
// census must not match it and the max0 admission must not refuse the install carrying it.
//
// It asserts the ADMISSION, not the whole boot, and that boundary is deliberate — the v10
// authority verifier keeps its own by-name function projection, which is a different
// subsystem's contract and is pinned separately below.
func TestPostgresFreshBootstrapDoesNotSeizeForeignCoreV10Signatures(t *testing.T) {
	ctx := context.Background()
	_, owner, dia := accessEvidencePGStore(t)
	for _, stmt := range []string{
		`CREATE FUNCTION public.olivares_lock_core_user_authority(review boolean) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$ SELECT review $$`,
		`CREATE FUNCTION public.olivares_retain_user_authority(review text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$ SELECT review $$`,
	} {
		mustExec(t, owner, stmt)
	}

	// The registry Open closes, core invariants included, so the census is built from the
	// same declaration the boot uses.
	set, err := buildManagedObjectSet(dia, coreDescriptors(), freshBootstrapRegistry(t, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	found, cerr := censusManagedNamespace(ctx, owner, dia, set)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if len(found) != 0 {
		t.Fatalf("foreign routines of signatures this build never declares were taken for its core v10 identities: %v", found)
	}
	graph, gerr := guardEditionGraphFor(coreOnlyAccessEvidenceManifest(t))
	if gerr != nil {
		t.Fatal(gerr)
	}
	plan, perr := classifyAccessEvidenceBoot(ctx, owner, dia, graph, coreDescriptors(),
		freshBootstrapRegistry(t, nil), nil, guardEventFenceFacts{})
	if perr != nil {
		t.Fatalf("the max0 admission refused foreign overloads of a different signature: %v", perr)
	}
	if plan.Class != accessEvidenceStartFreshEmpty || plan.FreshBootstrap == nil {
		t.Fatalf("class = %s (admission=%v), want %s with the max0 admission proved",
			plan.Class, plan.FreshBootstrap != nil, accessEvidenceStartFreshEmpty)
	}
}

// TestPostgresUserAuthorityGuardStillRefusesANameOverload PINS an existing boundary this
// change did not touch, so losing it turns a suite red rather than passing silently.
//
// Core v10's authority verifier projects its functions BY NAME and refuses a name carrying
// more than one overload, because one function identity carries one footprint. That is
// stricter than the managed census above, and deliberately so: it is the subsystem that
// attests the SECURITY DEFINER lock and the retention handler, not the pre-v1 frontier.
//
// The distinction matters for what the inventory entry claims. The census does not seize a
// foreign signature; the boot may still refuse it, for a reason that predates this entry and
// belongs to another contract.
//
// ⛔ THE BOOT'S REFUSAL MOVED, AND THE ASSERTION MOVED WITH IT (2026-09-07). This test used to
// read `more than one overload` out of Open's error, and that string was the LATE verifier's —
// reached after classifyRolloutControls had committed the three rollout relations and core v1
// through v9 had committed. Root ratified moving the refusal in front of every effect, so Open
// now stops at the pre-effect compatibility preflight and the estate is untouched; asserting the
// old string would be asserting that the defect is still there.
//
// Both halves are kept, and deliberately: the BOOT's refusal is asserted by its typed sentinel
// AND by the absence of the objects a late refusal would have left, and the by-name PROJECTION
// that produced the old string is pinned directly, on a database carrying two routines of that
// name. A projection that stopped refusing would fail here even though no boot reaches it any
// more.
func TestPostgresUserAuthorityGuardStillRefusesANameOverload(t *testing.T) {
	ctx := context.Background()
	cfg, owner, _ := accessEvidencePGStore(t)
	mustExec(t, owner, `CREATE FUNCTION public.olivares_lock_core_user_authority(review boolean) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$ SELECT review $$`)

	st, err := Open(ctx, cfg, nil)
	if st != nil {
		_ = st.Close()
	}
	if err == nil {
		t.Fatal("Open admitted a second overload of an authority function name: one identity carries one footprint")
	}
	if !errors.Is(err, ErrUserAuthorityLockNameIncompatible) {
		t.Errorf("Open error = %v, want the User authority lock's own pre-effect compatibility refusal", err)
	}
	// ZERO EFFECTS, which is the property the move exists for and the one a new expected
	// string would not have measured.
	for _, relation := range []string{
		dialect.ControlRolloutStateTable, coreTrackingTable, userAuthorityDescriptor.Table,
	} {
		if pgRelationExists(t, owner, dialect.EngineSchema, relation) {
			t.Errorf("the refusal created %s: the boot committed before it stopped", relation)
		}
	}
	// AND THE PROJECTION ITSELF, on a database that really does carry two routines of that
	// name — this build's own beside the foreign one — so the pin does not depend on any boot
	// reaching it.
	mustExec(t, owner, postgresUserAuthorityLockDDL)
	if _, _, perr := projectGuardFunction(ctx, owner, dialect.EngineSchema,
		"olivares_lock_core_user_authority"); perr == nil ||
		!strings.Contains(perr.Error(), "more than one overload") {
		t.Fatalf("the authority projection over two overloads = %v, want its own overload refusal", perr)
	}
}

// TestPostgresFreshBootstrapAdmitsAnUnrelatedNeighbouringRoutineName is the other direction,
// and the one a prefix sweep would get wrong at an operator's expense.
//
// olivares_retain_user_authority_review() shares a prefix with a managed identity and is a
// different name. Nothing in this build creates it, so the whole boot must proceed and leave
// it untouched beside this build's own routines.
func TestPostgresFreshBootstrapAdmitsAnUnrelatedNeighbouringRoutineName(t *testing.T) {
	ctx := context.Background()
	cfg, owner, dia := accessEvidencePGStore(t)
	mustExec(t, owner, `CREATE FUNCTION public.olivares_retain_user_authority_review() RETURNS text
LANGUAGE sql IMMUTABLE AS $$ SELECT 'review' $$`)

	for _, pass := range []string{"first Open", "reopen"} {
		st, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("%s: a neighbouring routine name this build never declares refused the boot: %v", pass, err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got := postgresMaxCoreVersion(t, owner, dia); got != coreSupportedMigrationVersion {
		t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
	}
	if got := pgRoutineSignatures(t, owner, "olivares_retain_user_authority_review"); len(got) != 1 || got[0] != "" {
		t.Fatalf("the neighbouring routine = %q, want it preserved untouched", got)
	}
	// And this build's own two identities exist exactly once each beside it.
	for _, name := range []string{"olivares_lock_core_user_authority", "olivares_retain_user_authority"} {
		if got := pgRoutineSignatures(t, owner, name); len(got) != 1 {
			t.Fatalf("%s signatures = %q, want exactly this build's own", name, got)
		}
	}
}

// pgRoutineSignatures lists a routine name's input signatures, sorted, in the same spelling
// the managed census projects them from proargtypes.
func pgRoutineSignatures(t *testing.T, owner *sql.DB, name string) []string {
	t.Helper()
	return pgQueryStrings(t, owner, `SELECT COALESCE(pg_catalog.array_to_string(ARRAY(
  SELECT pg_catalog.format_type(t, NULL) FROM pg_catalog.unnest(p.proargtypes) AS t), ', '), '')
FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = $2`, dialect.EngineSchema, name)
}

// TestPostgresFreshBootstrapPreservesWhatItDoesNotAdminister is the preservation half, on the
// clause a name-only sweep gets wrong in the EXPENSIVE direction: by refusing an install it has
// no business refusing.
//
// A foreign SCHEMA whose tables are named exactly like this build's is another owner's database.
// The engine resolves its own objects in dialect.EngineSchema and nowhere else, so those names are
// not collisions, and the install must leave that schema byte-for-byte alone.
func TestPostgresFreshBootstrapPreservesWhatItDoesNotAdminister(t *testing.T) {
	ctx := context.Background()
	cfg, owner, dia := accessEvidencePGStore(t)
	mustExec(t, owner, "CREATE SCHEMA review_foreign")
	for _, stmt := range []string{
		"CREATE TABLE review_foreign.orgs (review text NOT NULL)",
		"INSERT INTO review_foreign.orgs (review) VALUES ('another-owner')",
		"CREATE TABLE review_foreign." + coreTrackingTable + " (version integer PRIMARY KEY)",
		"INSERT INTO review_foreign." + coreTrackingTable + " (version) VALUES (42)",
		"CREATE TABLE review_foreign." + dialect.ControlRolloutStateTable + " (control_key text PRIMARY KEY)",
		"CREATE TABLE public.review_unrelated (value text NOT NULL)",
		"INSERT INTO public.review_unrelated (value) VALUES ('preserve-me')",
	} {
		mustExec(t, owner, stmt)
	}
	foreignBefore := pgLogicalSnapshot(t, owner, "review_foreign")

	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("objects this build does not administer blocked an install: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if got := pgLogicalSnapshot(t, owner, "review_foreign"); got != foreignBefore {
		t.Fatalf("the install changed a foreign schema.\n--- before ---\n%s\n--- after ---\n%s",
			foreignBefore, got)
	}
	var value string
	if err := owner.QueryRowContext(ctx, "SELECT value FROM public.review_unrelated").Scan(&value); err != nil {
		t.Fatalf("read the foreign row: %v", err)
	}
	if value != "preserve-me" {
		t.Fatalf("the foreign row = %q", value)
	}
	if got := postgresMaxCoreVersion(t, owner, dia); got != coreSupportedMigrationVersion {
		t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
	}
}

// TestPostgresFreshBootstrapCoexistsWithAnUnrelatedRoutineOverload is FB-1, and it replaces a
// test that recorded a defect and skipped itself if anybody fixed it.
//
// THE MEASURED DEFECT: with only an unrelated `public.olivares_block_mutation(text)` present, the
// max0 admission correctly admitted, the three rollout relations committed, and
// verifyBootstrapFunction then refused the boot — because its projection selected by schema and
// name alone and its decoder refuses an ambiguous result. A foreign overload is a different
// function that this build never creates, never replaces and never points a trigger at, so the
// ratified end-to-end coexistence case failed on an object nobody had a claim on.
//
// THE INTENDED OUTCOME, asserted here end to end: the install completes, reopens, and the foreign
// overload is still there beside the zero-argument function this build created.
func TestPostgresFreshBootstrapCoexistsWithAnUnrelatedRoutineOverload(t *testing.T) {
	ctx := context.Background()
	cfg, owner, dia := accessEvidencePGStore(t)
	mustExec(t, owner, `CREATE FUNCTION public.olivares_block_mutation(review text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$ SELECT review $$`)

	// The admission itself must not seize the signature either.
	graph, gerr := guardEditionGraphFor(coreOnlyAccessEvidenceManifest(t))
	if gerr != nil {
		t.Fatal(gerr)
	}
	plan, cerr := classifyAccessEvidenceBoot(ctx, owner, dia, graph, coreDescriptors(),
		coreOnlyRegistry(t), nil, pgFenceFacts(t, guardPGProbe(t, cfg.DSN)))
	if cerr != nil {
		t.Fatalf("the max0 admission seized a routine signature this build never creates: %v", cerr)
	}
	if plan.Class != accessEvidenceStartFreshEmpty || plan.FreshBootstrap == nil {
		t.Fatalf("class = %s (admission=%v), want %s with the max0 admission proved",
			plan.Class, plan.FreshBootstrap != nil, accessEvidenceStartFreshEmpty)
	}

	for _, pass := range []string{"first Open", "reopen"} {
		st, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("%s: an unrelated overload refused the boot: %v", pass, err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got := postgresMaxCoreVersion(t, owner, dia); got != coreSupportedMigrationVersion {
		t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
	}
	signatures := pgQueryStrings(t, owner, `SELECT COALESCE(pg_catalog.array_to_string(ARRAY(
  SELECT pg_catalog.format_type(t, NULL) FROM pg_catalog.unnest(p.proargtypes) AS t), ', '), '')
FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'public' AND p.proname = $1`, dialect.BlockMutationFn)
	if len(signatures) != 2 || signatures[0] != "" || signatures[1] != "text" {
		t.Fatalf("olivares_block_mutation signatures = %q, want the foreign (text) overload beside this build's own zero-input function",
			signatures)
	}
}

// TestPostgresBootstrapFunctionGuardStillRefusesItsOwnIdentity is the other half of FB-1: the
// narrowing must not have weakened the guard it narrowed.
//
// Both cases run on a database whose core v1 is ALREADY RECORDED, so the max0 admission is not
// what decides them — verifyBootstrapFunction is. A canonical function is accepted under the
// conditions it was always accepted under; one whose body was replaced is still refused, and the
// refusal still names the divergence rather than replacing the object every installed guard
// points at.
func TestPostgresBootstrapFunctionGuardStillRefusesItsOwnIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		damage  string
		wantErr bool
	}{
		{name: "the canonical function core v1 created"},
		{
			name: "the same identity with a replaced body",
			damage: `CREATE OR REPLACE FUNCTION public.olivares_block_mutation() RETURNS trigger AS $$
BEGIN RETURN NEW; END $$ LANGUAGE plpgsql`,
			wantErr: true,
		},
		{
			name: "the same identity turned into a security-definer",
			damage: `CREATE OR REPLACE FUNCTION public.olivares_block_mutation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER AS $$ BEGIN RAISE EXCEPTION 'table is append-only'; END; $$`,
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, dia := accessEvidencePGStore(t)
			// Core v1 through the product's own plan: it is what creates the canonical
			// function, so this is not a fixture anybody typed out.
			plan := buildCoreMigrations(dia, coreDescriptors(), nil, nil)
			if err := migrate.Apply(ctx, owner, dia, coreTrackingTable, plan[:1]); err != nil {
				t.Fatalf("apply the real core v1: %v", err)
			}
			if tc.damage != "" {
				mustExec(t, owner, tc.damage)
			}
			st, err := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("the canonical bootstrap function was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Open accepted a bootstrap function whose declared form changed")
			}
			if !errors.Is(err, ErrGuardBootstrapFunctionDivergent) {
				t.Fatalf("Open error = %v, want the bootstrap function's own divergence refusal", err)
			}
		})
	}
}

// TestPostgresFreshBootstrapRefusesACollisionInPgClass is FB-2's PostgreSQL half.
//
// pg_class holds relations AND indexes in one namespace, and the reviewed candidate kept two
// buckets. Both witnesses below reached a DURABLE product checkpoint before failing: the index
// named `orgs` reached v2 with three rollout relations and core v1, and the table named
// `federation_configs_scope_uniq` — the name core v4 drops unqualified — reached v4 with core v3.
func TestPostgresFreshBootstrapRefusesACollisionInPgClass(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seed    []string
		survive string
	}{
		{
			name: "an index wearing a managed table's name",
			seed: []string{
				"CREATE TABLE public.review_unrelated (value text NOT NULL)",
				"INSERT INTO public.review_unrelated (value) VALUES ('preserve-me')",
				"CREATE INDEX orgs ON public.review_unrelated(value)",
			},
			survive: "orgs",
		},
		{
			name: "a table wearing the name core v4 drops unqualified",
			seed: []string{
				"CREATE TABLE public.federation_configs_scope_uniq (value text NOT NULL)",
				"INSERT INTO public.federation_configs_scope_uniq (value) VALUES ('preserve-me')",
			},
			survive: "federation_configs_scope_uniq",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, _ := accessEvidencePGStore(t)
			for _, stmt := range tc.seed {
				mustExec(t, owner, stmt)
			}
			before := pgLogicalSnapshot(t, owner, dialect.EngineSchema)

			st, err := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			t.Logf("REFUSAL|%v", err)
			if err == nil {
				t.Fatal("Open admitted a name this build's own DDL would take or destroy")
			}
			if pgRelationExists(t, owner, dialect.EngineSchema, dialect.ControlRolloutStateTable) {
				t.Fatal("the refusal created the rollout relations")
			}
			if pgRelationExists(t, owner, dialect.EngineSchema, coreTrackingTable) {
				t.Fatal("the refusal created the core tracker")
			}
			if !pgRelationExists(t, owner, dialect.EngineSchema, tc.survive) {
				t.Fatalf("the refusal destroyed %s, which it exists to preserve", tc.survive)
			}
			if got := pgLogicalSnapshot(t, owner, dialect.EngineSchema); got != before {
				t.Fatalf("the refusal changed durable state.\n--- before ---\n%s\n--- after ---\n%s", before, got)
			}
		})
	}
}

// declaredSingleTextRoutine picks a routine this build declares with exactly one `text` input,
// from the inventory rather than by naming one, so the two tests below cannot drift apart or
// quietly start measuring a routine that no longer exists.
//
// Selection is ordered and excludes the User authority lock. Map iteration is unspecified;
// it could select `olivares_lock_core_user_authority`, making the lineage test expect its
// sentinel from a boot refused by the authority contract. No failure frequency is inferred
// from the number of candidate routines.
//
// The H lock's public name has its own pre-effect contract now
// (freshbootstrapauthoritylock.go), measured in its own file. It is skipped here rather than
// hard-coded around, so this helper still fails loudly if the build ever stops declaring a
// single-text routine that belongs to the guard it is asked about.
func declaredSingleTextRoutine(t *testing.T, dia dialect.Dialect, set *managedObjectSet) managedObject {
	t.Helper()
	var candidates []managedObject
	for _, obj := range set.byClass[managedClassRoutine] {
		if obj.name == "olivares_lock_core_user_authority" {
			continue
		}
		if sig, err := normalizeRoutineArgTypes(obj.args); err == nil && sig == "text" {
			candidates = append(candidates, obj)
		}
	}
	if len(candidates) == 0 {
		t.Fatal("this build declares no single-text-argument routine outside the User authority lock; both witnesses below need one")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].name < candidates[j].name })
	return candidates[0]
}

// TestPostgresFreshBootstrapDoesNotMatchARoutineByArity is the second FB-2 identity: a routine is
// its name AND its input types.
//
// Measured by the first reviewer: a foreign `olivares_lineage_drop(boolean)` matched this build's
// declared `olivares_lineage_drop(target_tenant text)` when the set keyed by argument COUNT. They
// are different functions; PostgreSQL resolves them separately and `CREATE OR REPLACE FUNCTION`
// replaces only the one it declares.
//
// This is the POSITIVE half only — what the admission must not seize. The preservation of the
// other consumers' overload guard is its own test below, because a single test that ended in
// t.Skip when that guard disappeared would have reported a green suite for a boundary that moved.
func TestPostgresFreshBootstrapDoesNotMatchARoutineByArity(t *testing.T) {
	ctx := context.Background()
	_, owner, dia := accessEvidencePGStore(t)
	reg := coreOnlyRegistry(t)
	set, err := buildManagedObjectSet(dia, coreDescriptors(), reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	declared := declaredSingleTextRoutine(t, dia, set)
	mustExec(t, owner, fmt.Sprintf(
		"CREATE FUNCTION public.%s(review boolean) RETURNS boolean LANGUAGE sql AS $$ SELECT review $$",
		declared.name))

	found, cerr := censusManagedNamespace(ctx, owner, dia, set)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if len(found) != 0 {
		t.Fatalf("a foreign (boolean) overload of %s was taken for this build's (text) declaration: %v",
			declared.name, found)
	}
	graph, gerr := guardEditionGraphFor(coreOnlyAccessEvidenceManifest(t))
	if gerr != nil {
		t.Fatal(gerr)
	}
	plan, perr := classifyAccessEvidenceBoot(ctx, owner, dia, graph, coreDescriptors(), reg, nil,
		guardEventFenceFacts{})
	if perr != nil {
		t.Fatalf("the admission refused a foreign overload of a different signature: %v", perr)
	}
	if plan.Class != accessEvidenceStartFreshEmpty || plan.FreshBootstrap == nil {
		t.Fatalf("class = %s, want %s with the max0 admission proved", plan.Class, accessEvidenceStartFreshEmpty)
	}
}

// TestPostgresLineageGuardStillRefusesAReservedNameOverload is the PRESERVATION half, and it
// FAILS if that guard ever stops refusing.
//
// It used to be the tail of the test above and ended in `t.Skip` when Open succeeded, which is the
// defect an independent review named: root requires the other consumers' overload guard to be
// preserved, so losing it must turn a suite red rather than appear as an acceptable omission. A
// skip is not a verdict — it is the absence of one.
//
// WHAT IT IS NOT: an assertion about the max0 admission, which by design does not seize a
// signature it never creates (the test above). The lineage guard reconciler keeps its OWN function
// census, which reports drift for any unexpected routine of a lineage name whatever its signature.
// That is a deliberate existing boundary of a different subsystem, and root's direction narrows
// only verifyBootstrapFunction and expressly forbids wider routine-authority changes — so it is
// left exactly as it is, and pinned here.
func TestPostgresLineageGuardStillRefusesAReservedNameOverload(t *testing.T) {
	ctx := context.Background()
	cfg, owner, dia := accessEvidencePGStore(t)
	set, err := buildManagedObjectSet(dia, coreDescriptors(), coreOnlyRegistry(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	declared := declaredSingleTextRoutine(t, dia, set)
	mustExec(t, owner, fmt.Sprintf(
		"CREATE FUNCTION public.%s(review boolean) RETURNS boolean LANGUAGE sql AS $$ SELECT review $$",
		declared.name))

	st, oerr := Open(ctx, cfg, nil)
	if st != nil {
		_ = st.Close()
	}
	t.Logf("LINEAGE_OVERLOAD_GUARD|routine=%s|open_error=%v", declared.name, oerr)
	if oerr == nil {
		t.Fatalf("Open served a database carrying a foreign overload of the reserved lineage routine %s. The lineage guard's own function census is required to keep refusing that, and this test exists so its loss is a failure rather than a skipped case",
			declared.name)
	}
	// The lineage guard's OWN sentinel, so this cannot pass on somebody else's refusal. It is
	// the sentinel rather than a message because that census has two ways of saying the same
	// thing — an overloaded routine and a drifted projection — and both are it refusing.
	if !errors.Is(oerr, store.ErrLineageUnavailable) {
		t.Fatalf("Open error = %v, want the lineage guard's own refusal for %s; another refusal would mean this test passes without measuring the guard it names",
			oerr, declared.name)
	}
}

// TestPostgresFreshBootstrapProbeDoesNotAdoptAForeignRelation is FB-5.
//
// The shape oracle used the predictable name `olivares_fresh_probe_<table>` with the rendered
// `CREATE TABLE IF NOT EXISTS`, so a relation of that name that another application owns was read
// AS THE ORACLE and a perfectly valid R was refused for somebody else's contract. The probe now
// mints a random identity, proves it free, creates it without IF NOT EXISTS and censuses it by
// OID — so the formerly conflicting name is just another foreign object.
func TestPostgresFreshBootstrapProbeDoesNotAdoptAForeignRelation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		damage  func(*testing.T, *sql.DB)
		wantErr bool
	}{
		{name: "a valid checkpoint beside the formerly conflicting name", damage: func(*testing.T, *sql.DB) {}},
		{
			name: "an actually altered R beside the same name is still refused",
			damage: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, "ALTER TABLE public."+dialect.ControlRolloutStateTable+" ADD COLUMN review text")
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, dia := accessEvidencePGStore(t)
			realRolloutCheckpoint(t, owner, dia, nil)
			for _, stmt := range []string{
				"CREATE TABLE public.olivares_fresh_probe_" + dialect.ControlRolloutStateTable + " (review text)",
				"INSERT INTO public.olivares_fresh_probe_" + dialect.ControlRolloutStateTable + " VALUES ('preserve-me')",
			} {
				mustExec(t, owner, stmt)
			}
			tc.damage(t, owner)

			st, err := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			if tc.wantErr {
				if err == nil {
					t.Fatal("an altered rollout relation was admitted")
				}
			} else if err != nil {
				t.Fatalf("a valid checkpoint was refused because of a foreign relation named like the probe: %v", err)
			}
			// The foreign relation is intact either way, and no probe of this build survives.
			var value string
			if qerr := owner.QueryRowContext(ctx,
				"SELECT review FROM public.olivares_fresh_probe_"+dialect.ControlRolloutStateTable).Scan(&value); qerr != nil || value != "preserve-me" {
				t.Fatalf("the foreign relation changed: value=%q err=%v", value, qerr)
			}
			leftovers := pgQueryStrings(t, owner, `SELECT c.relname
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname LIKE 'olivares\_fresh\_probe\_%'`, dialect.EngineSchema)
			if len(leftovers) != 1 {
				t.Fatalf("probe relations left behind: %v", leftovers)
			}
		})
	}
}

// TestPostgresFreshBootstrapRefusesAnUnreadableEmptyTracker is the clause a filtered read would
// break: "empty" must be cardinality zero, not a SELECT a policy emptied.
//
// A tracker with row-level security is not this build's relation whatever it contains, and the
// refusal must arrive before the boot treats an invisible history as no history.
func TestPostgresFreshBootstrapRefusesAnUnreadableEmptyTracker(t *testing.T) {
	ctx := context.Background()
	cfg, owner, _ := accessEvidencePGStore(t)
	for _, stmt := range []string{
		"CREATE TABLE public." + coreTrackingTable +
			" (version integer PRIMARY KEY, name text NOT NULL, applied_at text NOT NULL," +
			" phase text NOT NULL DEFAULT 'expand', reverted_at text)",
		"INSERT INTO public." + coreTrackingTable +
			" (version, name, applied_at) VALUES (1, 'tenancy', '2026-01-01T00:00:00Z')",
		"ALTER TABLE public." + coreTrackingTable + " ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE public." + coreTrackingTable + " FORCE ROW LEVEL SECURITY",
	} {
		mustExec(t, owner, stmt)
	}
	before := pgLogicalSnapshot(t, owner, dialect.EngineSchema)

	st, err := Open(ctx, cfg, nil)
	if st != nil {
		_ = st.Close()
	}
	if err == nil {
		t.Fatal("Open accepted a tracker whose rows it cannot read")
	}
	if pgRelationExists(t, owner, dialect.EngineSchema, dialect.ControlRolloutStateTable) {
		t.Fatal("the refusal created the rollout relations")
	}
	if got := pgLogicalSnapshot(t, owner, dialect.EngineSchema); got != before {
		t.Fatalf("the refusal changed durable state.\n--- before ---\n%s\n--- after ---\n%s", before, got)
	}
}

// TestPostgresFreshBootstrapAdmitsAndRefusesRolloutCheckpoints exercises the R half where the
// contract is a catalog projection: the admitted checkpoint is produced by the product's own
// transaction, and the refused one differs from it by one column nobody declared.
func TestPostgresFreshBootstrapAdmitsAndRefusesRolloutCheckpoints(t *testing.T) {
	for _, tc := range []struct {
		name    string
		damage  func(*testing.T, *sql.DB)
		wantErr bool
	}{
		{name: "the checkpoint the product's own transaction commits", damage: func(*testing.T, *sql.DB) {}},
		{
			name: "one column nobody declared, with every ordinary SELECT still working",
			damage: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, "ALTER TABLE public."+dialect.ControlRolloutStateTable+" ADD COLUMN review text")
			},
			wantErr: true,
		},
		{
			name: "a receipt that disagrees with its state row",
			damage: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, "UPDATE public."+dialect.ControlRolloutClassificationTable+
					" SET witness_detail = witness_detail || '-review'")
			},
			wantErr: true,
		},
		{
			name: "a CHECK relaxed so a stored mode this build never writes would fit",
			damage: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, "ALTER TABLE public."+dialect.ControlRolloutStateTable+
					" DROP CONSTRAINT "+dialect.ControlRolloutStateTable+"_classified_mode_check")
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, dia := accessEvidencePGStore(t)
			realRolloutCheckpoint(t, owner, dia, []store.RolloutControl{testRolloutControl})
			tc.damage(t, owner)
			before := pgLogicalSnapshot(t, owner, dialect.EngineSchema)

			st, err := Open(ctx, cfg, registerWidgetStaged)
			if st != nil {
				_ = st.Close()
			}
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("a real pre-v1 checkpoint was refused: %v", err)
				}
				if got := postgresMaxCoreVersion(t, owner, dia); got != coreSupportedMigrationVersion {
					t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
				}
				assertPGReceiptDetail(t, owner, testControlKey,
					"table:"+testRolloutControl.WitnessTable+":absent"+freshWitnessSuffixVirgin)
				return
			}
			if err == nil {
				t.Fatal("Open admitted a rollout checkpoint this build could not have committed")
			}
			if pgRelationExists(t, owner, dialect.EngineSchema, coreTrackingTable) {
				t.Fatal("the refusal created the core tracker")
			}
			if got := pgLogicalSnapshot(t, owner, dialect.EngineSchema); got != before {
				t.Fatalf("the refusal changed durable state.\n--- before ---\n%s\n--- after ---\n%s", before, got)
			}
		})
	}
}

func assertPGReceiptDetail(t *testing.T, db *sql.DB, key, want string) {
	t.Helper()
	var detail string
	if err := db.QueryRowContext(context.Background(),
		"SELECT witness_detail FROM public."+dialect.ControlRolloutClassificationTable+
			" WHERE control_key = $1", key).Scan(&detail); err != nil {
		t.Fatalf("read the classification receipt for %q: %v", key, err)
	}
	if detail != want {
		t.Fatalf("receipt detail = %q, want %q", detail, want)
	}
}

// TestPostgresFreshBootstrapJudgesTheReservedFenceAsOneFamily is IR-R2-01, and it replaces a
// version of this test that only proved the easy half.
//
// `CREATE EVENT TRIGGER` is superuser-only and every role this product uses is NOSUPERUSER, so
// dialect.GuardEventFenceStmts() has NO production caller: an operator applies it with the
// maintenance role BEFORE the first boot. That makes the fence a legitimate pre-v1 presence — but
// as ONE FAMILY. The reviewed candidate excused its three identities SEPARATELY, and an
// independent review measured the consequence on PostgreSQL 16.15: a database carrying only the
// canonical handler was admitted as `fresh-empty`, reached core v9 and all three rollout
// relations, and was refused by the late verifier — or, under `GuardEventFenceOff`, was not
// refused at all.
//
// Three answers and only three: none of it is absence, all of it must pass the EXISTING
// projection and judge as `installed`, and any proper subset is refused before anything commits.
func TestPostgresFreshBootstrapJudgesTheReservedFenceAsOneFamily(t *testing.T) {
	for _, tc := range []struct {
		name string
		// install runs as the SUPERUSER, which is the only role that can create these
		// objects, and returns what the operator step left behind.
		install func(*testing.T, *sql.DB)
		policy  store.GuardEventFencePolicy
		wantErr bool
		// wantMembers is how many reserved identities the admission should report as found
		// and judged installed.
		wantMembers int
	}{
		{
			name:        "the complete canonical fence the real constructor renders",
			install:     func(t *testing.T, super *sql.DB) { installFenceStatements(t, super, -1) },
			wantMembers: 3,
		},
		{
			name:    "the complete canonical fence, with the service policy switched off",
			install: func(t *testing.T, super *sql.DB) { installFenceStatements(t, super, -1) },
			policy:  store.GuardEventFenceOff,
			// A complete, canonical fence is admitted whatever the service policy says: the
			// policy governs what a running deployment asserts, not what this checkpoint is.
			wantMembers: 3,
		},
		{
			name:    "no fence at all, which is the ordinary case and the only real absence",
			install: func(*testing.T, *sql.DB) {},
		},
		{
			// The reviewer's exact witness: the FIRST statement of the real constructor is
			// the canonical handler, and nothing else.
			name:    "the handler alone, under the default verify posture",
			install: func(t *testing.T, super *sql.DB) { installFenceStatements(t, super, 1) },
			wantErr: true,
		},
		{
			// The other policy branch. `off` deliberately performs no later projection, so
			// under the reviewed candidate this partial set was ADMITTED and served.
			name:    "the handler alone, with the service policy switched off",
			install: func(t *testing.T, super *sql.DB) { installFenceStatements(t, super, 1) },
			policy:  store.GuardEventFenceOff,
			wantErr: true,
		},
		{
			name: "the complete family with a neutralized handler body",
			install: func(t *testing.T, super *sql.DB) {
				installFenceStatements(t, super, -1)
				mustExec(t, super, `CREATE OR REPLACE FUNCTION `+dialect.EngineSchema+`.`+
					dialect.GuardEventFenceHandlerFn+`() RETURNS event_trigger LANGUAGE plpgsql AS $x$ BEGIN END $x$`)
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dsns := isolatedPG(t)
			super := guardPGProbe(t, dsns.Superuser)
			app := guardPGProbe(t, dsns.App)
			tc.install(t, super)

			dia, ok := dialect.New(store.EnginePostgres)
			if !ok {
				t.Fatal("no PostgreSQL dialect")
			}
			graph, gerr := guardEditionGraphFor(coreOnlyAccessEvidenceManifest(t))
			if gerr != nil {
				t.Fatal(gerr)
			}
			// THE ADMISSION'S OWN ANSWER, read before anything can commit. Asserting it
			// directly is what separates "rejected early" from "rejected at all".
			plan, cerr := classifyAccessEvidenceBoot(ctx, app, dia, graph, coreDescriptors(),
				coreOnlyRegistry(t), nil, pgFenceFacts(t, app))

			cfg := store.Config{
				Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4,
				GuardEventFence: tc.policy,
			}
			st, oerr := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			legs, handler := countFenceObjects(t, super)
			t.Logf("RESERVED_FENCE|case=%s|policy=%q|classify_error=%v|open_error=%v|handler=%d|legs=%d",
				tc.name, string(tc.policy), cerr, oerr, handler, legs)

			if !tc.wantErr {
				if cerr != nil {
					t.Fatalf("the admission refused an admissible fence state: %v", cerr)
				}
				if plan.FreshBootstrap == nil ||
					len(plan.FreshBootstrap.OperatorProvisioned) != tc.wantMembers {
					t.Fatalf("reserved identities judged installed = %v, want %d",
						plan.FreshBootstrap, tc.wantMembers)
				}
				if oerr != nil {
					t.Fatalf("Open refused an admissible fence state: %v", oerr)
				}
				if got := postgresMaxCoreVersion(t, super, dia); got != coreSupportedMigrationVersion {
					t.Fatalf("core history reached v%d, want v%d", got, coreSupportedMigrationVersion)
				}
				return
			}

			// THE REFUSAL IS THE ADMISSION'S, and it arrives with nothing created. Both
			// halves matter: the reviewed candidate also refused the first case, but only
			// after core v9 and three rollout relations were durable.
			if cerr == nil {
				t.Fatal("the max0 admission accepted a fence family it cannot have been left by an installation")
			}
			if !errors.Is(cerr, ErrGuardManifestNoEdge) {
				t.Fatalf("classification error = %v, want the classifier's own refusal", cerr)
			}
			if oerr == nil {
				t.Fatal("Open served a database whose reserved fence family is not an installation")
			}
			if pgRelationExists(t, super, dialect.EngineSchema, coreTrackingTable) {
				t.Fatal("the refusal created the core tracker")
			}
			for _, table := range []string{
				dialect.ControlRolloutStateTable,
				dialect.ControlRolloutTransitionTable,
				dialect.ControlRolloutClassificationTable,
			} {
				if pgRelationExists(t, super, dialect.EngineSchema, table) {
					t.Fatalf("the refusal created %s", table)
				}
			}
			// And the operator's own objects are left exactly as they were found.
			wantLegs, wantHandler := countFenceObjectsWanted(tc.name)
			if legs != wantLegs || handler != wantHandler {
				t.Fatalf("the refusal changed the operator's fence: handler=%d legs=%d, want %d and %d",
					handler, legs, wantHandler, wantLegs)
			}
		})
	}
}

// installFenceStatements applies the first n statements of the REAL constructor as the superuser,
// or all of them when n is negative. Taking the partial set from the product's own rendering is
// what makes "the handler alone" the operator's first step rather than a fixture somebody wrote.
func installFenceStatements(t *testing.T, super *sql.DB, n int) {
	t.Helper()
	stmts := dialect.GuardEventFenceStmts()
	if len(stmts) == 0 {
		t.Fatal("the product rendered no operator fence statements")
	}
	if n >= 0 && n < len(stmts) {
		stmts = stmts[:n]
	}
	for _, stmt := range stmts {
		mustExec(t, super, stmt)
	}
}

func countFenceObjects(t *testing.T, super *sql.DB) (legs, handler int) {
	t.Helper()
	ctx := context.Background()
	names := guardEventFenceLegNames()
	list, args := pgNameList(names)
	if err := super.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_catalog.pg_event_trigger WHERE evtname IN (`+list+`)`, // #nosec G202 -- placeholders only
		args...).Scan(&legs); err != nil {
		t.Fatalf("count the fence legs: %v", err)
	}
	if err := super.QueryRowContext(ctx, `SELECT count(*) FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = $2`,
		dialect.EngineSchema, dialect.GuardEventFenceHandlerFn).Scan(&handler); err != nil {
		t.Fatalf("count the fence handler: %v", err)
	}
	return legs, handler
}

// countFenceObjectsWanted is what each refused case installed, so the preservation assertion names
// a number rather than re-reading the database it is checking.
func countFenceObjectsWanted(name string) (legs, handler int) {
	if strings.Contains(name, "handler alone") {
		return 0, 1
	}
	return len(guardEventFenceLegNames()), 1
}

// pgFenceFacts resolves the two facts guardEventFenceFacts carries the way Open resolves them:
// from the APPLICATION pool, before anything is created. A test that passed literals here would
// be asserting about a role and a major nobody looked up.
func pgFenceFacts(t *testing.T, appPool *sql.DB) guardEventFenceFacts {
	t.Helper()
	ctx := context.Background()
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	posture, err := dia.ConnRolePosture(ctx, appPool)
	if err != nil {
		t.Fatalf("read the application role posture: %v", err)
	}
	major, err := postgresServerMajor(ctx, appPool)
	if err != nil {
		t.Fatalf("read the server major: %v", err)
	}
	if posture.Role == "" || major == 0 {
		t.Fatalf("resolved facts are incomplete: role=%q major=%d", posture.Role, major)
	}
	return guardEventFenceFacts{AppRole: posture.Role, Major: major}
}

// pgNameList renders $1,$2,… placeholders for a name list, so no name ever reaches the SQL text.
func pgNameList(names []string) (string, []any) {
	ph := make([]string, len(names))
	args := make([]any, len(names))
	for i, n := range names {
		ph[i], args[i] = fmt.Sprintf("$%d", i+1), n
	}
	return strings.Join(ph, ","), args
}
