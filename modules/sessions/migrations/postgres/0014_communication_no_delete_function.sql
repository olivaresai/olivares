-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE OR REPLACE FUNCTION olivares_sessions_communication_no_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
BEGIN
	RAISE EXCEPTION 'olivares: sessions communication rows cannot be hard-deleted'
		USING ERRCODE = '23514';
END;
$fn$;
