// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dialect

import "fmt"

// guardplane.go renders the C4 guard control plane: the three relations the append-only
// rollout keeps its own history in.
//
// They are LOGS OF INSERTS. There is no UPDATE, no DELETE and no TRUNCATE, and there is
// no mutable "current state" row anywhere — the gate's phase and condition are a FOLD
// over its events. That shape is the point: a mutable row can be set to `ready` by
// anything that can write it, whereas a fold can only reach `ready` by an event that had
// to be appended with its predecessor's hash.
//
// The three carry append-only guards in ALWAYS, not ORIGIN, and the difference was
// measured rather than assumed: certified on 15.18, a guard left at 'O' let a publisher
// UPDATE apply on a logical-replication subscriber with ZERO errors while 'A' preserved
// the row and raised three apply errors.
//
// WHAT THE ACL POSTURE IS, stated exactly because it differs from every other append-only
// table in this schema AND because it is CONDITIONAL: where owner and application are
// genuinely different roles, the application role is revoked INSERT as well as UPDATE,
// DELETE and TRUNCATE. Runtime traffic has no business writing this history — the owner pool
// writes it during boot and is closed before the store serves. Adding these to the registry's
// append-only set would do the OPPOSITE: verifyAppendOnlyACL demands SELECT and INSERT of
// every REGISTERED table, so registering them would require the app role to be able to
// append gate events.
//
// The revoke IS in this DDL, conditioned on the application role not owning the relation —
// see the note after GuardControlPlaneStmts. Unconditional, it would revoke the engine's own
// writer under a single role; absent, it would leave the three logs insertable by the
// application role for the whole of the first rollout.
//
// The limit is declared, not implied: when OwnerDSN is empty, equal to DSN, or resolves to
// the same role, the INSERT posture is neither applied nor verified. The gate stays durable
// against a crash, and that role can fabricate events. It cannot be labeled resistant to it.

// The three control-plane relations. The names are normative (step-2 design §5).
const (
	// GuardGateEventsTable is the rollout's own history: which edition opened, which
	// attempts started, what was judged under the lock, what failed and why, and the
	// single `ready` that closes an edition.
	GuardGateEventsTable = "olivares_guard_gate_events"
	// GuardInventoryEventsTable is the lifecycle of managed entries: activation now,
	// and retention/reactivation/tombstones in later editions.
	GuardInventoryEventsTable = "olivares_guard_inventory_events"
	// GuardReceiptsTable attributes confirmed units, with the precedence links that let a
	// later boot verify a lineage rather than re-derive it from the catalog.
	GuardReceiptsTable = "olivares_guard_receipts"
)

// GuardControlPlaneTables lists the three relations in the FIXED order the lock plan's
// common metadata prefix takes them.
//
// A common prefix is what stops two units forming a lock cycle with each other, and a
// prefix that is common but differently ORDERED provides none of that. Exporting the
// order from one place means the plan and the DDL cannot disagree about it.
func GuardControlPlaneTables() []string {
	return []string{GuardGateEventsTable, GuardInventoryEventsTable, GuardReceiptsTable}
}

// BlockMutationFn is the shared trigger function every append-only guard calls.
//
// Exported so the guard manifest can name the function it expects WITHOUT copying the
// literal. Two constants that must agree, with nothing checking that they do, is how a
// manifest ends up describing an object nothing creates.
const BlockMutationFn = blockMutationFn

