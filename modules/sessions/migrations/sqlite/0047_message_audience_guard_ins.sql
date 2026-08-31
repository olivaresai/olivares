-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_message_audience_guard_ins
BEFORE INSERT ON sessions_message_audience
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
	SELECT RAISE(ABORT, 'olivares: invalid MessageAudience shape')
	WHERE NEW.version <> 1 OR NEW.ordinal < 1
		OR NEW.selector_kind NOT IN ('user','user_group','agent','agent_group','session','subscribers','workspace_members')
		OR NEW.selector_wake_policy NOT IN ('none','primary','all')
		OR (NEW.selector_kind IN ('subscribers','workspace_members')) IS NOT (NEW.selector_ref IS NULL)
		OR (NEW.selector_kind = 'session' AND
			(length(NEW.selector_ref) <> 40 OR substr(NEW.selector_ref,1,4) <> 'osn_'))
		OR (NEW.selector_kind IN ('user','user_group','agent','agent_group')
			AND length(NEW.selector_ref) <> 36)
		OR (NEW.selector_kind = 'session' AND
			(NEW.selector_ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(NEW.selector_ref,5),'-','')) <> 32
				OR replace(substr(NEW.selector_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.selector_kind IN ('user','user_group','agent','agent_group') AND
			(NEW.selector_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.selector_ref,'-','')) <> 32
				OR replace(NEW.selector_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR NEW.channel_acl_revision < 1 OR NEW.route_revision < 1 OR NEW.subscription_revision < 1
		OR NEW.directory_epoch < 1
		OR NEW.resolved_count < 0 OR length(NEW.selector_hash) <> 32 OR length(NEW.resolved_hash) <> 32
		OR (NEW.selector_kind IN ('user','agent','session') AND NEW.resolved_count <> 1)
		OR (NEW.selector_required AND NEW.resolved_count = 0);
	SELECT RAISE(ABORT, 'olivares: MessageAudience message crosses tenant/workspace')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_message m
		WHERE m.id = NEW.message_id AND m.tenant_id = NEW.tenant_id
			AND m.workspace_id = NEW.workspace_id AND m.state = 'draft');
	SELECT RAISE(ABORT, 'olivares: MessageAudience route crosses tenant/workspace')
	WHERE NEW.route_rule_id IS NOT NULL AND NOT EXISTS (
		SELECT 1 FROM sessions_channel_route r
		JOIN sessions_message m ON m.id = NEW.message_id
			AND m.tenant_id = NEW.tenant_id AND m.workspace_id = NEW.workspace_id
		WHERE r.id = NEW.route_rule_id AND r.tenant_id = NEW.tenant_id
			AND r.workspace_id = NEW.workspace_id AND r.target_channel_id = m.channel_id);
END;
