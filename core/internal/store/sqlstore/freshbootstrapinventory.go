// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The MANAGED OBJECT SET of this build — the identities `fresh-empty` is a statement about.
//
// The ratified frontier says `fresh-empty` means "no confirmed core migration AND no product
// object of this build's managed namespace, except the preliminary checkpoints T and R". That
// sentence is only checkable if "object of this build" is a CLOSED list, and the two ways of
// faking one are both refused here:
//
//   - A PREFIX is not membership. `orgs`, `audit_events` and `_scope_tenant` share no prefix,
//     and a foreign `olivares_things` shares one with objects this build does own. The set is
//     built from the descriptors and the statement constructors the plan actually runs.
//   - A REGEX over arbitrary SQL is not coverage. The extractor below reads exactly the
//     statement forms this repository emits and returns an error for anything else, so a
//     statement it cannot name becomes a declared residual rather than a silent omission.
//
// WHAT THE SET IS FOR, and it is worth being exact because it bounds what it must contain: at
// max0 the only effects that can reach the database are the ones a FRESH Open of this build
// performs. So the set is "every identity a fresh Open creates", plus the identities the closed
// plan DROPS or REPLACES without creating them fresh — of which the measured example is the
// historical index core v4 drops unqualified (federation_configs_scope_uniq), an index a fresh
// install never creates and whose name a foreign table may legitimately carry today.
//
// TestFreshBootstrapInventoryCoversARealInstall proves the first half empirically rather than
// by re-reading this file: it opens a real store on each engine and asserts every object left
// in the managed namespace is named here. That is what stops this list drifting behind the
// plan, and it is not circular — the census comes from the catalog, not from the constructors.

// errManagedStatementUnreadable is returned by managedStatementObjects for a statement form it
// cannot name exactly. For this build's own constructors it is a hard error (the plan grew a
// form the inventory does not read). For a module's registered migration filesystem it is
// recorded as a residual: root's decision is explicit that a module supported today is not to
// be rejected because enumerating its helper objects would need an API this contract did not
// change.
var errManagedStatementUnreadable = errors.New("sqlstore: managed statement cannot be named")

// managedObjectClass separates the catalog namespaces an identity can collide in.
//
// SQLite keeps tables, indexes, triggers and views in ONE per-database namespace, so the three
// classes fold into one name set there. PostgreSQL keeps relations and indexes together in
// pg_class, routines in pg_proc (by name AND argument signature, which is why a non-colliding
// overload must not be forbidden) and event triggers in a database-global catalog of their own.
type managedObjectClass string

const (
	managedClassRelation     managedObjectClass = "relation"
	managedClassIndex        managedObjectClass = "index"
	managedClassTrigger      managedObjectClass = "trigger"
	managedClassRoutine      managedObjectClass = "routine"
	managedClassEventTrigger managedObjectClass = "event trigger"
)

// managedObject is one identity this build administers.
type managedObject struct {
	class managedObjectClass
	name  string
	// args is the routine argument list as declared, and is empty for every other class.
	// It is carried so a refusal can name the exact signature and so an overload this build
	// does not create stays legal.
	args string
	// origin names the constructor the identity came from, so a refusal says WHY a name is
	// this build's rather than only that it is.
	origin string
	// reservedFamily names a SET of identities this build verifies and never creates, and it
	// is a family rather than a per-object flag because the set is the unit of meaning: the
	// members are installed together by one operator step, so a proper subset of them is not a
	// smaller version of that step. Empty for every ordinary identity.
	reservedFamily string
}

// guardEventFenceFamily is the one reserved family this build has.
//
// `CREATE EVENT TRIGGER` is superuser-only and every role this product uses is NOSUPERUSER, so
// dialect.GuardEventFenceStmts() has NO production caller: an operator applies it with the
// maintenance role and this engine only ever VERIFIES it. Installing it BEFORE the first boot is
// the documented order, which is why its presence at max0 is not evidence of a prior product
// commit — and why the SET, judged as a set, is what has to be admitted.
const guardEventFenceFamily = "guard event fence"

// reservedFamilySize is how many identities this build declares in a family, which is what makes
// "a proper subset" a computable statement rather than a hard-coded three.
func (s *managedObjectSet) reservedFamilySize(family string) int {
	n := 0
	for _, bucket := range s.byClass {
		for _, obj := range bucket {
			if obj.reservedFamily == family {
				n++
			}
		}
	}
	return n
}

