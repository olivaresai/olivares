-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_message_guard_ins
BEFORE INSERT ON sessions_message
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
	SELECT RAISE(ABORT, 'olivares: invalid Message envelope or protected payload')
	WHERE NEW.version <> 1 OR NEW.updated_at < NEW.created_at
		OR NEW.kind NOT IN ('notice','announcement','request','decision_request','handoff_offer','system')
		OR NEW.state <> 'draft' OR NEW.last_event_seq <> 0
		OR NEW.sender_kind NOT IN ('user','agent','session','system')
		OR length(CAST(NEW.sender_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR (NEW.sender_kind = 'session' AND
			(length(NEW.sender_ref) <> 40 OR substr(NEW.sender_ref,1,4) <> 'osn_'))
		OR (NEW.sender_kind IN ('user','agent') AND length(NEW.sender_ref) <> 36)
		OR (NEW.sender_kind = 'session' AND
			(NEW.sender_ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(NEW.sender_ref,5),'-','')) <> 32
				OR replace(substr(NEW.sender_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.sender_kind IN ('user','agent') AND
			(NEW.sender_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.sender_ref,'-','')) <> 32
				OR replace(NEW.sender_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.sender_kind = 'system' AND
			(trim(NEW.sender_ref) <> NEW.sender_ref OR instr(NEW.sender_ref,char(10)) > 0
				OR instr(NEW.sender_ref,char(13)) > 0 OR instr(NEW.sender_ref,char(0)) > 0))
		OR NEW.urgency NOT IN ('normal','high','critical')
		OR NEW.ack_policy NOT IN ('none','each_required','quorum')
		OR NEW.available_at < NEW.created_at OR NEW.automation_depth < 0 OR NEW.last_event_seq < 0
		OR NOT COALESCE((NEW.payload_encoding IN ('plain_json','sealed_v1')
			AND NEW.payload_schema = 'communication.message.v1' AND length(NEW.payload_digest) = 32
			AND NEW.payload_protection_generation >= 1
			AND ((NEW.payload_encoding = 'plain_json'
				AND NEW.payload_plain_json IS NOT NULL AND json_valid(NEW.payload_plain_json)
				AND json_type(NEW.payload_plain_json) = 'object'
				AND length(CAST(NEW.payload_plain_json AS BLOB)) <= 65536
				AND NEW.payload_sealed_json IS NULL
				AND NEW.payload_seal_key_version IS NULL AND NEW.payload_digest_key_version IS NULL)
			OR (NEW.payload_encoding = 'sealed_v1'
				AND NEW.payload_plain_json IS NULL AND NEW.payload_sealed_json IS NOT NULL
				AND json_valid(NEW.payload_sealed_json) AND json_type(NEW.payload_sealed_json) = 'object'
				AND length(CAST(NEW.payload_sealed_json AS BLOB)) <= 196608
				AND length(CAST(NEW.payload_seal_key_version AS BLOB)) BETWEEN 1 AND 512
				AND length(CAST(NEW.payload_digest_key_version AS BLOB)) BETWEEN 1 AND 512
				AND trim(NEW.payload_seal_key_version) = NEW.payload_seal_key_version
				AND trim(NEW.payload_digest_key_version) = NEW.payload_digest_key_version
				AND instr(NEW.payload_seal_key_version,char(0)) = 0
				AND instr(NEW.payload_seal_key_version,char(10)) = 0
				AND instr(NEW.payload_seal_key_version,char(13)) = 0
				AND instr(NEW.payload_digest_key_version,char(0)) = 0
				AND instr(NEW.payload_digest_key_version,char(10)) = 0
				AND instr(NEW.payload_digest_key_version,char(13)) = 0
				AND json_type(NEW.payload_sealed_json,'$.ciphertext') = 'text'
				AND length(json_extract(NEW.payload_sealed_json,'$.ciphertext')) > 0
				AND json_type(NEW.payload_sealed_json,'$.key_version') = 'text'
				AND json_extract(NEW.payload_sealed_json,'$.key_version') = NEW.payload_seal_key_version))), 0)
		OR (NEW.labels_json IS NULL) IS NOT (NEW.labels_hash IS NULL)
		OR (NEW.labels_json IS NOT NULL AND
			(NOT json_valid(NEW.labels_json) OR json_type(NEW.labels_json) <> 'object'
				OR length(CAST(NEW.labels_json AS BLOB)) > 8192 OR length(NEW.labels_hash) <> 32
				OR (SELECT count(*) FROM json_each(CASE
					WHEN json_valid(NEW.labels_json) THEN NEW.labels_json ELSE '{}' END))
					NOT BETWEEN 1 AND 32
				OR EXISTS (SELECT 1 FROM json_each(CASE
					WHEN json_valid(NEW.labels_json) THEN NEW.labels_json ELSE '{}' END) AS label
					WHERE length(CAST(label.key AS TEXT)) NOT BETWEEN 1 AND 128
						OR CAST(label.key AS TEXT) GLOB '*[^a-z0-9._-]*'
						OR label.type <> 'text'
						OR length(CAST(label.value AS TEXT)) NOT BETWEEN 1 AND 128
						OR CAST(label.value AS TEXT) GLOB '*[^a-z0-9._-]*')))
		OR (NEW.reply_to_id IS NULL AND NEW.thread_id <> NEW.id)
		OR NEW.reply_to_id IS NEW.id OR NEW.supersedes_id IS NEW.id
		OR (NEW.kind IN ('request','decision_request','handoff_offer') AND NEW.work_item_id IS NULL)
		OR (NEW.work_item_id IS NOT NULL AND NEW.last_event_seq <> 0)
		OR (NEW.work_item_id IS NULL AND NEW.state = 'draft' AND NEW.last_event_seq <> 0)
		OR (NEW.work_item_id IS NULL AND NEW.state = 'published' AND NEW.last_event_seq < 1)
		OR (NEW.work_item_id IS NULL AND NEW.state IN ('retracted','expired') AND NEW.last_event_seq < 2)
		OR (NEW.work_item_id IS NULL AND NEW.state = 'discarded' AND NEW.last_event_seq < 1)
		OR (NEW.ack_due_at IS NOT NULL AND NEW.ack_due_at < NEW.available_at)
		OR (NEW.expires_at IS NOT NULL AND NEW.expires_at <= NEW.available_at)
		OR (NEW.ack_due_at IS NOT NULL AND NEW.expires_at IS NOT NULL AND NEW.ack_due_at > NEW.expires_at)
		OR (NEW.ack_policy = 'none' AND (NEW.ack_quorum <> 0 OR NEW.ack_due_at IS NOT NULL))
		OR (NEW.ack_policy = 'each_required' AND (NEW.ack_quorum <> 0 OR NEW.ack_due_at IS NULL))
		OR (NEW.ack_policy = 'quorum' AND (NEW.ack_quorum < 1 OR NEW.ack_due_at IS NULL))
		OR (NEW.terminal_reason_encoding IS NULL AND
			(NEW.terminal_reason_plain_json IS NOT NULL OR NEW.terminal_reason_sealed_json IS NOT NULL
				OR NEW.terminal_reason_schema IS NOT NULL OR NEW.terminal_reason_digest IS NOT NULL
				OR NEW.terminal_reason_seal_key_version IS NOT NULL
				OR NEW.terminal_reason_digest_key_version IS NOT NULL
				OR NEW.terminal_reason_protection_generation IS NOT NULL))
		OR (NEW.terminal_reason_encoding IS NOT NULL AND NOT COALESCE((
			NEW.terminal_reason_encoding IN ('plain_json','sealed_v1')
			AND NEW.terminal_reason_schema = 'communication.message-terminal-reason.v1'
			AND length(NEW.terminal_reason_digest) = 32
			AND NEW.terminal_reason_protection_generation >= 1
			AND ((NEW.terminal_reason_encoding = 'plain_json'
				AND NEW.terminal_reason_plain_json IS NOT NULL
				AND json_valid(NEW.terminal_reason_plain_json)
				AND json_type(NEW.terminal_reason_plain_json) = 'object'
				AND length(CAST(NEW.terminal_reason_plain_json AS BLOB)) <= 65536
				AND NEW.terminal_reason_sealed_json IS NULL
				AND NEW.terminal_reason_seal_key_version IS NULL
				AND NEW.terminal_reason_digest_key_version IS NULL)
			OR (NEW.terminal_reason_encoding = 'sealed_v1'
				AND NEW.terminal_reason_plain_json IS NULL
				AND NEW.terminal_reason_sealed_json IS NOT NULL
				AND json_valid(NEW.terminal_reason_sealed_json)
				AND json_type(NEW.terminal_reason_sealed_json) = 'object'
				AND length(CAST(NEW.terminal_reason_sealed_json AS BLOB)) <= 196608
				AND length(CAST(NEW.terminal_reason_seal_key_version AS BLOB)) BETWEEN 1 AND 512
				AND length(CAST(NEW.terminal_reason_digest_key_version AS BLOB)) BETWEEN 1 AND 512
				AND trim(NEW.terminal_reason_seal_key_version) = NEW.terminal_reason_seal_key_version
				AND trim(NEW.terminal_reason_digest_key_version) = NEW.terminal_reason_digest_key_version
				AND instr(NEW.terminal_reason_seal_key_version,char(0)) = 0
				AND instr(NEW.terminal_reason_seal_key_version,char(10)) = 0
				AND instr(NEW.terminal_reason_seal_key_version,char(13)) = 0
				AND instr(NEW.terminal_reason_digest_key_version,char(0)) = 0
				AND instr(NEW.terminal_reason_digest_key_version,char(10)) = 0
				AND instr(NEW.terminal_reason_digest_key_version,char(13)) = 0
				AND json_type(NEW.terminal_reason_sealed_json,'$.ciphertext') = 'text'
				AND length(json_extract(NEW.terminal_reason_sealed_json,'$.ciphertext')) > 0
				AND json_type(NEW.terminal_reason_sealed_json,'$.key_version') = 'text'
				AND json_extract(NEW.terminal_reason_sealed_json,'$.key_version') =
					NEW.terminal_reason_seal_key_version))), 0))
		OR (NEW.state IN ('retracted','expired','discarded') AND
			(NEW.terminal_at IS NULL OR NEW.terminal_at < NEW.created_at OR NEW.terminal_at > NEW.updated_at
				OR NEW.terminal_code IS NULL OR length(NEW.terminal_code) NOT BETWEEN 1 AND 128
				OR NEW.terminal_code GLOB '*[^a-z0-9._-]*'
				OR NOT ((NEW.terminal_reason_encoding IS NULL AND NEW.terminal_reason_plain_json IS NULL AND NEW.terminal_reason_sealed_json IS NULL
			AND NEW.terminal_reason_schema IS NULL AND NEW.terminal_reason_digest IS NULL
			AND NEW.terminal_reason_seal_key_version IS NULL AND NEW.terminal_reason_digest_key_version IS NULL
			AND NEW.terminal_reason_protection_generation IS NULL) OR (NEW.terminal_reason_encoding IN ('plain_json','sealed_v1')
			AND NEW.terminal_reason_schema = 'communication.message-terminal-reason.v1' AND length(NEW.terminal_reason_digest) = 32
			AND NEW.terminal_reason_protection_generation >= 1
			AND ((NEW.terminal_reason_encoding = 'plain_json'
				AND NEW.terminal_reason_plain_json IS NOT NULL AND json_valid(NEW.terminal_reason_plain_json)
				AND json_type(NEW.terminal_reason_plain_json) = 'object'
				AND length(CAST(NEW.terminal_reason_plain_json AS BLOB)) <= 65536
				AND NEW.terminal_reason_sealed_json IS NULL
				AND NEW.terminal_reason_seal_key_version IS NULL AND NEW.terminal_reason_digest_key_version IS NULL)
			OR (NEW.terminal_reason_encoding = 'sealed_v1'
				AND NEW.terminal_reason_plain_json IS NULL AND NEW.terminal_reason_sealed_json IS NOT NULL
				AND json_valid(NEW.terminal_reason_sealed_json) AND json_type(NEW.terminal_reason_sealed_json) = 'object'
				AND length(CAST(NEW.terminal_reason_sealed_json AS BLOB)) <= 196608
				AND length(CAST(NEW.terminal_reason_seal_key_version AS BLOB)) BETWEEN 1 AND 512
				AND length(CAST(NEW.terminal_reason_digest_key_version AS BLOB)) BETWEEN 1 AND 512
				AND json_type(NEW.terminal_reason_sealed_json,'$.ciphertext') = 'text'
				AND length(json_extract(NEW.terminal_reason_sealed_json,'$.ciphertext')) > 0
				AND json_extract(NEW.terminal_reason_sealed_json,'$.key_version') = NEW.terminal_reason_seal_key_version))))))
		OR (NEW.state NOT IN ('retracted','expired','discarded') AND
			(NEW.terminal_at IS NOT NULL OR NEW.terminal_code IS NOT NULL
				OR NEW.terminal_reason_encoding IS NOT NULL))
		OR (NEW.state IN ('published','retracted','expired') AND
			(NEW.published_at IS NULL OR NEW.published_at < NEW.created_at
				OR NEW.published_at > NEW.updated_at OR NEW.audience_hash IS NULL
				OR length(NEW.audience_hash) <> 32))
		OR (NEW.state IN ('draft','discarded') AND
			(NEW.published_at IS NOT NULL OR NEW.audience_hash IS NOT NULL))
		OR (NEW.published_at IS NOT NULL AND NEW.terminal_at IS NOT NULL
			AND NEW.terminal_at < NEW.published_at)
		OR (NEW.terminal_reason_encoding IS NOT NULL AND
			(NEW.terminal_reason_encoding <> NEW.payload_encoding
				OR NEW.terminal_reason_protection_generation <>
					NEW.payload_protection_generation))
		OR (NEW.state = 'expired' AND (NEW.expires_at IS NULL OR NEW.terminal_at < NEW.expires_at));
	SELECT RAISE(ABORT, 'olivares: Message channel/protection crosses lineage')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_channel c
		WHERE c.id = NEW.channel_id AND c.tenant_id = NEW.tenant_id
			AND c.workspace_id = NEW.workspace_id
			AND c.protection_generation = NEW.payload_protection_generation
			AND ((c.content_protection = 'storage' AND NEW.payload_encoding = 'plain_json')
				OR (c.content_protection = 'application_sealed' AND NEW.payload_encoding = 'sealed_v1')));
	SELECT RAISE(ABORT, 'olivares: Message WorkItem crosses tenant/workspace')
	WHERE NEW.work_item_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM sessions_work_item w
		WHERE w.id = NEW.work_item_id AND w.tenant_id = NEW.tenant_id
			AND w.workspace_id = NEW.workspace_id);
	SELECT RAISE(ABORT, 'olivares: Message reply crosses thread or aggregate lineage')
	WHERE NEW.reply_to_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM sessions_message p
		WHERE p.id = NEW.reply_to_id AND p.tenant_id = NEW.tenant_id
			AND p.workspace_id = NEW.workspace_id AND p.channel_id = NEW.channel_id
			AND p.thread_id = NEW.thread_id AND p.work_item_id IS NEW.work_item_id);
	SELECT RAISE(ABORT, 'olivares: Message supersedes lineage is invalid')
	WHERE NEW.supersedes_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM sessions_message p
		WHERE p.id = NEW.supersedes_id AND p.tenant_id = NEW.tenant_id
			AND p.workspace_id = NEW.workspace_id AND p.channel_id = NEW.channel_id
			AND p.work_item_id IS NEW.work_item_id
			AND p.state IN ('retracted','expired','discarded'));
	SELECT RAISE(ABORT, 'olivares: Message origin Event crosses tenant/workspace')
	WHERE NEW.origin_event_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM sessions_work_event e
		WHERE e.event_id = NEW.origin_event_id AND e.tenant_id = NEW.tenant_id
			AND e.workspace_id = NEW.workspace_id);
	SELECT RAISE(ABORT, 'olivares: published Message Ack policy contradicts deliveries')
	WHERE NEW.state IN ('published','retracted','expired') AND (
		(NEW.ack_policy = 'none' AND EXISTS (SELECT 1 FROM sessions_message_delivery d
			WHERE d.tenant_id = NEW.tenant_id AND d.message_id = NEW.id AND d.required))
		OR (NEW.ack_policy = 'each_required' AND NOT EXISTS (
			SELECT 1 FROM sessions_message_delivery d
			WHERE d.tenant_id = NEW.tenant_id AND d.message_id = NEW.id AND d.required))
		OR (NEW.ack_policy = 'quorum' AND NEW.ack_quorum >
			(SELECT count(*) FROM sessions_message_delivery d
				WHERE d.tenant_id = NEW.tenant_id AND d.message_id = NEW.id AND d.required)));
END;
