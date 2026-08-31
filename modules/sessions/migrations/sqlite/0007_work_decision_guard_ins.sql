-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_decision_guard_ins
BEFORE INSERT ON sessions_work_decision
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: invalid sessions work decision vocabulary, size or hash')
	WHERE length(NEW.decision_key) NOT BETWEEN 1 AND 128
		OR NEW.decision_key GLOB '*[^a-z0-9._-]*' OR NEW.decision_seq < 1
		OR length(NEW.subject_kind) NOT BETWEEN 1 AND 64 OR NEW.subject_kind GLOB '*[^a-z0-9._-]*'
		OR length(CAST(NEW.subject_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR NEW.operation NOT IN ('set','supersede','revoke')
		OR length(CAST(NEW.statement_md AS BLOB)) NOT BETWEEN 1 AND 16384
		OR length(CAST(NEW.rationale_md AS BLOB)) NOT BETWEEN 1 AND 16384
		OR NEW.decided_by_kind NOT IN ('user','agent','system')
		OR length(CAST(NEW.decided_by_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR length(CAST(NEW.authority_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR length(NEW.decision_hash) <> 32;
	SELECT RAISE(ABORT, 'olivares: decision parent must exist in the same tenant/workspace')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_work_item w WHERE w.id = NEW.work_item_id
		AND w.tenant_id = NEW.tenant_id AND w.workspace_id = NEW.workspace_id);
	SELECT RAISE(ABORT, 'olivares: set decision is not the first decision or a post-revoke successor')
	WHERE NEW.operation = 'set' AND (NEW.supersedes_id IS NOT NULL OR NEW.revokes_id IS NOT NULL OR NOT (
		(NEW.decision_seq = 1 AND NOT EXISTS (
			SELECT 1 FROM sessions_work_decision_head h WHERE h.tenant_id = NEW.tenant_id
				AND h.workspace_id = NEW.workspace_id AND h.work_item_id = NEW.work_item_id
				AND h.decision_key = NEW.decision_key))
		OR (NEW.decision_seq > 1 AND EXISTS (
			SELECT 1 FROM sessions_work_decision_head h WHERE h.tenant_id = NEW.tenant_id
				AND h.workspace_id = NEW.workspace_id AND h.work_item_id = NEW.work_item_id
				AND h.decision_key = NEW.decision_key AND h.state = 'revoked'
				AND h.current_seq = NEW.decision_seq - 1))));
	SELECT RAISE(ABORT, 'olivares: supersede must name the current effective decision')
	WHERE NEW.operation = 'supersede' AND (NEW.supersedes_id IS NULL OR NEW.revokes_id IS NOT NULL
		OR NOT EXISTS (SELECT 1 FROM sessions_work_decision_head h WHERE h.tenant_id = NEW.tenant_id
			AND h.workspace_id = NEW.workspace_id AND h.work_item_id = NEW.work_item_id
			AND h.decision_key = NEW.decision_key AND h.state = 'effective'
			AND h.current_decision_id = NEW.supersedes_id AND h.current_seq = NEW.decision_seq - 1));
	SELECT RAISE(ABORT, 'olivares: revoke must name the current effective decision')
	WHERE NEW.operation = 'revoke' AND (NEW.revokes_id IS NULL OR NEW.supersedes_id IS NOT NULL
		OR NOT EXISTS (SELECT 1 FROM sessions_work_decision_head h WHERE h.tenant_id = NEW.tenant_id
			AND h.workspace_id = NEW.workspace_id AND h.work_item_id = NEW.work_item_id
			AND h.decision_key = NEW.decision_key AND h.state = 'effective'
			AND h.current_decision_id = NEW.revokes_id AND h.current_seq = NEW.decision_seq - 1));
END;
