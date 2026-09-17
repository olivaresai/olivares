-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- OT-V. Move only sessions_work_handoff to a versioned validator. One shared
-- function backs NINETEEN communication triggers, so its body cannot be edited
-- without invalidating all nineteen pinned digests at once, and the store does
-- not support replacing a shared function: a transition must move the trigger to
-- a freshly reserved identity (core/store/registry.go). The store reserves this
-- function identity and its exact ACL before this statement runs; CREATE OR
-- REPLACE preserves both the reserved OID and that ACL. The EIGHTEEN other
-- communication triggers deliberately remain attached to the immutable shared
-- validator, whose now-dead handoff branch is deliberately NOT deleted: deleting
-- it would change the digest they are all pinned to.
--
-- The counts are twenty/nineteen in the proposal and were nineteen/eighteen when
-- measured: migration 0018 had already moved the CommunicationCommand guard off
-- the shared validator before this lot. The number is not the invariant — the
-- enumerated identities are — but a comment that disagrees with the catalog is a
-- comment a later reader will trust. Captured on a real migrated database, fresh
-- and upgraded, in assessments/implementation/handoff-vacant-transfer-r2.
--
-- The body is the shared validator's own entity prologue, typed-reference loop
-- and handoff branch, verbatim except for the accepted-state lease-effect rule:
-- an accept that found an execution generation (offered >= 1) must still
-- invalidate it and advance past it, and an accept sealed against the item's one
-- vacant generation (offered 0/NULL) must NOT invent one. That is the exact SQL
-- twin of handoffAcceptFenceOK in communication_state.go and of the SQLite
-- 0096/0097 pair. The branches of the shared prologue that this table can never
-- reach (the append-only arm, the other tables) are replaced by an explicit
-- attachment self-check, exactly as migration 0018 did for the command guard.
--
-- No table, column, type, index or constraint change SURVIVES this migration, and
-- there is no data migration: nothing here repairs, rewrites or deletes a row.
--
-- THE PRE-CHECK BELOW REPLACES AN ASSUMPTION THAT WAS NOT SOUND, AND THE
-- DIFFERENCE IS THE WHOLE POINT. This header used to argue that no existing
-- accepted row could be in the newly forbidden shape because the pre-transition
-- public accept refuses a vacant lease. That is an argument about the HTTP path,
-- not about the durable relation's accepted-state domain: the pre-transition
-- guard (0012_communication_validate_function.sql) only required a non-null
-- resulting fence greater than COALESCE(offered,0), so an otherwise valid
-- accepted envelope with offered absent/zero and resulting 1 satisfied the old
-- rule and violates the new one. Installing a row trigger never revisits
-- existing rows, and the migration coordinator verifies catalog definitions,
-- reserved identities/ACLs, attachments and callers — not data rows. So the
-- refusal is PROVED here rather than assumed, exactly as SQLite 0096/0097 do.
--
-- How it is proved, and why it is not a SELECT. sessions_work_handoff carries
-- FORCE ROW LEVEL SECURITY whose policy reads current_setting('app.tenant_id')
-- without missing_ok, and this migration's own role is the table owner WITHOUT
-- BYPASSRLS. A plain SELECT here therefore either RAISES 42704 (no tenant bound)
-- or, once any earlier transaction on this connection has defined the GUC,
-- silently returns ZERO ROWS for every tenant — a pre-check that always passes.
-- Measured on PostgreSQL 16.15 before this was written. PostgreSQL validates a
-- newly added CHECK constraint with an internal full scan that row-level
-- security does not filter, so the rule is added as a constraint, validated by
-- the server across every tenant's rows, and then rolled back with the
-- subtransaction that added it: a violation aborts the migration with the
-- guard, the trigger and the prestate unchanged, and a clean estate continues
-- with the table's shape byte-identical to what it had. The table is held in
-- ACCESS EXCLUSIVE by the outer transaction first, so the observation stays true
-- until commit; the transition's own Before hook already fenced the functions
-- and locked the caller tables before this statement ran.
DO $migration$
DECLARE
	existing_violation boolean := false;
