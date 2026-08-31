-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

CREATE TRIGGER sessions_communication_binding_spec_guard_ins
BEFORE INSERT ON sessions_communication_binding_spec
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: invalid ProtocolBindingSpec identity')
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
		OR NEW.updated_at < NEW.created_at;
	SELECT RAISE(ABORT, 'olivares: invalid ProtocolBindingSpec vocabulary')
	WHERE NEW.generation < 1
		OR NEW.protocol NOT IN ('a2a','mcp')
		OR NEW.direction NOT IN ('inbound','outbound','bidirectional')
		OR NEW.local_kind NOT IN ('work_item','agent','model','channel')
		OR NEW.currency_policy <> 'pinned'
		OR NEW.validation_verdict NOT IN ('CLEAN','BROKEN','UNKNOWN')
		OR (NEW.validation_verdict = 'CLEAN' AND NEW.validated_at IS NULL)
		OR NEW.state <> 'draft' OR NEW.active_slot IS NOT NULL;
	SELECT RAISE(ABORT, 'olivares: invalid ProtocolBindingSpec hashes')
	WHERE length(NEW.mapping_hash) <> 32 OR length(NEW.losses_hash) <> 32
		OR length(NEW.spec_hash) <> 32 OR length(NEW.plan_hash) <> 32
		OR length(NEW.command_key_hash) <> 32 OR length(NEW.request_hash) <> 32;
	SELECT RAISE(ABORT, 'olivares: invalid ProtocolBindingSpec selector JSON')
	WHERE NOT json_valid(NEW.local_selector_json)
		OR json_type(NEW.local_selector_json) <> 'object';
	SELECT RAISE(ABORT, 'olivares: invalid ProtocolBindingSpec mapping JSON')
	WHERE NOT json_valid(NEW.mapping_json) OR json_type(NEW.mapping_json) <> 'array';
	SELECT RAISE(ABORT, 'olivares: invalid ProtocolBindingSpec losses JSON')
	WHERE NOT json_valid(NEW.known_losses_json)
		OR json_type(NEW.known_losses_json) NOT IN ('array','null');
	SELECT RAISE(ABORT, 'olivares: invalid ProtocolBindingSpec rules JSON')
	WHERE NOT json_valid(NEW.rule_refs_json) OR json_type(NEW.rule_refs_json) <> 'array';
END;