func (o managedObject) String() string {
	if o.class == managedClassRoutine {
		return fmt.Sprintf("%s %s(%s)", o.class, o.name, o.args)
	}
	return fmt.Sprintf("%s %s", o.class, o.name)
}

// managedObjectSet is the closed inventory, indexed by the identity a live catalog exposes.
type managedObjectSet struct {
	engine store.Engine
	// byClass[class][key] is the declared object. key is the dialect-normalized name, and for
	// a routine it is "name/<argument count>": PostgreSQL resolves overloads by signature and
	// this build only ever declares one arity per name.
	byClass map[managedObjectClass]map[string]managedObject
	// unnamedModuleEffects holds one entry per statement of a module-registered migration
	// filesystem whose object this build cannot name. It is a DECLARED LIMIT, never a boot
	// refusal.
	unnamedModuleEffects []unnamedModuleEffect
}

// unnamedModuleEffect is a module migration statement whose durable effect the inventory could
// not describe in closed form.
//
// It records WHERE and WHAT FORM, and deliberately nothing else. The statement text is never
// kept: a module's migration body is the operator's SQL, it can carry identifiers and literals
// this engine has no business copying into a boot log or an error, and an earlier revision of
// this struct held a 60-byte excerpt of it.
type unnamedModuleEffect struct {
	namespace string
	migration string
	// form is a CLOSED classification of the statement, never its content: the leading
	// keyword when it is one this build recognizes, and "unrecognized" otherwise.
	form string
}

func (e unnamedModuleEffect) String() string {
	return e.namespace + "/" + e.migration + " form=" + e.form
}

// moduleStatementForms is the closed vocabulary an unnamed module effect may be labeled with.
// A keyword outside it is reported as "unrecognized" rather than echoed, so no part of a
// module's SQL can reach a log through this path.
var moduleStatementForms = map[string]bool{
	"ALTER": true, "ANALYZE": true, "COMMENT": true, "CREATE": true, "DELETE": true,
	"DO": true, "DROP": true, "GRANT": true, "INSERT": true, "PRAGMA": true, "REVOKE": true,
	"SELECT": true, "SET": true, "TRUNCATE": true, "UPDATE": true, "VACUUM": true, "WITH": true,
}

func moduleStatementForm(stmt string) string {
	fields := strings.Fields(stripSQLComments(stmt))
	if len(fields) == 0 {
		return "empty"
	}
	keyword := strings.ToUpper(fields[0])
	if !moduleStatementForms[keyword] {
		return "unrecognized"
	}
	if keyword == "CREATE" || keyword == "DROP" {
		// The object kind is part of the FORM, not of the content, and it is what makes the
		// diagnostic actionable: "DROP INDEX" and "DROP TABLE" are different admissions.
		rest := skipKeywords(fields[1:], "OR", "REPLACE", "UNIQUE", "TEMP", "TEMPORARY",
			"GLOBAL", "LOCAL", "IF", "NOT", "EXISTS", "CONCURRENTLY")
		if len(rest) > 0 {
			kind := strings.ToUpper(rest[0])
			if _, known := managedClassForKeyword(kind); known || kind == "POLICY" ||
				kind == "SCHEMA" || kind == "EVENT" || kind == "MATERIALIZED" {
				return keyword + " " + kind
			}
		}
		return keyword + " <other>"
	}
	return keyword
}

func newManagedObjectSet(engine store.Engine) *managedObjectSet {
	return &managedObjectSet{
		engine:  engine,
		byClass: map[managedObjectClass]map[string]managedObject{},
	}
}

// managedNameKey normalizes a name for the comparison the ENGINE itself performs.
//
// SQLite resolves unquoted and quoted identifiers case-insensitively over ASCII, so a
// precreated `ORGS` is the same object as `orgs` there and must be recognized as a collision.
// PostgreSQL folds unquoted identifiers to lower case at parse time, and this build renders
// every managed name already lower case, so a quoted `"Orgs"` really is a different object and
// must NOT be seized.
func managedNameKey(engine store.Engine, name string) string {
	if engine == store.EngineSQLite {
		return asciiLower(name)
	}
	return name
}

