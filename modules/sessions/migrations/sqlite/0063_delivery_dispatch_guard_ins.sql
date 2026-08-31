-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_delivery_dispatch_guard_ins
BEFORE INSERT ON sessions_delivery_dispatch
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: invalid sessions communication entity identity')
	WHERE NEW.id IS NULL
		OR NEW.id NOT GLOB '????????-????-7???-[89ab]???-????????????'
		OR length(replace(NEW.id,'-','')) <> 32
		OR replace(NEW.id,'-','') GLOB '*[^0-9a-f]*'
		OR NEW.tenant_id IS NULL
		OR NEW.tenant_id NOT GLOB '????????-????-7???-[89ab]???-????????????'
		OR length(replace(NEW.tenant_id,'-','')) <> 32
		OR replace(NEW.tenant_id,'-','') GLOB '*[^0-9a-f]*'
		OR NEW.tenant_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff'
		OR NEW.workspace_id IS NULL
		OR NEW.workspace_id NOT GLOB '????????-????-7???-[89ab]???-????????????'
		OR length(replace(NEW.workspace_id,'-','')) <> 32
		OR replace(NEW.workspace_id,'-','') GLOB '*[^0-9a-f]*';
	SELECT RAISE(ABORT, 'olivares: invalid DeliveryDispatch envelope')
	WHERE NEW.version < 1 OR NEW.updated_at < NEW.created_at
		OR NEW.state <> 'pending' OR NEW.attempt_count <> 0
		OR NEW.endpoint_generation < 1
		OR (NEW.route_rule_id IS NULL) IS NOT (NEW.route_rule_generation IS NULL)
		OR (NEW.route_rule_generation IS NOT NULL AND NEW.route_rule_generation < 1)
		OR NEW.dispatch_generation < 1 OR NEW.reroute_rung < 0 OR NEW.policy_generation < 1
		OR NEW.state NOT IN ('pending','in_flight','succeeded','failed','unknown','dead_letter','superseded')
		OR NEW.attempt_count NOT BETWEEN 0 AND 1 OR length(NEW.idempotency_key_hash) <> 32
		OR (NEW.dispatch_generation = 1 AND
			(NEW.id <> NEW.root_dispatch_id OR NEW.predecessor_id IS NOT NULL OR NEW.reroute_rung <> 0))
		OR (NEW.dispatch_generation > 1 AND
			(NEW.predecessor_id IS NULL OR NEW.id = NEW.root_dispatch_id))
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
			OR NEW.last_verdict IS NULL OR NEW.last_verdict <> 'LIMPIO' OR NEW.last_code IS NULL
			OR NEW.resolution_deadline_at IS NOT NULL OR NEW.resolution_code IS NULL))
		OR (NEW.state IN ('failed','unknown') AND
			(NEW.attempt_count <> 1 OR NEW.claim_owner IS NOT NULL
				OR NEW.last_verdict IS NULL
				OR NEW.last_verdict IS NOT CASE NEW.state WHEN 'failed' THEN 'ROTO'
					ELSE 'NO_HE_PODIDO_MIRAR' END
				OR NEW.last_code IS NULL OR NEW.resolution_code IS NULL))
		OR (NEW.state IN ('dead_letter','superseded') AND
			(NEW.attempt_count <> 1 OR NEW.claim_owner IS NOT NULL
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
	SELECT RAISE(ABORT, 'olivares: DeliveryDispatch delivery/endpoint route crosses generation')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_message_delivery d
		WHERE d.id = NEW.delivery_id AND d.tenant_id = NEW.tenant_id
			AND d.workspace_id = NEW.workspace_id)
		OR NOT EXISTS (SELECT 1 FROM sessions_communication_endpoint e
			WHERE e.id = NEW.endpoint_id AND e.tenant_id = NEW.tenant_id
				AND e.workspace_id = NEW.workspace_id AND e.generation = NEW.endpoint_generation)
		OR (NEW.route_rule_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM sessions_channel_route r
			WHERE r.id = NEW.route_rule_id AND r.tenant_id = NEW.tenant_id
				AND r.workspace_id = NEW.workspace_id AND r.generation = NEW.route_rule_generation));
	SELECT RAISE(ABORT, 'olivares: DeliveryDispatch predecessor lineage is not serialized')
	WHERE NEW.dispatch_generation > 1 AND NOT EXISTS (
		SELECT 1 FROM sessions_delivery_dispatch p
		WHERE p.id = NEW.predecessor_id AND p.tenant_id = NEW.tenant_id
			AND p.workspace_id = NEW.workspace_id AND p.root_dispatch_id = NEW.root_dispatch_id
			AND p.delivery_id = NEW.delivery_id AND p.dispatch_generation + 1 = NEW.dispatch_generation
			AND p.state = 'superseded' AND p.updated_at = NEW.created_at
			AND p.settled_at = NEW.created_at
			AND NEW.reroute_rung BETWEEN p.reroute_rung AND p.reroute_rung + 1
			AND ((NEW.reroute_rung = p.reroute_rung AND NEW.endpoint_id = p.endpoint_id
				AND NEW.endpoint_generation = p.endpoint_generation
				AND NEW.route_rule_id IS p.route_rule_id
				AND NEW.route_rule_generation IS p.route_rule_generation
				AND NEW.policy_generation = p.policy_generation)
				OR NEW.reroute_rung = p.reroute_rung + 1));
	SELECT RAISE(ABORT, 'olivares: DeliveryDispatch root has multiple current generations')
	WHERE NEW.state IN ('pending','in_flight') AND EXISTS (
		SELECT 1 FROM sessions_delivery_dispatch p
		WHERE p.tenant_id = NEW.tenant_id AND p.root_dispatch_id = NEW.root_dispatch_id
			AND p.state IN ('pending','in_flight'));
END;
