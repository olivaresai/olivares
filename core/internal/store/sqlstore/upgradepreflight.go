// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// THE UPGRADE PRIVILEGE PREFLIGHT (HC-R1).
//
// The failure this removes is causal and was measured, not hypothetical: on a
// PostgreSQL deployment with a separate owner role, an upgrade applied schema the
// APPLICATION role then could not use. The migration advanced durably — the core
// tracking table moved, the relations were physically created — and only afterwards
// did the deployment fail with 42501. Rolling the binary back does not undo a
// tracking row, so the estate was left in a state no side of the upgrade could serve.
//
// The remedy is to ask the question BEFORE the first durable change, inside the
// migration advisory lock, and to ask it about EFFECTIVE privileges rather than about
// how they were provisioned. Two readings, and they are genuinely different questions:
//
//  1. RELATIONS THAT ALREADY EXIST. Measure the app role's effective privileges on
//     their OIDs. A grant held directly, through a group role, or through PUBLIC all
//     count, because all three work at runtime.
//
//  2. RELATIONS THIS BOOT STILL HAS TO CREATE. Their privileges cannot be measured —
//     the objects do not exist. So the owner session creates ONE ordinary probe table
//     inside a transaction that is ALWAYS rolled back, and measures the app role's
//     effective privileges on that fresh OID. What the probe answers is the only
//     question that matters: "when this owner creates a table in this schema, what
//     will the application role be able to do with it?"
//
// pg_default_acl is deliberately NEITHER consulted NOR required. It is a PROVISIONING
// MECHANISM, not the contract: a deployment whose schema is already complete and whose
// grants were applied by hand has an empty pg_default_acl and is perfectly correct, and
// a check that demanded a default-ACL row would refuse it. This is measured behavior —
// the viability review observed the probe return true through nothing but an inherited
// group membership, with no direct grant and no pg_default_acl row naming the app role.
//
// WHAT THIS DOES NOT PROMISE. The advisory lock coordinates Olivares nodes; it does not
// stop a DBA from changing an ACL a millisecond after the preflight passes. The
// pre-serve verifications still refuse to serve in that race, and durable advance is
// possible inside it. HC-R1 removes a STABLE causal failure; it does not serialize
// against a concurrent administrator, and saying otherwise would be a promise the
// advisory lock cannot keep.

// upgradePreflightRefusal wraps every refusal this preflight can produce in the one
// public sentinel.
//
// ONE sentinel for schema access, owner authority, an existing relation, the probe and
// a failed rollback is deliberate: to a caller they all mean "the upgrade was refused
// and nothing was changed". A hierarchy would invite matching on some and letting the
// rest through, and "the preflight could not finish" is never a permit.
func upgradePreflightRefusal(format string, args ...any) error {
	return fmt.Errorf("sqlstore: %w: %s", store.ErrPostgresUpgradePrivilegePreflight,
		fmt.Sprintf(format, args...))
}

// upgradePreflightCause is upgradePreflightRefusal for a refusal that HAS an underlying
// error, and it keeps that error in the chain instead of flattening it into text.
//
// The difference is not cosmetic. A refusal caused by a dead connection, a permission
// error and a canceled context all read alike once the cause has been rendered with %v,
// and the one place it matters most is the probe's rollback: "the transaction was already
// finished" and "the server went away" call for completely different operator responses.
// Wrapping keeps errors.Is/errors.As usable on the cause while the public sentinel stays
// the thing callers match on.
func upgradePreflightCause(cause error, format string, args ...any) error {
	return fmt.Errorf("sqlstore: %w: %s: %w", store.ErrPostgresUpgradePrivilegePreflight,
		fmt.Sprintf(format, args...), cause)
}

// upgradePreflightConfig is the CONFIGURATION half of the union: the facts about THIS
// boot that decide whether a conditional relation is consumed at all.
//
// It exists so the preflight cannot reach for the whole store.Config and start deriving
// requirements from settings this boot will not act on. An option that is off requires
// no privilege — demanding one would refuse a correctly-provisioned deployment for a
// capability it never enabled — and a later configuration change re-runs this preflight
// before that node serves, because it runs on every Open.
type upgradePreflightConfig struct {
	// spoolBudgeted is AuditSpoolMaxBytes > 0: the counter is read and written.
	spoolBudgeted bool
	// spoolDegrade is the EFFECTIVE on-full mode, with the empty value already
	// resolved to block (the writer's own rule: anything but an explicit degrade is
	// block).
	spoolDegrade bool
	// blinding is the configured metadata-commitment write rule, unresolved: whether
	// it will actuate also depends on the ledger's own durable record, which this
	// preflight reads from the owner session.
	blinding store.AuditBlindingMode
}

func upgradePreflightConfigOf(cfg store.Config) upgradePreflightConfig {
	mode := cfg.AuditSpoolOnFull
	if mode != store.AuditSpoolDegrade {
		mode = store.AuditSpoolBlock
	}
	return upgradePreflightConfig{
		spoolBudgeted: cfg.AuditSpoolMaxBytes > 0,
		spoolDegrade:  mode == store.AuditSpoolDegrade,
		blinding:      cfg.AuditMetaBlinding,
	}
}

