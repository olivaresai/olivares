<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Directory User authority: core v10

Core v10 (`user_authority`) adds a permanent SYSTEM fence H for each User and
extends the directory writer protocol. It preserves the exact v1–v9 migration
history and the append-only edition graph. H is mutable version evidence outside
that graph; retirement retains H permanently and its UUIDv7 identity cannot be
reused. H exposes no public repository or payload.

This increment supplies durable writers, locking and activation. It does not
complete grant-only principal admission, messaging evidence, recipient receipts
or the remaining F2 readiness work. `DirectoryStatus.Enabled` stays false.

## Upgrade a stopped installation

Stop every engine and external writer on this database, deploy the upgraded
binary everywhere, and drain every old transaction. The operator must establish
these facts before asserting `--writers-upgraded --writers-drained`; the database
cannot enumerate the cluster. Use the same engine, DSNs, edition registration and
configuration as serving. Maintenance performs the normal schema, migration,
edition, guard, role, ACL and database-identity checks, then closes all private
pools. It returns a typed result and never publishes a Store or starts an elector.

The migration preserves the existing mode and generation and records
`membership-union-v1`. It creates the empty H relation and current guards without
backfilling existing Users. A staged installation may continue legacy operation
with H plus its existing G invalidations. A normal enforced legacy Open refuses
incomplete H; the maintenance command is the permitted transition path.

```sh
olivares db activate-directory-writer --engine postgres \
  --dsn env:DATABASE_URL --owner-dsn env:OWNER_DSN --admin-dsn env:ADMIN_DSN \
  --expected-generation 2 --writers-upgraded --writers-drained \
  --actor ops --reason "all upgraded writers stopped and drained"
```

Set `--expected-generation` to the observed predecessor, including an already
enforced v9 installation. Fresh staged installations normally start at 1; an
installation activated by v9 commonly has enforced generation 2. SQLite uses
`--data-dir` and needs no PostgreSQL roles. Complete ordinary SYSTEM bootstrap
before activation; maintenance never synthesizes SYSTEM or its genesis event.

Both `(staged,g,membership-union-v1)` and `(enforced,g,membership-union-v1)` become
`(enforced,g+1,user-authority-v1)`. Under migration/global locks and the closed
source inventory, one transaction validates retained H, creates H=1 only for
missing live Users, proves one SYSTEM org and the complete business-org/G
bijection, locks and advances each business G exactly once, and performs an exact
mode/generation/protocol CAS. SYSTEM never has a G. No H, G or source DML follows
the CAS. Existing H values and timestamps are preserved.

A successful retry uses the **same predecessor generation** and verifies the
exact target plus complete H/org/G/guard/posture evidence without writing. Using
the target generation as a new predecessor is refused. A lost commit response is
reconciled with a fresh locked read: complete exact target and each G=pre+1 means
committed; exact predecessor with unchanged H/G means not committed; any other
result is indeterminate. Reopen after success or indeterminate outcome. Preserve
the JSON result; do not interpret an indeterminate result as success or rollback.

## PostgreSQL without AdminDSN

Complete directory inventory needs either the existing read-only BYPASSRLS admin
connection or the attested closed routine. A tenant-filtered RLS query cannot
establish an empty or complete estate. Without the routine, a staged legacy Open
reports unknown inventory and no readiness; target Open and maintenance refuse.

After core v10 tables exist, install the routine with an explicit DBA credential:

```sh
olivares db init --install-directory-inventory --database olivares \
  --app-role olivares_app --owner-role olivares_owner \
  --superuser-dsn env:DBA_DSN
```

This operation requires existing app/owner roles and product tables. It changes
neither their passwords nor role memberships and does not grant application
access to the inventory owner. Repeating the command verifies existing objects;
any drift is refused. `--print-sql` renders the first-install SQL for review; the
raw rendered CREATE ROLE script is not the command's idempotent verifier.

For a stopped v9 installation without AdminDSN, the first maintenance attempt may
commit the additive v10 migration and then refuse because the routine is absent.
That refusal is not activation: H remains unbackfilled and the original
mode/generation/legacy protocol remain. Install the routine, then repeat the
maintenance command with the same predecessor generation. A dedicated general
migration-only command is outside this increment.

`public.olivares_directory_inventory_v1()` has no inputs and returns only
`object_kind,id,tenant_id,version`. Its fixed owner
`olivares_directory_inventory_owner` is NOLOGIN, NOINHERIT, NOSUPERUSER, BYPASSRLS,
NOCREATEROLE, NOCREATEDB and NOREPLICATION. It owns that routine and has public
schema USAGE plus SELECT on exactly orgs(id,tenant_id) and
core_directory_epoch(id,tenant_id,version). The app has EXECUTE only. The function
uses the compiled UNION ALL, exact signature and `search_path=pg_catalog`.

Boot and activation attest effective privileges and transitive SET, INHERIT and
ADMIN paths in both directions. Neither app nor schema owner may reach the
inventory role; the inventory role may not reach them or an intermediary carrying
administrative authority. Table-wide SELECT, extra columns, DML, schema CREATE,
extra ownership or administrative function EXECUTE are refused. Effective manual
grants are valid; default ACLs alone are not assumed to prove them. Superusers
remain the explicit administrative trust boundary.

