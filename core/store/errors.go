// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import "errors"

// Sentinel errors returned by the store. Callers match them with errors.Is.
// Strings are lowercase and unpunctuated per the project's lint rules.
var (
	// ErrNotFound is returned when an entity does not exist OR belongs to
	// another tenant. The two cases are indistinguishable on purpose: a tenant
	// must not be able to probe the existence of another tenant's ids.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned on an optimistic-concurrency mismatch or unique-key collision.
	ErrConflict = errors.New("version conflict")
	// ErrInvalidID is returned when CreateWithID receives an empty, non-canonical
	// or non-UUIDv7 identifier. The write is refused before any SQL is issued.
	ErrInvalidID = errors.New("invalid id")
	// ErrNoTenant is returned when a tenant-scoped operation is attempted with
	// no tenant bound (fail closed: never run unscoped).
	ErrNoTenant = errors.New("no tenant bound")
	// ErrUnknownEntity is returned for an entity kind that was never registered.
	ErrUnknownEntity = errors.New("unknown entity kind")
	// ErrInvalidDescriptor is returned when a module descriptor violates the
	// multi-tenant, naming or type rules.
	ErrInvalidDescriptor = errors.New("invalid entity descriptor")
	// ErrAppendOnly is returned when an update/delete is attempted on an
	// append-only table.
	ErrAppendOnly = errors.New("table is append-only")
	// ErrTenantViolation is returned when a write would place a row in a tenant
	// other than the bound one.
	ErrTenantViolation = errors.New("tenant scope violation")
	// ErrReadOnly is returned when a write is attempted inside a View scope.
	ErrReadOnly = errors.New("scope is read-only")
	// ErrTransactionTimeNotObserved is returned by the optional
	// TransactionStampedGenericRepo methods unless TransactionNow succeeded first
	// on the exact surrounding Scope. The repository never substitutes process
	// time or accepts a caller-provided timestamp for an authority-sensitive write.
	ErrTransactionTimeNotObserved = errors.New("transaction time not observed")
	// ErrRowLockUnavailable means a repository cannot fence a row for the
	// lifetime of the surrounding mutation. Authorization code must treat this
	// as missing evidence, never as permission to continue with an unlocked Get.
	ErrRowLockUnavailable = errors.New("row locking unavailable")
	// ErrResidencyViolation is returned when a tenant-scoped unit of work is
	// attempted against a tenant whose data-residency pin (orgs.data_region) is
	// not served by this region-scoped instance (gap OPS-4). It is the
	// deny-closed cross-region backstop: data physically resident in another
	// region is never read or written from the wrong instance, and a misrouted
	// request fails loudly rather than returning a silent empty set. The API
	// layer maps it to 403.
	ErrResidencyViolation = errors.New("cross-region residency violation")
	// ErrTenantSuspended is returned when a tenant-scoped unit of work is attempted
	// against a tenant whose service has been withdrawn (orgs.status == suspended —
	//). It is the deny-closed backstop for the intermediate state between
	// "served" and "deleted": the tenant's DATA PLANE is frozen — every tenant-scoped
	// View and Mutate is denied, and nothing is deleted — while every row survives
	// intact so that restoring service is lossless.
	//
	// It is the DATA PLANE and not the whole store, and the distinction is load
	// bearing. Custody — anchoring the chain, reading its tip for a backup,
	// fencing a signing-key epoch — goes through Store.Custody, which this sentinel
	// never guards. This comment used to say "nothing is read, nothing is written",
	// and that was the bug: the chain kept being appended to by the suspension
	// itself while nothing was left able to anchor or certify it.
	//
	// It sits at the STORE, not at the HTTP edge, because that is the only layer
	// every path must cross: REST, gRPC, and the in-process background pumps that
	// advance a tenant's workflows and egress its events all re-enter through
	// View/Mutate. A gate at the edge would leave the pumps serving a tenant we
	// have stopped serving.
	//
	// The System and Auth paths deliberately pass through: the operator must always
	// be able to list, restore, and (after the grace period) delete a suspended
	// tenant, and its users must still be able to authenticate and be told WHY they
	// are refused. The API layer maps it to 423 Locked.
	ErrTenantSuspended = errors.New("tenant service suspended")
	// ErrTenantNotInService is the deny-closed refusal for a tenant whose org row
	// carries a status that is NEITHER active NOR suspended — "inactive", "error",
	// or a value this binary does not know.
	//
	// The guard used to answer ErrTenantSuspended for every non-active status, so a
	// row left inconsistent by an import, a migration or an internal caller told the
	// customer their service had been WITHDRAWN — a console renders that as a
	// billing problem, and an operator chasing a billing problem that does not exist
	// never looks at the corrupt row that caused it. SetOrgStatus accepts only the
	// two service states, so this is not reachable through the public API today; the
	// internal CreateOrg does not validate what it is handed, which is how such a row
	// gets written at all.
	//
	// It is a SEPARATE sentinel rather than a widened meaning of the one above
	// because callers narrow their handling on the name: anything matching
	// ErrTenantSuspended is entitled to assume a commercial decision was recorded,
	// and for these rows none was. The API maps it to 423 Locked as well — the work
	// must not run either way — but under its own code, so the two are never
	// rendered as the same thing.
	ErrTenantNotInService = errors.New("tenant is not in service")
	// ErrCursorWithSort is returned when a keyset Cursor is combined with a
	// custom Sort: the cursor is only valid for the default id ordering.
	ErrCursorWithSort = errors.New("cursor not supported with custom sort")
	// ErrNotLeader is returned when a write is attempted on a node that is not the
	// active writer in an active-passive HA cluster. It is the store-level
	// defense in depth behind the load-balancer drain: a standby is removed from the
	// Service endpoints by its /readyz probe, but a write that still reaches it
	// (an in-process background loop, a request racing the drain) fails closed here
	// rather than forking the signed audit chain. The API maps it to HTTP 503 so the
	// caller retries against the current leader. It is never returned by a single-node
	// store (SQLite, or a Postgres node whose elector was never armed).
	ErrNotLeader = errors.New("not the leader")
	// ErrAuditSpoolFull is returned in block mode when a governed write would
	// exceed the declared logical audit spool budget (ADR-0024 Q2). The write is
	// refused deny-closed before evidence is lost; the API maps it to HTTP 503 so
	// reads remain available while the operator restores audit capacity.
	ErrAuditSpoolFull = errors.New("audit spool full")
	// ErrReservedAuditAction prevents structural poisoning of the audit verifier
	// vocabulary. Only the store's internal seal path may write audit.gap, and
	// audit.recover is admitted only with its detached off-box signature.
	ErrReservedAuditAction = errors.New("audit action is reserved")
	// ErrStoreUnavailable wraps a backend-availability failure at the SQL
	// boundary: a lost/refused connection or a Postgres SQLSTATE class-08
	// "Connection Exception". It distinguishes "the database is unreachable
	// right now" (transient - the evidence taxonomy maps it to
	// ledger_unavailable, sdk/evidence.go) from a genuine write fault
	// (constraint, serialization, bad statement), which stays unwrapped.
	ErrStoreUnavailable = errors.New("store backend unavailable")
	// ErrResourceCycle is returned when a resource-tree Move would make a resource
	// its own ancestor (moving it under itself or one of its descendants), which
	// would corrupt the materialized path (FASE X). The move is rejected
	// whole rather than left to build an unreachable cycle.
	ErrResourceCycle = errors.New("resource move would create a cycle")
	// ErrInvalidVerdict is returned by AuthScope.FinalizeDecisionClaim when the
	// verdict material is not self-consistent: an empty or non-JSON verdict
	// document, or a verdict_hash that is not the SHA-256 of the verdict bytes. The
	// store refuses to persist a decision it cannot itself validate, so a raw store
	// caller cannot finalize a claim with forged or malformed material.
	ErrInvalidVerdict = errors.New("invalid decision verdict")
	// ErrEvidenceMissing is returned by AuthScope.FinalizeDecisionClaim when the
	// target pending claim carries no durable per-operation anchor
	// (evidence_anchored is false because its own claim/overclaim audit dropped
	// under a degrade-mode spool). Such a claim is a deny-closed tombstone: the
	// store refuses to finalize it, so a raw store caller cannot resurrect it into a
	// usable decision with only a healthy finalize anchor.
	ErrEvidenceMissing = errors.New("decision claim has no durable evidence anchor")
	// ErrEnumerationNotAuthoritative is returned by SystemScope.ListOrgs when this
	// store cannot perform an AUTHORITATIVE cross-tenant read: Postgres, where the
	// System path clears the tenant GUC and FORCE-RLS therefore matches nothing,
	// with no dedicated BYPASSRLS admin pool configured (--admin-dsn) to run it on.
	//
	// It exists because the answer used to be an EMPTY SLICE, and an empty slice
	// says "there are no tenants" using the same bytes as "I was not allowed to
	// look". Every caller read the first meaning. Two ceremonies then certified
	// over material they never enumerated: the periodic checkpointer logged
	// "checkpoint written for all tenants" (cmd/olivares/checkpoint.go:120-128) and
	// `audit key-transition` swept a blind tenant list. The fix is here rather than
	// in each caller so a caller cannot inherit the blindness by being written
	// later: fail closed at the read, and let it propagate.
	//
	// Callers that legitimately tolerate a partial list must say so by NAME, with
	// ListOrgsVisible — which returns the same rows plus the authoritative flag,
	// so the tolerance is a written decision and greppable rather than a default.
	ErrEnumerationNotAuthoritative = errors.New("cross-tenant enumeration is not authoritative on this store")

	// The boot self-test's trigger-boundary verdicts, one sentinel per CATEGORY of
	// fault. Open wraps the matching sentinel, so a caller — an operator tool, a
	// health endpoint, a regression test — attributes a refusal by errors.Is rather
	// than by matching message text. Text is diagnostics: it is meant to change as
	// the message gets more helpful, and a check built on it cannot tell "the guard
	// is absent" from "the guard is present and inert", which are different
	// incidents with different remedies.

	// ErrSchemaTriggerMissing: a module-declared trigger is absent from the table it
	// was declared on. A namesake elsewhere in the schema does not satisfy it — on
	// PostgreSQL a trigger name is only unique per table.
	ErrSchemaTriggerMissing = errors.New("required schema trigger is absent from its table")
	// ErrSchemaTriggerInert: the trigger exists on the right table and DOES NOT FIRE.
	// PostgreSQL keeps a DISABLED ('D') or replica-only ('R') trigger in the catalog,
	// so this is invisible to any check that asks only whether the object exists.
	ErrSchemaTriggerInert = errors.New("required schema trigger exists but does not fire")
	// ErrSchemaTriggerTampered: right name, right table, firing — and a body that no
	// longer matches the declared digest, i.e. the object was replaced (a no-op body
	// passes every structural check, because structurally it IS the declared trigger).
	ErrSchemaTriggerTampered = errors.New("required schema trigger does not match its declared definition")
	// ErrSchemaTriggerUnexecutable: in an owner/app split the guard is present and
	// firing, but the application role may not EXECUTE the function it invokes.
	ErrSchemaTriggerUnexecutable = errors.New("application role cannot execute a required trigger function")
	// ErrSchemaBoundaryTableMissing: a module declared trigger invariants but no
	// append-only fact table, so the grant half of the boundary cannot be verified.
	ErrSchemaBoundaryTableMissing = errors.New("no security-boundary fact table was registered for required triggers")
	// ErrSchemaBoundaryGrantMissing: in an owner/app split the application role lacks
	// SELECT or INSERT on a security-boundary fact table. The guard would fire, but the
	// application could not write the fact it guards — the boundary is only half there,
	// and the failure would otherwise surface as a runtime error on first use.
	ErrSchemaBoundaryGrantMissing = errors.New("application role lacks a grant on a security-boundary table")
	// ErrSchemaBoundaryUnjudgeable: the boundary cannot be judged at all, because the
	// session's replication role inverts PostgreSQL's firing rules and a verdict would
	// be the opposite of the truth. Boot refuses such a session earlier; this is the
	// self-test refusing to guess if it is ever reached.
	ErrSchemaBoundaryUnjudgeable = errors.New("the trigger boundary cannot be judged in this session")
	// ErrAppendOnlyACLOpen is returned by the boot verification when the application
	// role still holds UPDATE, DELETE or TRUNCATE on an append-only table after the
	// engine re-asserted the revoke. TRUNCATE is the one that matters most: it is a
	// statement-level operation, so the immutability trigger cannot see it and the ACL
	// is the only defense the schema has. A privilege that survives the re-assertion
	// was granted outside it — directly, through a group role, or through PUBLIC.
	ErrAppendOnlyACLOpen = errors.New("append-only ACL is open to the application role")
	// ErrAppendOnlyGrantMissing is returned by the boot verification when the
	// application role cannot SELECT or INSERT on an append-only table. The engine
	// removes only UPDATE/DELETE/TRUNCATE there, so a role that also lacks read or
	// append is misprovisioned — it would fail at the first attempt to record
	// evidence, which is the worst possible moment to discover it.
	ErrAppendOnlyGrantMissing = errors.New("append-only table is not readable/appendable by the application role")
	// ErrEngineSchemaUnusable is returned when the application role lacks USAGE on the
	// schema the engine's tables live in. Table privileges are meaningless without it:
	// PostgreSQL refuses every query against a schema the role may not use, so a boot
	// that checked only table ACLs would report evidence readable and appendable and
	// then fail on the first row it touched.
	ErrEngineSchemaUnusable = errors.New("application role cannot use the engine schema")
	// ErrAppendOnlyACLUnverifiable is returned when the engine cannot establish the
	// append-only boundary's state: some of the tables in scope did not resolve, or
	// the DDL connection does not own them and therefore cannot administer their ACL
	// (PostgreSQL reports a revoke of privileges one did not grant as a SUCCESS, so
	// the reconcile must check ownership rather than trust the statement's result).
	// Deny-closed: an unanswerable check is a refusal, never a pass.
	ErrAppendOnlyACLUnverifiable = errors.New("append-only ACL could not be verified")
	// ErrWorkspaceConfinement is returned when a workspace-confined unit of work
	// (ConfineWorkspace/B-03) is asked to write outside the confined
	// workspace — creating or moving a row into another workspace, or mutating a
	// tenant-wide entity that carries no workspace lineage. The API maps it to 403.
	ErrWorkspaceConfinement = errors.New("workspace confinement violation")
	// ErrWorkspaceLineageRequired is returned when a workspace-confined unit of
	// work is asked for an entity that declares no workspace lineage, so the store
	// cannot prove which of its rows belong to the caller. It is deliberately an
	// error and not an empty page: an empty page asserts "this collection was
	// inspected in full and holds nothing of yours", which is a claim the engine
	// cannot make. The API maps it to 403.
	ErrWorkspaceLineageRequired = errors.New("workspace lineage required")
	// ErrTenantTablesUnresolved is returned when the boot privilege check cannot
	// resolve every registered tenant table in the engine's schema, so its answer
	// covers less than it claims. It is deliberately NOT the append-only sentinel:
	// this one is about ordinary tenant tables, and conflating them would give a
	// caller asking "is the evidence boundary intact?" a false positive.
	ErrTenantTablesUnresolved = errors.New("registered tenant tables did not resolve in the engine schema")
)