// appRelationNeed is one relation and the effective privileges the APPLICATION role
// must hold on it for this binary to serve.
type appRelationNeed struct {
	table string
	// privileges is the exact, ordered set. It is never rendered into a single
	// has_table_privilege string: PostgreSQL accepts "SELECT,INSERT" and answers about
	// the CONJUNCTION, so one false collapses into an answer that cannot say which.
	privileges []string
	// provisionedByReconciler marks a relation whose own DDL or per-boot reconciler
	// GRANTS the app exactly what it needs (lineage metadata, the directory writer
	// control). Such a relation is still MEASURED when it already exists — drift is
	// real — but it is deliberately excluded from the FUTURE union, because requiring
	// the operator to pre-grant an access the engine is about to grant itself would
	// refuse a correct deployment.
	provisionedByReconciler bool
	// origin names where the requirement comes from, so a refusal says WHY.
	origin string
	// appendOnly marks a relation the engine guards as append-only. Like boundary it
	// changes only the sentinel a refusal carries: verifyAppendOnlyACL reports a missing
	// SELECT/INSERT there as store.ErrAppendOnlyGrantMissing, and a role that cannot
	// read or append evidence would discover it at the first attempt to record some.
	appendOnly bool
	// boundary marks a module SECURITY-BOUNDARY fact table (SchemaInvariants). It
	// changes nothing about what is required — the privileges are the append-only
	// SELECT/INSERT either way — and only about which sentinel a refusal carries.
	//
	// runSchemaInvariantSelfTest has always reported this exact condition as
	// store.ErrSchemaBoundaryGrantMissing, and callers match on it. This preflight does
	// not change what is wrong with the deployment, only WHEN it is noticed: now before
	// the first durable change instead of at the pre-serve self-test. A refusal that
	// dropped the established sentinel would silently answer "no" to every caller
	// asking whether the trigger boundary is half-installed.
	boundary bool
}

