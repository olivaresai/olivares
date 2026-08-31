-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_channel_grant_guard_ins
BEFORE INSERT ON sessions_channel_grant
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
	SELECT RAISE(ABORT, 'olivares: invalid ChannelGrant shape')
	WHERE NEW.version < 1 OR NEW.updated_at < NEW.created_at OR NEW.generation < 1
		OR NEW.subject_kind NOT IN ('user','user_group','agent','agent_group','session')
		OR (NEW.subject_kind = 'session' AND
			(length(NEW.subject_ref) <> 40 OR substr(NEW.subject_ref,1,4) <> 'osn_'
				OR substr(NEW.subject_ref,13,1) <> '-' OR substr(NEW.subject_ref,18,1) <> '-'
				OR substr(NEW.subject_ref,19,1) <> '7' OR substr(NEW.subject_ref,23,1) <> '-'
				OR substr(NEW.subject_ref,24,1) NOT IN ('8','9','a','b')
				OR substr(NEW.subject_ref,28,1) <> '-'
				OR length(replace(substr(NEW.subject_ref,5),'-','')) <> 32
				OR replace(substr(NEW.subject_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.subject_kind <> 'session' AND
			(length(NEW.subject_ref) <> 36 OR substr(NEW.subject_ref,9,1) <> '-'
				OR substr(NEW.subject_ref,14,1) <> '-' OR substr(NEW.subject_ref,15,1) <> '7'
				OR substr(NEW.subject_ref,19,1) <> '-'
				OR substr(NEW.subject_ref,20,1) NOT IN ('8','9','a','b')
				OR substr(NEW.subject_ref,24,1) <> '-'
				OR length(replace(NEW.subject_ref,'-','')) <> 32
				OR replace(NEW.subject_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR NOT (NEW.can_read OR NEW.can_write OR NEW.can_admin)
		OR NEW.state NOT IN ('active','revoked','expired')
		OR NEW.granted_by_kind NOT IN ('user','agent','session','system')
		OR length(CAST(NEW.granted_by_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR (NEW.granted_by_kind IN ('user','agent') AND
			(length(NEW.granted_by_ref) <> 36
				OR substr(NEW.granted_by_ref,9,1) <> '-' OR substr(NEW.granted_by_ref,14,1) <> '-'
				OR substr(NEW.granted_by_ref,15,1) <> '7'
				OR substr(NEW.granted_by_ref,19,1) <> '-'
				OR substr(NEW.granted_by_ref,20,1) NOT IN ('8','9','a','b')
				OR substr(NEW.granted_by_ref,24,1) <> '-'
				OR length(replace(NEW.granted_by_ref,'-','')) <> 32
				OR replace(NEW.granted_by_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.granted_by_kind = 'session' AND
			(length(NEW.granted_by_ref) <> 40 OR substr(NEW.granted_by_ref,1,4) <> 'osn_'
				OR substr(NEW.granted_by_ref,13,1) <> '-'
				OR substr(NEW.granted_by_ref,18,1) <> '-'
				OR substr(NEW.granted_by_ref,19,1) <> '7'
				OR substr(NEW.granted_by_ref,23,1) <> '-'
				OR substr(NEW.granted_by_ref,24,1) NOT IN ('8','9','a','b')
				OR substr(NEW.granted_by_ref,28,1) <> '-'
				OR length(replace(substr(NEW.granted_by_ref,5),'-','')) <> 32
				OR replace(substr(NEW.granted_by_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.granted_by_kind = 'system' AND
			(trim(NEW.granted_by_ref) <> NEW.granted_by_ref
				OR instr(NEW.granted_by_ref,char(10)) > 0
				OR instr(NEW.granted_by_ref,char(13)) > 0
				OR instr(NEW.granted_by_ref,char(0)) > 0))
		OR (NEW.revoked_by_kind IS NULL) IS NOT (NEW.revoked_by_ref IS NULL)
		OR (NEW.state = 'revoked') IS NOT (NEW.revoked_by_kind IS NOT NULL)
		OR (NEW.revoked_by_kind IS NOT NULL AND NEW.revoked_by_kind NOT IN ('user','agent','session','system'))
		OR (NEW.revoked_by_ref IS NOT NULL AND length(CAST(NEW.revoked_by_ref AS BLOB)) NOT BETWEEN 1 AND 512)
		OR (NEW.revoked_by_kind IN ('user','agent') AND
			(length(NEW.revoked_by_ref) <> 36
				OR substr(NEW.revoked_by_ref,9,1) <> '-' OR substr(NEW.revoked_by_ref,14,1) <> '-'
				OR substr(NEW.revoked_by_ref,15,1) <> '7'
				OR substr(NEW.revoked_by_ref,19,1) <> '-'
				OR substr(NEW.revoked_by_ref,20,1) NOT IN ('8','9','a','b')
				OR substr(NEW.revoked_by_ref,24,1) <> '-'
				OR length(replace(NEW.revoked_by_ref,'-','')) <> 32
				OR replace(NEW.revoked_by_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.revoked_by_kind = 'session' AND
			(length(NEW.revoked_by_ref) <> 40 OR substr(NEW.revoked_by_ref,1,4) <> 'osn_'
				OR substr(NEW.revoked_by_ref,13,1) <> '-'
				OR substr(NEW.revoked_by_ref,18,1) <> '-'
				OR substr(NEW.revoked_by_ref,19,1) <> '7'
				OR substr(NEW.revoked_by_ref,23,1) <> '-'
				OR substr(NEW.revoked_by_ref,24,1) NOT IN ('8','9','a','b')
				OR substr(NEW.revoked_by_ref,28,1) <> '-'
				OR length(replace(substr(NEW.revoked_by_ref,5),'-','')) <> 32
				OR replace(substr(NEW.revoked_by_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.revoked_by_kind = 'system' AND
			(trim(NEW.revoked_by_ref) <> NEW.revoked_by_ref
				OR instr(NEW.revoked_by_ref,char(10)) > 0
				OR instr(NEW.revoked_by_ref,char(13)) > 0
				OR instr(NEW.revoked_by_ref,char(0)) > 0))
		OR (NEW.expires_at IS NOT NULL AND NEW.expires_at <= NEW.created_at)
		OR (NEW.state = 'expired' AND (NEW.expires_at IS NULL OR NEW.updated_at < NEW.expires_at))
		OR (NEW.generation = 1) IS NOT (NEW.supersedes_id IS NULL)
		OR NEW.supersedes_id IS NEW.id;
	SELECT RAISE(ABORT, 'olivares: ChannelGrant channel crosses tenant/workspace')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_channel c WHERE c.id = NEW.channel_id
		AND c.tenant_id = NEW.tenant_id AND c.workspace_id = NEW.workspace_id);
	SELECT RAISE(ABORT, 'olivares: ChannelGrant predecessor lineage is not serialized')
	WHERE NEW.generation > 1 AND NOT EXISTS (
		SELECT 1 FROM sessions_channel_grant p
		WHERE p.id = NEW.supersedes_id AND p.tenant_id = NEW.tenant_id
			AND p.workspace_id = NEW.workspace_id AND p.channel_id = NEW.channel_id
			AND p.subject_kind = NEW.subject_kind AND p.subject_ref = NEW.subject_ref
			AND p.generation + 1 = NEW.generation AND p.state IN ('revoked','expired'));
END;