// GuardControlPlaneStmts renders the control plane's DDL for one engine.
//
// It is DDL ONLY: tables, guards and ACL. The bootstrap ROWS — the inventory activations,
// the first `pending-opened` and the bootstrap receipts — are written by the engine with
// BOUND parameters inside the same migration transaction, because they carry digests and
// timestamps that must not be rendered into a SQL string. See migrate.Migration.Exec.
func (d postgresDialect) GuardControlPlaneStmts() []string {
	out := []string{
		`CREATE TABLE ` + GuardGateEventsTable + ` (
  event_sha256 BYTEA PRIMARY KEY CHECK (pg_catalog.octet_length(event_sha256) = 32),
  rollout_id TEXT NOT NULL,
  event_ordinal BIGINT NOT NULL CHECK (event_ordinal > 0),
  prev_event_sha256 BYTEA,
  kind TEXT NOT NULL CHECK (kind IN ('pending-opened','attempt-started','attempt-judged','attempt-failed','verification-failed','reconciled','ready')),
  unit_id TEXT,
  attempt_id TEXT,
  intent TEXT,
  relation_schema TEXT,
  relation_name TEXT,
  trigger_name TEXT,
  manifest_format BIGINT NOT NULL CHECK (manifest_format > 0),
  code_epoch BIGINT NOT NULL CHECK (code_epoch > 0),
  code_sha256 BYTEA NOT NULL CHECK (pg_catalog.octet_length(code_sha256) = 32),
  retained_revision BIGINT NOT NULL CHECK (retained_revision >= 0),
  retained_sha256 BYTEA NOT NULL CHECK (pg_catalog.octet_length(retained_sha256) = 32),
  spec_sha256 BYTEA CHECK (spec_sha256 IS NULL OR pg_catalog.octet_length(spec_sha256) = 32),
  definition_sha256 BYTEA CHECK (definition_sha256 IS NULL OR pg_catalog.octet_length(definition_sha256) = 32),
  prestate_sha256 BYTEA CHECK (prestate_sha256 IS NULL OR pg_catalog.octet_length(prestate_sha256) = 32),
  -- The judged prestate's OBSERVABLE fields, typed, so a later boot can RECONSTRUCT the
  -- reading that authorized a change instead of trusting a digest it cannot reproduce. The
  -- reconstruction is then re-hashed and compared with prestate_sha256, which is what makes
  -- it verifiable rather than merely available.
  prestate_target_exists BOOLEAN,
  prestate_guard_present BOOLEAN,
  prestate_guard_state TEXT,
  prestate_guard_canonical BOOLEAN,
  prestate_receipt_present BOOLEAN,
  prestate_bytes TEXT,
  phase TEXT NOT NULL CHECK (phase IN ('pending','ready')),
  gate_condition TEXT NOT NULL CHECK (gate_condition IN ('clean','retryable','blocked','verified')),
  diagnostic_code TEXT NOT NULL,
  retry_class TEXT NOT NULL,
  unblock_policy TEXT NOT NULL CHECK (unblock_policy IN ('','read_reconcile','operator')),
  sqlstate TEXT NOT NULL,
  expected_sha TEXT NOT NULL,
  observed_sha TEXT NOT NULL,
  diagnostic_fingerprint BYTEA CHECK (diagnostic_fingerprint IS NULL OR pg_catalog.octet_length(diagnostic_fingerprint) = 32),
  details TEXT NOT NULL,
  expected_units TEXT NOT NULL,
  -- THE CHECKPOINT. A ready event records the HEAD and the COUNT of the other two logs, so a
  -- row removed from either of them after closure has no successor to hide behind. Without it a
  -- verified chain only ever proved the prefix that still existed.
  checkpoint_inventory_sha256 BYTEA
    CHECK (checkpoint_inventory_sha256 IS NULL OR pg_catalog.octet_length(checkpoint_inventory_sha256) = 32),
  checkpoint_inventory_count BIGINT CHECK (checkpoint_inventory_count IS NULL OR checkpoint_inventory_count >= 0),
  checkpoint_receipt_sha256 BYTEA
    CHECK (checkpoint_receipt_sha256 IS NULL OR pg_catalog.octet_length(checkpoint_receipt_sha256) = 32),
  checkpoint_receipt_count BIGINT CHECK (checkpoint_receipt_count IS NULL OR checkpoint_receipt_count >= 0),
  build_id TEXT NOT NULL,
  actor TEXT NOT NULL,
  recorded_at TEXT NOT NULL,
  CHECK (
    (event_ordinal = 1 AND prev_event_sha256 IS NULL)
    OR
    (event_ordinal > 1 AND pg_catalog.octet_length(prev_event_sha256) = 32)
  ),
  -- The four checkpoint columns are present as a GROUP and ONLY on a ready event. A partial
  -- checkpoint would attest half a history, and a checkpoint on any other event would be an
  -- attestation nothing computed.
  CHECK (
    (kind = 'ready'
      AND checkpoint_inventory_sha256 IS NOT NULL AND checkpoint_inventory_count IS NOT NULL
      AND checkpoint_receipt_sha256 IS NOT NULL AND checkpoint_receipt_count IS NOT NULL)
    OR
    (kind <> 'ready'
      AND checkpoint_inventory_sha256 IS NULL AND checkpoint_inventory_count IS NULL
      AND checkpoint_receipt_sha256 IS NULL AND checkpoint_receipt_count IS NULL)
  ),
  CHECK (
    (kind IN ('pending-opened','ready') AND unit_id IS NULL)
    OR
    (kind NOT IN ('pending-opened','ready') AND unit_id IS NOT NULL)
  ),
  UNIQUE (rollout_id, event_ordinal),
  UNIQUE (rollout_id, unit_id, diagnostic_fingerprint)
)`,
		`CREATE INDEX ` + GuardGateEventsTable + `_unit_idx
    ON ` + GuardGateEventsTable + ` (rollout_id, unit_id, event_ordinal)`,

		`CREATE TABLE ` + GuardInventoryEventsTable + ` (
  event_sha256 BYTEA PRIMARY KEY CHECK (pg_catalog.octet_length(event_sha256) = 32),
  event_ordinal BIGINT NOT NULL UNIQUE CHECK (event_ordinal > 0),
  prev_event_sha256 BYTEA,
  kind TEXT NOT NULL CHECK (kind IN ('activate','retain','reactivate','tombstone')),
  relation_schema TEXT NOT NULL,
  relation_name TEXT NOT NULL,
  trigger_name TEXT NOT NULL,
  producer TEXT NOT NULL,
  manifest_format BIGINT NOT NULL CHECK (manifest_format > 0),
  code_epoch BIGINT NOT NULL CHECK (code_epoch > 0),
  definition_sha256 BYTEA NOT NULL CHECK (pg_catalog.octet_length(definition_sha256) = 32),
  spec_sha256 BYTEA NOT NULL CHECK (pg_catalog.octet_length(spec_sha256) = 32),
  desired_enable_state TEXT NOT NULL CHECK (
    pg_catalog.octet_length(desired_enable_state) = 1
    AND desired_enable_state IN ('O','A')
  ),
  legacy_allowed_states TEXT NOT NULL,
  retained_revision BIGINT NOT NULL CHECK (retained_revision >= 0),
  retained_sha256 BYTEA NOT NULL CHECK (pg_catalog.octet_length(retained_sha256) = 32),
  recorded_at TEXT NOT NULL,
  CHECK (
    (event_ordinal = 1 AND prev_event_sha256 IS NULL)
    OR
    (event_ordinal > 1 AND pg_catalog.octet_length(prev_event_sha256) = 32)
  )
)`,
		`CREATE INDEX ` + GuardInventoryEventsTable + `_entry_idx
    ON ` + GuardInventoryEventsTable + ` (relation_schema, relation_name, trigger_name, event_ordinal)`,

		`CREATE TABLE ` + GuardReceiptsTable + ` (
  receipt_id BYTEA PRIMARY KEY CHECK (pg_catalog.octet_length(receipt_id) = 32),
  rollout_id TEXT NOT NULL,
  unit_id TEXT NOT NULL,
  receipt_kind TEXT NOT NULL CHECK (receipt_kind IN ('bootstrap','unit')),
  intent TEXT NOT NULL,
  relation_schema TEXT NOT NULL,
  relation_name TEXT NOT NULL,
  trigger_name TEXT NOT NULL,
  epoch BIGINT NOT NULL CHECK (epoch > 0),
  manifest_format BIGINT NOT NULL CHECK (manifest_format > 0),
  code_sha256 BYTEA NOT NULL CHECK (pg_catalog.octet_length(code_sha256) = 32),
  retained_revision BIGINT NOT NULL CHECK (retained_revision >= 0),
  retained_sha256 BYTEA NOT NULL CHECK (pg_catalog.octet_length(retained_sha256) = 32),
  spec_sha256 BYTEA NOT NULL CHECK (pg_catalog.octet_length(spec_sha256) = 32),
  definition_sha256 BYTEA NOT NULL CHECK (pg_catalog.octet_length(definition_sha256) = 32),
  prestate_sha256 BYTEA NOT NULL CHECK (pg_catalog.octet_length(prestate_sha256) = 32),
  from_enable_state TEXT,
  to_enable_state TEXT NOT NULL,
  predecessor_receipt_id BYTEA,
  attempt_id TEXT NOT NULL,
  event_ordinal BIGINT NOT NULL CHECK (event_ordinal > 0),
  prev_event_sha256 BYTEA,
  event_sha256 BYTEA NOT NULL CHECK (pg_catalog.octet_length(event_sha256) = 32),
  applied_at TEXT NOT NULL,
  CHECK (
    (receipt_kind = 'bootstrap' AND intent = 'bootstrap')
    OR
    (receipt_kind = 'unit' AND intent IN (
      'create-guard',
      'adopt-legacy',
      'transition-legacy-o-to-a',
      'repair'
    ))
  ),
  CHECK (
    from_enable_state IS NULL
    OR (
      pg_catalog.octet_length(from_enable_state) = 1
      AND from_enable_state IN ('O','A','D','R')
    )
  ),
  CHECK (
    pg_catalog.octet_length(to_enable_state) = 1
    AND to_enable_state IN ('O','A')
  ),
  CHECK (
    predecessor_receipt_id IS NULL
    OR pg_catalog.octet_length(predecessor_receipt_id) = 32
  ),
  CHECK (
    (event_ordinal = 1 AND prev_event_sha256 IS NULL)
    OR
    (event_ordinal > 1 AND pg_catalog.octet_length(prev_event_sha256) = 32)
  ),
  UNIQUE (rollout_id, unit_id, receipt_kind),
  UNIQUE (rollout_id, event_ordinal)
)`,
		`CREATE INDEX ` + GuardReceiptsTable + `_target_idx
    ON ` + GuardReceiptsTable + `
       (rollout_id, relation_schema, relation_name, trigger_name, receipt_kind)`,
	}

	// The guards, in ALWAYS. Each table gets the same immutability trigger every other
	// append-only table carries, and then the one statement that separates this class from
	// the rest: ENABLE ALWAYS, applied here rather than adopted later, because these
	// relations are created by this migration and have no legacy history to adopt.
	for _, t := range GuardControlPlaneTables() {
		out = append(out,
			fmt.Sprintf("CREATE TRIGGER %s%s BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()",
				t, guardTriggerSuffix, t, blockMutationFn),
			fmt.Sprintf("ALTER TABLE ONLY %s ENABLE ALWAYS TRIGGER %s%s", t, t, guardTriggerSuffix),
		)
	}
	// AND THE REVOKE, IN THIS TRANSACTION, WHENEVER THE APPLICATION ROLE IS NOT THE OWNER.
	//
	// The three relations are born INSERTABLE by the application role on a split topology,
	// and it is the provisioner that makes them so: deploy/postgres/01-app-role.sql runs
	// ALTER DEFAULT PRIVILEGES ... GRANT SELECT, INSERT, UPDATE, DELETE, so every FUTURE table
	// the owner creates arrives with those privileges already granted to app. The guards stop
	// UPDATE and DELETE. Nothing stopped INSERT.
	//
	// Reconciling at boot is not sufficient by itself, because the window opens the moment this
	// migration commits: between that commit and the reconcile the application pool can append
	// to the very history that says which schema changes were authorized. So the posture is
	// applied HERE, in the transaction that creates the relations, and re-asserted at boot for
	// databases created before this migration existed.
	//
	// UNLESS THE ROLE IS THE OWNER, which is the condition that made the earlier, unconditional
	// version of this revoke fatal rather than merely wrong. Under a single role the owner pool
	// IS the application pool, so revoking would disable the engine's own writer and the rollout
	// would fail 42501 opening itself a few statements later. The test is the OWNER of the
	// relation as the server reports it, not a name comparison in Go: it is the same question
	// PostgreSQL will answer when the revoke runs, asked of the same catalog.
	for _, t := range GuardControlPlaneTables() {
		out = append(out, pgRevokeAllWritesUnlessOwner(t, d.appRole))
	}
	return out
}

