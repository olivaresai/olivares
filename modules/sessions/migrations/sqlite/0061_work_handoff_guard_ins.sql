-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_handoff_guard_ins
BEFORE INSERT ON sessions_work_handoff
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
	SELECT RAISE(ABORT, 'olivares: invalid Handoff envelope')
	WHERE NEW.version < 1 OR NEW.updated_at < NEW.created_at
		OR NEW.from_kind NOT IN ('user','agent','session') OR NEW.to_kind NOT IN ('user','agent','session')
		OR (NEW.from_kind = 'session' AND
			(length(NEW.from_ref) <> 40 OR substr(NEW.from_ref,1,4) <> 'osn_'))
		OR (NEW.from_kind <> 'session' AND length(NEW.from_ref) <> 36)
		OR (NEW.to_kind = 'session' AND
			(length(NEW.to_ref) <> 40 OR substr(NEW.to_ref,1,4) <> 'osn_'))
		OR (NEW.to_kind <> 'session' AND length(NEW.to_ref) <> 36)
		OR (NEW.from_kind = 'session' AND
			(NEW.from_ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(NEW.from_ref,5),'-','')) <> 32
				OR replace(substr(NEW.from_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.from_kind IN ('user','agent') AND
			(NEW.from_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.from_ref,'-','')) <> 32
				OR replace(NEW.from_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.to_kind = 'session' AND
			(NEW.to_ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(NEW.to_ref,5),'-','')) <> 32
				OR replace(substr(NEW.to_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.to_kind IN ('user','agent') AND
			(NEW.to_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.to_ref,'-','')) <> 32
				OR replace(NEW.to_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.from_kind = NEW.to_kind AND NEW.from_ref = NEW.to_ref)
		OR NEW.from_owner_epoch < 1 OR COALESCE(NEW.offered_lease_fence,0) < 0
		OR NEW.context_event_seq < 1 OR length(NEW.context_hash) <> 32
		OR NOT COALESCE((NEW.handoff_encoding IN ('plain_json','sealed_v1')
			AND NEW.handoff_schema = 'communication.handoff.v1' AND length(NEW.handoff_digest) = 32
			AND NEW.handoff_protection_generation >= 1
			AND ((NEW.handoff_encoding = 'plain_json'
				AND NEW.handoff_plain_json IS NOT NULL AND json_valid(NEW.handoff_plain_json)
				AND json_type(NEW.handoff_plain_json) = 'object'
				AND length(CAST(NEW.handoff_plain_json AS BLOB)) <= 65536
				AND NEW.handoff_sealed_json IS NULL
				AND NEW.handoff_seal_key_version IS NULL AND NEW.handoff_digest_key_version IS NULL)
			OR (NEW.handoff_encoding = 'sealed_v1'
				AND NEW.handoff_plain_json IS NULL AND NEW.handoff_sealed_json IS NOT NULL
				AND json_valid(NEW.handoff_sealed_json) AND json_type(NEW.handoff_sealed_json) = 'object'
				AND length(CAST(NEW.handoff_sealed_json AS BLOB)) <= 196608
				AND length(CAST(NEW.handoff_seal_key_version AS BLOB)) BETWEEN 1 AND 512
				AND length(CAST(NEW.handoff_digest_key_version AS BLOB)) BETWEEN 1 AND 512
				AND trim(NEW.handoff_seal_key_version) = NEW.handoff_seal_key_version
				AND trim(NEW.handoff_digest_key_version) = NEW.handoff_digest_key_version
				AND instr(NEW.handoff_seal_key_version,char(0)) = 0
				AND instr(NEW.handoff_seal_key_version,char(10)) = 0
				AND instr(NEW.handoff_seal_key_version,char(13)) = 0
				AND instr(NEW.handoff_digest_key_version,char(0)) = 0
				AND instr(NEW.handoff_digest_key_version,char(10)) = 0
				AND instr(NEW.handoff_digest_key_version,char(13)) = 0
				AND json_type(NEW.handoff_sealed_json,'$.ciphertext') = 'text'
				AND length(json_extract(NEW.handoff_sealed_json,'$.ciphertext')) > 0
				AND json_type(NEW.handoff_sealed_json,'$.key_version') = 'text'
				AND json_extract(NEW.handoff_sealed_json,'$.key_version') = NEW.handoff_seal_key_version))), 0)
		OR NEW.state NOT IN ('offered','accepted','rejected','withdrawn','expired')
		OR NEW.ack_deadline <= NEW.created_at
		OR (NEW.terminal_reason_encoding IS NULL AND
			(NEW.terminal_reason_plain_json IS NOT NULL OR NEW.terminal_reason_sealed_json IS NOT NULL
				OR NEW.terminal_reason_schema IS NOT NULL OR NEW.terminal_reason_digest IS NOT NULL
				OR NEW.terminal_reason_seal_key_version IS NOT NULL
				OR NEW.terminal_reason_digest_key_version IS NOT NULL
				OR NEW.terminal_reason_protection_generation IS NOT NULL))
		OR (NEW.terminal_reason_encoding IS NOT NULL AND NOT COALESCE((
			NEW.terminal_reason_encoding IN ('plain_json','sealed_v1')
			AND NEW.terminal_reason_schema = 'communication.handoff-terminal-reason.v1'
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
		OR NOT ((NEW.terminal_reason_encoding IS NULL AND NEW.terminal_reason_plain_json IS NULL AND NEW.terminal_reason_sealed_json IS NULL
			AND NEW.terminal_reason_schema IS NULL AND NEW.terminal_reason_digest IS NULL
			AND NEW.terminal_reason_seal_key_version IS NULL AND NEW.terminal_reason_digest_key_version IS NULL
			AND NEW.terminal_reason_protection_generation IS NULL) OR (NEW.terminal_reason_encoding IN ('plain_json','sealed_v1')
			AND NEW.terminal_reason_schema = 'communication.handoff-terminal-reason.v1' AND length(NEW.terminal_reason_digest) = 32
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
				AND json_extract(NEW.terminal_reason_sealed_json,'$.key_version') = NEW.terminal_reason_seal_key_version))));
	SELECT RAISE(ABORT, 'olivares: Handoff terminal payload crosses protection policy')
	WHERE NEW.terminal_reason_encoding IS NOT NULL
		AND (NEW.terminal_reason_encoding <> NEW.handoff_encoding
			OR NEW.terminal_reason_protection_generation <>
				NEW.handoff_protection_generation);
	SELECT RAISE(ABORT, 'olivares: Handoff state evidence is inconsistent')
	WHERE (NEW.state = 'offered' AND
			(NEW.ack_id IS NOT NULL OR NEW.accepted_at IS NOT NULL OR NEW.rejected_at IS NOT NULL
				OR NEW.withdrawn_at IS NOT NULL OR NEW.expired_at IS NOT NULL
				OR NEW.terminal_code IS NOT NULL OR NEW.terminal_reason_encoding IS NOT NULL
				OR NEW.resulting_lease_fence IS NOT NULL))
		OR (NEW.state = 'accepted' AND
			(NEW.ack_id IS NULL OR NEW.accepted_at IS NULL
				OR NEW.accepted_at < NEW.created_at OR NEW.accepted_at > NEW.updated_at
				OR NEW.accepted_at >= NEW.ack_deadline
				OR NEW.rejected_at IS NOT NULL OR NEW.withdrawn_at IS NOT NULL OR NEW.expired_at IS NOT NULL
				OR NEW.terminal_code IS NOT NULL OR NEW.terminal_reason_encoding IS NOT NULL
				OR NEW.resulting_lease_fence IS NULL
				OR NEW.resulting_lease_fence <= COALESCE(NEW.offered_lease_fence,0)))
		OR (NEW.state IN ('rejected','withdrawn','expired') AND
			(NEW.ack_id IS NOT NULL OR NEW.accepted_at IS NOT NULL
				OR (NEW.state = 'rejected') IS NOT (NEW.rejected_at IS NOT NULL)
				OR (NEW.state = 'withdrawn') IS NOT (NEW.withdrawn_at IS NOT NULL)
				OR (NEW.state = 'expired') IS NOT (NEW.expired_at IS NOT NULL)
				OR NEW.terminal_code IS NULL OR NEW.terminal_reason_encoding IS NULL
				OR NEW.resulting_lease_fence IS NOT NULL))
		OR (NEW.rejected_at IS NOT NULL AND
			(NEW.rejected_at < NEW.created_at OR NEW.rejected_at > NEW.updated_at))
		OR (NEW.withdrawn_at IS NOT NULL AND
			(NEW.withdrawn_at < NEW.created_at OR NEW.withdrawn_at > NEW.updated_at))
		OR (NEW.expired_at IS NOT NULL AND
			(NEW.expired_at < NEW.created_at OR NEW.expired_at > NEW.updated_at))
		OR (NEW.state = 'expired' AND NEW.expired_at < NEW.ack_deadline);
	SELECT RAISE(ABORT, 'olivares: Handoff message/delivery/work lineage is invalid')
	WHERE NOT EXISTS (SELECT 1
		FROM sessions_message m
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
					AND rd.message_id = m.id AND rd.required) = 1);
	SELECT RAISE(ABORT, 'olivares: Handoff context Event crosses WorkItem lineage')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_work_event e
		WHERE e.tenant_id = NEW.tenant_id AND e.workspace_id = NEW.workspace_id
			AND e.aggregate_kind = 'sessions.work_item' AND e.aggregate_id = NEW.work_item_id
			AND e.seq = NEW.context_event_seq);
	SELECT RAISE(ABORT, 'olivares: accepted Handoff Ack crosses exact Delivery')
	WHERE NEW.state = 'accepted' AND NOT EXISTS (
		SELECT 1 FROM sessions_message_ack a WHERE a.id = NEW.ack_id
			AND a.tenant_id = NEW.tenant_id AND a.workspace_id = NEW.workspace_id
			AND a.delivery_id = NEW.delivery_id AND NOT a.late);
	SELECT RAISE(ABORT, 'olivares: WorkItem has another offered Handoff')
	WHERE NEW.state = 'offered' AND EXISTS (
		SELECT 1 FROM sessions_work_handoff p
		WHERE p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
			AND p.work_item_id = NEW.work_item_id AND p.state = 'offered' AND p.id <> NEW.id);
END;
