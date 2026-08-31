-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

CREATE TRIGGER sessions_communication_binding_spec_guard_upd
BEFORE UPDATE ON sessions_communication_binding_spec
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: invalid ProtocolBindingSpec row')
	WHERE NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at
		OR NEW.state NOT IN ('active','disabled','superseded')
		OR (NEW.state = 'active' AND NEW.active_slot IS NOT NEW.binding_key)
		OR (NEW.state <> 'active' AND NEW.active_slot IS NOT NULL)
		OR NEW.validation_verdict NOT IN ('CLEAN','BROKEN','UNKNOWN')
		OR (NEW.validation_verdict = 'CLEAN' AND NEW.validated_at IS NULL)
		OR length(NEW.plan_hash) <> 32;
	SELECT RAISE(ABORT, 'olivares: ProtocolBindingSpec immutable configuration changed')
	WHERE NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id
		OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at
		OR NEW.binding_key IS NOT OLD.binding_key OR NEW.generation IS NOT OLD.generation
		OR NEW.protocol IS NOT OLD.protocol OR NEW.protocol_version IS NOT OLD.protocol_version
		OR NEW.direction IS NOT OLD.direction OR NEW.local_kind IS NOT OLD.local_kind
		OR NEW.local_selector_json IS NOT OLD.local_selector_json
		OR NEW.peer_authority IS NOT OLD.peer_authority
		OR NEW.remote_resource_kind IS NOT OLD.remote_resource_kind
		OR NEW.remote_resource_ref IS NOT OLD.remote_resource_ref
		OR NEW.mapping_schema IS NOT OLD.mapping_schema OR NEW.mapping_json IS NOT OLD.mapping_json
		OR NEW.mapping_hash IS NOT OLD.mapping_hash
		OR NEW.known_losses_json IS NOT OLD.known_losses_json
		OR NEW.losses_hash IS NOT OLD.losses_hash OR NEW.rule_refs_json IS NOT OLD.rule_refs_json
		OR NEW.permission_profile_ref IS NOT OLD.permission_profile_ref
		OR NEW.currency_policy IS NOT OLD.currency_policy
		OR NEW.supersedes_id IS NOT OLD.supersedes_id OR NEW.spec_hash IS NOT OLD.spec_hash
		OR NEW.command_key_hash IS NOT OLD.command_key_hash OR NEW.request_hash IS NOT OLD.request_hash;
	SELECT RAISE(ABORT, 'olivares: invalid ProtocolBindingSpec state transition')
	WHERE NOT ((OLD.state = 'draft' AND NEW.state IN ('active','disabled'))
		OR (OLD.state = 'active' AND NEW.state IN ('disabled','superseded')));
	SELECT RAISE(ABORT, 'olivares: ProtocolBindingSpec validation changed outside activation')
	WHERE (NEW.validation_verdict IS NOT OLD.validation_verdict
		OR NEW.validation_code IS NOT OLD.validation_code
		OR NEW.validated_at IS NOT OLD.validated_at)
		AND NOT (OLD.state = 'draft' AND NEW.state = 'active');
END;
