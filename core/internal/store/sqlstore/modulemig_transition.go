// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/store"
)

type schemaTransitionSpec struct {
	table                string
	name                 string
	previous             string
	next                 string
	previousFunctionName string
	nextFunctionName     string
}

type transitionFunction struct {
	schema string
	name   string
}

func (f transitionFunction) String() string {
	return quoteIdent(f.schema) + "." + quoteIdent(f.name) + "()"
}

func (f transitionFunction) dialectKey() dialect.SchemaTriggerFunctionKey {
	return dialect.SchemaTriggerFunctionKey{Schema: f.schema, Name: f.name}
}

type transitionTriggerState struct {
	function       transitionFunction
	functionRender string
	enableState    dialect.TriggerEnableState
	canExecute     bool
	definition     string
}

func triggerTransitionState(info dialect.TriggerInfo) transitionTriggerState {
	return transitionTriggerState{
		function: transitionFunction{
			schema: info.FunctionSchema,
			name:   info.FunctionName,
		},
		functionRender: info.Function,
		enableState:    info.EnableState,
		canExecute:     info.CanExecute,
		definition:     info.Definition,
	}
}

type postgresTransitionSnapshot struct {
	oldFunctions     map[transitionFunction]dialect.SchemaTriggerFunctionInfo
	oldCallerWitness map[dialect.TriggerKey]dialect.TriggerInfo
	nextReservations map[transitionFunction]dialect.SchemaTriggerFunctionInfo
}

type schemaTransitionSnapshot struct {
	schema    string
	byTrigger map[dialect.TriggerKey]transitionTriggerState
	postgres  *postgresTransitionSnapshot
}

type schemaTransitionHook struct {
	dia           dialect.Dialect
	engine        store.Engine
	namespace     string
	trackingTable string
	version       int
	specs         []schemaTransitionSpec
	beforeState   *schemaTransitionSnapshot
}

func hasSchemaTransitions(triggers []store.SchemaTrigger) bool {
	for _, trigger := range triggers {
		if len(trigger.Transitions) != 0 {
			return true
		}
	}
	return false
}

// attachSchemaTransitionHooks binds every declared definition transition to its
// exact module-file migration. It is deliberately a plan-building operation: a
// missing or duplicate version is rejected before migrate.Apply can create even
// the module tracking table.
func attachSchemaTransitionHooks(
	dia dialect.Dialect,
	engine store.Engine,
	namespace string,
	trackingTable string,
	migrations []migrate.Migration,
	reg *registry,
) error {
	declared, ok := reg.invariants[namespace]
	if !ok {
		return nil
	}
	byVersion := make(map[int][]schemaTransitionSpec)
	for _, trigger := range declared.byEngine[engine] {
		for i, transition := range trigger.Transitions {
			next := trigger.DefinitionSHA256
			if i+1 < len(trigger.Transitions) {
				next = trigger.Transitions[i+1].PreviousDefinitionSHA256
			}
			spec := schemaTransitionSpec{
				table: trigger.Table, name: trigger.Name,
				previous: transition.PreviousDefinitionSHA256, next: next,
			}
			if transition.PostgresFunctionIdentity != nil {
				spec.previousFunctionName = transition.PostgresFunctionIdentity.PreviousName
				spec.nextFunctionName = transition.PostgresFunctionIdentity.NextName
			}
			byVersion[transition.MigrationVersion] = append(
				byVersion[transition.MigrationVersion], spec,
			)
		}
	}
	if len(byVersion) == 0 {
		return nil
	}

	indexes := make(map[int][]int, len(migrations))
	for i := range migrations {
		indexes[migrations[i].Version] = append(indexes[migrations[i].Version], i)
	}
	versions := make([]int, 0, len(byVersion))
	for version := range byVersion {
		versions = append(versions, version)
	}
	sort.Ints(versions)
	for _, version := range versions {
		matches := indexes[version]
		if len(matches) != 1 {
			return fmt.Errorf(
				"schema-trigger transition v%d must bind to exactly one %s migration file; found %d",
				version, engine, len(matches),
			)
		}
		specs := append([]schemaTransitionSpec(nil), byVersion[version]...)
		sort.Slice(specs, func(i, j int) bool {
			if specs[i].table != specs[j].table {
				return specs[i].table < specs[j].table
			}
			return specs[i].name < specs[j].name
		})
		migration := &migrations[matches[0]]
		if migration.Before != nil || migration.After != nil {
			return fmt.Errorf(
				"schema-trigger transition v%d cannot replace pre-existing migration hooks",
				version,
			)
		}
		hook := &schemaTransitionHook{
			dia: dia, engine: engine, namespace: namespace,
			trackingTable: trackingTable, version: version, specs: specs,
		}
		migration.Before = hook.before
		migration.After = hook.after
	}
	return nil
}

