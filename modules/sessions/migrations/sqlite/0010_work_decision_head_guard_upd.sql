-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_decision_head_guard_upd
BEFORE UPDATE ON sessions_work_decision_head
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: sessions work tenant/workspace lineage is immutable')
	WHERE NEW.tenant_id IS NOT OLD.tenant_id OR NEW.workspace_id IS NOT OLD.workspace_id;
	SELECT RAISE(ABORT, 'olivares: decision head identity is immutable and sequence advances by one')
	WHERE OLD.work_item_id IS NOT NEW.work_item_id OR OLD.decision_key IS NOT NEW.decision_key
		OR NEW.current_seq <> OLD.current_seq + 1;
	SELECT RAISE(ABORT, 'olivares: invalid sessions work decision-head vocabulary or hash')
	WHERE length(NEW.decision_key) NOT BETWEEN 1 AND 128
		OR NEW.decision_key GLOB '*[^a-z0-9._-]*' OR NEW.current_seq < 1
		OR NEW.state NOT IN ('effective','revoked') OR length(NEW.head_hash) <> 32;
	SELECT RAISE(ABORT, 'olivares: decision head must name the matching decision in the same aggregate')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_work_decision d WHERE d.id = NEW.current_decision_id
		AND d.tenant_id = NEW.tenant_id AND d.workspace_id = NEW.workspace_id
		AND d.work_item_id = NEW.work_item_id AND d.decision_key = NEW.decision_key
		AND d.decision_seq = NEW.current_seq AND d.decision_hash = NEW.head_hash
		AND ((d.operation = 'revoke' AND NEW.state = 'revoked')
			OR (d.operation <> 'revoke' AND NEW.state = 'effective')));
END;
