-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_decision_guard_upd
BEFORE UPDATE ON sessions_work_decision
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: sessions work tenant/workspace lineage is immutable')
	WHERE NEW.tenant_id IS NOT OLD.tenant_id OR NEW.workspace_id IS NOT OLD.workspace_id;
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
	SELECT RAISE(ABORT, 'olivares: decision rows are immutable')
	WHERE OLD.id IS NEW.id;
END;