func (h *schemaTransitionHook) before(ctx context.Context, tx *sql.Tx) error {
	if h.engine == store.EngineSQLite {
		// BEGIN is deferred on SQLite. A catalog read alone therefore leaves a window
		// in which another writer can replace the trigger after the precondition was
		// accepted. This deliberately writes no row, but enters the write transaction
		// before projecting sqlite_schema; the write lock then lives until commit.
		// #nosec G202 -- the tracking table is derived from a fixed prefix plus a namespace validated by isNamespace, then quoted; no value is interpolated
		stmt := "UPDATE main." + quoteIdent(h.trackingTable) + " SET name = name WHERE 0"
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("acquire SQLite schema-transition writer lock: %w", err)
		}
		snapshot, err := h.projectSelected(ctx, tx, false, nil)
		if err != nil {
			return err
		}
		h.beforeState = snapshot
		return nil
	}
	return h.beforePostgres(ctx, tx)
}

func (h *schemaTransitionHook) beforePostgres(ctx context.Context, tx *sql.Tx) error {
	if err := pinPostgresTransitionSearchPath(ctx, tx); err != nil {
		return fmt.Errorf("pin PostgreSQL schema-transition catalog search path: %w", err)
	}
	inventory, catalog, err := h.postgresSurfaces()
	if err != nil {
		return err
	}
	provisional, err := h.projectSelected(ctx, tx, false, nil)
	if err != nil {
		return err
	}
	selected := transitionSelectedKeys(provisional)
	oldFunctions, nextFunctions := h.postgresFunctionSets(provisional.schema)
	provisionalCallers, err := inventory.SchemaTriggerCallers(
		ctx, tx, transitionFunctionKeys(oldFunctions),
	)
	if err != nil {
		return fmt.Errorf("inventory old schema-transition function callers: %w", err)
	}
	if err := validateSelectedCallerInventory(provisionalCallers, provisional); err != nil {
		return err
	}
	oldBeforeFence, err := projectTransitionFunctions(ctx, tx, catalog, oldFunctions)
	if err != nil {
		return fmt.Errorf("project old schema-transition functions: %w", err)
	}

	// Check every destination before reserving any of them. The following plain
	// CREATE FUNCTION calls still close a check/create race through PostgreSQL's
	// routine-signature uniqueness: one concurrent creator succeeds and the other
	// transaction fails, never silently sharing an already-visible identity.
	for _, function := range nextFunctions {
		if _, exists, err := catalog.SchemaTriggerFunction(ctx, tx, function.dialectKey()); err != nil {
			return fmt.Errorf("project next schema-transition function %s: %w", function, err)
		} else if exists {
			return fmt.Errorf(
				"schema transition v%d requires next function %s to be absent before reservation",
				h.version, function,
			)
		}
	}
	for _, function := range nextFunctions {
		if err := catalog.ReserveSchemaTriggerFunction(ctx, tx, function.dialectKey()); err != nil {
			return fmt.Errorf("reserve next schema-transition function %s: %w", function, err)
		}
	}
	nextReservations, err := projectTransitionFunctions(ctx, tx, catalog, nextFunctions)
	if err != nil {
		return fmt.Errorf("project reserved schema-transition functions: %w", err)
	}
	if err := validateTransitionReservations(nextReservations); err != nil {
		return err
	}

	// The old shared functions and every table carrying a pre-existing caller are
	// fenced in deterministic order. A new trigger on some other table may still be
	// attached to an old function, deliberately: the old function remains byte- and
	// OID-exact, so that new caller is outside this migration's affected set.
	for _, function := range oldFunctions {
		if _, err := tx.ExecContext(ctx,
			dialect.PostgresFunctionFenceStatement(function.schema, function.name)); err != nil {
			return fmt.Errorf("fence old schema-transition function %s: %w", function, err)
		}
	}
	for _, table := range transitionCallerTables(provisionalCallers) {
		// #nosec G202 -- schema and table are read from the trigger catalog, so they are identifiers that already exist in this database, and both go through quoteIdent
		stmt := "LOCK TABLE ONLY " + quoteIdent(table.Schema) + "." +
			quoteIdent(table.Table) + " IN ROW EXCLUSIVE MODE"
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf(
				"lock old schema-transition caller table %s.%s: %w",
				quoteIdent(table.Schema), quoteIdent(table.Table), err,
			)
		}
	}

	stable, err := h.projectSelected(ctx, tx, false, provisional)
	if err != nil {
		return fmt.Errorf("revalidate selected schema transitions after fencing: %w", err)
	}
	stableCallers, err := inventory.SchemaTriggerCallers(
		ctx, tx, transitionFunctionKeys(oldFunctions),
	)
	if err != nil {
		return fmt.Errorf("reproject old schema-transition function callers: %w", err)
	}
	if err := validateCallerWitnesses(provisionalCallers, stableCallers); err != nil {
		return fmt.Errorf("revalidate old callers after locking: %w", err)
	}
	oldStable, err := projectTransitionFunctions(ctx, tx, catalog, oldFunctions)
	if err != nil {
		return fmt.Errorf("reproject old schema-transition functions after fencing: %w", err)
	}
	if err := validateFunctionWitnesses(oldBeforeFence, oldStable, "changed while acquiring its fence"); err != nil {
		return err
	}

	witnesses := make(map[dialect.TriggerKey]dialect.TriggerInfo)
	for key, info := range provisionalCallers {
		if !selected[key] {
			witnesses[key] = info
		}
	}
	stable.postgres = &postgresTransitionSnapshot{
		oldFunctions: oldStable, oldCallerWitness: witnesses,
		nextReservations: nextReservations,
	}
	h.beforeState = stable
	return nil
}