// asciiLower lower-cases ASCII only, which is exactly SQLite's identifier folding rule. Go's
// strings.ToLower is Unicode-aware and would fold pairs SQLite keeps distinct.
func asciiLower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// collisionNamespace maps a class to the catalog namespace a name of that class actually
// collides in, PER ENGINE. The two engines disagree, and getting this wrong is executable —
// it produced both a missed collision and a false refusal in the reviewed candidate.
//
// MEASURED on the SQLite this build links, not inferred from the fact that sqlite_master holds
// everything in one table:
//
//	CREATE TRIGGER orgs …; CREATE TABLE orgs (…)   -> BOTH SUCCEED
//	CREATE INDEX  x    …; CREATE TABLE x    (…)    -> "there is already an index named x"
//	CREATE TABLE  x    …; CREATE INDEX x    …      -> "there is already a table named x"
//	CREATE TRIGGER x   …; CREATE INDEX x    …      -> BOTH SUCCEED
//
// So on SQLite tables, views and indexes share ONE namespace and TRIGGERS HAVE THEIR OWN. The
// earlier revision folded triggers in with relations, which refused an unrelated database that
// merely had a trigger named `orgs` — a name that is not the table's identity.
//
// On PostgreSQL relations and indexes live together in pg_class, so an index named `orgs`
// really does collide with the table this build creates. Triggers do NOT: they are scoped to
// their relation, so a foreign trigger on a foreign table is not a collision at all and the
// admission census deliberately never looks at pg_trigger.
func (s *managedObjectSet) collisionNamespace(class managedObjectClass) managedObjectClass {
	if class == managedClassIndex {
		return managedClassRelation
	}
	return class
}

func (s *managedObjectSet) add(obj managedObject) {
	class := s.collisionNamespace(obj.class)
	bucket := s.byClass[class]
	if bucket == nil {
		bucket = map[string]managedObject{}
		s.byClass[class] = bucket
	}
	key, err := s.identityKey(class, obj.name, obj.args)
	if err != nil {
		// A declared routine whose argument list this build cannot reduce to a signature is
		// recorded under its raw list rather than silently keyed as zero-argument. It then
		// matches nothing in the catalog, and the empirical coverage test reports the live
		// object as unnamed — which is the loud failure, not a quiet miss.
		key = managedNameKey(s.engine, obj.name) + "(?" + strings.TrimSpace(obj.args) + ")"
	}
	if _, present := bucket[key]; !present {
		bucket[key] = obj
	}
}

// lookup answers whether a live catalog entry is one of this build's identities.
//
// signature is the catalog's own identity argument list for a routine — on PostgreSQL,
// pg_get_function_identity_arguments — and is ignored for every other class.
func (s *managedObjectSet) lookup(class managedObjectClass, name, signature string) (managedObject, bool) {
	class = s.collisionNamespace(class)
	key, err := s.identityKey(class, name, signature)
	if err != nil {
		return managedObject{}, false
	}
	obj, ok := s.byClass[class][key]
	return obj, ok
}

// identityKey is the string two identities are equal at.
//
// For a routine that is NAME PLUS THE EXACT INPUT TYPE SIGNATURE, because that is what
// PostgreSQL resolves and what `CREATE OR REPLACE FUNCTION` replaces. Keying by ARGUMENT COUNT
// was wrong and measurably so: a foreign `olivares_lineage_drop(boolean)` matched this build's
// declared `olivares_lineage_drop(target_tenant text)`, which is a different function.
func (s *managedObjectSet) identityKey(class managedObjectClass, name, args string) (string, error) {
	key := managedNameKey(s.engine, name)
	if class != managedClassRoutine {
		return key, nil
	}
	sig, err := normalizeRoutineArgTypes(args)
	if err != nil {
		return "", err
	}
	return key + "(" + sig + ")", nil
}

