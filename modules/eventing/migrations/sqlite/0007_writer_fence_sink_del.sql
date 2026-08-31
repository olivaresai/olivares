-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
--
-- unit H — the DELETE half of the writer fence. SQLite side; the PostgreSQL counterpart is
-- migrations/postgres/0004_writer_fence_delete_fn.sql, which carries the full reasoning.
--
-- Deleting a sink profile while its subscription SURVIVES re-points the destination: the delivery
-- path reads an absent profile as the generic-webhook case, so the bytes move from the URL the
-- profile rendered to the base endpoint. A fence on INSERT and UPDATE never sees that step, which
-- left exactly one mutation able to move a live destination silently. An adversarial review of this
-- unit's design found it.
--
-- A DELETE has no NEW row, so it cannot carry a nonce, and it does not need to: the module no longer
-- performs this mutation at all — dropping a profile is an UPDATE to the empty profile, which IS
-- proof-carrying — so the only legitimate physical delete is the one that accompanies the
-- subscription's own deletion. The ordering that relies on is verified rather than assumed: the
-- delete handler removes the subscription row first and the subscription is HARD-deleted (its
-- descriptor declares no soft delete), so the parent is genuinely absent by the time this fires.
CREATE TRIGGER IF NOT EXISTS eventing_subscription_sink_writer_fence_del
BEFORE DELETE ON eventing_subscription_sink
FOR EACH ROW
WHEN NOT EXISTS (
	SELECT 1 FROM control_rollout_state
	WHERE control_key = 'eventing.egress.writer_fence.v1'
	  AND current_mode <> 'enforced')
BEGIN
	SELECT RAISE(ABORT, 'olivares: eventing egress writer fence: this deployment has no classification for eventing.egress.writer_fence.v1, so a sink profile deletion cannot be judged')
	WHERE NOT EXISTS (
		SELECT 1 FROM control_rollout_state
		WHERE control_key = 'eventing.egress.writer_fence.v1');

	SELECT RAISE(ABORT, 'olivares: eventing egress writer fence: deleting the sink profile of a LIVE subscription re-points its destination to the base endpoint, which a binary carrying the egress gate performs as an update to the empty profile instead; clear the profile with an update, or delete the subscription itself')
	WHERE EXISTS (
		SELECT 1 FROM eventing_subscription
		WHERE id = OLD.subscription_ref
		  AND tenant_id = OLD.tenant_id);
END
