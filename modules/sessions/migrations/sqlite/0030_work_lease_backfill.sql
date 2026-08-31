-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
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
