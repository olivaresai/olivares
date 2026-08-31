-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_decision_request_guard_upd
BEFORE UPDATE ON sessions_decision_request
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: DecisionRequest immutable lineage or transition changed')
	WHERE NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id
		OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at
		OR NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at
		OR NEW.message_id IS NOT OLD.message_id OR NEW.work_item_id IS NOT OLD.work_item_id
		OR NEW.decision_key IS NOT OLD.decision_key
		OR NEW.requester_kind IS NOT OLD.requester_kind OR NEW.requester_ref IS NOT OLD.requester_ref
		OR NEW.owner_kind IS NOT OLD.owner_kind OR NEW.owner_ref IS NOT OLD.owner_ref
		OR NEW.request_encoding IS NOT OLD.request_encoding
		OR NEW.request_plain_json IS NOT OLD.request_plain_json
		OR NEW.request_sealed_json IS NOT OLD.request_sealed_json
		OR NEW.request_schema IS NOT OLD.request_schema OR NEW.request_digest IS NOT OLD.request_digest
		OR NEW.request_seal_key_version IS NOT OLD.request_seal_key_version
		OR NEW.request_digest_key_version IS NOT OLD.request_digest_key_version
		OR NEW.request_protection_generation IS NOT OLD.request_protection_generation
		OR NEW.authority_requirement IS NOT OLD.authority_requirement OR NEW.due_at IS NOT OLD.due_at
		OR NEW.last_response_seq <> OLD.last_response_seq + 1
		OR NEW.version <> NEW.last_response_seq + 1
		OR (OLD.state <> NEW.state AND NOT (
			(OLD.state IN ('pending','blocked') AND NEW.state = 'accepted') OR
			(OLD.state = 'accepted' AND NEW.state = 'blocked') OR
			(OLD.state NOT IN ('resolved','rejected','canceled','expired')
				AND NEW.state IN ('resolved','rejected','canceled','expired'))))
		OR (NOT (OLD.state IN ('pending','blocked') AND NEW.state = 'accepted')
			AND (NEW.accepted_delivery_id IS NOT OLD.accepted_delivery_id
				OR NEW.accepted_at IS NOT OLD.accepted_at))
		OR (NEW.state IN ('accepted','blocked','resolved','rejected')
			AND NEW.updated_at >= OLD.due_at)
		OR OLD.state IN ('resolved','rejected','canceled','expired');
	SELECT RAISE(ABORT, 'olivares: DecisionRequest typed reference is non-canonical')
	FROM (
		SELECT NEW.requester_kind AS kind, NEW.requester_ref AS ref
		UNION ALL SELECT NEW.owner_kind, NEW.owner_ref
	) refs
	WHERE (refs.kind = 'session' AND
			(refs.ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(refs.ref,5),'-','')) <> 32
				OR replace(substr(refs.ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (refs.kind IN ('user','user_group','agent','agent_group') AND
			(refs.ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(refs.ref,'-','')) <> 32
				OR replace(refs.ref,'-','') GLOB '*[^0-9a-f]*'))
		OR (refs.kind = 'system' AND
			(trim(refs.ref) <> refs.ref OR instr(refs.ref,char(10)) > 0
				OR instr(refs.ref,char(13)) > 0 OR instr(refs.ref,char(0)) > 0));
	SELECT RAISE(ABORT, 'olivares: DecisionRequest transition evidence is inconsistent')
	WHERE NEW.state NOT IN ('pending','accepted','blocked','resolved','rejected','canceled','expired')
		OR (NEW.accepted_delivery_id IS NULL) IS NOT (NEW.accepted_at IS NULL)
		OR (NEW.accepted_at IS NOT NULL AND
			(NEW.accepted_at < NEW.created_at OR NEW.accepted_at > NEW.updated_at))
		OR (NEW.state = 'pending' AND (NEW.last_response_seq <> 0
			OR NEW.accepted_delivery_id IS NOT NULL OR NEW.blocked_code IS NOT NULL
			OR NEW.terminal_code IS NOT NULL OR NEW.resolved_decision_id IS NOT NULL))
		OR (NEW.state = 'accepted' AND (NEW.last_response_seq < 1 OR NEW.last_response_seq % 2 <> 1
			OR NEW.accepted_delivery_id IS NULL OR NEW.accepted_at IS NULL
			OR NEW.blocked_code IS NOT NULL OR NEW.terminal_code IS NOT NULL
			OR NEW.resolved_decision_id IS NOT NULL))
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
