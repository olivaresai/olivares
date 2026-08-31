-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- PostgreSQL 15 has no CREATE TRIGGER IF NOT EXISTS. The module migration is
-- transactional and versioned, so one DO statement installs the complete K1
-- trigger set atomically and the boot self-test verifies every named object.
DO $do$
BEGIN
	CREATE TRIGGER sessions_work_item_state_guard
		BEFORE INSERT OR UPDATE ON sessions_work_item
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_validate();
	CREATE TRIGGER sessions_work_dependency_guard
		BEFORE INSERT OR UPDATE ON sessions_work_dependency
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_validate();
	CREATE TRIGGER sessions_work_acceptance_guard
		BEFORE INSERT OR UPDATE ON sessions_work_acceptance
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_validate();
	CREATE TRIGGER sessions_work_decision_guard
		BEFORE INSERT OR UPDATE ON sessions_work_decision
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_validate();
	CREATE TRIGGER sessions_work_decision_head_guard
		BEFORE INSERT OR UPDATE ON sessions_work_decision_head
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_validate();
	CREATE TRIGGER sessions_work_command_guard
		BEFORE INSERT OR UPDATE ON sessions_work_command
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_validate();
	CREATE TRIGGER sessions_work_event_guard
		BEFORE INSERT OR UPDATE ON sessions_work_event
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_validate();
	CREATE TRIGGER sessions_work_outbox_guard
		BEFORE INSERT OR UPDATE ON sessions_work_outbox
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_validate();
	CREATE TRIGGER sessions_work_guard_guard
		BEFORE INSERT OR UPDATE ON sessions_work_guard
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_validate();
END
$do$;
