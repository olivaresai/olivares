// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

// guardshape.go declares the EXACT shape the three control-plane relations must have, so a
// boot can compare the live catalog against something rather than against nothing.
//
// WHY A SECOND DECLARATION EXISTS AT ALL, when the DDL above renders the same relations: the
// DDL says what to CREATE, and nothing in it can be asked what an EXISTING relation should
// look like. A preflight that only asked "does a table by this name have at least one
// column" classified a homonymous table with no ordinal uniqueness as a finished bootstrap —
// and the record then said version 6 was applied while the objects it names could not hold
// the invariants the ledger depends on.
//
// So the shape is declared, once, next to the DDL that produces it, and a regression against
// a real server proves the two agree. Duplication with a proof beats a single source nobody
// can check at runtime.
//
// EVERY VALUE HERE WAS MEASURED, not transcribed from the DDL by eye: the relations were
// created from GuardControlPlaneStmts on PostgreSQL 15.18 and the catalog was read back. That
// matters most for the CHECK definitions, which are the DEPARSER's rendering rather than the
// text this file emits — PostgreSQL rewrites `x IN ('a','b')` as `x = ANY (ARRAY['a'::text,
// 'b'::text])`, adds parentheses and drops the pg_catalog qualification of functions it can
// resolve through search_path. Declaring what the DDL says instead of what the server answers
// would refuse every correct database.
//
// THE CONSEQUENCE OF THAT, stated because it bounds what the caller may enforce: a deparser
// rendering is a property of the SERVER VERSION. The comparison of Definition therefore
// belongs only to the major this repository has actually run, and the caller
// (verifyGuardControlPlaneShape) enforces it there and compares the constrained COLUMNS
// everywhere. Certifying a new major and enforcing its renderings are the same act.

// GuardColumnShape is one column: its name, the type as format_type renders it, and whether
// it is NOT NULL.
//
// The type is the RENDERED form ("bigint", not "BIGINT" and not "int8"), because that is what
// pg_catalog.format_type answers and comparing anything else would compare a spelling.
type GuardColumnShape struct {
	Name    string
	Type    string
	NotNull bool
}

// GuardCheckShape is one CHECK constraint, identified by the columns it constrains rather
// than by its name.
//
// The name is deliberately absent: PostgreSQL generates it from the relation and the columns
// and TRUNCATES it at 63 bytes, so two constraints on this schema already carry names that
// are artifacts of that truncation. What identifies a check for this purpose is what it
// constrains and what it says.
//
// Columns is the comma-joined attribute names in conkey order.
type GuardCheckShape struct {
	Columns    string
	Definition string
}

// GuardIndexShape is one index this schema NAMES.
//
// Only the explicitly named, non-constraint indexes appear here. The indexes PostgreSQL
// creates to back a PRIMARY KEY or a UNIQUE constraint are identified by their column tuple
// through UniqueKeys instead, because their names are server-chosen.
type GuardIndexShape struct {
	Name    string
	Columns string
}

// GuardRelationShape is the whole declared shape of one control-plane relation.
type GuardRelationShape struct {
	Relation string
	// PrimaryKey is the comma-joined column tuple of the PRIMARY KEY.
	PrimaryKey string
	// UniqueKeys is every uniquely-indexed column tuple, INCLUDING the primary key's. A
	// uniqueness the database has lost is the defect this whole file exists to catch, so the
	// set is compared exactly: neither a missing tuple nor an extra one is tolerated.
	UniqueKeys []string
	// Indexes is the named, non-unique indexes.
	Indexes []GuardIndexShape
	// Columns is every column, in attnum order.
	Columns []GuardColumnShape
	// Checks is every CHECK constraint.
	Checks []GuardCheckShape
}

