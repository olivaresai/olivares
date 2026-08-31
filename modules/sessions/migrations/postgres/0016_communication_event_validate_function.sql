-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE OR REPLACE FUNCTION olivares_sessions_communication_event_validate()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
BEGIN
	IF TG_OP = 'UPDATE' THEN
		RAISE EXCEPTION 'olivares: sessions work events are immutable'
			USING ERRCODE = '23514';
	END IF;
	IF NEW.aggregate_kind NOT IN ('sessions.work_item','sessions.message')
		OR NEW.seq < 1
		OR NEW.event_type !~ '^[a-z0-9._-]{1,128}$'
		OR NEW.actor_kind NOT IN ('user','agent','session','system')
		OR octet_length(NEW.actor_ref) NOT BETWEEN 1 AND 512
		OR NEW.actor_ref ~ E'[\r\n]'
		OR octet_length(NEW.payload_json::text) > 16384
		OR jsonb_typeof(NEW.payload_json::jsonb) <> 'object'
		OR octet_length(NEW.payload_hash) <> 32
		OR NEW.audit_seq < 1 OR octet_length(NEW.audit_hash) <> 32 THEN
		RAISE EXCEPTION 'olivares: invalid sessions communication event vocabulary, payload or evidence hash'
			USING ERRCODE = '23514';
	END IF;
	IF NEW.aggregate_kind = 'sessions.work_item' AND NOT EXISTS (
		SELECT 1 FROM sessions_work_item w
		WHERE w.id = NEW.aggregate_id AND w.tenant_id = NEW.tenant_id
			AND w.workspace_id = NEW.workspace_id) THEN
		RAISE EXCEPTION 'olivares: work-item event aggregate crosses tenant/workspace'
			USING ERRCODE = '23514';
	END IF;
	IF NEW.aggregate_kind = 'sessions.message' AND NOT EXISTS (
		SELECT 1 FROM sessions_message m
		WHERE m.id = NEW.aggregate_id AND m.tenant_id = NEW.tenant_id
			AND m.workspace_id = NEW.workspace_id AND m.work_item_id IS NULL) THEN
		RAISE EXCEPTION 'olivares: message event aggregate is not standalone in the same tenant/workspace'
			USING ERRCODE = '23514';
	END IF;
	RETURN NEW;
END;
$fn$;
