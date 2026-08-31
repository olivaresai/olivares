-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
DO $do$
BEGIN
	CREATE UNIQUE INDEX sessions_inbox_cursor_barrier_active_uniq
		ON sessions_inbox_cursor_barrier
		(tenant_id, workspace_id, reader_kind, reader_ref, mailbox_kind, mailbox_ref,
			filter_hash, delivery_id) NULLS NOT DISTINCT
		WHERE state = 'active';
	CREATE UNIQUE INDEX sessions_communication_endpoint_active_uniq
		ON sessions_communication_endpoint (tenant_id, provider_key, endpoint_ref)
		WHERE state = 'active';
	CREATE UNIQUE INDEX sessions_decision_request_active_uniq
		ON sessions_decision_request (tenant_id, work_item_id, decision_key)
		WHERE state NOT IN ('resolved','rejected','canceled','expired');
	CREATE UNIQUE INDEX sessions_work_handoff_offered_uniq
		ON sessions_work_handoff (tenant_id, work_item_id)
		WHERE state = 'offered';
	CREATE TRIGGER sessions_channel_guard
		BEFORE INSERT OR UPDATE ON sessions_channel
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_channel_grant_guard
		BEFORE INSERT OR UPDATE ON sessions_channel_grant
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_channel_subscription_guard
		BEFORE INSERT OR UPDATE ON sessions_channel_subscription
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_channel_label_definition_guard
		BEFORE INSERT OR UPDATE ON sessions_channel_label_definition
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_channel_route_guard
		BEFORE INSERT OR UPDATE ON sessions_channel_route
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_communication_endpoint_guard
		BEFORE INSERT OR UPDATE ON sessions_communication_endpoint
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_message_guard
		BEFORE INSERT OR UPDATE ON sessions_message
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_message_audience_guard
		BEFORE INSERT OR UPDATE ON sessions_message_audience
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_message_audience_recipient_guard
		BEFORE INSERT OR UPDATE ON sessions_message_audience_recipient
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_message_delivery_guard
		BEFORE INSERT OR UPDATE ON sessions_message_delivery
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_inbox_cursor_guard
		BEFORE INSERT OR UPDATE ON sessions_inbox_cursor
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_inbox_cursor_barrier_guard
		BEFORE INSERT OR UPDATE ON sessions_inbox_cursor_barrier
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_message_ack_guard
		BEFORE INSERT OR UPDATE ON sessions_message_ack
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_communication_guard_guard
		BEFORE INSERT OR UPDATE ON sessions_communication_guard
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_decision_request_guard
		BEFORE INSERT OR UPDATE ON sessions_decision_request
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_decision_response_guard
		BEFORE INSERT OR UPDATE ON sessions_decision_response
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_work_handoff_guard
		BEFORE INSERT OR UPDATE ON sessions_work_handoff
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_delivery_dispatch_guard
		BEFORE INSERT OR UPDATE ON sessions_delivery_dispatch
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_delivery_attempt_guard
		BEFORE INSERT OR UPDATE ON sessions_delivery_attempt
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
	CREATE TRIGGER sessions_communication_command_guard
		BEFORE INSERT OR UPDATE ON sessions_communication_command
		FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_validate();
END
$do$;
