-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- FORCE RLS also binds the deliberately NOBYPASSRLS schema owner. The two
-- transactional NO FORCE changes let that owner backfill every tenant without
-- introducing a privileged migration role. ACCESS EXCLUSIVE locks make the
-- intermediate catalog state invisible; either both tables are FORCE again at
-- commit, or the whole migration rolls back.
DO $backfill$
BEGIN
	EXECUTE 'LOCK TABLE sessions_work_item, sessions_work_lease IN ACCESS EXCLUSIVE MODE';
	EXECUTE 'ALTER TABLE sessions_work_item NO FORCE ROW LEVEL SECURITY';
	EXECUTE 'ALTER TABLE sessions_work_lease NO FORCE ROW LEVEL SECURITY';

	INSERT INTO sessions_work_lease (
		id,
		tenant_id,
		created_at,
		updated_at,
		version,
		workspace_id,
		work_item_id,
		fence,
		state,
		renewal_count
	)
	SELECT
		i.id,
		i.tenant_id,
		i.created_at,
		i.updated_at,
		1,
		i.workspace_id,
		i.id,
		0,
		'vacant',
		0
	FROM sessions_work_item AS i
	WHERE NOT EXISTS (
		SELECT 1
		FROM sessions_work_lease AS l
		WHERE l.tenant_id = i.tenant_id
			AND l.work_item_id = i.id
	)
	ON CONFLICT (tenant_id, work_item_id) DO NOTHING;

	EXECUTE 'ALTER TABLE sessions_work_lease FORCE ROW LEVEL SECURITY';
	EXECUTE 'ALTER TABLE sessions_work_item FORCE ROW LEVEL SECURITY';
END;
$backfill$;
