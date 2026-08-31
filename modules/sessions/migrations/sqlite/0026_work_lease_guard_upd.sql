-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_lease_guard_upd
BEFORE UPDATE ON sessions_work_lease
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: sessions work lease lineage is immutable')
	WHERE NEW.tenant_id IS NOT OLD.tenant_id OR NEW.workspace_id IS NOT OLD.workspace_id
		OR NEW.work_item_id IS NOT OLD.work_item_id;
	SELECT RAISE(ABORT, 'olivares: invalid sessions work lease identity or vocabulary')
	WHERE NEW.fence < 0 OR NEW.renewal_count < 0
		OR NEW.state NOT IN ('vacant','active','released','expired','revoked')
		OR (NEW.holder_run_ref IS NOT NULL AND length(CAST(NEW.holder_run_ref AS BLOB)) NOT BETWEEN 1 AND 512)
		OR (NEW.holder_agent_ref IS NOT NULL AND length(CAST(NEW.holder_agent_ref AS BLOB)) NOT BETWEEN 1 AND 512)
		OR (NEW.end_reason IS NOT NULL AND length(CAST(NEW.end_reason AS BLOB)) NOT BETWEEN 1 AND 2048);
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
	SELECT RAISE(ABORT, 'olivares: sessions work lease renewal changed authority or omitted evidence')
	WHERE OLD.state = 'active' AND NEW.state = 'active'
		AND NEW.holder_sid IS OLD.holder_sid AND NEW.holder_run_ref IS OLD.holder_run_ref
		AND NEW.holder_agent_ref IS OLD.holder_agent_ref
		AND (NEW.fence <> OLD.fence OR NEW.renewal_count <> OLD.renewal_count + 1
			OR NEW.renewed_at IS NULL OR NEW.renewed_at IS OLD.renewed_at);
	SELECT RAISE(ABORT, 'olivares: sessions work lease end did not invalidate its fence')
	WHERE OLD.state = 'active' AND NEW.state IN ('released','expired','revoked')
		AND (NEW.fence <> OLD.fence + 1 OR NEW.renewal_count <> OLD.renewal_count);
	SELECT RAISE(ABORT, 'olivares: sessions work lease acquisition did not mint one fence')
	WHERE NEW.state = 'active' AND (OLD.state <> 'active' OR NEW.holder_sid IS NOT OLD.holder_sid
		OR NEW.holder_run_ref IS NOT OLD.holder_run_ref OR NEW.holder_agent_ref IS NOT OLD.holder_agent_ref)
		AND (NEW.fence <> OLD.fence + 1 OR NEW.renewal_count <> 0);
	SELECT RAISE(ABORT, 'olivares: illegal sessions work lease transition')
	WHERE NOT ((OLD.state = 'active' AND NEW.state = 'active'
			AND NEW.holder_sid IS OLD.holder_sid AND NEW.holder_run_ref IS OLD.holder_run_ref
			AND NEW.holder_agent_ref IS OLD.holder_agent_ref)
		OR (OLD.state = 'active' AND NEW.state IN ('released','expired','revoked'))
		OR (NEW.state = 'active' AND (OLD.state <> 'active' OR NEW.holder_sid IS NOT OLD.holder_sid
			OR NEW.holder_run_ref IS NOT OLD.holder_run_ref OR NEW.holder_agent_ref IS NOT OLD.holder_agent_ref)));
END;