BEGIN
	LOCK TABLE ONLY public.sessions_work_handoff IN ACCESS EXCLUSIVE MODE;
	BEGIN
		EXECUTE $accepted_state_precheck$
ALTER TABLE public.sessions_work_handoff
	ADD CONSTRAINT sessions_work_handoff_vacant_transfer_precheck_v24
	CHECK (state <> 'accepted'
		OR COALESCE(offered_lease_fence,0) <> 0
		OR COALESCE(resulting_lease_fence,0) = 0)
$accepted_state_precheck$;
		-- Validation passed on every existing row. Abort THIS subtransaction so
		-- the constraint never becomes durable; the migration continues below.
		RAISE EXCEPTION 'olivares: OT-V accepted-state pre-check satisfied'
			USING ERRCODE = 'OLV24';
	EXCEPTION
		WHEN check_violation THEN
			existing_violation := true;
		WHEN SQLSTATE 'OLV24' THEN
			existing_violation := false;
	END;
	IF existing_violation THEN
		RAISE EXCEPTION 'olivares: an accepted Handoff already records a lease effect on a vacant offered generation'
			USING ERRCODE = '23514';
	END IF;
	EXECUTE $function_definition$
CREATE OR REPLACE FUNCTION public.olivares_sessions_work_handoff_validate_v24()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $handoff_guard$
DECLARE
	row_data jsonb;
	ref_pair record;
	ref_kind text;
	ref_value text;
