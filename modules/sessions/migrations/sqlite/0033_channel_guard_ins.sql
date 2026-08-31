-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_channel_guard_ins
BEFORE INSERT ON sessions_channel
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
	SELECT RAISE(ABORT, 'olivares: invalid Channel shape')
	WHERE NEW.version < 1 OR NEW.updated_at < NEW.created_at
		OR length(NEW.slug) NOT BETWEEN 1 AND 128 OR NEW.slug GLOB '*[^a-z0-9._-]*'
		OR length(CAST(NEW.name AS BLOB)) NOT BETWEEN 1 AND 256
		OR (NEW.description IS NOT NULL AND length(CAST(NEW.description AS BLOB)) NOT BETWEEN 1 AND 4096)
		OR NEW.kind NOT IN ('coordination','work','incident','announcement','private')
		OR NEW.state NOT IN ('active','archived')
		OR NEW.sensitivity NOT IN ('internal','restricted')
		OR NEW.content_protection NOT IN ('storage','application_sealed')
		OR NEW.protection_generation < 1
		OR NEW.default_ack_policy NOT IN ('none','each_required','quorum')
		OR NEW.default_ack_timeout_ms < 0
		OR (NEW.default_ack_policy = 'none') IS NOT (NEW.default_ack_timeout_ms = 0)
		OR NEW.default_wake NOT IN ('none','primary','all')
		OR (NEW.retention_policy_ref IS NOT NULL AND
			(length(CAST(NEW.retention_policy_ref AS BLOB)) NOT BETWEEN 1 AND 512
				OR trim(NEW.retention_policy_ref) <> NEW.retention_policy_ref
				OR instr(NEW.retention_policy_ref, char(0)) > 0
				OR instr(NEW.retention_policy_ref, char(10)) > 0
				OR instr(NEW.retention_policy_ref, char(13)) > 0))
		OR NEW.max_fanout < 1 OR NEW.max_automation_depth < 0
		OR NEW.acl_revision < 1 OR NEW.route_revision < 1 OR NEW.subscription_revision < 1
		OR (NEW.sensitivity = 'restricted' AND NEW.content_protection <> 'application_sealed');
END;