// normalizeRoutineArgTypes reduces an argument list to the comma-separated input TYPE list that
// identifies a routine, in the spelling PostgreSQL's own
// pg_get_function_identity_arguments produces.
//
// It is DENY-CLOSED for the same reason the statement extractor is: an argument form it cannot
// reduce exactly returns an error rather than a guess, because a wrong signature is either a
// missed collision or a seized foreign overload. Every routine this build declares is either
// zero-input or `<name> <type>`, so the forms it reads are the forms this repository emits.
func normalizeRoutineArgTypes(args string) (string, error) {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return "", nil
	}
	var out []string
	depth, start := 0, 0
	fields := func(from, to int) string { return strings.TrimSpace(trimmed[from:to]) }
	for i := 0; i < len(trimmed); i++ {
		switch trimmed[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, fields(start, i))
				start = i + 1
			}
		}
	}
	out = append(out, fields(start, len(trimmed)))

	types := make([]string, 0, len(out))
	for _, arg := range out {
		// An argmode prefix is dropped; what remains is either the bare type or a name and
		// its type. Anything longer is a form this build does not emit.
		tokens := strings.Fields(arg)
		if len(tokens) > 0 {
			switch strings.ToUpper(tokens[0]) {
			case "IN", "OUT", "INOUT", "VARIADIC":
				tokens = tokens[1:]
			}
		}
		switch len(tokens) {
		case 1:
			types = append(types, strings.ToLower(tokens[0]))
		case 2:
			types = append(types, strings.ToLower(tokens[1]))
		default:
			return "", fmt.Errorf("%w: routine argument %q is not `[mode] [name] type`",
				errManagedStatementUnreadable, arg)
		}
	}
	return strings.Join(types, ", "), nil
}

func (s *managedObjectSet) addStatements(origin string, stmts []string) error {
	for _, stmt := range stmts {
		objs, err := managedStatementObjects(stmt)
		if err != nil {
			return fmt.Errorf("sqlstore: managed object inventory: %s: %w", origin, err)
		}
		for _, obj := range objs {
			obj.origin = origin
			s.add(obj)
		}
	}
	return nil
}

// addModuleStatements is addStatements for SQL THIS BUILD DID NOT AUTHOR, and it is a
// deliberately different rule from the one applied to this build's own constructors.
//
// THE DISTINCTION IS THE CORRECTION. For a statement this repository renders, "GRANT creates no
// object" is a fact about a statement whose author is this file. For a module's registered
// migration filesystem it is not a fact at all, and the reviewer measured why:
//
//	DO $$ BEGIN EXECUTE 'CREATE TABLE review_managed_hidden(v text)'; END $$
//
// created a relation while the inventory reported zero identities AND zero residuals — an
// effect that was neither named nor declared unknown. So here only the statement forms whose
// durable effect is fully determined by their own text produce an identity; EVERY other form,
// including ALTER, DO, WITH and a bare SELECT, is recorded as an UNPROVEN EFFECT.
//
// It never fails the boot, and it never rejects a module. The residual is a declared limit of
// this inventory's coverage, surfaced to the caller by verifyFreshBootstrapAdmission; it is not
// an accusation about the module and not a claim that anything is wrong with it.
func (s *managedObjectSet) addModuleStatements(namespace, migration string, stmts []string) {
	for _, stmt := range stmts {
		objs, err := managedStatementObjects(stmt)
		if err != nil || !moduleStatementEffectIsDetermined(stmt) {
			s.unnamedModuleEffects = append(s.unnamedModuleEffects, unnamedModuleEffect{
				namespace: namespace, migration: migration, form: moduleStatementForm(stmt),
			})
			continue
		}
		for _, obj := range objs {
			obj.origin = "module " + namespace + " migration " + migration
			s.add(obj)
		}
	}
}

// moduleStatementEffectIsDetermined reports whether a statement's durable effect is decided by
// its own leading form, so that naming the object it creates or drops is the WHOLE of it.
//
// Only CREATE and DROP qualify. An ALTER may add a constraint or rename a relation, a DO block
// may execute anything at all, and a CTE or a SELECT may call a function that does. None of
// those can be read as "no object" without reading the body, which is exactly the arbitrary-SQL
// parser this correction is required NOT to pretend to have.
func moduleStatementEffectIsDetermined(stmt string) bool {
	fields := strings.Fields(stripSQLComments(stmt))
	if len(fields) == 0 {
		return true
	}
	switch strings.ToUpper(fields[0]) {
	case "CREATE", "DROP":
		return true
	default:
		return false
	}
}

