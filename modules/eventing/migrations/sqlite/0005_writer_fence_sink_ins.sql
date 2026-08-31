-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
--
-- unit H — a sink profile may not be CREATED without a proof. SQLite side; the rule and the
-- messages are the subscription INSERT trigger's (0003), which carries the reasoning.
--
-- The sink profile is a DESTINATION surface, not decoration: its kind, format, options and sealed
-- credential are what the delivery path renders the effective URL from, so introducing one moves
-- where the bytes go exactly as re-pointing the endpoint does.
CREATE TRIGGER IF NOT EXISTS eventing_subscription_sink_writer_fence_ins
BEFORE INSERT ON eventing_subscription_sink
FOR EACH ROW
WHEN NOT EXISTS (
	SELECT 1 FROM control_rollout_state
	WHERE control_key = 'eventing.egress.writer_fence.v1'
	  AND current_mode <> 'enforced')
BEGIN
	SELECT RAISE(ABORT, 'olivares: eventing egress writer fence: this deployment has no classification for eventing.egress.writer_fence.v1, so it cannot be established whether an un-upgraded writer may author a destination')
	WHERE NOT EXISTS (
		SELECT 1 FROM control_rollout_state
		WHERE control_key = 'eventing.egress.writer_fence.v1');

	SELECT RAISE(ABORT, 'olivares: eventing egress writer fence: this write carries no capability attestation, so the binary that made it does not consult the egress destination control (required capability 1); every node that authors event subscriptions must run a binary carrying the egress gate')
	WHERE NEW.writer_nonce IS NULL OR NEW.writer_nonce = '';

	SELECT RAISE(ABORT, 'olivares: eventing egress writer fence: no live capability attestation matches this write (required capability 1); the attestation is spent, was written for another generation or another tenant, or the nonce was preserved from an earlier row by a binary that does not carry the egress gate')
	WHERE NOT EXISTS (
		SELECT 1 FROM eventing_writer_attest
		WHERE nonce = NEW.writer_nonce
		  AND tenant_id = NEW.tenant_id
		  AND capability >= 1
		  AND fence_generation = (
			SELECT generation FROM control_rollout_state
			WHERE control_key = 'eventing.egress.writer_fence.v1'));

	DELETE FROM eventing_writer_attest
	WHERE nonce = NEW.writer_nonce
	  AND tenant_id = NEW.tenant_id
	  AND capability >= 1
	  AND fence_generation = (
		SELECT generation FROM control_rollout_state
		WHERE control_key = 'eventing.egress.writer_fence.v1');
END
