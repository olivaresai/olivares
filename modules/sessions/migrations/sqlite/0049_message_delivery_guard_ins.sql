-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_message_delivery_guard_ins
BEFORE INSERT ON sessions_message_delivery
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
	SELECT RAISE(ABORT, 'olivares: invalid MessageDelivery shape')
	WHERE NEW.version < 1 OR NEW.updated_at < NEW.created_at OR NEW.state <> 'available'
		OR NEW.recipient_kind NOT IN ('user','agent','session')
		OR (NEW.recipient_kind = 'session' AND
			(length(NEW.recipient_ref) <> 40 OR substr(NEW.recipient_ref,1,4) <> 'osn_'))
		OR (NEW.recipient_kind <> 'session' AND length(NEW.recipient_ref) <> 36)
		OR (NEW.recipient_kind = 'session' AND
			(NEW.recipient_ref NOT GLOB 'osn_????????-????-7???-[89ab]???-????????????'
				OR length(replace(substr(NEW.recipient_ref,5),'-','')) <> 32
				OR replace(substr(NEW.recipient_ref,5),'-','') GLOB '*[^0-9a-f]*'))
		OR (NEW.recipient_kind <> 'session' AND
			(NEW.recipient_ref NOT GLOB '????????-????-7???-[89ab]???-????????????'
				OR length(replace(NEW.recipient_ref,'-','')) <> 32
				OR replace(NEW.recipient_ref,'-','') GLOB '*[^0-9a-f]*'))
		OR NEW.recipient_epoch < 1 OR NEW.delivery_seq < 1
		OR NEW.wake_policy NOT IN ('none','primary','all')
		OR NEW.state NOT IN ('available','acknowledged','expired','retracted','undeliverable')
		OR NEW.available_at < NEW.created_at
		OR NOT json_valid(NEW.route_reasons_json) OR json_type(NEW.route_reasons_json) <> 'array'
		OR json_array_length(NEW.route_reasons_json) NOT BETWEEN 1 AND 32
		OR EXISTS (SELECT 1 FROM json_each(NEW.route_reasons_json) r
			WHERE r.type <> 'text' OR length(r.value) NOT BETWEEN 1 AND 128
				OR r.value GLOB '*[^a-z0-9._-]*')
		OR EXISTS (SELECT 1 FROM json_each(NEW.route_reasons_json) a
			JOIN json_each(NEW.route_reasons_json) b
				ON CAST(b.key AS INTEGER) = CAST(a.key AS INTEGER) + 1
			WHERE a.value >= b.value)
		OR (NEW.required AND NEW.ack_due_at IS NULL)
		OR (NEW.ack_due_at IS NOT NULL AND NEW.ack_due_at < NEW.available_at)
		OR (NEW.expires_at IS NOT NULL AND NEW.expires_at <= NEW.available_at)
		OR (NEW.ack_due_at IS NOT NULL AND NEW.expires_at IS NOT NULL AND NEW.ack_due_at > NEW.expires_at)
		OR (NEW.first_seen_at IS NOT NULL AND
			(NEW.first_seen_at < NEW.available_at OR NEW.first_seen_at > NEW.updated_at))
		OR (NEW.last_wake_verdict IS NULL) IS NOT (NEW.last_wake_code IS NULL)
		OR (NEW.last_wake_verdict IS NULL) IS NOT (NEW.last_wake_at IS NULL)
		OR (NEW.last_wake_verdict IS NOT NULL AND
			(NEW.last_wake_verdict NOT IN ('LIMPIO','ROTO','NO_HE_PODIDO_MIRAR')
				OR length(NEW.last_wake_code) NOT BETWEEN 1 AND 128
				OR NEW.last_wake_at < NEW.available_at OR NEW.last_wake_at > NEW.updated_at));
	SELECT RAISE(ABORT, 'olivares: MessageDelivery state evidence is inconsistent')
	WHERE (NEW.state = 'available' AND
			(NEW.ack_id IS NOT NULL OR NEW.acknowledged_at IS NOT NULL
				OR NEW.retirement_tombstone_kind IS NOT NULL
				OR NEW.retirement_tombstone_id IS NOT NULL
				OR NEW.retirement_tombstone_version IS NOT NULL
				OR NEW.retirement_epoch IS NOT NULL OR NEW.undeliverable_at IS NOT NULL
				OR NEW.undeliverable_code IS NOT NULL))
		OR (NEW.state = 'acknowledged' AND
			(NEW.ack_id IS NULL OR NEW.acknowledged_at IS NULL
				OR NEW.acknowledged_at < NEW.available_at OR NEW.acknowledged_at > NEW.updated_at
				OR (NEW.ack_due_at IS NOT NULL AND NEW.acknowledged_at > NEW.ack_due_at)
					OR NEW.retirement_tombstone_kind IS NOT NULL
					OR NEW.retirement_tombstone_id IS NOT NULL
					OR NEW.retirement_tombstone_version IS NOT NULL
					OR NEW.retirement_epoch IS NOT NULL OR NEW.undeliverable_at IS NOT NULL
					OR NEW.undeliverable_code IS NOT NULL))
		OR (NEW.state IN ('expired','retracted') AND
			(NEW.ack_id IS NOT NULL OR NEW.acknowledged_at IS NOT NULL
					OR NEW.retirement_tombstone_kind IS NOT NULL
					OR NEW.retirement_tombstone_id IS NOT NULL
					OR NEW.retirement_tombstone_version IS NOT NULL
					OR NEW.retirement_epoch IS NOT NULL OR NEW.undeliverable_at IS NOT NULL
					OR NEW.undeliverable_code IS NOT NULL))
		OR (NEW.state = 'undeliverable' AND
				(NEW.recipient_kind = 'session' OR NEW.ack_id IS NOT NULL OR NEW.acknowledged_at IS NOT NULL
					OR NEW.retirement_tombstone_kind IS NULL
				OR NEW.retirement_tombstone_kind <>
					CASE NEW.recipient_kind WHEN 'user' THEN 'core.user_tombstone'
						ELSE 'core.directory_tombstone' END
				OR NEW.retirement_tombstone_id IS NULL OR NEW.retirement_tombstone_version <> 1
				OR NEW.retirement_epoch < 1 OR NEW.undeliverable_at IS NULL
				OR NEW.undeliverable_at < NEW.created_at OR NEW.undeliverable_at > NEW.updated_at
					OR NEW.undeliverable_code IS NULL
					OR length(NEW.undeliverable_code) NOT BETWEEN 1 AND 128
					OR NEW.undeliverable_code GLOB '*[^a-z0-9._-]*'));
	SELECT RAISE(ABORT, 'olivares: MessageDelivery retirement witness crosses core tombstone')
	WHERE NEW.state = 'undeliverable' AND (
		(NEW.recipient_kind = 'user' AND NOT EXISTS (
			SELECT 1 FROM core_user_tombstone t
			WHERE t.id = NEW.retirement_tombstone_id
				AND t.version = NEW.retirement_tombstone_version
				AND t.principal_kind = 'user' AND t.principal_ref = NEW.recipient_ref
				AND json_extract(t.resulting_epochs, '$."' || NEW.tenant_id || '"') =
					NEW.retirement_epoch))
		OR (NEW.recipient_kind = 'agent' AND NOT EXISTS (
			SELECT 1 FROM core_directory_tombstone t
			WHERE t.id = NEW.retirement_tombstone_id AND t.tenant_id = NEW.tenant_id
				AND t.version = NEW.retirement_tombstone_version
				AND t.principal_ref = NEW.recipient_ref
				AND t.resulting_epoch = NEW.retirement_epoch
				AND ((t.principal_kind = 'identity'
						AND t.workspace_ref = '00000000-0000-0000-0000-000000000000')
					OR (t.principal_kind = 'agent' AND t.workspace_ref = NEW.workspace_id)))));
	SELECT RAISE(ABORT, 'olivares: MessageDelivery message crosses tenant/workspace')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_message m
		WHERE m.id = NEW.message_id AND m.tenant_id = NEW.tenant_id
			AND m.workspace_id = NEW.workspace_id AND m.state = 'draft'
			AND m.available_at = NEW.available_at
			AND m.expires_at IS NEW.expires_at
			AND (NOT NEW.required OR m.ack_due_at IS NEW.ack_due_at)
			AND (m.ack_policy <> 'none' OR NEW.ack_due_at IS NULL)
			AND NOT (m.state = 'published' AND NEW.state = 'retracted')
			AND NOT (m.state = 'retracted' AND NEW.state = 'available')
			AND NOT (m.state = 'expired' AND NEW.state IN ('available','retracted')));
END;
