-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_guard_clock_monotonic
BEFORE UPDATE ON sessions_work_guard
FOR EACH ROW
WHEN OLD.guard_kind = 'lease_clock'
BEGIN
	SELECT RAISE(ABORT, 'olivares: lease clock guard identity/time is immutable and required')
	WHERE NEW.guard_kind <> OLD.guard_kind OR NEW.workspace_id <> OLD.workspace_id
		OR NEW.tenant_id <> OLD.tenant_id OR NEW.last_db_time IS NULL;
	SELECT RAISE(ABORT, 'olivares: lease clock moved backwards without effective rebase evidence')
	WHERE NEW.last_db_time < OLD.last_db_time AND (NEW.clock_rebase_decision_id IS NULL
		OR NEW.clock_rebase_decision_id IS OLD.clock_rebase_decision_id
		OR NEW.clock_rebase_evidence_ref IS NULL
		OR length(CAST(NEW.clock_rebase_evidence_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR NOT EXISTS (SELECT 1 FROM sessions_work_decision d
			JOIN sessions_work_decision_head h ON h.tenant_id = d.tenant_id
				AND h.workspace_id = d.workspace_id AND h.work_item_id = d.work_item_id
				AND h.current_decision_id = d.id AND h.state = 'effective'
			WHERE d.id = NEW.clock_rebase_decision_id AND d.tenant_id = NEW.tenant_id
				AND d.workspace_id = NEW.workspace_id));
	SELECT RAISE(ABORT, 'olivares: lease clock rebase evidence changed without rollback')
	WHERE NEW.last_db_time >= OLD.last_db_time AND
		(NEW.clock_rebase_decision_id IS NOT OLD.clock_rebase_decision_id
		OR NEW.clock_rebase_evidence_ref IS NOT OLD.clock_rebase_evidence_ref);
END;