func (h *schemaTransitionHook) after(ctx context.Context, tx *sql.Tx) error {
	if h.beforeState == nil {
		return fmt.Errorf("schema transition v%d has no successful Before snapshot", h.version)
	}
	if h.engine == store.EngineSQLite {
		_, err := h.projectSelected(ctx, tx, true, h.beforeState)
		if err != nil {
			return fmt.Errorf("verify schema-transition postcondition: %w", err)
		}
		return nil
	}
	if err := h.afterPostgres(ctx, tx); err != nil {
		return fmt.Errorf("verify schema-transition postcondition: %w", err)
	}
	return nil
}

func (h *schemaTransitionHook) afterPostgres(ctx context.Context, tx *sql.Tx) error {
	before := h.beforeState.postgres
	if before == nil {
		return fmt.Errorf("schema transition v%d has no PostgreSQL Before witnesses", h.version)
	}
	if err := pinPostgresTransitionSearchPath(ctx, tx); err != nil {
		return fmt.Errorf("pin PostgreSQL schema-transition catalog search path: %w", err)
	}
	inventory, catalog, err := h.postgresSurfaces()
	if err != nil {
		return err
	}
	after, err := h.projectSelected(ctx, tx, true, nil)
	if err != nil {
		return err
	}
	if after.schema != h.beforeState.schema {
		return fmt.Errorf(
			"schema transition v%d changed effective schema from %q to %q",
			h.version, h.beforeState.schema, after.schema,
		)
	}
	selected := transitionSelectedKeys(after)
	oldFunctions, nextFunctions := h.postgresFunctionSets(after.schema)

	nextLive, err := projectTransitionFunctions(ctx, tx, catalog, nextFunctions)
	if err != nil {
		return fmt.Errorf("project installed schema-transition functions: %w", err)
	}
	for _, function := range nextFunctions {
		reserved := before.nextReservations[function]
		live := nextLive[function]
		if live.OID != reserved.OID {
			return fmt.Errorf(
				"schema transition v%d dropped and recreated reserved function %s (reserved OID %d, live OID %d)",
				h.version, function, reserved.OID, live.OID,
			)
		}
		if live.Definition == reserved.Definition {
			return fmt.Errorf(
				"schema transition v%d left fail-closed reservation %s unreplaced",
				h.version, function,
			)
		}
		if !live.CanExecute || !live.AppRoleDirectExecute || live.PublicCanExecute {
			return fmt.Errorf(
				"%w: installed function %s does not preserve the exact application-role grant and PUBLIC revoke",
				store.ErrSchemaTriggerUnexecutable, function,
			)
		}
		if !live.ACLIsExact {
			return fmt.Errorf(
				"schema transition v%d installed function %s without the exact owner/application-role ACL",
				h.version, function,
			)
		}
		if live.ACL != reserved.ACL {
			return fmt.Errorf(
				"schema transition v%d changed the exact reserved ACL of function %s",
				h.version, function,
			)
		}
	}
	nextCallers, err := inventory.SchemaTriggerCallers(
		ctx, tx, transitionFunctionKeys(nextFunctions),
	)
	if err != nil {
		return fmt.Errorf("inventory installed schema-transition function callers: %w", err)
	}
	if err := exactNextFunctionCallerSet(nextCallers, selected, after); err != nil {
		return err
	}

	oldLive, err := projectTransitionFunctions(ctx, tx, catalog, oldFunctions)
	if err != nil {
		return fmt.Errorf("project old schema-transition functions after migration: %w", err)
	}
	if err := validateFunctionWitnesses(before.oldFunctions, oldLive, "changed during identity transition"); err != nil {
		return err
	}
	oldCallers, err := inventory.SchemaTriggerCallers(
		ctx, tx, transitionFunctionKeys(oldFunctions),
	)
	if err != nil {
		return fmt.Errorf("inventory old schema-transition callers after migration: %w", err)
	}
	if err := validateCallerWitnesses(before.oldCallerWitness, oldCallers); err != nil {
		return fmt.Errorf("old shared-function caller changed during identity transition: %w", err)
	}
	return nil
}

