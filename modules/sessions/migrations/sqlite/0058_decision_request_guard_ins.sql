-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_decision_request_guard_ins
BEFORE INSERT ON sessions_decision_request
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
	SELECT RAISE(ABORT, 'olivares: invalid DecisionRequest envelope')
	WHERE NEW.version < 1 OR NEW.updated_at < NEW.created_at
		OR length(NEW.decision_key) NOT BETWEEN 1 AND 128
		OR NEW.decision_key GLOB '*[^a-z0-9._-]*'
		OR NEW.requester_kind NOT IN ('user','agent','session','system')
		OR length(CAST(NEW.requester_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR (NEW.requester_kind = 'session' AND
			(length(NEW.requester_ref) <> 40 OR substr(NEW.requester_ref,1,4) <> 'osn_'))
		OR (NEW.requester_kind IN ('user','agent') AND length(NEW.requester_ref) <> 36)
		OR NEW.owner_kind NOT IN ('user','user_group','agent','agent_group','session')
		OR (NEW.owner_kind = 'session' AND
			(length(NEW.owner_ref) <> 40 OR substr(NEW.owner_ref,1,4) <> 'osn_'))
		OR (NEW.owner_kind <> 'session' AND length(NEW.owner_ref) <> 36)
		OR (NEW.requester_kind = 'session' AND
			(NEW.requester_ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(NEW.requester_ref,5),'-','')) <> 32
				OR replace(substr(NEW.requester_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.requester_kind IN ('user','agent') AND
			(NEW.requester_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.requester_ref,'-','')) <> 32
				OR replace(NEW.requester_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.requester_kind = 'system' AND
			(trim(NEW.requester_ref) <> NEW.requester_ref
				OR instr(NEW.requester_ref,char(10)) > 0 OR instr(NEW.requester_ref,char(13)) > 0
				OR instr(NEW.requester_ref,char(0)) > 0))
		OR (NEW.owner_kind = 'session' AND
			(NEW.owner_ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(NEW.owner_ref,5),'-','')) <> 32
				OR replace(substr(NEW.owner_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.owner_kind IN ('user','user_group','agent','agent_group') AND
			(NEW.owner_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.owner_ref,'-','')) <> 32
				OR replace(NEW.owner_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR NEW.state NOT IN ('pending','accepted','blocked','resolved','rejected','canceled','expired')
		OR NOT COALESCE((NEW.request_encoding IN ('plain_json','sealed_v1')
			AND NEW.request_schema = 'communication.decision-request.v1' AND length(NEW.request_digest) = 32
			AND NEW.request_protection_generation >= 1
			AND ((NEW.request_encoding = 'plain_json'
				AND NEW.request_plain_json IS NOT NULL AND json_valid(NEW.request_plain_json)
				AND json_type(NEW.request_plain_json) = 'object'
				AND length(CAST(NEW.request_plain_json AS BLOB)) <= 65536
				AND NEW.request_sealed_json IS NULL
				AND NEW.request_seal_key_version IS NULL AND NEW.request_digest_key_version IS NULL)
			OR (NEW.request_encoding = 'sealed_v1'
				AND NEW.request_plain_json IS NULL AND NEW.request_sealed_json IS NOT NULL
				AND json_valid(NEW.request_sealed_json) AND json_type(NEW.request_sealed_json) = 'object'
				AND length(CAST(NEW.request_sealed_json AS BLOB)) <= 196608
				AND length(CAST(NEW.request_seal_key_version AS BLOB)) BETWEEN 1 AND 512
				AND length(CAST(NEW.request_digest_key_version AS BLOB)) BETWEEN 1 AND 512
				AND trim(NEW.request_seal_key_version) = NEW.request_seal_key_version
				AND trim(NEW.request_digest_key_version) = NEW.request_digest_key_version
				AND instr(NEW.request_seal_key_version,char(0)) = 0
				AND instr(NEW.request_seal_key_version,char(10)) = 0
				AND instr(NEW.request_seal_key_version,char(13)) = 0
				AND instr(NEW.request_digest_key_version,char(0)) = 0
				AND instr(NEW.request_digest_key_version,char(10)) = 0
				AND instr(NEW.request_digest_key_version,char(13)) = 0
				AND json_type(NEW.request_sealed_json,'$.ciphertext') = 'text'
				AND length(json_extract(NEW.request_sealed_json,'$.ciphertext')) > 0
				AND json_type(NEW.request_sealed_json,'$.key_version') = 'text'
				AND json_extract(NEW.request_sealed_json,'$.key_version') = NEW.request_seal_key_version))), 0)
		OR length(NEW.authority_requirement) NOT BETWEEN 1 AND 256
		OR NEW.authority_requirement GLOB '*[^a-z0-9._-]*'
		OR NEW.due_at <= NEW.created_at OR NEW.last_response_seq < 0
		OR NEW.version <> NEW.last_response_seq + 1
		OR (NEW.accepted_delivery_id IS NULL) IS NOT (NEW.accepted_at IS NULL)
		OR (NEW.accepted_at IS NOT NULL AND
			(NEW.accepted_at < NEW.created_at OR NEW.accepted_at > NEW.updated_at));
	SELECT RAISE(ABORT, 'olivares: DecisionRequest state evidence is inconsistent')
	WHERE (NEW.state = 'pending' AND (NEW.last_response_seq <> 0
			OR NEW.accepted_delivery_id IS NOT NULL OR NEW.blocked_code IS NOT NULL
			OR NEW.terminal_code IS NOT NULL OR NEW.resolved_decision_id IS NOT NULL))
		OR (NEW.state = 'accepted' AND (NEW.last_response_seq < 1 OR NEW.last_response_seq % 2 <> 1
			OR NEW.accepted_delivery_id IS NULL OR NEW.blocked_code IS NOT NULL
			OR NEW.terminal_code IS NOT NULL OR NEW.resolved_decision_id IS NOT NULL))
		OR (NEW.state = 'blocked' AND (NEW.last_response_seq < 2 OR NEW.last_response_seq % 2 <> 0
			OR NEW.accepted_delivery_id IS NULL OR NEW.blocked_code IS NULL
			OR length(NEW.blocked_code) NOT BETWEEN 1 AND 128
			OR NEW.blocked_code GLOB '*[^a-z0-9._-]*'
			OR NEW.terminal_code IS NOT NULL OR NEW.resolved_decision_id IS NOT NULL))
		OR (NEW.state = 'resolved' AND (NEW.last_response_seq < 1 OR NEW.blocked_code IS NOT NULL
			OR NEW.terminal_code IS NULL OR length(NEW.terminal_code) NOT BETWEEN 1 AND 128
			OR NEW.terminal_code GLOB '*[^a-z0-9._-]*' OR NEW.resolved_decision_id IS NULL))
		OR (NEW.state IN ('rejected','canceled','expired') AND
			(NEW.last_response_seq < 1 OR NEW.blocked_code IS NOT NULL
				OR NEW.terminal_code IS NULL OR length(NEW.terminal_code) NOT BETWEEN 1 AND 128
				OR NEW.terminal_code GLOB '*[^a-z0-9._-]*'
				OR NEW.resolved_decision_id IS NOT NULL))
		OR (NEW.state = 'expired' AND NEW.updated_at < NEW.due_at);
	SELECT RAISE(ABORT, 'olivares: DecisionRequest message/work lineage is invalid')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_message m
		WHERE m.id = NEW.message_id AND m.tenant_id = NEW.tenant_id
			AND m.workspace_id = NEW.workspace_id AND m.work_item_id = NEW.work_item_id
			AND m.kind = 'decision_request'
			AND m.payload_encoding = NEW.request_encoding
			AND m.payload_protection_generation = NEW.request_protection_generation
			AND (m.expires_at IS NULL OR m.expires_at >= NEW.due_at))
		OR NOT EXISTS (SELECT 1 FROM sessions_work_item w
			WHERE w.id = NEW.work_item_id AND w.tenant_id = NEW.tenant_id
				AND w.workspace_id = NEW.workspace_id);
	SELECT RAISE(ABORT, 'olivares: DecisionRequest accepted Delivery crosses owner/message')
	WHERE NEW.accepted_delivery_id IS NOT NULL AND NOT EXISTS (
		SELECT 1 FROM sessions_message_delivery d
		WHERE d.id = NEW.accepted_delivery_id AND d.tenant_id = NEW.tenant_id
			AND d.workspace_id = NEW.workspace_id AND d.message_id = NEW.message_id
			AND ((NEW.owner_kind IN ('user','agent','session')
					AND d.recipient_kind = NEW.owner_kind AND d.recipient_ref = NEW.owner_ref)
				OR (NEW.owner_kind = 'user_group' AND d.recipient_kind = 'user')
				OR (NEW.owner_kind = 'agent_group' AND d.recipient_kind = 'agent')));
	SELECT RAISE(ABORT, 'olivares: DecisionRequest resolved WorkDecision crosses WorkItem')
	WHERE NEW.resolved_decision_id IS NOT NULL AND NOT EXISTS (
		SELECT 1 FROM sessions_work_decision d
		WHERE d.id = NEW.resolved_decision_id AND d.tenant_id = NEW.tenant_id
			AND d.workspace_id = NEW.workspace_id AND d.work_item_id = NEW.work_item_id);
	SELECT RAISE(ABORT, 'olivares: DecisionRequest has another active request for key')
	WHERE NEW.state NOT IN ('resolved','rejected','canceled','expired') AND EXISTS (
		SELECT 1 FROM sessions_decision_request p
		WHERE p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
			AND p.work_item_id = NEW.work_item_id AND p.decision_key = NEW.decision_key
			AND p.state NOT IN ('resolved','rejected','canceled','expired') AND p.id <> NEW.id);
END;