// buildManagedObjectSet assembles the inventory from the closed registry and the statement
// constructors this build's plan runs. It takes no database handle and reads nothing: the set
// is a property of the binary, which is what lets it be compared against a database rather
// than derived from one.
func buildManagedObjectSet(
	dia dialect.Dialect,
	descs []model.EntityDescriptor,
	reg *registry,
	modulePlans []moduleFileMigrationPlan,
) (*managedObjectSet, error) {
	set := newManagedObjectSet(dia.Name())

	// (1) The engine's own bookkeeping relations, which have no descriptor and are therefore
	// the half a descriptor walk cannot find. Every one of them is created by a constructor
	// named beside it.
	for _, obj := range []managedObject{
		{class: managedClassRelation, name: coreTrackingTable, origin: "migrate.ensureTracking"},
		{class: managedClassRelation, name: moduleTablesTracking, origin: "ensureModuleTracking"},
		{class: managedClassRelation, name: dialect.ControlRolloutStateTable, origin: "classifyRolloutControls"},
		{class: managedClassRelation, name: dialect.ControlRolloutTransitionTable, origin: "classifyRolloutControls"},
		{class: managedClassRelation, name: dialect.ControlRolloutClassificationTable, origin: "classifyRolloutControls"},
		{class: managedClassRelation, name: dialect.AuditBlindingStateTable, origin: "ensureAuditBlindingState"},
		{class: managedClassRelation, name: dialect.ControlAppendOnlyScopeTable, origin: "ensureAppendOnlyScopeTable"},
	} {
		set.add(obj)
	}
	if dia.Name() == store.EnginePostgres {
		set.add(managedObject{class: managedClassRelation, name: leaderEpochTable, origin: "postgres leader election"})
	}

	// (2) THE HISTORICAL INDEX CORE v4 DROPS. It is the one identity a fresh install never
	// creates and the plan still reaches: `DROP INDEX IF EXISTS federation_configs_scope_uniq`
	// is unqualified on both engines, so a foreign index of that exact name — on a foreign
	// table — is destroyed by a v1..v9 run. Admitting the bootstrap without naming it would
	// make this correction's own preservation promise false.
	if err := set.addStatements("core v4 federation_multi_idp", []string{
		"DROP INDEX IF EXISTS federation_configs_scope_uniq",
	}); err != nil {
		return nil, err
	}

	// (3) The rendered schema of every registered entity, core and module, from the same
	// constructors the migrations call.
	all := append(append([]model.EntityDescriptor(nil), descs...), reg.moduleDescriptors()...)
	for _, d := range all {
		if err := set.addStatements("descriptor "+string(d.Kind), dia.CreateTableStmts(d)); err != nil {
			return nil, err
		}
		if err := set.addStatements("descriptor "+string(d.Kind)+" reconcile", reconcileIndexStmts(d)); err != nil {
			return nil, err
		}
	}
	// The append-only guards, on the descriptor-declared relations AND on the two rollout
	// evidence logs. The second set is easy to miss because it is installed by a per-boot
	// reconciler rather than by creation DDL — which is precisely why it is installed there
	// (see reconcileRolloutEvidenceGuards).
	guarded := append([]string(nil), reg.appendOnlyTables()...)
	guarded = append(guarded,
		dialect.ControlRolloutTransitionTable,
		dialect.ControlRolloutClassificationTable,
	)
	if dia.Name() == store.EnginePostgres {
		guarded = append(guarded, dialect.ControlAppendOnlyScopeTable)
	}
	for _, table := range guarded {
		if err := set.addStatements("append-only guard "+table, dia.AppendOnlyGuardStmts(table)); err != nil {
			return nil, err
		}
	}

	// (4) The version-tracked core plan's own statement lists.
	for _, source := range []struct {
		origin string
		stmts  []string
	}{
		{"core v1 tenancy", dia.TenancyStmts()},
		{"core v3 audit_chain", dia.AuditTableStmts()},
		{"core v5 audit_spool", dia.AuditSpoolStmts()},
		{"core v6 guard control plane", dia.GuardControlPlaneStmts()},
		{"core v7 directory writer control", dia.DirectoryWriterControlStmts()},
		{"guard metadata ACL", dia.GuardMetadataACLStmts()},
		{"append-only ACL", dia.AppendOnlyACLStmts(reg.appendOnlyTables())},
	} {
		if err := set.addStatements(source.origin, source.stmts); err != nil {
			return nil, err
		}
	}
	for _, obj := range directoryWriterMarkerInventory(dia) {
		set.add(obj)
	}

	// (5) Core v8's lineage objects, which carry their own identities rather than needing the
	// statement read back: lineageSQLObject IS the declaration.
	for _, obj := range append(lineageControlDDL(dia), lineageGuardObjects(dia)...) {
		class := managedClassTrigger
		switch {
		case strings.HasPrefix(obj.statement, "CREATE TABLE"):
			class = managedClassRelation
		case obj.result != "":
			class = managedClassRoutine
		}
		set.add(managedObject{class: class, name: obj.name, args: obj.arguments, origin: "core v8 lineage"})
	}

	// (6) Core v10's User authority routines, on PostgreSQL. The identities come from the
	// same constants the migration executes, so a change to either signature moves this
	// inventory with it.
	//
	// The retention TRIGGER is deliberately not named here: it is declared through
	// SchemaInvariants under the core namespace and block (8) already names it from the
	// closed registry. Naming it twice would put one identity under two authorities.
	if dia.Name() == store.EnginePostgres {
		if err := set.addStatements("core v10 User authority", []string{
			postgresUserAuthorityLockDDL,
			postgresUserAuthorityRetentionDDL,
		}); err != nil {
			return nil, err
		}
	}

	// (7) The per-boot guard reconcilers whose objects are named by constant rather than
	// rendered into a statement list this build can hand over.
	for _, obj := range perBootGuardInventory(dia) {
		set.add(obj)
	}

	// (8) Modules, and every schema invariant the closed registry carries. The tracking
	// table of each DECLARED namespace, the triggers declared through SchemaInvariants —
	// core's own included, which is where core v10's retention trigger is named — and the
	// statements of each module's migration filesystem.
	for _, mm := range reg.modMig {
		set.add(managedObject{
			class:  managedClassRelation,
			name:   "schema_migrations_mod_" + mm.namespace,
			origin: "module " + mm.namespace + " migration tracker",
		})
	}
	for namespace, invariant := range reg.invariants {
		for _, trigger := range invariant.byEngine[dia.Name()] {
			set.add(managedObject{
				class:  managedClassTrigger,
				name:   trigger.Name,
				origin: "module " + namespace + " schema invariant",
			})
		}
	}
	for _, plan := range modulePlans {
		set.add(managedObject{
			class:  managedClassRelation,
			name:   plan.trackingTable,
			origin: "module " + plan.namespace + " migration tracker",
		})
		for _, mig := range plan.migrations {
			set.addModuleStatements(plan.namespace, mig.Name, mig.Stmts)
		}
	}
	return set, nil
}

