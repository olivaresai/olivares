-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
--
-- unit H — the cross-version writer fence, PostgreSQL side.
--
-- WHAT IT CLOSES. Unit G made the egress destination control's disposition durable and left one
-- limit declared as UNVERIFIED: nothing proved that every node able to author a destination carries
-- the gate. A binary that predates the gate does not consult the classification at all, so it can
-- introduce or re-point a destination that passed no policy — and the actuation ceremony asked the
-- operator to ASSERT the fleet was upgraded and merely recorded the assertion. This function is
-- what makes a violation fail instead of being asserted away.
--
-- WHAT IT DOES NOT CLAIM. It does not verify the fleet's composition and it does not verify the
-- past. It makes a future violation enforceable and observable.
--
-- HOW THE PROOF WORKS. A writer that carries the gate writes an attestation row and stamps the same
-- nonce on the row it is about to write, in ONE transaction. This function requires the two to
-- match and CONSUMES the attestation. Three properties follow, and the third is why the nonce is on
-- the row rather than only in a side table:
--
--   * an old binary omits the column entirely on INSERT — the engine's generic writer emits only
--     the fields of ITS descriptor, and the column does not exist in an older one;
--   * an old binary preserves the stored value on UPDATE, and the attestation that created that
--     value was already consumed, so the match fails;
--   * an orphaned attestation carries a nonce no row ever received. Only a collision on 32 bytes of
--     crypto/rand could bind it to a future mutation.
--
-- An earlier design used an attestation that merely EXISTED for the current transaction id. It
-- survives every defeat attempt on PostgreSQL, but on SQLite a committed orphan authorized an old
-- write forever, and consuming it on use still authorized ONE. The target was zero, so both engines
-- use the row-bound nonce and there is one mechanism to get right instead of two.
--
-- THE PRIVILEGE THIS IMPLIES, stated because it was measured rather than predicted: the function is
-- SECURITY INVOKER and takes FOR SHARE on the control row, and PostgreSQL charges UPDATE privilege
-- for a locking SELECT — not SELECT. So every role that writes a governed table must hold SELECT AND
-- UPDATE on control_rollout_state. The documented posture already does (the owner grants the
-- application role SELECT, INSERT, UPDATE, DELETE); a narrower hand-built grant fails closed with
-- SQLSTATE 42501, which is the right direction and an opaque error.
--
-- THE ARMING RACE, and why FOR SHARE. Without a lock, a writer whose transaction started while the
-- fence was dormant can commit AFTER the arming commits: its trigger read the old requirement. The
-- shared lock on the control row makes the arming wait for every in-flight writer, so when the
-- arming returns, no pre-arm write can still land. The measurements that preceded this function
-- proved sequential statements; this is the concurrent boundary they did not.
--
-- LOGICAL REPLICATION. This is a NORMAL trigger, deliberately not ENABLE ALWAYS. A subscriber
-- applying replicated rows carries the nonce column but has no attestation for its own apply
-- transaction, so an ALWAYS trigger would reject the replicated row and break replication of every
-- governed table. A subscriber applying changes is not an authoring writer. What that costs is
-- stated rather than hidden: a subscriber does not enforce the fence, so if one is ever PROMOTED to
-- writer, the promotion ceremony has to verify the fence itself.
CREATE OR REPLACE FUNCTION olivares_eventing_writer_fence()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
DECLARE
	fence_mode text;
	fence_gen bigint;
	nonce_in text;
	consumed int;
BEGIN
	-- The requirement, under a SHARED lock so an arming cannot slip past an in-flight writer.
	-- A missing row means this deployment was never classified for the fence, which is not
	-- "dormant": it is unknown, and the honest answer to a mutation is to refuse it.
	SELECT current_mode, generation INTO fence_mode, fence_gen
	FROM control_rollout_state
	WHERE control_key = 'eventing.egress.writer_fence.v1'
	FOR SHARE;

	IF fence_mode IS NULL THEN
		RAISE EXCEPTION 'olivares: eventing egress writer fence: this deployment has no classification for %, so it cannot be established whether an un-upgraded writer may author a destination',
			'eventing.egress.writer_fence.v1'
			USING ERRCODE = 'OL441', HINT = 'the engine classifies this on boot; a missing row means the state was lost';
	END IF;

	-- Only an ARMED fence demands anything. A deployment whose fleet predates the fence is
	-- classified dormant on purpose: arming it before its nodes are replaced would fail the
	-- authoring of a leader the operator has not touched yet.
	IF fence_mode <> 'enforced' THEN
		RETURN NEW;
	END IF;

	nonce_in := NEW.writer_nonce;
	IF nonce_in IS NULL OR nonce_in = '' THEN
		RAISE EXCEPTION 'olivares: eventing egress writer fence: this write carries no capability attestation, so the binary that made it does not consult the egress destination control (required capability %, fence generation %)',
			1, fence_gen
			USING ERRCODE = 'OL441',
			      HINT = 'every node that authors event subscriptions must run a binary carrying the egress gate; disarm the fence deliberately before rolling back to one that does not';
	END IF;

	-- Consume the proof. The DELETE is the check: it matches the nonce, the tenant, the capability
	-- level this migration requires, and the generation the writer OBSERVED. The generation
	-- comparison is what makes the proof mean "the writer read the current disposition" rather than
	-- merely "code able to write an attestation ran" — a node holding a cached read attests a stale
	-- generation, is refused, and retries.
	--
	-- The TENANT predicate is explicit rather than left to row-level security. RLS does isolate this
	-- in the documented posture — the application role is NOBYPASSRLS and the tables FORCE RLS — but
	-- that makes the guarantee a property of how the roles were configured, and a BYPASSRLS
	-- connection would quietly lose it. Written here, one tenant's proof cannot authorize another's
	-- mutation because the RULE says so, on this engine and on SQLite, which has no RLS at all and
	-- whose scope pin is empty outside a scoped transaction. RLS remains as the second, independent
	-- defence; it is no longer the only one.
	DELETE FROM eventing_writer_attest
	WHERE nonce = nonce_in
	  AND tenant_id = NEW.tenant_id
	  AND capability >= 1
	  AND fence_generation = fence_gen;
	GET DIAGNOSTICS consumed = ROW_COUNT;

	IF consumed = 0 THEN
		RAISE EXCEPTION 'olivares: eventing egress writer fence: no live capability attestation matches this write (required capability %, fence generation %)',
			1, fence_gen
			USING ERRCODE = 'OL441',
			      HINT = 'the attestation is spent, was written for another generation, or the nonce was preserved from an earlier row by a binary that does not carry the egress gate';
	END IF;

	RETURN NEW;
END
$fn$;
