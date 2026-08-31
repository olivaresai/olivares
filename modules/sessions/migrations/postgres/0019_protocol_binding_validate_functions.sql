-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

CREATE OR REPLACE FUNCTION olivares_sessions_protocol_binding_spec_validate()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $fn$
BEGIN
	IF TG_TABLE_SCHEMA <> 'public'
		OR TG_TABLE_NAME <> 'sessions_communication_binding_spec'
		OR TG_OP NOT IN ('INSERT', 'UPDATE') THEN
		RAISE EXCEPTION 'olivares: protocol binding spec validator attached outside its exact table/operation'
			USING ERRCODE = '23514';
	END IF;

	IF NEW.id IS NULL
		OR NEW.id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR NEW.tenant_id IS NULL
		OR NEW.tenant_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR NEW.tenant_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff'
		OR NEW.workspace_id IS NULL
		OR NEW.workspace_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR NEW.version < 1 OR NEW.created_at IS NULL OR NEW.updated_at IS NULL
		OR NEW.updated_at < NEW.created_at
		OR NEW.generation < 1
		OR NEW.protocol NOT IN ('a2a', 'mcp')
		OR NEW.direction NOT IN ('inbound', 'outbound', 'bidirectional')
		OR NEW.local_kind NOT IN ('work_item', 'agent', 'model', 'channel')
		OR NEW.currency_policy <> 'pinned'
		OR NEW.validation_verdict NOT IN ('CLEAN', 'BROKEN', 'UNKNOWN')
		OR (NEW.validation_verdict = 'CLEAN' AND NEW.validated_at IS NULL)
		OR NEW.state NOT IN ('draft', 'active', 'disabled', 'superseded')
		OR (NEW.state = 'active' AND NEW.active_slot IS DISTINCT FROM NEW.binding_key)
		OR (NEW.state <> 'active' AND NEW.active_slot IS NOT NULL)
		OR pg_catalog.octet_length(NEW.mapping_hash) <> 32
		OR pg_catalog.octet_length(NEW.losses_hash) <> 32
		OR pg_catalog.octet_length(NEW.spec_hash) <> 32
		OR pg_catalog.octet_length(NEW.plan_hash) <> 32
		OR pg_catalog.octet_length(NEW.command_key_hash) <> 32
		OR pg_catalog.octet_length(NEW.request_hash) <> 32
		OR pg_catalog.jsonb_typeof(NEW.local_selector_json::pg_catalog.jsonb) <> 'object'
		OR pg_catalog.jsonb_typeof(NEW.mapping_json::pg_catalog.jsonb) <> 'array'
		OR pg_catalog.jsonb_typeof(NEW.known_losses_json::pg_catalog.jsonb) NOT IN ('array', 'null')
		OR pg_catalog.jsonb_typeof(NEW.rule_refs_json::pg_catalog.jsonb) <> 'array' THEN
		RAISE EXCEPTION 'olivares: invalid ProtocolBindingSpec row'
			USING ERRCODE = '23514';
	END IF;

	IF TG_OP = 'INSERT' THEN
		IF NEW.version <> 1 OR NEW.state <> 'draft' OR NEW.active_slot IS NOT NULL THEN
			RAISE EXCEPTION 'olivares: ProtocolBindingSpec must begin as draft version one'
				USING ERRCODE = '23514';
		END IF;
		RETURN NEW;
	END IF;

	IF NEW.id IS DISTINCT FROM OLD.id
		OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
		OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
		OR NEW.created_at IS DISTINCT FROM OLD.created_at
		OR NEW.version <> OLD.version + 1
		OR NEW.updated_at < OLD.updated_at
		OR NEW.binding_key IS DISTINCT FROM OLD.binding_key
		OR NEW.generation IS DISTINCT FROM OLD.generation
		OR NEW.protocol IS DISTINCT FROM OLD.protocol
		OR NEW.protocol_version IS DISTINCT FROM OLD.protocol_version
		OR NEW.direction IS DISTINCT FROM OLD.direction
		OR NEW.local_kind IS DISTINCT FROM OLD.local_kind
		OR NEW.local_selector_json IS DISTINCT FROM OLD.local_selector_json
		OR NEW.peer_authority IS DISTINCT FROM OLD.peer_authority
		OR NEW.remote_resource_kind IS DISTINCT FROM OLD.remote_resource_kind
		OR NEW.remote_resource_ref IS DISTINCT FROM OLD.remote_resource_ref
		OR NEW.mapping_schema IS DISTINCT FROM OLD.mapping_schema
		OR NEW.mapping_json IS DISTINCT FROM OLD.mapping_json
		OR NEW.mapping_hash IS DISTINCT FROM OLD.mapping_hash
		OR NEW.known_losses_json IS DISTINCT FROM OLD.known_losses_json
		OR NEW.losses_hash IS DISTINCT FROM OLD.losses_hash
		OR NEW.rule_refs_json IS DISTINCT FROM OLD.rule_refs_json
		OR NEW.permission_profile_ref IS DISTINCT FROM OLD.permission_profile_ref
		OR NEW.currency_policy IS DISTINCT FROM OLD.currency_policy
		OR NEW.supersedes_id IS DISTINCT FROM OLD.supersedes_id
		OR NEW.spec_hash IS DISTINCT FROM OLD.spec_hash
		OR NEW.command_key_hash IS DISTINCT FROM OLD.command_key_hash
		OR NEW.request_hash IS DISTINCT FROM OLD.request_hash THEN
		RAISE EXCEPTION 'olivares: ProtocolBindingSpec immutable configuration changed'
			USING ERRCODE = '23514';
	END IF;

	IF NOT (
		(OLD.state = 'draft' AND NEW.state IN ('active', 'disabled'))
		OR (OLD.state = 'active' AND NEW.state IN ('disabled', 'superseded'))
	) THEN
		RAISE EXCEPTION 'olivares: invalid ProtocolBindingSpec state transition'
			USING ERRCODE = '23514';
	END IF;
	IF (NEW.validation_verdict IS DISTINCT FROM OLD.validation_verdict
		OR NEW.validation_code IS DISTINCT FROM OLD.validation_code
		OR NEW.validated_at IS DISTINCT FROM OLD.validated_at)
		AND NOT (OLD.state = 'draft' AND NEW.state = 'active') THEN
		RAISE EXCEPTION 'olivares: ProtocolBindingSpec validation changed outside activation'
			USING ERRCODE = '23514';
	END IF;
	RETURN NEW;