// pinPostgresTransitionSearchPath makes pg_catalog implicit ahead of the engine
// schema for operators and functions. A previous migration, or this transition's
// own SQL before After, may have explicitly placed public ahead of pg_catalog.
// The reset is a transactional session setting (is_local=false): it prevents such
// objects from changing catalog query semantics now and prevents a persistent SET
// in migration SQL from leaking through COMMIT into the pooled connection. A
// rollback restores the connection's prior setting. Catalog relations and types
// are still explicitly qualified because pg_temp precedes even an exact search_path
// for those object classes.
func pinPostgresTransitionSearchPath(ctx context.Context, tx *sql.Tx) error {
	var pinned string
	if err := tx.QueryRowContext(ctx,
		"SELECT pg_catalog.set_config('search_path', $1, false)",
		dialect.EngineSchema,
	).Scan(&pinned); err != nil {
		return err
	}
	if pinned != dialect.EngineSchema {
		return fmt.Errorf("set_config returned %q, want %q", pinned, dialect.EngineSchema)
	}
	return nil
}

func (h *schemaTransitionHook) projectSelected(
	ctx context.Context,
	tx *sql.Tx,
	after bool,
	preserve *schemaTransitionSnapshot,
) (*schemaTransitionSnapshot, error) {
	schema, err := h.dia.SchemaName(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("resolve schema-transition schema: %w", err)
	}
	live, err := h.dia.SchemaTriggers(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("project schema-transition triggers: %w", err)
	}

	snapshot := &schemaTransitionSnapshot{
		schema: schema, byTrigger: make(map[dialect.TriggerKey]transitionTriggerState, len(h.specs)),
	}
	for _, spec := range h.specs {
		key := dialect.TriggerKey{Schema: schema, Table: spec.table, Name: spec.name}
		if _, duplicate := snapshot.byTrigger[key]; duplicate {
			return nil, fmt.Errorf("schema transition v%d declares %s more than once", h.version, key)
		}
		info, ok := live[key]
		if !ok {
			return nil, fmt.Errorf("%w: %s", store.ErrSchemaTriggerMissing, key)
		}
		if !info.EnableState.Fires() {
			return nil, fmt.Errorf(
				"%w: %s: %s",
				store.ErrSchemaTriggerInert, key, info.EnableState.Describe(),
			)
		}
		if h.engine == store.EnginePostgres && !info.CanExecute {
			return nil, fmt.Errorf(
				"%w: %s via %s", store.ErrSchemaTriggerUnexecutable, key, info.Function,
			)
		}
		if h.engine == store.EngineSQLite {
			if info.EnableState != dialect.TriggerNoEnableState || !info.CanExecute ||
				info.Function != "" || info.FunctionSchema != "" || info.FunctionName != "" {
				return nil, fmt.Errorf(
					"schema transition v%d cannot prove exact SQLite trigger posture for %s",
					h.version, key,
				)
			}
		} else {
			wantFunction := spec.previousFunctionName
			if after {
				wantFunction = spec.nextFunctionName
			}
			if info.FunctionSchema != schema || info.FunctionName != wantFunction || info.Function == "" {
				return nil, fmt.Errorf(
					"schema transition v%d found function identity %q.%q for %s; want %q.%q",
					h.version, info.FunctionSchema, info.FunctionName, key, schema, wantFunction,
				)
			}
		}
		wantDigest := spec.previous
		if after {
			wantDigest = spec.next
		}
		gotDigest := sha256.Sum256([]byte(info.Definition))
		gotHex := hex.EncodeToString(gotDigest[:])
		if gotHex != wantDigest {
			return nil, fmt.Errorf(
				"%w: %s at transition v%d (want %s, live %s)",
				store.ErrSchemaTriggerTampered, key, h.version,
				shortDigest(wantDigest), shortDigest(gotHex),
			)
		}
		state := triggerTransitionState(info)
		if preserve != nil {
			before, ok := preserve.byTrigger[key]
			unchanged := ok && before == state
			if h.engine == store.EngineSQLite && ok {
				before.definition = ""
				stateWithoutDefinition := state
				stateWithoutDefinition.definition = ""
				unchanged = before == stateWithoutDefinition
			}
			if !unchanged {
				return nil, fmt.Errorf(
					"schema transition v%d changed the selected precondition for %s while acquiring its fences",
					h.version, key,
				)
			}
		}
		snapshot.byTrigger[key] = state
	}
	return snapshot, nil
}

