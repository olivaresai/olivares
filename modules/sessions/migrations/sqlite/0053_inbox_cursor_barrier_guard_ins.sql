-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_inbox_cursor_barrier_guard_ins
BEFORE INSERT ON sessions_inbox_cursor_barrier
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
	SELECT RAISE(ABORT, 'olivares: invalid InboxCursorBarrier shape')
	WHERE NEW.version < 1 OR NEW.updated_at < NEW.created_at
		OR NEW.reader_kind NOT IN ('user','agent','session')
		OR NEW.mailbox_kind NOT IN ('personal','channel') OR length(NEW.filter_hash) <> 32
		OR NEW.barrier_seq < 1 OR NEW.cause NOT IN ('not_yet_available','temporarily_invisible')
		OR NEW.state NOT IN ('active','resolved')
		OR length(NEW.reason_code) NOT BETWEEN 1 AND 128 OR NEW.reason_code GLOB '*[^a-z0-9._-]*'
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
	SELECT RAISE(ABORT, 'olivares: InboxCursorBarrier crosses cursor/delivery lineage')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_inbox_cursor c
		WHERE c.tenant_id = NEW.tenant_id AND c.workspace_id = NEW.workspace_id
			AND c.reader_kind = NEW.reader_kind AND c.reader_ref = NEW.reader_ref
			AND c.mailbox_kind = NEW.mailbox_kind AND c.mailbox_ref = NEW.mailbox_ref
			AND c.filter_hash = NEW.filter_hash
			AND (NEW.state = 'resolved' OR NEW.barrier_seq > c.last_seen_seq))
		OR NOT EXISTS (SELECT 1 FROM sessions_message_delivery d
			JOIN sessions_message m ON m.id = d.message_id AND m.tenant_id = d.tenant_id
				AND m.workspace_id = d.workspace_id
			WHERE d.id = NEW.delivery_id AND d.tenant_id = NEW.tenant_id
				AND d.workspace_id = NEW.workspace_id AND d.delivery_seq = NEW.barrier_seq
				AND d.recipient_kind = NEW.reader_kind AND d.recipient_ref = NEW.reader_ref
				AND ((NEW.mailbox_kind = 'personal' AND NEW.mailbox_ref = NEW.reader_ref)
					OR (NEW.mailbox_kind = 'channel' AND m.channel_id = NEW.mailbox_ref)));
	SELECT RAISE(ABORT, 'olivares: InboxCursorBarrier has duplicate active identity')
	WHERE NEW.state = 'active' AND EXISTS (
		SELECT 1 FROM sessions_inbox_cursor_barrier p
		WHERE p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
			AND p.reader_kind = NEW.reader_kind AND p.reader_ref = NEW.reader_ref
			AND p.mailbox_kind = NEW.mailbox_kind AND p.mailbox_ref IS NEW.mailbox_ref
			AND p.filter_hash = NEW.filter_hash AND p.delivery_id = NEW.delivery_id
			AND p.state = 'active' AND p.id <> NEW.id);
END;