END;
$fn$;

CREATE OR REPLACE FUNCTION olivares_sessions_protocol_binding_validate()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $fn$
BEGIN
	IF TG_TABLE_SCHEMA <> 'public'
		OR TG_TABLE_NAME <> 'sessions_communication_binding'
		OR TG_OP NOT IN ('INSERT', 'UPDATE') THEN
		RAISE EXCEPTION 'olivares: protocol binding validator attached outside its exact table/operation'
			USING ERRCODE = '23514';
	END IF;

	IF NEW.id IS NULL
		OR NEW.id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR NEW.tenant_id IS NULL
		OR NEW.tenant_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR NEW.tenant_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff'
		OR NEW.workspace_id IS NULL
		OR NEW.workspace_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR NEW.version < 1 OR NEW.created_at IS NULL OR NEW.updated_at IS NULL
		OR NEW.updated_at < NEW.created_at
		OR NEW.binding_spec_generation < 1 OR NEW.generation < 1
		OR NEW.protocol NOT IN ('a2a', 'mcp')
		OR NEW.direction NOT IN ('inbound', 'outbound', 'bidirectional')
		OR NEW.external_kind NOT IN ('task', 'message', 'task_or_message')
		OR NEW.observation_verdict NOT IN ('CLEAN', 'BROKEN', 'UNKNOWN')
		OR pg_catalog.octet_length(NEW.pinned_spec_hash) <> 32
		OR pg_catalog.octet_length(NEW.pinned_mapping_hash) <> 32
		OR pg_catalog.octet_length(NEW.pinned_losses_hash) <> 32
		OR pg_catalog.octet_length(NEW.dispatch_key_hash) <> 32
		OR pg_catalog.octet_length(NEW.reservation_hash) <> 32
		OR (NEW.owner_digest IS NOT NULL AND pg_catalog.octet_length(NEW.owner_digest) <> 32)
		OR (NEW.detail_hash IS NOT NULL AND pg_catalog.octet_length(NEW.detail_hash) <> 32)
		OR (NEW.last_update_hash IS NOT NULL AND pg_catalog.octet_length(NEW.last_update_hash) <> 32)
		OR (NEW.cancel_key_hash IS NOT NULL AND pg_catalog.octet_length(NEW.cancel_key_hash) <> 32)
		OR (NEW.mcp_task_json IS NULL) IS DISTINCT FROM (NEW.mcp_task_hash IS NULL)
		OR (NEW.mcp_task_hash IS NOT NULL AND pg_catalog.octet_length(NEW.mcp_task_hash) <> 32)
		OR (NEW.current_ttl_ms IS NOT NULL AND NEW.current_ttl_ms < 1)
		OR (NEW.current_poll_interval_ms IS NOT NULL AND NEW.current_poll_interval_ms < 1)
		OR NEW.last_event_seq < 1
		OR (NEW.cancel_requested IS DISTINCT FROM (NEW.cancel_requested_at IS NOT NULL))
		OR (NEW.cancel_requested IS DISTINCT FROM (NEW.cancel_reason_code IS NOT NULL))
		OR (NEW.terminal AND NEW.external_active_slot IS NOT NULL)
		OR (NOT NEW.terminal AND NEW.external_id IS NOT NULL
			AND NEW.external_active_slot IS DISTINCT FROM NEW.external_id)
		OR (NEW.external_id IS NULL AND NEW.external_active_slot IS NOT NULL) THEN
		RAISE EXCEPTION 'olivares: invalid ProtocolBinding row'
			USING ERRCODE = '23514';
	END IF;

	IF TG_OP = 'INSERT' THEN
		IF NEW.version <> 1 OR NEW.terminal OR NEW.cancel_requested THEN
			RAISE EXCEPTION 'olivares: ProtocolBinding must begin non-terminal at version one'
				USING ERRCODE = '23514';
		END IF;
		RETURN NEW;
	END IF;

	IF OLD.terminal THEN
		RAISE EXCEPTION 'olivares: terminal ProtocolBinding is immutable'
			USING ERRCODE = '23514';
	END IF;
	IF NEW.id IS DISTINCT FROM OLD.id
		OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
		OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
		OR NEW.created_at IS DISTINCT FROM OLD.created_at
		OR NEW.version <> OLD.version + 1
		OR NEW.updated_at < OLD.updated_at
		OR NEW.binding_spec_id IS DISTINCT FROM OLD.binding_spec_id
		OR NEW.binding_spec_generation IS DISTINCT FROM OLD.binding_spec_generation
		OR NEW.pinned_spec_hash IS DISTINCT FROM OLD.pinned_spec_hash
		OR NEW.pinned_mapping_hash IS DISTINCT FROM OLD.pinned_mapping_hash
		OR NEW.pinned_losses_hash IS DISTINCT FROM OLD.pinned_losses_hash
		OR NEW.message_id IS DISTINCT FROM OLD.message_id
		OR NEW.delivery_id IS DISTINCT FROM OLD.delivery_id
		OR NEW.work_item_id IS DISTINCT FROM OLD.work_item_id
		OR NEW.protocol IS DISTINCT FROM OLD.protocol
		OR NEW.protocol_version IS DISTINCT FROM OLD.protocol_version
		OR NEW.direction IS DISTINCT FROM OLD.direction
		OR NEW.peer_authority IS DISTINCT FROM OLD.peer_authority
		OR NEW.remote_resource_ref IS DISTINCT FROM OLD.remote_resource_ref
		OR NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
		OR NEW.dispatch_key_hash IS DISTINCT FROM OLD.dispatch_key_hash
		OR NEW.reservation_hash IS DISTINCT FROM OLD.reservation_hash
		OR NEW.generation IS DISTINCT FROM OLD.generation
		OR NEW.synthetic_sid IS DISTINCT FROM OLD.synthetic_sid
		OR NEW.owner_kind IS DISTINCT FROM OLD.owner_kind
		OR NEW.owner_ref IS DISTINCT FROM OLD.owner_ref
		OR NEW.owner_digest IS DISTINCT FROM OLD.owner_digest
		OR NEW.owner_epoch IS DISTINCT FROM OLD.owner_epoch
		OR NEW.lease_fence IS DISTINCT FROM OLD.lease_fence
		OR NEW.mcp_task_json IS DISTINCT FROM OLD.mcp_task_json
		OR NEW.mcp_task_hash IS DISTINCT FROM OLD.mcp_task_hash THEN
		RAISE EXCEPTION 'olivares: ProtocolBinding pinned identity changed'
			USING ERRCODE = '23514';
	END IF;
	IF NOT (
		NEW.external_kind IS NOT DISTINCT FROM OLD.external_kind
		OR (OLD.external_kind = 'task_or_message' AND NEW.external_kind IN ('task', 'message'))
	) OR (OLD.external_id IS NOT NULL AND NEW.external_id IS DISTINCT FROM OLD.external_id)
		OR (OLD.context_id IS NOT NULL AND NEW.context_id IS DISTINCT FROM OLD.context_id)
		OR (OLD.external_message_id IS NOT NULL
			AND NEW.external_message_id IS DISTINCT FROM OLD.external_message_id)
		OR (OLD.cancel_requested AND NOT NEW.cancel_requested)
		OR OLD.last_event_seq = 9223372036854775807
		OR NEW.last_event_seq <> OLD.last_event_seq + 1 THEN
		RAISE EXCEPTION 'olivares: invalid ProtocolBinding lifecycle update'
			USING ERRCODE = '23514';
	END IF;
	RETURN NEW;
END;
$fn$;

CREATE OR REPLACE FUNCTION olivares_sessions_protocol_binding_no_delete()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $fn$
BEGIN
	IF TG_TABLE_SCHEMA <> 'public'
		OR TG_TABLE_NAME NOT IN (
			'sessions_communication_binding_spec',
			'sessions_communication_binding'
		)
		OR TG_OP <> 'DELETE' THEN
		RAISE EXCEPTION 'olivares: protocol binding no-delete guard attached outside its exact tables/operation'
			USING ERRCODE = '23514';
	END IF;
	RAISE EXCEPTION 'olivares: protocol binding rows cannot be hard-deleted'
		USING ERRCODE = '23514';
END;
$fn$;
