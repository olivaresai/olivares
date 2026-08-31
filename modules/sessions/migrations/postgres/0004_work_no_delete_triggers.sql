-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
DO $do$
BEGIN
	CREATE TRIGGER sessions_work_item_no_delete
		BEFORE DELETE ON sessions_work_item
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_no_delete();
	CREATE TRIGGER sessions_work_dependency_no_delete
		BEFORE DELETE ON sessions_work_dependency
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_no_delete();
	CREATE TRIGGER sessions_work_acceptance_no_delete
		BEFORE DELETE ON sessions_work_acceptance
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_no_delete();
	CREATE TRIGGER sessions_work_decision_head_no_delete
		BEFORE DELETE ON sessions_work_decision_head
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_no_delete();
	CREATE TRIGGER sessions_work_outbox_no_delete
		BEFORE DELETE ON sessions_work_outbox
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_no_delete();
	CREATE TRIGGER sessions_work_guard_no_delete
		BEFORE DELETE ON sessions_work_guard
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_no_delete();
END;
$do$;
