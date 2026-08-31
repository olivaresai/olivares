-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_handoff_guard_upd
BEFORE UPDATE ON sessions_work_handoff
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: Handoff immutable lineage or transition changed')
	WHERE NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id
		OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at
		OR NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at
		OR NEW.work_item_id IS NOT OLD.work_item_id OR NEW.message_id IS NOT OLD.message_id
		OR NEW.delivery_id IS NOT OLD.delivery_id OR NEW.from_kind IS NOT OLD.from_kind
		OR NEW.from_ref IS NOT OLD.from_ref OR NEW.from_owner_epoch IS NOT OLD.from_owner_epoch
		OR NEW.to_kind IS NOT OLD.to_kind OR NEW.to_ref IS NOT OLD.to_ref
		OR NEW.offered_lease_fence IS NOT OLD.offered_lease_fence
		OR NEW.context_event_seq IS NOT OLD.context_event_seq OR NEW.context_hash IS NOT OLD.context_hash
		OR NEW.handoff_encoding IS NOT OLD.handoff_encoding
		OR NEW.handoff_plain_json IS NOT OLD.handoff_plain_json
		OR NEW.handoff_sealed_json IS NOT OLD.handoff_sealed_json
		OR NEW.handoff_schema IS NOT OLD.handoff_schema OR NEW.handoff_digest IS NOT OLD.handoff_digest
		OR NEW.handoff_seal_key_version IS NOT OLD.handoff_seal_key_version
		OR NEW.handoff_digest_key_version IS NOT OLD.handoff_digest_key_version
		OR NEW.handoff_protection_generation IS NOT OLD.handoff_protection_generation
		OR NEW.ack_deadline IS NOT OLD.ack_deadline
		OR (OLD.state <> NEW.state AND NOT
			(OLD.state = 'offered' AND NEW.state IN ('accepted','rejected','withdrawn','expired')))
		OR (NEW.state = 'accepted' AND
			(NEW.updated_at >= OLD.ack_deadline OR NEW.accepted_at IS NOT NEW.updated_at))
		OR OLD.state <> 'offered';
	SELECT RAISE(ABORT, 'olivares: Handoff typed reference is non-canonical')
	FROM (
		SELECT NEW.from_kind AS kind, NEW.from_ref AS ref
		UNION ALL SELECT NEW.to_kind, NEW.to_ref
	) refs
	WHERE (refs.kind = 'session' AND
			(refs.ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(refs.ref,5),'-','')) <> 32
				OR replace(substr(refs.ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (refs.kind IN ('user','agent') AND
			(refs.ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(refs.ref,'-','')) <> 32
				OR replace(refs.ref,'-','') GLOB '*[^0-9a-f]*'));
	SELECT RAISE(ABORT, 'olivares: invalid Handoff terminal ProtectedPayload')
	WHERE (NEW.terminal_reason_encoding IS NULL AND
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
				NEW.terminal_reason_seal_key_version))), 0));
	SELECT RAISE(ABORT, 'olivares: Handoff terminal state evidence is inconsistent')
	WHERE NEW.state NOT IN ('offered','accepted','rejected','withdrawn','expired')
		OR (NEW.state = 'offered' AND
			(NEW.ack_id IS NOT NULL OR NEW.accepted_at IS NOT NULL OR NEW.rejected_at IS NOT NULL
				OR NEW.withdrawn_at IS NOT NULL OR NEW.expired_at IS NOT NULL
				OR NEW.terminal_code IS NOT NULL OR NEW.terminal_reason_encoding IS NOT NULL
				OR NEW.resulting_lease_fence IS NOT NULL))
		OR (NEW.state = 'accepted' AND
			(NEW.ack_id IS NULL OR NEW.accepted_at IS NULL
				OR NEW.accepted_at < NEW.created_at OR NEW.accepted_at > NEW.updated_at
				OR NEW.accepted_at >= NEW.ack_deadline
				OR NEW.rejected_at IS NOT NULL OR NEW.withdrawn_at IS NOT NULL
				OR NEW.expired_at IS NOT NULL OR NEW.terminal_code IS NOT NULL
				OR NEW.terminal_reason_encoding IS NOT NULL OR NEW.resulting_lease_fence IS NULL
				OR NEW.resulting_lease_fence <= COALESCE(NEW.offered_lease_fence,0)))
		OR (NEW.state IN ('rejected','withdrawn','expired') AND
			(NEW.ack_id IS NOT NULL OR NEW.accepted_at IS NOT NULL
				OR (NEW.state = 'rejected') IS NOT (NEW.rejected_at IS NOT NULL)
				OR (NEW.state = 'withdrawn') IS NOT (NEW.withdrawn_at IS NOT NULL)
				OR (NEW.state = 'expired') IS NOT (NEW.expired_at IS NOT NULL)
				OR NEW.terminal_code IS NULL OR length(NEW.terminal_code) NOT BETWEEN 1 AND 128
				OR NEW.terminal_code GLOB '*[^a-z0-9._-]*'
				OR NEW.terminal_reason_encoding IS NULL OR NEW.resulting_lease_fence IS NOT NULL))
		OR (NEW.rejected_at IS NOT NULL AND
			(NEW.rejected_at < NEW.created_at OR NEW.rejected_at > NEW.updated_at))
		OR (NEW.withdrawn_at IS NOT NULL AND
			(NEW.withdrawn_at < NEW.created_at OR NEW.withdrawn_at > NEW.updated_at))
		OR (NEW.expired_at IS NOT NULL AND
			(NEW.expired_at < NEW.created_at OR NEW.expired_at > NEW.updated_at))
		OR (NEW.terminal_reason_encoding IS NOT NULL AND
			(NEW.terminal_reason_encoding <> NEW.handoff_encoding
				OR NEW.terminal_reason_protection_generation <>
					NEW.handoff_protection_generation))
		OR (NEW.state = 'expired' AND NEW.expired_at < NEW.ack_deadline);
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
