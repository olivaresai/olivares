-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- Mutable K1 projections are mutable only through governed commands, but none
-- may be hard-deleted. Tombstones/archive fields carry the supported lifecycle.
CREATE OR REPLACE FUNCTION olivares_sessions_work_no_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
BEGIN
	RAISE EXCEPTION 'olivares: sessions work rows cannot be hard-deleted'
		USING ERRCODE = '55000';
END;
$fn$;
