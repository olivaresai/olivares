<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Core migration order

Core v17 records the nullable authentication freshness witness. Core v16 remains
permanently unregistered. E12 must use a new ordinal greater than the greatest
registered core version when that work is implemented; it must not fill v16.

`Open` checks the recorded versions against the compiled plan before migration
DDL. `preflightCoreMigrationVersion` refuses an unrecognized version, including
v16. `classifyAccessEvidenceBoot` then requires the recorded history to be an
ordered prefix of that plan. The sequence through v17 is `1..11, 13, 14, 15, 17`.
An absent unregistered ordinal is valid; a missing intermediate registered row
is edited history and must refuse. Inserting v16 into a later compiled plan would
invalidate already deployed histories that contain v17, so it is not an upgrade
strategy. Generic `migrate.Apply` behavior does not establish valid core boot.

This decision changes no migration statement, historical render, backfill or
boot validation. `TestAuthenticationFreshnessOpenRefusesEditedHistory` covers
actual `Open` with SQLite and PostgreSQL: the completed plan opens, deleting v15
while retaining v17 refuses, and inserting v16 refuses without repairing history.
`TestAuthenticationFreshnessMigrationPreservesPermanentGapAndHistoricalRender`
pins the registered plan and the unchanged historical session DDL. These tests
require the official CI database qualification; source preparation alone is not
runtime evidence.

Schema adoption and security repair use the same versioned runner. Core v21
adopts the descriptor schema and OS-account reservations once; the separate
`schema_migrations_os_account_guards` plan reasserts reservation indexes at
every boot. Rollout evidence guards likewise use `migrate.ReconcileTx` with
`schema_migrations_rollout_guards`. Repair preserves the original version
receipt and runs transactionally: missing guards are restored, and a failed
repair refuses startup. Schema evolution still requires a new version.

`TestRolloutEvidenceGuardsRepairOnRestart` drops each SQLite evidence trigger,
reopens the store, and checks that updates/deletes remain blocked without
rewriting receipts or losing evidence. `TestOSAccountReservationIndexesRepairOnRestart`
covers all three reservation indexes on SQLite and PostgreSQL.

Core v23 adopts the audit blinding schema once. On PostgreSQL, the separate
`schema_migrations_audit_blind_guards` repair plan reasserts the blind-length
CHECK on every boot without changing v23 or sealed rows. The existing non-owner
warning and SQLite upgrade posture remain unchanged. `TestAuditBlindGuardRepairsOnRestart`
removes the CHECK across successive restarts, verifies malformed blinds are
rejected, and refuses startup when existing malformed data prevents repair.
Audit rows, blinding state, and migration receipts remain unchanged.
