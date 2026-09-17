<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Core v11: evidence-operation `refused` state

Status: source increment for Root review. It is not a deployed migration, release readiness or
activation of any producer.

## What changes

- `model.EvidenceOpRefused` (`refused`) is a valid evidence-operation state.
- `Terminal()` is exactly `completed`, `not_sent`, `unknown`, `blocked` and `withheld`, so no
  settlement can request `refused`.
- The journal codec requires a `refused` row to carry an empty claim reference, outcome
  reference, result digest and dispatch reference. Any value there is an integrity error.
- Generic `Settle` rejects an existing `refused` row with `ErrEvidenceIntegrity` before the
  re-settle comparison.
- Generic `Claim` is unchanged. A same-digest replay of a `refused` row returns it with
  `Fresh=false` and no anchor, which consumers classify as a missing anchor and refuse.
- No code path in this increment writes `refused`. Its future producer must also require the
  verified v11 journal.

## Core migration v11 (`evidence_operation_refused_state`)

One migration owns the change. The journal DDL, the row witness and the tracking row commit in
one transaction on each engine.

The supported inputs are generated from this binary's descriptor and dialect.

| Engine | Input | Action |
|---|---|---|
| SQLite | S5 (five words), S6 (six words) | Rebuild into the seven-word table and recreate the generated indexes and triggers |
| SQLite | S7 | Verify only |
| PostgreSQL | P5 (`evidence_operations_state_check`), P6-fresh (same name), P6-widened (`evidence_operations_state_vocab`) | Replace the state CHECK with the seven-word `evidence_operations_state_vocab` |
| PostgreSQL | P7 under either name | Verify only; no rename |

Every other input refuses unchanged. That includes:

- an absent relation;
- a SQLite table without a state CHECK;
- a lax, extra or unvalidated CHECK;
- an unknown constraint name;
- any extra or altered column, index, trigger, policy, rule or child relation.

**SQLite comparison.** The whole `CREATE TABLE`, index and trigger text is compared as tokens
against the generated definitions. Only whitespace, identifier case and double-quoted identifier
spelling are normalized. Comments and unknown syntax refuse. `table_xinfo` and the index catalog
are additional witnesses.

**PostgreSQL comparison.**

- Columns, all constraints, index facts, the tenant policy, row-level security, triggers, rules,
  children, owner and ACL are all compared.
- The expected CHECK and policy expressions are deparsed by the server in rolled-back TEMP
  probes. The probes run on the migration-lock connection after the version preflight and before
  any migration transaction.
- The ACL is preserved as found.
- A non-owner role refuses.

**Row witness.** Every row is hashed in canonical tenant and ID order with a length-prefixed
encoding that distinguishes NULL from the empty string. The witness must be equal before and
after the change.

**Per boot.** Once v11 is recorded, the journal must classify as S7 or P7 before any reconciler
runs. Otherwise boot fails with `ErrEvidenceRefusedTrackedStale`.

## Compatibility limits

- **Old binaries.** A v10 binary stops at the core-version preflight with
  `ErrCoreSchemaVersionAhead`. The tests inject the supported version into that preflight; they do
  not execute an old binary.
- **No downgrade.** Recovery is a restore of the pre-transition backup or a forward fix.
- **Operator preconditions.** Writer drain and a qualified backup before upgrade stay outside
  this source.
- **Logical restore.** The PostgreSQL logical-restore privilege closure accepts the exact active
  prefix of this binary's compiled plan through the supported version (v11). It refuses future,
  missing, misnamed or reverted history and runs no migration or object repair.
- **PostgreSQL 18.** NOT NULL constraint rows are accepted only as the exact generated set. Only
  PostgreSQL 16 was exercised.
