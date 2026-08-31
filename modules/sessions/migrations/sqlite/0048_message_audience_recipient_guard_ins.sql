-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_message_audience_recipient_guard_ins
BEFORE INSERT ON sessions_message_audience_recipient
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
	SELECT RAISE(ABORT, 'olivares: invalid MessageAudienceRecipient contribution')
	WHERE NEW.version <> 1 OR NEW.recipient_kind NOT IN ('user','agent','session')
		OR (NEW.recipient_kind = 'session' AND
			(length(NEW.recipient_ref) <> 40 OR substr(NEW.recipient_ref,1,4) <> 'osn_'))
		OR (NEW.recipient_kind <> 'session' AND length(NEW.recipient_ref) <> 36)
		OR NEW.recipient_epoch < 1 OR NEW.wake_policy NOT IN ('none','primary','all')
		OR NOT json_valid(NEW.route_reasons_json) OR json_type(NEW.route_reasons_json) <> 'array'
		OR json_array_length(NEW.route_reasons_json) NOT BETWEEN 1 AND 32
		OR EXISTS (SELECT 1 FROM json_each(NEW.route_reasons_json) r
			WHERE r.type <> 'text' OR length(r.value) NOT BETWEEN 1 AND 128
				OR r.value GLOB '*[^a-z0-9._-]*')
		OR EXISTS (SELECT 1 FROM json_each(NEW.route_reasons_json) a
			JOIN json_each(NEW.route_reasons_json) b ON CAST(b.key AS INTEGER) = CAST(a.key AS INTEGER) + 1
			WHERE a.value >= b.value)
		OR NEW.selector_kind NOT IN ('user','user_group','agent','agent_group','session','subscribers','workspace_members')
		OR NEW.selector_wake_policy NOT IN ('none','primary','all')
		OR (NEW.selector_kind IN ('subscribers','workspace_members')) IS NOT (NEW.selector_ref IS NULL)
		OR NEW.directory_epoch < 1 OR NEW.channel_acl_revision < 1 OR NEW.route_revision < 1
		OR NEW.subscription_revision < 1
		OR NEW.causal_kind NOT IN ('direct','user_group','agent_group','workspace_member','subscriber')
		OR length(CAST(NEW.causal_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR (NEW.causal_fact_kind IS NULL) IS NOT (NEW.causal_fact_id IS NULL)
		OR (NEW.causal_fact_kind IS NULL) IS NOT (NEW.causal_fact_version IS NULL)
		OR (NEW.causal_fact_id IS NOT NULL AND
			(NEW.causal_fact_id NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.causal_fact_id,'-','')) <> 32
				OR replace(NEW.causal_fact_id,'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.causal_fact_version IS NOT NULL AND NEW.causal_fact_version < 1)
		OR (NEW.recipient_kind = 'session' AND
			(NEW.observed_session_sid IS NULL OR NEW.observed_claim_fence IS NULL
				OR NEW.observed_session_sid IS NOT NEW.recipient_ref
				OR NEW.observed_claim_fence < 1))
		OR (NEW.recipient_kind <> 'session' AND
			(NEW.observed_session_sid IS NOT NULL OR NEW.observed_claim_fence IS NOT NULL))
		OR (NEW.subscription_id IS NULL) IS NOT (NEW.subscription_generation IS NULL)
		OR (NEW.subscription_generation IS NOT NULL AND NEW.subscription_generation < 1)
		OR (NEW.route_rule_id IS NULL) IS NOT (NEW.route_rule_generation IS NULL)
		OR (NEW.route_rule_generation IS NOT NULL AND NEW.route_rule_generation < 1)
		OR length(NEW.causal_arc_hash) <> 32;
	SELECT RAISE(ABORT, 'olivares: audience contribution typed reference is non-canonical')
	FROM (
		SELECT NEW.recipient_kind AS kind, NEW.recipient_ref AS ref
		UNION ALL SELECT NEW.selector_kind, NEW.selector_ref
		UNION ALL SELECT NEW.original_subscriber_kind, NEW.original_subscriber_ref
	) refs
	WHERE (refs.kind = 'session' AND
			(refs.ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(refs.ref,5),'-','')) <> 32
				OR replace(substr(refs.ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (refs.kind IN ('user','user_group','agent','agent_group') AND
			(refs.ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(refs.ref,'-','')) <> 32
				OR replace(refs.ref,'-','') GLOB '*[^0-9a-f]*'));
	SELECT RAISE(ABORT, 'olivares: direct audience causality is inconsistent')
	WHERE NEW.causal_kind = 'direct' AND NOT (
		NEW.causal_fact_kind IS NULL AND NEW.subscription_id IS NULL
		AND NEW.causal_ref = NEW.selector_ref AND NEW.causal_ref = NEW.recipient_ref
		AND ((NEW.selector_kind = 'user' AND NEW.recipient_kind = 'user')
			OR (NEW.selector_kind = 'agent' AND NEW.recipient_kind = 'agent')
			OR (NEW.selector_kind = 'session' AND NEW.recipient_kind = 'session')));
	SELECT RAISE(ABORT, 'olivares: group audience causality is inconsistent')
	WHERE (NEW.causal_kind = 'user_group' AND
			(NEW.selector_kind IS NOT 'user_group' OR NEW.recipient_kind IS NOT 'user'
				OR NEW.causal_ref IS NOT NEW.selector_ref
				OR NEW.causal_fact_kind IS NOT 'core.user_group_member'
				OR NEW.causal_fact_id IS NULL OR NEW.causal_fact_version IS NULL))
		OR (NEW.causal_kind = 'agent_group' AND
			(NEW.selector_kind IS NOT 'agent_group' OR NEW.recipient_kind IS NOT 'agent'
				OR NEW.causal_ref IS NOT NEW.selector_ref
				OR NEW.causal_fact_kind IS NOT 'core.agent_group_member'
				OR NEW.causal_fact_id IS NULL OR NEW.causal_fact_version IS NULL))
		OR (NEW.causal_kind = 'workspace_member' AND
			(NEW.selector_kind IS NOT 'workspace_members' OR NEW.selector_ref IS NOT NULL
				OR NEW.causal_ref IS NOT NEW.workspace_id
				OR NOT (((NEW.recipient_kind = 'user'
						AND NEW.causal_fact_kind = 'core.membership')
					OR (NEW.recipient_kind = 'agent'
						AND NEW.causal_fact_kind = 'core.agent')) IS TRUE)
				OR NEW.causal_fact_id IS NULL OR NEW.causal_fact_version IS NULL));
	SELECT RAISE(ABORT, 'olivares: subscriber audience causality is incomplete')
	WHERE NEW.causal_kind = 'subscriber' AND
		(NEW.selector_kind <> 'subscribers' OR NEW.subscription_id IS NULL
			OR NEW.original_subscriber_kind IS NULL OR NEW.original_subscriber_ref IS NULL
			OR NEW.original_subscriber_kind NOT IN ('user','user_group','agent','agent_group','session')
			OR (NEW.original_subscriber_kind = 'session' AND
				(length(NEW.original_subscriber_ref) <> 40
					OR substr(NEW.original_subscriber_ref,1,4) <> 'osn_'))
			OR (NEW.original_subscriber_kind <> 'session'
				AND length(NEW.original_subscriber_ref) <> 36)
			OR NEW.causal_ref <> NEW.original_subscriber_ref
			OR (NEW.original_subscriber_kind = 'user' AND NOT
				(NEW.recipient_kind = 'user' AND NEW.recipient_ref = NEW.original_subscriber_ref
					AND NEW.causal_fact_kind IS NULL))
			OR (NEW.original_subscriber_kind = 'agent' AND NOT
				(NEW.recipient_kind = 'agent' AND NEW.recipient_ref = NEW.original_subscriber_ref
					AND NEW.causal_fact_kind IS NULL))
			OR (NEW.original_subscriber_kind = 'session' AND NOT
				(NEW.recipient_kind = 'session' AND NEW.recipient_ref = NEW.original_subscriber_ref
					AND NEW.causal_fact_kind IS NULL))
			OR (NEW.original_subscriber_kind = 'user_group' AND
				(NEW.recipient_kind IS NOT 'user'
					OR NEW.causal_fact_kind IS NOT 'core.user_group_member'
					OR NEW.causal_fact_id IS NULL OR NEW.causal_fact_version IS NULL))
			OR (NEW.original_subscriber_kind = 'agent_group' AND
				(NEW.recipient_kind IS NOT 'agent'
					OR NEW.causal_fact_kind IS NOT 'core.agent_group_member'
					OR NEW.causal_fact_id IS NULL OR NEW.causal_fact_version IS NULL)));
	SELECT RAISE(ABORT, 'olivares: non-subscriber contribution carries subscription provenance')
	WHERE NEW.causal_kind <> 'subscriber' AND
		(NEW.subscription_id IS NOT NULL OR NEW.original_subscriber_kind IS NOT NULL
			OR NEW.original_subscriber_ref IS NOT NULL);
	SELECT RAISE(ABORT, 'olivares: audience contribution crosses normalized lineage')
	WHERE NOT EXISTS (SELECT 1
		FROM sessions_message_audience a
		JOIN sessions_message m ON m.id = a.message_id AND m.tenant_id = a.tenant_id
		JOIN sessions_message_delivery d ON d.id = NEW.message_delivery_id
			AND d.tenant_id = a.tenant_id AND d.message_id = a.message_id
		WHERE a.id = NEW.message_audience_id AND a.tenant_id = NEW.tenant_id
			AND a.workspace_id = NEW.workspace_id AND m.workspace_id = NEW.workspace_id
			AND m.state = 'draft'
			AND d.workspace_id = NEW.workspace_id
			AND a.selector_kind = NEW.selector_kind AND a.selector_ref IS NEW.selector_ref
			AND a.selector_required = NEW.selector_required
			AND a.selector_wake_policy = NEW.selector_wake_policy
			AND a.directory_epoch = NEW.directory_epoch
			AND a.channel_acl_revision = NEW.channel_acl_revision
			AND a.route_revision = NEW.route_revision
			AND a.subscription_revision = NEW.subscription_revision
			AND (a.route_rule_id IS NULL OR a.route_rule_id = NEW.route_rule_id)
			AND d.recipient_kind = NEW.recipient_kind AND d.recipient_ref = NEW.recipient_ref
			AND d.recipient_epoch = NEW.recipient_epoch);
	SELECT RAISE(ABORT, 'olivares: audience contribution route provenance crosses generation')
	WHERE NEW.route_rule_id IS NOT NULL AND NOT EXISTS (
		SELECT 1 FROM sessions_channel_route r
		JOIN sessions_message_audience a ON a.id = NEW.message_audience_id
			AND a.tenant_id = NEW.tenant_id AND a.workspace_id = NEW.workspace_id
		JOIN sessions_message m ON m.id = a.message_id AND m.tenant_id = a.tenant_id
			AND m.workspace_id = a.workspace_id
		WHERE r.id = NEW.route_rule_id AND r.tenant_id = NEW.tenant_id
			AND r.workspace_id = NEW.workspace_id AND r.generation = NEW.route_rule_generation
			AND r.target_channel_id = m.channel_id);
	SELECT RAISE(ABORT, 'olivares: subscriber provenance crosses generation')
	WHERE NEW.subscription_id IS NOT NULL AND NOT EXISTS (
		SELECT 1 FROM sessions_channel_subscription s
		JOIN sessions_message_audience a ON a.id = NEW.message_audience_id
			AND a.tenant_id = NEW.tenant_id AND a.workspace_id = NEW.workspace_id
		JOIN sessions_message m ON m.id = a.message_id AND m.tenant_id = a.tenant_id
			AND m.workspace_id = a.workspace_id
		WHERE s.id = NEW.subscription_id AND s.tenant_id = NEW.tenant_id
			AND s.workspace_id = NEW.workspace_id AND s.generation = NEW.subscription_generation
			AND s.channel_id = m.channel_id
			AND s.subscriber_kind = NEW.original_subscriber_kind
			AND s.subscriber_ref = NEW.original_subscriber_ref);
END;
