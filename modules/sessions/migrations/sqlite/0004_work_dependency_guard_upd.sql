-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_dependency_guard_upd
BEFORE UPDATE ON sessions_work_dependency
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: sessions work tenant/workspace lineage is immutable')
	WHERE NEW.tenant_id IS NOT OLD.tenant_id OR NEW.workspace_id IS NOT OLD.workspace_id;
	SELECT RAISE(ABORT, 'olivares: dependency identity and creation evidence are immutable')
	WHERE OLD.work_item_id IS NOT NEW.work_item_id OR OLD.depends_on_id IS NOT NEW.depends_on_id
		OR OLD.relation IS NOT NEW.relation OR OLD.added_by_kind IS NOT NEW.added_by_kind
		OR OLD.added_by_ref IS NOT NEW.added_by_ref;
	SELECT RAISE(ABORT, 'olivares: invalid sessions work dependency vocabulary or self-edge')
	WHERE NEW.relation <> 'blocks' OR NEW.work_item_id = NEW.depends_on_id
		OR NEW.active NOT IN (0,1)
		OR NEW.added_by_kind NOT IN ('user','agent','session','system')
		OR length(CAST(NEW.added_by_ref AS BLOB)) NOT BETWEEN 1 AND 512;
	SELECT RAISE(ABORT, 'olivares: active dependency cannot carry removal evidence')
	WHERE NEW.active = 1 AND (NEW.removed_by_kind IS NOT NULL
		OR NEW.removed_by_ref IS NOT NULL OR NEW.removed_at IS NOT NULL);
	SELECT RAISE(ABORT, 'olivares: inactive dependency requires complete removal evidence')
	WHERE NEW.active = 0 AND (NEW.removed_by_kind NOT IN ('user','agent','session','system')
		OR NEW.removed_by_ref IS NULL OR length(CAST(NEW.removed_by_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR NEW.removed_at IS NULL);
	SELECT RAISE(ABORT, 'olivares: dependency endpoints must exist in the same tenant/workspace')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_work_item w WHERE w.id = NEW.work_item_id
		AND w.tenant_id = NEW.tenant_id AND w.workspace_id = NEW.workspace_id)
		OR NOT EXISTS (SELECT 1 FROM sessions_work_item w WHERE w.id = NEW.depends_on_id
			AND w.tenant_id = NEW.tenant_id AND w.workspace_id = NEW.workspace_id);
	SELECT RAISE(ABORT, 'olivares: sessions work dependency cycle')
	WHERE NEW.active = 1 AND EXISTS (
		WITH RECURSIVE reach(id) AS (
			SELECT d.depends_on_id FROM sessions_work_dependency d
			WHERE d.tenant_id = NEW.tenant_id AND d.workspace_id = NEW.workspace_id
				AND d.active = 1 AND d.work_item_id = NEW.depends_on_id AND d.id <> NEW.id
			UNION
			SELECT d.depends_on_id FROM sessions_work_dependency d
			JOIN reach r ON r.id = d.work_item_id
			WHERE d.tenant_id = NEW.tenant_id AND d.workspace_id = NEW.workspace_id
				AND d.active = 1 AND d.id <> NEW.id)
		SELECT 1 FROM reach WHERE id = NEW.work_item_id);
END;
