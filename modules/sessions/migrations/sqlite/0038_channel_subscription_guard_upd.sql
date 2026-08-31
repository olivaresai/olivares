-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_channel_subscription_guard_upd
BEFORE UPDATE ON sessions_channel_subscription
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: ChannelSubscription immutable generation changed')
	WHERE NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id
		OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at
		OR NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at
		OR NEW.channel_id IS NOT OLD.channel_id OR NEW.subscriber_kind IS NOT OLD.subscriber_kind
		OR NEW.subscriber_ref IS NOT OLD.subscriber_ref OR NEW.generation IS NOT OLD.generation
		OR NEW.mode IS NOT OLD.mode OR NEW.wake IS NOT OLD.wake
		OR NEW.required_for_critical IS NOT OLD.required_for_critical
		OR NEW.filter_json IS NOT OLD.filter_json OR NEW.filter_hash IS NOT OLD.filter_hash
		OR NEW.supersedes_id IS NOT OLD.supersedes_id
		OR (OLD.state <> NEW.state AND NOT (
			(OLD.state = 'active' AND NEW.state IN ('paused','revoked')) OR
			(OLD.state = 'paused' AND NEW.state IN ('active','revoked'))))
		OR (OLD.state = 'revoked' AND NEW.state <> OLD.state);
	SELECT RAISE(ABORT, 'olivares: invalid ChannelSubscription state')
	WHERE NEW.state NOT IN ('active','paused','revoked');
	SELECT RAISE(ABORT, 'olivares: ChannelSubscription subscriber reference is non-canonical')
	WHERE (NEW.subscriber_kind = 'session' AND
			(NEW.subscriber_ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(NEW.subscriber_ref,5),'-','')) <> 32
				OR replace(substr(NEW.subscriber_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.subscriber_kind IN ('user','user_group','agent','agent_group') AND
			(NEW.subscriber_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.subscriber_ref,'-','')) <> 32
				OR replace(NEW.subscriber_ref,'-','') GLOB '*[^0-9a-f]*'));
END;
