-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
--
-- unit H — the cross-version writer fence, SQLite side: a subscription may not be CREATED by a
-- binary that cannot prove it consults the egress destination control.
--
-- WHY SQLITE GETS THE FENCE AT ALL. The single-node deployment is the one an operator runs on a box
-- they upgrade by hand, which is precisely where two binaries against one database is most likely,
-- not least. Shipping the fence on PostgreSQL only would leave the estate with the least ceremony
-- unguarded, and would make the guarantee depend on which engine was chosen — the shape of promise
-- this project does not make.
--
-- WHAT THE ENGINE FORCES, MEASURED RATHER THAN ASSUMED. The PostgreSQL side is one plpgsql function
-- called by five triggers. SQLite has no stored functions, no control flow inside a trigger body, no
-- GET DIAGNOSTICS and no row-count, so the same rule is expressed as a fixed sequence of statements:
-- each refusal is `SELECT RAISE(ABORT, ...) WHERE <the condition that makes it wrong>`, and the
-- consumption is the DELETE that follows. There is no `RETURN NEW`, so the dormant case cannot be an
-- early exit: the WHEN clause carries it instead, and the body only ever runs on a deployment that
-- is armed or unclassified.
--
--   * NO TRANSACTION ID. PostgreSQL's first design bound a proof to pg_current_xact_id(). SQLite has
--     no equivalent, and the row-bound nonce this file requires is what replaced it on BOTH engines —
--     one mechanism to get right instead of two. The measurement that forced it was here: a
--     COMMITTED, unconsumed attestation authorized an old binary's next write forever when the proof
--     only had to EXIST. Bound to the row, an orphan carries a nonce no row ever received.
--   * NO `FOR SHARE`. The arming race the PostgreSQL function closes with a shared lock on the
--     control row is closed differently here, and the reasoning has to be stated precisely because a
--     looser version of it was wrong. SetMaxOpenConns(1) caps ONE POOL, not the database file: two
--     processes against the same file are two pools, and this unit's own tests open a second one.
--     What actually serializes those is SQLite itself — one writer at a time per database. The test
--     named below pins the single-pool property (a write and an arming issued through the same store
--     cannot overlap), which is the configuration the first-party binary runs; the two-process case
--     rests on SQLite's own write serialization and is NOT measured here.
--   * `IS NOT` is the null-safe comparison; it is what `IS DISTINCT FROM` is on PostgreSQL.
--
-- THE TRIGGER NAME IS LOAD-BEARING. The engine's boot self-test decides which tables carry a tenant
-- guard by looking for triggers whose name ENDS IN `_scope_ins`
-- (core/internal/store/dialect/sqlite.go, GuardedTables). A module trigger named that way would
-- silently FORGE a tenant guard for its table. Every trigger this unit installs ends in
-- `_writer_fence_<event>` for that reason. It is a finding about the engine, not about this unit,
-- and it is reported as such.
--
-- THE COLUMN NAMES ARE A SECOND COPY of the entity descriptor's, which SQL cannot type-check against
-- the Go constants. The mitigation is that the tests apply this file for real against the real
-- schema: on the PostgreSQL side a first draft named `sink_cred` instead of `sink_cred_sealed` and
-- the engine refused the migration at open — loud, at boot, before any row was written.
CREATE TRIGGER IF NOT EXISTS eventing_subscription_writer_fence_ins
BEFORE INSERT ON eventing_subscription
FOR EACH ROW
WHEN NOT EXISTS (
	SELECT 1 FROM control_rollout_state
	WHERE control_key = 'eventing.egress.writer_fence.v1'
	  AND current_mode <> 'enforced')
BEGIN
	-- A missing row is not "dormant": it is unknown, and the honest answer to a mutation whose
	-- admissibility cannot be established is to refuse it. A release cannot produce this state — the
	-- migration that installs this trigger and the declaration that classifies the control ship in
	-- the same module version — so it is a corruption detector for a hand-edited database.
	SELECT RAISE(ABORT, 'olivares: eventing egress writer fence: this deployment has no classification for eventing.egress.writer_fence.v1, so it cannot be established whether an un-upgraded writer may author a destination')
	WHERE NOT EXISTS (
		SELECT 1 FROM control_rollout_state
		WHERE control_key = 'eventing.egress.writer_fence.v1');

	-- An old binary omits the column entirely: the engine's generic writer emits only the fields of
	-- ITS descriptor, and this one does not exist in a pre-fence release.
	SELECT RAISE(ABORT, 'olivares: eventing egress writer fence: this write carries no capability attestation, so the binary that made it does not consult the egress destination control (required capability 1); every node that authors event subscriptions must run a binary carrying the egress gate')
	WHERE NEW.writer_nonce IS NULL OR NEW.writer_nonce = '';

	-- The proof must be LIVE, its own tenant's, at least the required capability level, and made
	-- against the generation the writer observed. The generation comparison is what makes it mean
	-- "this writer read the current disposition" rather than "code able to write an attestation ran":
	-- a node holding a cached read attests a stale generation, is refused, and retries.
	--
	-- The tenant predicate is explicit here because SQLite has no row-level security and the scope
	-- pin is empty on the System path. On PostgreSQL the same predicate is carried too, so the
	-- isolation of a proof is a property of the RULE on both engines rather than of how the roles
	-- happen to be configured.
	SELECT RAISE(ABORT, 'olivares: eventing egress writer fence: no live capability attestation matches this write (required capability 1); the attestation is spent, was written for another generation or another tenant, or the nonce was preserved from an earlier row by a binary that does not carry the egress gate')
	WHERE NOT EXISTS (
		SELECT 1 FROM eventing_writer_attest
		WHERE nonce = NEW.writer_nonce
		  AND tenant_id = NEW.tenant_id
		  AND capability >= 1
		  AND fence_generation = (
			SELECT generation FROM control_rollout_state
			WHERE control_key = 'eventing.egress.writer_fence.v1'));

	-- Consume it. A proof that survives its mutation is a proof a second writer can use, and the
	-- EXISTS above would accept it again. The check and the consumption are two statements rather
	-- than PostgreSQL's one DELETE + ROW_COUNT because a SQLite trigger body has no diagnostics; they
	-- cannot drift apart under concurrency because this store writes through a single connection.
	DELETE FROM eventing_writer_attest
	WHERE nonce = NEW.writer_nonce
	  AND tenant_id = NEW.tenant_id
	  AND capability >= 1
	  AND fence_generation = (
		SELECT generation FROM control_rollout_state
		WHERE control_key = 'eventing.egress.writer_fence.v1');
END
