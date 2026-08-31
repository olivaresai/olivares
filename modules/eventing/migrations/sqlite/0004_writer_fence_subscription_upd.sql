-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
--
-- unit H — a subscription's destination may not MOVE without a proof. SQLite side; the rule and
-- the messages are the INSERT trigger's (0003), which carries the reasoning.
--
-- WHY THE WHEN CLAUSE COMPARES VALUES rather than the statement naming a column: the engine's
-- generic writer puts EVERY descriptor field in the SET, so `UPDATE OF endpoint` would fire for
-- endpoint = endpoint and would block a pre-existing subscription from being renamed or DISABLED by
-- a node the operator has not replaced yet. Unit G preserves that editability on purpose —
-- disabling a subscription is what an operator does in an incident. `IS NOT` is the null-safe
-- comparison.
--
-- THE RULE, in one line: THE FENCE NEVER BLOCKS TURNING EGRESS OFF; IT GOVERNS TURNING IT ON.
-- Disabling stays free because it only reduces what leaves this deployment. Moving the endpoint and
-- REACTIVATING a dormant subscription both make a destination effective, so both carry a proof.
--
-- Reactivation was missed by the first version, which compared the endpoint alone: an old binary
-- could flip enabled 0→1 and resume delivery with no proof. `enabled` is INTEGER on this engine
-- (KindBool), hence 0/1 rather than false/true. What this is NOT is an escape from the destination
-- policy — the delivery path re-checks the destination authoritatively on the URL it is about to
-- dial (dispatch.go) — so it is defence in depth rather than a plugged leak.
--
-- THE AUTHENTICATION CREDENTIAL IS PART OF THE DESTINATION, and that is a decision this unit had to
-- make explicitly rather than inherit. The sink's sealed credential was already fenced because it
-- "changes where the bytes go"; the subscription's auth credential ends up in exactly the same place
-- — an Authorization or custom header on the same request — and on a multi-tenant SaaS collector it
-- is the token, not the URL, that selects the receiving workspace. Fencing one and not the other was
-- an inconsistency an adversarial review of the implementation named: the definition of
-- "destination" could not sustain both answers.
--
-- The cost is real and accepted: a credential ROTATION for the same receiver — the common,
-- innocent case — is indistinguishable from a switch to another receiver's token, so both are
-- governed. A node that carries the gate rotates freely; one that does not cannot, which is what
-- arming means.
--
--
-- COALESCE, ON EVERY NULLABLE COLUMN, and it is load-bearing rather than defensive. SQL's
-- IS DISTINCT FROM treats NULL as different from '', while the module's Record.String reads a NULL
-- column as "". Those two readings disagree exactly where a nullable column goes from unset to
-- empty — which the update path does on every request that omits an auth credential — and the
-- disagreement is not theoretical: it made a plain "disable" fire this trigger while the Go writer,
-- seeing "" both sides, stamped nothing. The write was refused with a fence error for a mutation
-- that moves no destination.
--
-- Normalising NULL to '' here makes the trigger agree with the Go writer BY CONSTRUCTION rather than
-- by both being written carefully, which is the only version of "two copies of a rule" this campaign
-- has any reason to trust.
-- An old binary that re-points the endpoint while preserving every column it read fails here for the
-- right reason: the nonce it preserved was CONSUMED by the mutation that created it, so no live
-- attestation matches.
CREATE TRIGGER IF NOT EXISTS eventing_subscription_writer_fence_upd
BEFORE UPDATE ON eventing_subscription
FOR EACH ROW
WHEN (OLD.endpoint IS NOT NEW.endpoint
	OR (OLD.enabled = 0 AND NEW.enabled = 1)
	OR COALESCE(OLD.auth_type, '') IS NOT COALESCE(NEW.auth_type, '')
	OR COALESCE(OLD.auth_header_name, '') IS NOT COALESCE(NEW.auth_header_name, '')
	OR COALESCE(OLD.auth_value_sealed, '') IS NOT COALESCE(NEW.auth_value_sealed, ''))
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
