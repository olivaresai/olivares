-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_item_state_guard_ins
BEFORE INSERT ON sessions_work_item
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: sessions work item vocabulary, size or hash invariant failed')
	WHERE length(NEW.work_kind) NOT BETWEEN 1 AND 64
		OR NEW.work_kind GLOB '*[^a-z0-9._-]*'
		OR length(CAST(NEW.title AS BLOB)) NOT BETWEEN 1 AND 256
		OR length(CAST(NEW.brief_md AS BLOB)) NOT BETWEEN 1 AND 65536
		OR length(NEW.brief_hash) <> 32
		OR length(CAST(NEW.context_refs AS BLOB)) > 16384
		OR NOT json_valid(NEW.context_refs)
		OR json_type(NEW.context_refs) <> 'array'
		OR json_array_length(NEW.context_refs) > 64
		OR NEW.status NOT IN ('draft','ready','active','blocked','review','completed','failed','canceled')
		OR NEW.priority NOT IN ('p0','p1','p2','p3')
		OR NEW.owner_kind NOT IN ('user','agent','session')
		OR length(CAST(NEW.owner_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR NEW.owner_epoch < 1
		OR NEW.provenance_kind NOT IN ('human','workflow','a2a','mcp','migration','system')
		OR length(CAST(NEW.provenance_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR (NEW.provenance_hash IS NOT NULL AND length(NEW.provenance_hash) <> 32)
		OR NEW.acceptance_revision < 1 OR NEW.last_event_seq < 0;
	SELECT RAISE(ABORT, 'olivares: blocked work requires a typed reason')
	WHERE NEW.status = 'blocked' AND (NEW.blocked_code IS NULL
		OR length(NEW.blocked_code) NOT BETWEEN 1 AND 64 OR NEW.blocked_code GLOB '*[^a-z0-9._-]*'
		OR NEW.blocked_reason IS NULL OR length(CAST(NEW.blocked_reason AS BLOB)) NOT BETWEEN 1 AND 2048);
	SELECT RAISE(ABORT, 'olivares: non-blocked work cannot carry a blocked reason')
	WHERE NEW.status <> 'blocked' AND (NEW.blocked_code IS NOT NULL OR NEW.blocked_reason IS NOT NULL);
	SELECT RAISE(ABORT, 'olivares: failed/canceled work requires a typed terminal reason')
	WHERE NEW.status IN ('failed','canceled') AND (NEW.terminal_code IS NULL
		OR length(NEW.terminal_code) NOT BETWEEN 1 AND 64 OR NEW.terminal_code GLOB '*[^a-z0-9._-]*'
		OR NEW.terminal_reason IS NULL OR length(CAST(NEW.terminal_reason AS BLOB)) NOT BETWEEN 1 AND 2048);
	SELECT RAISE(ABORT, 'olivares: completed work terminal code/reason must be a valid pair')
	WHERE NEW.status = 'completed' AND ((NEW.terminal_code IS NULL) <> (NEW.terminal_reason IS NULL)
		OR (NEW.terminal_code IS NOT NULL AND (length(NEW.terminal_code) NOT BETWEEN 1 AND 64
			OR NEW.terminal_code GLOB '*[^a-z0-9._-]*'))
		OR (NEW.terminal_reason IS NOT NULL AND length(CAST(NEW.terminal_reason AS BLOB)) NOT BETWEEN 1 AND 2048));
	SELECT RAISE(ABORT, 'olivares: non-terminal work cannot carry a terminal reason')
	WHERE NEW.status NOT IN ('completed','failed','canceled')
		AND (NEW.terminal_code IS NOT NULL OR NEW.terminal_reason IS NOT NULL);
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
END;