func (h *schemaTransitionHook) postgresSurfaces() (
	dialect.SchemaTriggerCallerInventory,
	dialect.SchemaTriggerFunctionCatalog,
	error,
) {
	inventory, ok := h.dia.(dialect.SchemaTriggerCallerInventory)
	if !ok {
		return nil, nil, fmt.Errorf(
			"schema transition v%d cannot inventory PostgreSQL trigger-function callers",
			h.version,
		)
	}
	catalog, ok := h.dia.(dialect.SchemaTriggerFunctionCatalog)
	if !ok {
		return nil, nil, fmt.Errorf(
			"schema transition v%d cannot project and reserve PostgreSQL trigger functions",
			h.version,
		)
	}
	return inventory, catalog, nil
}

func (h *schemaTransitionHook) postgresFunctionSets(schema string) ([]transitionFunction, []transitionFunction) {
	oldSet := make(map[transitionFunction]bool)
	nextSet := make(map[transitionFunction]bool)
	for _, spec := range h.specs {
		oldSet[transitionFunction{schema: schema, name: spec.previousFunctionName}] = true
		nextSet[transitionFunction{schema: schema, name: spec.nextFunctionName}] = true
	}
	return sortedTransitionFunctions(oldSet), sortedTransitionFunctions(nextSet)
}

func sortedTransitionFunctions(set map[transitionFunction]bool) []transitionFunction {
	result := make([]transitionFunction, 0, len(set))
	for function := range set {
		result = append(result, function)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].schema != result[j].schema {
			return result[i].schema < result[j].schema
		}
		return result[i].name < result[j].name
	})
	return result
}

func transitionFunctionKeys(functions []transitionFunction) []dialect.SchemaTriggerFunctionKey {
	keys := make([]dialect.SchemaTriggerFunctionKey, len(functions))
	for i, function := range functions {
		keys[i] = function.dialectKey()
	}
	return keys
}

func projectTransitionFunctions(
	ctx context.Context,
	tx *sql.Tx,
	catalog dialect.SchemaTriggerFunctionCatalog,
	functions []transitionFunction,
) (map[transitionFunction]dialect.SchemaTriggerFunctionInfo, error) {
	result := make(map[transitionFunction]dialect.SchemaTriggerFunctionInfo, len(functions))
	for _, function := range functions {
		info, exists, err := catalog.SchemaTriggerFunction(ctx, tx, function.dialectKey())
		if err != nil {
			return nil, fmt.Errorf("project function %s: %w", function, err)
		}
		if !exists {
			return nil, fmt.Errorf("schema-transition function %s is missing", function)
		}
		result[function] = info
	}
	return result, nil
}

func validateTransitionReservations(
	reservations map[transitionFunction]dialect.SchemaTriggerFunctionInfo,
) error {
	set := make(map[transitionFunction]bool, len(reservations))
	for function := range reservations {
		set[function] = true
	}
	for _, function := range sortedTransitionFunctions(set) {
		info := reservations[function]
		if info.OID <= 0 || info.Definition == "" || info.ACL == "" || !info.ACLIsExact ||
			!info.CanExecute ||
			!info.AppRoleDirectExecute || info.PublicCanExecute {
			return fmt.Errorf(
				"reserved schema-transition function %s lacks its fail-closed definition, stable OID, exact application-role grant or PUBLIC revoke",
				function,
			)
		}
	}
	return nil
}