// pgRevokeAllWritesUnlessOwner revokes the four write privileges from a role, and does
// nothing at all when that role owns the relation.
//
// Skipping the owner is not politeness: PostgreSQL lets an owner revoke its own privileges
// and then silently re-grant them, so the revoke buys nothing there — while on the single-role
// topology it disables the only role that can write the control plane at all.
//
// The rendering discipline is pgRevokeAllWrites': the role and the relation are RUNTIME data,
// so they cross into SQL once as dollar-quoted literals whose tag is chosen against the value,
// and the identifiers are quoted SERVER-side. The relation is resolved through to_regclass so
// the search_path in force decides which relation is meant, exactly as the CREATE TABLE above
// did — comparing a bare name against pg_class would match a same-named relation in another
// schema.
func pgRevokeAllWritesUnlessOwner(table, role string) string {
	body := fmt.Sprintf(`
DECLARE
  target text := %s;
  rel    text := %s;
  relid  oid;
BEGIN
  relid := pg_catalog.to_regclass(pg_catalog.quote_ident(rel));
  IF relid IS NULL THEN
    RETURN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = target) THEN
    RETURN;
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_catalog.pg_class c
    JOIN pg_catalog.pg_roles r ON r.oid = c.relowner
    WHERE c.oid = relid AND r.rolname = target
  ) THEN
    RETURN;
  END IF;
  EXECUTE pg_catalog.format('REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON %%s FROM %%I', relid::regclass::text, target);
END `, pgDollarQuote(role), pgDollarQuote(table))
	tag := pgDollarTagNotIn(body)
	return "DO " + tag + body + tag
}

