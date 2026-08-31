-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_lease_guard_ins
BEFORE INSERT ON sessions_work_lease
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: invalid sessions work lease identity, vocabulary or lineage')
	WHERE NEW.fence < 0 OR NEW.renewal_count < 0
		OR NEW.state NOT IN ('vacant','active','released','expired','revoked')
		OR (NEW.holder_run_ref IS NOT NULL AND length(CAST(NEW.holder_run_ref AS BLOB)) NOT BETWEEN 1 AND 512)
		OR (NEW.holder_agent_ref IS NOT NULL AND length(CAST(NEW.holder_agent_ref AS BLOB)) NOT BETWEEN 1 AND 512)
		OR (NEW.end_reason IS NOT NULL AND length(CAST(NEW.end_reason AS BLOB)) NOT BETWEEN 1 AND 2048)
		OR NOT EXISTS (SELECT 1 FROM sessions_work_item i
			WHERE i.id = NEW.work_item_id AND i.tenant_id = NEW.tenant_id
				AND i.workspace_id = NEW.workspace_id);
	SELECT RAISE(ABORT, 'olivares: vacant sessions work lease carries authority')
	WHERE NEW.state = 'vacant' AND (NEW.fence <> 0 OR NEW.holder_sid IS NOT NULL
		OR NEW.holder_run_ref IS NOT NULL OR NEW.holder_agent_ref IS NOT NULL
		OR NEW.acquired_at IS NOT NULL OR NEW.renewed_at IS NOT NULL OR NEW.expires_at IS NOT NULL
		OR NEW.ended_at IS NOT NULL OR NEW.end_reason IS NOT NULL OR NEW.renewal_count <> 0);
	SELECT RAISE(ABORT, 'olivares: materialized sessions work lease lacks holder or timing')
	WHERE NEW.state <> 'vacant' AND (NEW.holder_sid IS NULL OR length(NEW.holder_sid) <> 40
		OR substr(NEW.holder_sid, 1, 4) <> 'osn_' OR NEW.acquired_at IS NULL OR NEW.expires_at IS NULL);
	SELECT RAISE(ABORT, 'olivares: active sessions work lease has invalid lifetime')
	WHERE NEW.state = 'active' AND (NEW.ended_at IS NOT NULL OR NEW.end_reason IS NOT NULL
		OR NEW.expires_at <= NEW.acquired_at);
	SELECT RAISE(ABORT, 'olivares: ended sessions work lease lacks end evidence')
	WHERE NEW.state IN ('released','expired','revoked') AND
		(NEW.ended_at IS NULL OR (NEW.state IN ('expired','revoked') AND NEW.end_reason IS NULL));
END;