func validateFunctionWitnesses(
	want map[transitionFunction]dialect.SchemaTriggerFunctionInfo,
	live map[transitionFunction]dialect.SchemaTriggerFunctionInfo,
	reason string,
) error {
	set := make(map[transitionFunction]bool, len(want))
	for function := range want {
		set[function] = true
	}
	for _, function := range sortedTransitionFunctions(set) {
		got, ok := live[function]
		if !ok || got != want[function] {
			return fmt.Errorf("old schema-transition function %s %s", function, reason)
		}
	}
	return nil
}

func transitionSelectedKeys(snapshot *schemaTransitionSnapshot) map[dialect.TriggerKey]bool {
	selected := make(map[dialect.TriggerKey]bool, len(snapshot.byTrigger))
	for key := range snapshot.byTrigger {
		selected[key] = true
	}
	return selected
}

func validateSelectedCallerInventory(
	callers map[dialect.TriggerKey]dialect.TriggerInfo,
	selected *schemaTransitionSnapshot,
) error {
	for _, key := range sortedTransitionTriggerKeys(transitionSelectedKeys(selected)) {
		info, ok := callers[key]
		if !ok || triggerTransitionState(info) != selected.byTrigger[key] {
			return fmt.Errorf(
				"old function caller inventory does not contain the selected trigger %s byte-exactly",
				key,
			)
		}
	}
	return nil
}

func validateCallerWitnesses(
	want map[dialect.TriggerKey]dialect.TriggerInfo,
	live map[dialect.TriggerKey]dialect.TriggerInfo,
) error {
	keys := make(map[dialect.TriggerKey]bool, len(want))
	for key := range want {
		keys[key] = true
	}
	for _, key := range sortedTransitionTriggerKeys(keys) {
		got, ok := live[key]
		if !ok || got != want[key] {
			return fmt.Errorf("pre-existing caller %s is missing or changed", key)
		}
	}
	return nil
}

// exactNextFunctionCallerSet is sound across the final scan-to-commit window
// because every next function was created, and remains uncommitted, in this same
// transaction. An external session cannot resolve its identity until COMMIT; an
// extra caller visible here was therefore created by the migration itself.
func exactNextFunctionCallerSet(
	live map[dialect.TriggerKey]dialect.TriggerInfo,
	selected map[dialect.TriggerKey]bool,
	after *schemaTransitionSnapshot,
) error {
	for _, key := range sortedTransitionTriggerKeys(selected) {
		info, ok := live[key]
		state := after.byTrigger[key]
		if !ok || info.FunctionSchema != state.function.schema ||
			info.FunctionName != state.function.name {
			return fmt.Errorf(
				"installed function caller inventory does not contain selected trigger %s",
				key,
			)
		}
	}
	liveKeys := make(map[dialect.TriggerKey]bool, len(live))
	for key := range live {
		liveKeys[key] = true
	}
	for _, key := range sortedTransitionTriggerKeys(liveKeys) {
		if !selected[key] {
			return fmt.Errorf(
				"schema transition created undeclared caller %s of a newly reserved function",
				key,
			)
		}
	}
	return nil
}

func sortedTransitionTriggerKeys(set map[dialect.TriggerKey]bool) []dialect.TriggerKey {
	keys := make([]dialect.TriggerKey, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Schema != keys[j].Schema {
			return keys[i].Schema < keys[j].Schema
		}
		if keys[i].Table != keys[j].Table {
			return keys[i].Table < keys[j].Table
		}
		return keys[i].Name < keys[j].Name
	})
	return keys
}

func transitionCallerTables(callers map[dialect.TriggerKey]dialect.TriggerInfo) []dialect.TriggerKey {
	type tableIdentity struct{ schema, table string }
	set := make(map[tableIdentity]bool)
	for key := range callers {
		set[tableIdentity{schema: key.Schema, table: key.Table}] = true
	}
	result := make([]dialect.TriggerKey, 0, len(set))
	for table := range set {
		result = append(result, dialect.TriggerKey{Schema: table.schema, Table: table.table})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Schema != result[j].Schema {
			return result[i].Schema < result[j].Schema
		}
		return result[i].Table < result[j].Table
	})
	return result
}