// upgradePreflightNeeds builds the closed inventory of application-role needs.
//
// The identity comes from the CLOSED REGISTRY and from constants that are already
// authoritative — never from buildCoreMigrations (a deliberate historical v1–v8
// prefix), never from a version maximum, and never from whatever happens to exist in
// the database. Deriving it from the database is the mistake that makes a preflight
// agree with any estate it is pointed at.
//
// Relations that are OWNER-ONLY generate no requirement at all, and listing them here
// with an empty set would be a lie in the other direction: the tracking tables, the
// rollout CLASSIFICATION receipt, the append-only scope inventory and the three guard
// control-plane relations are written by the owner under this lock, and the split
// posture deliberately DENIES the app role INSERT on the last three.
func upgradePreflightNeeds(reg *registry, cfg upgradePreflightConfig, blindingWillActuate bool) []appRelationNeed {
	const (
		sel = "SELECT"
		ins = "INSERT"
		upd = "UPDATE"
		del = "DELETE"
	)
	byTable := map[string]*appRelationNeed{}
	add := func(origin string, provisioned bool, tables []string, privileges ...string) {
		for _, table := range tables {
			need, ok := byTable[table]
			if !ok {
				need = &appRelationNeed{table: table, origin: origin, provisionedByReconciler: provisioned}
				byTable[table] = need
			}
			for _, privilege := range privileges {
				if !slices.Contains(need.privileges, privilege) {
					need.privileges = append(need.privileges, privilege)
				}
			}
		}
	}

	// Mutable descriptors, core and module, plus audit_heads. This is the class the
	// existing readiness gate already demands full DML of, and core v10's
	// core_user_authority is a member of it: H is mutable and pinned to the SYSTEM
	// tenant, and "tenantless" describes its AUTHORITY, not a missing tenant_id column.
	//
	// DELETE is required for H even though H has no delete API, because DELETE is what
	// checkAppTablePrivileges(mutableTenantTables) demands today. Narrowing it to
	// SELECT/INSERT/UPDATE would have to move the final readiness gate in the same
	// change, with its own coverage; doing it here alone would make the preflight and
	// the gate disagree about the same role.
	add("mutable descriptor (registry.mutableTenantTables)", false,
		reg.mutableTenantTables(), sel, ins, upd, del)

	// Append-only descriptors and audit_events. The engine REMOVES UPDATE/DELETE/
	// TRUNCATE here and that negative stays exactly where it is (reconcileAppendOnlyACL
	// and verifyAppendOnlyACL); what this adds is the positive half, which is what an
	// upgrade can leave missing.
	appendOnlyTables := reg.appendOnlyTables()
	add("append-only descriptor (registry.appendOnlyTables)", false,
		appendOnlyTables, sel, ins)
	for _, table := range appendOnlyTables {
		if need, ok := byTable[table]; ok {
			need.appendOnly = true
		}
	}
	// Module security-boundary fact tables are a SUBSET of the above, so this merges
	// into the same entries rather than stating a second, contradictory requirement.
	// It is merged for the PRIVILEGES and marked for the SENTINEL.
	boundaryTables := reg.invariantBoundaryTables(store.EnginePostgres)
	add("module invariant boundary (registry.invariantBoundaryTables)", false,
		boundaryTables, sel, ins)
	for _, table := range boundaryTables {
		if need, ok := byTable[table]; ok {
			need.boundary = true
		}
	}

	// Lineage: the epoch relations and the two control tables are read by the runtime
	// and their SELECT is GRANTED by reconcileLineageACL in the hardened posture. The
	// writer/touched protocol tables get no direct privilege at all — the app reaches
	// them only through routines that receive an explicit EXECUTE grant, which is a
	// function ACL and not inferred from any table default.
	lineageReadable := []string{lineageControlTable, lineageSeededTable}
	for _, relation := range lineageRelations {
		lineageReadable = append(lineageReadable, relation.descriptor().Table)
	}
	add("lineage metadata (reconcileLineageACL grants SELECT)", true, lineageReadable, sel)

	// The directory writer control: SELECT, granted and then VERIFIED non-owner
	// SELECT-only by reconcileDirectoryWriterGuards.
	add("directory writer control (its reconciler grants SELECT)", true,
		[]string{dialect.DirectoryWriterControlTable}, sel)

	// The rollout control plane's RUNTIME pair, and this is the easy one to get wrong.
	// classifyRolloutControls creates all three relations as the owner and grants
	// nothing; SetRolloutMode then operates on state and transitions through s.db —
	// the APPLICATION pool — reading and CAS-ing the state row and appending a
	// transition. The CLASSIFICATION receipt is owner-only and correctly absent here.
	add("rollout state (store.SetRolloutMode CAS via the app pool)", false,
		[]string{dialect.ControlRolloutStateTable}, sel, upd)
	add("rollout transitions (store.SetRolloutMode append via the app pool)", false,
		[]string{dialect.ControlRolloutTransitionTable}, sel, ins)

	// The audit spool counter, only when a budget makes it run at all.
	if cfg.spoolBudgeted {
		add("audit spool budget (AuditSpoolMaxBytes > 0)", false,
			[]string{dialect.AuditSpoolUsageTable}, sel, upd)
	}

	// THE DEGRADE EPISODE TABLE, AND THE DISTINCTION THAT DECIDES IT: the configuration
	// that CREATES an episode is not the configuration that CONSUMES one.
	//
	// SELECT and DELETE are UNCONDITIONAL. auditLog.Append reads the tenant's pending
	// episode on every single append, before it looks at any budget; and if an episode
	// is pending, ANY unsigned append seals it as a signed marker and clears its mutable
	// row in the same transaction. Both of those live OUTSIDE the budget block — with
	// AuditSpoolMaxBytes == 0 that is the direct path, and under `block` an exempt
	// append reaches it too. An episode written by a boot that ran budget + degrade
	// therefore has to be recoverable by the NEXT boot, which may legitimately run with
	// the budget switched off. Requiring only SELECT there accepts a role that opens the
	// service and then fails 42501 on the first append that tries to seal what an
	// earlier boot left behind.
	//
	// The earlier reading of this branch folded DELETE into the creating configuration
	// and was WRONG for exactly that reason. The residual it named — "a live pending row
	// under a budget since turned off" — is not a rare corner, it is the ordinary
	// consequence of restarting a node after a degrade episode.
	//
	// IT IS NOT MEASURED, AND THAT IS THE POINT. Deciding whether a row exists is a
	// GLOBAL question about a table under FORCE ROW LEVEL SECURITY whose policy calls
	// current_setting('app.tenant_id') without missing_ok. The owner this product
	// accepts is NOSUPERUSER NOBYPASSRLS, so FORCE binds it too: an unbound session
	// raises rather than answering, and binding one tenant answers only for that tenant.
	// The product's one global reader, AuditSpoolStatus, uses the BYPASSRLS admin pool
	// for precisely this query. This preflight must return identical verdicts with NO
	// AdminDSN, and it will not weaken a guard to interrogate it — no admin pool, no
	// temporary NO FORCE, no inference from an RLS exception. So the capability is
	// granted unconditionally instead of censused: DELETE covers the SUPPORTED RECOVERY
	// of persisted state, which is not the same thing as enabling the option that
	// creates losses.
	//
	// AND THE FUTURE PROBE CARRIES THE SAME OBLIGATION, which is why this single `add`
	// serves both readings. Asking for SELECT-only while the relation is absent and
	// SELECT+DELETE once it exists would make the schema phase itself invalidate a
	// posture it had just accepted: first boot passes, the migration creates the table,
	// and the very next boot — same binary, same configuration, same grants, same data —
	// refuses. A contract whose verdict flips because the relation it describes came
	// into existence is not a contract.
	add("audit degrade episode recovery (auditLog.Append seals and clears a persisted episode in every mode)",
		false, []string{dialect.AuditSpoolGapsTable}, sel, del)
	// INSERT and UPDATE stay conditional. recordDrop is the only writer that CREATES or
	// EXTENDS episode state, and it is reachable only over budget with the degrade
	// policy selected. A deployment running the default block policy must not be asked
	// for the capability to record losses it has deliberately not enabled.
	if cfg.spoolBudgeted && cfg.spoolDegrade {
		add("audit degrade episode creation (budget + on_full=degrade)", false,
			[]string{dialect.AuditSpoolGapsTable}, ins, upd)
	}

	// The metadata-blinding record. SELECT covers both the "off" refusal path and the
	// default mode's read; UPDATE is added only when THIS boot's mode, against this
	// ledger's own durable default, is actually going to record an actuation.
	add("audit blinding rule (resolveBlindingMode)", false,
		[]string{dialect.AuditBlindingStateTable}, sel)
	if blindingWillActuate {
		add("audit blinding actuation (recordBlindingActuation)", false,
			[]string{dialect.AuditBlindingStateTable}, upd)
	}

	// The fencing epoch. It is cluster infrastructure with no descriptor, and the
	// elector reads, seeds and bumps it from its own pool authenticating as the app
	// role.
	add("leader election fencing epoch", false, []string{leaderEpochTable}, sel, ins, upd)

	out := make([]appRelationNeed, 0, len(byTable))
	for _, need := range byTable {
		sortPrivileges(need.privileges)
		out = append(out, *need)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].table < out[j].table })
	return out
}

