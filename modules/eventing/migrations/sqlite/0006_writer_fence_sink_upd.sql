-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
--
-- unit H — a sink profile's effective destination may not MOVE without a proof. SQLite side;
-- the rule and the messages are the subscription INSERT trigger's (0003).
--
-- Kind, format, options and the sealed credential all feed the renderer, so any of them changing
-- changes where the bytes go — INCLUDING a change to the empty kind, which is how a binary carrying
-- the gate drops a profile (see 0007: the physical DELETE of a live profile is refused, because a
-- fence on INSERT and UPDATE would never have seen it). The display hint does not feed the renderer,
-- so a hint-only update stays free.
--
-- `IS NOT` is the null-safe comparison, and every nullable column is wrapped in COALESCE(..., '')
-- so this trigger and the Go writer agree BY CONSTRUCTION. SQL treats NULL as different from '',
-- while the module's Record.String reads a NULL column as "" — and that gap is not theoretical: on
-- the subscription table it made a plain "disable" fire the fence while the writer, seeing "" on
-- both sides, stamped nothing. Documenting the asymmetry was not enough; normalising it is. See
-- upsertSinkRow in sink.go, which carries the other half of this note.
CREATE TRIGGER IF NOT EXISTS eventing_subscription_sink_writer_fence_upd
BEFORE UPDATE ON eventing_subscription_sink
FOR EACH ROW
WHEN (COALESCE(OLD.sink_kind, '') IS NOT COALESCE(NEW.sink_kind, '')
	OR COALESCE(OLD.sink_format, '') IS NOT COALESCE(NEW.sink_format, '')
	OR COALESCE(OLD.sink_opts, '') IS NOT COALESCE(NEW.sink_opts, '')
	OR COALESCE(OLD.sink_cred_sealed, '') IS NOT COALESCE(NEW.sink_cred_sealed, ''))
 AND NOT EXISTS (
	SELECT 1 FROM control_rollout_state
	WHERE control_key = 'eventing.egress.writer_fence.v1'
	  AND current_mode <> 'enforced')
BEGIN
	SELECT RAISE(ABORT, 'olivares: eventing egress writer fence: this deployment has no classification for eventing.egress.writer_fence.v1, so it cannot be established whether an un-upgraded writer may move a destination')
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
