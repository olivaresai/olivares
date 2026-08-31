-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_event_guard_upd
BEFORE UPDATE ON sessions_work_event
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: sessions work tenant/workspace lineage is immutable')
	WHERE NEW.tenant_id IS NOT OLD.tenant_id OR NEW.workspace_id IS NOT OLD.workspace_id;
	SELECT RAISE(ABORT, 'olivares: work events are immutable') WHERE OLD.id IS NEW.id;
END;
