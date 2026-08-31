-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_channel_grant_guard_upd
BEFORE UPDATE ON sessions_channel_grant
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: ChannelGrant immutable lineage changed')
	WHERE NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id
		OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at
		OR NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at
		OR NEW.channel_id IS NOT OLD.channel_id OR NEW.subject_kind IS NOT OLD.subject_kind
		OR NEW.subject_ref IS NOT OLD.subject_ref OR NEW.generation IS NOT OLD.generation
		OR NEW.can_read IS NOT OLD.can_read OR NEW.can_write IS NOT OLD.can_write
		OR NEW.can_admin IS NOT OLD.can_admin OR NEW.granted_by_kind IS NOT OLD.granted_by_kind
		OR NEW.granted_by_ref IS NOT OLD.granted_by_ref OR NEW.expires_at IS NOT OLD.expires_at
		OR NEW.supersedes_id IS NOT OLD.supersedes_id
		OR (OLD.state <> NEW.state AND NOT
			(OLD.state = 'active' AND NEW.state IN ('revoked','expired')))
		OR OLD.state IN ('revoked','expired');
	SELECT RAISE(ABORT, 'olivares: invalid ChannelGrant terminal evidence')
	WHERE NEW.state NOT IN ('active','revoked','expired')
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
		OR (NEW.revoked_by_kind IS NOT NULL AND
			NEW.revoked_by_kind NOT IN ('user','agent','session','system'))
		OR (NEW.revoked_by_ref IS NOT NULL AND
			length(CAST(NEW.revoked_by_ref AS BLOB)) NOT BETWEEN 1 AND 512)
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
		OR (NEW.state <> 'revoked' AND
			(NEW.revoked_by_kind IS NOT NULL OR NEW.revoked_by_ref IS NOT NULL))
		OR (NEW.state = 'expired' AND (NEW.expires_at IS NULL OR NEW.updated_at < NEW.expires_at));
	SELECT RAISE(ABORT, 'olivares: ChannelGrant subject reference is non-canonical')
	WHERE (NEW.subject_kind = 'session' AND
			(NEW.subject_ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(NEW.subject_ref,5),'-','')) <> 32
				OR replace(substr(NEW.subject_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.subject_kind IN ('user','user_group','agent','agent_group') AND
			(NEW.subject_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.subject_ref,'-','')) <> 32
				OR replace(NEW.subject_ref,'-','') GLOB '*[^0-9a-f]*'));
END;
