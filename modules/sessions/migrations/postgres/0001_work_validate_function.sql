-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- K1 durable-work invariants. EntityDescriptor cannot accept module-authored
-- CHECK expressions, so the database-enforced vocabulary, lineage and custody
-- rules live in this trigger function. All referenced relations already exist:
-- module descriptors are materialized before module file migrations run.
CREATE OR REPLACE FUNCTION olivares_sessions_work_validate()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
BEGIN
	IF NEW.id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
		OR NEW.tenant_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
		OR NEW.workspace_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
		RAISE EXCEPTION 'olivares: sessions work row has a non-canonical id'
			USING ERRCODE = '23514';
	END IF;
	IF TG_OP = 'UPDATE' THEN
		IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
			OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id THEN
			RAISE EXCEPTION 'olivares: sessions work tenant/workspace lineage is immutable'
				USING ERRCODE = '23514';
		END IF;
	END IF;

	CASE TG_TABLE_NAME
	WHEN 'sessions_work_item' THEN
		IF NEW.work_kind !~ '^[a-z0-9._-]{1,64}$'
			OR octet_length(NEW.title) NOT BETWEEN 1 AND 256
			OR octet_length(NEW.brief_md) NOT BETWEEN 1 AND 65536
			OR octet_length(NEW.brief_hash) <> 32
			OR octet_length(NEW.context_refs) > 16384
			OR jsonb_typeof(NEW.context_refs::jsonb) <> 'array'
			OR jsonb_array_length(NEW.context_refs::jsonb) > 64
			OR NEW.status NOT IN ('draft','ready','active','blocked','review','completed','failed','canceled')
			OR NEW.priority NOT IN ('p0','p1','p2','p3')
			OR NEW.owner_kind NOT IN ('user','agent','session')
			OR octet_length(NEW.owner_ref) NOT BETWEEN 1 AND 512
			OR NEW.owner_epoch < 1
			OR NEW.provenance_kind NOT IN ('human','workflow','a2a','mcp','migration','system')
			OR octet_length(NEW.provenance_ref) NOT BETWEEN 1 AND 512
			OR (NEW.provenance_hash IS NOT NULL AND octet_length(NEW.provenance_hash) <> 32)
			OR NEW.acceptance_revision < 1
			OR NEW.last_event_seq < 0 THEN
			RAISE EXCEPTION 'olivares: sessions work item vocabulary, size or hash invariant failed'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.status = 'blocked' THEN
			IF NEW.blocked_code IS NULL OR NEW.blocked_code !~ '^[a-z0-9._-]{1,64}$'
				OR NEW.blocked_reason IS NULL
				OR octet_length(NEW.blocked_reason) NOT BETWEEN 1 AND 2048 THEN
				RAISE EXCEPTION 'olivares: blocked work requires a typed reason'
					USING ERRCODE = '23514';
			END IF;
		ELSIF NEW.blocked_code IS NOT NULL OR NEW.blocked_reason IS NOT NULL THEN
			RAISE EXCEPTION 'olivares: non-blocked work cannot carry a blocked reason'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.status IN ('failed','canceled') THEN
			IF NEW.terminal_code IS NULL OR NEW.terminal_code !~ '^[a-z0-9._-]{1,64}$'
				OR NEW.terminal_reason IS NULL
				OR octet_length(NEW.terminal_reason) NOT BETWEEN 1 AND 2048 THEN
				RAISE EXCEPTION 'olivares: failed/canceled work requires a typed terminal reason'
					USING ERRCODE = '23514';
			END IF;
		ELSIF NEW.status = 'completed' THEN
			IF (NEW.terminal_code IS NULL) <> (NEW.terminal_reason IS NULL)
				OR (NEW.terminal_code IS NOT NULL AND NEW.terminal_code !~ '^[a-z0-9._-]{1,64}$')
				OR (NEW.terminal_reason IS NOT NULL AND octet_length(NEW.terminal_reason) NOT BETWEEN 1 AND 2048) THEN
				RAISE EXCEPTION 'olivares: completed work terminal code/reason must be a valid pair'
					USING ERRCODE = '23514';
			END IF;
		ELSIF NEW.terminal_code IS NOT NULL OR NEW.terminal_reason IS NOT NULL THEN
			RAISE EXCEPTION 'olivares: non-terminal work cannot carry a terminal reason'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.status IN ('completed','failed','canceled') THEN
			IF NEW.terminal_at IS NULL THEN
				RAISE EXCEPTION 'olivares: terminal work requires terminal_at'
					USING ERRCODE = '23514';
			END IF;
		ELSIF NEW.terminal_at IS NOT NULL OR NEW.archived_at IS NOT NULL THEN
			RAISE EXCEPTION 'olivares: non-terminal work cannot be terminal or archived'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.status = 'ready' AND NEW.ready_at IS NULL
			OR NEW.status = 'active' AND (NEW.ready_at IS NULL OR NEW.started_at IS NULL)
			OR NEW.status = 'blocked' AND NEW.ready_at IS NULL
			OR NEW.status = 'review' AND (NEW.ready_at IS NULL OR NEW.started_at IS NULL OR NEW.review_at IS NULL)
			OR NEW.status = 'completed' AND (NEW.ready_at IS NULL OR NEW.started_at IS NULL OR NEW.review_at IS NULL) THEN
			RAISE EXCEPTION 'olivares: sessions work phase timestamps are incoherent'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.parent_id IS NOT NULL AND (NEW.parent_id = NEW.id OR NOT EXISTS (
			SELECT 1 FROM sessions_work_item p
			WHERE p.id = NEW.parent_id AND p.tenant_id = NEW.tenant_id
				AND p.workspace_id = NEW.workspace_id)) THEN
			RAISE EXCEPTION 'olivares: work parent must exist in the same tenant/workspace and differ from self'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.supersedes_id IS NOT NULL AND (NEW.supersedes_id = NEW.id OR NOT EXISTS (
			SELECT 1 FROM sessions_work_item s
			WHERE s.id = NEW.supersedes_id AND s.tenant_id = NEW.tenant_id
				AND s.workspace_id = NEW.workspace_id
				AND s.status IN ('completed','failed','canceled'))) THEN
			RAISE EXCEPTION 'olivares: superseded work must be terminal in the same tenant/workspace'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' THEN
			IF (OLD.owner_kind, OLD.owner_ref) IS DISTINCT FROM (NEW.owner_kind, NEW.owner_ref) THEN
				IF NEW.owner_epoch <> OLD.owner_epoch + 1 THEN
					RAISE EXCEPTION 'olivares: owner change must increment owner_epoch exactly once'
						USING ERRCODE = '23514';
				END IF;
			ELSIF NEW.owner_epoch <> OLD.owner_epoch THEN
				RAISE EXCEPTION 'olivares: owner_epoch changed without an owner change'
					USING ERRCODE = '23514';
			END IF;
			IF NEW.acceptance_revision < OLD.acceptance_revision
				OR (NEW.acceptance_revision <> OLD.acceptance_revision AND NEW.status <> 'draft') THEN
				RAISE EXCEPTION 'olivares: acceptance_revision may advance only in draft'
					USING ERRCODE = '23514';
			END IF;
			IF OLD.status <> NEW.status AND NOT (
				(OLD.status = 'draft' AND NEW.status IN ('ready','canceled')) OR
				(OLD.status = 'ready' AND NEW.status IN ('draft','active','blocked','canceled')) OR
				(OLD.status = 'active' AND NEW.status IN ('blocked','review','failed','canceled')) OR
				(OLD.status = 'blocked' AND NEW.status IN ('ready','active','review','failed','canceled')) OR
				(OLD.status = 'review' AND NEW.status IN ('active','blocked','completed','failed','canceled'))) THEN
				RAISE EXCEPTION 'olivares: illegal sessions work status transition % -> %', OLD.status, NEW.status
					USING ERRCODE = '23514';
			END IF;
		END IF;
		IF NEW.status = 'ready' AND (NOT EXISTS (
			SELECT 1 FROM sessions_work_acceptance a
			WHERE a.tenant_id = NEW.tenant_id AND a.workspace_id = NEW.workspace_id
				AND a.work_item_id = NEW.id AND a.required = true)
			OR EXISTS (
			SELECT 1 FROM sessions_work_dependency d
			JOIN sessions_work_item p ON p.id = d.depends_on_id
				AND p.tenant_id = d.tenant_id AND p.workspace_id = d.workspace_id
			WHERE d.tenant_id = NEW.tenant_id AND d.workspace_id = NEW.workspace_id
				AND d.work_item_id = NEW.id AND d.active = true AND p.status <> 'completed')) THEN
			RAISE EXCEPTION 'olivares: ready work requires a required criterion and completed blockers'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.status = 'completed' AND (NOT EXISTS (
			SELECT 1 FROM sessions_work_acceptance a
			WHERE a.tenant_id = NEW.tenant_id AND a.workspace_id = NEW.workspace_id
				AND a.work_item_id = NEW.id AND a.required = true)
			OR EXISTS (
			SELECT 1 FROM sessions_work_acceptance a
			WHERE a.tenant_id = NEW.tenant_id AND a.workspace_id = NEW.workspace_id
				AND a.work_item_id = NEW.id AND a.required = true
				AND (a.state NOT IN ('passed','waived') OR (a.state = 'waived' AND NOT EXISTS (
					SELECT 1 FROM sessions_work_decision_head h
					WHERE h.tenant_id = a.tenant_id AND h.workspace_id = a.workspace_id
						AND h.work_item_id = a.work_item_id AND h.current_decision_id = a.waiver_decision_id
						AND h.state = 'effective'))))) THEN
			RAISE EXCEPTION 'olivares: completed work has unmet required acceptance'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_work_dependency' THEN
		IF NEW.relation <> 'blocks' OR NEW.work_item_id = NEW.depends_on_id
			OR NEW.added_by_kind NOT IN ('user','agent','session','system')
			OR octet_length(NEW.added_by_ref) NOT BETWEEN 1 AND 512 THEN
			RAISE EXCEPTION 'olivares: invalid sessions work dependency vocabulary or self-edge'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.active THEN
			IF NEW.removed_by_kind IS NOT NULL OR NEW.removed_by_ref IS NOT NULL OR NEW.removed_at IS NOT NULL THEN
				RAISE EXCEPTION 'olivares: active dependency cannot carry removal evidence'
					USING ERRCODE = '23514';
			END IF;
		ELSE
			IF NEW.removed_by_kind NOT IN ('user','agent','session','system')
				OR NEW.removed_by_ref IS NULL OR octet_length(NEW.removed_by_ref) NOT BETWEEN 1 AND 512
				OR NEW.removed_at IS NULL THEN
				RAISE EXCEPTION 'olivares: inactive dependency requires complete removal evidence'
					USING ERRCODE = '23514';
			END IF;
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_work_item w
			WHERE w.id = NEW.work_item_id AND w.tenant_id = NEW.tenant_id AND w.workspace_id = NEW.workspace_id)
			OR NOT EXISTS (SELECT 1 FROM sessions_work_item w
			WHERE w.id = NEW.depends_on_id AND w.tenant_id = NEW.tenant_id AND w.workspace_id = NEW.workspace_id) THEN
			RAISE EXCEPTION 'olivares: dependency endpoints must exist in the same tenant/workspace'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (OLD.work_item_id, OLD.depends_on_id, OLD.relation,
			OLD.added_by_kind, OLD.added_by_ref) IS DISTINCT FROM
			(NEW.work_item_id, NEW.depends_on_id, NEW.relation, NEW.added_by_kind, NEW.added_by_ref) THEN
			RAISE EXCEPTION 'olivares: dependency identity and creation evidence are immutable'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.active AND EXISTS (
			WITH RECURSIVE reach(id) AS (
				SELECT d.depends_on_id FROM sessions_work_dependency d
				WHERE d.tenant_id = NEW.tenant_id AND d.workspace_id = NEW.workspace_id
					AND d.active = true AND d.work_item_id = NEW.depends_on_id AND d.id <> NEW.id
				UNION
				SELECT d.depends_on_id FROM sessions_work_dependency d
				JOIN reach r ON r.id = d.work_item_id
				WHERE d.tenant_id = NEW.tenant_id AND d.workspace_id = NEW.workspace_id
					AND d.active = true AND d.id <> NEW.id)
			SELECT 1 FROM reach WHERE id = NEW.work_item_id) THEN
			RAISE EXCEPTION 'olivares: sessions work dependency cycle'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_work_acceptance' THEN
		IF NEW.criterion_key !~ '^[a-z0-9._-]{1,64}$' OR NEW.ordinal < 0
			OR octet_length(NEW.statement) NOT BETWEEN 1 AND 4096
			OR NEW.state NOT IN ('pending','passed','failed','waived')
			OR (NEW.evidence_hash IS NOT NULL AND octet_length(NEW.evidence_hash) <> 32) THEN
			RAISE EXCEPTION 'olivares: invalid sessions work acceptance vocabulary, size or hash'
				USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_work_item w
			WHERE w.id = NEW.work_item_id AND w.tenant_id = NEW.tenant_id AND w.workspace_id = NEW.workspace_id) THEN
			RAISE EXCEPTION 'olivares: acceptance parent must exist in the same tenant/workspace'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'pending' THEN
			IF NEW.evidence_ref IS NOT NULL OR NEW.evidence_hash IS NOT NULL
				OR NEW.verified_by_kind IS NOT NULL OR NEW.verified_by_ref IS NOT NULL
				OR NEW.verified_at IS NOT NULL OR NEW.waiver_decision_id IS NOT NULL THEN
				RAISE EXCEPTION 'olivares: pending acceptance cannot carry verification evidence'
					USING ERRCODE = '23514';
			END IF;
		ELSE
			IF NEW.verified_by_kind NOT IN ('user','agent','session','system')
				OR NEW.verified_by_ref IS NULL OR octet_length(NEW.verified_by_ref) NOT BETWEEN 1 AND 512
				OR NEW.verified_at IS NULL THEN
				RAISE EXCEPTION 'olivares: evaluated acceptance requires verifier and time'
					USING ERRCODE = '23514';
			END IF;
		END IF;
		IF NEW.state IN ('passed','failed') AND (NEW.evidence_ref IS NULL OR octet_length(NEW.evidence_ref) NOT BETWEEN 1 AND 512)
			OR NEW.state = 'passed' AND NEW.evidence_hash IS NULL
			OR NEW.state <> 'waived' AND NEW.waiver_decision_id IS NOT NULL THEN
			RAISE EXCEPTION 'olivares: acceptance evidence does not match its state'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'waived' AND (NEW.waiver_decision_id IS NULL OR NOT EXISTS (
			SELECT 1 FROM sessions_work_decision_head h
			WHERE h.tenant_id = NEW.tenant_id AND h.workspace_id = NEW.workspace_id
				AND h.work_item_id = NEW.work_item_id AND h.current_decision_id = NEW.waiver_decision_id
				AND h.state = 'effective')) THEN
			RAISE EXCEPTION 'olivares: waived acceptance requires the effective decision head'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (OLD.criterion_key <> NEW.criterion_key OR OLD.work_item_id <> NEW.work_item_id) THEN
			RAISE EXCEPTION 'olivares: acceptance identity is immutable'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (OLD.statement, OLD.required, OLD.ordinal) IS DISTINCT FROM
			(NEW.statement, NEW.required, NEW.ordinal) AND NOT EXISTS (
			SELECT 1 FROM sessions_work_item w WHERE w.id = NEW.work_item_id
				AND w.tenant_id = NEW.tenant_id AND w.workspace_id = NEW.workspace_id AND w.status = 'draft') THEN
			RAISE EXCEPTION 'olivares: acceptance definition is editable only in draft'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_work_decision' THEN
		IF NEW.decision_key !~ '^[a-z0-9._-]{1,128}$' OR NEW.decision_seq < 1
			OR NEW.subject_kind !~ '^[a-z0-9._-]{1,64}$'
			OR octet_length(NEW.subject_ref) NOT BETWEEN 1 AND 512
			OR NEW.operation NOT IN ('set','supersede','revoke')
			OR octet_length(NEW.statement_md) NOT BETWEEN 1 AND 16384
			OR octet_length(NEW.rationale_md) NOT BETWEEN 1 AND 16384
			OR NEW.decided_by_kind NOT IN ('user','agent','system')
			OR octet_length(NEW.decided_by_ref) NOT BETWEEN 1 AND 512
			OR octet_length(NEW.authority_ref) NOT BETWEEN 1 AND 512
			OR octet_length(NEW.decision_hash) <> 32 THEN
			RAISE EXCEPTION 'olivares: invalid sessions work decision vocabulary, size or hash'
				USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_work_item w
			WHERE w.id = NEW.work_item_id AND w.tenant_id = NEW.tenant_id AND w.workspace_id = NEW.workspace_id) THEN
			RAISE EXCEPTION 'olivares: decision parent must exist in the same tenant/workspace'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.operation = 'set' THEN
			IF NEW.supersedes_id IS NOT NULL OR NEW.revokes_id IS NOT NULL OR NOT (
				(NEW.decision_seq = 1 AND NOT EXISTS (
					SELECT 1 FROM sessions_work_decision_head h WHERE h.tenant_id = NEW.tenant_id
						AND h.workspace_id = NEW.workspace_id AND h.work_item_id = NEW.work_item_id
						AND h.decision_key = NEW.decision_key)) OR
				(NEW.decision_seq > 1 AND EXISTS (
					SELECT 1 FROM sessions_work_decision_head h WHERE h.tenant_id = NEW.tenant_id
						AND h.workspace_id = NEW.workspace_id AND h.work_item_id = NEW.work_item_id
						AND h.decision_key = NEW.decision_key AND h.state = 'revoked'
						AND h.current_seq = NEW.decision_seq - 1))) THEN
				RAISE EXCEPTION 'olivares: set decision is not the first decision or a post-revoke successor'
					USING ERRCODE = '23514';
			END IF;
		ELSIF NEW.operation = 'supersede' THEN
			IF NEW.supersedes_id IS NULL OR NEW.revokes_id IS NOT NULL OR NOT EXISTS (
				SELECT 1 FROM sessions_work_decision_head h
				WHERE h.tenant_id = NEW.tenant_id AND h.workspace_id = NEW.workspace_id
					AND h.work_item_id = NEW.work_item_id AND h.decision_key = NEW.decision_key
					AND h.state = 'effective' AND h.current_decision_id = NEW.supersedes_id
					AND h.current_seq = NEW.decision_seq - 1) THEN
				RAISE EXCEPTION 'olivares: supersede must name the current effective decision'
					USING ERRCODE = '23514';
			END IF;
		ELSE
			IF NEW.revokes_id IS NULL OR NEW.supersedes_id IS NOT NULL OR NOT EXISTS (
				SELECT 1 FROM sessions_work_decision_head h
				WHERE h.tenant_id = NEW.tenant_id AND h.workspace_id = NEW.workspace_id
					AND h.work_item_id = NEW.work_item_id AND h.decision_key = NEW.decision_key
					AND h.state = 'effective' AND h.current_decision_id = NEW.revokes_id
					AND h.current_seq = NEW.decision_seq - 1) THEN
				RAISE EXCEPTION 'olivares: revoke must name the current effective decision'
					USING ERRCODE = '23514';
			END IF;
		END IF;

	WHEN 'sessions_work_decision_head' THEN
		IF NEW.decision_key !~ '^[a-z0-9._-]{1,128}$' OR NEW.current_seq < 1
			OR NEW.state NOT IN ('effective','revoked') OR octet_length(NEW.head_hash) <> 32 THEN
			RAISE EXCEPTION 'olivares: invalid sessions work decision-head vocabulary or hash'
				USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_work_item w
			WHERE w.id = NEW.work_item_id AND w.tenant_id = NEW.tenant_id AND w.workspace_id = NEW.workspace_id)
			OR NOT EXISTS (SELECT 1 FROM sessions_work_decision d
			WHERE d.id = NEW.current_decision_id AND d.tenant_id = NEW.tenant_id
				AND d.workspace_id = NEW.workspace_id AND d.work_item_id = NEW.work_item_id
				AND d.decision_key = NEW.decision_key AND d.decision_seq = NEW.current_seq
				AND d.decision_hash = NEW.head_hash
				AND ((d.operation = 'revoke' AND NEW.state = 'revoked')
					OR (d.operation <> 'revoke' AND NEW.state = 'effective'))) THEN
			RAISE EXCEPTION 'olivares: decision head must name the matching decision in the same aggregate'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (OLD.work_item_id <> NEW.work_item_id OR OLD.decision_key <> NEW.decision_key
			OR NEW.current_seq <> OLD.current_seq + 1) THEN
			RAISE EXCEPTION 'olivares: decision head identity is immutable and sequence advances by one'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_work_command' THEN
		IF octet_length(NEW.actor_fingerprint) <> 32
			OR octet_length(NEW.command_scope) NOT BETWEEN 1 AND 512
			OR octet_length(NEW.idempotency_key_hash) <> 32
			OR octet_length(NEW.request_hash) <> 32
			OR octet_length(NEW.plan_hash) <> 32
			OR NEW.result_kind !~ '^[a-z0-9._-]{1,128}$'
			OR NEW.http_status NOT BETWEEN 100 AND 599
			OR octet_length(NEW.response_json) > 16384
			OR jsonb_typeof(NEW.response_json::jsonb) <> 'object'
			OR octet_length(NEW.response_hash) <> 32
			OR NEW.audit_seq < 1 OR octet_length(NEW.audit_hash) <> 32 THEN
			RAISE EXCEPTION 'olivares: invalid sessions work command receipt'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.result_kind = 'sessions.work_item' AND (NEW.result_id IS NULL OR NOT EXISTS (
			SELECT 1 FROM sessions_work_item w WHERE w.id = NEW.result_id
				AND w.tenant_id = NEW.tenant_id AND w.workspace_id = NEW.workspace_id)) THEN
			RAISE EXCEPTION 'olivares: command result must resolve in the same tenant/workspace'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_work_event' THEN
		IF NEW.aggregate_kind !~ '^[a-z0-9._-]{1,128}$' OR NEW.seq < 1
			OR NEW.event_type !~ '^[a-z0-9._-]{1,128}$'
			OR NEW.actor_kind NOT IN ('user','agent','session','system')
			OR octet_length(NEW.actor_ref) NOT BETWEEN 1 AND 512
			OR octet_length(NEW.payload_json) > 16384
			OR jsonb_typeof(NEW.payload_json::jsonb) <> 'object'
			OR octet_length(NEW.payload_hash) <> 32
			OR NEW.audit_seq < 1 OR octet_length(NEW.audit_hash) <> 32 THEN
			RAISE EXCEPTION 'olivares: invalid sessions work event vocabulary, payload or evidence hash'
				USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_work_item w
			WHERE w.id = NEW.aggregate_id AND w.tenant_id = NEW.tenant_id AND w.workspace_id = NEW.workspace_id) THEN
			RAISE EXCEPTION 'olivares: event aggregate must resolve in the same tenant/workspace'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_work_outbox' THEN
		IF NEW.state NOT IN ('pending','delivering','published','dead_letter') OR NEW.attempts < 0
			OR (NEW.last_outcome IS NOT NULL AND (NEW.last_outcome !~ '^[a-z0-9._-]{1,64}$')) THEN
			RAISE EXCEPTION 'olivares: invalid sessions work outbox vocabulary'
				USING ERRCODE = '23514';
		END IF;
		IF NOT EXISTS (SELECT 1 FROM sessions_work_event e
			WHERE e.event_id = NEW.event_id AND e.tenant_id = NEW.tenant_id AND e.workspace_id = NEW.workspace_id) THEN
			RAISE EXCEPTION 'olivares: outbox event must resolve in the same tenant/workspace'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'pending' AND (NEW.claim_owner IS NOT NULL OR NEW.claim_until IS NOT NULL OR NEW.published_at IS NOT NULL)
			OR NEW.state = 'delivering' AND (NEW.claim_owner IS NULL OR octet_length(NEW.claim_owner) NOT BETWEEN 1 AND 512 OR NEW.claim_until IS NULL OR NEW.published_at IS NOT NULL)
			OR NEW.state = 'published' AND (NEW.claim_owner IS NOT NULL OR NEW.claim_until IS NOT NULL OR NEW.published_at IS NULL)
			OR NEW.state = 'dead_letter' AND (NEW.claim_owner IS NOT NULL OR NEW.claim_until IS NOT NULL OR NEW.published_at IS NOT NULL OR NEW.last_outcome IS NULL) THEN
			RAISE EXCEPTION 'olivares: sessions work outbox lifecycle fields are incoherent'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (NEW.event_id <> OLD.event_id OR NEW.attempts < OLD.attempts) THEN
			RAISE EXCEPTION 'olivares: outbox event is immutable and attempts cannot decrease'
				USING ERRCODE = '23514';
		END IF;

	WHEN 'sessions_work_guard' THEN
		IF NEW.guard_kind NOT IN ('dependency_graph','lease_clock') OR NEW.epoch < 0
			OR (NEW.guard_kind = 'dependency_graph' AND NEW.last_db_time IS NOT NULL) THEN
			RAISE EXCEPTION 'olivares: invalid sessions work guard state'
				USING ERRCODE = '23514';
		END IF;
		IF TG_OP = 'UPDATE' AND (NEW.guard_kind <> OLD.guard_kind OR NEW.epoch <> OLD.epoch + 1) THEN
			RAISE EXCEPTION 'olivares: work guard identity is immutable and epoch advances by one'
				USING ERRCODE = '23514';
		END IF;
	END CASE;

	RETURN NEW;
END
$fn$;