// THE REVOKE IS HERE **AND** AT BOOT, AND THE TWO ARE NOT REDUNDANT.
//
// The history of this comment is worth keeping, because both earlier versions were wrong in
// opposite directions. The first revoked INSERT unconditionally as the relations were created:
// right on the split topology, FATAL on the single-role default, where the owner pool is the
// application pool and the rollout would have revoked its own writer and then failed 42501
// opening itself. The second removed the revoke from the DDL entirely and left it to
// reconcileGuardMetadataACL at boot: correct about the topology, and it left the relations
// INSERTABLE by the application role from the instant this migration committed until the
// reconcile ran — a window that spans the whole of the first rollout.
//
// What closes both is the CONDITION rather than the placement. pgRevokeAllWritesUnlessOwner
// runs in this transaction and asks the server who owns the relation, so:
//
//   - split topology: the application role is not the owner, the revoke applies, and the three
//     relations are never insertable by it — not for one statement;
//   - single role: the application role IS the owner, nothing is revoked, and the engine can
//     still write its own history.
//
// The boot-time reconcile stays, and it is what covers databases created before this migration
// and ACL drift applied afterwards. It now runs BEFORE the rollout reads or opens any stream —
// see store.go — because a receipt written while the application role could forge events beside
// it attests nothing.
//
// The limit that remains is DECLARED rather than papered over: under a single role the gate is
// durable against a crash, but that role can fabricate events, and no code here can change that.

