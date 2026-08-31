-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE OR REPLACE FUNCTION olivares_sessions_work_lease_validate()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
DECLARE
	holder_changed boolean;
BEGIN
	IF NEW.id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
		OR NEW.tenant_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
		OR NEW.workspace_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
		OR NEW.work_item_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
		OR NEW.fence < 0 OR NEW.renewal_count < 0
		OR NEW.state NOT IN ('vacant','active','released','expired','revoked')
		OR (NEW.holder_run_ref IS NOT NULL AND octet_length(NEW.holder_run_ref) NOT BETWEEN 1 AND 512)
		OR (NEW.holder_agent_ref IS NOT NULL AND octet_length(NEW.holder_agent_ref) NOT BETWEEN 1 AND 512)
		OR (NEW.end_reason IS NOT NULL AND octet_length(NEW.end_reason) NOT BETWEEN 1 AND 2048)
		OR NOT EXISTS (
			SELECT 1 FROM sessions_work_item i
			WHERE i.id = NEW.work_item_id AND i.tenant_id = NEW.tenant_id
				AND i.workspace_id = NEW.workspace_id) THEN
		RAISE EXCEPTION 'olivares: invalid sessions work lease identity, vocabulary or lineage'
			USING ERRCODE = '23514';
	END IF;
	IF NEW.state = 'vacant' THEN
		IF NEW.fence <> 0 OR NEW.holder_sid IS NOT NULL OR NEW.holder_run_ref IS NOT NULL
			OR NEW.holder_agent_ref IS NOT NULL OR NEW.acquired_at IS NOT NULL
			OR NEW.renewed_at IS NOT NULL OR NEW.expires_at IS NOT NULL
			OR NEW.ended_at IS NOT NULL OR NEW.end_reason IS NOT NULL
			OR NEW.renewal_count <> 0 THEN
			RAISE EXCEPTION 'olivares: vacant sessions work lease carries authority'
				USING ERRCODE = '23514';
		END IF;
	ELSE
		IF NEW.holder_sid IS NULL
			OR NEW.holder_sid !~ '^osn_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
			OR NEW.acquired_at IS NULL OR NEW.expires_at IS NULL THEN
			RAISE EXCEPTION 'olivares: materialized sessions work lease lacks holder or timing'
				USING ERRCODE = '23514';
		END IF;
		IF NEW.state = 'active' THEN
			IF NEW.ended_at IS NOT NULL OR NEW.end_reason IS NOT NULL OR NEW.expires_at <= NEW.acquired_at THEN
				RAISE EXCEPTION 'olivares: active sessions work lease has invalid lifetime'
					USING ERRCODE = '23514';
			END IF;
		ELSIF NEW.ended_at IS NULL OR (NEW.state IN ('expired','revoked') AND NEW.end_reason IS NULL) THEN
			RAISE EXCEPTION 'olivares: ended sessions work lease lacks end evidence'
				USING ERRCODE = '23514';
		END IF;
	END IF;
	IF TG_OP = 'UPDATE' THEN
		IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
			OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
			OR NEW.work_item_id IS DISTINCT FROM OLD.work_item_id THEN
			RAISE EXCEPTION 'olivares: sessions work lease lineage is immutable'
				USING ERRCODE = '23514';
		END IF;
		holder_changed := (NEW.holder_sid, NEW.holder_run_ref, NEW.holder_agent_ref)
			IS DISTINCT FROM (OLD.holder_sid, OLD.holder_run_ref, OLD.holder_agent_ref);
		IF OLD.state = 'active' AND NEW.state = 'active' AND NOT holder_changed THEN
			IF NEW.fence <> OLD.fence OR NEW.renewal_count <> OLD.renewal_count + 1
				OR NEW.renewed_at IS NULL OR NEW.renewed_at IS NOT DISTINCT FROM OLD.renewed_at THEN
				RAISE EXCEPTION 'olivares: sessions work lease renewal changed authority or omitted evidence'
					USING ERRCODE = '23514';
			END IF;
		ELSIF OLD.state = 'active' AND NEW.state IN ('released','expired','revoked') THEN
			IF OLD.fence = 9223372036854775807 THEN
				IF NEW.fence <> OLD.fence OR NEW.renewal_count <> OLD.renewal_count THEN
					RAISE EXCEPTION 'olivares: exhausted sessions work lease end changed its fence'
						USING ERRCODE = '23514';
				END IF;
			ELSIF NEW.fence <> OLD.fence + 1 OR NEW.renewal_count <> OLD.renewal_count THEN
				RAISE EXCEPTION 'olivares: sessions work lease end did not invalidate its fence'
					USING ERRCODE = '23514';
			END IF;
		ELSIF NEW.state = 'active' AND (OLD.state <> 'active' OR holder_changed) THEN
			IF OLD.fence = 9223372036854775807 THEN
				RAISE EXCEPTION 'olivares: exhausted sessions work lease cannot mint another fence'
					USING ERRCODE = '23514';
			ELSIF NEW.fence <> OLD.fence + 1 OR NEW.renewal_count <> 0 THEN
				RAISE EXCEPTION 'olivares: sessions work lease acquisition did not mint one fence'
					USING ERRCODE = '23514';
			END IF;
		ELSE
			RAISE EXCEPTION 'olivares: illegal sessions work lease transition % -> %', OLD.state, NEW.state
				USING ERRCODE = '23514';
		END IF;
	END IF;
	RETURN NEW;
END;
$fn$;
