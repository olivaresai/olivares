-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_work_acceptance_guard_ins
BEFORE INSERT ON sessions_work_acceptance
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: invalid sessions work acceptance vocabulary, size or hash')
	WHERE length(NEW.criterion_key) NOT BETWEEN 1 AND 64
		OR NEW.criterion_key GLOB '*[^a-z0-9._-]*' OR NEW.ordinal < 0
		OR length(CAST(NEW.statement AS BLOB)) NOT BETWEEN 1 AND 4096
		OR NEW.required NOT IN (0,1)
		OR NEW.state NOT IN ('pending','passed','failed','waived')
		OR (NEW.evidence_hash IS NOT NULL AND length(NEW.evidence_hash) <> 32);
	SELECT RAISE(ABORT, 'olivares: acceptance parent must exist in the same tenant/workspace')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_work_item w WHERE w.id = NEW.work_item_id
		AND w.tenant_id = NEW.tenant_id AND w.workspace_id = NEW.workspace_id);
	SELECT RAISE(ABORT, 'olivares: pending acceptance cannot carry verification evidence')
	WHERE NEW.state = 'pending' AND (NEW.evidence_ref IS NOT NULL OR NEW.evidence_hash IS NOT NULL
		OR NEW.verified_by_kind IS NOT NULL OR NEW.verified_by_ref IS NOT NULL
		OR NEW.verified_at IS NOT NULL OR NEW.waiver_decision_id IS NOT NULL);
	SELECT RAISE(ABORT, 'olivares: evaluated acceptance requires verifier and time')
	WHERE NEW.state <> 'pending' AND (NEW.verified_by_kind NOT IN ('user','agent','session','system')
		OR NEW.verified_by_ref IS NULL OR length(CAST(NEW.verified_by_ref AS BLOB)) NOT BETWEEN 1 AND 512
		OR NEW.verified_at IS NULL);
	SELECT RAISE(ABORT, 'olivares: acceptance evidence does not match its state')
	WHERE (NEW.state IN ('passed','failed') AND (NEW.evidence_ref IS NULL
		OR length(CAST(NEW.evidence_ref AS BLOB)) NOT BETWEEN 1 AND 512))
		OR (NEW.state = 'passed' AND NEW.evidence_hash IS NULL)
		OR (NEW.state <> 'waived' AND NEW.waiver_decision_id IS NOT NULL);
	SELECT RAISE(ABORT, 'olivares: waived acceptance requires the effective decision head')
	WHERE NEW.state = 'waived' AND (NEW.waiver_decision_id IS NULL OR NOT EXISTS (
		SELECT 1 FROM sessions_work_decision_head h WHERE h.tenant_id = NEW.tenant_id
			AND h.workspace_id = NEW.workspace_id AND h.work_item_id = NEW.work_item_id
			AND h.current_decision_id = NEW.waiver_decision_id AND h.state = 'effective'));
END;
