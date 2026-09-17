-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- B1: retire UNIQUE (tenant_id, session_ref). Its successor 0021 already holds.
-- The descriptor no longer declares this index, so reconcile does not recreate it.
DROP INDEX IF EXISTS public.sessions_live_ref_uniq;