BEGIN
	IF TG_TABLE_SCHEMA <> 'public'
		OR TG_TABLE_NAME <> 'sessions_work_handoff'
		OR TG_OP NOT IN ('INSERT', 'UPDATE') THEN
		RAISE EXCEPTION 'olivares: Handoff validator attached outside its exact table/operation'
			USING ERRCODE = '23514';
	END IF;

	IF NEW.id IS NULL
		OR NEW.id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR NEW.tenant_id IS NULL
		OR NEW.tenant_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR NEW.tenant_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff'
		OR NEW.workspace_id IS NULL
		OR NEW.workspace_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
		OR NEW.version < 1 OR NEW.created_at IS NULL THEN
		RAISE EXCEPTION 'olivares: invalid sessions communication entity identity'
			USING ERRCODE = '23514';
	END IF;

	IF NEW.updated_at IS NULL OR NEW.updated_at < NEW.created_at THEN
		RAISE EXCEPTION 'olivares: invalid mutable communication timestamps'
			USING ERRCODE = '23514';
	END IF;
	IF TG_OP = 'UPDATE' AND (
		NEW.id IS DISTINCT FROM OLD.id
		OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
		OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
		OR NEW.created_at IS DISTINCT FROM OLD.created_at
		OR NEW.version <> OLD.version + 1
		OR NEW.updated_at < OLD.updated_at
	) THEN
		RAISE EXCEPTION 'olivares: mutable communication identity, version or time changed illegally'
			USING ERRCODE = '23514';
	END IF;

	row_data := to_jsonb(NEW);
	FOR ref_pair IN
		SELECT * FROM (VALUES
			('subject_kind','subject_ref'), ('subscriber_kind','subscriber_ref'),
			('owner_kind','owner_ref'), ('sender_kind','sender_ref'),
			('selector_kind','selector_ref'), ('recipient_kind','recipient_ref'),
			('original_subscriber_kind','original_subscriber_ref'),
			('reader_kind','reader_ref'), ('actor_kind','actor_ref'),
			('on_behalf_of_kind','on_behalf_of_ref'), ('requester_kind','requester_ref'),
			('from_kind','from_ref'), ('to_kind','to_ref'),
			('granted_by_kind','granted_by_ref'), ('revoked_by_kind','revoked_by_ref'),
			('audience_kind','audience_ref')
		) AS pairs(kind_column, ref_column)
	LOOP
		IF row_data ? ref_pair.kind_column AND row_data ? ref_pair.ref_column THEN
			ref_kind := row_data ->> ref_pair.kind_column;
			ref_value := row_data ->> ref_pair.ref_column;
			IF ref_value IS NOT NULL AND (
				(ref_kind = 'session' AND ref_value !~
					'^osn_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
				OR (ref_kind IN ('user','user_group','agent','agent_group') AND ref_value !~
					'^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
				OR (ref_kind = 'system' AND
					(octet_length(ref_value) NOT BETWEEN 1 AND 512
						OR ref_value <> btrim(ref_value) OR ref_value ~ E'[\\r\\n]'))
			) THEN
				RAISE EXCEPTION 'olivares: non-canonical typed communication reference'
					USING ERRCODE = '23514';
			END IF;
		END IF;
	END LOOP;

	IF NEW.from_kind NOT IN ('user','agent','session')
		OR NEW.to_kind NOT IN ('user','agent','session')
		OR (NEW.from_kind = 'session'
			AND (length(NEW.from_ref) <> 40 OR left(NEW.from_ref, 4) <> 'osn_'))
		OR (NEW.from_kind <> 'session' AND length(NEW.from_ref) <> 36)
		OR (NEW.to_kind = 'session'
			AND (length(NEW.to_ref) <> 40 OR left(NEW.to_ref, 4) <> 'osn_'))
		OR (NEW.to_kind <> 'session' AND length(NEW.to_ref) <> 36)
		OR (NEW.from_kind,NEW.from_ref) = (NEW.to_kind,NEW.to_ref)
		OR NEW.from_owner_epoch < 1 OR COALESCE(NEW.offered_lease_fence,0) < 0
		OR NEW.context_event_seq < 1 OR octet_length(NEW.context_hash) <> 32
		OR NEW.handoff_encoding IS NULL
		OR NEW.handoff_encoding NOT IN ('plain_json','sealed_v1')
		OR NEW.handoff_schema IS DISTINCT FROM 'communication.handoff.v1'
		OR NEW.handoff_digest IS NULL OR octet_length(NEW.handoff_digest) <> 32
		OR NEW.handoff_protection_generation IS NULL OR NEW.handoff_protection_generation < 1
		OR (NEW.handoff_encoding = 'plain_json' AND
			(NEW.handoff_plain_json IS NULL OR NEW.handoff_sealed_json IS NOT NULL
				OR jsonb_typeof(NEW.handoff_plain_json::jsonb) <> 'object'
				OR octet_length(NEW.handoff_plain_json::text) > 65536
				OR NEW.handoff_seal_key_version IS NOT NULL
				OR NEW.handoff_digest_key_version IS NOT NULL))
		OR (NEW.handoff_encoding = 'sealed_v1' AND
			(NEW.handoff_plain_json IS NOT NULL OR NEW.handoff_sealed_json IS NULL
				OR jsonb_typeof(NEW.handoff_sealed_json::jsonb) <> 'object'
				OR octet_length(NEW.handoff_sealed_json::text) > 196608
				OR octet_length(NEW.handoff_seal_key_version) NOT BETWEEN 1 AND 512
				OR octet_length(NEW.handoff_digest_key_version) NOT BETWEEN 1 AND 512
				OR NEW.handoff_seal_key_version IS NULL
				OR NEW.handoff_digest_key_version IS NULL
				OR NEW.handoff_seal_key_version <> btrim(NEW.handoff_seal_key_version)
				OR NEW.handoff_digest_key_version <> btrim(NEW.handoff_digest_key_version)
				OR NEW.handoff_seal_key_version ~ E'[\\r\\n]'
				OR NEW.handoff_digest_key_version ~ E'[\\r\\n]'
				OR jsonb_typeof(NEW.handoff_sealed_json::jsonb -> 'ciphertext')
					IS DISTINCT FROM 'string'
				OR COALESCE(length(NEW.handoff_sealed_json::jsonb ->> 'ciphertext'), 0) = 0
				OR jsonb_typeof(NEW.handoff_sealed_json::jsonb -> 'key_version')
					IS DISTINCT FROM 'string'
				OR NEW.handoff_sealed_json::jsonb ->> 'key_version' IS DISTINCT FROM
					NEW.handoff_seal_key_version))
		OR NEW.state NOT IN ('offered','accepted','rejected','withdrawn','expired')
		OR NEW.ack_deadline <= NEW.created_at THEN
		RAISE EXCEPTION 'olivares: invalid Handoff envelope' USING ERRCODE = '23514';
	END IF;
	IF NEW.terminal_reason_encoding IS NULL AND (
		NEW.terminal_reason_plain_json IS NOT NULL
		OR NEW.terminal_reason_sealed_json IS NOT NULL
		OR NEW.terminal_reason_schema IS NOT NULL OR NEW.terminal_reason_digest IS NOT NULL
		OR NEW.terminal_reason_seal_key_version IS NOT NULL
		OR NEW.terminal_reason_digest_key_version IS NOT NULL
		OR NEW.terminal_reason_protection_generation IS NOT NULL) THEN
		RAISE EXCEPTION 'olivares: invalid Handoff terminal ProtectedPayload'
			USING ERRCODE = '23514';
	END IF;
	IF NEW.terminal_reason_encoding IS NOT NULL AND NOT COALESCE((
		(NEW.terminal_reason_encoding IS NULL
			AND NEW.terminal_reason_plain_json IS NULL
			AND NEW.terminal_reason_sealed_json IS NULL
			AND NEW.terminal_reason_schema IS NULL
			AND NEW.terminal_reason_digest IS NULL
			AND NEW.terminal_reason_seal_key_version IS NULL
			AND NEW.terminal_reason_digest_key_version IS NULL
			AND NEW.terminal_reason_protection_generation IS NULL)
		OR (
			NEW.terminal_reason_encoding IN ('plain_json','sealed_v1')
			AND NEW.terminal_reason_encoding = NEW.handoff_encoding
			AND NEW.terminal_reason_schema = 'communication.handoff-terminal-reason.v1'
			AND octet_length(NEW.terminal_reason_digest) = 32
			AND NEW.terminal_reason_protection_generation =
				NEW.handoff_protection_generation
			AND (
				(NEW.terminal_reason_encoding = 'plain_json'
					AND NEW.terminal_reason_plain_json IS NOT NULL
					AND jsonb_typeof(NEW.terminal_reason_plain_json::jsonb) = 'object'
					AND octet_length(NEW.terminal_reason_plain_json::text) <= 65536
					AND NEW.terminal_reason_sealed_json IS NULL
					AND NEW.terminal_reason_seal_key_version IS NULL
					AND NEW.terminal_reason_digest_key_version IS NULL)
				OR (NEW.terminal_reason_encoding = 'sealed_v1'
					AND NEW.terminal_reason_plain_json IS NULL
					AND NEW.terminal_reason_sealed_json IS NOT NULL
					AND jsonb_typeof(NEW.terminal_reason_sealed_json::jsonb) = 'object'
					AND octet_length(NEW.terminal_reason_sealed_json::text) <= 196608
					AND octet_length(NEW.terminal_reason_seal_key_version)
						BETWEEN 1 AND 512
					AND octet_length(NEW.terminal_reason_digest_key_version)
						BETWEEN 1 AND 512
					AND NEW.terminal_reason_seal_key_version =
						btrim(NEW.terminal_reason_seal_key_version)
					AND NEW.terminal_reason_digest_key_version =
						btrim(NEW.terminal_reason_digest_key_version)
					AND NEW.terminal_reason_seal_key_version !~ E'[\\r\\n]'
					AND NEW.terminal_reason_digest_key_version !~ E'[\\r\\n]'
					AND jsonb_typeof(NEW.terminal_reason_sealed_json::jsonb -> 'ciphertext') =
						'string'
					AND COALESCE(length(
						NEW.terminal_reason_sealed_json::jsonb ->> 'ciphertext'), 0) > 0
					AND jsonb_typeof(
						NEW.terminal_reason_sealed_json::jsonb -> 'key_version') = 'string'
					AND NEW.terminal_reason_sealed_json::jsonb ->> 'key_version' =
						NEW.terminal_reason_seal_key_version)
			)
		)
	), false) THEN
		RAISE EXCEPTION 'olivares: invalid Handoff terminal ProtectedPayload'
			USING ERRCODE = '23514';
	END IF;
	IF NEW.state = 'offered' AND (
			NEW.ack_id IS NOT NULL OR NEW.accepted_at IS NOT NULL
			OR NEW.rejected_at IS NOT NULL OR NEW.withdrawn_at IS NOT NULL
			OR NEW.expired_at IS NOT NULL OR NEW.terminal_code IS NOT NULL
			OR NEW.terminal_reason_encoding IS NOT NULL
			OR NEW.resulting_lease_fence IS NOT NULL)
		OR NEW.state = 'accepted' AND (
			NEW.ack_id IS NULL OR NEW.accepted_at IS NULL
			OR NEW.accepted_at < NEW.created_at OR NEW.accepted_at > NEW.updated_at
			OR NEW.accepted_at >= NEW.ack_deadline
			OR NEW.rejected_at IS NOT NULL OR NEW.withdrawn_at IS NOT NULL
			OR NEW.expired_at IS NOT NULL OR NEW.terminal_code IS NOT NULL
			OR NEW.terminal_reason_encoding IS NOT NULL
			OR (COALESCE(NEW.offered_lease_fence,0) = 0
				AND COALESCE(NEW.resulting_lease_fence,0) <> 0)
			OR (COALESCE(NEW.offered_lease_fence,0) > 0
				AND COALESCE(NEW.resulting_lease_fence,0) <=
					COALESCE(NEW.offered_lease_fence,0)))
		OR NEW.state IN ('rejected','withdrawn','expired') AND (
			NEW.ack_id IS NOT NULL OR NEW.accepted_at IS NOT NULL
			OR (NEW.state = 'rejected') <> (NEW.rejected_at IS NOT NULL)
			OR (NEW.state = 'withdrawn') <> (NEW.withdrawn_at IS NOT NULL)
			OR (NEW.state = 'expired') <> (NEW.expired_at IS NOT NULL)
			OR NEW.terminal_code IS NULL
			OR NEW.terminal_code !~ '^[a-z0-9._-]{1,128}$'
			OR NEW.terminal_reason_encoding IS NULL
			OR NEW.resulting_lease_fence IS NOT NULL)
		OR NEW.rejected_at IS NOT NULL AND
			(NEW.rejected_at < NEW.created_at OR NEW.rejected_at > NEW.updated_at)
		OR NEW.withdrawn_at IS NOT NULL AND
			(NEW.withdrawn_at < NEW.created_at OR NEW.withdrawn_at > NEW.updated_at)
		OR NEW.expired_at IS NOT NULL AND
			(NEW.expired_at < NEW.created_at OR NEW.expired_at > NEW.updated_at)
		OR NEW.state = 'expired' AND NEW.expired_at < NEW.ack_deadline THEN
		RAISE EXCEPTION 'olivares: Handoff state evidence is inconsistent'
			USING ERRCODE = '23514';
	END IF;
	IF NEW.state = 'offered' THEN
		PERFORM pg_advisory_xact_lock(hashtextextended(concat_ws('|',
			'sessions_work_handoff_offered', NEW.tenant_id::text,
			NEW.workspace_id::text, NEW.work_item_id::text), 0));
	END IF;
	IF NOT EXISTS (
		SELECT 1 FROM sessions_message m
		JOIN sessions_message_delivery d ON d.message_id = m.id AND d.tenant_id = m.tenant_id
		JOIN sessions_work_item w ON w.id = m.work_item_id AND w.tenant_id = m.tenant_id
		WHERE m.id = NEW.message_id AND m.tenant_id = NEW.tenant_id
			AND m.workspace_id = NEW.workspace_id AND m.work_item_id = NEW.work_item_id
			AND m.kind = 'handoff_offer' AND d.id = NEW.delivery_id
			AND d.workspace_id = NEW.workspace_id AND d.recipient_kind = NEW.to_kind
			AND d.recipient_ref = NEW.to_ref AND d.required
			AND w.workspace_id = NEW.workspace_id
			AND m.payload_encoding = NEW.handoff_encoding
			AND m.payload_protection_generation = NEW.handoff_protection_generation
			AND (m.expires_at IS NULL OR m.expires_at >= NEW.ack_deadline)
			AND (SELECT count(*) FROM sessions_message_delivery rd
				WHERE rd.tenant_id = m.tenant_id AND rd.workspace_id = m.workspace_id
					AND rd.message_id = m.id AND rd.required) = 1) THEN
		RAISE EXCEPTION 'olivares: Handoff message/delivery/work lineage is invalid'
			USING ERRCODE = '23514';
	END IF;
	IF NOT EXISTS (SELECT 1 FROM sessions_work_event e
		WHERE e.tenant_id = NEW.tenant_id AND e.workspace_id = NEW.workspace_id
			AND e.aggregate_kind = 'sessions.work_item' AND e.aggregate_id = NEW.work_item_id
			AND e.seq = NEW.context_event_seq) THEN
		RAISE EXCEPTION 'olivares: Handoff context Event crosses WorkItem lineage'
			USING ERRCODE = '23514';
	END IF;
	IF NEW.state = 'accepted' AND NOT EXISTS (
		SELECT 1 FROM sessions_message_ack a WHERE a.id = NEW.ack_id
			AND a.tenant_id = NEW.tenant_id AND a.workspace_id = NEW.workspace_id
			AND a.delivery_id = NEW.delivery_id AND NOT a.late) THEN
		RAISE EXCEPTION 'olivares: accepted Handoff Ack crosses exact Delivery'
			USING ERRCODE = '23514';
	END IF;
	IF NEW.state = 'offered' AND EXISTS (
		SELECT 1 FROM sessions_work_handoff p
		WHERE p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
			AND p.work_item_id = NEW.work_item_id AND p.state = 'offered'
			AND p.id <> NEW.id) THEN
		RAISE EXCEPTION 'olivares: WorkItem has another offered Handoff'
			USING ERRCODE = '23514';
	END IF;
	IF TG_OP = 'UPDATE' AND (
		(to_jsonb(NEW) - ARRAY[
			'version','updated_at','state','ack_id','accepted_at','rejected_at',
			'withdrawn_at','expired_at','terminal_code','terminal_reason_encoding',
			'terminal_reason_plain_json','terminal_reason_sealed_json','terminal_reason_schema',
			'terminal_reason_digest','terminal_reason_seal_key_version',
			'terminal_reason_digest_key_version','terminal_reason_protection_generation',
			'resulting_lease_fence'
		]) IS DISTINCT FROM
		(to_jsonb(OLD) - ARRAY[
			'version','updated_at','state','ack_id','accepted_at','rejected_at',
			'withdrawn_at','expired_at','terminal_code','terminal_reason_encoding',
			'terminal_reason_plain_json','terminal_reason_sealed_json','terminal_reason_schema',
			'terminal_reason_digest','terminal_reason_seal_key_version',
			'terminal_reason_digest_key_version','terminal_reason_protection_generation',
			'resulting_lease_fence'
		])
		OR (OLD.state <> NEW.state AND NOT
			(OLD.state = 'offered'
				AND NEW.state IN ('accepted','rejected','withdrawn','expired')))
		OR (NEW.state = 'accepted' AND (
			NEW.updated_at >= OLD.ack_deadline
			OR NEW.accepted_at IS DISTINCT FROM NEW.updated_at))
		OR OLD.state <> 'offered'
	) THEN
		RAISE EXCEPTION 'olivares: Handoff immutable lineage or transition changed'
			USING ERRCODE = '23514';
	END IF;
	RETURN NEW;
END;
$handoff_guard$;
$function_definition$;

	EXECUTE $drop_trigger$
DROP TRIGGER sessions_work_handoff_guard ON public.sessions_work_handoff
$drop_trigger$;
	EXECUTE $create_trigger$
CREATE TRIGGER sessions_work_handoff_guard
BEFORE INSERT OR UPDATE ON public.sessions_work_handoff
FOR EACH ROW EXECUTE FUNCTION public.olivares_sessions_work_handoff_validate_v24()
$create_trigger$;
END
$migration$;
