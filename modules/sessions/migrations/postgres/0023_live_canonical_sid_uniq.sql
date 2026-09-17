-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- B1: at most ONE managed live row per canonical session. Only the plane's own
-- bridge writes canonical_sid; a cooperative observation never does, so it can
-- never collide here and never claims a run's row.
CREATE UNIQUE INDEX IF NOT EXISTS sessions_live_canonical_sid_uniq
	ON public.sessions_live (tenant_id, canonical_sid) WHERE canonical_sid IS NOT NULL;
