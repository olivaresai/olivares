-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
DO $do$
BEGIN
	CREATE TRIGGER sessions_channel_no_delete BEFORE DELETE ON sessions_channel
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_no_delete();
	CREATE TRIGGER sessions_channel_grant_no_delete BEFORE DELETE ON sessions_channel_grant
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_no_delete();
	CREATE TRIGGER sessions_channel_subscription_no_delete BEFORE DELETE ON sessions_channel_subscription
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_no_delete();
	CREATE TRIGGER sessions_channel_label_definition_no_delete BEFORE DELETE ON sessions_channel_label_definition
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_no_delete();
	CREATE TRIGGER sessions_channel_route_no_delete BEFORE DELETE ON sessions_channel_route
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_no_delete();
	CREATE TRIGGER sessions_communication_endpoint_no_delete BEFORE DELETE ON sessions_communication_endpoint
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_no_delete();
	CREATE TRIGGER sessions_message_no_delete BEFORE DELETE ON sessions_message
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_no_delete();
	CREATE TRIGGER sessions_message_delivery_no_delete BEFORE DELETE ON sessions_message_delivery
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_no_delete();
	CREATE TRIGGER sessions_inbox_cursor_no_delete BEFORE DELETE ON sessions_inbox_cursor
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_no_delete();
	CREATE TRIGGER sessions_inbox_cursor_barrier_no_delete BEFORE DELETE ON sessions_inbox_cursor_barrier
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_no_delete();
	CREATE TRIGGER sessions_communication_guard_no_delete BEFORE DELETE ON sessions_communication_guard
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_no_delete();
	CREATE TRIGGER sessions_decision_request_no_delete BEFORE DELETE ON sessions_decision_request
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_no_delete();
	CREATE TRIGGER sessions_work_handoff_no_delete BEFORE DELETE ON sessions_work_handoff
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_no_delete();
	CREATE TRIGGER sessions_delivery_dispatch_no_delete BEFORE DELETE ON sessions_delivery_dispatch
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_no_delete();
	CREATE TRIGGER sessions_delivery_attempt_no_delete BEFORE DELETE ON sessions_delivery_attempt
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_no_delete();
END
$do$;
