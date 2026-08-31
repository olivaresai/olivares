-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
--
-- unit H — attach the fence to the governed surface, and to nothing else.
--
-- WHAT IS GOVERNED, and why the cut is exactly here. Only a mutation that can introduce or move a
-- DESTINATION is fenced: the subscription's endpoint and the sink profile that decides the rendered
-- URL. Everything else is deliberately outside.
--
--   * The DELIVERY RAIL is not fenced. During a rolling update an un-replaced node keeps delivering,
--     and its rail writes events, deliveries and cursors. Fencing those would break the evidence
--     flow of every estate on every upgrade, which is a far worse failure than the one being closed.
--   * A rename is not fenced, and neither is DISABLING. The engine's generic writer puts every
--     descriptor field in the SET, so fencing any UPDATE would block a pre-existing subscription
--     from being disabled by a node the operator has not replaced — and unit G preserves on purpose
--     that such a subscription stays editable, INCLUDING to disable it, which is what an operator
--     does in an incident. Hence the WHEN clauses below compare the effective destination rather
--     than trusting the statement's shape.
--
-- THE RULE, in one line, after an adversarial review of the implementation found the asymmetry
-- below: THE FENCE NEVER BLOCKS TURNING EGRESS OFF; IT GOVERNS TURNING IT ON. Disabling, deleting
-- and cleanup stay free because they only ever reduce what leaves this deployment. Everything that
-- makes a destination effective — creating it, moving it, and REACTIVATING it — carries a proof.
--
-- Reactivation was missed by the first version, which compared the endpoint alone. An old binary
-- could flip enabled false→true on a subscription it had disabled, resuming delivery, with no proof
-- at all. What that is NOT is an escape from the destination policy: the delivery path re-checks the
-- destination authoritatively on the URL it is about to dial (dispatch.go), added precisely so a
-- policy authored later cannot grandfather a subscription forever. So this is defence in depth
-- rather than a plugged leak — and it is still the right cut, because the fence's own promise is
-- that a writer without the gate cannot make a destination effective.
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
-- Each statement is idempotent by DROP-then-CREATE inside a DO block, because CREATE TRIGGER has no
-- IF NOT EXISTS in PostgreSQL 15 and a module migration is one statement per file.
--
-- The column names below are a SECOND COPY of the descriptor's, which SQL cannot type-check against
-- the Go constants. That is a real fragility of putting a rule in a migration, and the mitigation is
-- that the PostgreSQL test applies this file for real: the first draft of it named `sink_cred`
-- instead of `sink_cred_sealed` and the engine refused the migration at open, which is exactly the
-- failure mode one wants — loud, at boot, before any row is written.
DO $do$
BEGIN
	DROP TRIGGER IF EXISTS eventing_subscription_writer_fence_ins ON eventing_subscription;
	CREATE TRIGGER eventing_subscription_writer_fence_ins
		BEFORE INSERT ON eventing_subscription
		FOR EACH ROW EXECUTE FUNCTION olivares_eventing_writer_fence();

	-- When the destination MOVES, or when a dormant one is REACTIVATED. `UPDATE OF endpoint` would
	-- not do: the generic writer lists every column in the SET, so it fires even for
	-- endpoint = endpoint. The enabled clause is one-directional on purpose — false→true is
	-- governed, true→false is free.
	DROP TRIGGER IF EXISTS eventing_subscription_writer_fence_upd ON eventing_subscription;
	CREATE TRIGGER eventing_subscription_writer_fence_upd
		BEFORE UPDATE ON eventing_subscription
		FOR EACH ROW
		WHEN (OLD.endpoint IS DISTINCT FROM NEW.endpoint
			OR (OLD.enabled = false AND NEW.enabled = true)
			OR COALESCE(OLD.auth_type, '') IS DISTINCT FROM COALESCE(NEW.auth_type, '')
			OR COALESCE(OLD.auth_header_name, '') IS DISTINCT FROM COALESCE(NEW.auth_header_name, '')
			OR COALESCE(OLD.auth_value_sealed, '') IS DISTINCT FROM COALESCE(NEW.auth_value_sealed, ''))
		EXECUTE FUNCTION olivares_eventing_writer_fence();

	DROP TRIGGER IF EXISTS eventing_subscription_sink_writer_fence_ins ON eventing_subscription_sink;
	CREATE TRIGGER eventing_subscription_sink_writer_fence_ins
		BEFORE INSERT ON eventing_subscription_sink
		FOR EACH ROW EXECUTE FUNCTION olivares_eventing_writer_fence();

	-- kind, format, opts and the sealed credential all feed the renderer, so any of them changing
	-- changes where the bytes go. The display hint does not, and a hint-only update stays free.
	DROP TRIGGER IF EXISTS eventing_subscription_sink_writer_fence_upd ON eventing_subscription_sink;
	CREATE TRIGGER eventing_subscription_sink_writer_fence_upd
		BEFORE UPDATE ON eventing_subscription_sink
		FOR EACH ROW
		WHEN (COALESCE(OLD.sink_kind, '') IS DISTINCT FROM COALESCE(NEW.sink_kind, '')
			OR COALESCE(OLD.sink_format, '') IS DISTINCT FROM COALESCE(NEW.sink_format, '')
			OR COALESCE(OLD.sink_opts, '') IS DISTINCT FROM COALESCE(NEW.sink_opts, '')
			OR COALESCE(OLD.sink_cred_sealed, '') IS DISTINCT FROM COALESCE(NEW.sink_cred_sealed, ''))
		EXECUTE FUNCTION olivares_eventing_writer_fence();

	-- The DELETE half: refuse while the parent lives (that is a re-point), allow once it is gone
	-- (that is cleanup). A WHEN clause cannot carry the subquery this needs, so the check is inside
	-- the function.
	DROP TRIGGER IF EXISTS eventing_subscription_sink_writer_fence_del ON eventing_subscription_sink;
	CREATE TRIGGER eventing_subscription_sink_writer_fence_del
		BEFORE DELETE ON eventing_subscription_sink
		FOR EACH ROW EXECUTE FUNCTION olivares_eventing_writer_fence_delete();
END
$do$;