// guardTriggerSuffix is the name every engine-owned append-only guard carries. It is the
// same suffix CreateTableStmts and AuditTableStmts already use; naming it once means the
// manifest's expected trigger name and the DDL's rendered one cannot drift apart.
const guardTriggerSuffix = "_immutable"

// GuardMetadataACLStmts re-asserts the control plane's revoke, one statement per relation.
//
// One per relation rather than one server-side loop, for the same reason the append-only
// reconcile does it that way: a failure then names the relation it happened on, which is
// what an operator needs.
func (d postgresDialect) GuardMetadataACLStmts() []string {
	tables := GuardControlPlaneTables()
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		out = append(out, pgRevokeAllWrites(t, d.appRole))
	}
	return out
}

// GuardMetadataACLStmts is nil for SQLite: no roles, so no ACL to re-assert, and no
// TRUNCATE statement either. Its append-only defense is the trigger pair, which applies to
// every connection unconditionally.
func (sqliteDialect) GuardMetadataACLStmts() []string { return nil }

// pgRevokeAllWrites revokes INSERT as well as UPDATE, DELETE and TRUNCATE.
//
// It is pgRevokeMutations plus INSERT, and the extra privilege is the whole difference
// between an evidence table the runtime appends to and a control plane the runtime has no
// business writing at all. The rendering discipline is identical and for the same reasons:
// the role name is RUNTIME data, so it crosses into SQL exactly once as a dollar-quoted
// literal whose tag is chosen against the value, and the identifier is quoted SERVER-side
// by format('%I'). The pg_roles existence gate stays: REVOKE naming a role that does not
// exist is a hard ERROR, which would abort the migration if the role were dropped between
// introspection and execution.
func pgRevokeAllWrites(table, role string) string {
	body := fmt.Sprintf(`
DECLARE target text := %s;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = target) THEN
    EXECUTE pg_catalog.format('REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON %%I FROM %%I', %s, target);
  END IF;
END `, pgDollarQuote(role), pgDollarQuote(table))
	tag := pgDollarTagNotIn(body)
	return "DO " + tag + body + tag
}

