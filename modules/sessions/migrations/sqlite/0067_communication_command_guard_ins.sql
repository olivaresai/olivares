-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_communication_command_guard_ins
BEFORE INSERT ON sessions_communication_command
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: invalid sessions communication entity identity')
	WHERE NEW.id IS NULL
		OR NEW.id NOT GLOB '????????-????-7???-[89ab]???-????????????'
		OR length(replace(NEW.id,'-','')) <> 32
		OR replace(NEW.id,'-','') GLOB '*[^0-9a-f]*'
		OR NEW.tenant_id IS NULL
		OR NEW.tenant_id NOT GLOB '????????-????-7???-[89ab]???-????????????'
		OR length(replace(NEW.tenant_id,'-','')) <> 32
		OR replace(NEW.tenant_id,'-','') GLOB '*[^0-9a-f]*'
		OR NEW.tenant_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff'
		OR NEW.workspace_id IS NULL
		OR NEW.workspace_id NOT GLOB '????????-????-7???-[89ab]???-????????????'
		OR length(replace(NEW.workspace_id,'-','')) <> 32
		OR replace(NEW.workspace_id,'-','') GLOB '*[^0-9a-f]*';
	SELECT RAISE(ABORT, 'olivares: invalid CommunicationCommand receipt')
	WHERE NEW.version <> 1
		OR NEW.command_id IS NULL
		OR NEW.command_id NOT GLOB '????????-????-7???-[89ab]???-????????????'
		OR length(replace(NEW.command_id,'-','')) <> 32
		OR replace(NEW.command_id,'-','') GLOB '*[^0-9a-f]*'
		OR (NEW.result_id IS NOT NULL AND
			(NEW.result_id NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.result_id,'-','')) <> 32
				OR replace(NEW.result_id,'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.event_id IS NOT NULL AND
			(NEW.event_id NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.event_id,'-','')) <> 32
				OR replace(NEW.event_id,'-','') GLOB '*[^0-9a-f]*'))
		OR length(NEW.actor_fingerprint) <> 32
		OR length(CAST(NEW.command_scope AS BLOB)) NOT BETWEEN 1 AND 512
		OR trim(NEW.command_scope) <> NEW.command_scope
		OR instr(NEW.command_scope,char(0)) > 0 OR instr(NEW.command_scope,char(10)) > 0
		OR instr(NEW.command_scope,char(13)) > 0
		OR length(NEW.idempotency_key_hash) <> 32 OR length(NEW.request_digest) <> 32
		OR (NEW.seal_key_version IS NULL) IS NOT (NEW.digest_key_version IS NULL)
		OR (NEW.seal_key_version IS NOT NULL AND
			(length(CAST(NEW.seal_key_version AS BLOB)) NOT BETWEEN 1 AND 512
				OR trim(NEW.seal_key_version) <> NEW.seal_key_version
				OR instr(NEW.seal_key_version,char(0)) > 0
				OR instr(NEW.seal_key_version,char(10)) > 0
				OR instr(NEW.seal_key_version,char(13)) > 0))
		OR (NEW.digest_key_version IS NOT NULL AND
			(length(CAST(NEW.digest_key_version AS BLOB)) NOT BETWEEN 1 AND 512
				OR trim(NEW.digest_key_version) <> NEW.digest_key_version
				OR instr(NEW.digest_key_version,char(0)) > 0
				OR instr(NEW.digest_key_version,char(10)) > 0
				OR instr(NEW.digest_key_version,char(13)) > 0))
		OR length(NEW.plan_hash) <> 32
		OR length(CAST(NEW.result_kind AS BLOB)) NOT BETWEEN 1 AND 512
		OR trim(NEW.result_kind) <> NEW.result_kind
		OR instr(NEW.result_kind,char(0)) > 0 OR instr(NEW.result_kind,char(10)) > 0
		OR instr(NEW.result_kind,char(13)) > 0
		OR NEW.http_status NOT BETWEEN 100 AND 599
		OR NOT json_valid(NEW.response_projection_json)
		OR json_type(NEW.response_projection_json) <> 'object'
		OR length(CAST(NEW.response_projection_json AS BLOB)) > 4096
		OR EXISTS (SELECT 1 FROM json_each(NEW.response_projection_json)
			WHERE key NOT IN ('ids','version','state','counts','digests'))
		OR (json_type(NEW.response_projection_json,'$.ids') IS NOT NULL
			AND json_type(NEW.response_projection_json,'$.ids') NOT IN ('object','null'))
		OR (json_type(NEW.response_projection_json,'$.counts') IS NOT NULL
			AND json_type(NEW.response_projection_json,'$.counts') NOT IN ('object','null'))
		OR (json_type(NEW.response_projection_json,'$.digests') IS NOT NULL
			AND json_type(NEW.response_projection_json,'$.digests') NOT IN ('object','null'))
		OR (json_type(NEW.response_projection_json,'$.version') IS NOT NULL
			AND json_type(NEW.response_projection_json,'$.version') NOT IN ('integer','null'))
		OR (json_type(NEW.response_projection_json,'$.version') = 'integer'
			AND json_extract(NEW.response_projection_json,'$.version') < 0)
		OR (json_type(NEW.response_projection_json,'$.state') IS NOT NULL
			AND json_type(NEW.response_projection_json,'$.state') NOT IN ('text','null'))
		OR (json_type(NEW.response_projection_json,'$.state') = 'text'
			AND json_extract(NEW.response_projection_json,'$.state') NOT IN (
				'', 'active','archived','revoked','expired','paused','disabled','stale',
				'draft','published','retracted','discarded','available','acknowledged',
				'undeliverable','pending','accepted','blocked','resolved','rejected','canceled',
				'offered','withdrawn','in_flight','succeeded','failed','unknown','dead_letter',
				'superseded'))
		OR ((SELECT count(*) FROM json_each(NEW.response_projection_json,'$.ids'))
			+ (SELECT count(*) FROM json_each(NEW.response_projection_json,'$.counts'))
			+ (SELECT count(*) FROM json_each(NEW.response_projection_json,'$.digests'))) > 32
		OR EXISTS (SELECT 1 FROM json_each(NEW.response_projection_json,'$.ids') entry
			WHERE entry.key NOT IN (
				'channel_id','message_id','delivery_id','ack_id','request_id','response_id',
				'handoff_id','dispatch_id','attempt_id','result_id','work_item_id','event_id')
				OR entry.type <> 'text' OR length(entry.value) <> 36
				OR substr(entry.value,9,1) <> '-' OR substr(entry.value,14,1) <> '-'
				OR substr(entry.value,15,1) <> '7' OR substr(entry.value,19,1) <> '-'
				OR substr(entry.value,20,1) NOT IN ('8','9','a','b')
				OR substr(entry.value,24,1) <> '-'
				OR length(replace(entry.value,'-','')) <> 32
				OR replace(entry.value,'-','') GLOB '*[^0-9a-f]*')
		OR EXISTS (SELECT 1 FROM json_each(NEW.response_projection_json,'$.counts') entry
			WHERE entry.key NOT IN (
				'required','acknowledged','viable','unmet','quorum','resolved_count',
				'delivery_count')
				OR entry.type <> 'integer' OR entry.value < 0)
		OR EXISTS (SELECT 1 FROM json_each(NEW.response_projection_json,'$.digests') entry
			WHERE entry.key NOT IN (
				'request','plan','response','audience','route_reasons','contributions','payload')
				OR entry.type <> 'text' OR length(entry.value) <> 44
				OR substr(entry.value,44,1) <> '='
				OR substr(entry.value,1,43) GLOB '*[^A-Za-z0-9+/]*')
		OR length(NEW.response_digest) <> 32 OR NEW.audit_seq < 0
		OR (NEW.audit_seq = 0) IS NOT (NEW.audit_hash IS NULL)
		OR (NEW.audit_seq > 0 AND length(NEW.audit_hash) <> 32)
		OR NEW.completed_at <> NEW.created_at;
	SELECT RAISE(ABORT, 'olivares: CommunicationCommand Event crosses tenant/workspace')
	WHERE NEW.event_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM sessions_work_event e
		WHERE e.event_id = NEW.event_id AND e.tenant_id = NEW.tenant_id
			AND e.workspace_id = NEW.workspace_id);
END;
