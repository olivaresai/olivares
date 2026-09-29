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
