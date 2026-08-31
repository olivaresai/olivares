-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
--
-- unit H — the DELETE half of the writer fence.
--
-- Deleting a sink profile while its subscription SURVIVES re-points the destination: the delivery
-- path reads an absent profile as the generic-webhook case, so the bytes move from the URL the
-- profile rendered to the base endpoint. A fence on INSERT and UPDATE never saw that step, which
-- left exactly one mutation able to move a live destination silently. An adversarial review of this
-- unit's design found it.
--
-- A DELETE has no NEW row, so it cannot carry a nonce. It does not need to: the module no longer
-- performs this mutation at all — dropping a profile is now an UPDATE to the empty profile, which
-- IS proof-carrying — so the only legitimate physical delete is the one that accompanies the
-- subscription's own deletion. This function encodes exactly that: refuse while the parent exists,
-- allow once it is gone.
--
-- The ordering it relies on is verified, not assumed: the delete handler removes the subscription
-- row and only then the sink row, and the subscription is HARD-deleted (its descriptor declares no
-- soft delete), so the parent is genuinely absent by the time this fires.
CREATE OR REPLACE FUNCTION olivares_eventing_writer_fence_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
DECLARE
	fence_mode text;
	parent_alive boolean;
BEGIN
	SELECT current_mode INTO fence_mode
	FROM control_rollout_state
	WHERE control_key = 'eventing.egress.writer_fence.v1'
	FOR SHARE;

	IF fence_mode IS NULL THEN
		RAISE EXCEPTION 'olivares: eventing egress writer fence: this deployment has no classification for %, so a sink profile deletion cannot be judged',
			'eventing.egress.writer_fence.v1'
			USING ERRCODE = 'OL441';
	END IF;

	IF fence_mode <> 'enforced' THEN
		RETURN OLD;
	END IF;

	SELECT EXISTS (SELECT 1 FROM eventing_subscription WHERE id = OLD.subscription_ref) INTO parent_alive;
	IF parent_alive THEN
		RAISE EXCEPTION 'olivares: eventing egress writer fence: deleting the sink profile of a LIVE subscription re-points its destination to the base endpoint, which a binary carrying the egress gate performs as an update to the empty profile instead'
			USING ERRCODE = 'OL441',
			      HINT = 'clear the profile with an update, or delete the subscription itself';
	END IF;

	RETURN OLD;
END
$fn$;
