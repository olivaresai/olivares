-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
CREATE TRIGGER sessions_work_lease_guard
BEFORE INSERT OR UPDATE ON sessions_work_lease
FOR EACH ROW EXECUTE FUNCTION olivares_sessions_work_lease_validate();
