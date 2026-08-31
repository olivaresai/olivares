-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_channel_label_definition_guard_ins
BEFORE INSERT ON sessions_channel_label_definition
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
	SELECT RAISE(ABORT, 'olivares: invalid ChannelLabelDefinition shape')
	WHERE NEW.version < 1 OR NEW.updated_at < NEW.created_at
		OR length(NEW.key) NOT BETWEEN 1 AND 128 OR NEW.key GLOB '*[^a-z0-9._-]*'
		OR NEW.generation < 1 OR NEW.classification <> 'non_sensitive'
		OR NEW.state NOT IN ('active','disabled') OR length(NEW.values_hash) <> 32
		OR NOT json_valid(NEW.allowed_values_json)
		OR json_type(NEW.allowed_values_json) <> 'array'
		OR json_array_length(NEW.allowed_values_json) NOT BETWEEN 1 AND 64
		OR EXISTS (SELECT 1 FROM json_each(NEW.allowed_values_json) v
			WHERE v.type <> 'text' OR length(v.value) NOT BETWEEN 1 AND 128
				OR v.value GLOB '*[^a-z0-9._-]*')
		OR EXISTS (SELECT 1 FROM json_each(NEW.allowed_values_json) a
			JOIN json_each(NEW.allowed_values_json) b ON CAST(b.key AS INTEGER) = CAST(a.key AS INTEGER) + 1
			WHERE a.value >= b.value);
	SELECT RAISE(ABORT, 'olivares: ChannelLabelDefinition channel crosses tenant/workspace')
	WHERE NOT EXISTS (SELECT 1 FROM sessions_channel c WHERE c.id = NEW.channel_id
		AND c.tenant_id = NEW.tenant_id AND c.workspace_id = NEW.workspace_id);
	SELECT RAISE(ABORT, 'olivares: ChannelLabelDefinition generation is not serialized')
	WHERE (NEW.generation = 1 AND EXISTS (
			SELECT 1 FROM sessions_channel_label_definition p
			WHERE p.tenant_id = NEW.tenant_id AND p.channel_id = NEW.channel_id AND p.key = NEW.key))
		OR (NEW.generation > 1 AND NOT EXISTS (
			SELECT 1 FROM sessions_channel_label_definition p
			WHERE p.tenant_id = NEW.tenant_id AND p.workspace_id = NEW.workspace_id
				AND p.channel_id = NEW.channel_id AND p.key = NEW.key
				AND p.generation = NEW.generation - 1 AND p.state = 'disabled'));
END;