// privilegeOrder pins a stable, human-readable order for every message this file
// renders. Sorting alphabetically would print "DELETE, INSERT, SELECT, UPDATE", which
// reads as an arbitrary list rather than as the familiar DML sequence.
var privilegeOrder = map[string]int{"SELECT": 0, "INSERT": 1, "UPDATE": 2, "DELETE": 3}

func sortPrivileges(privileges []string) {
	sort.Slice(privileges, func(i, j int) bool {
		return privilegeOrder[privileges[i]] < privilegeOrder[privileges[j]]
	})
}

// preflightPostgresUpgradePrivileges is the whole check. It runs on the connection
// that holds the migration advisory lock, as the OWNER, immediately after the
// access-evidence start class is fixed and immediately before classifyRolloutControls
// — which is the first thing in this callback that creates a relation.
//
// Everything it does is a catalog read, except the probe, which is DDL inside a
// transaction that is always rolled back. On a refusal the database is byte-identical
// to how this boot found it: no tracking row, no relation, no receipt.
func preflightPostgresUpgradePrivileges(
	ctx context.Context,
	owner dialect.Execer,
	dia dialect.Dialect,
	roles guardRoles,
	reg *registry,
	modulePlans []moduleFileMigrationPlan,
	purpose preparePurpose,
	cfg upgradePreflightConfig,
) error {
	if dia.Name() != store.EnginePostgres {
		return nil
	}
	switch guardMetadataTopologyOf(roles) {
	case guardTopologySingleRole:
		// The application role IS the owner, by configuration or because both DSNs
		// authenticate as one role. It owns every table this plan creates, so there is
		// no future grant to be missing and no separate subject to ask about. The
		// append-only and guard-plane verifications still run, unchanged.
		return nil
	case guardTopologyUnknown:
		// An OwnerDSN is configured and this boot could not read one of the two roles.
		// The whole question is "what will THAT role be able to do", so an unread role
		// leaves it unanswerable — and an unanswerable privilege question is a refusal,
		// never a pass. Refusing HERE rather than after v1–v8 have already applied is
		// the entire point of this preflight's position.
		return upgradePreflightRefusal(
			"this deployment configures a separate owner role (--owner-dsn) and this boot could not resolve %s, so the effective privileges of the application role on the schema this upgrade is about to apply cannot be measured at all. Grant SELECT on pg_roles to both roles (only the RLS attributes need it; the identity does not), or remove --owner-dsn to declare the single-role topology deliberately",
			describeUnresolvedGuardRoles(roles))
	case guardTopologySplit:
	}
	appRole, ownerRole := roles.App.Role, roles.Owner.Role

	// (1) SCHEMA REACHABILITY, for both roles, before anything about tables.
	//
	// Table privileges are meaningless without USAGE: PostgreSQL refuses every query
	// against a schema the role may not use, so a check that skipped this would report
	// a table readable and then fail on the first row. And the owner's CREATE is the
	// precondition of the probe itself — without it the probe's failure would be about
	// the owner, not about the app role it is trying to ask about.
	var appUsage, ownerUsage, ownerCreate bool
	if err := owner.QueryRowContext(ctx, `SELECT
  pg_catalog.has_schema_privilege($1, $3, 'USAGE'),
  pg_catalog.has_schema_privilege($2, $3, 'USAGE'),
  pg_catalog.has_schema_privilege($2, $3, 'CREATE')`,
		appRole, ownerRole, dialect.EngineSchema).Scan(&appUsage, &ownerUsage, &ownerCreate); err != nil {
		return upgradePreflightCause(err, "read effective schema privileges on %q", dialect.EngineSchema)
	}
	if !appUsage {
		// TWO sentinels, and the second one is not decoration. checkSchemaAccess has
		// always reported this exact condition as store.ErrEngineSchemaUnusable, and
		// callers — including this package's own regression coverage — match on it. This
		// preflight does not change what is wrong with the deployment, only WHEN it is
		// noticed: now before the first durable change instead of after. Replacing the
		// established sentinel with the new one would silently break every caller that
		// asks "is the engine schema unusable?", so the refusal answers both questions.
		return fmt.Errorf(
			"sqlstore: %w: %w — the application role %q has no effective USAGE on schema %q, so every query against the engine's tables fails regardless of their table privileges. Grant it (GRANT USAGE ON SCHEMA %s TO %s) or re-run `olivares db init`",
			store.ErrPostgresUpgradePrivilegePreflight, store.ErrEngineSchemaUnusable,
			appRole, dialect.EngineSchema, dialect.EngineSchema, appRole)
	}
	if !ownerUsage || !ownerCreate {
		return upgradePreflightRefusal(
			"the owner role %q lacks effective USAGE/CREATE on schema %q (usage=%t create=%t), so it cannot apply this upgrade's schema at all",
			ownerRole, dialect.EngineSchema, ownerUsage, ownerCreate)
	}

	// (2) THE CONDITIONAL BLINDING BIT, resolved against the ledger's own record.
	blindingWillActuate, err := upgradeBlindingWillActuate(ctx, owner, dia, cfg.blinding)
	if err != nil {
		return err
	}
	needs := upgradePreflightNeeds(reg, cfg, blindingWillActuate)

	// (3) WHICH OF THEM EXIST. The split is explicit and comes from the catalog, never
	// from "expected minus present": the plan separates a relation that is THERE from
	// one this boot still has to CREATE, and conflating them is what would let an
	// absent object be read as a permission.
	tables := make([]string, 0, len(needs))
	for _, need := range needs {
		tables = append(tables, need.table)
	}
	oids, err := upgradePreflightRelationOIDs(ctx, owner, tables)
	if err != nil {
		return upgradePreflightCause(err, "resolve the engine's relations in schema %q", dialect.EngineSchema)
	}

	// (4) OWNER AUTHORITY over every managed relation that already exists.
	//
	// An ACL of ALL is NOT authority to ALTER: only the table's owner — or a role that
	// is an effective member of the owning role — may alter or administer it. The
	// inventory is the binary's own declaration (buildManagedObjectSet), so this asks
	// about the objects this plan may touch rather than about whatever else lives in
	// the schema.
	if err := upgradePreflightOwnerAuthority(ctx, owner, dia, reg, modulePlans, ownerRole); err != nil {
		return err
	}

	// (5) EXISTING RELATIONS: one query per privilege, on the OID.
	//
	// Per privilege, because has_table_privilege accepts a comma-joined string and
	// answers about the CONJUNCTION — one false collapses four answers into one that
	// cannot name which. On the OID, because the name was resolved once, here, and a
	// second name lookup inside the privilege call would re-open the search_path
	// question this engine closes everywhere else.
	for _, need := range needs {
		oid, exists := oids[need.table]
		if !exists {
			continue
		}
		missing, err := upgradeMissingPrivileges(ctx, owner, appRole, oid, need.privileges)
		if err != nil {
			return upgradePreflightCause(err,
				"read the application role's effective privileges on existing relation %q (OID %d)",
				need.table, oid)
		}
		if len(missing) > 0 {
			// TWO sentinels wherever an established one already names this condition:
			// the new one says the upgrade was refused, the established one says WHAT is
			// wrong. Boundary is checked FIRST because it is the narrower classification
			// and it is the one runSchemaInvariantSelfTest reaches first today — a
			// boundary table is also an append-only one, and answering with the wider
			// sentinel would tell a caller less than the code already knows.
			if need.boundary {
				return fmt.Errorf(
					"sqlstore: %w: %w — the application role %q lacks %s on the EXISTING security-boundary relation %q (OID %d), required by: %s. The guard would fire and the application could not write the fact it guards. Nothing has been migrated: this boot refused before its first durable change",
					store.ErrPostgresUpgradePrivilegePreflight, store.ErrSchemaBoundaryGrantMissing,
					appRole, strings.Join(missing, ", "), need.table, oid, need.origin)
			}
			if need.appendOnly {
				return fmt.Errorf(
					"sqlstore: %w: %w — the application role %q lacks %s on the EXISTING append-only relation %q (OID %d), required by: %s. The engine removes only UPDATE/DELETE/TRUNCATE there, so a role that cannot read or append is misprovisioned and would fail at the first attempt to record evidence. Nothing has been migrated: this boot refused before its first durable change",
					store.ErrPostgresUpgradePrivilegePreflight, store.ErrAppendOnlyGrantMissing,
					appRole, strings.Join(missing, ", "), need.table, oid, need.origin)
			}
			return upgradePreflightRefusal(
				"the application role %q lacks %s on the EXISTING relation %q (OID %d), required by: %s. Nothing has been migrated: this boot refused before its first durable change. Grant the missing privileges (a direct grant, a group role or PUBLIC all count) and start again",
				appRole, strings.Join(missing, ", "), need.table, oid, need.origin)
		}
	}

	// (6) THE FUTURE UNION, and only what this plan is really going to create.
	//
	// A relation whose own reconciler grants the access is excluded: requiring the
	// operator to pre-grant something the engine grants itself would refuse a correct
	// deployment. What is left is the set whose access can only come from outside the
	// engine — descriptor tables, the rollout runtime pair, the spool/blinding
	// bookkeeping and the fencing epoch.
	future := map[string]bool{}
	var futureTables []string
	for _, need := range needs {
		if _, exists := oids[need.table]; exists || need.provisionedByReconciler {
			continue
		}
		futureTables = append(futureTables, need.table)
		for _, privilege := range need.privileges {
			future[privilege] = true
		}
	}
	if len(future) == 0 {
		// Either the schema is already complete, or everything still missing is
		// provisioned by its own reconciler. A deployment whose grants were applied BY
		// HAND lands here, and it must pass: pg_default_acl being empty is not a defect,
		// it is a provisioning style.
		return nil
	}
	if purpose == prepareSchemaOnly {
		// `migrate apply` is the operator SAYING that grants come after the schema. It
		// omits exactly this test — the one about objects that do not exist yet — and
		// nothing else: every check above still ran, and the pre-serve verifications
		// are untouched. The next `serve` finds these relations EXISTING and measures
		// them for real.
		return nil
	}
	required := make([]string, 0, len(future))
	for privilege := range future {
		required = append(required, privilege)
	}
	sortPrivileges(required)
	sort.Strings(futureTables)
	return upgradePreflightProbeFuture(ctx, owner, appRole, ownerRole, required, futureTables)
}

