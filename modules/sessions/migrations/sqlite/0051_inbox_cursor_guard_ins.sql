-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_inbox_cursor_guard_ins
BEFORE INSERT ON sessions_inbox_cursor
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
	SELECT RAISE(ABORT, 'olivares: invalid InboxCursor shape')
	WHERE NEW.version < 1 OR NEW.updated_at < NEW.created_at
		OR NEW.reader_kind NOT IN ('user','agent','session')
		OR (NEW.reader_kind = 'session' AND
			(length(NEW.reader_ref) <> 40 OR substr(NEW.reader_ref,1,4) <> 'osn_'))
		OR (NEW.reader_kind <> 'session' AND length(NEW.reader_ref) <> 36)
		OR (NEW.reader_kind = 'session' AND
			(NEW.reader_ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(NEW.reader_ref,5),'-','')) <> 32
				OR replace(substr(NEW.reader_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.reader_kind IN ('user','agent') AND
			(NEW.reader_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.reader_ref,'-','')) <> 32
				OR replace(NEW.reader_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR NEW.mailbox_kind NOT IN ('personal','channel')
		OR length(CAST(NEW.mailbox_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR (NEW.mailbox_kind = 'personal' AND NEW.mailbox_ref <> NEW.reader_ref)
		OR (NEW.mailbox_kind = 'channel' AND length(NEW.mailbox_ref) <> 36)
		OR (NEW.mailbox_kind = 'channel' AND
			(NEW.mailbox_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.mailbox_ref,'-','')) <> 32
				OR replace(NEW.mailbox_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR NEW.last_seen_seq < 0 OR NEW.last_seen_at < NEW.created_at
		OR NEW.last_seen_at > NEW.updated_at OR length(NEW.filter_hash) <> 32;
END;