// directoryWriterMarkerInventory names the writer-control relations the dialects do not both
// render in DirectoryWriterControlStmts. The PostgreSQL marker relation is created by the
// writer-control reconciler, not by v7's statement list.
func directoryWriterMarkerInventory(dia dialect.Dialect) []managedObject {
	out := []managedObject{
		{class: managedClassRelation, name: dialect.DirectoryWriterControlTable, origin: "directory writer control"},
		{class: managedClassRelation, name: dialect.DirectoryWriterMarkerTable, origin: "directory writer control"},
		{class: managedClassRelation, name: dialect.LoginCapabilityObservationTable, origin: "login capability control (core v13)"},
	}
	if dia.Name() == store.EnginePostgres {
		out = append(out, managedObject{
			class: managedClassRoutine, name: dialect.DirectoryWriterGuardFunction,
			origin: "directory writer control",
		})
	}
	return out
}

// perBootGuardInventory names the objects the per-boot reconcilers install, which are derived
// from a constant plus a table rather than from a statement list this build can hand over.
//
// The directory writer guards are the reason this function exists: they are named per SOURCE
// TABLE and per engine, and the two dialects name them differently — one trigger per table on
// PostgreSQL, three (one per event) on SQLite. A descriptor walk finds neither.
func perBootGuardInventory(dia dialect.Dialect) []managedObject {
	var out []managedObject
	if dia.Name() != store.EnginePostgres {
		for _, spec := range sqliteDirectoryWriterGuardSpecs() {
			out = append(out, managedObject{
				class: managedClassTrigger, name: spec.Name, origin: "directory writer guard",
			})
		}
		return out
	}
	out = append(out,
		managedObject{class: managedClassRoutine, name: dialect.BlockMutationFn, origin: "audit guard function"},
	)
	// THE GUARD EVENT FENCE, AS ONE RESERVED FAMILY. Marking its members individually was the
	// defect an independent review measured: each was excused on its own, so a database
	// carrying ONLY the handler was admitted as fresh, reached core v9 and all three rollout
	// relations, and was refused by the late verifier — or, under the `off` service policy,
	// was not refused at all. Half a fence is not a smaller fence; nobody installs one.
	//
	// The legs are enumerated from dialect.GuardEventFenceEvents() rather than named here, so
	// the family cannot silently stop describing the DDL the operator actually applies.
	for _, obj := range append(
		[]managedObject{{class: managedClassRoutine, name: dialect.GuardEventFenceHandlerFn}},
		guardEventFenceLegInventory()...,
	) {
		obj.origin, obj.reservedFamily = "guard event fence (operator-provisioned)", guardEventFenceFamily
		out = append(out, obj)
	}
	for _, table := range directoryWriterSourceTables {
		out = append(out, managedObject{
			class: managedClassTrigger, name: table + "_directory_writer_guard",
			origin: "directory writer guard",
		})
	}
	return out
}