// GuardControlPlaneStmts renders the SQLite control plane.
//
// It exists because buildCoreMigrations runs on BOTH engines: a core migration carrying
// PostgreSQL SQL would break every SQLite boot. So this is a real implementation, not a
// stub — the same three logs, the same constraints expressed in SQLite's vocabulary, and
// SQLite's own append-only trigger pair.
//
// What it deliberately does NOT carry, and why that is honest rather than a gap:
//
//   - No REVOKE. SQLite has no role layer, and it has no TRUNCATE statement either — the
//     one operation the PostgreSQL ACL leg exists to stop.
//   - No pg_catalog, no BYTEA, no DO block. A regression pins that: the SQLite statements
//     must contain none of those tokens.
//   - No ENABLE ALWAYS. SQLite triggers have no enable state; the trigger pair applies to
//     every connection unconditionally, which is the property 'A' buys on PostgreSQL.
//
// The engine's guard RUNNER remains PostgreSQL-only. This migration exists so that a
// SQLite deployment has a coherent, tracked v6 rather than a version hole, and so that the
// same Go bootstrap code can write the same rows on either engine.
func (sqliteDialect) GuardControlPlaneStmts() []string {
	out := []string{
		`CREATE TABLE ` + GuardGateEventsTable + ` (
  event_sha256 BLOB PRIMARY KEY CHECK (length(event_sha256) = 32),
  rollout_id TEXT NOT NULL,
  event_ordinal INTEGER NOT NULL CHECK (event_ordinal > 0),
  prev_event_sha256 BLOB,
  kind TEXT NOT NULL CHECK (kind IN ('pending-opened','attempt-started','attempt-judged','attempt-failed','verification-failed','reconciled','ready')),
  unit_id TEXT,
  attempt_id TEXT,
  intent TEXT,
  relation_schema TEXT,
  relation_name TEXT,
  trigger_name TEXT,
  manifest_format INTEGER NOT NULL CHECK (manifest_format > 0),
  code_epoch INTEGER NOT NULL CHECK (code_epoch > 0),
  code_sha256 BLOB NOT NULL CHECK (length(code_sha256) = 32),
  retained_revision INTEGER NOT NULL CHECK (retained_revision >= 0),
  retained_sha256 BLOB NOT NULL CHECK (length(retained_sha256) = 32),
  spec_sha256 BLOB CHECK (spec_sha256 IS NULL OR length(spec_sha256) = 32),
  definition_sha256 BLOB CHECK (definition_sha256 IS NULL OR length(definition_sha256) = 32),
  prestate_sha256 BLOB CHECK (prestate_sha256 IS NULL OR length(prestate_sha256) = 32),
  -- See the PostgreSQL dialect: the judged prestate is stored FIELD BY FIELD so it can be
  -- reconstructed and re-hashed, not merely displayed.
  prestate_target_exists INTEGER CHECK (prestate_target_exists IS NULL OR prestate_target_exists IN (0,1)),
  prestate_guard_present INTEGER CHECK (prestate_guard_present IS NULL OR prestate_guard_present IN (0,1)),
  prestate_guard_state TEXT,
  prestate_guard_canonical INTEGER CHECK (prestate_guard_canonical IS NULL OR prestate_guard_canonical IN (0,1)),
  prestate_receipt_present INTEGER CHECK (prestate_receipt_present IS NULL OR prestate_receipt_present IN (0,1)),
  prestate_bytes TEXT,
  phase TEXT NOT NULL CHECK (phase IN ('pending','ready')),
  gate_condition TEXT NOT NULL CHECK (gate_condition IN ('clean','retryable','blocked','verified')),
  diagnostic_code TEXT NOT NULL,
  retry_class TEXT NOT NULL,
  unblock_policy TEXT NOT NULL CHECK (unblock_policy IN ('','read_reconcile','operator')),
  sqlstate TEXT NOT NULL,
  expected_sha TEXT NOT NULL,
  observed_sha TEXT NOT NULL,
  diagnostic_fingerprint BLOB CHECK (diagnostic_fingerprint IS NULL OR length(diagnostic_fingerprint) = 32),
  details TEXT NOT NULL,
  expected_units TEXT NOT NULL,
  -- See the PostgreSQL dialect: a ready event records the head and count of the other two logs,
  -- so a removed tail has no successor to hide behind.
  checkpoint_inventory_sha256 BLOB
    CHECK (checkpoint_inventory_sha256 IS NULL OR length(checkpoint_inventory_sha256) = 32),
  checkpoint_inventory_count INTEGER CHECK (checkpoint_inventory_count IS NULL OR checkpoint_inventory_count >= 0),
  checkpoint_receipt_sha256 BLOB
    CHECK (checkpoint_receipt_sha256 IS NULL OR length(checkpoint_receipt_sha256) = 32),
  checkpoint_receipt_count INTEGER CHECK (checkpoint_receipt_count IS NULL OR checkpoint_receipt_count >= 0),
  build_id TEXT NOT NULL,
  actor TEXT NOT NULL,
  recorded_at TEXT NOT NULL,
  CHECK (
    (event_ordinal = 1 AND prev_event_sha256 IS NULL)
    OR
    (event_ordinal > 1 AND length(prev_event_sha256) = 32)
  ),
  CHECK (
    (kind = 'ready'
      AND checkpoint_inventory_sha256 IS NOT NULL AND checkpoint_inventory_count IS NOT NULL
      AND checkpoint_receipt_sha256 IS NOT NULL AND checkpoint_receipt_count IS NOT NULL)
    OR
    (kind <> 'ready'
      AND checkpoint_inventory_sha256 IS NULL AND checkpoint_inventory_count IS NULL
      AND checkpoint_receipt_sha256 IS NULL AND checkpoint_receipt_count IS NULL)
  ),
  CHECK (
    (kind IN ('pending-opened','ready') AND unit_id IS NULL)
    OR
    (kind NOT IN ('pending-opened','ready') AND unit_id IS NOT NULL)
  ),
  UNIQUE (rollout_id, event_ordinal),
  UNIQUE (rollout_id, unit_id, diagnostic_fingerprint)
)`,
		`CREATE INDEX ` + GuardGateEventsTable + `_unit_idx
    ON ` + GuardGateEventsTable + ` (rollout_id, unit_id, event_ordinal)`,

		`CREATE TABLE ` + GuardInventoryEventsTable + ` (
  event_sha256 BLOB PRIMARY KEY CHECK (length(event_sha256) = 32),
  event_ordinal INTEGER NOT NULL UNIQUE CHECK (event_ordinal > 0),
  prev_event_sha256 BLOB,
  kind TEXT NOT NULL CHECK (kind IN ('activate','retain','reactivate','tombstone')),
  relation_schema TEXT NOT NULL,
  relation_name TEXT NOT NULL,
  trigger_name TEXT NOT NULL,
  producer TEXT NOT NULL,
  manifest_format INTEGER NOT NULL CHECK (manifest_format > 0),
  code_epoch INTEGER NOT NULL CHECK (code_epoch > 0),
  definition_sha256 BLOB NOT NULL CHECK (length(definition_sha256) = 32),
  spec_sha256 BLOB NOT NULL CHECK (length(spec_sha256) = 32),
  desired_enable_state TEXT NOT NULL CHECK (
    length(desired_enable_state) = 1
    AND desired_enable_state IN ('O','A')
  ),
  legacy_allowed_states TEXT NOT NULL,
  retained_revision INTEGER NOT NULL CHECK (retained_revision >= 0),
  retained_sha256 BLOB NOT NULL CHECK (length(retained_sha256) = 32),
  recorded_at TEXT NOT NULL,
  CHECK (
    (event_ordinal = 1 AND prev_event_sha256 IS NULL)
    OR
    (event_ordinal > 1 AND length(prev_event_sha256) = 32)
  )
)`,
		`CREATE INDEX ` + GuardInventoryEventsTable + `_entry_idx
    ON ` + GuardInventoryEventsTable + ` (relation_schema, relation_name, trigger_name, event_ordinal)`,

		`CREATE TABLE ` + GuardReceiptsTable + ` (
  receipt_id BLOB PRIMARY KEY CHECK (length(receipt_id) = 32),
  rollout_id TEXT NOT NULL,
  unit_id TEXT NOT NULL,
  receipt_kind TEXT NOT NULL CHECK (receipt_kind IN ('bootstrap','unit')),
  intent TEXT NOT NULL,
  relation_schema TEXT NOT NULL,
  relation_name TEXT NOT NULL,
  trigger_name TEXT NOT NULL,
  epoch INTEGER NOT NULL CHECK (epoch > 0),
  manifest_format INTEGER NOT NULL CHECK (manifest_format > 0),
  code_sha256 BLOB NOT NULL CHECK (length(code_sha256) = 32),
  retained_revision INTEGER NOT NULL CHECK (retained_revision >= 0),
  retained_sha256 BLOB NOT NULL CHECK (length(retained_sha256) = 32),
  spec_sha256 BLOB NOT NULL CHECK (length(spec_sha256) = 32),
  definition_sha256 BLOB NOT NULL CHECK (length(definition_sha256) = 32),
  prestate_sha256 BLOB NOT NULL CHECK (length(prestate_sha256) = 32),
  from_enable_state TEXT,
  to_enable_state TEXT NOT NULL,
  predecessor_receipt_id BLOB,
  attempt_id TEXT NOT NULL,
  event_ordinal INTEGER NOT NULL CHECK (event_ordinal > 0),
  prev_event_sha256 BLOB,
  event_sha256 BLOB NOT NULL CHECK (length(event_sha256) = 32),
  applied_at TEXT NOT NULL,
  CHECK (
    (receipt_kind = 'bootstrap' AND intent = 'bootstrap')
    OR
    (receipt_kind = 'unit' AND intent IN (
      'create-guard',
      'adopt-legacy',
      'transition-legacy-o-to-a',
      'repair'
    ))
  ),
  CHECK (
    from_enable_state IS NULL
    OR (
      length(from_enable_state) = 1
      AND from_enable_state IN ('O','A','D','R')
    )
  ),
  CHECK (
    length(to_enable_state) = 1
    AND to_enable_state IN ('O','A')
  ),
  CHECK (
    predecessor_receipt_id IS NULL
    OR length(predecessor_receipt_id) = 32
  ),
  CHECK (
    (event_ordinal = 1 AND prev_event_sha256 IS NULL)
    OR
    (event_ordinal > 1 AND length(prev_event_sha256) = 32)
  ),
  UNIQUE (rollout_id, unit_id, receipt_kind),
  UNIQUE (rollout_id, event_ordinal)
)`,
		`CREATE INDEX ` + GuardReceiptsTable + `_target_idx
    ON ` + GuardReceiptsTable + `
       (rollout_id, relation_schema, relation_name, trigger_name, receipt_kind)`,
	}
	for _, t := range GuardControlPlaneTables() {
		out = append(out, sqliteAppendOnlyTriggers(t)...)
	}
	return out
}
