-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_communication_guard_guard_upd
BEFORE UPDATE ON sessions_communication_guard
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: CommunicationGuard monotonicity changed')
	WHERE NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id
		OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at
		OR NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at
		OR NEW.guard_kind IS NOT OLD.guard_kind OR NEW.next_seq < OLD.next_seq
		OR NEW.last_db_time < OLD.last_db_time OR NEW.last_db_time > NEW.updated_at;
END;
