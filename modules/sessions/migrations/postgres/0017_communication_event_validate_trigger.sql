-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE OR REPLACE TRIGGER sessions_work_event_guard
BEFORE INSERT OR UPDATE ON sessions_work_event
FOR EACH ROW EXECUTE FUNCTION olivares_sessions_communication_event_validate();
