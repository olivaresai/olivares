-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_guard_guard_ins
BEFORE INSERT ON sessions_work_guard
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: invalid sessions work guard state')
	WHERE NEW.guard_kind NOT IN ('dependency_graph','lease_clock') OR NEW.epoch < 0
		OR (NEW.guard_kind = 'dependency_graph' AND NEW.last_db_time IS NOT NULL);
END;
