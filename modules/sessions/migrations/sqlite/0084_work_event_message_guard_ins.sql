-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_event_guard_ins
BEFORE INSERT ON sessions_work_event
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: invalid sessions communication event vocabulary, payload or evidence hash')
	WHERE NEW.aggregate_kind NOT IN ('sessions.work_item','sessions.message')
		OR NEW.seq < 1
		OR length(NEW.event_type) NOT BETWEEN 1 AND 128 OR NEW.event_type GLOB '*[^a-z0-9._-]*'
		OR NEW.actor_kind NOT IN ('user','agent','session','system')
		OR length(CAST(NEW.actor_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR instr(NEW.actor_ref, char(0)) > 0 OR instr(NEW.actor_ref, char(10)) > 0 OR instr(NEW.actor_ref, char(13)) > 0
		OR length(CAST(NEW.payload_json AS BLOB)) > 16384
		OR NOT json_valid(NEW.payload_json) OR json_type(NEW.payload_json) <> 'object'
		OR length(NEW.payload_hash) <> 32 OR NEW.audit_seq < 1 OR length(NEW.audit_hash) <> 32;
	SELECT RAISE(ABORT, 'olivares: work-item event aggregate must resolve in the same tenant/workspace')
	WHERE NEW.aggregate_kind = 'sessions.work_item' AND NOT EXISTS (
		SELECT 1 FROM sessions_work_item w
		WHERE w.id = NEW.aggregate_id AND w.tenant_id = NEW.tenant_id
			AND w.workspace_id = NEW.workspace_id);
	SELECT RAISE(ABORT, 'olivares: message event aggregate must be standalone in the same tenant/workspace')
	WHERE NEW.aggregate_kind = 'sessions.message' AND NOT EXISTS (
		SELECT 1 FROM sessions_message m
		WHERE m.id = NEW.aggregate_id AND m.tenant_id = NEW.tenant_id
			AND m.workspace_id = NEW.workspace_id AND m.work_item_id IS NULL);
END;
