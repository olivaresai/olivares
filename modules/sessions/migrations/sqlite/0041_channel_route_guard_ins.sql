-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_channel_route_guard_ins
BEFORE INSERT ON sessions_channel_route
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
	SELECT RAISE(ABORT, 'olivares: invalid ChannelRoute shape')
	WHERE NEW.version < 1 OR NEW.updated_at < NEW.created_at
		OR length(NEW.route_key) NOT BETWEEN 1 AND 128 OR NEW.route_key GLOB '*[^a-z0-9._-]*'
		OR NEW.generation < 1 OR NEW.priority < 0
		OR NEW.source_kind NOT IN ('user_message','work_event','system_event','protocol')
		OR (NEW.event_type IS NOT NULL AND
			(length(NEW.event_type) NOT BETWEEN 1 AND 256 OR NEW.event_type GLOB '*[^a-z0-9._-]*'))
		OR (NEW.message_kind IS NOT NULL AND NEW.message_kind NOT IN
			('notice','announcement','request','decision_request','handoff_offer','system'))
		OR (NEW.minimum_urgency IS NOT NULL AND NEW.minimum_urgency NOT IN ('normal','high','critical'))
		OR (NEW.label_match_json IS NOT NULL AND
			(NOT json_valid(NEW.label_match_json) OR json_type(NEW.label_match_json) <> 'object'
				OR length(CAST(NEW.label_match_json AS BLOB)) > 8192
				OR (SELECT count(*) FROM json_each(CASE
					WHEN json_valid(NEW.label_match_json) THEN NEW.label_match_json ELSE '{}' END))
					NOT BETWEEN 1 AND 32
				OR EXISTS (SELECT 1 FROM json_each(CASE
					WHEN json_valid(NEW.label_match_json) THEN NEW.label_match_json ELSE '{}' END) AS label
					WHERE length(CAST(label.key AS TEXT)) NOT BETWEEN 1 AND 128
						OR CAST(label.key AS TEXT) GLOB '*[^a-z0-9._-]*'
						OR label.type <> 'text'
						OR length(CAST(label.value AS TEXT)) NOT BETWEEN 1 AND 128
						OR CAST(label.value AS TEXT) GLOB '*[^a-z0-9._-]*')))
		OR NEW.audience_kind NOT IN ('subscribers','user_group','agent_group','workspace_members')
		OR NEW.ack_policy NOT IN ('none','each_required','quorum')
		OR NEW.wake_policy NOT IN ('none','primary','all','inherit')
		OR NEW.state NOT IN ('active','disabled')
		OR (NEW.catch_all AND (NEW.event_type IS NOT NULL OR NEW.message_kind IS NOT NULL
			OR NEW.minimum_urgency IS NOT NULL OR NEW.label_match_json IS NOT NULL))
		OR (NOT NEW.catch_all AND NEW.source_kind = 'user_message'
			AND (NEW.event_type IS NOT NULL OR NEW.message_kind IS NULL))
		OR (NOT NEW.catch_all AND NEW.source_kind <> 'user_message'
			AND (NEW.event_type IS NULL OR NEW.message_kind IS NOT NULL OR NEW.minimum_urgency IS NOT NULL))
		OR (NEW.audience_kind IN ('user_group','agent_group')) IS NOT (NEW.audience_ref IS NOT NULL)
		OR (NEW.audience_ref IS NOT NULL AND
			(length(NEW.audience_ref) <> 36
				OR substr(NEW.audience_ref,9,1) <> '-' OR substr(NEW.audience_ref,14,1) <> '-'
				OR substr(NEW.audience_ref,15,1) <> '7'
				OR substr(NEW.audience_ref,19,1) <> '-'
				OR substr(NEW.audience_ref,20,1) NOT IN ('8','9','a','b')
				OR substr(NEW.audience_ref,24,1) <> '-'
				OR length(replace(NEW.audience_ref,'-','')) <> 32
				OR replace(NEW.audience_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.generation = 1) IS NOT (NEW.supersedes_id IS NULL)
		OR NEW.supersedes_id IS NEW.id;
	SELECT RAISE(ABORT, 'olivares: ChannelRoute target crosses tenant/workspace')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_channel c WHERE c.id = NEW.target_channel_id
		AND c.tenant_id = NEW.tenant_id AND c.workspace_id = NEW.workspace_id);
	SELECT RAISE(ABORT, 'olivares: ChannelRoute predecessor lineage is not serialized')
	WHERE NEW.generation > 1 AND NOT EXISTS (
		SELECT 1 FROM sessions_channel_route p
		WHERE p.id = NEW.supersedes_id AND p.tenant_id = NEW.tenant_id
			AND p.workspace_id = NEW.workspace_id AND p.route_key = NEW.route_key
			AND p.generation + 1 = NEW.generation AND p.state = 'disabled');
END;
