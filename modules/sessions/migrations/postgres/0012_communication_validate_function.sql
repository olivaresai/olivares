-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- K3 communication-plane vocabulary, shape, transition and lineage guards.
-- Cryptographic digests are required at their exact byte size but are never
-- fabricated or recomputed by SQL; trusted Go validators bind their contents.
CREATE OR REPLACE FUNCTION olivares_sessions_communication_validate()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
DECLARE
	required_count bigint;
	prior_tenant text;
	user_tombstone_found boolean;
	row_data jsonb;
	ref_pair record;
	ref_kind text;
	ref_value text;
	parent_message_id text;
BEGIN
	IF NEW.id IS NULL
		OR NEW.id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR NEW.tenant_id IS NULL
		OR NEW.tenant_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR NEW.tenant_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff'
		OR NEW.workspace_id IS NULL
		OR NEW.workspace_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR NEW.version < 1 OR NEW.created_at IS NULL THEN
		RAISE EXCEPTION 'olivares: invalid sessions communication entity identity'
			USING ERRCODE = '23514';
	END IF;

	IF TG_TABLE_NAME IN (
		'sessions_message_audience','sessions_message_audience_recipient',
		'sessions_message_ack','sessions_decision_response','sessions_communication_command'
	) THEN
		IF NEW.version <> 1 THEN
			RAISE EXCEPTION 'olivares: append-only communication version must be one'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' THEN
			RAISE EXCEPTION 'olivares: append-only communication row is immutable'
				USING ERRCODE = '23514';
		END IF;
	ELSE
		IF NEW.updated_at IS NULL OR NEW.updated_at < NEW.created_at THEN
			RAISE EXCEPTION 'olivares: invalid mutable communication timestamps'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			NEW.id IS DISTINCT FROM OLD.id
			OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
			OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
			OR NEW.created_at IS DISTINCT FROM OLD.created_at
			OR NEW.version <> OLD.version + 1
			OR NEW.updated_at < OLD.updated_at
		) THEN
			RAISE EXCEPTION 'olivares: mutable communication identity, version or time changed illegally'
				USING ERRCODE = '23514';
		END IF;
	END IF;

	row_data := to_jsonb(NEW);
	FOR ref_pair IN
		SELECT * FROM (VALUES
			('subject_kind','subject_ref'), ('subscriber_kind','subscriber_ref'),
			('owner_kind','owner_ref'), ('sender_kind','sender_ref'),
			('selector_kind','selector_ref'), ('recipient_kind','recipient_ref'),
			('original_subscriber_kind','original_subscriber_ref'),
			('reader_kind','reader_ref'), ('actor_kind','actor_ref'),
			('on_behalf_of_kind','on_behalf_of_ref'), ('requester_kind','requester_ref'),
			('from_kind','from_ref'), ('to_kind','to_ref'),
			('granted_by_kind','granted_by_ref'), ('revoked_by_kind','revoked_by_ref'),
			('audience_kind','audience_ref')
		) AS pairs(kind_column, ref_column)
	LOOP
		IF row_data ? ref_pair.kind_column AND row_data ? ref_pair.ref_column THEN
			ref_kind := row_data ->> ref_pair.kind_column;
			ref_value := row_data ->> ref_pair.ref_column;
			IF ref_value IS NOT NULL AND (
				(ref_kind = 'session' AND ref_value !~
					'^osn_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
				OR (ref_kind IN ('user','user_group','agent','agent_group') AND ref_value !~
					'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
				OR (ref_kind = 'system' AND
					(octet_length(ref_value) NOT BETWEEN 1 AND 512
						OR ref_value <> btrim(ref_value) OR ref_value ~ E'[\\r\\n]'))
			) THEN
				RAISE EXCEPTION 'olivares: non-canonical typed communication reference'
					USING ERRCODE = '23514';
			END IF;
		END IF;
	END LOOP;

	CASE TG_TABLE_NAME
	WHEN 'sessions_channel' THEN
		IF NEW.slug !~ '^[a-z0-9._-]{1,128}$'
			OR octet_length(NEW.name) NOT BETWEEN 1 AND 256
			OR (NEW.description IS NOT NULL AND octet_length(NEW.description) NOT BETWEEN 1 AND 4096)
			OR NEW.kind NOT IN ('coordination','work','incident','announcement','private')
			OR NEW.state NOT IN ('active','archived')
			OR NEW.sensitivity NOT IN ('internal','restricted')
			OR NEW.content_protection NOT IN ('storage','application_sealed')
			OR NEW.protection_generation < 1
			OR NEW.default_ack_policy NOT IN ('none','each_required','quorum')
			OR NEW.default_ack_timeout_ms < 0
			OR (NEW.default_ack_policy = 'none') <> (NEW.default_ack_timeout_ms = 0)
			OR NEW.default_wake NOT IN ('none','primary','all')
			OR (NEW.retention_policy_ref IS NOT NULL
				AND (octet_length(NEW.retention_policy_ref) NOT BETWEEN 1 AND 512
					OR NEW.retention_policy_ref <> btrim(NEW.retention_policy_ref)
					OR NEW.retention_policy_ref ~ E'[\\r\\n]'))
			OR NEW.max_fanout < 1 OR NEW.max_automation_depth < 0
			OR NEW.acl_revision < 1 OR NEW.route_revision < 1 OR NEW.subscription_revision < 1
			OR (NEW.sensitivity = 'restricted' AND NEW.content_protection <> 'application_sealed') THEN
			RAISE EXCEPTION 'olivares: invalid Channel shape' USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			NEW.slug IS DISTINCT FROM OLD.slug OR NEW.kind IS DISTINCT FROM OLD.kind
			OR NEW.acl_revision < OLD.acl_revision OR NEW.route_revision < OLD.route_revision
			OR NEW.subscription_revision < OLD.subscription_revision
			OR (OLD.state = 'archived' AND NEW.state <> OLD.state)
			OR (OLD.state <> NEW.state AND NOT (OLD.state = 'active' AND NEW.state = 'archived'))
			OR (OLD.sensitivity = 'restricted' AND NEW.sensitivity <> 'restricted')
			OR (OLD.content_protection = 'application_sealed'
				AND NEW.content_protection <> 'application_sealed')
			OR NEW.protection_generation <> OLD.protection_generation +
				CASE WHEN NEW.sensitivity IS DISTINCT FROM OLD.sensitivity
					OR NEW.content_protection IS DISTINCT FROM OLD.content_protection
				THEN 1 ELSE 0 END
		) THEN
			RAISE EXCEPTION 'olivares: invalid Channel transition or protection generation'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_channel_grant' THEN
		IF NEW.subject_kind NOT IN ('user','user_group','agent','agent_group','session')
			OR (NEW.subject_kind = 'session'
				AND (length(NEW.subject_ref) <> 40 OR left(NEW.subject_ref, 4) <> 'osn_'))
			OR (NEW.subject_kind <> 'session' AND length(NEW.subject_ref) <> 36)
			OR NEW.generation < 1
			OR NOT (NEW.can_read OR NEW.can_write OR NEW.can_admin)
			OR NEW.state NOT IN ('active','revoked','expired')
			OR NEW.granted_by_kind NOT IN ('user','agent','session','system')
			OR octet_length(NEW.granted_by_ref) NOT BETWEEN 1 AND 512
			OR (NEW.granted_by_kind IN ('user','agent') AND NEW.granted_by_ref !~
				'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
			OR (NEW.granted_by_kind = 'session' AND NEW.granted_by_ref !~
				'^osn_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
			OR (NEW.granted_by_kind = 'system' AND
				(NEW.granted_by_ref <> btrim(NEW.granted_by_ref)
					OR NEW.granted_by_ref ~ E'[\\r\\n]'))
			OR (NEW.revoked_by_kind IS NULL) <> (NEW.revoked_by_ref IS NULL)
			OR (NEW.state = 'revoked') <> (NEW.revoked_by_kind IS NOT NULL)
			OR (NEW.revoked_by_kind IS NOT NULL
				AND NEW.revoked_by_kind NOT IN ('user','agent','session','system'))
			OR (NEW.revoked_by_ref IS NOT NULL
				AND octet_length(NEW.revoked_by_ref) NOT BETWEEN 1 AND 512)
			OR (NEW.revoked_by_kind IN ('user','agent') AND NEW.revoked_by_ref !~
				'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
			OR (NEW.revoked_by_kind = 'session' AND NEW.revoked_by_ref !~
				'^osn_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
			OR (NEW.revoked_by_kind = 'system' AND
				(NEW.revoked_by_ref <> btrim(NEW.revoked_by_ref)
					OR NEW.revoked_by_ref ~ E'[\\r\\n]'))
			OR (NEW.expires_at IS NOT NULL AND NEW.expires_at <= NEW.created_at)
			OR (NEW.state = 'expired'
				AND (NEW.expires_at IS NULL OR NEW.updated_at < NEW.expires_at))
			OR (NEW.generation = 1) <> (NEW.supersedes_id IS NULL)
			OR NEW.supersedes_id IS NOT DISTINCT FROM NEW.id THEN
			RAISE EXCEPTION 'olivares: invalid ChannelGrant shape' USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_channel c WHERE c.id = NEW.channel_id
			AND c.tenant_id = NEW.tenant_id AND c.workspace_id = NEW.workspace_id) THEN
			RAISE EXCEPTION 'olivares: ChannelGrant channel crosses tenant/workspace'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'INSERT' AND NEW.generation > 1 AND NOT EXISTS (
			SELECT 1 FROM sessions_channel_grant p WHERE p.id = NEW.supersedes_id
				AND p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
				AND p.channel_id = NEW.channel_id AND p.subject_kind = NEW.subject_kind
				AND p.subject_ref = NEW.subject_ref AND p.generation + 1 = NEW.generation
				AND p.state IN ('revoked','expired')) THEN
			RAISE EXCEPTION 'olivares: ChannelGrant predecessor generation is not serialized'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			(to_jsonb(NEW) - ARRAY['version','updated_at','state','revoked_by_kind','revoked_by_ref'])
				IS DISTINCT FROM
			(to_jsonb(OLD) - ARRAY['version','updated_at','state','revoked_by_kind','revoked_by_ref'])
			OR (OLD.state <> NEW.state AND NOT
				(OLD.state = 'active' AND NEW.state IN ('revoked','expired')))
			OR OLD.state IN ('revoked','expired')
		) THEN
			RAISE EXCEPTION 'olivares: ChannelGrant immutable generation or state changed'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_channel_subscription' THEN
		IF NEW.subscriber_kind NOT IN ('user','user_group','agent','agent_group','session')
			OR (NEW.subscriber_kind = 'session'
				AND (length(NEW.subscriber_ref) <> 40 OR left(NEW.subscriber_ref, 4) <> 'osn_'))
			OR (NEW.subscriber_kind <> 'session' AND length(NEW.subscriber_ref) <> 36)
			OR NEW.generation < 1
			OR NEW.mode NOT IN ('all','mentions','critical','none')
			OR NEW.wake NOT IN ('none','primary','all')
			OR NEW.state NOT IN ('active','paused','revoked')
			OR (NEW.filter_json IS NULL) <> (NEW.filter_hash IS NULL)
			OR (NEW.filter_json IS NOT NULL AND
				(NEW.filter_json::jsonb IS NULL OR octet_length(NEW.filter_json::text) > 65536
					OR octet_length(NEW.filter_hash) <> 32))
			OR (NEW.mode = 'none' AND (NEW.wake <> 'none' OR NEW.required_for_critical))
			OR (NEW.generation = 1) <> (NEW.supersedes_id IS NULL)
			OR NEW.supersedes_id IS NOT DISTINCT FROM NEW.id THEN
			RAISE EXCEPTION 'olivares: invalid ChannelSubscription shape' USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_channel c WHERE c.id = NEW.channel_id
			AND c.tenant_id = NEW.tenant_id AND c.workspace_id = NEW.workspace_id) THEN
			RAISE EXCEPTION 'olivares: ChannelSubscription channel crosses tenant/workspace'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'INSERT' AND NEW.generation > 1 AND NOT EXISTS (
			SELECT 1 FROM sessions_channel_subscription p WHERE p.id = NEW.supersedes_id
				AND p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
				AND p.channel_id = NEW.channel_id AND p.subscriber_kind = NEW.subscriber_kind
				AND p.subscriber_ref = NEW.subscriber_ref AND p.generation + 1 = NEW.generation
				AND p.state = 'revoked') THEN
			RAISE EXCEPTION 'olivares: ChannelSubscription predecessor generation is not serialized'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			(to_jsonb(NEW) - ARRAY['version','updated_at','state']) IS DISTINCT FROM
			(to_jsonb(OLD) - ARRAY['version','updated_at','state'])
			OR (OLD.state <> NEW.state AND NOT (
				(OLD.state = 'active' AND NEW.state IN ('paused','revoked')) OR
				(OLD.state = 'paused' AND NEW.state IN ('active','revoked'))))
			OR (OLD.state = 'revoked' AND NEW.state <> OLD.state)
		) THEN
			RAISE EXCEPTION 'olivares: ChannelSubscription immutable generation or state changed'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_channel_label_definition' THEN
		IF NEW.key !~ '^[a-z0-9._-]{1,128}$' OR NEW.generation < 1
			OR NEW.classification <> 'non_sensitive' OR NEW.state NOT IN ('active','disabled')
			OR octet_length(NEW.values_hash) <> 32
			OR jsonb_typeof(NEW.allowed_values_json::jsonb) <> 'array'
			OR jsonb_array_length(NEW.allowed_values_json::jsonb) NOT BETWEEN 1 AND 64
			OR EXISTS (SELECT 1 FROM jsonb_array_elements(NEW.allowed_values_json::jsonb) v
				WHERE jsonb_typeof(v) <> 'string'
					OR (v #>> '{}') !~ '^[a-z0-9._-]{1,128}$')
			OR EXISTS (
				SELECT 1
				FROM jsonb_array_elements_text(NEW.allowed_values_json::jsonb)
					WITH ORDINALITY AS a(value, ordinal)
				JOIN jsonb_array_elements_text(NEW.allowed_values_json::jsonb)
					WITH ORDINALITY AS b(value, ordinal) ON b.ordinal = a.ordinal + 1
				WHERE a.value >= b.value) THEN
			RAISE EXCEPTION 'olivares: invalid ChannelLabelDefinition shape'
				USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_channel c WHERE c.id = NEW.channel_id
			AND c.tenant_id = NEW.tenant_id AND c.workspace_id = NEW.workspace_id) THEN
			RAISE EXCEPTION 'olivares: ChannelLabelDefinition channel crosses tenant/workspace'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'INSERT' AND (
			(NEW.generation = 1 AND EXISTS (SELECT 1 FROM sessions_channel_label_definition p
				WHERE p.tenant_id = NEW.tenant_id AND p.channel_id = NEW.channel_id AND p.key = NEW.key))
			OR (NEW.generation > 1 AND NOT EXISTS (
				SELECT 1 FROM sessions_channel_label_definition p
				WHERE p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
					AND p.channel_id = NEW.channel_id AND p.key = NEW.key
					AND p.generation = NEW.generation - 1 AND p.state = 'disabled'))
		) THEN
			RAISE EXCEPTION 'olivares: ChannelLabelDefinition generation is not serialized'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			(to_jsonb(NEW) - ARRAY['version','updated_at','state']) IS DISTINCT FROM
			(to_jsonb(OLD) - ARRAY['version','updated_at','state'])
			OR (OLD.state <> NEW.state AND NOT (OLD.state = 'active' AND NEW.state = 'disabled'))
			OR (OLD.state = 'disabled' AND NEW.state <> OLD.state)
		) THEN
			RAISE EXCEPTION 'olivares: ChannelLabelDefinition immutable generation changed'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_channel_route' THEN
		IF NEW.route_key !~ '^[a-z0-9._-]{1,128}$' OR NEW.generation < 1 OR NEW.priority < 0
			OR NEW.source_kind NOT IN ('user_message','work_event','system_event','protocol')
			OR (NEW.event_type IS NOT NULL AND NEW.event_type !~ '^[a-z0-9._-]{1,256}$')
			OR (NEW.message_kind IS NOT NULL AND NEW.message_kind NOT IN
				('notice','announcement','request','decision_request','handoff_offer','system'))
			OR (NEW.minimum_urgency IS NOT NULL
				AND NEW.minimum_urgency NOT IN ('normal','high','critical'))
			OR (NEW.label_match_json IS NOT NULL AND
				(jsonb_typeof(NEW.label_match_json::jsonb) <> 'object'
					OR octet_length(NEW.label_match_json::text) > 8192
					OR (SELECT count(*) FROM jsonb_object_keys(NEW.label_match_json::jsonb))
						NOT BETWEEN 1 AND 32
					OR EXISTS (
						SELECT 1 FROM jsonb_each(NEW.label_match_json::jsonb) AS label
						WHERE label.key !~ '^[a-z0-9._-]{1,128}$'
							OR jsonb_typeof(label.value) IS DISTINCT FROM 'string'
							OR COALESCE(label.value #>> '{}', '') !~ '^[a-z0-9._-]{1,128}$'
					)))
			OR NEW.audience_kind NOT IN ('subscribers','user_group','agent_group','workspace_members')
			OR NEW.ack_policy NOT IN ('none','each_required','quorum')
			OR NEW.wake_policy NOT IN ('none','primary','all','inherit')
			OR NEW.state NOT IN ('active','disabled')
			OR (NEW.catch_all AND (NEW.event_type IS NOT NULL OR NEW.message_kind IS NOT NULL
				OR NEW.minimum_urgency IS NOT NULL OR NEW.label_match_json IS NOT NULL))
			OR (NOT NEW.catch_all AND NEW.source_kind = 'user_message'
				AND (NEW.event_type IS NOT NULL OR NEW.message_kind IS NULL))
			OR (NOT NEW.catch_all AND NEW.source_kind <> 'user_message'
				AND (NEW.event_type IS NULL OR NEW.message_kind IS NOT NULL
					OR NEW.minimum_urgency IS NOT NULL))
			OR (NEW.audience_kind IN ('user_group','agent_group')) <> (NEW.audience_ref IS NOT NULL)
			OR (NEW.audience_ref IS NOT NULL AND NEW.audience_ref !~
				'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
			OR (NEW.generation = 1) <> (NEW.supersedes_id IS NULL)
			OR NEW.supersedes_id IS NOT DISTINCT FROM NEW.id THEN
			RAISE EXCEPTION 'olivares: invalid ChannelRoute shape' USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_channel c WHERE c.id = NEW.target_channel_id
			AND c.tenant_id = NEW.tenant_id AND c.workspace_id = NEW.workspace_id) THEN
			RAISE EXCEPTION 'olivares: ChannelRoute target crosses tenant/workspace'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'INSERT' AND NEW.generation > 1 AND NOT EXISTS (
			SELECT 1 FROM sessions_channel_route p WHERE p.id = NEW.supersedes_id
				AND p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
				AND p.route_key = NEW.route_key AND p.generation + 1 = NEW.generation
				AND p.state = 'disabled') THEN
			RAISE EXCEPTION 'olivares: ChannelRoute predecessor generation is not serialized'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			(to_jsonb(NEW) - ARRAY['version','updated_at','state']) IS DISTINCT FROM
			(to_jsonb(OLD) - ARRAY['version','updated_at','state'])
			OR (OLD.state <> NEW.state AND NOT (OLD.state = 'active' AND NEW.state = 'disabled'))
			OR (OLD.state = 'disabled' AND NEW.state <> OLD.state)
		) THEN
			RAISE EXCEPTION 'olivares: ChannelRoute immutable generation changed'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_communication_endpoint' THEN
		IF NEW.owner_kind NOT IN ('user','agent','session')
			OR (NEW.owner_kind = 'session'
				AND (length(NEW.owner_ref) <> 40 OR left(NEW.owner_ref, 4) <> 'osn_'))
			OR (NEW.owner_kind <> 'session' AND length(NEW.owner_ref) <> 36)
			OR NOT (NEW.provider_key IN
				('claude-channel','claude-stream-json','codex-app-server','a2a','mcp')
				OR NEW.provider_key ~ '^driver:[a-z0-9._-]{1,96}$')
			OR NEW.transport !~ '^[a-z0-9._-]{1,128}$'
			OR octet_length(NEW.endpoint_ref) NOT BETWEEN 1 AND 512
			OR NEW.endpoint_ref <> btrim(NEW.endpoint_ref) OR NEW.endpoint_ref ~ E'[\\r\\n]'
			OR (NEW.owner_kind = 'session') <> (NEW.session_sid IS NOT NULL)
			OR (NEW.session_sid IS NOT NULL AND NEW.session_sid <> NEW.owner_ref)
			OR NEW.capabilities_json::jsonb IS NULL
			OR octet_length(NEW.capabilities_json::text) > 65536
			OR NEW.support_level NOT IN ('stable','preview','experimental')
			OR NEW.priority < 0 OR NEW.state NOT IN ('active','stale','disabled')
			OR NEW.generation < 1
			OR (NEW.heartbeat_expires_at IS NOT NULL
				AND NEW.heartbeat_expires_at < NEW.created_at)
			OR (NEW.transport_fingerprint IS NOT NULL
				AND (octet_length(NEW.transport_fingerprint) NOT BETWEEN 1 AND 512
					OR NEW.transport_fingerprint <> btrim(NEW.transport_fingerprint)
					OR NEW.transport_fingerprint ~ E'[\\r\\n]'))
			OR (NEW.secret_ref IS NOT NULL
				AND (octet_length(NEW.secret_ref) NOT BETWEEN 1 AND 512
					OR NEW.secret_ref <> btrim(NEW.secret_ref)
					OR NEW.secret_ref ~ E'[\\r\\n]')) THEN
			RAISE EXCEPTION 'olivares: invalid CommunicationEndpoint shape'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'active' THEN
			PERFORM pg_advisory_xact_lock(hashtextextended(concat_ws('|',
				'sessions_communication_endpoint_active', NEW.tenant_id::text,
				NEW.provider_key, NEW.endpoint_ref), 0));
		END IF;
		IF TG_OP = 'INSERT' AND (
			(NEW.generation = 1 AND EXISTS (SELECT 1 FROM sessions_communication_endpoint p
				WHERE p.tenant_id = NEW.tenant_id AND p.provider_key = NEW.provider_key
					AND p.endpoint_ref = NEW.endpoint_ref))
			OR (NEW.generation > 1 AND NOT EXISTS (
				SELECT 1 FROM sessions_communication_endpoint p
				WHERE p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
					AND p.provider_key = NEW.provider_key AND p.endpoint_ref = NEW.endpoint_ref
					AND p.generation = NEW.generation - 1 AND p.state <> 'active'))
		) THEN
			RAISE EXCEPTION 'olivares: CommunicationEndpoint generation is not serialized'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'active' AND EXISTS (
			SELECT 1 FROM sessions_communication_endpoint p
			WHERE p.tenant_id = NEW.tenant_id AND p.provider_key = NEW.provider_key
				AND p.endpoint_ref = NEW.endpoint_ref
				AND p.state = 'active' AND p.id <> NEW.id) THEN
			RAISE EXCEPTION 'olivares: CommunicationEndpoint has multiple active generations'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			(to_jsonb(NEW) - ARRAY['version','updated_at','state','heartbeat_expires_at'])
				IS DISTINCT FROM
			(to_jsonb(OLD) - ARRAY['version','updated_at','state','heartbeat_expires_at'])
			OR (OLD.state <> NEW.state AND NOT (
				(OLD.state = 'active' AND NEW.state IN ('stale','disabled')) OR
				(OLD.state = 'stale' AND NEW.state IN ('active','disabled'))))
			OR (OLD.state = 'disabled' AND NEW.state <> OLD.state)
		) THEN
			RAISE EXCEPTION 'olivares: CommunicationEndpoint immutable generation changed'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_message' THEN
		PERFORM pg_advisory_xact_lock(hashtextextended(concat_ws('|',
			'sessions_message_publish', NEW.tenant_id::text, NEW.id::text), 0));
		IF (TG_OP = 'INSERT' AND
				(NEW.version <> 1 OR NEW.state <> 'draft' OR NEW.last_event_seq <> 0))
			OR NEW.kind NOT IN ('notice','announcement','request','decision_request','handoff_offer','system')
			OR NEW.state NOT IN ('draft','published','retracted','expired','discarded')
			OR NEW.sender_kind NOT IN ('user','agent','session','system')
			OR octet_length(NEW.sender_ref) NOT BETWEEN 1 AND 512
			OR (NEW.sender_kind = 'session'
				AND (length(NEW.sender_ref) <> 40 OR left(NEW.sender_ref, 4) <> 'osn_'))
			OR (NEW.sender_kind IN ('user','agent') AND length(NEW.sender_ref) <> 36)
			OR NEW.payload_encoding IS NULL
			OR NEW.payload_encoding NOT IN ('plain_json','sealed_v1')
			OR NEW.payload_schema IS DISTINCT FROM 'communication.message.v1'
			OR NEW.payload_digest IS NULL OR octet_length(NEW.payload_digest) <> 32
			OR NEW.payload_protection_generation IS NULL OR NEW.payload_protection_generation < 1
			OR (NEW.payload_encoding = 'plain_json' AND
				(NEW.payload_plain_json IS NULL OR NEW.payload_sealed_json IS NOT NULL
					OR NEW.payload_seal_key_version IS NOT NULL
					OR NEW.payload_digest_key_version IS NOT NULL
					OR jsonb_typeof(NEW.payload_plain_json::jsonb) <> 'object'
					OR octet_length(NEW.payload_plain_json::text) > 65536))
			OR (NEW.payload_encoding = 'sealed_v1' AND
				(NEW.payload_plain_json IS NOT NULL OR NEW.payload_sealed_json IS NULL
					OR jsonb_typeof(NEW.payload_sealed_json::jsonb) <> 'object'
					OR octet_length(NEW.payload_sealed_json::text) > 196608
					OR NEW.payload_seal_key_version IS NULL OR NEW.payload_digest_key_version IS NULL
					OR octet_length(NEW.payload_seal_key_version) NOT BETWEEN 1 AND 512
					OR octet_length(NEW.payload_digest_key_version) NOT BETWEEN 1 AND 512
					OR NEW.payload_seal_key_version <> btrim(NEW.payload_seal_key_version)
					OR NEW.payload_digest_key_version <> btrim(NEW.payload_digest_key_version)
					OR NEW.payload_seal_key_version ~ E'[\\r\\n]'
					OR NEW.payload_digest_key_version ~ E'[\\r\\n]'
					OR jsonb_typeof(NEW.payload_sealed_json::jsonb -> 'ciphertext')
						IS DISTINCT FROM 'string'
					OR COALESCE(length(NEW.payload_sealed_json::jsonb ->> 'ciphertext'), 0) = 0
					OR jsonb_typeof(NEW.payload_sealed_json::jsonb -> 'key_version')
						IS DISTINCT FROM 'string'
					OR NEW.payload_sealed_json::jsonb ->> 'key_version'
						IS DISTINCT FROM NEW.payload_seal_key_version))
			OR (NEW.labels_json IS NULL) <> (NEW.labels_hash IS NULL)
			OR (NEW.labels_json IS NOT NULL AND
				(jsonb_typeof(NEW.labels_json::jsonb) <> 'object'
					OR octet_length(NEW.labels_json::text) > 8192
					OR octet_length(NEW.labels_hash) <> 32
					OR (SELECT count(*) FROM jsonb_object_keys(NEW.labels_json::jsonb))
						NOT BETWEEN 1 AND 32
					OR EXISTS (
						SELECT 1 FROM jsonb_each(NEW.labels_json::jsonb) AS label
						WHERE label.key !~ '^[a-z0-9._-]{1,128}$'
							OR jsonb_typeof(label.value) IS DISTINCT FROM 'string'
							OR COALESCE(label.value #>> '{}', '') !~ '^[a-z0-9._-]{1,128}$'
					)))
			OR NEW.urgency NOT IN ('normal','high','critical')
			OR NEW.ack_policy NOT IN ('none','each_required','quorum')
			OR NEW.available_at < NEW.created_at OR NEW.automation_depth < 0 OR NEW.last_event_seq < 0
			OR (NEW.reply_to_id IS NULL AND NEW.thread_id <> NEW.id)
			OR NEW.reply_to_id IS NOT DISTINCT FROM NEW.id
			OR NEW.supersedes_id IS NOT DISTINCT FROM NEW.id
			OR (NEW.kind IN ('request','decision_request','handoff_offer') AND NEW.work_item_id IS NULL)
			OR (NEW.work_item_id IS NOT NULL AND NEW.last_event_seq <> 0)
			OR (NEW.work_item_id IS NULL AND NEW.state = 'draft' AND NEW.last_event_seq <> 0)
			OR (NEW.work_item_id IS NULL AND NEW.state = 'published' AND NEW.last_event_seq < 1)
			OR (NEW.work_item_id IS NULL AND NEW.state IN ('retracted','expired')
				AND NEW.last_event_seq < 2)
			OR (NEW.work_item_id IS NULL AND NEW.state = 'discarded' AND NEW.last_event_seq < 1)
			OR (NEW.ack_due_at IS NOT NULL AND NEW.ack_due_at < NEW.available_at)
			OR (NEW.expires_at IS NOT NULL AND NEW.expires_at <= NEW.available_at)
			OR (NEW.ack_due_at IS NOT NULL AND NEW.expires_at IS NOT NULL
				AND NEW.ack_due_at > NEW.expires_at)
			OR (NEW.ack_policy = 'none' AND (NEW.ack_quorum <> 0 OR NEW.ack_due_at IS NOT NULL))
			OR (NEW.ack_policy = 'each_required'
				AND (NEW.ack_quorum <> 0 OR NEW.ack_due_at IS NULL))
			OR (NEW.ack_policy = 'quorum'
				AND (NEW.ack_quorum < 1 OR NEW.ack_due_at IS NULL)) THEN
			RAISE EXCEPTION 'olivares: invalid Message envelope or protected payload'
				USING ERRCODE = '23514';
		END IF;
		IF (NEW.terminal_reason_encoding IS NULL AND (
				NEW.terminal_reason_plain_json IS NOT NULL
				OR NEW.terminal_reason_sealed_json IS NOT NULL
				OR NEW.terminal_reason_schema IS NOT NULL
				OR NEW.terminal_reason_digest IS NOT NULL
				OR NEW.terminal_reason_seal_key_version IS NOT NULL
				OR NEW.terminal_reason_digest_key_version IS NOT NULL
				OR NEW.terminal_reason_protection_generation IS NOT NULL))
			OR (NEW.terminal_reason_encoding IS NOT NULL AND NOT COALESCE((
				NEW.terminal_reason_encoding IN ('plain_json','sealed_v1')
				AND NEW.terminal_reason_encoding = NEW.payload_encoding
				AND NEW.terminal_reason_schema = 'communication.message-terminal-reason.v1'
				AND octet_length(NEW.terminal_reason_digest) = 32
				AND NEW.terminal_reason_protection_generation =
					NEW.payload_protection_generation
				AND (
					(NEW.terminal_reason_encoding = 'plain_json'
						AND NEW.terminal_reason_plain_json IS NOT NULL
						AND jsonb_typeof(NEW.terminal_reason_plain_json::jsonb) = 'object'
						AND octet_length(NEW.terminal_reason_plain_json::text) <= 65536
						AND NEW.terminal_reason_sealed_json IS NULL
						AND NEW.terminal_reason_seal_key_version IS NULL
						AND NEW.terminal_reason_digest_key_version IS NULL)
					OR (NEW.terminal_reason_encoding = 'sealed_v1'
						AND NEW.terminal_reason_plain_json IS NULL
						AND NEW.terminal_reason_sealed_json IS NOT NULL
						AND jsonb_typeof(NEW.terminal_reason_sealed_json::jsonb) = 'object'
						AND octet_length(NEW.terminal_reason_sealed_json::text) <= 196608
						AND octet_length(NEW.terminal_reason_seal_key_version)
							BETWEEN 1 AND 512
						AND octet_length(NEW.terminal_reason_digest_key_version)
							BETWEEN 1 AND 512
						AND NEW.terminal_reason_seal_key_version =
							btrim(NEW.terminal_reason_seal_key_version)
						AND NEW.terminal_reason_digest_key_version =
							btrim(NEW.terminal_reason_digest_key_version)
						AND NEW.terminal_reason_seal_key_version !~ E'[\\r\\n]'
						AND NEW.terminal_reason_digest_key_version !~ E'[\\r\\n]'
						AND jsonb_typeof(NEW.terminal_reason_sealed_json::jsonb -> 'ciphertext') =
							'string'
						AND COALESCE(length(
							NEW.terminal_reason_sealed_json::jsonb ->> 'ciphertext'), 0) > 0
						AND jsonb_typeof(
							NEW.terminal_reason_sealed_json::jsonb -> 'key_version') = 'string'
						AND NEW.terminal_reason_sealed_json::jsonb ->> 'key_version' =
							NEW.terminal_reason_seal_key_version)
				)
			), false)) THEN
			RAISE EXCEPTION 'olivares: invalid Message terminal ProtectedPayload'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state IN ('retracted','expired','discarded') THEN
			IF NEW.terminal_at IS NULL OR NEW.terminal_at < NEW.created_at
				OR NEW.terminal_at > NEW.updated_at
				OR (NEW.published_at IS NOT NULL AND NEW.terminal_at < NEW.published_at)
				OR NEW.terminal_code IS NULL
				OR NEW.terminal_code !~ '^[a-z0-9._-]{1,128}$' THEN
				RAISE EXCEPTION 'olivares: terminal Message lacks typed DB-time evidence'
					USING ERRCODE = '23514';
			END IF;
		ELSIF NEW.terminal_at IS NOT NULL OR NEW.terminal_code IS NOT NULL
			OR NEW.terminal_reason_encoding IS NOT NULL THEN
			RAISE EXCEPTION 'olivares: non-terminal Message carries terminal evidence'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state IN ('published','retracted','expired') THEN
			IF NEW.published_at IS NULL OR NEW.published_at < NEW.created_at
				OR NEW.published_at > NEW.updated_at OR NEW.audience_hash IS NULL
				OR octet_length(NEW.audience_hash) <> 32 THEN
				RAISE EXCEPTION 'olivares: published Message lacks audience evidence'
					USING ERRCODE = '23514';
			END IF;
		ELSIF NEW.published_at IS NOT NULL OR NEW.audience_hash IS NOT NULL THEN
			RAISE EXCEPTION 'olivares: unpublished Message carries publication evidence'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'expired'
			AND (NEW.expires_at IS NULL OR NEW.terminal_at < NEW.expires_at) THEN
			RAISE EXCEPTION 'olivares: Message expired before expires_at'
				USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_channel c WHERE c.id = NEW.channel_id
			AND c.tenant_id = NEW.tenant_id AND c.workspace_id = NEW.workspace_id
			AND (NOT (TG_OP = 'INSERT' OR
				(TG_OP = 'UPDATE' AND OLD.state = 'draft' AND NEW.state = 'published'))
				OR (c.protection_generation = NEW.payload_protection_generation
					AND ((c.content_protection = 'storage'
						AND NEW.payload_encoding = 'plain_json')
						OR (c.content_protection = 'application_sealed'
							AND NEW.payload_encoding = 'sealed_v1'))))) THEN
			RAISE EXCEPTION 'olivares: Message channel/protection crosses lineage'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.work_item_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM sessions_work_item w WHERE w.id = NEW.work_item_id
				AND w.tenant_id = NEW.tenant_id AND w.workspace_id = NEW.workspace_id) THEN
			RAISE EXCEPTION 'olivares: Message WorkItem crosses tenant/workspace'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.origin_event_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM sessions_work_event e WHERE e.event_id = NEW.origin_event_id
				AND e.tenant_id = NEW.tenant_id AND e.workspace_id = NEW.workspace_id) THEN
			RAISE EXCEPTION 'olivares: Message origin Event crosses tenant/workspace'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.reply_to_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM sessions_message p WHERE p.id = NEW.reply_to_id
				AND p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
				AND p.channel_id = NEW.channel_id AND p.thread_id = NEW.thread_id
				AND p.work_item_id IS NOT DISTINCT FROM NEW.work_item_id) THEN
			RAISE EXCEPTION 'olivares: Message reply crosses thread or aggregate lineage'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.supersedes_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM sessions_message p WHERE p.id = NEW.supersedes_id
				AND p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
				AND p.channel_id = NEW.channel_id
				AND p.work_item_id IS NOT DISTINCT FROM NEW.work_item_id
				AND p.state IN ('retracted','expired','discarded')) THEN
			RAISE EXCEPTION 'olivares: Message supersedes lineage is invalid'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			(to_jsonb(NEW) - ARRAY[
				'version','updated_at','state','published_at','terminal_at','terminal_code',
				'terminal_reason_encoding','terminal_reason_plain_json','terminal_reason_sealed_json',
				'terminal_reason_schema','terminal_reason_digest','terminal_reason_seal_key_version',
				'terminal_reason_digest_key_version','terminal_reason_protection_generation',
				'audience_hash','last_event_seq'
			]) IS DISTINCT FROM
			(to_jsonb(OLD) - ARRAY[
				'version','updated_at','state','published_at','terminal_at','terminal_code',
				'terminal_reason_encoding','terminal_reason_plain_json','terminal_reason_sealed_json',
				'terminal_reason_schema','terminal_reason_digest','terminal_reason_seal_key_version',
				'terminal_reason_digest_key_version','terminal_reason_protection_generation',
				'audience_hash','last_event_seq'
			])
			OR (OLD.state <> NEW.state AND NOT (
				(OLD.state = 'draft' AND NEW.state IN ('published','discarded')) OR
				(OLD.state = 'published' AND NEW.state IN ('retracted','expired'))))
			OR (OLD.state <> 'draft' AND
				(NEW.published_at IS DISTINCT FROM OLD.published_at
					OR NEW.audience_hash IS DISTINCT FROM OLD.audience_hash))
			OR (OLD.state = NEW.state AND NOT (
				OLD.state IN ('published','retracted','expired')
				AND NEW.work_item_id IS NULL
				AND NEW.last_event_seq = OLD.last_event_seq + 1
				AND (to_jsonb(NEW) - ARRAY['version','updated_at','last_event_seq'])
					IS NOT DISTINCT FROM
					(to_jsonb(OLD) - ARRAY['version','updated_at','last_event_seq'])))
			OR (OLD.state IN ('retracted','expired','discarded') AND NEW.state <> OLD.state)
			OR (NEW.work_item_id IS NULL AND NEW.last_event_seq <>
				OLD.last_event_seq + 1)
		) THEN
			RAISE EXCEPTION 'olivares: Message immutable lineage or transition changed'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state IN ('published','retracted','expired') THEN
			SELECT count(*) INTO required_count FROM sessions_message_delivery d
				WHERE d.tenant_id = NEW.tenant_id AND d.message_id = NEW.id AND d.required;
			IF (NEW.ack_policy = 'none' AND required_count <> 0)
				OR (NEW.ack_policy = 'each_required' AND required_count < 1)
				OR (NEW.ack_policy = 'quorum'
					AND (NEW.ack_quorum < 1 OR NEW.ack_quorum > required_count)) THEN
				RAISE EXCEPTION 'olivares: Message Ack policy contradicts required deliveries'
					USING ERRCODE = '23514';
			END IF;
		END IF;

	WHEN 'sessions_message_audience' THEN
		PERFORM pg_advisory_xact_lock(hashtextextended(concat_ws('|',
			'sessions_message_publish', NEW.tenant_id::text, NEW.message_id::text), 0));
		IF NEW.ordinal < 1
			OR NEW.selector_kind NOT IN
				('user','user_group','agent','agent_group','session','subscribers','workspace_members')
			OR NEW.selector_wake_policy NOT IN ('none','primary','all')
			OR (NEW.selector_kind IN ('subscribers','workspace_members')) <>
				(NEW.selector_ref IS NULL)
			OR (NEW.selector_kind = 'session' AND
				(length(NEW.selector_ref) <> 40 OR left(NEW.selector_ref, 4) <> 'osn_'))
			OR (NEW.selector_kind IN ('user','user_group','agent','agent_group')
				AND length(NEW.selector_ref) <> 36)
			OR NEW.channel_acl_revision < 1 OR NEW.route_revision < 1
			OR NEW.subscription_revision < 1 OR NEW.directory_epoch < 1
			OR NEW.directory_snapshot_at IS NULL OR NEW.resolved_count < 0
			OR octet_length(NEW.selector_hash) <> 32 OR octet_length(NEW.resolved_hash) <> 32
			OR (NEW.selector_kind IN ('user','agent','session') AND NEW.resolved_count <> 1)
			OR (NEW.selector_required AND NEW.resolved_count = 0) THEN
			RAISE EXCEPTION 'olivares: invalid MessageAudience shape' USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_message m WHERE m.id = NEW.message_id
			AND m.tenant_id = NEW.tenant_id AND m.workspace_id = NEW.workspace_id
			AND m.state = 'draft') THEN
			RAISE EXCEPTION 'olivares: MessageAudience message crosses tenant/workspace'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.route_rule_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM sessions_channel_route r
			JOIN sessions_message m ON m.id = NEW.message_id
				AND m.tenant_id = NEW.tenant_id AND m.workspace_id = NEW.workspace_id
			WHERE r.id = NEW.route_rule_id AND r.tenant_id = NEW.tenant_id
				AND r.workspace_id = NEW.workspace_id AND r.target_channel_id = m.channel_id) THEN
			RAISE EXCEPTION 'olivares: MessageAudience route crosses tenant/workspace'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_message_audience_recipient' THEN
		SELECT a.message_id INTO parent_message_id
		FROM sessions_message_audience a
		WHERE a.id = NEW.message_audience_id AND a.tenant_id = NEW.tenant_id
			AND a.workspace_id = NEW.workspace_id;
		IF parent_message_id IS NOT NULL THEN
			PERFORM pg_advisory_xact_lock(hashtextextended(concat_ws('|',
				'sessions_message_publish', NEW.tenant_id::text, parent_message_id), 0));
		END IF;
		IF NEW.recipient_kind NOT IN ('user','agent','session')
			OR (NEW.recipient_kind = 'session'
				AND (length(NEW.recipient_ref) <> 40 OR left(NEW.recipient_ref, 4) <> 'osn_'))
			OR (NEW.recipient_kind <> 'session' AND length(NEW.recipient_ref) <> 36)
			OR NEW.recipient_epoch < 1
			OR NEW.wake_policy NOT IN ('none','primary','all')
			OR jsonb_typeof(NEW.route_reasons_json::jsonb) <> 'array'
			OR jsonb_array_length(NEW.route_reasons_json::jsonb) NOT BETWEEN 1 AND 32
			OR EXISTS (SELECT 1 FROM jsonb_array_elements(NEW.route_reasons_json::jsonb) r
				WHERE jsonb_typeof(r) <> 'string' OR (r #>> '{}') !~ '^[a-z0-9._-]{1,128}$')
			OR EXISTS (
				SELECT 1 FROM jsonb_array_elements_text(NEW.route_reasons_json::jsonb)
					WITH ORDINALITY AS a(value, ordinal)
				JOIN jsonb_array_elements_text(NEW.route_reasons_json::jsonb)
					WITH ORDINALITY AS b(value, ordinal) ON b.ordinal = a.ordinal + 1
				WHERE a.value >= b.value)
			OR NEW.selector_kind NOT IN
				('user','user_group','agent','agent_group','session','subscribers','workspace_members')
			OR NEW.selector_wake_policy NOT IN ('none','primary','all')
			OR (NEW.selector_kind IN ('subscribers','workspace_members')) <>
				(NEW.selector_ref IS NULL)
			OR NEW.directory_epoch < 1 OR NEW.channel_acl_revision < 1 OR NEW.route_revision < 1
			OR NEW.subscription_revision < 1
			OR NEW.causal_kind NOT IN
				('direct','user_group','agent_group','workspace_member','subscriber')
			OR octet_length(NEW.causal_ref) NOT BETWEEN 1 AND 512
			OR (NEW.causal_fact_kind IS NULL) <> (NEW.causal_fact_id IS NULL)
			OR (NEW.causal_fact_kind IS NULL) <> (NEW.causal_fact_version IS NULL)
			OR (NEW.causal_fact_id IS NOT NULL AND NEW.causal_fact_id !~
				'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
			OR (NEW.causal_fact_version IS NOT NULL AND NEW.causal_fact_version < 1)
			OR (NEW.recipient_kind = 'session' AND
				(NEW.observed_session_sid IS NULL OR NEW.observed_claim_fence IS NULL
					OR NEW.observed_session_sid <> NEW.recipient_ref
					OR NEW.observed_claim_fence < 1))
			OR (NEW.recipient_kind <> 'session' AND
				(NEW.observed_session_sid IS NOT NULL OR NEW.observed_claim_fence IS NOT NULL))
			OR (NEW.subscription_id IS NULL) <> (NEW.subscription_generation IS NULL)
			OR (NEW.subscription_generation IS NOT NULL AND NEW.subscription_generation < 1)
			OR (NEW.route_rule_id IS NULL) <> (NEW.route_rule_generation IS NULL)
			OR (NEW.route_rule_generation IS NOT NULL AND NEW.route_rule_generation < 1)
			OR octet_length(NEW.causal_arc_hash) <> 32 THEN
			RAISE EXCEPTION 'olivares: invalid MessageAudienceRecipient contribution'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.causal_kind = 'direct' AND NOT (
			NEW.causal_fact_kind IS NULL AND NEW.subscription_id IS NULL
			AND NEW.causal_ref = NEW.selector_ref AND NEW.causal_ref = NEW.recipient_ref
			AND ((NEW.selector_kind = 'user' AND NEW.recipient_kind = 'user')
				OR (NEW.selector_kind = 'agent' AND NEW.recipient_kind = 'agent')
				OR (NEW.selector_kind = 'session' AND NEW.recipient_kind = 'session'))) THEN
			RAISE EXCEPTION 'olivares: direct audience causality is inconsistent'
				USING ERRCODE = '23514';
		END IF;
		IF (NEW.causal_kind = 'user_group' AND (
				NEW.selector_kind IS DISTINCT FROM 'user_group'
				OR NEW.recipient_kind IS DISTINCT FROM 'user'
				OR NEW.causal_ref IS DISTINCT FROM NEW.selector_ref
				OR NEW.causal_fact_kind IS DISTINCT FROM 'core.user_group_member'
				OR NEW.causal_fact_id IS NULL OR NEW.causal_fact_version IS NULL))
			OR (NEW.causal_kind = 'agent_group' AND (
				NEW.selector_kind IS DISTINCT FROM 'agent_group'
				OR NEW.recipient_kind IS DISTINCT FROM 'agent'
				OR NEW.causal_ref IS DISTINCT FROM NEW.selector_ref
				OR NEW.causal_fact_kind IS DISTINCT FROM 'core.agent_group_member'
				OR NEW.causal_fact_id IS NULL OR NEW.causal_fact_version IS NULL))
			OR (NEW.causal_kind = 'workspace_member' AND (
				NEW.selector_kind IS DISTINCT FROM 'workspace_members'
				OR NEW.selector_ref IS NOT NULL
				OR NEW.causal_ref IS DISTINCT FROM NEW.workspace_id
				OR NOT ((NEW.recipient_kind = 'user'
						AND NEW.causal_fact_kind = 'core.membership')
					OR (NEW.recipient_kind = 'agent'
						AND NEW.causal_fact_kind = 'core.agent')) IS TRUE
				OR NEW.causal_fact_id IS NULL OR NEW.causal_fact_version IS NULL)) THEN
			RAISE EXCEPTION 'olivares: group audience causality is inconsistent'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.causal_kind = 'subscriber' AND (
			NEW.selector_kind <> 'subscribers' OR NEW.subscription_id IS NULL
			OR NEW.original_subscriber_kind IS NULL OR NEW.original_subscriber_ref IS NULL
			OR NEW.original_subscriber_kind NOT IN
				('user','user_group','agent','agent_group','session')
			OR (NEW.original_subscriber_kind = 'session' AND
				(length(NEW.original_subscriber_ref) <> 40
					OR left(NEW.original_subscriber_ref, 4) <> 'osn_'))
			OR (NEW.original_subscriber_kind <> 'session'
				AND length(NEW.original_subscriber_ref) <> 36)
			OR NEW.causal_ref <> NEW.original_subscriber_ref
			OR (NEW.original_subscriber_kind = 'user' AND NOT (
				NEW.recipient_kind = 'user' AND NEW.recipient_ref = NEW.original_subscriber_ref
				AND NEW.causal_fact_kind IS NULL))
			OR (NEW.original_subscriber_kind = 'agent' AND NOT (
				NEW.recipient_kind = 'agent' AND NEW.recipient_ref = NEW.original_subscriber_ref
				AND NEW.causal_fact_kind IS NULL))
			OR (NEW.original_subscriber_kind = 'session' AND NOT (
				NEW.recipient_kind = 'session' AND NEW.recipient_ref = NEW.original_subscriber_ref
				AND NEW.causal_fact_kind IS NULL))
			OR (NEW.original_subscriber_kind = 'user_group' AND (
				NEW.recipient_kind IS DISTINCT FROM 'user'
				OR NEW.causal_fact_kind IS DISTINCT FROM 'core.user_group_member'
				OR NEW.causal_fact_id IS NULL OR NEW.causal_fact_version IS NULL))
			OR (NEW.original_subscriber_kind = 'agent_group' AND (
				NEW.recipient_kind IS DISTINCT FROM 'agent'
				OR NEW.causal_fact_kind IS DISTINCT FROM 'core.agent_group_member'
				OR NEW.causal_fact_id IS NULL OR NEW.causal_fact_version IS NULL))) THEN
			RAISE EXCEPTION 'olivares: subscriber audience causality is incomplete'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.causal_kind <> 'subscriber' AND (
			NEW.subscription_id IS NOT NULL OR NEW.original_subscriber_kind IS NOT NULL
			OR NEW.original_subscriber_ref IS NOT NULL) THEN
			RAISE EXCEPTION 'olivares: non-subscriber contribution carries subscription provenance'
				USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (
			SELECT 1 FROM sessions_message_audience a
			JOIN sessions_message m ON m.id = a.message_id AND m.tenant_id = a.tenant_id
			JOIN sessions_message_delivery d ON d.id = NEW.message_delivery_id
				AND d.tenant_id = a.tenant_id AND d.message_id = a.message_id
			WHERE a.id = NEW.message_audience_id AND a.tenant_id = NEW.tenant_id
				AND a.workspace_id = NEW.workspace_id AND m.workspace_id = NEW.workspace_id
				AND m.state = 'draft'
				AND d.workspace_id = NEW.workspace_id
				AND a.selector_kind = NEW.selector_kind
				AND a.selector_ref IS NOT DISTINCT FROM NEW.selector_ref
				AND a.selector_required = NEW.selector_required
				AND a.selector_wake_policy = NEW.selector_wake_policy
				AND a.directory_epoch = NEW.directory_epoch
				AND a.channel_acl_revision = NEW.channel_acl_revision
				AND a.route_revision = NEW.route_revision
				AND a.subscription_revision = NEW.subscription_revision
				AND (a.route_rule_id IS NULL OR a.route_rule_id = NEW.route_rule_id)
				AND d.recipient_kind = NEW.recipient_kind
				AND d.recipient_ref = NEW.recipient_ref
				AND d.recipient_epoch = NEW.recipient_epoch) THEN
			RAISE EXCEPTION 'olivares: audience contribution crosses normalized lineage'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.route_rule_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM sessions_channel_route r
			JOIN sessions_message_audience a ON a.id = NEW.message_audience_id
				AND a.tenant_id = NEW.tenant_id AND a.workspace_id = NEW.workspace_id
			JOIN sessions_message m ON m.id = a.message_id AND m.tenant_id = a.tenant_id
				AND m.workspace_id = a.workspace_id
			WHERE r.id = NEW.route_rule_id AND r.tenant_id = NEW.tenant_id
				AND r.workspace_id = NEW.workspace_id AND r.generation = NEW.route_rule_generation
				AND r.target_channel_id = m.channel_id) THEN
			RAISE EXCEPTION 'olivares: audience route provenance crosses generation'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.subscription_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM sessions_channel_subscription s
			JOIN sessions_message_audience a ON a.id = NEW.message_audience_id
				AND a.tenant_id = NEW.tenant_id AND a.workspace_id = NEW.workspace_id
			JOIN sessions_message m ON m.id = a.message_id AND m.tenant_id = a.tenant_id
				AND m.workspace_id = a.workspace_id
			WHERE s.id = NEW.subscription_id AND s.tenant_id = NEW.tenant_id
				AND s.workspace_id = NEW.workspace_id AND s.generation = NEW.subscription_generation
				AND s.channel_id = m.channel_id
				AND s.subscriber_kind = NEW.original_subscriber_kind
				AND s.subscriber_ref = NEW.original_subscriber_ref) THEN
			RAISE EXCEPTION 'olivares: subscriber provenance crosses generation'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_message_delivery' THEN
		PERFORM pg_advisory_xact_lock(hashtextextended(concat_ws('|',
			'sessions_message_publish', NEW.tenant_id::text, NEW.message_id::text), 0));
		IF (TG_OP = 'INSERT' AND NEW.state <> 'available')
			OR NEW.recipient_kind NOT IN ('user','agent','session')
			OR (NEW.recipient_kind = 'session'
				AND (length(NEW.recipient_ref) <> 40 OR left(NEW.recipient_ref, 4) <> 'osn_'))
			OR (NEW.recipient_kind <> 'session' AND length(NEW.recipient_ref) <> 36)
			OR NEW.recipient_epoch < 1 OR NEW.delivery_seq < 1
			OR NEW.wake_policy NOT IN ('none','primary','all')
			OR NEW.state NOT IN ('available','acknowledged','expired','retracted','undeliverable')
			OR NEW.available_at < NEW.created_at
			OR jsonb_typeof(NEW.route_reasons_json::jsonb) <> 'array'
			OR jsonb_array_length(NEW.route_reasons_json::jsonb) NOT BETWEEN 1 AND 32
			OR EXISTS (SELECT 1 FROM jsonb_array_elements(NEW.route_reasons_json::jsonb) r
				WHERE jsonb_typeof(r) <> 'string' OR (r #>> '{}') !~ '^[a-z0-9._-]{1,128}$')
			OR EXISTS (
				SELECT 1 FROM jsonb_array_elements_text(NEW.route_reasons_json::jsonb)
					WITH ORDINALITY AS a(value, ordinal)
				JOIN jsonb_array_elements_text(NEW.route_reasons_json::jsonb)
					WITH ORDINALITY AS b(value, ordinal) ON b.ordinal = a.ordinal + 1
				WHERE a.value >= b.value)
			OR (NEW.required AND NEW.ack_due_at IS NULL)
			OR (NEW.ack_due_at IS NOT NULL AND NEW.ack_due_at < NEW.available_at)
			OR (NEW.expires_at IS NOT NULL AND NEW.expires_at <= NEW.available_at)
			OR (NEW.ack_due_at IS NOT NULL AND NEW.expires_at IS NOT NULL
				AND NEW.ack_due_at > NEW.expires_at)
			OR (NEW.first_seen_at IS NOT NULL AND
				(NEW.first_seen_at < NEW.available_at OR NEW.first_seen_at > NEW.updated_at))
			OR (NEW.last_wake_verdict IS NULL) <> (NEW.last_wake_code IS NULL)
			OR (NEW.last_wake_verdict IS NULL) <> (NEW.last_wake_at IS NULL)
			OR (NEW.last_wake_verdict IS NOT NULL AND
				(NEW.last_wake_verdict NOT IN ('LIMPIO','ROTO','NO_HE_PODIDO_MIRAR')
					OR NEW.last_wake_code !~ '^[a-z0-9._-]{1,128}$'
					OR NEW.last_wake_at < NEW.available_at
					OR NEW.last_wake_at > NEW.updated_at)) THEN
			RAISE EXCEPTION 'olivares: invalid MessageDelivery shape' USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'available' AND (
				NEW.ack_id IS NOT NULL OR NEW.acknowledged_at IS NOT NULL
				OR NEW.retirement_tombstone_kind IS NOT NULL
				OR NEW.retirement_tombstone_id IS NOT NULL
				OR NEW.retirement_tombstone_version IS NOT NULL
				OR NEW.retirement_epoch IS NOT NULL OR NEW.undeliverable_at IS NOT NULL
				OR NEW.undeliverable_code IS NOT NULL)
			OR NEW.state = 'acknowledged' AND (
				NEW.ack_id IS NULL OR NEW.acknowledged_at IS NULL
				OR NEW.acknowledged_at < NEW.available_at
				OR NEW.acknowledged_at > NEW.updated_at
				OR (NEW.ack_due_at IS NOT NULL AND NEW.acknowledged_at > NEW.ack_due_at)
				OR NEW.retirement_tombstone_kind IS NOT NULL
				OR NEW.retirement_tombstone_id IS NOT NULL
				OR NEW.retirement_tombstone_version IS NOT NULL
				OR NEW.retirement_epoch IS NOT NULL OR NEW.undeliverable_at IS NOT NULL
				OR NEW.undeliverable_code IS NOT NULL)
			OR NEW.state IN ('expired','retracted') AND (
				NEW.ack_id IS NOT NULL OR NEW.acknowledged_at IS NOT NULL
				OR NEW.retirement_tombstone_kind IS NOT NULL
				OR NEW.retirement_tombstone_id IS NOT NULL
				OR NEW.retirement_tombstone_version IS NOT NULL
				OR NEW.retirement_epoch IS NOT NULL OR NEW.undeliverable_at IS NOT NULL
				OR NEW.undeliverable_code IS NOT NULL)
			OR NEW.state = 'undeliverable' AND (
				NEW.recipient_kind = 'session' OR NEW.ack_id IS NOT NULL
				OR NEW.acknowledged_at IS NOT NULL
				OR NEW.retirement_tombstone_kind IS NULL
				OR NEW.retirement_tombstone_kind <>
					CASE NEW.recipient_kind WHEN 'user' THEN 'core.user_tombstone'
						ELSE 'core.directory_tombstone' END
				OR NEW.retirement_tombstone_id IS NULL
				OR NEW.retirement_tombstone_version <> 1 OR NEW.retirement_epoch < 1
				OR NEW.undeliverable_at IS NULL OR NEW.undeliverable_at < NEW.created_at
				OR NEW.undeliverable_at > NEW.updated_at OR NEW.undeliverable_code IS NULL
				OR NEW.undeliverable_code !~ '^[a-z0-9._-]{1,128}$') THEN
			RAISE EXCEPTION 'olivares: MessageDelivery state evidence is inconsistent'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'undeliverable' AND NEW.recipient_kind = 'user' THEN
			prior_tenant := pg_catalog.current_setting('app.tenant_id');
			BEGIN
				PERFORM pg_catalog.set_config(
					'app.tenant_id', 'ffffffff-ffff-ffff-ffff-ffffffffffff', true);
				SELECT EXISTS (
					SELECT 1 FROM core_user_tombstone t
					WHERE t.id = NEW.retirement_tombstone_id
						AND t.tenant_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff'
						AND t.version = NEW.retirement_tombstone_version
						AND t.principal_kind = 'user'
						AND t.principal_ref = NEW.recipient_ref
						AND (t.resulting_epochs::jsonb ->> NEW.tenant_id::text)::bigint =
							NEW.retirement_epoch)
				INTO user_tombstone_found;
				PERFORM pg_catalog.set_config('app.tenant_id', prior_tenant, true);
			EXCEPTION WHEN OTHERS THEN
				PERFORM pg_catalog.set_config('app.tenant_id', prior_tenant, true);
				RAISE;
			END;
			IF NOT user_tombstone_found THEN
				RAISE EXCEPTION 'olivares: MessageDelivery retirement witness crosses core tombstone'
					USING ERRCODE = '23514';
			END IF;
		ELSIF NEW.state = 'undeliverable' AND NEW.recipient_kind = 'agent'
			AND NOT EXISTS (
				SELECT 1 FROM core_directory_tombstone t
				WHERE t.id = NEW.retirement_tombstone_id AND t.tenant_id = NEW.tenant_id
					AND t.version = NEW.retirement_tombstone_version
					AND t.principal_ref = NEW.recipient_ref
					AND t.resulting_epoch = NEW.retirement_epoch
					AND ((t.principal_kind = 'identity'
							AND t.workspace_ref = '00000000-0000-0000-0000-000000000000')
						OR (t.principal_kind = 'agent'
							AND t.workspace_ref = NEW.workspace_id))) THEN
			RAISE EXCEPTION 'olivares: MessageDelivery retirement witness crosses core tombstone'
				USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_message m WHERE m.id = NEW.message_id
			AND m.tenant_id = NEW.tenant_id AND m.workspace_id = NEW.workspace_id
			AND (TG_OP <> 'INSERT' OR m.state = 'draft')
			AND m.available_at = NEW.available_at
			AND m.expires_at IS NOT DISTINCT FROM NEW.expires_at
			AND (NOT NEW.required OR m.ack_due_at IS NOT DISTINCT FROM NEW.ack_due_at)
			AND (m.ack_policy <> 'none' OR NEW.ack_due_at IS NULL)
			AND NOT (m.state = 'published' AND NEW.state = 'retracted')
			AND NOT (m.state = 'retracted' AND NEW.state = 'available')
			AND NOT (m.state = 'expired' AND NEW.state IN ('available','retracted'))) THEN
			RAISE EXCEPTION 'olivares: MessageDelivery crosses Message lineage'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			(to_jsonb(NEW) - ARRAY[
				'version','updated_at','state','first_seen_at','ack_id','acknowledged_at',
				'last_wake_verdict','last_wake_code','last_wake_at',
				'retirement_tombstone_kind','retirement_tombstone_id',
				'retirement_tombstone_version','retirement_epoch',
				'undeliverable_at','undeliverable_code'
			]) IS DISTINCT FROM
			(to_jsonb(OLD) - ARRAY[
				'version','updated_at','state','first_seen_at','ack_id','acknowledged_at',
				'last_wake_verdict','last_wake_code','last_wake_at',
				'retirement_tombstone_kind','retirement_tombstone_id',
				'retirement_tombstone_version','retirement_epoch',
				'undeliverable_at','undeliverable_code'
			])
			OR (OLD.first_seen_at IS NOT NULL AND NEW.first_seen_at IS DISTINCT FROM OLD.first_seen_at)
			OR (OLD.state <> NEW.state AND NOT (OLD.state = 'available'
				AND NEW.state IN ('acknowledged','expired','retracted','undeliverable')))
			OR (OLD.state = 'available' AND NEW.state = 'expired' AND NOT (
				(OLD.ack_due_at IS NOT NULL AND NEW.updated_at >= OLD.ack_due_at)
				OR (OLD.expires_at IS NOT NULL AND NEW.updated_at >= OLD.expires_at)))
			OR (OLD.state = 'available' AND NEW.state IN ('acknowledged','retracted') AND (
				(OLD.ack_due_at IS NOT NULL AND NEW.updated_at >= OLD.ack_due_at)
				OR (OLD.expires_at IS NOT NULL AND NEW.updated_at >= OLD.expires_at)))
			OR (OLD.state = 'available' AND NEW.state = 'undeliverable' AND OLD.required
				AND OLD.ack_due_at IS NOT NULL AND NEW.updated_at >= OLD.ack_due_at)
			OR (OLD.state <> 'available' AND NEW.state <> OLD.state)
			OR (OLD.state <> 'available' AND (
				NEW.ack_id IS DISTINCT FROM OLD.ack_id
				OR NEW.acknowledged_at IS DISTINCT FROM OLD.acknowledged_at
				OR NEW.retirement_tombstone_kind IS DISTINCT FROM OLD.retirement_tombstone_kind
				OR NEW.retirement_tombstone_id IS DISTINCT FROM OLD.retirement_tombstone_id
				OR NEW.retirement_tombstone_version IS DISTINCT FROM
					OLD.retirement_tombstone_version
				OR NEW.retirement_epoch IS DISTINCT FROM OLD.retirement_epoch
				OR NEW.undeliverable_at IS DISTINCT FROM OLD.undeliverable_at
				OR NEW.undeliverable_code IS DISTINCT FROM OLD.undeliverable_code))
		) THEN
			RAISE EXCEPTION 'olivares: MessageDelivery immutable lineage or state changed'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_inbox_cursor' THEN
		PERFORM pg_advisory_xact_lock(hashtextextended(concat_ws('|',
			'sessions_inbox_cursor_identity', NEW.tenant_id::text, NEW.workspace_id::text,
			NEW.reader_kind, NEW.reader_ref, NEW.mailbox_kind,
			COALESCE(NEW.mailbox_ref, '<null>'), encode(NEW.filter_hash, 'hex')), 0));
		IF NEW.reader_kind NOT IN ('user','agent','session')
			OR (NEW.reader_kind = 'session'
				AND (length(NEW.reader_ref) <> 40 OR left(NEW.reader_ref, 4) <> 'osn_'))
			OR (NEW.reader_kind <> 'session' AND length(NEW.reader_ref) <> 36)
			OR NEW.mailbox_kind NOT IN ('personal','channel')
			OR octet_length(NEW.mailbox_ref) NOT BETWEEN 1 AND 512
			OR (NEW.mailbox_kind = 'personal' AND NEW.mailbox_ref <> NEW.reader_ref)
			OR (NEW.mailbox_kind = 'channel' AND NEW.mailbox_ref !~
				'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
			OR NEW.last_seen_seq < 0 OR NEW.last_seen_at IS NULL
			OR NEW.last_seen_at > NEW.updated_at OR octet_length(NEW.filter_hash) <> 32 THEN
			RAISE EXCEPTION 'olivares: invalid InboxCursor shape' USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			(to_jsonb(NEW) - ARRAY['version','updated_at','last_seen_seq','last_seen_at'])
				IS DISTINCT FROM
			(to_jsonb(OLD) - ARRAY['version','updated_at','last_seen_seq','last_seen_at'])
			OR NEW.last_seen_seq < OLD.last_seen_seq OR NEW.last_seen_at < OLD.last_seen_at
		) THEN
			RAISE EXCEPTION 'olivares: InboxCursor identity or monotonicity changed'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND EXISTS (
			SELECT 1 FROM sessions_inbox_cursor_barrier b
			WHERE b.tenant_id = NEW.tenant_id AND b.workspace_id = NEW.workspace_id
				AND b.reader_kind = NEW.reader_kind AND b.reader_ref = NEW.reader_ref
				AND b.mailbox_kind = NEW.mailbox_kind
				AND b.mailbox_ref IS NOT DISTINCT FROM NEW.mailbox_ref
				AND b.filter_hash = NEW.filter_hash AND b.state = 'active'
				AND NEW.last_seen_seq >= b.barrier_seq) THEN
			RAISE EXCEPTION 'olivares: InboxCursor cannot cross an active barrier'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_inbox_cursor_barrier' THEN
		IF NEW.reader_kind NOT IN ('user','agent','session')
			OR NEW.mailbox_kind NOT IN ('personal','channel')
			OR octet_length(NEW.filter_hash) <> 32 OR NEW.barrier_seq < 1
			OR NEW.cause NOT IN ('not_yet_available','temporarily_invisible')
			OR NEW.state NOT IN ('active','resolved')
			OR NEW.reason_code !~ '^[a-z0-9._-]{1,128}$'
			OR (NEW.state = 'active' AND NEW.resolved_at IS NOT NULL)
			OR (NEW.state = 'resolved' AND
				(NEW.resolved_at IS NULL OR NEW.resolved_at < NEW.created_at
					OR NEW.resolved_at > NEW.updated_at)) THEN
			RAISE EXCEPTION 'olivares: invalid InboxCursorBarrier shape'
				USING ERRCODE = '23514';
		END IF;
		PERFORM pg_advisory_xact_lock(hashtextextended(concat_ws('|',
			'sessions_inbox_cursor_identity', NEW.tenant_id::text, NEW.workspace_id::text,
			NEW.reader_kind, NEW.reader_ref, NEW.mailbox_kind,
			COALESCE(NEW.mailbox_ref, '<null>'), encode(NEW.filter_hash, 'hex')), 0));
		IF NEW.state = 'active' THEN
			PERFORM pg_advisory_xact_lock(hashtextextended(concat_ws('|',
				'sessions_inbox_cursor_barrier_active', NEW.tenant_id::text,
				NEW.workspace_id::text, NEW.reader_kind, NEW.reader_ref, NEW.mailbox_kind,
				COALESCE(NEW.mailbox_ref, '<null>'), encode(NEW.filter_hash, 'hex'),
				NEW.delivery_id::text), 0));
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_inbox_cursor c
			WHERE c.tenant_id = NEW.tenant_id AND c.workspace_id = NEW.workspace_id
				AND c.reader_kind = NEW.reader_kind AND c.reader_ref = NEW.reader_ref
				AND c.mailbox_kind = NEW.mailbox_kind AND c.mailbox_ref = NEW.mailbox_ref
				AND c.filter_hash = NEW.filter_hash
				AND (NEW.state = 'resolved' OR NEW.barrier_seq > c.last_seen_seq))
			OR NOT EXISTS (SELECT 1 FROM sessions_message_delivery d
				JOIN sessions_message m ON m.id = d.message_id AND m.tenant_id = d.tenant_id
					AND m.workspace_id = d.workspace_id
				WHERE d.id = NEW.delivery_id AND d.tenant_id = NEW.tenant_id
					AND d.workspace_id = NEW.workspace_id AND d.delivery_seq = NEW.barrier_seq
					AND d.recipient_kind = NEW.reader_kind AND d.recipient_ref = NEW.reader_ref
					AND ((NEW.mailbox_kind = 'personal' AND NEW.mailbox_ref = NEW.reader_ref)
						OR (NEW.mailbox_kind = 'channel' AND m.channel_id = NEW.mailbox_ref))) THEN
			RAISE EXCEPTION 'olivares: InboxCursorBarrier crosses cursor/delivery lineage'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'active' AND EXISTS (
			SELECT 1 FROM sessions_inbox_cursor_barrier p
			WHERE p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
				AND p.reader_kind = NEW.reader_kind AND p.reader_ref = NEW.reader_ref
				AND p.mailbox_kind = NEW.mailbox_kind
				AND p.mailbox_ref IS NOT DISTINCT FROM NEW.mailbox_ref
				AND p.filter_hash = NEW.filter_hash AND p.delivery_id = NEW.delivery_id
				AND p.state = 'active' AND p.id <> NEW.id) THEN
			RAISE EXCEPTION 'olivares: InboxCursorBarrier has duplicate active identity'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			(to_jsonb(NEW) - ARRAY['version','updated_at','state','resolved_at'])
				IS DISTINCT FROM
			(to_jsonb(OLD) - ARRAY['version','updated_at','state','resolved_at'])
			OR (OLD.state <> NEW.state AND NOT
				(OLD.state = 'active' AND NEW.state = 'resolved'))
			OR OLD.state = 'resolved'
		) THEN
			RAISE EXCEPTION 'olivares: InboxCursorBarrier immutable lineage changed'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_message_ack' THEN
		IF NEW.ack_kind <> 'received' OR NEW.actor_kind NOT IN ('user','agent','session')
			OR (NEW.actor_kind = 'session'
				AND (length(NEW.actor_ref) <> 40 OR left(NEW.actor_ref, 4) <> 'osn_'))
			OR (NEW.actor_kind <> 'session' AND length(NEW.actor_ref) <> 36)
			OR (NEW.on_behalf_of_kind IS NULL) <> (NEW.on_behalf_of_ref IS NULL)
			OR (NEW.on_behalf_of_kind IS NOT NULL
				AND NEW.on_behalf_of_kind NOT IN ('user','agent','session'))
			OR (NEW.on_behalf_of_kind = 'session' AND
				(length(NEW.on_behalf_of_ref) <> 40 OR left(NEW.on_behalf_of_ref, 4) <> 'osn_'))
			OR (NEW.on_behalf_of_kind IN ('user','agent')
				AND length(NEW.on_behalf_of_ref) <> 36)
			OR (NEW.on_behalf_of_kind IS NOT NULL AND NEW.note_encoding IS NULL)
			OR NEW.acknowledged_at <> NEW.created_at THEN
			RAISE EXCEPTION 'olivares: invalid MessageAck shape' USING ERRCODE = '23514';
		END IF;
		IF NEW.note_encoding IS NULL AND (
			NEW.note_plain_json IS NOT NULL OR NEW.note_sealed_json IS NOT NULL
			OR NEW.note_schema IS NOT NULL OR NEW.note_digest IS NOT NULL
			OR NEW.note_seal_key_version IS NOT NULL OR NEW.note_digest_key_version IS NOT NULL
			OR NEW.note_protection_generation IS NOT NULL) THEN
			RAISE EXCEPTION 'olivares: invalid MessageAck protected note'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.note_encoding IS NOT NULL AND NOT COALESCE((
			(NEW.note_encoding IS NULL AND NEW.note_plain_json IS NULL
				AND NEW.note_sealed_json IS NULL AND NEW.note_schema IS NULL
				AND NEW.note_digest IS NULL AND NEW.note_seal_key_version IS NULL
				AND NEW.note_digest_key_version IS NULL
				AND NEW.note_protection_generation IS NULL)
			OR (
				NEW.note_encoding IN ('plain_json','sealed_v1')
				AND NEW.note_schema = 'communication.ack-note.v1'
				AND octet_length(NEW.note_digest) = 32
				AND NEW.note_protection_generation >= 1
				AND (
					(NEW.note_encoding = 'plain_json' AND NEW.note_plain_json IS NOT NULL
						AND jsonb_typeof(NEW.note_plain_json::jsonb) = 'object'
						AND octet_length(NEW.note_plain_json::text) <= 65536
						AND NEW.note_sealed_json IS NULL
						AND NEW.note_seal_key_version IS NULL
						AND NEW.note_digest_key_version IS NULL)
					OR (NEW.note_encoding = 'sealed_v1' AND NEW.note_plain_json IS NULL
						AND NEW.note_sealed_json IS NOT NULL
						AND jsonb_typeof(NEW.note_sealed_json::jsonb) = 'object'
						AND octet_length(NEW.note_sealed_json::text) <= 196608
						AND octet_length(NEW.note_seal_key_version) BETWEEN 1 AND 512
						AND octet_length(NEW.note_digest_key_version) BETWEEN 1 AND 512
						AND NEW.note_seal_key_version = btrim(NEW.note_seal_key_version)
						AND NEW.note_digest_key_version = btrim(NEW.note_digest_key_version)
						AND NEW.note_seal_key_version !~ E'[\\r\\n]'
						AND NEW.note_digest_key_version !~ E'[\\r\\n]'
						AND jsonb_typeof(NEW.note_sealed_json::jsonb -> 'ciphertext') = 'string'
						AND COALESCE(length(NEW.note_sealed_json::jsonb ->> 'ciphertext'), 0) > 0
						AND jsonb_typeof(NEW.note_sealed_json::jsonb -> 'key_version') = 'string'
						AND NEW.note_sealed_json::jsonb ->> 'key_version' =
							NEW.note_seal_key_version)
				)
			)
		), false) THEN
			RAISE EXCEPTION 'olivares: invalid MessageAck protected note'
				USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_message_delivery d
			JOIN sessions_message m ON m.id = d.message_id AND m.tenant_id = d.tenant_id
				AND m.workspace_id = d.workspace_id
			WHERE d.id = NEW.delivery_id AND d.tenant_id = NEW.tenant_id
				AND d.workspace_id = NEW.workspace_id
				AND (NEW.note_encoding IS NULL OR
					(NEW.note_encoding = m.payload_encoding
						AND NEW.note_protection_generation = m.payload_protection_generation))
				AND ((NOT NEW.late AND d.state = 'acknowledged'
						AND d.ack_id = NEW.id AND d.acknowledged_at = NEW.acknowledged_at)
					OR (NEW.late AND d.state IN ('expired','retracted')))) THEN
			RAISE EXCEPTION 'olivares: MessageAck crosses Delivery lineage or effectiveness'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_communication_guard' THEN
		IF NEW.guard_kind NOT IN ('delivery_sequence','route_revision')
			OR NEW.next_seq < 1 OR NEW.last_db_time < NEW.created_at
			OR NEW.last_db_time > NEW.updated_at THEN
			RAISE EXCEPTION 'olivares: invalid CommunicationGuard shape'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			NEW.guard_kind IS DISTINCT FROM OLD.guard_kind
			OR NEW.next_seq < OLD.next_seq OR NEW.last_db_time < OLD.last_db_time
		) THEN
			RAISE EXCEPTION 'olivares: CommunicationGuard monotonicity changed'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_decision_request' THEN
		IF NEW.decision_key !~ '^[a-z0-9._-]{1,128}$'
			OR NEW.requester_kind NOT IN ('user','agent','session','system')
			OR octet_length(NEW.requester_ref) NOT BETWEEN 1 AND 512
			OR (NEW.requester_kind = 'session'
				AND (length(NEW.requester_ref) <> 40 OR left(NEW.requester_ref, 4) <> 'osn_'))
			OR (NEW.requester_kind IN ('user','agent') AND length(NEW.requester_ref) <> 36)
			OR NEW.owner_kind NOT IN ('user','user_group','agent','agent_group','session')
			OR (NEW.owner_kind = 'session'
				AND (length(NEW.owner_ref) <> 40 OR left(NEW.owner_ref, 4) <> 'osn_'))
			OR (NEW.owner_kind <> 'session' AND length(NEW.owner_ref) <> 36)
			OR NEW.state NOT IN ('pending','accepted','blocked','resolved','rejected','canceled','expired')
			OR NEW.request_encoding IS NULL
			OR NEW.request_encoding NOT IN ('plain_json','sealed_v1')
			OR NEW.request_schema IS DISTINCT FROM 'communication.decision-request.v1'
			OR NEW.request_digest IS NULL OR octet_length(NEW.request_digest) <> 32
			OR NEW.request_protection_generation IS NULL OR NEW.request_protection_generation < 1
			OR (NEW.request_encoding = 'plain_json' AND
				(NEW.request_plain_json IS NULL OR NEW.request_sealed_json IS NOT NULL
					OR jsonb_typeof(NEW.request_plain_json::jsonb) <> 'object'
					OR octet_length(NEW.request_plain_json::text) > 65536
					OR NEW.request_seal_key_version IS NOT NULL
					OR NEW.request_digest_key_version IS NOT NULL))
			OR (NEW.request_encoding = 'sealed_v1' AND
				(NEW.request_plain_json IS NOT NULL OR NEW.request_sealed_json IS NULL
					OR jsonb_typeof(NEW.request_sealed_json::jsonb) <> 'object'
					OR octet_length(NEW.request_sealed_json::text) > 196608
					OR octet_length(NEW.request_seal_key_version) NOT BETWEEN 1 AND 512
					OR octet_length(NEW.request_digest_key_version) NOT BETWEEN 1 AND 512
					OR NEW.request_seal_key_version IS NULL
					OR NEW.request_digest_key_version IS NULL
					OR NEW.request_seal_key_version <> btrim(NEW.request_seal_key_version)
					OR NEW.request_digest_key_version <> btrim(NEW.request_digest_key_version)
					OR NEW.request_seal_key_version ~ E'[\\r\\n]'
					OR NEW.request_digest_key_version ~ E'[\\r\\n]'
					OR jsonb_typeof(NEW.request_sealed_json::jsonb -> 'ciphertext')
						IS DISTINCT FROM 'string'
					OR COALESCE(length(NEW.request_sealed_json::jsonb ->> 'ciphertext'), 0) = 0
					OR jsonb_typeof(NEW.request_sealed_json::jsonb -> 'key_version')
						IS DISTINCT FROM 'string'
					OR NEW.request_sealed_json::jsonb ->> 'key_version' IS DISTINCT FROM
						NEW.request_seal_key_version))
			OR NEW.authority_requirement !~ '^[a-z0-9._-]{1,256}$'
			OR NEW.due_at <= NEW.created_at OR NEW.last_response_seq < 0
			OR NEW.version <> NEW.last_response_seq + 1
			OR (NEW.accepted_delivery_id IS NULL) <> (NEW.accepted_at IS NULL)
			OR (NEW.accepted_at IS NOT NULL
				AND (NEW.accepted_at < NEW.created_at OR NEW.accepted_at > NEW.updated_at)) THEN
			RAISE EXCEPTION 'olivares: invalid DecisionRequest envelope'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'pending' AND (
				NEW.last_response_seq <> 0 OR NEW.accepted_delivery_id IS NOT NULL
				OR NEW.blocked_code IS NOT NULL OR NEW.terminal_code IS NOT NULL
				OR NEW.resolved_decision_id IS NOT NULL)
			OR NEW.state = 'accepted' AND (
				NEW.last_response_seq < 1 OR NEW.last_response_seq % 2 <> 1
				OR NEW.accepted_delivery_id IS NULL OR NEW.blocked_code IS NOT NULL
				OR NEW.terminal_code IS NOT NULL OR NEW.resolved_decision_id IS NOT NULL)
			OR NEW.state = 'blocked' AND (
				NEW.last_response_seq < 2 OR NEW.last_response_seq % 2 <> 0
				OR NEW.accepted_delivery_id IS NULL
				OR NEW.blocked_code IS NULL OR NEW.blocked_code !~ '^[a-z0-9._-]{1,128}$'
				OR NEW.terminal_code IS NOT NULL OR NEW.resolved_decision_id IS NOT NULL)
			OR NEW.state = 'resolved' AND (
				NEW.last_response_seq < 1 OR NEW.blocked_code IS NOT NULL
				OR NEW.terminal_code IS NULL OR NEW.terminal_code !~ '^[a-z0-9._-]{1,128}$'
				OR NEW.resolved_decision_id IS NULL)
			OR NEW.state IN ('rejected','canceled','expired') AND (
				NEW.last_response_seq < 1 OR NEW.blocked_code IS NOT NULL
				OR NEW.terminal_code IS NULL OR NEW.terminal_code !~ '^[a-z0-9._-]{1,128}$'
				OR NEW.resolved_decision_id IS NOT NULL)
			OR NEW.state = 'expired' AND NEW.updated_at < NEW.due_at THEN
			RAISE EXCEPTION 'olivares: DecisionRequest state evidence is inconsistent'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state NOT IN ('resolved','rejected','canceled','expired') THEN
			PERFORM pg_advisory_xact_lock(hashtextextended(concat_ws('|',
				'sessions_decision_request_active', NEW.tenant_id::text,
				NEW.workspace_id::text, NEW.work_item_id::text, NEW.decision_key), 0));
		END IF;
		IF NOT EXISTS (
			SELECT 1 FROM sessions_message m JOIN sessions_work_item w
				ON w.id = m.work_item_id AND w.tenant_id = m.tenant_id
			WHERE m.id = NEW.message_id AND m.tenant_id = NEW.tenant_id
				AND m.workspace_id = NEW.workspace_id AND m.work_item_id = NEW.work_item_id
				AND m.kind = 'decision_request' AND w.workspace_id = NEW.workspace_id
				AND m.payload_encoding = NEW.request_encoding
				AND m.payload_protection_generation = NEW.request_protection_generation
				AND (m.expires_at IS NULL OR m.expires_at >= NEW.due_at)) THEN
			RAISE EXCEPTION 'olivares: DecisionRequest message/work lineage is invalid'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.accepted_delivery_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM sessions_message_delivery d
			WHERE d.id = NEW.accepted_delivery_id AND d.tenant_id = NEW.tenant_id
				AND d.workspace_id = NEW.workspace_id AND d.message_id = NEW.message_id
				AND (((NEW.owner_kind IN ('user','agent','session'))
						AND d.recipient_kind = NEW.owner_kind
						AND d.recipient_ref = NEW.owner_ref)
					OR (NEW.owner_kind = 'user_group' AND d.recipient_kind = 'user')
					OR (NEW.owner_kind = 'agent_group' AND d.recipient_kind = 'agent'))) THEN
			RAISE EXCEPTION 'olivares: DecisionRequest accepted Delivery crosses message lineage'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.resolved_decision_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM sessions_work_decision d
			WHERE d.id = NEW.resolved_decision_id AND d.tenant_id = NEW.tenant_id
				AND d.workspace_id = NEW.workspace_id AND d.work_item_id = NEW.work_item_id) THEN
			RAISE EXCEPTION 'olivares: DecisionRequest resolved WorkDecision crosses WorkItem'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state NOT IN ('resolved','rejected','canceled','expired') AND EXISTS (
			SELECT 1 FROM sessions_decision_request p
			WHERE p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
				AND p.work_item_id = NEW.work_item_id AND p.decision_key = NEW.decision_key
				AND p.state NOT IN ('resolved','rejected','canceled','expired')
				AND p.id <> NEW.id) THEN
			RAISE EXCEPTION 'olivares: DecisionRequest has another active request for key'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			(to_jsonb(NEW) - ARRAY[
				'version','updated_at','state','accepted_delivery_id','accepted_at',
				'blocked_code','terminal_code','resolved_decision_id','last_response_seq'
			]) IS DISTINCT FROM
			(to_jsonb(OLD) - ARRAY[
				'version','updated_at','state','accepted_delivery_id','accepted_at',
				'blocked_code','terminal_code','resolved_decision_id','last_response_seq'
			])
			OR NEW.last_response_seq <> OLD.last_response_seq + 1
			OR (OLD.state <> NEW.state AND NOT (
				(OLD.state IN ('pending','blocked') AND NEW.state = 'accepted') OR
				(OLD.state = 'accepted' AND NEW.state = 'blocked') OR
				(OLD.state NOT IN ('resolved','rejected','canceled','expired')
					AND NEW.state IN ('resolved','rejected','canceled','expired'))))
			OR (NOT (OLD.state IN ('pending','blocked') AND NEW.state = 'accepted')
				AND (NEW.accepted_delivery_id IS DISTINCT FROM OLD.accepted_delivery_id
					OR NEW.accepted_at IS DISTINCT FROM OLD.accepted_at))
			OR (NEW.state IN ('accepted','blocked','resolved','rejected')
				AND NEW.updated_at >= OLD.due_at)
			OR OLD.state IN ('resolved','rejected','canceled','expired')
		) THEN
			RAISE EXCEPTION 'olivares: DecisionRequest immutable lineage or transition changed'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_decision_response' THEN
		IF NEW.response_seq < 1 OR NEW.from_state NOT IN ('pending','accepted','blocked')
			OR NEW.to_state NOT IN ('accepted','blocked','resolved','rejected','canceled','expired')
			OR NOT ((NEW.from_state IN ('pending','blocked') AND NEW.to_state = 'accepted')
				OR (NEW.from_state = 'accepted' AND NEW.to_state = 'blocked')
				OR NEW.to_state IN ('resolved','rejected','canceled','expired'))
			OR NEW.actor_kind NOT IN ('user','agent','session','system')
			OR octet_length(NEW.actor_ref) NOT BETWEEN 1 AND 512
			OR (NEW.actor_kind = 'session'
				AND (length(NEW.actor_ref) <> 40 OR left(NEW.actor_ref, 4) <> 'osn_'))
			OR (NEW.actor_kind IN ('user','agent') AND length(NEW.actor_ref) <> 36)
			OR NEW.response_encoding IS NULL
			OR NEW.response_encoding NOT IN ('plain_json','sealed_v1')
			OR NEW.response_schema IS DISTINCT FROM 'communication.decision-response.v1'
			OR NEW.response_digest IS NULL OR octet_length(NEW.response_digest) <> 32
			OR NEW.response_protection_generation IS NULL OR NEW.response_protection_generation < 1
			OR (NEW.response_encoding = 'plain_json' AND
				(NEW.response_plain_json IS NULL OR NEW.response_sealed_json IS NOT NULL
					OR jsonb_typeof(NEW.response_plain_json::jsonb) <> 'object'
					OR octet_length(NEW.response_plain_json::text) > 65536
					OR NEW.response_seal_key_version IS NOT NULL
					OR NEW.response_digest_key_version IS NOT NULL))
			OR (NEW.response_encoding = 'sealed_v1' AND
				(NEW.response_plain_json IS NOT NULL OR NEW.response_sealed_json IS NULL
					OR jsonb_typeof(NEW.response_sealed_json::jsonb) <> 'object'
					OR octet_length(NEW.response_sealed_json::text) > 196608
					OR octet_length(NEW.response_seal_key_version) NOT BETWEEN 1 AND 512
					OR octet_length(NEW.response_digest_key_version) NOT BETWEEN 1 AND 512
					OR NEW.response_seal_key_version IS NULL
					OR NEW.response_digest_key_version IS NULL
					OR NEW.response_seal_key_version <> btrim(NEW.response_seal_key_version)
					OR NEW.response_digest_key_version <> btrim(NEW.response_digest_key_version)
					OR NEW.response_seal_key_version ~ E'[\\r\\n]'
					OR NEW.response_digest_key_version ~ E'[\\r\\n]'
					OR jsonb_typeof(NEW.response_sealed_json::jsonb -> 'ciphertext')
						IS DISTINCT FROM 'string'
					OR COALESCE(length(NEW.response_sealed_json::jsonb ->> 'ciphertext'), 0) = 0
					OR jsonb_typeof(NEW.response_sealed_json::jsonb -> 'key_version')
						IS DISTINCT FROM 'string'
					OR NEW.response_sealed_json::jsonb ->> 'key_version' IS DISTINCT FROM
						NEW.response_seal_key_version))
			OR NEW.responded_at <> NEW.created_at
			OR (NEW.to_state = 'resolved') <> (NEW.work_decision_id IS NOT NULL)
			OR (NEW.to_state <> 'blocked' AND NEW.blocker_work_item_id IS NOT NULL) THEN
			RAISE EXCEPTION 'olivares: invalid DecisionResponse shape' USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_decision_request r
			WHERE r.id = NEW.request_id AND r.tenant_id = NEW.tenant_id
				AND r.workspace_id = NEW.workspace_id AND r.last_response_seq = NEW.response_seq
				AND r.state = NEW.to_state AND r.updated_at = NEW.responded_at
				AND r.request_encoding = NEW.response_encoding
				AND r.request_protection_generation = NEW.response_protection_generation
				AND r.accepted_delivery_id IS NOT DISTINCT FROM NEW.accepted_delivery_id
				AND r.resolved_decision_id IS NOT DISTINCT FROM NEW.work_decision_id) THEN
			RAISE EXCEPTION 'olivares: DecisionResponse crosses current request lineage'
				USING ERRCODE = '23514';
		END IF;
		IF (NEW.response_seq = 1 AND NEW.from_state <> 'pending')
			OR (NEW.response_seq > 1 AND NOT EXISTS (
				SELECT 1 FROM sessions_decision_response p
				WHERE p.tenant_id = NEW.tenant_id AND p.request_id = NEW.request_id
					AND p.response_seq = NEW.response_seq - 1
					AND p.to_state = NEW.from_state)) THEN
			RAISE EXCEPTION 'olivares: DecisionResponse sequence is not serialized'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.blocker_work_item_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM sessions_work_item w
			JOIN sessions_decision_request r
				ON r.id = NEW.request_id AND r.tenant_id = NEW.tenant_id
			WHERE w.id = NEW.blocker_work_item_id AND w.tenant_id = NEW.tenant_id
				AND w.workspace_id = NEW.workspace_id
				AND r.workspace_id = NEW.workspace_id) THEN
			RAISE EXCEPTION 'olivares: DecisionResponse blocker crosses WorkItem lineage'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.work_decision_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM sessions_work_decision d
			JOIN sessions_decision_request r
				ON r.id = NEW.request_id AND r.tenant_id = NEW.tenant_id
			WHERE d.id = NEW.work_decision_id AND d.tenant_id = NEW.tenant_id
				AND d.workspace_id = NEW.workspace_id
				AND d.work_item_id = r.work_item_id) THEN
			RAISE EXCEPTION 'olivares: DecisionResponse WorkDecision crosses request WorkItem'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_work_handoff' THEN
		IF NEW.from_kind NOT IN ('user','agent','session')
			OR NEW.to_kind NOT IN ('user','agent','session')
			OR (NEW.from_kind = 'session'
				AND (length(NEW.from_ref) <> 40 OR left(NEW.from_ref, 4) <> 'osn_'))
			OR (NEW.from_kind <> 'session' AND length(NEW.from_ref) <> 36)
			OR (NEW.to_kind = 'session'
				AND (length(NEW.to_ref) <> 40 OR left(NEW.to_ref, 4) <> 'osn_'))
			OR (NEW.to_kind <> 'session' AND length(NEW.to_ref) <> 36)
			OR (NEW.from_kind,NEW.from_ref) = (NEW.to_kind,NEW.to_ref)
			OR NEW.from_owner_epoch < 1 OR COALESCE(NEW.offered_lease_fence,0) < 0
			OR NEW.context_event_seq < 1 OR octet_length(NEW.context_hash) <> 32
			OR NEW.handoff_encoding IS NULL
			OR NEW.handoff_encoding NOT IN ('plain_json','sealed_v1')
			OR NEW.handoff_schema IS DISTINCT FROM 'communication.handoff.v1'
			OR NEW.handoff_digest IS NULL OR octet_length(NEW.handoff_digest) <> 32
			OR NEW.handoff_protection_generation IS NULL OR NEW.handoff_protection_generation < 1
			OR (NEW.handoff_encoding = 'plain_json' AND
				(NEW.handoff_plain_json IS NULL OR NEW.handoff_sealed_json IS NOT NULL
					OR jsonb_typeof(NEW.handoff_plain_json::jsonb) <> 'object'
					OR octet_length(NEW.handoff_plain_json::text) > 65536
					OR NEW.handoff_seal_key_version IS NOT NULL
					OR NEW.handoff_digest_key_version IS NOT NULL))
			OR (NEW.handoff_encoding = 'sealed_v1' AND
				(NEW.handoff_plain_json IS NOT NULL OR NEW.handoff_sealed_json IS NULL
					OR jsonb_typeof(NEW.handoff_sealed_json::jsonb) <> 'object'
					OR octet_length(NEW.handoff_sealed_json::text) > 196608
					OR octet_length(NEW.handoff_seal_key_version) NOT BETWEEN 1 AND 512
					OR octet_length(NEW.handoff_digest_key_version) NOT BETWEEN 1 AND 512
					OR NEW.handoff_seal_key_version IS NULL
					OR NEW.handoff_digest_key_version IS NULL
					OR NEW.handoff_seal_key_version <> btrim(NEW.handoff_seal_key_version)
					OR NEW.handoff_digest_key_version <> btrim(NEW.handoff_digest_key_version)
					OR NEW.handoff_seal_key_version ~ E'[\\r\\n]'
					OR NEW.handoff_digest_key_version ~ E'[\\r\\n]'
					OR jsonb_typeof(NEW.handoff_sealed_json::jsonb -> 'ciphertext')
						IS DISTINCT FROM 'string'
					OR COALESCE(length(NEW.handoff_sealed_json::jsonb ->> 'ciphertext'), 0) = 0
					OR jsonb_typeof(NEW.handoff_sealed_json::jsonb -> 'key_version')
						IS DISTINCT FROM 'string'
					OR NEW.handoff_sealed_json::jsonb ->> 'key_version' IS DISTINCT FROM
						NEW.handoff_seal_key_version))
			OR NEW.state NOT IN ('offered','accepted','rejected','withdrawn','expired')
			OR NEW.ack_deadline <= NEW.created_at THEN
			RAISE EXCEPTION 'olivares: invalid Handoff envelope' USING ERRCODE = '23514';
		END IF;
		IF NEW.terminal_reason_encoding IS NULL AND (
			NEW.terminal_reason_plain_json IS NOT NULL
			OR NEW.terminal_reason_sealed_json IS NOT NULL
			OR NEW.terminal_reason_schema IS NOT NULL OR NEW.terminal_reason_digest IS NOT NULL
			OR NEW.terminal_reason_seal_key_version IS NOT NULL
			OR NEW.terminal_reason_digest_key_version IS NOT NULL
			OR NEW.terminal_reason_protection_generation IS NOT NULL) THEN
			RAISE EXCEPTION 'olivares: invalid Handoff terminal ProtectedPayload'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.terminal_reason_encoding IS NOT NULL AND NOT COALESCE((
			(NEW.terminal_reason_encoding IS NULL
				AND NEW.terminal_reason_plain_json IS NULL
				AND NEW.terminal_reason_sealed_json IS NULL
				AND NEW.terminal_reason_schema IS NULL
				AND NEW.terminal_reason_digest IS NULL
				AND NEW.terminal_reason_seal_key_version IS NULL
				AND NEW.terminal_reason_digest_key_version IS NULL
				AND NEW.terminal_reason_protection_generation IS NULL)
			OR (
				NEW.terminal_reason_encoding IN ('plain_json','sealed_v1')
				AND NEW.terminal_reason_encoding = NEW.handoff_encoding
				AND NEW.terminal_reason_schema = 'communication.handoff-terminal-reason.v1'
				AND octet_length(NEW.terminal_reason_digest) = 32
				AND NEW.terminal_reason_protection_generation =
					NEW.handoff_protection_generation
				AND (
					(NEW.terminal_reason_encoding = 'plain_json'
						AND NEW.terminal_reason_plain_json IS NOT NULL
						AND jsonb_typeof(NEW.terminal_reason_plain_json::jsonb) = 'object'
						AND octet_length(NEW.terminal_reason_plain_json::text) <= 65536
						AND NEW.terminal_reason_sealed_json IS NULL
						AND NEW.terminal_reason_seal_key_version IS NULL
						AND NEW.terminal_reason_digest_key_version IS NULL)
					OR (NEW.terminal_reason_encoding = 'sealed_v1'
						AND NEW.terminal_reason_plain_json IS NULL
						AND NEW.terminal_reason_sealed_json IS NOT NULL
						AND jsonb_typeof(NEW.terminal_reason_sealed_json::jsonb) = 'object'
						AND octet_length(NEW.terminal_reason_sealed_json::text) <= 196608
						AND octet_length(NEW.terminal_reason_seal_key_version)
							BETWEEN 1 AND 512
						AND octet_length(NEW.terminal_reason_digest_key_version)
							BETWEEN 1 AND 512
						AND NEW.terminal_reason_seal_key_version =
							btrim(NEW.terminal_reason_seal_key_version)
						AND NEW.terminal_reason_digest_key_version =
							btrim(NEW.terminal_reason_digest_key_version)
						AND NEW.terminal_reason_seal_key_version !~ E'[\\r\\n]'
						AND NEW.terminal_reason_digest_key_version !~ E'[\\r\\n]'
						AND jsonb_typeof(NEW.terminal_reason_sealed_json::jsonb -> 'ciphertext') =
							'string'
						AND COALESCE(length(
							NEW.terminal_reason_sealed_json::jsonb ->> 'ciphertext'), 0) > 0
						AND jsonb_typeof(
							NEW.terminal_reason_sealed_json::jsonb -> 'key_version') = 'string'
						AND NEW.terminal_reason_sealed_json::jsonb ->> 'key_version' =
							NEW.terminal_reason_seal_key_version)
				)
			)
		), false) THEN
			RAISE EXCEPTION 'olivares: invalid Handoff terminal ProtectedPayload'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'offered' AND (
				NEW.ack_id IS NOT NULL OR NEW.accepted_at IS NOT NULL
				OR NEW.rejected_at IS NOT NULL OR NEW.withdrawn_at IS NOT NULL
				OR NEW.expired_at IS NOT NULL OR NEW.terminal_code IS NOT NULL
				OR NEW.terminal_reason_encoding IS NOT NULL
				OR NEW.resulting_lease_fence IS NOT NULL)
			OR NEW.state = 'accepted' AND (
				NEW.ack_id IS NULL OR NEW.accepted_at IS NULL
				OR NEW.accepted_at < NEW.created_at OR NEW.accepted_at > NEW.updated_at
				OR NEW.accepted_at >= NEW.ack_deadline
				OR NEW.rejected_at IS NOT NULL OR NEW.withdrawn_at IS NOT NULL
				OR NEW.expired_at IS NOT NULL OR NEW.terminal_code IS NOT NULL
				OR NEW.terminal_reason_encoding IS NOT NULL
				OR NEW.resulting_lease_fence IS NULL
				OR NEW.resulting_lease_fence <= COALESCE(NEW.offered_lease_fence,0))
			OR NEW.state IN ('rejected','withdrawn','expired') AND (
				NEW.ack_id IS NOT NULL OR NEW.accepted_at IS NOT NULL
				OR (NEW.state = 'rejected') <> (NEW.rejected_at IS NOT NULL)
				OR (NEW.state = 'withdrawn') <> (NEW.withdrawn_at IS NOT NULL)
				OR (NEW.state = 'expired') <> (NEW.expired_at IS NOT NULL)
				OR NEW.terminal_code IS NULL
				OR NEW.terminal_code !~ '^[a-z0-9._-]{1,128}$'
				OR NEW.terminal_reason_encoding IS NULL
				OR NEW.resulting_lease_fence IS NOT NULL)
			OR NEW.rejected_at IS NOT NULL AND
				(NEW.rejected_at < NEW.created_at OR NEW.rejected_at > NEW.updated_at)
			OR NEW.withdrawn_at IS NOT NULL AND
				(NEW.withdrawn_at < NEW.created_at OR NEW.withdrawn_at > NEW.updated_at)
			OR NEW.expired_at IS NOT NULL AND
				(NEW.expired_at < NEW.created_at OR NEW.expired_at > NEW.updated_at)
			OR NEW.state = 'expired' AND NEW.expired_at < NEW.ack_deadline THEN
			RAISE EXCEPTION 'olivares: Handoff state evidence is inconsistent'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'offered' THEN
			PERFORM pg_advisory_xact_lock(hashtextextended(concat_ws('|',
				'sessions_work_handoff_offered', NEW.tenant_id::text,
				NEW.workspace_id::text, NEW.work_item_id::text), 0));
		END IF;
		IF NOT EXISTS (
			SELECT 1 FROM sessions_message m
			JOIN sessions_message_delivery d ON d.message_id = m.id AND d.tenant_id = m.tenant_id
			JOIN sessions_work_item w ON w.id = m.work_item_id AND w.tenant_id = m.tenant_id
			WHERE m.id = NEW.message_id AND m.tenant_id = NEW.tenant_id
				AND m.workspace_id = NEW.workspace_id AND m.work_item_id = NEW.work_item_id
				AND m.kind = 'handoff_offer' AND d.id = NEW.delivery_id
				AND d.workspace_id = NEW.workspace_id AND d.recipient_kind = NEW.to_kind
				AND d.recipient_ref = NEW.to_ref AND d.required
				AND w.workspace_id = NEW.workspace_id
				AND m.payload_encoding = NEW.handoff_encoding
				AND m.payload_protection_generation = NEW.handoff_protection_generation
				AND (m.expires_at IS NULL OR m.expires_at >= NEW.ack_deadline)
				AND (SELECT count(*) FROM sessions_message_delivery rd
					WHERE rd.tenant_id = m.tenant_id AND rd.workspace_id = m.workspace_id
						AND rd.message_id = m.id AND rd.required) = 1) THEN
			RAISE EXCEPTION 'olivares: Handoff message/delivery/work lineage is invalid'
				USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_work_event e
			WHERE e.tenant_id = NEW.tenant_id AND e.workspace_id = NEW.workspace_id
				AND e.aggregate_kind = 'sessions.work_item' AND e.aggregate_id = NEW.work_item_id
				AND e.seq = NEW.context_event_seq) THEN
			RAISE EXCEPTION 'olivares: Handoff context Event crosses WorkItem lineage'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'accepted' AND NOT EXISTS (
			SELECT 1 FROM sessions_message_ack a WHERE a.id = NEW.ack_id
				AND a.tenant_id = NEW.tenant_id AND a.workspace_id = NEW.workspace_id
				AND a.delivery_id = NEW.delivery_id AND NOT a.late) THEN
			RAISE EXCEPTION 'olivares: accepted Handoff Ack crosses exact Delivery'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'offered' AND EXISTS (
			SELECT 1 FROM sessions_work_handoff p
			WHERE p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
				AND p.work_item_id = NEW.work_item_id AND p.state = 'offered'
				AND p.id <> NEW.id) THEN
			RAISE EXCEPTION 'olivares: WorkItem has another offered Handoff'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			(to_jsonb(NEW) - ARRAY[
				'version','updated_at','state','ack_id','accepted_at','rejected_at',
				'withdrawn_at','expired_at','terminal_code','terminal_reason_encoding',
				'terminal_reason_plain_json','terminal_reason_sealed_json','terminal_reason_schema',
				'terminal_reason_digest','terminal_reason_seal_key_version',
				'terminal_reason_digest_key_version','terminal_reason_protection_generation',
				'resulting_lease_fence'
			]) IS DISTINCT FROM
			(to_jsonb(OLD) - ARRAY[
				'version','updated_at','state','ack_id','accepted_at','rejected_at',
				'withdrawn_at','expired_at','terminal_code','terminal_reason_encoding',
				'terminal_reason_plain_json','terminal_reason_sealed_json','terminal_reason_schema',
				'terminal_reason_digest','terminal_reason_seal_key_version',
				'terminal_reason_digest_key_version','terminal_reason_protection_generation',
				'resulting_lease_fence'
			])
			OR (OLD.state <> NEW.state AND NOT
				(OLD.state = 'offered'
					AND NEW.state IN ('accepted','rejected','withdrawn','expired')))
			OR (NEW.state = 'accepted' AND (
				NEW.updated_at >= OLD.ack_deadline
				OR NEW.accepted_at IS DISTINCT FROM NEW.updated_at))
			OR OLD.state <> 'offered'
		) THEN
			RAISE EXCEPTION 'olivares: Handoff immutable lineage or transition changed'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_delivery_dispatch' THEN
		IF (TG_OP = 'INSERT' AND (NEW.state <> 'pending' OR NEW.attempt_count <> 0))
			OR NEW.endpoint_generation < 1
			OR (NEW.route_rule_id IS NULL) <> (NEW.route_rule_generation IS NULL)
			OR (NEW.route_rule_generation IS NOT NULL AND NEW.route_rule_generation < 1)
			OR NEW.dispatch_generation < 1 OR NEW.reroute_rung < 0 OR NEW.policy_generation < 1
			OR NEW.state NOT IN
				('pending','in_flight','succeeded','failed','unknown','dead_letter','superseded')
			OR NEW.attempt_count NOT BETWEEN 0 AND 1
			OR octet_length(NEW.idempotency_key_hash) <> 32
			OR (NEW.dispatch_generation = 1 AND
				(NEW.id <> NEW.root_dispatch_id OR NEW.predecessor_id IS NOT NULL
					OR NEW.reroute_rung <> 0))
			OR (NEW.dispatch_generation > 1 AND
				(NEW.predecessor_id IS NULL OR NEW.id = NEW.root_dispatch_id))
			OR (NEW.claim_owner IS NULL) <> (NEW.claim_until IS NULL)
			OR (NEW.claim_owner IS NOT NULL AND
				(NEW.claim_owner !~ '^[a-z0-9._-]{1,128}$'
					OR NEW.claim_until <= NEW.created_at))
			OR (NEW.next_attempt_at IS NOT NULL
				AND (NEW.state <> 'pending' OR NEW.next_attempt_at < NEW.updated_at))
			OR (NEW.last_code IS NOT NULL AND NEW.last_code !~ '^[a-z0-9._-]{1,128}$')
			OR (NEW.resolution_code IS NOT NULL
				AND NEW.resolution_code !~ '^[a-z0-9._-]{1,128}$')
			OR (NEW.state IN ('failed','unknown') AND
				(NEW.resolution_deadline_at IS NULL
					OR NEW.resolution_deadline_at <= NEW.updated_at))
			OR (NEW.state IN ('succeeded','dead_letter','superseded')) <>
				(NEW.settled_at IS NOT NULL)
			OR (NEW.settled_at IS NOT NULL AND NEW.settled_at < NEW.created_at) THEN
			RAISE EXCEPTION 'olivares: invalid DeliveryDispatch envelope'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'pending' AND (
				NEW.attempt_count <> 0 OR NEW.claim_owner IS NOT NULL
				OR NEW.last_verdict IS NOT NULL OR NEW.last_code IS NOT NULL
				OR NEW.resolution_deadline_at IS NOT NULL OR NEW.resolution_code IS NOT NULL)
			OR NEW.state = 'in_flight' AND (
				NEW.attempt_count <> 1 OR NEW.claim_owner IS NULL
				OR NEW.last_verdict IS NOT NULL OR NEW.last_code IS NOT NULL
				OR NEW.resolution_deadline_at IS NOT NULL OR NEW.resolution_code IS NOT NULL)
			OR NEW.state = 'succeeded' AND (
				NEW.attempt_count <> 1 OR NEW.claim_owner IS NOT NULL
				OR NEW.last_verdict IS NULL OR NEW.last_verdict <> 'LIMPIO'
				OR NEW.last_code IS NULL
				OR NEW.resolution_deadline_at IS NOT NULL OR NEW.resolution_code IS NULL)
			OR NEW.state IN ('failed','unknown') AND (
				NEW.attempt_count <> 1 OR NEW.claim_owner IS NOT NULL
				OR NEW.last_verdict IS NULL
				OR NEW.last_verdict <> CASE NEW.state WHEN 'failed' THEN 'ROTO'
					ELSE 'NO_HE_PODIDO_MIRAR' END
				OR NEW.last_code IS NULL OR NEW.resolution_code IS NULL)
			OR NEW.state IN ('dead_letter','superseded') AND (
				NEW.attempt_count <> 1 OR NEW.claim_owner IS NOT NULL
				OR NEW.last_verdict IS NULL
				OR NEW.last_verdict NOT IN ('ROTO','NO_HE_PODIDO_MIRAR')
				OR NEW.last_code IS NULL OR NEW.resolution_code IS NULL
				OR NEW.resolution_deadline_at IS NOT NULL) THEN
			RAISE EXCEPTION 'olivares: DeliveryDispatch state evidence is inconsistent'
				USING ERRCODE = '23514';
		END IF;
		IF NOT COALESCE((
			(NEW.reconciled_attempt_id IS NULL AND NEW.reconciled_endpoint_id IS NULL
				AND NEW.reconciled_endpoint_generation IS NULL
				AND NEW.reconciliation_verdict IS NULL AND NEW.reconciliation_code IS NULL
				AND NEW.reconciliation_evidence_ref IS NULL
				AND NEW.reconciliation_observed_at IS NULL
				AND NEW.provider_acceptance_hash IS NULL)
			OR (NEW.reconciled_attempt_id IS NOT NULL
				AND NEW.reconciled_endpoint_id = NEW.endpoint_id
				AND NEW.reconciled_endpoint_generation = NEW.endpoint_generation
				AND NEW.reconciliation_verdict IN ('LIMPIO','ROTO','NO_HE_PODIDO_MIRAR')
				AND NEW.reconciliation_code ~ '^[a-z0-9._-]{1,128}$'
				AND octet_length(NEW.reconciliation_evidence_ref) BETWEEN 1 AND 512
				AND NEW.reconciliation_evidence_ref = btrim(NEW.reconciliation_evidence_ref)
				AND NEW.reconciliation_evidence_ref !~ E'[\\r\\n]'
				AND NEW.reconciliation_observed_at BETWEEN NEW.created_at AND NEW.updated_at
				AND octet_length(NEW.provider_acceptance_hash) = 32
				AND NEW.attempt_count = 1
				AND NEW.state IN ('succeeded','failed','dead_letter','superseded'))
		), false) THEN
			RAISE EXCEPTION 'olivares: DeliveryDispatch reconciliation shape is incomplete'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.reconciled_attempt_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM sessions_delivery_attempt a
			WHERE a.id = NEW.reconciled_attempt_id AND a.tenant_id = NEW.tenant_id
				AND a.workspace_id = NEW.workspace_id AND a.dispatch_id = NEW.id
				AND a.attempt_seq = 1 AND a.state IN ('finished','abandoned')) THEN
			RAISE EXCEPTION 'olivares: DeliveryDispatch reconciliation crosses exact Attempt'
				USING ERRCODE = '23514';
		END IF;
		IF (NEW.state = 'in_flight' AND EXISTS (
				SELECT 1 FROM sessions_delivery_attempt a
				WHERE a.tenant_id = NEW.tenant_id AND a.workspace_id = NEW.workspace_id
					AND a.dispatch_id = NEW.id AND a.attempt_seq = 1
					AND a.state <> 'reserved'))
			OR (NEW.state IN ('succeeded','failed','unknown','dead_letter','superseded')
				AND NOT EXISTS (
					SELECT 1 FROM sessions_delivery_attempt a
					WHERE a.tenant_id = NEW.tenant_id AND a.workspace_id = NEW.workspace_id
						AND a.dispatch_id = NEW.id AND a.attempt_seq = 1
						AND (
							(NEW.state = 'succeeded' AND (
								(a.state = 'finished' AND a.transmit_boundary = 'crossed'
									AND a.verdict = 'LIMPIO' AND NEW.last_verdict = a.verdict)
								OR (a.state IN ('finished','abandoned')
									AND a.transmit_boundary = 'unknown'
									AND a.verdict = 'NO_HE_PODIDO_MIRAR'
									AND NEW.reconciled_attempt_id = a.id
									AND NEW.reconciliation_verdict = 'LIMPIO')))
							OR (NEW.state = 'failed' AND (
								(a.state = 'finished' AND a.transmit_boundary = 'not_crossed'
									AND a.verdict = 'ROTO' AND NEW.last_verdict = a.verdict)
								OR (a.state IN ('finished','abandoned')
									AND a.transmit_boundary = 'unknown'
									AND a.verdict = 'NO_HE_PODIDO_MIRAR'
									AND NEW.reconciled_attempt_id = a.id
									AND NEW.reconciliation_verdict = 'ROTO')))
							OR (NEW.state = 'unknown' AND a.state IN ('finished','abandoned')
								AND a.transmit_boundary = 'unknown'
								AND a.verdict = 'NO_HE_PODIDO_MIRAR'
								AND NEW.last_verdict = a.verdict)
							OR (NEW.state = 'dead_letter' AND a.state IN ('finished','abandoned')
								AND ((a.verdict = 'NO_HE_PODIDO_MIRAR'
									AND NEW.reconciled_attempt_id = a.id
									AND NEW.reconciliation_verdict = 'NO_HE_PODIDO_MIRAR')
									OR (a.verdict = 'ROTO' AND NEW.last_verdict = a.verdict)))
							OR (NEW.state = 'superseded' AND (
								(a.state = 'finished' AND a.transmit_boundary = 'not_crossed'
									AND a.verdict = 'ROTO' AND NEW.last_verdict = 'ROTO')
								OR (a.state IN ('finished','abandoned')
									AND a.transmit_boundary = 'unknown'
									AND a.verdict = 'NO_HE_PODIDO_MIRAR'
									AND NEW.reconciled_attempt_id = a.id
									AND NEW.reconciliation_verdict = 'ROTO')))))) THEN
			RAISE EXCEPTION 'olivares: DeliveryDispatch state contradicts its exact Attempt'
				USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_message_delivery d WHERE d.id = NEW.delivery_id
			AND d.tenant_id = NEW.tenant_id AND d.workspace_id = NEW.workspace_id)
			OR NOT EXISTS (SELECT 1 FROM sessions_communication_endpoint e
				WHERE e.id = NEW.endpoint_id AND e.tenant_id = NEW.tenant_id
					AND e.workspace_id = NEW.workspace_id
					AND e.generation = NEW.endpoint_generation)
			OR (NEW.route_rule_id IS NOT NULL AND NOT EXISTS (
				SELECT 1 FROM sessions_channel_route r WHERE r.id = NEW.route_rule_id
					AND r.tenant_id = NEW.tenant_id AND r.workspace_id = NEW.workspace_id
					AND r.generation = NEW.route_rule_generation)) THEN
			RAISE EXCEPTION 'olivares: DeliveryDispatch route crosses generation lineage'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'INSERT' AND NEW.dispatch_generation > 1 AND NOT EXISTS (
			SELECT 1 FROM sessions_delivery_dispatch p WHERE p.id = NEW.predecessor_id
				AND p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
				AND p.root_dispatch_id = NEW.root_dispatch_id AND p.delivery_id = NEW.delivery_id
				AND p.dispatch_generation + 1 = NEW.dispatch_generation
				AND p.state = 'superseded' AND p.updated_at = NEW.created_at
				AND p.settled_at = NEW.created_at
				AND NEW.reroute_rung BETWEEN p.reroute_rung AND p.reroute_rung + 1
				AND ((NEW.reroute_rung = p.reroute_rung
						AND NEW.endpoint_id = p.endpoint_id
						AND NEW.endpoint_generation = p.endpoint_generation
						AND NEW.route_rule_id IS NOT DISTINCT FROM p.route_rule_id
						AND NEW.route_rule_generation IS NOT DISTINCT FROM p.route_rule_generation
						AND NEW.policy_generation = p.policy_generation)
					OR NEW.reroute_rung = p.reroute_rung + 1)) THEN
			RAISE EXCEPTION 'olivares: DeliveryDispatch predecessor lineage is not serialized'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'INSERT' AND NEW.state IN ('pending','in_flight') AND EXISTS (
			SELECT 1 FROM sessions_delivery_dispatch p
			WHERE p.tenant_id = NEW.tenant_id AND p.root_dispatch_id = NEW.root_dispatch_id
				AND p.state IN ('pending','in_flight')) THEN
			RAISE EXCEPTION 'olivares: DeliveryDispatch root has multiple current generations'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			(to_jsonb(NEW) - ARRAY[
				'version','updated_at','state','attempt_count','next_attempt_at','claim_owner',
				'claim_until','last_verdict','last_code','resolution_deadline_at','resolution_code',
				'reconciled_attempt_id','reconciled_endpoint_id','reconciled_endpoint_generation',
				'reconciliation_verdict','reconciliation_code','reconciliation_evidence_ref',
				'reconciliation_observed_at','provider_acceptance_hash','settled_at'
			]) IS DISTINCT FROM
			(to_jsonb(OLD) - ARRAY[
				'version','updated_at','state','attempt_count','next_attempt_at','claim_owner',
				'claim_until','last_verdict','last_code','resolution_deadline_at','resolution_code',
				'reconciled_attempt_id','reconciled_endpoint_id','reconciled_endpoint_generation',
				'reconciliation_verdict','reconciliation_code','reconciliation_evidence_ref',
				'reconciliation_observed_at','provider_acceptance_hash','settled_at'
			])
			OR NEW.attempt_count < OLD.attempt_count
			OR (OLD.state <> NEW.state AND NOT (
				(OLD.state = 'pending' AND NEW.state = 'in_flight') OR
				(OLD.state = 'in_flight' AND NEW.state IN ('succeeded','failed','unknown')) OR
				(OLD.state = 'failed' AND NEW.state IN ('dead_letter','superseded')) OR
				(OLD.state = 'unknown'
					AND NEW.state IN ('succeeded','failed','dead_letter','superseded'))))
			OR (OLD.state IN ('failed','unknown') AND NEW.state = OLD.state)
			OR (OLD.state IN ('failed','unknown') AND NEW.state = 'dead_letter'
				AND (OLD.resolution_deadline_at IS NULL
					OR NEW.updated_at < OLD.resolution_deadline_at))
			OR (OLD.state = 'unknown' AND NEW.state <> OLD.state
				AND (NEW.reconciliation_observed_at IS NULL
					OR NEW.reconciliation_observed_at < OLD.updated_at))
			OR (OLD.state = 'failed' AND NEW.state IN ('dead_letter','superseded')
				AND NEW.last_verdict IS DISTINCT FROM OLD.last_verdict)
			OR OLD.state IN ('succeeded','dead_letter','superseded')
		) THEN
			RAISE EXCEPTION 'olivares: DeliveryDispatch immutable generation or state changed'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'superseded' AND NEW.last_verdict = 'NO_HE_PODIDO_MIRAR'
			AND NEW.reconciliation_verdict IS DISTINCT FROM 'ROTO'
			OR NEW.state = 'dead_letter' AND NEW.last_verdict = 'NO_HE_PODIDO_MIRAR'
			AND NEW.reconciliation_verdict IS DISTINCT FROM 'NO_HE_PODIDO_MIRAR' THEN
			RAISE EXCEPTION 'olivares: DeliveryDispatch UNKNOWN reconciliation is invalid'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_delivery_attempt' THEN
		IF (TG_OP = 'INSERT' AND NEW.state <> 'reserved')
			OR NEW.attempt_seq <> 1 OR NEW.state NOT IN ('reserved','finished','abandoned')
			OR NEW.transmit_boundary NOT IN ('not_crossed','crossed','unknown')
			OR NEW.started_at < NEW.created_at OR octet_length(NEW.request_hash) <> 32
			OR (NEW.provider_receipt_hash IS NOT NULL
				AND octet_length(NEW.provider_receipt_hash) <> 32)
			OR NEW.state = 'reserved' AND (
				NEW.transmit_boundary <> 'unknown' OR NEW.finished_at IS NOT NULL
				OR NEW.verdict IS NOT NULL OR NEW.code IS NOT NULL
				OR NEW.provider_receipt_hash IS NOT NULL)
			OR NEW.state <> 'reserved' AND (
				NEW.finished_at IS NULL OR NEW.finished_at < NEW.started_at
				OR NEW.finished_at > NEW.updated_at
				OR NEW.verdict IS NULL
				OR NEW.verdict NOT IN ('LIMPIO','ROTO','NO_HE_PODIDO_MIRAR')
				OR NEW.code IS NULL OR NEW.code !~ '^[a-z0-9._-]{1,128}$')
			OR NEW.state = 'abandoned' AND (
				NEW.transmit_boundary <> 'unknown' OR NEW.verdict <> 'NO_HE_PODIDO_MIRAR'
				OR NEW.provider_receipt_hash IS NOT NULL)
			OR NEW.state = 'finished' AND NOT (
				(NEW.transmit_boundary = 'crossed' AND NEW.verdict = 'LIMPIO'
					AND NEW.provider_receipt_hash IS NOT NULL
					AND octet_length(NEW.provider_receipt_hash) = 32)
				OR (NEW.transmit_boundary = 'not_crossed' AND NEW.verdict = 'ROTO'
					AND NEW.provider_receipt_hash IS NULL)
				OR (NEW.transmit_boundary = 'unknown'
					AND NEW.verdict = 'NO_HE_PODIDO_MIRAR'
					AND NEW.provider_receipt_hash IS NULL)) THEN
			RAISE EXCEPTION 'olivares: invalid DeliveryAttempt shape' USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_delivery_dispatch d WHERE d.id = NEW.dispatch_id
			AND d.tenant_id = NEW.tenant_id AND d.workspace_id = NEW.workspace_id
			AND d.attempt_count = 1) THEN
			RAISE EXCEPTION 'olivares: DeliveryAttempt crosses Dispatch generation'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (
			(to_jsonb(NEW) - ARRAY[
				'version','updated_at','state','transmit_boundary','finished_at',
				'verdict','code','provider_receipt_hash'
			]) IS DISTINCT FROM
			(to_jsonb(OLD) - ARRAY[
				'version','updated_at','state','transmit_boundary','finished_at',
				'verdict','code','provider_receipt_hash'
			])
			OR (OLD.state <> NEW.state AND NOT
				(OLD.state = 'reserved' AND NEW.state IN ('finished','abandoned')))
			OR OLD.state <> 'reserved'
		) THEN
			RAISE EXCEPTION 'olivares: DeliveryAttempt immutable invocation changed'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_communication_command' THEN
		IF NEW.command_id IS NULL OR NEW.command_id !~
				'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
			OR (NEW.result_id IS NOT NULL AND NEW.result_id !~
				'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
			OR (NEW.event_id IS NOT NULL AND NEW.event_id !~
				'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
			OR octet_length(NEW.actor_fingerprint) <> 32
			OR octet_length(NEW.command_scope) NOT BETWEEN 1 AND 512
			OR NEW.command_scope <> btrim(NEW.command_scope)
			OR NEW.command_scope ~ E'[\\r\\n]'
			OR octet_length(NEW.idempotency_key_hash) <> 32
			OR octet_length(NEW.request_digest) <> 32
			OR (NEW.seal_key_version IS NULL) <> (NEW.digest_key_version IS NULL)
			OR (NEW.seal_key_version IS NOT NULL AND
				(octet_length(NEW.seal_key_version) NOT BETWEEN 1 AND 512
					OR NEW.seal_key_version <> btrim(NEW.seal_key_version)
					OR NEW.seal_key_version ~ E'[\\r\\n]'))
			OR (NEW.digest_key_version IS NOT NULL AND
				(octet_length(NEW.digest_key_version) NOT BETWEEN 1 AND 512
					OR NEW.digest_key_version <> btrim(NEW.digest_key_version)
					OR NEW.digest_key_version ~ E'[\\r\\n]'))
			OR octet_length(NEW.plan_hash) <> 32
			OR octet_length(NEW.result_kind) NOT BETWEEN 1 AND 512
			OR NEW.result_kind <> btrim(NEW.result_kind)
			OR NEW.result_kind ~ E'[\\r\\n]'
			OR NEW.http_status NOT BETWEEN 100 AND 599
			OR jsonb_typeof(NEW.response_projection_json::jsonb) IS DISTINCT FROM 'object'
			OR octet_length(NEW.response_projection_json::text) > 4096
			OR EXISTS (SELECT 1 FROM jsonb_object_keys(NEW.response_projection_json::jsonb) key
				WHERE key NOT IN ('ids','version','state','counts','digests'))
			OR (NEW.response_projection_json::jsonb ? 'ids'
				AND NEW.response_projection_json::jsonb -> 'ids' <> 'null'::jsonb
				AND jsonb_typeof(NEW.response_projection_json::jsonb -> 'ids') <> 'object')
			OR (NEW.response_projection_json::jsonb ? 'counts'
				AND NEW.response_projection_json::jsonb -> 'counts' <> 'null'::jsonb
				AND jsonb_typeof(NEW.response_projection_json::jsonb -> 'counts') <> 'object')
			OR (NEW.response_projection_json::jsonb ? 'digests'
				AND NEW.response_projection_json::jsonb -> 'digests' <> 'null'::jsonb
				AND jsonb_typeof(NEW.response_projection_json::jsonb -> 'digests') <> 'object')
			OR (NEW.response_projection_json::jsonb ? 'version'
				AND NEW.response_projection_json::jsonb -> 'version' <> 'null'::jsonb
				AND (jsonb_typeof(NEW.response_projection_json::jsonb -> 'version') <> 'number'
					OR NEW.response_projection_json::jsonb ->> 'version'
						!~ '^(0|[1-9][0-9]{0,18})$'
					OR (length(NEW.response_projection_json::jsonb ->> 'version') = 19
						AND NEW.response_projection_json::jsonb ->> 'version' >
							'9223372036854775807')))
			OR (NEW.response_projection_json::jsonb ? 'state'
				AND NEW.response_projection_json::jsonb -> 'state' <> 'null'::jsonb
				AND (jsonb_typeof(NEW.response_projection_json::jsonb -> 'state') <> 'string'
					OR NEW.response_projection_json::jsonb ->> 'state' NOT IN (
						'', 'active','archived','revoked','expired','paused','disabled','stale',
						'draft','published','retracted','discarded','available','acknowledged',
						'undeliverable','pending','accepted','blocked','resolved','rejected',
						'canceled','offered','withdrawn','in_flight','succeeded','failed',
						'unknown','dead_letter','superseded')))
			OR ((SELECT count(*) FROM jsonb_object_keys(CASE
					WHEN jsonb_typeof(NEW.response_projection_json::jsonb -> 'ids') = 'object'
					THEN NEW.response_projection_json::jsonb -> 'ids' ELSE '{}'::jsonb END))
				+ (SELECT count(*) FROM jsonb_object_keys(CASE
					WHEN jsonb_typeof(NEW.response_projection_json::jsonb -> 'counts') = 'object'
					THEN NEW.response_projection_json::jsonb -> 'counts' ELSE '{}'::jsonb END))
				+ (SELECT count(*) FROM jsonb_object_keys(CASE
					WHEN jsonb_typeof(NEW.response_projection_json::jsonb -> 'digests') = 'object'
					THEN NEW.response_projection_json::jsonb -> 'digests' ELSE '{}'::jsonb END))) > 32
			OR EXISTS (
				SELECT 1 FROM jsonb_each(CASE
					WHEN jsonb_typeof(NEW.response_projection_json::jsonb -> 'ids') = 'object'
					THEN NEW.response_projection_json::jsonb -> 'ids' ELSE '{}'::jsonb END)
					AS entry(key,value)
				WHERE entry.key NOT IN (
					'channel_id','message_id','delivery_id','ack_id','request_id','response_id',
					'handoff_id','dispatch_id','attempt_id','result_id','work_item_id','event_id')
					OR jsonb_typeof(entry.value) <> 'string'
					OR entry.value #>> '{}' !~
						'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
			OR EXISTS (
				SELECT 1 FROM jsonb_each(CASE
					WHEN jsonb_typeof(NEW.response_projection_json::jsonb -> 'counts') = 'object'
					THEN NEW.response_projection_json::jsonb -> 'counts' ELSE '{}'::jsonb END)
					AS entry(key,value)
				WHERE entry.key NOT IN (
					'required','acknowledged','viable','unmet','quorum','resolved_count',
					'delivery_count')
					OR jsonb_typeof(entry.value) <> 'number'
					OR entry.value #>> '{}' !~ '^(0|[1-9][0-9]{0,18})$'
					OR (length(entry.value #>> '{}') = 19
						AND entry.value #>> '{}' > '9223372036854775807'))
			OR EXISTS (
				SELECT 1 FROM jsonb_each(CASE
					WHEN jsonb_typeof(NEW.response_projection_json::jsonb -> 'digests') = 'object'
					THEN NEW.response_projection_json::jsonb -> 'digests' ELSE '{}'::jsonb END)
					AS entry(key,value)
				WHERE entry.key NOT IN (
					'request','plan','response','audience','route_reasons','contributions','payload')
					OR jsonb_typeof(entry.value) <> 'string'
					OR entry.value #>> '{}' !~ '^[A-Za-z0-9+/]{43}=$')
			OR octet_length(NEW.response_digest) <> 32 OR NEW.audit_seq < 0
			OR (NEW.audit_seq = 0) <> (NEW.audit_hash IS NULL)
			OR (NEW.audit_seq > 0 AND octet_length(NEW.audit_hash) <> 32)
			OR NEW.completed_at <> NEW.created_at THEN
			RAISE EXCEPTION 'olivares: invalid CommunicationCommand receipt'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.event_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM sessions_work_event e WHERE e.event_id = NEW.event_id
				AND e.tenant_id = NEW.tenant_id AND e.workspace_id = NEW.workspace_id) THEN
			RAISE EXCEPTION 'olivares: CommunicationCommand Event crosses tenant/workspace'
				USING ERRCODE = '23514';
		END IF;

	ELSE
		RAISE EXCEPTION 'olivares: communication validator attached to unknown table %', TG_TABLE_NAME
			USING ERRCODE = '23514';
	END CASE;
	RETURN NEW;
END;
$fn$;