// upgradeBlindingWillActuate answers whether resolveBlindingMode is going to WRITE to
// the blinding record on this boot, which is the only thing that makes UPDATE on it a
// requirement rather than an inactive option.
//
// "on" always actuates. "off" never does. The default follows the ledger's own durable
// record, which is why this reads it instead of assuming: a ledger seeded
// default_blinded=0 (it was accumulating rows under the legacy rule when the column
// arrived) never actuates on the default, and demanding UPDATE from it would refuse a
// correct deployment.
//
// The record may not exist yet — it is created later in this same lock — so its future
// seed is derived exactly the way reconcileAuditLedger derives it: from whether the
// ledger already carries meta_blind. A ledger that does not exist at all is fresh, and
// a fresh one is seeded blinded.
func upgradeBlindingWillActuate(ctx context.Context, owner dialect.Execer, dia dialect.Dialect, mode store.AuditBlindingMode) (bool, error) {
	switch mode {
	case store.AuditBlindingOff:
		return false, nil
	case store.AuditBlindingOn:
		return true, nil
	case store.AuditBlindingAuto:
	default:
		// An unknown mode is refused by resolveBlindingMode later with a precise
		// message. Reporting "no actuation" here neither hides nor pre-empts that.
		return false, nil
	}
	state, err := dia.TableColumns(ctx, owner, dialect.AuditBlindingStateTable)
	if err != nil {
		return false, upgradePreflightCause(err, "read the shape of %q", dialect.AuditBlindingStateTable)
	}
	if len(state) > 0 {
		var defaultBlinded sql.NullInt64
		// #nosec G202 -- every fragment is an internal dialect constant (the engine's own
		// schema and table name, the latter quoted); no caller input reaches this string.
		if err := owner.QueryRowContext(ctx,
			"SELECT default_blinded FROM "+dialect.EngineSchema+"."+quoteIdent(dialect.AuditBlindingStateTable)+
				" WHERE id = 1").Scan(&defaultBlinded); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// The table exists and its seed has not been written yet, so the seed
				// below still decides. Fall through to the ledger-shape derivation.
				return upgradeBlindingSeedWillBeBlinded(ctx, owner, dia)
			}
			return false, upgradePreflightCause(err, "read %q", dialect.AuditBlindingStateTable)
		}
		return defaultBlinded.Valid && defaultBlinded.Int64 != 0, nil
	}
	return upgradeBlindingSeedWillBeBlinded(ctx, owner, dia)
}

