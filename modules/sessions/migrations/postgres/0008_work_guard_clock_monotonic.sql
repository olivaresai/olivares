-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE OR REPLACE FUNCTION olivares_sessions_work_clock_monotonic()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
DECLARE
	rebase_authorized boolean;
BEGIN
	IF OLD.guard_kind = 'lease_clock' THEN
		IF NEW.guard_kind <> OLD.guard_kind OR NEW.workspace_id <> OLD.workspace_id
			OR NEW.tenant_id <> OLD.tenant_id OR NEW.last_db_time IS NULL THEN
			RAISE EXCEPTION 'olivares: lease clock guard identity/time is immutable and required'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.last_db_time < OLD.last_db_time THEN
			SELECT EXISTS (
				SELECT 1 FROM sessions_work_decision d
				JOIN sessions_work_decision_head h
					ON h.tenant_id = d.tenant_id AND h.workspace_id = d.workspace_id
					AND h.work_item_id = d.work_item_id
					AND h.current_decision_id = d.id AND h.state = 'effective'
				WHERE d.id = NEW.clock_rebase_decision_id
					AND d.tenant_id = NEW.tenant_id AND d.workspace_id = NEW.workspace_id
			) INTO rebase_authorized;
			IF NEW.clock_rebase_decision_id IS NULL
				OR NEW.clock_rebase_decision_id IS NOT DISTINCT FROM OLD.clock_rebase_decision_id
				OR NEW.clock_rebase_evidence_ref IS NULL
				OR octet_length(NEW.clock_rebase_evidence_ref) NOT BETWEEN 1 AND 512
				OR NOT rebase_authorized THEN
				RAISE EXCEPTION 'olivares: lease clock moved backwards without effective rebase evidence'
					USING ERRCODE = '23514';
			END IF;
		ELSIF NEW.clock_rebase_decision_id IS DISTINCT FROM OLD.clock_rebase_decision_id
			OR NEW.clock_rebase_evidence_ref IS DISTINCT FROM OLD.clock_rebase_evidence_ref THEN
			RAISE EXCEPTION 'olivares: lease clock rebase evidence changed without rollback'
				USING ERRCODE = '23514';
		END IF;
	END IF;
	RETURN NEW;
END;
$fn$;

CREATE TRIGGER sessions_work_guard_clock_monotonic
BEFORE UPDATE ON sessions_work_guard
FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_clock_monotonic();
