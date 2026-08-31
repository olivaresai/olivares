-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER IF NOT EXISTS sessions_channel_label_definition_guard_upd
BEFORE UPDATE ON sessions_channel_label_definition
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: ChannelLabelDefinition immutable generation changed')
	WHERE NEW.id IS NOT OLD.id OR NEW.tenant_id IS NOT OLD.tenant_id
		OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.created_at IS NOT OLD.created_at
		OR NEW.version <> OLD.version + 1 OR NEW.updated_at < OLD.updated_at
		OR NEW.channel_id IS NOT OLD.channel_id OR NEW.key IS NOT OLD.key
		OR NEW.generation IS NOT OLD.generation
		OR NEW.allowed_values_json IS NOT OLD.allowed_values_json
		OR NEW.values_hash IS NOT OLD.values_hash OR NEW.classification IS NOT OLD.classification
		OR (OLD.state <> NEW.state AND NOT (OLD.state = 'active' AND NEW.state = 'disabled'))
		OR (OLD.state = 'disabled' AND NEW.state <> OLD.state);
	SELECT RAISE(ABORT, 'olivares: invalid ChannelLabelDefinition state')
	WHERE NEW.state NOT IN ('active','disabled');
END;
