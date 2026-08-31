-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_delivery_dispatch_guard_upd
BEFORE UPDATE ON sessions_delivery_dispatch
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: DeliveryDispatch immutable generation or state changed')
	WHERE NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id
		OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at
		OR NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at
		OR NEW.delivery_id IS NOT OLD.delivery_id OR NEW.root_dispatch_id IS NOT OLD.root_dispatch_id
		OR NEW.predecessor_id IS NOT OLD.predecessor_id OR NEW.endpoint_id IS NOT OLD.endpoint_id
		OR NEW.endpoint_generation IS NOT OLD.endpoint_generation
		OR NEW.route_rule_id IS NOT OLD.route_rule_id
		OR NEW.route_rule_generation IS NOT OLD.route_rule_generation
		OR NEW.dispatch_generation IS NOT OLD.dispatch_generation
		OR NEW.reroute_rung IS NOT OLD.reroute_rung
		OR NEW.policy_generation IS NOT OLD.policy_generation
		OR NEW.idempotency_key_hash IS NOT OLD.idempotency_key_hash
		OR NEW.attempt_count < OLD.attempt_count OR NEW.attempt_count > 1
		OR (OLD.state <> NEW.state AND NOT (
			(OLD.state = 'pending' AND NEW.state = 'in_flight') OR
			(OLD.state = 'in_flight' AND NEW.state IN ('succeeded','failed','unknown')) OR
			(OLD.state = 'failed' AND NEW.state IN ('dead_letter','superseded')) OR
			(OLD.state = 'unknown' AND NEW.state IN ('succeeded','failed','dead_letter','superseded'))))
		OR (OLD.state IN ('failed','unknown') AND NEW.state IS OLD.state)
		OR (OLD.state IN ('failed','unknown') AND NEW.state = 'dead_letter'
			AND (OLD.resolution_deadline_at IS NULL
				OR NEW.updated_at < OLD.resolution_deadline_at))
		OR (OLD.state = 'unknown' AND NEW.state <> OLD.state
			AND (NEW.reconciliation_observed_at IS NULL
				OR NEW.reconciliation_observed_at < OLD.updated_at))
		OR (OLD.state = 'failed' AND NEW.state IN ('dead_letter','superseded')
			AND NEW.last_verdict IS NOT OLD.last_verdict)
		OR OLD.state IN ('succeeded','dead_letter','superseded');
	SELECT RAISE(ABORT, 'olivares: invalid DeliveryDispatch envelope')
	WHERE NEW.state NOT IN ('pending','in_flight','succeeded','failed','unknown','dead_letter','superseded')
		OR NEW.attempt_count NOT BETWEEN 0 AND 1
		OR (NEW.claim_owner IS NULL) IS NOT (NEW.claim_until IS NULL)
		OR (NEW.claim_owner IS NOT NULL AND
			(length(NEW.claim_owner) NOT BETWEEN 1 AND 128
				OR NEW.claim_owner GLOB '*[^a-z0-9._-]*' OR NEW.claim_until <= NEW.created_at))
		OR (NEW.next_attempt_at IS NOT NULL AND
			(NEW.state <> 'pending' OR NEW.next_attempt_at < NEW.updated_at))
		OR (NEW.last_code IS NOT NULL AND
			(length(NEW.last_code) NOT BETWEEN 1 AND 128 OR NEW.last_code GLOB '*[^a-z0-9._-]*'))
		OR (NEW.resolution_code IS NOT NULL AND
			(length(NEW.resolution_code) NOT BETWEEN 1 AND 128
				OR NEW.resolution_code GLOB '*[^a-z0-9._-]*'))
		OR (NEW.state IN ('failed','unknown') AND
			(NEW.resolution_deadline_at IS NULL OR NEW.resolution_deadline_at <= NEW.updated_at))
		OR (NEW.state IN ('succeeded','dead_letter','superseded')) IS NOT (NEW.settled_at IS NOT NULL)
		OR (NEW.settled_at IS NOT NULL AND NEW.settled_at < NEW.created_at);
	SELECT RAISE(ABORT, 'olivares: DeliveryDispatch state evidence is inconsistent')
	WHERE (NEW.state = 'pending' AND (NEW.attempt_count <> 0 OR NEW.claim_owner IS NOT NULL
			OR NEW.last_verdict IS NOT NULL OR NEW.last_code IS NOT NULL
			OR NEW.resolution_deadline_at IS NOT NULL OR NEW.resolution_code IS NOT NULL))
		OR (NEW.state = 'in_flight' AND (NEW.attempt_count <> 1 OR NEW.claim_owner IS NULL
			OR NEW.last_verdict IS NOT NULL OR NEW.last_code IS NOT NULL
			OR NEW.resolution_deadline_at IS NOT NULL OR NEW.resolution_code IS NOT NULL))
		OR (NEW.state = 'succeeded' AND (NEW.attempt_count <> 1 OR NEW.claim_owner IS NOT NULL
			OR NEW.last_verdict IS NULL OR NEW.last_verdict <> 'LIMPIO'
			OR NEW.last_code IS NULL OR NEW.resolution_deadline_at IS NOT NULL
			OR NEW.resolution_code IS NULL))
		OR (NEW.state IN ('failed','unknown') AND
			(NEW.attempt_count <> 1 OR NEW.claim_owner IS NOT NULL
				OR NEW.last_verdict IS NULL
				OR NEW.last_verdict IS NOT CASE NEW.state WHEN 'failed' THEN 'ROTO'
					ELSE 'NO_HE_PODIDO_MIRAR' END
				OR NEW.last_code IS NULL OR NEW.resolution_code IS NULL))
		OR (NEW.state IN ('dead_letter','superseded') AND
			(NEW.attempt_count <> 1 OR NEW.claim_owner IS NOT NULL
				OR NEW.last_verdict IS NULL
				OR NEW.last_verdict NOT IN ('ROTO','NO_HE_PODIDO_MIRAR')
				OR NEW.last_code IS NULL OR NEW.resolution_code IS NULL
				OR NEW.resolution_deadline_at IS NOT NULL));
	SELECT RAISE(ABORT, 'olivares: DeliveryDispatch reconciliation shape is incomplete')
	WHERE NOT COALESCE((
		(NEW.reconciled_attempt_id IS NULL AND NEW.reconciled_endpoint_id IS NULL
			AND NEW.reconciled_endpoint_generation IS NULL AND NEW.reconciliation_verdict IS NULL
			AND NEW.reconciliation_code IS NULL AND NEW.reconciliation_evidence_ref IS NULL
			AND NEW.reconciliation_observed_at IS NULL AND NEW.provider_acceptance_hash IS NULL)
		OR (NEW.reconciled_attempt_id IS NOT NULL
			AND NEW.reconciled_endpoint_id = NEW.endpoint_id
			AND NEW.reconciled_endpoint_generation = NEW.endpoint_generation
			AND NEW.reconciliation_verdict IN ('LIMPIO','ROTO','NO_HE_PODIDO_MIRAR')
			AND length(NEW.reconciliation_code) BETWEEN 1 AND 128
			AND NEW.reconciliation_code NOT GLOB '*[^a-z0-9._-]*'
			AND length(CAST(NEW.reconciliation_evidence_ref AS BLOB)) BETWEEN 1 AND 512
			AND trim(NEW.reconciliation_evidence_ref) = NEW.reconciliation_evidence_ref
			AND instr(NEW.reconciliation_evidence_ref,char(0)) = 0
			AND instr(NEW.reconciliation_evidence_ref,char(10)) = 0
			AND instr(NEW.reconciliation_evidence_ref,char(13)) = 0
			AND NEW.reconciliation_observed_at BETWEEN NEW.created_at AND NEW.updated_at
			AND length(NEW.provider_acceptance_hash) = 32 AND NEW.attempt_count = 1
			AND NEW.state IN ('succeeded','failed','dead_letter','superseded'))), 0);
	SELECT RAISE(ABORT, 'olivares: DeliveryDispatch UNKNOWN reconciliation is inconsistent')
	WHERE (NEW.state = 'superseded' AND NEW.last_verdict = 'NO_HE_PODIDO_MIRAR'
			AND NEW.reconciliation_verdict IS NOT 'ROTO')
		OR (NEW.state = 'dead_letter' AND NEW.last_verdict = 'NO_HE_PODIDO_MIRAR'
			AND NEW.reconciliation_verdict IS NOT 'NO_HE_PODIDO_MIRAR');
	SELECT RAISE(ABORT, 'olivares: DeliveryDispatch reconciliation crosses exact Attempt')
	WHERE NEW.reconciled_attempt_id IS NOT NULL AND NOT EXISTS (
		SELECT 1 FROM sessions_delivery_attempt a
		WHERE a.id = NEW.reconciled_attempt_id AND a.tenant_id = NEW.tenant_id
			AND a.workspace_id = NEW.workspace_id AND a.dispatch_id = NEW.id
			AND a.attempt_seq = 1 AND a.state IN ('finished','abandoned'));
	SELECT RAISE(ABORT, 'olivares: DeliveryDispatch state contradicts its exact Attempt')
	WHERE (NEW.state = 'in_flight' AND EXISTS (
			SELECT 1 FROM sessions_delivery_attempt a
			WHERE a.tenant_id = NEW.tenant_id AND a.workspace_id = NEW.workspace_id
				AND a.dispatch_id = NEW.id AND a.attempt_seq = 1 AND a.state <> 'reserved'))
		OR (NEW.state IN ('succeeded','failed','unknown','dead_letter','superseded')
			AND NOT EXISTS (
				SELECT 1 FROM sessions_delivery_attempt a
				WHERE a.tenant_id = NEW.tenant_id AND a.workspace_id = NEW.workspace_id
					AND a.dispatch_id = NEW.id AND a.attempt_seq = 1
					AND ((NEW.state = 'succeeded' AND (
						(a.state = 'finished' AND a.transmit_boundary = 'crossed'
							AND a.verdict = 'LIMPIO' AND NEW.last_verdict = a.verdict)
						OR (a.state IN ('finished','abandoned') AND a.transmit_boundary = 'unknown'
							AND a.verdict = 'NO_HE_PODIDO_MIRAR'
							AND NEW.reconciled_attempt_id = a.id
							AND NEW.reconciliation_verdict = 'LIMPIO')))
					OR (NEW.state = 'failed' AND (
						(a.state = 'finished' AND a.transmit_boundary = 'not_crossed'
							AND a.verdict = 'ROTO' AND NEW.last_verdict = a.verdict)
						OR (a.state IN ('finished','abandoned') AND a.transmit_boundary = 'unknown'
							AND a.verdict = 'NO_HE_PODIDO_MIRAR'
							AND NEW.reconciled_attempt_id = a.id
							AND NEW.reconciliation_verdict = 'ROTO')))
					OR (NEW.state = 'unknown' AND a.state IN ('finished','abandoned')
						AND a.transmit_boundary = 'unknown' AND a.verdict = 'NO_HE_PODIDO_MIRAR'
						AND NEW.last_verdict = a.verdict)
					OR (NEW.state = 'dead_letter' AND a.state IN ('finished','abandoned')
						AND ((a.verdict = 'NO_HE_PODIDO_MIRAR'
							AND NEW.reconciled_attempt_id = a.id
							AND NEW.reconciliation_verdict = 'NO_HE_PODIDO_MIRAR')
							OR (a.verdict = 'ROTO' AND NEW.last_verdict = a.verdict)))
					OR (NEW.state = 'superseded' AND (
						(a.state = 'finished' AND a.transmit_boundary = 'not_crossed'
							AND a.verdict = 'ROTO' AND NEW.last_verdict = 'ROTO')
						OR (a.state IN ('finished','abandoned') AND a.transmit_boundary = 'unknown'
							AND a.verdict = 'NO_HE_PODIDO_MIRAR'
							AND NEW.reconciled_attempt_id = a.id
							AND NEW.reconciliation_verdict = 'ROTO'))))));
END;