// GuardControlPlaneShapePostgres is the declared shape of the three relations on PostgreSQL.
//
// It is a free function rather than a Dialect method because SQLite needs nothing like it:
// sqlite_master stores the CREATE statement VERBATIM, so a SQLite deployment is verified by
// comparing that text with the text this package renders — an exact comparison that no
// declaration can improve on. Putting a nil-returning method on the interface would suggest
// the two engines verify the same way with one of them opting out, and they do not.
func GuardControlPlaneShapePostgres() []GuardRelationShape {
	return []GuardRelationShape{
		{
			Relation:   GuardGateEventsTable,
			PrimaryKey: "event_sha256",
			UniqueKeys: []string{
				"event_sha256",
				"rollout_id,event_ordinal",
				"rollout_id,unit_id,diagnostic_fingerprint",
			},
			Indexes: []GuardIndexShape{
				{Name: GuardGateEventsTable + "_unit_idx", Columns: "rollout_id,unit_id,event_ordinal"},
			},
			Columns: []GuardColumnShape{
				{Name: "event_sha256", Type: "bytea", NotNull: true},
				{Name: "rollout_id", Type: "text", NotNull: true},
				{Name: "event_ordinal", Type: "bigint", NotNull: true},
				{Name: "prev_event_sha256", Type: "bytea", NotNull: false},
				{Name: "kind", Type: "text", NotNull: true},
				{Name: "unit_id", Type: "text", NotNull: false},
				{Name: "attempt_id", Type: "text", NotNull: false},
				{Name: "intent", Type: "text", NotNull: false},
				{Name: "relation_schema", Type: "text", NotNull: false},
				{Name: "relation_name", Type: "text", NotNull: false},
				{Name: "trigger_name", Type: "text", NotNull: false},
				{Name: "manifest_format", Type: "bigint", NotNull: true},
				{Name: "code_epoch", Type: "bigint", NotNull: true},
				{Name: "code_sha256", Type: "bytea", NotNull: true},
				{Name: "retained_revision", Type: "bigint", NotNull: true},
				{Name: "retained_sha256", Type: "bytea", NotNull: true},
				{Name: "spec_sha256", Type: "bytea", NotNull: false},
				{Name: "definition_sha256", Type: "bytea", NotNull: false},
				{Name: "prestate_sha256", Type: "bytea", NotNull: false},
				{Name: "prestate_target_exists", Type: "boolean", NotNull: false},
				{Name: "prestate_guard_present", Type: "boolean", NotNull: false},
				{Name: "prestate_guard_state", Type: "text", NotNull: false},
				{Name: "prestate_guard_canonical", Type: "boolean", NotNull: false},
				{Name: "prestate_receipt_present", Type: "boolean", NotNull: false},
				{Name: "prestate_bytes", Type: "text", NotNull: false},
				{Name: "phase", Type: "text", NotNull: true},
				{Name: "gate_condition", Type: "text", NotNull: true},
				{Name: "diagnostic_code", Type: "text", NotNull: true},
				{Name: "retry_class", Type: "text", NotNull: true},
				{Name: "unblock_policy", Type: "text", NotNull: true},
				{Name: "sqlstate", Type: "text", NotNull: true},
				{Name: "expected_sha", Type: "text", NotNull: true},
				{Name: "observed_sha", Type: "text", NotNull: true},
				{Name: "diagnostic_fingerprint", Type: "bytea", NotNull: false},
				{Name: "details", Type: "text", NotNull: true},
				{Name: "expected_units", Type: "text", NotNull: true},
				{Name: "checkpoint_inventory_sha256", Type: "bytea", NotNull: false},
				{Name: "checkpoint_inventory_count", Type: "bigint", NotNull: false},
				{Name: "checkpoint_receipt_sha256", Type: "bytea", NotNull: false},
				{Name: "checkpoint_receipt_count", Type: "bigint", NotNull: false},
				{Name: "build_id", Type: "text", NotNull: true},
				{Name: "actor", Type: "text", NotNull: true},
				{Name: "recorded_at", Type: "text", NotNull: true},
			},
			Checks: []GuardCheckShape{
				{Columns: "checkpoint_inventory_count", Definition: `CHECK (((checkpoint_inventory_count IS NULL) OR (checkpoint_inventory_count >= 0)))`},
				{Columns: "checkpoint_inventory_sha256", Definition: `CHECK (((checkpoint_inventory_sha256 IS NULL) OR (octet_length(checkpoint_inventory_sha256) = 32)))`},
				{Columns: "checkpoint_receipt_count", Definition: `CHECK (((checkpoint_receipt_count IS NULL) OR (checkpoint_receipt_count >= 0)))`},
				{Columns: "checkpoint_receipt_sha256", Definition: `CHECK (((checkpoint_receipt_sha256 IS NULL) OR (octet_length(checkpoint_receipt_sha256) = 32)))`},
				{Columns: "code_epoch", Definition: `CHECK ((code_epoch > 0))`},
				{Columns: "code_sha256", Definition: `CHECK ((octet_length(code_sha256) = 32))`},
				{Columns: "definition_sha256", Definition: `CHECK (((definition_sha256 IS NULL) OR (octet_length(definition_sha256) = 32)))`},
				{Columns: "diagnostic_fingerprint", Definition: `CHECK (((diagnostic_fingerprint IS NULL) OR (octet_length(diagnostic_fingerprint) = 32)))`},
				{Columns: "event_ordinal", Definition: `CHECK ((event_ordinal > 0))`},
				{Columns: "event_ordinal,prev_event_sha256", Definition: `CHECK ((((event_ordinal = 1) AND (prev_event_sha256 IS NULL)) OR ((event_ordinal > 1) AND (octet_length(prev_event_sha256) = 32))))`},
				{Columns: "event_sha256", Definition: `CHECK ((octet_length(event_sha256) = 32))`},
				{Columns: "gate_condition", Definition: `CHECK ((gate_condition = ANY (ARRAY['clean'::text, 'retryable'::text, 'blocked'::text, 'verified'::text])))`},
				{Columns: "kind", Definition: `CHECK ((kind = ANY (ARRAY['pending-opened'::text, 'attempt-started'::text, 'attempt-judged'::text, 'attempt-failed'::text, 'verification-failed'::text, 'reconciled'::text, 'ready'::text])))`},
				{Columns: "kind,checkpoint_inventory_sha256,checkpoint_inventory_count,checkpoint_receipt_sha256,checkpoint_receipt_count", Definition: `CHECK ((((kind = 'ready'::text) AND (checkpoint_inventory_sha256 IS NOT NULL) AND (checkpoint_inventory_count IS NOT NULL) AND (checkpoint_receipt_sha256 IS NOT NULL) AND (checkpoint_receipt_count IS NOT NULL)) OR ((kind <> 'ready'::text) AND (checkpoint_inventory_sha256 IS NULL) AND (checkpoint_inventory_count IS NULL) AND (checkpoint_receipt_sha256 IS NULL) AND (checkpoint_receipt_count IS NULL))))`},
				{Columns: "kind,unit_id", Definition: `CHECK ((((kind = ANY (ARRAY['pending-opened'::text, 'ready'::text])) AND (unit_id IS NULL)) OR ((kind <> ALL (ARRAY['pending-opened'::text, 'ready'::text])) AND (unit_id IS NOT NULL))))`},
				{Columns: "manifest_format", Definition: `CHECK ((manifest_format > 0))`},
				{Columns: "phase", Definition: `CHECK ((phase = ANY (ARRAY['pending'::text, 'ready'::text])))`},
				{Columns: "prestate_sha256", Definition: `CHECK (((prestate_sha256 IS NULL) OR (octet_length(prestate_sha256) = 32)))`},
				{Columns: "retained_revision", Definition: `CHECK ((retained_revision >= 0))`},
				{Columns: "retained_sha256", Definition: `CHECK ((octet_length(retained_sha256) = 32))`},
				{Columns: "spec_sha256", Definition: `CHECK (((spec_sha256 IS NULL) OR (octet_length(spec_sha256) = 32)))`},
				{Columns: "unblock_policy", Definition: `CHECK ((unblock_policy = ANY (ARRAY[''::text, 'read_reconcile'::text, 'operator'::text])))`},
			},
		},
		{
			Relation:   GuardInventoryEventsTable,
			PrimaryKey: "event_sha256",
			UniqueKeys: []string{
				"event_sha256",
				"event_ordinal",
			},
			Indexes: []GuardIndexShape{
				{Name: GuardInventoryEventsTable + "_entry_idx", Columns: "relation_schema,relation_name,trigger_name,event_ordinal"},
			},
			Columns: []GuardColumnShape{
				{Name: "event_sha256", Type: "bytea", NotNull: true},
				{Name: "event_ordinal", Type: "bigint", NotNull: true},
				{Name: "prev_event_sha256", Type: "bytea", NotNull: false},
				{Name: "kind", Type: "text", NotNull: true},
				{Name: "relation_schema", Type: "text", NotNull: true},
				{Name: "relation_name", Type: "text", NotNull: true},
				{Name: "trigger_name", Type: "text", NotNull: true},
				{Name: "producer", Type: "text", NotNull: true},
				{Name: "manifest_format", Type: "bigint", NotNull: true},
				{Name: "code_epoch", Type: "bigint", NotNull: true},
				{Name: "definition_sha256", Type: "bytea", NotNull: true},
				{Name: "spec_sha256", Type: "bytea", NotNull: true},
				{Name: "desired_enable_state", Type: "text", NotNull: true},
				{Name: "legacy_allowed_states", Type: "text", NotNull: true},
				{Name: "retained_revision", Type: "bigint", NotNull: true},
				{Name: "retained_sha256", Type: "bytea", NotNull: true},
				{Name: "recorded_at", Type: "text", NotNull: true},
			},
			Checks: []GuardCheckShape{
				{Columns: "code_epoch", Definition: `CHECK ((code_epoch > 0))`},
				{Columns: "definition_sha256", Definition: `CHECK ((octet_length(definition_sha256) = 32))`},
				{Columns: "desired_enable_state", Definition: `CHECK (((octet_length(desired_enable_state) = 1) AND (desired_enable_state = ANY (ARRAY['O'::text, 'A'::text]))))`},
				{Columns: "event_ordinal", Definition: `CHECK ((event_ordinal > 0))`},
				{Columns: "event_ordinal,prev_event_sha256", Definition: `CHECK ((((event_ordinal = 1) AND (prev_event_sha256 IS NULL)) OR ((event_ordinal > 1) AND (octet_length(prev_event_sha256) = 32))))`},
				{Columns: "event_sha256", Definition: `CHECK ((octet_length(event_sha256) = 32))`},
				{Columns: "kind", Definition: `CHECK ((kind = ANY (ARRAY['activate'::text, 'retain'::text, 'reactivate'::text, 'tombstone'::text])))`},
				{Columns: "manifest_format", Definition: `CHECK ((manifest_format > 0))`},
				{Columns: "retained_revision", Definition: `CHECK ((retained_revision >= 0))`},
				{Columns: "retained_sha256", Definition: `CHECK ((octet_length(retained_sha256) = 32))`},
				{Columns: "spec_sha256", Definition: `CHECK ((octet_length(spec_sha256) = 32))`},
			},
		},
		{
			Relation:   GuardReceiptsTable,
			PrimaryKey: "receipt_id",
			UniqueKeys: []string{
				"receipt_id",
				"rollout_id,event_ordinal",
				"rollout_id,unit_id,receipt_kind",
			},
			Indexes: []GuardIndexShape{
				{Name: GuardReceiptsTable + "_target_idx", Columns: "rollout_id,relation_schema,relation_name,trigger_name,receipt_kind"},
			},
			Columns: []GuardColumnShape{
				{Name: "receipt_id", Type: "bytea", NotNull: true},
				{Name: "rollout_id", Type: "text", NotNull: true},
				{Name: "unit_id", Type: "text", NotNull: true},
				{Name: "receipt_kind", Type: "text", NotNull: true},
				{Name: "intent", Type: "text", NotNull: true},
				{Name: "relation_schema", Type: "text", NotNull: true},
				{Name: "relation_name", Type: "text", NotNull: true},
				{Name: "trigger_name", Type: "text", NotNull: true},
				{Name: "epoch", Type: "bigint", NotNull: true},
				{Name: "manifest_format", Type: "bigint", NotNull: true},
				{Name: "code_sha256", Type: "bytea", NotNull: true},
				{Name: "retained_revision", Type: "bigint", NotNull: true},
				{Name: "retained_sha256", Type: "bytea", NotNull: true},
				{Name: "spec_sha256", Type: "bytea", NotNull: true},
				{Name: "definition_sha256", Type: "bytea", NotNull: true},
				{Name: "prestate_sha256", Type: "bytea", NotNull: true},
				{Name: "from_enable_state", Type: "text", NotNull: false},
				{Name: "to_enable_state", Type: "text", NotNull: true},
				{Name: "predecessor_receipt_id", Type: "bytea", NotNull: false},
				{Name: "attempt_id", Type: "text", NotNull: true},
				{Name: "event_ordinal", Type: "bigint", NotNull: true},
				{Name: "prev_event_sha256", Type: "bytea", NotNull: false},
				{Name: "event_sha256", Type: "bytea", NotNull: true},
				{Name: "applied_at", Type: "text", NotNull: true},
			},
			Checks: []GuardCheckShape{
				{Columns: "code_sha256", Definition: `CHECK ((octet_length(code_sha256) = 32))`},
				{Columns: "definition_sha256", Definition: `CHECK ((octet_length(definition_sha256) = 32))`},
				{Columns: "epoch", Definition: `CHECK ((epoch > 0))`},
				{Columns: "event_ordinal", Definition: `CHECK ((event_ordinal > 0))`},
				{Columns: "event_ordinal,prev_event_sha256", Definition: `CHECK ((((event_ordinal = 1) AND (prev_event_sha256 IS NULL)) OR ((event_ordinal > 1) AND (octet_length(prev_event_sha256) = 32))))`},
				{Columns: "event_sha256", Definition: `CHECK ((octet_length(event_sha256) = 32))`},
				{Columns: "from_enable_state", Definition: `CHECK (((from_enable_state IS NULL) OR ((octet_length(from_enable_state) = 1) AND (from_enable_state = ANY (ARRAY['O'::text, 'A'::text, 'D'::text, 'R'::text])))))`},
				{Columns: "manifest_format", Definition: `CHECK ((manifest_format > 0))`},
				{Columns: "predecessor_receipt_id", Definition: `CHECK (((predecessor_receipt_id IS NULL) OR (octet_length(predecessor_receipt_id) = 32)))`},
				{Columns: "prestate_sha256", Definition: `CHECK ((octet_length(prestate_sha256) = 32))`},
				{Columns: "receipt_id", Definition: `CHECK ((octet_length(receipt_id) = 32))`},
				{Columns: "receipt_kind", Definition: `CHECK ((receipt_kind = ANY (ARRAY['bootstrap'::text, 'unit'::text])))`},
				{Columns: "receipt_kind,intent", Definition: `CHECK ((((receipt_kind = 'bootstrap'::text) AND (intent = 'bootstrap'::text)) OR ((receipt_kind = 'unit'::text) AND (intent = ANY (ARRAY['create-guard'::text, 'adopt-legacy'::text, 'transition-legacy-o-to-a'::text, 'repair'::text])))))`},
				{Columns: "retained_revision", Definition: `CHECK ((retained_revision >= 0))`},
				{Columns: "retained_sha256", Definition: `CHECK ((octet_length(retained_sha256) = 32))`},
				{Columns: "spec_sha256", Definition: `CHECK ((octet_length(spec_sha256) = 32))`},
				{Columns: "to_enable_state", Definition: `CHECK (((octet_length(to_enable_state) = 1) AND (to_enable_state = ANY (ARRAY['O'::text, 'A'::text]))))`},
			},
		},
	}
}