// managedStatementObjects names the durable objects one statement creates or drops.
//
// It reads exactly the forms this repository emits and REFUSES anything else, which is the
// property that makes it usable as an inventory input at all: a statement it silently skipped
// would be an object nobody knows is managed. Statements with no durable object of their own —
// ALTER, GRANT, REVOKE, INSERT, the anonymous DO block the PostgreSQL ACL legs use — return no
// object and no error, because their targets are already named by the statement that created
// them.
func managedStatementObjects(stmt string) ([]managedObject, error) {
	text := stripSQLComments(stmt)
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil, nil
	}
	switch strings.ToUpper(fields[0]) {
	case "ALTER", "GRANT", "REVOKE", "INSERT", "UPDATE", "DELETE", "SELECT", "COMMENT",
		"DO", "SET", "PRAGMA", "WITH", "ANALYZE", "VACUUM", "TRUNCATE":
		return nil, nil
	case "DROP":
		return dropStatementObject(fields, stmt)
	case "CREATE":
		return createStatementObject(fields, stmt)
	default:
		return nil, fmt.Errorf("%w: leading keyword %q in %.60q", errManagedStatementUnreadable, fields[0], text)
	}
}

func dropStatementObject(fields []string, stmt string) ([]managedObject, error) {
	rest := fields[1:]
	if len(rest) == 0 {
		return nil, fmt.Errorf("%w: %.60q", errManagedStatementUnreadable, stmt)
	}
	class, ok := managedClassForKeyword(rest[0])
	if !ok {
		return nil, fmt.Errorf("%w: DROP %s in %.60q", errManagedStatementUnreadable, rest[0], stmt)
	}
	rest = skipKeywords(rest[1:], "IF", "EXISTS", "CONCURRENTLY")
	if len(rest) == 0 {
		return nil, fmt.Errorf("%w: %.60q", errManagedStatementUnreadable, stmt)
	}
	name, args, err := managedIdentifier(rest[0], class)
	if err != nil {
		return nil, fmt.Errorf("%w: %.60q", err, stmt)
	}
	return []managedObject{{class: class, name: name, args: args}}, nil
}

func createStatementObject(fields []string, stmt string) ([]managedObject, error) {
	rest := skipKeywords(fields[1:], "OR", "REPLACE", "UNIQUE", "TEMP", "TEMPORARY", "GLOBAL", "LOCAL")
	if len(rest) == 0 {
		return nil, fmt.Errorf("%w: %.60q", errManagedStatementUnreadable, stmt)
	}
	keyword := rest[0]
	// A POLICY and a SCHEMA are named, and neither is an identity this census can collide on.
	// A row-level-security policy's name is scoped to ITS RELATION, which is already in the set
	// and must be absent — a policy cannot exist without it. The schema is dialect.EngineSchema
	// itself, which this build does not create and whose foreign twin is explicitly preserved.
	if strings.EqualFold(keyword, "POLICY") || strings.EqualFold(keyword, "SCHEMA") {
		return nil, nil
	}
	// The two two-word object kinds. Both consume their second word so the name that follows is
	// the name and not the rest of the kind.
	switch {
	case strings.EqualFold(keyword, "EVENT"):
		if len(rest) < 2 || !strings.EqualFold(rest[1], "TRIGGER") {
			return nil, fmt.Errorf("%w: %.60q", errManagedStatementUnreadable, stmt)
		}
		rest, keyword = rest[1:], "EVENT TRIGGER"
	case strings.EqualFold(keyword, "MATERIALIZED"):
		if len(rest) < 2 || !strings.EqualFold(rest[1], "VIEW") {
			return nil, fmt.Errorf("%w: %.60q", errManagedStatementUnreadable, stmt)
		}
		rest, keyword = rest[1:], "VIEW"
	}
	class, ok := managedClassForKeyword(keyword)
	if !ok {
		return nil, fmt.Errorf("%w: CREATE %s in %.60q", errManagedStatementUnreadable, keyword, stmt)
	}
	rest = skipKeywords(rest[1:], "IF", "NOT", "EXISTS", "CONCURRENTLY")
	if len(rest) == 0 {
		return nil, fmt.Errorf("%w: %.60q", errManagedStatementUnreadable, stmt)
	}
	// A routine's argument list may carry spaces, so it is taken from the raw text rather
	// than from the whitespace split.
	if class == managedClassRoutine {
		name, args, err := routineSignature(strings.Join(rest, " "))
		if err != nil {
			return nil, fmt.Errorf("%w: %.60q", err, stmt)
		}
		return []managedObject{{class: class, name: name, args: args}}, nil
	}
	name, args, err := managedIdentifier(rest[0], class)
	if err != nil {
		return nil, fmt.Errorf("%w: %.60q", err, stmt)
	}
	return []managedObject{{class: class, name: name, args: args}}, nil
}

