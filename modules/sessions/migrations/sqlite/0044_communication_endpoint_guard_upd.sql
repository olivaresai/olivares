-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_communication_endpoint_guard_upd
BEFORE UPDATE ON sessions_communication_endpoint
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: CommunicationEndpoint immutable generation changed')
	WHERE NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id
		OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at
		OR NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at
		OR NEW.owner_kind IS NOT OLD.owner_kind OR NEW.owner_ref IS NOT OLD.owner_ref
		OR NEW.provider_key IS NOT OLD.provider_key OR NEW.transport IS NOT OLD.transport
		OR NEW.endpoint_ref IS NOT OLD.endpoint_ref OR NEW.session_sid IS NOT OLD.session_sid
		OR NEW.capabilities_json IS NOT OLD.capabilities_json
		OR NEW.transport_fingerprint IS NOT OLD.transport_fingerprint
		OR NEW.support_level IS NOT OLD.support_level OR NEW.priority IS NOT OLD.priority
		OR NEW.generation IS NOT OLD.generation OR NEW.secret_ref IS NOT OLD.secret_ref
		OR (OLD.state <> NEW.state AND NOT (
			(OLD.state = 'active' AND NEW.state IN ('stale','disabled')) OR
			(OLD.state = 'stale' AND NEW.state IN ('active','disabled'))))
		OR (OLD.state = 'disabled' AND NEW.state <> OLD.state);
	SELECT RAISE(ABORT, 'olivares: invalid CommunicationEndpoint state or heartbeat')
	WHERE NEW.state NOT IN ('active','stale','disabled')
		OR (NEW.heartbeat_expires_at IS NOT NULL AND NEW.heartbeat_expires_at < NEW.created_at);
	SELECT RAISE(ABORT, 'olivares: CommunicationEndpoint has multiple active generations')
	WHERE NEW.state = 'active' AND EXISTS (
		SELECT 1 FROM sessions_communication_endpoint p
		WHERE p.tenant_id = NEW.tenant_id AND p.provider_key = NEW.provider_key
			AND p.endpoint_ref = NEW.endpoint_ref
			AND p.state = 'active' AND p.id <> NEW.id);
	SELECT RAISE(ABORT, 'olivares: CommunicationEndpoint owner reference is non-canonical')
	WHERE (NEW.owner_kind = 'session' AND
			(NEW.owner_ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(NEW.owner_ref,5),'-','')) <> 32
				OR replace(substr(NEW.owner_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.owner_kind IN ('user','agent') AND
			(NEW.owner_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.owner_ref,'-','')) <> 32
				OR replace(NEW.owner_ref,'-','') GLOB '*[^0-9a-f]*'));
END;
