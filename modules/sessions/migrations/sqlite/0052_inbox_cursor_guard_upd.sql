-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_inbox_cursor_guard_upd
BEFORE UPDATE ON sessions_inbox_cursor
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: InboxCursor immutable identity or monotonicity changed')
	WHERE NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id
		OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at
		OR NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at
		OR NEW.reader_kind IS NOT OLD.reader_kind OR NEW.reader_ref IS NOT OLD.reader_ref
		OR NEW.mailbox_kind IS NOT OLD.mailbox_kind OR NEW.mailbox_ref IS NOT OLD.mailbox_ref
		OR NEW.filter_hash IS NOT OLD.filter_hash OR NEW.last_seen_seq < OLD.last_seen_seq
		OR NEW.last_seen_at < OLD.last_seen_at OR NEW.last_seen_at > NEW.updated_at;
	SELECT RAISE(ABORT, 'olivares: InboxCursor typed reference is non-canonical')
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
	SELECT RAISE(ABORT, 'olivares: InboxCursor cannot cross an active barrier')
	WHERE EXISTS (
		SELECT 1 FROM sessions_inbox_cursor_barrier b
		WHERE b.tenant_id = NEW.tenant_id AND b.workspace_id = NEW.workspace_id
			AND b.reader_kind = NEW.reader_kind AND b.reader_ref = NEW.reader_ref
			AND b.mailbox_kind = NEW.mailbox_kind AND b.mailbox_ref IS NEW.mailbox_ref
			AND b.filter_hash IS NEW.filter_hash AND b.state = 'active'
			AND NEW.last_seen_seq >= b.barrier_seq);
END;
