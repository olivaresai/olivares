-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

CREATE TRIGGER sessions_communication_binding_spec_no_delete
BEFORE DELETE ON sessions_communication_binding_spec
FOR EACH ROW
BEGIN
	SELECT RAISE(ABORT, 'olivares: ProtocolBindingSpec rows cannot be hard-deleted');
END;
