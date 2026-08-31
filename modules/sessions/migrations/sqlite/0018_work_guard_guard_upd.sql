-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_guard_guard_upd
BEFORE UPDATE ON sessions_work_guard
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: sessions work tenant/workspace lineage is immutable')
	WHERE NEW.tenant_id IS NOT OLD.tenant_id OR NEW.workspace_id IS NOT OLD.workspace_id;
	SELECT RAISE(ABORT, 'olivares: invalid sessions work guard state')
	WHERE NEW.guard_kind NOT IN ('dependency_graph','lease_clock') OR NEW.epoch < 0
		OR (NEW.guard_kind = 'dependency_graph' AND NEW.last_db_time IS NOT NULL);
	SELECT RAISE(ABORT, 'olivares: work guard identity is immutable and epoch advances by one')
	WHERE NEW.guard_kind IS NOT OLD.guard_kind OR NEW.epoch <> OLD.epoch + 1;
END;
