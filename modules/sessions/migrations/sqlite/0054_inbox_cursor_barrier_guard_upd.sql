-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_inbox_cursor_barrier_guard_upd
BEFORE UPDATE ON sessions_inbox_cursor_barrier
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: InboxCursorBarrier immutable lineage or transition changed')
	WHERE NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id
		OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at
		OR NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at
		OR NEW.reader_kind IS NOT OLD.reader_kind OR NEW.reader_ref IS NOT OLD.reader_ref
		OR NEW.mailbox_kind IS NOT OLD.mailbox_kind OR NEW.mailbox_ref IS NOT OLD.mailbox_ref
		OR NEW.filter_hash IS NOT OLD.filter_hash OR NEW.delivery_id IS NOT OLD.delivery_id
		OR NEW.barrier_seq IS NOT OLD.barrier_seq OR NEW.cause IS NOT OLD.cause
		OR NEW.reason_code IS NOT OLD.reason_code
		OR (OLD.state <> NEW.state AND NOT (OLD.state = 'active' AND NEW.state = 'resolved'))
		OR OLD.state = 'resolved'
		OR (NEW.state = 'active' AND NEW.resolved_at IS NOT NULL)
		OR (NEW.state = 'resolved' AND
			(NEW.resolved_at IS NULL OR NEW.resolved_at < NEW.created_at OR NEW.resolved_at > NEW.updated_at));
	SELECT RAISE(ABORT, 'olivares: InboxCursorBarrier typed reference is non-canonical')
	FROM (
		SELECT NEW.reader_kind AS kind, NEW.reader_ref AS ref
		UNION ALL SELECT CASE WHEN NEW.mailbox_kind = 'channel' THEN 'user' END, NEW.mailbox_ref
	) refs
	WHERE (refs.kind = 'session' AND
			(refs.ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(refs.ref,5),'-','')) <> 32
				OR replace(substr(refs.ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (refs.kind IN ('user','agent') AND
			(refs.ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(refs.ref,'-','')) <> 32
				OR replace(refs.ref,'-','') GLOB '*[^0-9a-f]*'));
	SELECT RAISE(ABORT, 'olivares: InboxCursorBarrier has duplicate active identity')
	WHERE NEW.state = 'active' AND EXISTS (
		SELECT 1 FROM sessions_inbox_cursor_barrier p
		WHERE p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
			AND p.reader_kind = NEW.reader_kind AND p.reader_ref = NEW.reader_ref
			AND p.mailbox_kind = NEW.mailbox_kind AND p.mailbox_ref IS NEW.mailbox_ref
			AND p.filter_hash = NEW.filter_hash AND p.delivery_id = NEW.delivery_id
			AND p.state = 'active' AND p.id <> NEW.id);
END;