// upgradeBlindingSeedWillBeBlinded reproduces reconcileAuditLedger's own rule for the
// seed it has not written yet: legacyLedger is "audit_events exists and lacks
// meta_blind", and a legacy ledger seeds default_blinded=0.
func upgradeBlindingSeedWillBeBlinded(ctx context.Context, owner dialect.Execer, dia dialect.Dialect) (bool, error) {
	ledger, err := dia.TableColumns(ctx, owner, dialect.AuditEventsTable)
	if err != nil {
		return false, upgradePreflightCause(err, "read the shape of %q", dialect.AuditEventsTable)
	}
	if len(ledger) == 0 {
		// No ledger at all: this boot creates it WITH the column, so it has never held a
		// row under another rule and is seeded blinded.
		return true, nil
	}
	return ledger["meta_blind"], nil
}

// upgradePreflightRelationOIDs resolves the ordinary/partitioned relations that exist,
// bound to the engine's schema by name rather than to whatever the search_path happens
// to resolve to.
func upgradePreflightRelationOIDs(ctx context.Context, owner dialect.Execer, tables []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(tables) == 0 {
		return out, nil
	}
	list, args := tableParams([]any{dialect.EngineSchema}, tables)
	// #nosec G202 -- `list` is tableParams' output: ONLY "$2,$3,…" placeholders. Every
	// table name travels as a bound argument and the schema as $1.
	q := `SELECT c.relname, c.oid::int8
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p') AND c.relname IN (` + list + `)`
	rows, err := owner.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // rows.Err() below carries any read failure
	for rows.Next() {
		var name string
		var oid int64
		if err := rows.Scan(&name, &oid); err != nil {
			return nil, err
		}
		out[name] = oid
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// upgradeMissingPrivileges asks ONE question per privilege, against a resolved OID.
//
// It takes the package's rowQuerier rather than a concrete type so the SAME function
// serves both readings: the lock-holding owner connection for relations that exist, and
// the probe's own transaction for the uncommitted relation only that transaction can
// see. Two copies differing solely in a receiver type is how the two readings would
// drift apart.
func upgradeMissingPrivileges(ctx context.Context, q rowQuerier, appRole string, oid int64, privileges []string) ([]string, error) {
	var missing []string
	for _, privilege := range privileges {
		var allowed bool
		// $2::oid is REQUIRED, not stylistic: has_table_privilege is overloaded, and
		// without the cast the driver resolves the numeric argument against the TEXT
		// overload and fails to encode it. Measured in the viability review, where the
		// first harness iteration died on exactly this.
		if err := q.QueryRowContext(ctx,
			`SELECT pg_catalog.has_table_privilege($1, $2::oid, $3)`, appRole, oid, privilege).
			Scan(&allowed); err != nil {
			return nil, err
		}
		if !allowed {
			missing = append(missing, privilege)
		}
	}
	return missing, nil
}

// upgradePreflightOwnerAuthority refuses when the owner role could not ALTER an object
// this plan may touch.
//
// pg_has_role(owner, relowner, 'USAGE') is the question, and it is NOT the same as the
// escalation closure guardmetadataacl.go computes: that one asks whether the APP role
// can reach the owner and defeat the split. This asks whether the OWNER may administer
// a relation it does not itself own — a perfectly ordinary posture when a group role
// owns the schema — and answering it with an ACL check would call a deployment
// migratable when its very first ALTER will fail.
func upgradePreflightOwnerAuthority(
	ctx context.Context,
	owner dialect.Execer,
	dia dialect.Dialect,
	reg *registry,
	modulePlans []moduleFileMigrationPlan,
	ownerRole string,
) error {
	set, err := buildManagedObjectSet(dia, coreDescriptors(), reg, modulePlans)
	if err != nil {
		return upgradePreflightCause(err, "build this build's managed object inventory")
	}
	managed := set.byClass[managedClassRelation]
	if len(managed) == 0 {
		return nil
	}
	// Which of them carry the append-only boundary. It changes nothing about the
	// question — the owner either may administer the relation or may not — and only
	// which sentinel the refusal carries. See the wrap below.
	appendOnly := make(map[string]bool)
	for _, table := range reg.appendOnlyTables() {
		appendOnly[table] = true
	}
	names := make([]string, 0, len(managed))
	for _, obj := range managed {
		names = append(names, obj.name)
	}
	sort.Strings(names)
	list, args := tableParams([]any{dialect.EngineSchema, ownerRole}, names)
	// #nosec G202 -- `list` is tableParams' output: ONLY "$3,$4,…" placeholders. The
	// relation names travel as bound arguments, the schema as $1 and the role as $2.
	q := `SELECT c.relname, r.rolname, pg_catalog.pg_has_role($2, c.relowner, 'USAGE')
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_roles r ON r.oid = c.relowner
WHERE n.nspname = $1 AND c.relkind IN ('r','p') AND c.relname IN (` + list + `)`
	rows, err := owner.QueryContext(ctx, q, args...)
	if err != nil {
		return upgradePreflightCause(err, "read owner authority over the managed relations")
	}
	defer rows.Close() //nolint:errcheck // rows.Err() below carries any read failure
	var unauthorized []string
	boundaryAffected := false
	for rows.Next() {
		var relation, relationOwner string
		var authorized bool
		if err := rows.Scan(&relation, &relationOwner, &authorized); err != nil {
			return upgradePreflightCause(err, "read owner authority over the managed relations")
		}
		if !authorized {
			unauthorized = append(unauthorized, fmt.Sprintf("%s (owned by %q)", relation, relationOwner))
			if appendOnly[relation] {
				boundaryAffected = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return upgradePreflightCause(err, "read owner authority over the managed relations")
	}
	if len(unauthorized) > 0 {
		sort.Strings(unauthorized)
		if boundaryAffected {
			// TWO sentinels. reconcileAppendOnlyACL has always reported this exact
			// condition as store.ErrAppendOnlyACLUnverifiable — PostgreSQL reports a
			// REVOKE of privileges one did not grant as a SUCCESS, so an owner that does
			// not own the relation cannot administer or verify its ACL, and an
			// unanswerable boundary check is a refusal rather than a pass. This preflight
			// reaches that conclusion earlier; it must not rename it on the way.
			return fmt.Errorf(
				"sqlstore: %w: %w — the owner role %q is neither the owner of nor an effective member of the owning role for %d relation(s) this upgrade may alter or reconcile: %s. An ACL of ALL is not authority to ALTER or to administer an ACL, so this boot would fail partway through its own migration and its REVOKEs would report success while changing nothing. Nothing has been migrated",
				store.ErrPostgresUpgradePrivilegePreflight, store.ErrAppendOnlyACLUnverifiable,
				ownerRole, len(unauthorized), strings.Join(unauthorized, ", "))
		}
		return upgradePreflightRefusal(
			"the owner role %q is neither the owner of nor an effective member of the owning role for %d relation(s) this upgrade may alter or reconcile: %s. An ACL of ALL is not authority to ALTER, so this boot would fail partway through its own migration. Nothing has been migrated",
			ownerRole, len(unauthorized), strings.Join(unauthorized, ", "))
	}
	return nil
}

// upgradePreflightProbeFuture measures what the app role will hold on a relation this
// owner is about to create, by creating one and rolling it back.
//
// The identity is RANDOM and proved ABSENT before use, and the object is then addressed
// by the OID the creation produced — never by the name again. Accepting the probe by
// name would let a pre-existing object of that name answer for it; addressing it by OID
// means the answer is about the object this transaction just made.
//
// A rollback that does not complete is the preflight's OWN failure, not a footnote: it
// is the difference between "nothing was created" and "something may have been". The
// error is joined rather than replaced so the original cause survives, and
// withMigrationLock retires the session on every exit path anyway.
func upgradePreflightProbeFuture(
	ctx context.Context,
	owner dialect.Execer,
	appRole, ownerRole string,
	required []string,
	futureTables []string,
) error {
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		return upgradePreflightCause(err, "begin the future-relation probe")
	}
	// The probe transaction is ALWAYS rolled back — there is no success path that
	// commits it. finish is the single exit so no branch can forget.
	finish := func(cause error) error {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return errors.Join(cause, upgradePreflightCause(rollbackErr,
				"the future-relation probe could not be rolled back, so this boot cannot state that nothing was created"))
		}
		return cause
	}

	// The probe must run as the role whose future creations are the subject. Asking as
	// anyone else would answer a question about a different owner's default privileges.
	var acting string
	// current_user is a reserved SQL keyword, not a schema-qualifiable function, so
	// this is the one catalog read in this file that cannot carry a pg_catalog prefix.
	if err := tx.QueryRowContext(ctx, `SELECT current_user`).Scan(&acting); err != nil {
		return finish(upgradePreflightCause(err, "read the probe session's role"))
	}
	if acting != ownerRole {
		return finish(upgradePreflightRefusal(
			"the future-relation probe is running as %q but this boot resolved the owner role as %q; a probe run by another role would answer about that role's future objects",
			acting, ownerRole))
	}

	name, err := freshProbeRelationName()
	if err != nil {
		return finish(upgradePreflightCause(err, "mint a probe identity"))
	}
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, dialect.EngineSchema, name).Scan(&present); err != nil {
		return finish(upgradePreflightCause(err, "prove the probe identity absent"))
	}
	if present != 0 {
		return finish(upgradePreflightRefusal("the probe identity %q was already present", name))
	}
	// Qualified, ordinary, and with no guard of its own: what is being measured is the
	// SCHEMA's future-object posture for this owner, not anything about the table.
	// #nosec G202 -- `name` is freshProbeRelationName's output: a fixed prefix plus 32
	// hex characters, quoted. The schema is the engine's own constant.
	if _, err := tx.ExecContext(ctx, `CREATE TABLE `+quoteIdent(dialect.EngineSchema)+`.`+quoteIdent(name)+` (id bigint)`); err != nil {
		return finish(upgradePreflightCause(err, "create the future-relation probe as %q", ownerRole))
	}
	oid, err := pgRelationOID(ctx, tx, name)
	if err != nil {
		return finish(upgradePreflightCause(err, "resolve the created probe's OID"))
	}
	if oid == 0 {
		return finish(upgradePreflightRefusal("the created probe %q did not resolve to an OID", name))
	}
	// On the probe's OWN transaction, which is the only session that can see an
	// uncommitted relation.
	missing, err := upgradeMissingPrivileges(ctx, tx, appRole, oid, required)
	if err != nil {
		return finish(upgradePreflightCause(err, "read the application role's effective privileges on probe OID %d", oid))
	}
	if len(missing) > 0 {
		return finish(upgradePreflightRefusal(
			"this upgrade still has to create %d relation(s) — %s — and the application role %q would hold no %s on them: measured on a rolled-back probe relation (OID %d) created by %q in schema %q. NOTHING HAS BEEN MIGRATED: this boot refused before its first durable change, so the schema-migration trackers and every relation are exactly as they were.\n\nTwo supported ways forward, and both keep the preflight:\n  1. Provision the grants so they reach FUTURE tables too, then start again:\n       ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s GRANT %s ON TABLES TO %s;\n     A direct grant, a group role or PUBLIC all count — pg_default_acl is the convenient route, not the requirement.\n  2. Separate the phases deliberately: run\n       olivares migrate apply --dsn … --owner-dsn …\n     which applies the schema and stops WITHOUT serving, then GRANT on the now-existing relations, then start the service. Its success says the schema was applied; it does not say the node is ready",
			len(futureTables), strings.Join(futureTables, ", "), appRole,
			strings.Join(missing, "/"), oid, ownerRole, dialect.EngineSchema,
			ownerRole, dialect.EngineSchema, strings.Join(required, ", "), appRole))
	}
	return finish(nil)
}

// ensureLeaderEpochRelation creates the fencing-epoch table if it is absent, and does
// nothing else: no leadership is acquired, no row is inserted, no epoch is bumped.
//
// It is FACTORED OUT so the schema phase and the elector run the SAME DDL. That matters
// for exactly one reason, and it is the reason `migrate apply` exists: leader_epoch used
// to be born inside pgLockBackend.ensure, i.e. AFTER the point `migrate apply` returns.
// An operator who applied the schema, granted on everything that existed, and then
// started the service would find the elector creating a relation the grant never
// covered — so the ceremony this contract promises could not actually be completed
// without future-object defaults. Materializing it in the schema phase closes that, and
// sharing the statement is what keeps the two paths from drifting into two shapes.
func ensureLeaderEpochRelation(ctx context.Context, ex dialect.Execer) error {
	_, err := ex.ExecContext(ctx, fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (id INTEGER PRIMARY KEY, epoch BIGINT NOT NULL, holder TEXT NOT NULL, acquired_at TEXT NOT NULL)`,
		leaderEpochTable))
	return err
}
