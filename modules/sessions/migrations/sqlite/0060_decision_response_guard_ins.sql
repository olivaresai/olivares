-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_decision_response_guard_ins
BEFORE INSERT ON sessions_decision_response
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
	SELECT RAISE(ABORT, 'olivares: invalid DecisionResponse shape')
	WHERE NEW.version <> 1 OR NEW.response_seq < 1
		OR NEW.from_state NOT IN ('pending','accepted','blocked')
		OR NEW.to_state NOT IN ('accepted','blocked','resolved','rejected','canceled','expired')
		OR NOT ((NEW.from_state IN ('pending','blocked') AND NEW.to_state = 'accepted')
			OR (NEW.from_state = 'accepted' AND NEW.to_state = 'blocked')
			OR NEW.to_state IN ('resolved','rejected','canceled','expired'))
		OR NEW.actor_kind NOT IN ('user','agent','session','system')
		OR length(CAST(NEW.actor_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR (NEW.actor_kind = 'session' AND
			(length(NEW.actor_ref) <> 40 OR substr(NEW.actor_ref,1,4) <> 'osn_'))
		OR (NEW.actor_kind IN ('user','agent') AND length(NEW.actor_ref) <> 36)
		OR (NEW.actor_kind = 'session' AND
			(NEW.actor_ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(NEW.actor_ref,5),'-','')) <> 32
				OR replace(substr(NEW.actor_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.actor_kind IN ('user','agent') AND
			(NEW.actor_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.actor_ref,'-','')) <> 32
				OR replace(NEW.actor_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.actor_kind = 'system' AND
			(trim(NEW.actor_ref) <> NEW.actor_ref OR instr(NEW.actor_ref,char(10)) > 0
				OR instr(NEW.actor_ref,char(13)) > 0 OR instr(NEW.actor_ref,char(0)) > 0))
		OR NOT COALESCE((NEW.response_encoding IN ('plain_json','sealed_v1')
			AND NEW.response_schema = 'communication.decision-response.v1' AND length(NEW.response_digest) = 32
			AND NEW.response_protection_generation >= 1
			AND ((NEW.response_encoding = 'plain_json'
				AND NEW.response_plain_json IS NOT NULL AND json_valid(NEW.response_plain_json)
				AND json_type(NEW.response_plain_json) = 'object'
				AND length(CAST(NEW.response_plain_json AS BLOB)) <= 65536
				AND NEW.response_sealed_json IS NULL
				AND NEW.response_seal_key_version IS NULL AND NEW.response_digest_key_version IS NULL)
			OR (NEW.response_encoding = 'sealed_v1'
				AND NEW.response_plain_json IS NULL AND NEW.response_sealed_json IS NOT NULL
				AND json_valid(NEW.response_sealed_json) AND json_type(NEW.response_sealed_json) = 'object'
				AND length(CAST(NEW.response_sealed_json AS BLOB)) <= 196608
				AND length(CAST(NEW.response_seal_key_version AS BLOB)) BETWEEN 1 AND 512
				AND length(CAST(NEW.response_digest_key_version AS BLOB)) BETWEEN 1 AND 512
				AND trim(NEW.response_seal_key_version) = NEW.response_seal_key_version
				AND trim(NEW.response_digest_key_version) = NEW.response_digest_key_version
				AND instr(NEW.response_seal_key_version,char(0)) = 0
				AND instr(NEW.response_seal_key_version,char(10)) = 0
				AND instr(NEW.response_seal_key_version,char(13)) = 0
				AND instr(NEW.response_digest_key_version,char(0)) = 0
				AND instr(NEW.response_digest_key_version,char(10)) = 0
				AND instr(NEW.response_digest_key_version,char(13)) = 0
				AND json_type(NEW.response_sealed_json,'$.ciphertext') = 'text'
				AND length(json_extract(NEW.response_sealed_json,'$.ciphertext')) > 0
				AND json_type(NEW.response_sealed_json,'$.key_version') = 'text'
				AND json_extract(NEW.response_sealed_json,'$.key_version') = NEW.response_seal_key_version))), 0)
		OR NEW.responded_at <> NEW.created_at
		OR (NEW.to_state = 'resolved') IS NOT (NEW.work_decision_id IS NOT NULL)
		OR (NEW.to_state <> 'blocked' AND NEW.blocker_work_item_id IS NOT NULL);
	SELECT RAISE(ABORT, 'olivares: DecisionResponse crosses current request lineage')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_decision_request r
		WHERE r.id = NEW.request_id AND r.tenant_id = NEW.tenant_id
			AND r.workspace_id = NEW.workspace_id AND r.last_response_seq = NEW.response_seq
			AND r.state = NEW.to_state AND r.updated_at = NEW.responded_at
			AND r.request_encoding = NEW.response_encoding
			AND r.request_protection_generation = NEW.response_protection_generation
			AND r.accepted_delivery_id IS NEW.accepted_delivery_id
			AND r.resolved_decision_id IS NEW.work_decision_id);
	SELECT RAISE(ABORT, 'olivares: DecisionResponse predecessor sequence is not serialized')
	WHERE (NEW.response_seq = 1 AND NEW.from_state <> 'pending')
		OR (NEW.response_seq > 1 AND NOT EXISTS (
			SELECT 1 FROM sessions_decision_response p
			WHERE p.tenant_id = NEW.tenant_id AND p.request_id = NEW.request_id
				AND p.response_seq = NEW.response_seq - 1 AND p.to_state = NEW.from_state));
	SELECT RAISE(ABORT, 'olivares: DecisionResponse blocker crosses WorkItem lineage')
	WHERE NEW.blocker_work_item_id IS NOT NULL AND NOT EXISTS (
		SELECT 1 FROM sessions_work_item w JOIN sessions_decision_request r
			ON r.id = NEW.request_id AND r.tenant_id = NEW.tenant_id
		WHERE w.id = NEW.blocker_work_item_id AND w.tenant_id = NEW.tenant_id
			AND w.workspace_id = NEW.workspace_id AND r.workspace_id = NEW.workspace_id);
	SELECT RAISE(ABORT, 'olivares: DecisionResponse WorkDecision crosses request WorkItem')
	WHERE NEW.work_decision_id IS NOT NULL AND NOT EXISTS (
		SELECT 1 FROM sessions_work_decision d JOIN sessions_decision_request r
			ON r.id = NEW.request_id AND r.tenant_id = NEW.tenant_id
		WHERE d.id = NEW.work_decision_id AND d.tenant_id = NEW.tenant_id
			AND d.workspace_id = NEW.workspace_id AND d.work_item_id = r.work_item_id);
END;
