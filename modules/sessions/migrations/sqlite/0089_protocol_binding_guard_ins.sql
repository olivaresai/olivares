-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

CREATE TRIGGER sessions_communication_binding_guard_ins
BEFORE INSERT ON sessions_communication_binding
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: invalid ProtocolBinding row')
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
		OR replace(NEW.workspace_id,'-','') GLOB '*[^0-9a-f]*'
		OR NEW.version <> 1 OR NEW.created_at IS NULL OR NEW.updated_at IS NULL
		OR NEW.updated_at < NEW.created_at
		OR NEW.binding_spec_generation < 1 OR NEW.generation < 1
		OR NEW.protocol NOT IN ('a2a','mcp')
		OR NEW.direction NOT IN ('inbound','outbound','bidirectional')
		OR NEW.external_kind NOT IN ('task','message','task_or_message')
		OR NEW.observation_verdict NOT IN ('CLEAN','BROKEN','UNKNOWN')
		OR length(NEW.pinned_spec_hash) <> 32 OR length(NEW.pinned_mapping_hash) <> 32
		OR length(NEW.pinned_losses_hash) <> 32 OR length(NEW.dispatch_key_hash) <> 32
		OR length(NEW.reservation_hash) <> 32
		OR (NEW.owner_digest IS NOT NULL AND length(NEW.owner_digest) <> 32)
		OR (NEW.detail_hash IS NOT NULL AND length(NEW.detail_hash) <> 32)
		OR (NEW.last_update_hash IS NOT NULL AND length(NEW.last_update_hash) <> 32)
		OR (NEW.cancel_key_hash IS NOT NULL AND length(NEW.cancel_key_hash) <> 32)
		OR (NEW.mcp_task_json IS NULL) IS NOT (NEW.mcp_task_hash IS NULL)
		OR (NEW.mcp_task_hash IS NOT NULL AND length(NEW.mcp_task_hash) <> 32)
		OR (NEW.current_ttl_ms IS NOT NULL AND NEW.current_ttl_ms < 1)
		OR (NEW.current_poll_interval_ms IS NOT NULL AND NEW.current_poll_interval_ms < 1)
		OR NEW.last_event_seq < 1 OR NEW.terminal <> 0 OR NEW.cancel_requested <> 0
		OR NEW.cancel_requested_at IS NOT NULL OR NEW.cancel_reason_code IS NOT NULL
		OR (NEW.external_id IS NULL AND NEW.external_active_slot IS NOT NULL)
		OR (NEW.external_id IS NOT NULL AND NEW.external_active_slot IS NOT NEW.external_id);
END;
