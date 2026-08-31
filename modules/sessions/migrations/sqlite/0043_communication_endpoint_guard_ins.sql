-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_communication_endpoint_guard_ins
BEFORE INSERT ON sessions_communication_endpoint
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
	SELECT RAISE(ABORT, 'olivares: invalid CommunicationEndpoint shape')
	WHERE NEW.version < 1 OR NEW.updated_at < NEW.created_at
		OR NEW.owner_kind NOT IN ('user','agent','session')
		OR (NEW.owner_kind = 'session' AND
			(length(NEW.owner_ref) <> 40 OR substr(NEW.owner_ref,1,4) <> 'osn_'
				OR substr(NEW.owner_ref,13,1) <> '-' OR substr(NEW.owner_ref,18,1) <> '-'
				OR substr(NEW.owner_ref,19,1) <> '7' OR substr(NEW.owner_ref,23,1) <> '-'
				OR substr(NEW.owner_ref,24,1) NOT IN ('8','9','a','b')
				OR substr(NEW.owner_ref,28,1) <> '-'
				OR length(replace(substr(NEW.owner_ref,5),'-','')) <> 32
				OR replace(substr(NEW.owner_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.owner_kind <> 'session' AND
			(length(NEW.owner_ref) <> 36 OR substr(NEW.owner_ref,9,1) <> '-'
				OR substr(NEW.owner_ref,14,1) <> '-' OR substr(NEW.owner_ref,15,1) <> '7'
				OR substr(NEW.owner_ref,19,1) <> '-'
				OR substr(NEW.owner_ref,20,1) NOT IN ('8','9','a','b')
				OR substr(NEW.owner_ref,24,1) <> '-'
				OR length(replace(NEW.owner_ref,'-','')) <> 32
				OR replace(NEW.owner_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR NOT (NEW.provider_key IN ('claude-channel','claude-stream-json','codex-app-server','a2a','mcp')
			OR (substr(NEW.provider_key,1,7) = 'driver:' AND length(NEW.provider_key) BETWEEN 8 AND 103
				AND substr(NEW.provider_key,8) NOT GLOB '*[^a-z0-9._-]*'))
		OR length(NEW.transport) NOT BETWEEN 1 AND 128 OR NEW.transport GLOB '*[^a-z0-9._-]*'
		OR length(CAST(NEW.endpoint_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR trim(NEW.endpoint_ref) <> NEW.endpoint_ref
		OR instr(NEW.endpoint_ref,char(0)) > 0 OR instr(NEW.endpoint_ref,char(10)) > 0
		OR instr(NEW.endpoint_ref,char(13)) > 0
		OR (NEW.owner_kind = 'session') IS NOT (NEW.session_sid IS NOT NULL)
		OR (NEW.session_sid IS NOT NULL AND NEW.session_sid <> NEW.owner_ref)
		OR NOT json_valid(NEW.capabilities_json)
		OR length(CAST(NEW.capabilities_json AS BLOB)) > 65536
		OR NEW.support_level NOT IN ('stable','preview','experimental')
		OR NEW.priority < 0 OR NEW.state NOT IN ('active','stale','disabled')
		OR NEW.generation < 1
		OR (NEW.heartbeat_expires_at IS NOT NULL AND NEW.heartbeat_expires_at < NEW.created_at)
		OR (NEW.transport_fingerprint IS NOT NULL AND
			(length(CAST(NEW.transport_fingerprint AS BLOB)) NOT BETWEEN 1 AND 512
				OR trim(NEW.transport_fingerprint) <> NEW.transport_fingerprint
				OR instr(NEW.transport_fingerprint,char(0)) > 0
				OR instr(NEW.transport_fingerprint,char(10)) > 0
				OR instr(NEW.transport_fingerprint,char(13)) > 0))
		OR (NEW.secret_ref IS NOT NULL AND
			(length(CAST(NEW.secret_ref AS BLOB)) NOT BETWEEN 1 AND 512
				OR trim(NEW.secret_ref) <> NEW.secret_ref
				OR instr(NEW.secret_ref,char(0)) > 0
				OR instr(NEW.secret_ref,char(10)) > 0
				OR instr(NEW.secret_ref,char(13)) > 0));
	SELECT RAISE(ABORT, 'olivares: CommunicationEndpoint generation is not serialized')
	WHERE (NEW.generation = 1 AND EXISTS (
			SELECT 1 FROM sessions_communication_endpoint p
			WHERE p.tenant_id = NEW.tenant_id AND p.provider_key = NEW.provider_key
				AND p.endpoint_ref = NEW.endpoint_ref))
		OR (NEW.generation > 1 AND NOT EXISTS (
			SELECT 1 FROM sessions_communication_endpoint p
			WHERE p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
				AND p.provider_key = NEW.provider_key AND p.endpoint_ref = NEW.endpoint_ref
				AND p.generation = NEW.generation - 1 AND p.state <> 'active'));
	SELECT RAISE(ABORT, 'olivares: CommunicationEndpoint has multiple active generations')
	WHERE NEW.state = 'active' AND EXISTS (
		SELECT 1 FROM sessions_communication_endpoint p
		WHERE p.tenant_id = NEW.tenant_id AND p.provider_key = NEW.provider_key
			AND p.endpoint_ref = NEW.endpoint_ref
			AND p.state = 'active' AND p.id <> NEW.id);
END;
