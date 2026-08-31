-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_delivery_attempt_guard_ins
BEFORE INSERT ON sessions_delivery_attempt
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
	SELECT RAISE(ABORT, 'olivares: invalid DeliveryAttempt shape')
	WHERE NEW.version < 1 OR NEW.updated_at < NEW.created_at OR NEW.attempt_seq <> 1
		OR NEW.state <> 'reserved'
		OR NEW.state NOT IN ('reserved','finished','abandoned')
		OR NEW.transmit_boundary NOT IN ('not_crossed','crossed','unknown')
		OR NEW.started_at < NEW.created_at OR length(NEW.request_hash) <> 32
		OR (NEW.provider_receipt_hash IS NOT NULL AND length(NEW.provider_receipt_hash) <> 32)
		OR (NEW.state = 'reserved' AND (NEW.transmit_boundary <> 'unknown'
			OR NEW.finished_at IS NOT NULL OR NEW.verdict IS NOT NULL OR NEW.code IS NOT NULL
			OR NEW.provider_receipt_hash IS NOT NULL))
		OR (NEW.state <> 'reserved' AND (NEW.finished_at IS NULL
			OR NEW.finished_at < NEW.started_at OR NEW.finished_at > NEW.updated_at
			OR NEW.verdict IS NULL OR NEW.verdict NOT IN ('LIMPIO','ROTO','NO_HE_PODIDO_MIRAR')
			OR NEW.code IS NULL OR length(NEW.code) NOT BETWEEN 1 AND 128
			OR NEW.code GLOB '*[^a-z0-9._-]*'))
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
	SELECT RAISE(ABORT, 'olivares: DeliveryAttempt crosses Dispatch generation')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_delivery_dispatch d
		WHERE d.id = NEW.dispatch_id AND d.tenant_id = NEW.tenant_id
			AND d.workspace_id = NEW.workspace_id AND d.attempt_count = 1);
END;
