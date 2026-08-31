-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- Move only CommunicationCommand to a versioned validator. The store reserves
-- this function identity and its exact ACL before this statement runs; CREATE OR
-- REPLACE preserves both the reserved OID and that ACL. The other communication
-- triggers deliberately remain attached to the immutable shared validator.
DO $migration$
BEGIN
	EXECUTE $function_definition$
CREATE OR REPLACE FUNCTION public.olivares_sessions_communication_command_validate_v18()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $command_guard$
DECLARE
	projection_json pg_catalog.json;
	projection_jsonb pg_catalog.jsonb;
	cursor_json pg_catalog.json;
	canonical_cursor_projection pg_catalog.text;
	has_barrier_id boolean;
	has_barrier_reason boolean;
BEGIN
	IF TG_TABLE_SCHEMA <> 'public'
		OR TG_TABLE_NAME <> 'sessions_communication_command'
		OR TG_OP NOT IN ('INSERT', 'UPDATE') THEN
		RAISE EXCEPTION 'olivares: command validator attached outside its exact table/operation'
			USING ERRCODE = '23514';
	END IF;

	IF NEW.id IS NULL
		OR NEW.id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR NEW.tenant_id IS NULL
		OR NEW.tenant_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR NEW.tenant_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff'
		OR NEW.workspace_id IS NULL
		OR NEW.workspace_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR NEW.version < 1 OR NEW.created_at IS NULL THEN
		RAISE EXCEPTION 'olivares: invalid sessions communication entity identity'
			USING ERRCODE = '23514';
	END IF;
	IF NEW.version <> 1 THEN
		RAISE EXCEPTION 'olivares: append-only communication version must be one'
			USING ERRCODE = '23514';
	END IF;
	IF TG_OP = 'UPDATE' THEN
		RAISE EXCEPTION 'olivares: append-only communication row is immutable'
			USING ERRCODE = '23514';
	END IF;

	BEGIN
		projection_json := NEW.response_projection_json::pg_catalog.json;
		projection_jsonb := NEW.response_projection_json::pg_catalog.jsonb;
	EXCEPTION
		WHEN invalid_text_representation OR numeric_value_out_of_range THEN
			RAISE EXCEPTION 'olivares: invalid CommunicationCommand receipt'
				USING ERRCODE = '23514';
	END;
	IF pg_catalog.json_typeof(projection_json) IS DISTINCT FROM 'object'
		OR pg_catalog.octet_length(NEW.response_projection_json::pg_catalog.text) > 4096 THEN
		RAISE EXCEPTION 'olivares: invalid CommunicationCommand receipt'
			USING ERRCODE = '23514';
	END IF;

	IF NEW.command_id IS NULL OR NEW.command_id !~
			'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR (NEW.result_id IS NOT NULL AND NEW.result_id !~
			'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
		OR (NEW.event_id IS NOT NULL AND NEW.event_id !~
			'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
		OR pg_catalog.octet_length(NEW.actor_fingerprint) <> 32
		OR pg_catalog.octet_length(NEW.command_scope) NOT BETWEEN 1 AND 512
		OR NEW.command_scope <> pg_catalog.btrim(NEW.command_scope)
		OR NEW.command_scope ~ E'[\\r\\n]'
		OR pg_catalog.octet_length(NEW.idempotency_key_hash) <> 32
		OR pg_catalog.octet_length(NEW.request_digest) <> 32
		OR (NEW.seal_key_version IS NULL) <> (NEW.digest_key_version IS NULL)
		OR (NEW.seal_key_version IS NOT NULL AND
			(pg_catalog.octet_length(NEW.seal_key_version) NOT BETWEEN 1 AND 512
				OR NEW.seal_key_version <> pg_catalog.btrim(NEW.seal_key_version)
				OR NEW.seal_key_version ~ E'[\\r\\n]'))
		OR (NEW.digest_key_version IS NOT NULL AND
			(pg_catalog.octet_length(NEW.digest_key_version) NOT BETWEEN 1 AND 512
				OR NEW.digest_key_version <> pg_catalog.btrim(NEW.digest_key_version)
				OR NEW.digest_key_version ~ E'[\\r\\n]'))
		OR pg_catalog.octet_length(NEW.plan_hash) <> 32
		OR pg_catalog.octet_length(NEW.result_kind) NOT BETWEEN 1 AND 512
		OR NEW.result_kind <> pg_catalog.btrim(NEW.result_kind)
		OR NEW.result_kind ~ E'[\\r\\n]'
		OR NEW.http_status NOT BETWEEN 100 AND 599
		OR EXISTS (
			SELECT 1 FROM pg_catalog.json_each(projection_json) AS entry(key,value)
			WHERE entry.key NOT IN ('ids','version','state','counts','digests','inbox_cursor'))
		OR (projection_jsonb ? 'ids' AND projection_jsonb -> 'ids' <> 'null'::pg_catalog.jsonb
			AND pg_catalog.jsonb_typeof(projection_jsonb -> 'ids') <> 'object')
		OR (projection_jsonb ? 'counts' AND projection_jsonb -> 'counts' <> 'null'::pg_catalog.jsonb
			AND pg_catalog.jsonb_typeof(projection_jsonb -> 'counts') <> 'object')
		OR (projection_jsonb ? 'digests' AND projection_jsonb -> 'digests' <> 'null'::pg_catalog.jsonb
			AND pg_catalog.jsonb_typeof(projection_jsonb -> 'digests') <> 'object')
		OR (projection_jsonb ? 'version' AND projection_jsonb -> 'version' <> 'null'::pg_catalog.jsonb
			AND (pg_catalog.jsonb_typeof(projection_jsonb -> 'version') <> 'number'
				OR projection_jsonb ->> 'version' !~ '^(0|[1-9][0-9]{0,18})$'
				OR (pg_catalog.length(projection_jsonb ->> 'version') = 19
					AND projection_jsonb ->> 'version' > '9223372036854775807')))
		OR (projection_jsonb ? 'state' AND projection_jsonb -> 'state' <> 'null'::pg_catalog.jsonb
			AND (pg_catalog.jsonb_typeof(projection_jsonb -> 'state') <> 'string'
				OR projection_jsonb ->> 'state' NOT IN (
					'', 'active','archived','revoked','expired','paused','disabled','stale',
					'draft','published','retracted','discarded','available','acknowledged',
					'undeliverable','pending','accepted','blocked','resolved','rejected',
					'canceled','offered','withdrawn','in_flight','succeeded','failed',
					'unknown','dead_letter','superseded')))
		OR ((SELECT pg_catalog.count(*) FROM pg_catalog.jsonb_object_keys(CASE
				WHEN pg_catalog.jsonb_typeof(projection_jsonb -> 'ids') = 'object'
				THEN projection_jsonb -> 'ids' ELSE '{}'::pg_catalog.jsonb END))
			+ (SELECT pg_catalog.count(*) FROM pg_catalog.jsonb_object_keys(CASE
				WHEN pg_catalog.jsonb_typeof(projection_jsonb -> 'counts') = 'object'
				THEN projection_jsonb -> 'counts' ELSE '{}'::pg_catalog.jsonb END))
			+ (SELECT pg_catalog.count(*) FROM pg_catalog.jsonb_object_keys(CASE
				WHEN pg_catalog.jsonb_typeof(projection_jsonb -> 'digests') = 'object'
				THEN projection_jsonb -> 'digests' ELSE '{}'::pg_catalog.jsonb END))) > 32
		OR EXISTS (
			SELECT 1 FROM pg_catalog.jsonb_each(CASE
				WHEN pg_catalog.jsonb_typeof(projection_jsonb -> 'ids') = 'object'
				THEN projection_jsonb -> 'ids' ELSE '{}'::pg_catalog.jsonb END)
				AS entry(key,value)
			WHERE entry.key NOT IN (
				'channel_id','message_id','delivery_id','ack_id','request_id','response_id',
				'handoff_id','dispatch_id','attempt_id','result_id','work_item_id','event_id')
				OR pg_catalog.jsonb_typeof(entry.value) <> 'string'
				OR entry.value #>> '{}' !~
					'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
		OR EXISTS (
			SELECT 1 FROM pg_catalog.jsonb_each(CASE
				WHEN pg_catalog.jsonb_typeof(projection_jsonb -> 'counts') = 'object'
				THEN projection_jsonb -> 'counts' ELSE '{}'::pg_catalog.jsonb END)
				AS entry(key,value)
			WHERE entry.key NOT IN (
				'required','acknowledged','viable','unmet','quorum','resolved_count',
				'delivery_count')
				OR pg_catalog.jsonb_typeof(entry.value) <> 'number'
				OR entry.value #>> '{}' !~ '^(0|[1-9][0-9]{0,18})$'
				OR (pg_catalog.length(entry.value #>> '{}') = 19
					AND entry.value #>> '{}' > '9223372036854775807'))
		OR EXISTS (
			SELECT 1 FROM pg_catalog.jsonb_each(CASE
				WHEN pg_catalog.jsonb_typeof(projection_jsonb -> 'digests') = 'object'
				THEN projection_jsonb -> 'digests' ELSE '{}'::pg_catalog.jsonb END)
				AS entry(key,value)
			WHERE entry.key NOT IN (
				'request','plan','response','audience','route_reasons','contributions','payload')
				OR pg_catalog.jsonb_typeof(entry.value) <> 'string'
				OR entry.value #>> '{}' !~ '^[A-Za-z0-9+/]{43}=$')
		OR pg_catalog.octet_length(NEW.response_digest) <> 32 OR NEW.audit_seq < 0
		OR (NEW.audit_seq = 0) <> (NEW.audit_hash IS NULL)
		OR (NEW.audit_seq > 0 AND pg_catalog.octet_length(NEW.audit_hash) <> 32)
		OR NEW.completed_at <> NEW.created_at THEN
		RAISE EXCEPTION 'olivares: invalid CommunicationCommand receipt'
			USING ERRCODE = '23514';
	END IF;

	IF NEW.result_kind = 'sessions.inbox_cursor' THEN
		IF (SELECT pg_catalog.count(*) FROM pg_catalog.json_each(projection_json)) <> 2
			OR EXISTS (SELECT 1 FROM pg_catalog.json_each(projection_json) AS entry(key,value)
				GROUP BY entry.key HAVING pg_catalog.count(*) <> 1)
			OR pg_catalog.json_typeof(projection_json -> 'version') IS DISTINCT FROM 'number'
			OR projection_json ->> 'version' !~ '^[1-9][0-9]{0,18}$'
			OR (pg_catalog.length(projection_json ->> 'version') = 19
				AND projection_json ->> 'version' > '9223372036854775807')
			OR NEW.result_id IS NULL OR NEW.http_status <> 200 OR NEW.event_id IS NOT NULL THEN
			RAISE EXCEPTION 'olivares: invalid CommunicationCommand receipt'
				USING ERRCODE = '23514';
		END IF;

		SELECT entry.value INTO cursor_json
		FROM pg_catalog.json_each(projection_json) AS entry(key,value)
		WHERE entry.key = 'inbox_cursor';
		IF pg_catalog.json_typeof(cursor_json) IS DISTINCT FROM 'object' THEN
			RAISE EXCEPTION 'olivares: invalid CommunicationCommand receipt'
				USING ERRCODE = '23514';
		END IF;
		IF (SELECT pg_catalog.count(*) FROM pg_catalog.json_each(cursor_json)) NOT IN (1,3)
			OR EXISTS (SELECT 1 FROM pg_catalog.json_each(cursor_json) AS entry(key,value)
				WHERE entry.key NOT IN ('last_seen_seq','barrier_delivery_id','barrier_reason'))
			OR EXISTS (SELECT 1 FROM pg_catalog.json_each(cursor_json) AS entry(key,value)
				GROUP BY entry.key HAVING pg_catalog.count(*) <> 1)
			OR pg_catalog.json_typeof(cursor_json -> 'last_seen_seq') IS DISTINCT FROM 'number'
			OR cursor_json ->> 'last_seen_seq' !~ '^(0|[1-9][0-9]{0,18})$'
			OR (pg_catalog.length(cursor_json ->> 'last_seen_seq') = 19
				AND cursor_json ->> 'last_seen_seq' > '9223372036854775807') THEN
			RAISE EXCEPTION 'olivares: invalid CommunicationCommand receipt'
				USING ERRCODE = '23514';
		END IF;

		has_barrier_id := EXISTS (
			SELECT 1 FROM pg_catalog.json_each(cursor_json) AS entry(key,value)
			WHERE entry.key = 'barrier_delivery_id');
		has_barrier_reason := EXISTS (
			SELECT 1 FROM pg_catalog.json_each(cursor_json) AS entry(key,value)
			WHERE entry.key = 'barrier_reason');
		IF has_barrier_id <> has_barrier_reason
			OR (has_barrier_id AND (
				pg_catalog.json_typeof(cursor_json -> 'barrier_delivery_id') <> 'string'
				OR cursor_json ->> 'barrier_delivery_id' !~
					'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
				OR pg_catalog.json_typeof(cursor_json -> 'barrier_reason') <> 'string'
				OR cursor_json ->> 'barrier_reason' NOT IN (
					'not_yet_available','temporarily_invisible'))) THEN
			RAISE EXCEPTION 'olivares: invalid CommunicationCommand receipt'
				USING ERRCODE = '23514';
		END IF;
		IF has_barrier_id THEN
			canonical_cursor_projection :=
				'{"inbox_cursor":{"barrier_delivery_id":"' ||
				(cursor_json ->> 'barrier_delivery_id') || '","barrier_reason":"' ||
				(cursor_json ->> 'barrier_reason') || '","last_seen_seq":' ||
				(cursor_json ->> 'last_seen_seq') || '},"version":' ||
				(projection_json ->> 'version') || '}';
		ELSE
			canonical_cursor_projection := '{"inbox_cursor":{"last_seen_seq":' ||
				(cursor_json ->> 'last_seen_seq') || '},"version":' ||
				(projection_json ->> 'version') || '}';
		END IF;
		IF (NEW.response_projection_json COLLATE pg_catalog."C") IS DISTINCT FROM
			(canonical_cursor_projection COLLATE pg_catalog."C") THEN
			RAISE EXCEPTION 'olivares: invalid CommunicationCommand receipt'
				USING ERRCODE = '23514';
		END IF;
	ELSIF EXISTS (
		SELECT 1 FROM pg_catalog.json_each(projection_json) AS entry(key,value)
		WHERE entry.key = 'inbox_cursor') THEN
		RAISE EXCEPTION 'olivares: invalid CommunicationCommand receipt'
			USING ERRCODE = '23514';
	END IF;

	IF NEW.event_id IS NOT NULL AND NOT EXISTS (
		SELECT 1 FROM public.sessions_work_event e WHERE e.event_id = NEW.event_id
			AND e.tenant_id = NEW.tenant_id AND e.workspace_id = NEW.workspace_id) THEN
		RAISE EXCEPTION 'olivares: CommunicationCommand Event crosses tenant/workspace'
			USING ERRCODE = '23514';
	END IF;
	RETURN NEW;
END;
$command_guard$;
$function_definition$;

	EXECUTE $drop_trigger$
DROP TRIGGER sessions_communication_command_guard ON public.sessions_communication_command
$drop_trigger$;
	EXECUTE $create_trigger$
CREATE TRIGGER sessions_communication_command_guard
BEFORE INSERT OR UPDATE ON public.sessions_communication_command
FOR EACH ROW EXECUTE FUNCTION public.olivares_sessions_communication_command_validate_v18()
$create_trigger$;
END
$migration$;
