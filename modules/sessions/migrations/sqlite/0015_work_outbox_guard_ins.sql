-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_outbox_guard_ins
BEFORE INSERT ON sessions_work_outbox
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: invalid sessions work outbox vocabulary')
	WHERE NEW.state NOT IN ('pending','delivering','published','dead_letter') OR NEW.attempts < 0
		OR (NEW.last_outcome IS NOT NULL AND (length(NEW.last_outcome) NOT BETWEEN 1 AND 64
			OR NEW.last_outcome GLOB '*[^a-z0-9._-]*'));
	SELECT RAISE(ABORT, 'olivares: outbox event must resolve in the same tenant/workspace')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_work_event e WHERE e.event_id = NEW.event_id
		AND e.tenant_id = NEW.tenant_id AND e.workspace_id = NEW.workspace_id);
	SELECT RAISE(ABORT, 'olivares: sessions work outbox lifecycle fields are incoherent')
	WHERE (NEW.state = 'pending' AND (NEW.claim_owner IS NOT NULL OR NEW.claim_until IS NOT NULL OR NEW.published_at IS NOT NULL))
		OR (NEW.state = 'delivering' AND (NEW.claim_owner IS NULL
			OR length(CAST(NEW.claim_owner AS BLOB)) NOT BETWEEN 1 AND 512
			OR NEW.claim_until IS NULL OR NEW.published_at IS NOT NULL))
		OR (NEW.state = 'published' AND (NEW.claim_owner IS NOT NULL OR NEW.claim_until IS NOT NULL OR NEW.published_at IS NULL))
		OR (NEW.state = 'dead_letter' AND (NEW.claim_owner IS NOT NULL OR NEW.claim_until IS NOT NULL
			OR NEW.published_at IS NOT NULL OR NEW.last_outcome IS NULL));
END;
