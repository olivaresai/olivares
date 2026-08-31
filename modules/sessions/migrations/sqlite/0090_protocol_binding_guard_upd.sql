-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

CREATE TRIGGER sessions_communication_binding_guard_upd
BEFORE UPDATE ON sessions_communication_binding
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: terminal ProtocolBinding is immutable') WHERE OLD.terminal <> 0;
	SELECT RAISE(ABORT, 'olivares: invalid ProtocolBinding row')
	WHERE NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at
		OR NEW.external_kind NOT IN ('task','message','task_or_message')
		OR NEW.observation_verdict NOT IN ('CLEAN','BROKEN','UNKNOWN')
		OR (NEW.detail_hash IS NOT NULL AND length(NEW.detail_hash) <> 32)
		OR (NEW.last_update_hash IS NOT NULL AND length(NEW.last_update_hash) <> 32)
		OR (NEW.cancel_key_hash IS NOT NULL AND length(NEW.cancel_key_hash) <> 32)
		OR (NEW.current_ttl_ms IS NOT NULL AND NEW.current_ttl_ms < 1)
		OR (NEW.current_poll_interval_ms IS NOT NULL AND NEW.current_poll_interval_ms < 1)
		OR (NEW.cancel_requested <> (NEW.cancel_requested_at IS NOT NULL))
		OR (NEW.cancel_requested <> (NEW.cancel_reason_code IS NOT NULL))
		OR (NEW.terminal <> 0 AND NEW.external_active_slot IS NOT NULL)
		OR (NEW.terminal = 0 AND NEW.external_id IS NOT NULL
			AND NEW.external_active_slot IS NOT NEW.external_id)
		OR (NEW.external_id IS NULL AND NEW.external_active_slot IS NOT NULL);
	SELECT RAISE(ABORT, 'olivares: ProtocolBinding pinned identity changed')
	WHERE NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id
		OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at
		OR NEW.binding_spec_id IS NOT OLD.binding_spec_id
		OR NEW.binding_spec_generation IS NOT OLD.binding_spec_generation
		OR NEW.pinned_spec_hash IS NOT OLD.pinned_spec_hash
		OR NEW.pinned_mapping_hash IS NOT OLD.pinned_mapping_hash
		OR NEW.pinned_losses_hash IS NOT OLD.pinned_losses_hash
		OR NEW.message_id IS NOT OLD.message_id OR NEW.delivery_id IS NOT OLD.delivery_id
		OR NEW.work_item_id IS NOT OLD.work_item_id OR NEW.protocol IS NOT OLD.protocol
		OR NEW.protocol_version IS NOT OLD.protocol_version OR NEW.direction IS NOT OLD.direction
		OR NEW.peer_authority IS NOT OLD.peer_authority
		OR NEW.remote_resource_ref IS NOT OLD.remote_resource_ref
		OR NEW.attempt_id IS NOT OLD.attempt_id
		OR NEW.dispatch_key_hash IS NOT OLD.dispatch_key_hash
		OR NEW.reservation_hash IS NOT OLD.reservation_hash OR NEW.generation IS NOT OLD.generation
		OR NEW.synthetic_sid IS NOT OLD.synthetic_sid OR NEW.owner_kind IS NOT OLD.owner_kind
		OR NEW.owner_ref IS NOT OLD.owner_ref OR NEW.owner_digest IS NOT OLD.owner_digest
		OR NEW.owner_epoch IS NOT OLD.owner_epoch OR NEW.lease_fence IS NOT OLD.lease_fence
		OR NEW.mcp_task_json IS NOT OLD.mcp_task_json OR NEW.mcp_task_hash IS NOT OLD.mcp_task_hash;
	SELECT RAISE(ABORT, 'olivares: invalid ProtocolBinding lifecycle update')
	WHERE NOT (NEW.external_kind IS OLD.external_kind
		OR (OLD.external_kind = 'task_or_message' AND NEW.external_kind IN ('task','message')))
		OR (OLD.external_id IS NOT NULL AND NEW.external_id IS NOT OLD.external_id)
		OR (OLD.context_id IS NOT NULL AND NEW.context_id IS NOT OLD.context_id)
		OR (OLD.external_message_id IS NOT NULL
			AND NEW.external_message_id IS NOT OLD.external_message_id)
		OR (OLD.cancel_requested <> 0 AND NEW.cancel_requested = 0)
		OR OLD.last_event_seq = 9223372036854775807
		OR NEW.last_event_seq <> OLD.last_event_seq + 1;
END;
