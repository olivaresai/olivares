-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_delivery_attempt_guard_upd
BEFORE UPDATE ON sessions_delivery_attempt
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: DeliveryAttempt immutable invocation or state changed')
	WHERE NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id
		OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at
		OR NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at
		OR NEW.dispatch_id IS NOT OLD.dispatch_id OR NEW.attempt_seq IS NOT OLD.attempt_seq
		OR NEW.started_at IS NOT OLD.started_at OR NEW.request_hash IS NOT OLD.request_hash
		OR (OLD.state <> NEW.state AND NOT
			(OLD.state = 'reserved' AND NEW.state IN ('finished','abandoned')))
		OR OLD.state <> 'reserved';
	SELECT RAISE(ABORT, 'olivares: DeliveryAttempt settlement is inconsistent')
	WHERE NEW.state = 'reserved' OR NEW.finished_at IS NULL
		OR NEW.finished_at < NEW.started_at OR NEW.finished_at > NEW.updated_at
		OR NEW.verdict IS NULL OR NEW.verdict NOT IN ('LIMPIO','ROTO','NO_HE_PODIDO_MIRAR')
		OR NEW.code IS NULL OR length(NEW.code) NOT BETWEEN 1 AND 128
		OR NEW.code GLOB '*[^a-z0-9._-]*'
		OR (NEW.state = 'abandoned' AND (NEW.transmit_boundary <> 'unknown'
			OR NEW.verdict <> 'NO_HE_PODIDO_MIRAR' OR NEW.provider_receipt_hash IS NOT NULL))
		OR (NEW.state = 'finished' AND NOT (
			(NEW.transmit_boundary = 'crossed' AND NEW.verdict = 'LIMPIO'
				AND NEW.provider_receipt_hash IS NOT NULL
				AND length(NEW.provider_receipt_hash) = 32)
			OR (NEW.transmit_boundary = 'not_crossed' AND NEW.verdict = 'ROTO'
				AND NEW.provider_receipt_hash IS NULL)
			OR (NEW.transmit_boundary = 'unknown' AND NEW.verdict = 'NO_HE_PODIDO_MIRAR'
				AND NEW.provider_receipt_hash IS NULL)));
END;
