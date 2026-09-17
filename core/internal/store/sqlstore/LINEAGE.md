# Lineage authority (core v8)

Six positive generations belong to each business tenant: sessions, agents,
resources, workspaces, agent_groups and agent_group_members. Guards compare the
closed projections in `lineage_catalog.go`, with null-safe equality. Inserts,
physical deletes and visibility changes participate; presentation and no-op
updates do not advance lineage. A transaction advances each affected relation
once. Directory generations retain their separate, broader causal contract.

## Writers and trust

Business Mutate obtains a shared PostgreSQL L0 gate and an exclusive L1 for its
canonical tenant before invoking its callback. The first System mutator obtains
exclusive L0 before discovering targets or acquiring lower locks. Read-only
System and View acquire neither gate. Ordinary order is lineage gates, directory,
authority facts, audit. AuthScope decision Claims take directory and system audit
locks before Claim DML, matching DropTenant. Inverse ordinary ordering refuses and
poisons the envelope. SQLite retains its single-writer transaction semantics.

The private Scope and validated System lifecycle supply tenant authority. The
application SQL credential is a shared, trusted engine credential: a SQL lock,
transaction ID, GUC or function argument does not authenticate a user or authorize
a tenant. Database routines enforce scope consistency, participation, order and
monotonicity. They do not replace application authorization. An actor possessing
that shared credential can reproduce its SQL protocol; this is not an independent
SQL tenant authentication boundary.

With separate owner and app roles, app cannot write writer/touched/seed/control
metadata or epochs directly, or execute source trigger functions. The source
triggers alone insert touched markers and advance epochs. App can execute these
closed helpers (fixed search_path, owner and bodies attested):

| Helper | Validated input and effect |
| --- | --- |
| `olivares_lineage_begin(tenant)` | Tenant from private Mutate or System bind; canonical and current transaction binding, actual L0/L1 participation; enroll current DB transaction only. |
| `olivares_lineage_finish()` | No tenant input; removes only current DB transaction's writers, cascading its touched markers. |
| `olivares_lineage_seed(tenant)` | Tenant from CreateOrg or authoritative staged boot inventory; exclusive L0, canonical/current binding, unique durable seed witness; initializes six epochs once. |
| `olivares_lineage_drop(tenant)` | Tenant validated by DropTenant; exclusive L0 and binding; refuses remaining source rows and retires its epochs. |
| `olivares_lineage_complete()` | Exclusive boot reconciliation; only marks initial coverage complete, never reopens it. |
| Six `olivares_lock_core_*_lineage_epoch(tenant)` helpers | Current bound tenant and engine writer transaction; locks one fixed epoch row without granting app UPDATE privileges. |

The durable `core_lineage_seeded` witness survives tenant retirement. No app
helper deletes it. Its retention cost is one tenant-ID row per ever-seeded tenant;
there is no garbage collection in v8. Removing it would reopen generation-reset
ABA and requires a future explicit monotonic retirement contract.

SQLite and PostgreSQL single-role deployments preserve the same Scope, Claim,
transaction and concurrent-change checks. They trust their process/database
owner: a database owner can change SQL objects or metadata. Split-owner ACL tests
do not establish resistance to that owner, a superuser, or a stolen shared
application credential's application-level authority. Existing DDL-fence posture
continues to be reported separately.

## Reads and boot

The optional lineage reader returns only observed canonical generations, including
facts for an empty query. It never seeds missing coverage. Tracked v8 relations,
guards and source RLS/trigger inventories are verified before additive schema
reconciliation, and again at the relevant installation boundary. Prior core
migrations keep their existing rendered statements.

The read authority validator uses a fresh View, the same closed fact/lease/fence
grammar as the locker, and database time. It takes no row/advisory locks and never
renews a lease. Digest and freshness validation alone do not establish current
row authority. Missing, malformed or contradictory facts refuse delivery.
