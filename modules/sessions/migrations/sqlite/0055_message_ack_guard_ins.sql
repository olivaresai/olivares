-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_message_ack_guard_ins
BEFORE INSERT ON sessions_message_ack
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
	SELECT RAISE(ABORT, 'olivares: invalid MessageAck shape')
	WHERE NEW.version <> 1 OR NEW.ack_kind <> 'received'
		OR NEW.actor_kind NOT IN ('user','agent','session')
		OR (NEW.actor_kind = 'session' AND
			(length(NEW.actor_ref) <> 40 OR substr(NEW.actor_ref,1,4) <> 'osn_'))
		OR (NEW.actor_kind <> 'session' AND length(NEW.actor_ref) <> 36)
		OR (NEW.on_behalf_of_kind IS NULL) IS NOT (NEW.on_behalf_of_ref IS NULL)
		OR (NEW.on_behalf_of_kind IS NOT NULL AND NEW.on_behalf_of_kind NOT IN ('user','agent','session'))
		OR (NEW.on_behalf_of_kind = 'session' AND
			(length(NEW.on_behalf_of_ref) <> 40 OR substr(NEW.on_behalf_of_ref,1,4) <> 'osn_'))
		OR (NEW.on_behalf_of_kind IN ('user','agent') AND length(NEW.on_behalf_of_ref) <> 36)
		OR (NEW.actor_kind = 'session' AND
			(NEW.actor_ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(NEW.actor_ref,5),'-','')) <> 32
				OR replace(substr(NEW.actor_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.actor_kind IN ('user','agent') AND
			(NEW.actor_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.actor_ref,'-','')) <> 32
				OR replace(NEW.actor_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.on_behalf_of_kind = 'session' AND
			(NEW.on_behalf_of_ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(NEW.on_behalf_of_ref,5),'-','')) <> 32
				OR replace(substr(NEW.on_behalf_of_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.on_behalf_of_kind IN ('user','agent') AND
			(NEW.on_behalf_of_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.on_behalf_of_ref,'-','')) <> 32
				OR replace(NEW.on_behalf_of_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.on_behalf_of_kind IS NOT NULL AND NEW.note_encoding IS NULL)
		OR (NEW.note_encoding IS NULL AND
			(NEW.note_plain_json IS NOT NULL OR NEW.note_sealed_json IS NOT NULL
				OR NEW.note_schema IS NOT NULL OR NEW.note_digest IS NOT NULL
				OR NEW.note_seal_key_version IS NOT NULL OR NEW.note_digest_key_version IS NOT NULL
				OR NEW.note_protection_generation IS NOT NULL))
		OR (NEW.note_encoding IS NOT NULL AND NOT COALESCE((
			NEW.note_encoding IN ('plain_json','sealed_v1')
			AND NEW.note_schema = 'communication.ack-note.v1' AND length(NEW.note_digest) = 32
			AND NEW.note_protection_generation >= 1
			AND ((NEW.note_encoding = 'plain_json'
				AND NEW.note_plain_json IS NOT NULL AND json_valid(NEW.note_plain_json)
				AND json_type(NEW.note_plain_json) = 'object'
				AND length(CAST(NEW.note_plain_json AS BLOB)) <= 65536
				AND NEW.note_sealed_json IS NULL
				AND NEW.note_seal_key_version IS NULL AND NEW.note_digest_key_version IS NULL)
			OR (NEW.note_encoding = 'sealed_v1'
				AND NEW.note_plain_json IS NULL AND NEW.note_sealed_json IS NOT NULL
				AND json_valid(NEW.note_sealed_json) AND json_type(NEW.note_sealed_json) = 'object'
				AND length(CAST(NEW.note_sealed_json AS BLOB)) <= 196608
				AND length(CAST(NEW.note_seal_key_version AS BLOB)) BETWEEN 1 AND 512
				AND length(CAST(NEW.note_digest_key_version AS BLOB)) BETWEEN 1 AND 512
				AND trim(NEW.note_seal_key_version) = NEW.note_seal_key_version
				AND trim(NEW.note_digest_key_version) = NEW.note_digest_key_version
				AND instr(NEW.note_seal_key_version,char(0)) = 0
				AND instr(NEW.note_seal_key_version,char(10)) = 0
				AND instr(NEW.note_seal_key_version,char(13)) = 0
				AND instr(NEW.note_digest_key_version,char(0)) = 0
				AND instr(NEW.note_digest_key_version,char(10)) = 0
				AND instr(NEW.note_digest_key_version,char(13)) = 0
				AND json_type(NEW.note_sealed_json,'$.ciphertext') = 'text'
				AND length(json_extract(NEW.note_sealed_json,'$.ciphertext')) > 0
				AND json_type(NEW.note_sealed_json,'$.key_version') = 'text'
				AND json_extract(NEW.note_sealed_json,'$.key_version') = NEW.note_seal_key_version))), 0))
		OR NOT ((NEW.note_encoding IS NULL AND NEW.note_plain_json IS NULL AND NEW.note_sealed_json IS NULL
			AND NEW.note_schema IS NULL AND NEW.note_digest IS NULL
			AND NEW.note_seal_key_version IS NULL AND NEW.note_digest_key_version IS NULL
			AND NEW.note_protection_generation IS NULL) OR (NEW.note_encoding IN ('plain_json','sealed_v1')
			AND NEW.note_schema = 'communication.ack-note.v1' AND length(NEW.note_digest) = 32
			AND NEW.note_protection_generation >= 1
			AND ((NEW.note_encoding = 'plain_json'
				AND NEW.note_plain_json IS NOT NULL AND json_valid(NEW.note_plain_json)
				AND json_type(NEW.note_plain_json) = 'object'
				AND length(CAST(NEW.note_plain_json AS BLOB)) <= 65536
				AND NEW.note_sealed_json IS NULL
				AND NEW.note_seal_key_version IS NULL AND NEW.note_digest_key_version IS NULL)
			OR (NEW.note_encoding = 'sealed_v1'
				AND NEW.note_plain_json IS NULL AND NEW.note_sealed_json IS NOT NULL
				AND json_valid(NEW.note_sealed_json) AND json_type(NEW.note_sealed_json) = 'object'
				AND length(CAST(NEW.note_sealed_json AS BLOB)) <= 196608
				AND length(CAST(NEW.note_seal_key_version AS BLOB)) BETWEEN 1 AND 512
				AND length(CAST(NEW.note_digest_key_version AS BLOB)) BETWEEN 1 AND 512
				AND json_type(NEW.note_sealed_json,'$.ciphertext') = 'text'
				AND length(json_extract(NEW.note_sealed_json,'$.ciphertext')) > 0
				AND json_extract(NEW.note_sealed_json,'$.key_version') = NEW.note_seal_key_version))))
		OR NEW.acknowledged_at <> NEW.created_at;
	SELECT RAISE(ABORT, 'olivares: MessageAck delivery crosses tenant/workspace or evidence')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_message_delivery d
		JOIN sessions_message m ON m.id = d.message_id AND m.tenant_id = d.tenant_id
			AND m.workspace_id = d.workspace_id
		WHERE d.id = NEW.delivery_id AND d.tenant_id = NEW.tenant_id
			AND d.workspace_id = NEW.workspace_id
			AND (NEW.note_encoding IS NULL OR
				(NEW.note_encoding = m.payload_encoding
					AND NEW.note_protection_generation = m.payload_protection_generation))
			AND ((NOT NEW.late AND d.state = 'acknowledged'
				AND d.ack_id = NEW.id AND d.acknowledged_at = NEW.acknowledged_at)
				OR (NEW.late AND d.state IN ('expired','retracted'))));
END;
