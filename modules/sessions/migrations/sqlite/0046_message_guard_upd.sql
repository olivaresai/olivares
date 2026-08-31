-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_message_guard_upd
BEFORE UPDATE ON sessions_message
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: Message immutable lineage or transition changed')
	WHERE NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id
		OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at
		OR NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at
		OR NEW.channel_id IS NOT OLD.channel_id OR NEW.work_item_id IS NOT OLD.work_item_id
		OR NEW.thread_id IS NOT OLD.thread_id OR NEW.kind IS NOT OLD.kind
		OR NEW.sender_kind IS NOT OLD.sender_kind OR NEW.sender_ref IS NOT OLD.sender_ref
		OR NEW.payload_encoding IS NOT OLD.payload_encoding
		OR NEW.payload_plain_json IS NOT OLD.payload_plain_json
		OR NEW.payload_sealed_json IS NOT OLD.payload_sealed_json
		OR NEW.payload_schema IS NOT OLD.payload_schema OR NEW.payload_digest IS NOT OLD.payload_digest
		OR NEW.payload_seal_key_version IS NOT OLD.payload_seal_key_version
		OR NEW.payload_digest_key_version IS NOT OLD.payload_digest_key_version
		OR NEW.payload_protection_generation IS NOT OLD.payload_protection_generation
		OR NEW.labels_json IS NOT OLD.labels_json OR NEW.labels_hash IS NOT OLD.labels_hash
		OR NEW.urgency IS NOT OLD.urgency OR NEW.ack_policy IS NOT OLD.ack_policy
		OR NEW.ack_quorum IS NOT OLD.ack_quorum OR NEW.available_at IS NOT OLD.available_at
		OR NEW.ack_due_at IS NOT OLD.ack_due_at OR NEW.expires_at IS NOT OLD.expires_at
		OR NEW.reply_to_id IS NOT OLD.reply_to_id OR NEW.supersedes_id IS NOT OLD.supersedes_id
		OR NEW.origin_event_id IS NOT OLD.origin_event_id
		OR NEW.automation_depth IS NOT OLD.automation_depth
		OR (OLD.state <> NEW.state AND NOT (
			(OLD.state = 'draft' AND NEW.state IN ('published','discarded')) OR
			(OLD.state = 'published' AND NEW.state IN ('retracted','expired'))))
		OR (OLD.state <> 'draft' AND
			(NEW.published_at IS NOT OLD.published_at
				OR NEW.audience_hash IS NOT OLD.audience_hash))
		OR (OLD.state IS NEW.state AND NOT (
			OLD.state IN ('published','retracted','expired') AND NEW.work_item_id IS NULL
			AND NEW.last_event_seq = OLD.last_event_seq + 1
			AND NEW.terminal_at IS OLD.terminal_at
			AND NEW.terminal_code IS OLD.terminal_code
			AND NEW.terminal_reason_encoding IS OLD.terminal_reason_encoding
			AND NEW.terminal_reason_plain_json IS OLD.terminal_reason_plain_json
			AND NEW.terminal_reason_sealed_json IS OLD.terminal_reason_sealed_json
			AND NEW.terminal_reason_schema IS OLD.terminal_reason_schema
			AND NEW.terminal_reason_digest IS OLD.terminal_reason_digest
			AND NEW.terminal_reason_seal_key_version IS OLD.terminal_reason_seal_key_version
			AND NEW.terminal_reason_digest_key_version IS OLD.terminal_reason_digest_key_version
			AND NEW.terminal_reason_protection_generation IS
				OLD.terminal_reason_protection_generation))
		OR (OLD.state IN ('retracted','expired','discarded') AND NEW.state <> OLD.state)
		OR (NEW.work_item_id IS NOT NULL AND NEW.last_event_seq <> 0)
		OR (NEW.work_item_id IS NULL AND NEW.last_event_seq <> OLD.last_event_seq + 1);
	SELECT RAISE(ABORT, 'olivares: invalid Message transition evidence')
	WHERE NEW.state NOT IN ('draft','published','retracted','expired','discarded')
		OR (NEW.state IN ('retracted','expired','discarded') AND
			(NEW.terminal_at IS NULL OR NEW.terminal_at < NEW.created_at
				OR NEW.terminal_at > NEW.updated_at OR NEW.terminal_code IS NULL
				OR length(NEW.terminal_code) NOT BETWEEN 1 AND 128
				OR NEW.terminal_code GLOB '*[^a-z0-9._-]*'
				OR NOT ((NEW.terminal_reason_encoding IS NULL
					AND NEW.terminal_reason_plain_json IS NULL
					AND NEW.terminal_reason_sealed_json IS NULL
					AND NEW.terminal_reason_schema IS NULL
					AND NEW.terminal_reason_digest IS NULL
					AND NEW.terminal_reason_seal_key_version IS NULL
					AND NEW.terminal_reason_digest_key_version IS NULL
					AND NEW.terminal_reason_protection_generation IS NULL)
					OR COALESCE((NEW.terminal_reason_encoding IN ('plain_json','sealed_v1')
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
								NEW.terminal_reason_seal_key_version))), 0))))
		OR (NEW.state NOT IN ('retracted','expired','discarded') AND
			(NEW.terminal_at IS NOT NULL OR NEW.terminal_code IS NOT NULL
				OR NEW.terminal_reason_encoding IS NOT NULL
				OR NEW.terminal_reason_plain_json IS NOT NULL
				OR NEW.terminal_reason_sealed_json IS NOT NULL
				OR NEW.terminal_reason_schema IS NOT NULL OR NEW.terminal_reason_digest IS NOT NULL
				OR NEW.terminal_reason_seal_key_version IS NOT NULL
				OR NEW.terminal_reason_digest_key_version IS NOT NULL
				OR NEW.terminal_reason_protection_generation IS NOT NULL))
		OR (NEW.state IN ('published','retracted','expired') AND
			(NEW.published_at IS NULL OR NEW.published_at < NEW.created_at
				OR NEW.published_at > NEW.updated_at OR NEW.audience_hash IS NULL
				OR length(NEW.audience_hash) <> 32))
		OR (NEW.terminal_reason_encoding IS NOT NULL AND
			(NEW.terminal_reason_encoding <> NEW.payload_encoding
				OR NEW.terminal_reason_protection_generation <>
					NEW.payload_protection_generation))
		OR (NEW.published_at IS NOT NULL AND NEW.terminal_at IS NOT NULL
			AND NEW.terminal_at < NEW.published_at)
		OR (NEW.state = 'expired' AND (NEW.expires_at IS NULL OR NEW.terminal_at < NEW.expires_at));
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
	SELECT RAISE(ABORT, 'olivares: Message publish crosses current Channel protection')
	WHERE OLD.state = 'draft' AND NEW.state = 'published' AND NOT EXISTS (
		SELECT 1 FROM sessions_channel c
		WHERE c.id = NEW.channel_id AND c.tenant_id = NEW.tenant_id
			AND c.workspace_id = NEW.workspace_id
			AND c.protection_generation = NEW.payload_protection_generation
			AND ((c.content_protection = 'storage' AND NEW.payload_encoding = 'plain_json')
				OR (c.content_protection = 'application_sealed'
					AND NEW.payload_encoding = 'sealed_v1')));
	SELECT RAISE(ABORT, 'olivares: Message sender reference is non-canonical')
	WHERE (NEW.sender_kind = 'session' AND
			(NEW.sender_ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(NEW.sender_ref,5),'-','')) <> 32
				OR replace(substr(NEW.sender_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.sender_kind IN ('user','agent') AND
			(NEW.sender_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.sender_ref,'-','')) <> 32
				OR replace(NEW.sender_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.sender_kind = 'system' AND
			(trim(NEW.sender_ref) <> NEW.sender_ref OR instr(NEW.sender_ref,char(10)) > 0
				OR instr(NEW.sender_ref,char(13)) > 0 OR instr(NEW.sender_ref,char(0)) > 0));
END;
