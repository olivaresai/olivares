-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_channel_subscription_guard_ins
BEFORE INSERT ON sessions_channel_subscription
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
	SELECT RAISE(ABORT, 'olivares: invalid ChannelSubscription shape')
	WHERE NEW.version < 1 OR NEW.updated_at < NEW.created_at OR NEW.generation < 1
		OR NEW.subscriber_kind NOT IN ('user','user_group','agent','agent_group','session')
		OR (NEW.subscriber_kind = 'session' AND
			(length(NEW.subscriber_ref) <> 40 OR substr(NEW.subscriber_ref,1,4) <> 'osn_'
				OR substr(NEW.subscriber_ref,13,1) <> '-'
				OR substr(NEW.subscriber_ref,18,1) <> '-'
				OR substr(NEW.subscriber_ref,19,1) <> '7'
				OR substr(NEW.subscriber_ref,23,1) <> '-'
				OR substr(NEW.subscriber_ref,24,1) NOT IN ('8','9','a','b')
				OR substr(NEW.subscriber_ref,28,1) <> '-'
				OR length(replace(substr(NEW.subscriber_ref,5),'-','')) <> 32
				OR replace(substr(NEW.subscriber_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.subscriber_kind <> 'session' AND
			(length(NEW.subscriber_ref) <> 36
				OR substr(NEW.subscriber_ref,9,1) <> '-'
				OR substr(NEW.subscriber_ref,14,1) <> '-'
				OR substr(NEW.subscriber_ref,15,1) <> '7'
				OR substr(NEW.subscriber_ref,19,1) <> '-'
				OR substr(NEW.subscriber_ref,20,1) NOT IN ('8','9','a','b')
				OR substr(NEW.subscriber_ref,24,1) <> '-'
				OR length(replace(NEW.subscriber_ref,'-','')) <> 32
				OR replace(NEW.subscriber_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR NEW.mode NOT IN ('all','mentions','critical','none')
		OR NEW.wake NOT IN ('none','primary','all')
		OR NEW.state NOT IN ('active','paused','revoked')
		OR (NEW.filter_json IS NULL) IS NOT (NEW.filter_hash IS NULL)
		OR (NEW.filter_json IS NOT NULL AND
			(NOT json_valid(NEW.filter_json) OR length(CAST(NEW.filter_json AS BLOB)) > 65536
				OR length(NEW.filter_hash) <> 32))
		OR (NEW.mode = 'none' AND (NEW.wake <> 'none' OR NEW.required_for_critical))
		OR (NEW.generation = 1) IS NOT (NEW.supersedes_id IS NULL)
		OR NEW.supersedes_id IS NEW.id;
	SELECT RAISE(ABORT, 'olivares: ChannelSubscription channel crosses tenant/workspace')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_channel c WHERE c.id = NEW.channel_id
		AND c.tenant_id = NEW.tenant_id AND c.workspace_id = NEW.workspace_id);
	SELECT RAISE(ABORT, 'olivares: ChannelSubscription predecessor lineage is not serialized')
	WHERE NEW.generation > 1 AND NOT EXISTS (
		SELECT 1 FROM sessions_channel_subscription p
		WHERE p.id = NEW.supersedes_id AND p.tenant_id = NEW.tenant_id
			AND p.workspace_id = NEW.workspace_id AND p.channel_id = NEW.channel_id
			AND p.subscriber_kind = NEW.subscriber_kind AND p.subscriber_ref = NEW.subscriber_ref
			AND p.generation + 1 = NEW.generation AND p.state = 'revoked');
END;
