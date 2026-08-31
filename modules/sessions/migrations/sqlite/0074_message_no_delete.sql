-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_message_no_delete
BEFORE DELETE ON sessions_message
FOR EACH ROW BEGIN
	SELECT RAISE(ABORT, 'olivares: sessions communication rows cannot be hard-deleted');
END;
