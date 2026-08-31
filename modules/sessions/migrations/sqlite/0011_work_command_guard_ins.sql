-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_command_guard_ins
BEFORE INSERT ON sessions_work_command
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: invalid sessions work command receipt')
	WHERE length(NEW.actor_fingerprint) <> 32
		OR length(CAST(NEW.command_scope AS BLOB)) NOT BETWEEN 1 AND 512
		OR length(NEW.idempotency_key_hash) <> 32 OR length(NEW.request_hash) <> 32
		OR length(NEW.plan_hash) <> 32
		OR length(NEW.result_kind) NOT BETWEEN 1 AND 128 OR NEW.result_kind GLOB '*[^a-z0-9._-]*'
		OR NEW.http_status NOT BETWEEN 100 AND 599
		OR length(CAST(NEW.response_json AS BLOB)) > 16384
		OR NOT json_valid(NEW.response_json) OR json_type(NEW.response_json) <> 'object'
		OR length(NEW.response_hash) <> 32 OR NEW.audit_seq < 1 OR length(NEW.audit_hash) <> 32;
	SELECT RAISE(ABORT, 'olivares: command result must resolve in the same tenant/workspace')
	WHERE NEW.result_kind = 'sessions.work_item' AND (NEW.result_id IS NULL OR NOT EXISTS (
		SELECT 1 FROM sessions_work_item w WHERE w.id = NEW.result_id
			AND w.tenant_id = NEW.tenant_id AND w.workspace_id = NEW.workspace_id));
END;
