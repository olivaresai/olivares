-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_channel_route_guard_upd
BEFORE UPDATE ON sessions_channel_route
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: ChannelRoute immutable generation changed')
	WHERE NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id
		OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at
		OR NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at
		OR NEW.route_key IS NOT OLD.route_key OR NEW.generation IS NOT OLD.generation
		OR NEW.priority IS NOT OLD.priority OR NEW.source_kind IS NOT OLD.source_kind
		OR NEW.event_type IS NOT OLD.event_type OR NEW.message_kind IS NOT OLD.message_kind
		OR NEW.minimum_urgency IS NOT OLD.minimum_urgency
		OR NEW.label_match_json IS NOT OLD.label_match_json
		OR NEW.target_channel_id IS NOT OLD.target_channel_id
		OR NEW.audience_kind IS NOT OLD.audience_kind OR NEW.audience_ref IS NOT OLD.audience_ref
		OR NEW.ack_policy IS NOT OLD.ack_policy OR NEW.wake_policy IS NOT OLD.wake_policy
		OR NEW.catch_all IS NOT OLD.catch_all OR NEW.supersedes_id IS NOT OLD.supersedes_id
		OR (OLD.state <> NEW.state AND NOT (OLD.state = 'active' AND NEW.state = 'disabled'))
		OR (OLD.state = 'disabled' AND NEW.state <> OLD.state);
	SELECT RAISE(ABORT, 'olivares: invalid ChannelRoute state')
	WHERE NEW.state NOT IN ('active','disabled')
		OR (NEW.audience_kind IN ('user_group','agent_group')) IS NOT
			(NEW.audience_ref IS NOT NULL)
		OR (NEW.audience_ref IS NOT NULL AND
			(length(NEW.audience_ref) <> 36
				OR substr(NEW.audience_ref,9,1) <> '-' OR substr(NEW.audience_ref,14,1) <> '-'
				OR substr(NEW.audience_ref,15,1) <> '7'
				OR substr(NEW.audience_ref,19,1) <> '-'
				OR substr(NEW.audience_ref,20,1) NOT IN ('8','9','a','b')
				OR substr(NEW.audience_ref,24,1) <> '-'
				OR length(replace(NEW.audience_ref,'-','')) <> 32
				OR replace(NEW.audience_ref,'-','') GLOB '*[^0-9a-f]*'));
END;