## The retention guard's handler name, and what an overload of it costs

Core v10 creates the zero-input routine `public.olivares_retain_user_authority()`
and binds `core_user_authority_no_delete` to it by OID. Every boot re-verifies that
guard by hashing its **complete** catalog definition — `pg_get_triggerdef` framed with
the bound function's `pg_get_functiondef` — against one declared digest.

PostgreSQL prints that binding two ways. While the name is an unambiguous
zero-argument reference the handler is deparsed bare; create any overload of the same
name that is **callable with zero arguments**, and the same trigger is deparsed
`public.`-qualified. The measured witness is `(review text DEFAULT 'review')`. Nothing
about the trigger changes: `tgfoid`, the handler's own definition, the arguments, the
timing and the enable state are identical, and the foreign body is never executed.

This build accepts **both** complete renderings for this one invariant, measured on
PostgreSQL **16.15**. The equivalence is closed: it applies only to the exact key
`public.core_user_authority.core_user_authority_no_delete`, only to core's own
registration, only while the compiled declaration is still the exact revision the pair
was measured on, and only when the catalog reports the bound handler as
`public.olivares_retain_user_authority`. Overloads
that are not zero-callable — the `(text)` and `VARIADIC text[]` shapes — never changed
the rendering and always booted; that they did is not evidence that any other nonzero-input
overload was compatible, and the DEFAULT witness is itself nonzero-input.

Everything else still refuses the boot: a same-body handler in another schema, a handler
under another name, a replaced body, an added trigger argument, `AFTER` timing,
`WHEN (false)`, a missing guard, the name attached to another table, and a `DISABLED` or
replica-only guard. The application role's `EXECUTE` on the handler is checked separately
and still refuses when it is revoked.

**Limits, which this equivalence does not remove.**

- **Rolling the binary back does not undo it.** A binary from before this correction
  refuses the qualified rendering. If you must roll back, drop the zero-callable overload
  first; no claim is made that an older binary repairs itself.
- **Neither digest encodes owner or ACL.** `pg_get_functiondef` renders neither, so a
  `GRANT EXECUTE ... TO PUBLIC` and a changed function owner leave the digest exactly where
  it was. The independent `EXECUTE` probe and the logical restore ceremony's inventory
  helpers remain the only verification of those; a definition match is not owner/ACL closure.
- **Logical restore is a separate contract.** Its owner and ACL helpers use a wider
  name-based census, so a same-name retention overload remains an incompatibility there.
  That is unchanged and deliberately not repaired here.
- **Measured on 16.15 only.** The rendering of other PostgreSQL majors is not inferred from
  it. A privileged concurrent DDL actor remains outside the boot's snapshot guarantee.
- **The equivalence belongs to ONE revision of the retention body, and does not travel.** The
  two digests are stored as an immutable measured pair, separate from the declaration a build
  compiles in, and the comparator admits the qualified rendering only while that declaration is
  still the measured revision. So a later core release that changes the retention body does NOT
  inherit this acceptance: on that release a zero-callable overload makes the boot refuse again,
  exactly as it did before this correction, until both renderings of the new body are remeasured
  and registered. Plan an overload's removal for a core upgrade, not only for a rollback.

## Writer and consumer contract

User creation inserts H=1 atomically. User updates and retirement advance H.
AuthSession create, delete, revocation and authority changes advance the old/new
User H as applicable. Only a strict pure expiry extension, with every other
session field unchanged, preserves H; prior evidence keeps its shorter expiry.
In target protocol, ordinary User/session mutations do not enumerate and bump
business G. Membership/group changes and tenant-bound token cascades retain their
existing structural G invalidations.

Compound AuthMutate callbacks predeclare their full finite User set through
`store.AuthUserAuthorityWriter.PrepareUserAuthorityWrite`. The order is global
then sorted H then G then source/audit. Repeating a covered subset is idempotent.
Discovering a new H after G poisons the transaction even when the caller ignores
the error. Staged legacy alone can reserve a distinctly observed absent H under
the transaction-held global lock; predeclaration never creates H or grants
anything. Only an actual writer may create H for that existing legacy User.
Target missing H is refused and never repaired by an ordinary writer.

`AuthoritySnapshotBundleLocker` pins separately typed UserAuthorityFactRef values
before the unchanged 1..64 tenant Facts. H has no new 64-row cap. Duplicate equal
H refs dedupe; differing versions or malformed complete input refuse before H
locks. An ordinary AuthorizationFactRef cannot name H. Workspace confinement
preserves this optional capability and its existing transaction capabilities
without exposing SYSTEM or tenant-wide repositories. Temporary SYSTEM binding
uses the same transaction, retains locks and the SQLite writer marker, restores
the exact captured tenant on all paths, and poisons rollback if restoration fails.
A consumer requiring H must refuse an absent bundle capability. The auth H reader
and principal sealing belong to the subsequent increment.

An already-open v9 PostgreSQL process can still write during staged compatibility
and therefore must be drained. An old SQLite process cannot arm the new NOT NULL
protocol marker after migration. After target activation, missing or wrong
protocol is refused on both engines; old v7/v8/v9 reopen fails the schema-version
preflight. These cooperative guards do not defend against a malicious trusted SQL
client fabricating the current compiled protocol tag with matching privileges.