func managedClassForKeyword(keyword string) (managedObjectClass, bool) {
	switch strings.ToUpper(keyword) {
	case "TABLE":
		return managedClassRelation, true
	case "VIEW":
		return managedClassRelation, true
	case "SEQUENCE":
		return managedClassRelation, true
	case "INDEX":
		return managedClassIndex, true
	case "TRIGGER":
		return managedClassTrigger, true
	case "FUNCTION", "PROCEDURE":
		return managedClassRoutine, true
	case "EVENT TRIGGER":
		return managedClassEventTrigger, true
	default:
		return "", false
	}
}

func skipKeywords(fields []string, keywords ...string) []string {
	for len(fields) > 0 {
		matched := false
		for _, kw := range keywords {
			if strings.EqualFold(fields[0], kw) {
				fields, matched = fields[1:], true
				break
			}
		}
		if !matched {
			return fields
		}
	}
	return fields
}

// managedIdentifier reduces a rendered object reference to the bare name the catalog exposes.
func managedIdentifier(token string, class managedObjectClass) (string, string, error) {
	name := token
	if i := strings.IndexByte(name, '('); i >= 0 {
		name = name[:i]
	}
	name = strings.TrimSuffix(name, ";")
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		// Schema qualification. Only the two schemas this engine addresses are accepted, so a
		// statement aimed anywhere else is refused rather than silently reduced to its name.
		qualifier := strings.Trim(name[:i], `"`)
		if qualifier != dialect.EngineSchema && qualifier != "main" {
			return "", "", fmt.Errorf("%w: unexpected schema qualifier %q", errManagedStatementUnreadable, qualifier)
		}
		name = name[i+1:]
	}
	name = strings.Trim(name, `"`)
	if name == "" {
		return "", "", fmt.Errorf("%w: empty %s name", errManagedStatementUnreadable, class)
	}
	return name, "", nil
}

// routineSignature splits "public.name(arg type, …) RETURNS …" into its name and argument list.
func routineSignature(rest string) (string, string, error) {
	open := strings.IndexByte(rest, '(')
	if open < 0 {
		return "", "", fmt.Errorf("%w: routine declaration without an argument list", errManagedStatementUnreadable)
	}
	name, _, err := managedIdentifier(rest[:open], managedClassRoutine)
	if err != nil {
		return "", "", err
	}
	depth, end := 0, -1
	for i := open; i < len(rest); i++ {
		switch rest[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return "", "", fmt.Errorf("%w: unbalanced routine argument list", errManagedStatementUnreadable)
	}
	return name, strings.TrimSpace(rest[open+1 : end]), nil
}

// stripSQLComments removes the leading `--` lines every module migration file carries, so the
// first token of the statement is the statement's own keyword.
func stripSQLComments(stmt string) string {
	var out []string
	for _, line := range strings.Split(stmt, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// guardEventFenceLegInventory names the fence's event triggers from the same map the projection
// and the judge read, so the reserved family and the verification cannot disagree about how many
// legs there are.
func guardEventFenceLegInventory() []managedObject {
	out := make([]managedObject, 0, len(dialect.GuardEventFenceEvents()))
	for _, name := range guardEventFenceLegNames() {
		out = append(out, managedObject{class: managedClassEventTrigger, name: name})
	}
	return out
}
