-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_item_state_guard_upd
BEFORE UPDATE ON sessions_work_item
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: sessions work tenant/workspace lineage is immutable')
	WHERE NEW.tenant_id IS NOT OLD.tenant_id OR NEW.workspace_id IS NOT OLD.workspace_id;
	SELECT RAISE(ABORT, 'olivares: sessions work item vocabulary, size or hash invariant failed')
	WHERE length(NEW.work_kind) NOT BETWEEN 1 AND 64
		OR NEW.work_kind GLOB '*[^a-z0-9._-]*'
		OR length(CAST(NEW.title AS BLOB)) NOT BETWEEN 1 AND 256
		OR length(CAST(NEW.brief_md AS BLOB)) NOT BETWEEN 1 AND 65536
		OR length(NEW.brief_hash) <> 32
		OR length(CAST(NEW.context_refs AS BLOB)) > 16384
		OR NOT json_valid(NEW.context_refs) OR json_type(NEW.context_refs) <> 'array'
		OR json_array_length(NEW.context_refs) > 64
		OR NEW.status NOT IN ('draft','ready','active','blocked','review','completed','failed','canceled')
		OR NEW.priority NOT IN ('p0','p1','p2','p3')
		OR NEW.owner_kind NOT IN ('user','agent','session')
		OR length(CAST(NEW.owner_ref AS BLOB)) NOT BETWEEN 1 AND 512 OR NEW.owner_epoch < 1
		OR NEW.provenance_kind NOT IN ('human','workflow','a2a','mcp','migration','system')
		OR length(CAST(NEW.provenance_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR (NEW.provenance_hash IS NOT NULL AND length(NEW.provenance_hash) <> 32)
		OR NEW.acceptance_revision < 1 OR NEW.last_event_seq < 0;
	SELECT RAISE(ABORT, 'olivares: blocked work reason fields are incoherent')
	WHERE (NEW.status = 'blocked' AND (NEW.blocked_code IS NULL
		OR length(NEW.blocked_code) NOT BETWEEN 1 AND 64 OR NEW.blocked_code GLOB '*[^a-z0-9._-]*'
		OR NEW.blocked_reason IS NULL OR length(CAST(NEW.blocked_reason AS BLOB)) NOT BETWEEN 1 AND 2048))
		OR (NEW.status <> 'blocked' AND (NEW.blocked_code IS NOT NULL OR NEW.blocked_reason IS NOT NULL));
	SELECT RAISE(ABORT, 'olivares: terminal reason fields are incoherent')
	WHERE (NEW.status IN ('failed','canceled') AND (NEW.terminal_code IS NULL
		OR length(NEW.terminal_code) NOT BETWEEN 1 AND 64 OR NEW.terminal_code GLOB '*[^a-z0-9._-]*'
		OR NEW.terminal_reason IS NULL OR length(CAST(NEW.terminal_reason AS BLOB)) NOT BETWEEN 1 AND 2048))
		OR (NEW.status = 'completed' AND ((NEW.terminal_code IS NULL) <> (NEW.terminal_reason IS NULL)))
		OR (NEW.status NOT IN ('completed','failed','canceled')
			AND (NEW.terminal_code IS NOT NULL OR NEW.terminal_reason IS NOT NULL));
	SELECT RAISE(ABORT, 'olivares: sessions work terminal/archive fields are incoherent')
	WHERE (NEW.status IN ('completed','failed','canceled') AND NEW.terminal_at IS NULL)
		OR (NEW.status NOT IN ('completed','failed','canceled')
			AND (NEW.terminal_at IS NOT NULL OR NEW.archived_at IS NOT NULL));
	SELECT RAISE(ABORT, 'olivares: sessions work phase timestamps are incoherent')
	WHERE (NEW.status = 'ready' AND NEW.ready_at IS NULL)
		OR (NEW.status = 'active' AND (NEW.ready_at IS NULL OR NEW.started_at IS NULL))
		OR (NEW.status = 'blocked' AND NEW.ready_at IS NULL)
		OR (NEW.status = 'review' AND (NEW.ready_at IS NULL OR NEW.started_at IS NULL OR NEW.review_at IS NULL))
		OR (NEW.status = 'completed' AND (NEW.ready_at IS NULL OR NEW.started_at IS NULL OR NEW.review_at IS NULL));
	SELECT RAISE(ABORT, 'olivares: work parent must exist in the same tenant/workspace and differ from self')
	WHERE NEW.parent_id IS NOT NULL AND (NEW.parent_id = NEW.id OR NOT EXISTS (
		SELECT 1 FROM sessions_work_item p WHERE p.id = NEW.parent_id
			AND p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id));
	SELECT RAISE(ABORT, 'olivares: superseded work must be terminal in the same tenant/workspace')
	WHERE NEW.supersedes_id IS NOT NULL AND (NEW.supersedes_id = NEW.id OR NOT EXISTS (
		SELECT 1 FROM sessions_work_item s WHERE s.id = NEW.supersedes_id
			AND s.tenant_id = NEW.tenant_id AND s.workspace_id = NEW.workspace_id
			AND s.status IN ('completed','failed','canceled')));
	SELECT RAISE(ABORT, 'olivares: owner change must increment owner_epoch exactly once')
	WHERE (OLD.owner_kind IS NOT NEW.owner_kind OR OLD.owner_ref IS NOT NEW.owner_ref)
		AND NEW.owner_epoch <> OLD.owner_epoch + 1;
	SELECT RAISE(ABORT, 'olivares: owner_epoch changed without an owner change')
	WHERE OLD.owner_kind IS NEW.owner_kind AND OLD.owner_ref IS NEW.owner_ref
		AND NEW.owner_epoch <> OLD.owner_epoch;
	SELECT RAISE(ABORT, 'olivares: acceptance_revision may advance only in draft')
	WHERE NEW.acceptance_revision < OLD.acceptance_revision
		OR (NEW.acceptance_revision <> OLD.acceptance_revision AND NEW.status <> 'draft');
	SELECT RAISE(ABORT, 'olivares: illegal sessions work status transition')
	WHERE OLD.status <> NEW.status AND NOT (
		(OLD.status = 'draft' AND NEW.status IN ('ready','canceled')) OR
		(OLD.status = 'ready' AND NEW.status IN ('draft','active','blocked','canceled')) OR
		(OLD.status = 'active' AND NEW.status IN ('blocked','review','failed','canceled')) OR
		(OLD.status = 'blocked' AND NEW.status IN ('ready','active','review','failed','canceled')) OR
		(OLD.status = 'review' AND NEW.status IN ('active','blocked','completed','failed','canceled')));
	SELECT RAISE(ABORT, 'olivares: ready work requires a required criterion and completed blockers')
	WHERE NEW.status = 'ready' AND (NOT EXISTS (
		SELECT 1 FROM sessions_work_acceptance a WHERE a.tenant_id = NEW.tenant_id
			AND a.workspace_id = NEW.workspace_id AND a.work_item_id = NEW.id AND a.required = 1)
		OR EXISTS (SELECT 1 FROM sessions_work_dependency d
			JOIN sessions_work_item p ON p.id = d.depends_on_id
				AND p.tenant_id = d.tenant_id AND p.workspace_id = d.workspace_id
			WHERE d.tenant_id = NEW.tenant_id AND d.workspace_id = NEW.workspace_id
				AND d.work_item_id = NEW.id AND d.active = 1 AND p.status <> 'completed'));
	SELECT RAISE(ABORT, 'olivares: completed work has unmet required acceptance')
	WHERE NEW.status = 'completed' AND (NOT EXISTS (
		SELECT 1 FROM sessions_work_acceptance a WHERE a.tenant_id = NEW.tenant_id
			AND a.workspace_id = NEW.workspace_id AND a.work_item_id = NEW.id AND a.required = 1)
		OR EXISTS (SELECT 1 FROM sessions_work_acceptance a
			WHERE a.tenant_id = NEW.tenant_id AND a.workspace_id = NEW.workspace_id
				AND a.work_item_id = NEW.id AND a.required = 1
				AND (a.state NOT IN ('passed','waived') OR (a.state = 'waived' AND NOT EXISTS (
					SELECT 1 FROM sessions_work_decision_head h WHERE h.tenant_id = a.tenant_id
						AND h.workspace_id = a.workspace_id AND h.work_item_id = a.work_item_id
						AND h.current_decision_id = a.waiver_decision_id AND h.state = 'effective')))));
END;
