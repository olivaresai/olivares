-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- B1: live rows become unique per (tenant, observation scope, external id). The
-- COALESCE keeps legacy rows (scope NULL) unique too — a plain index over a NULL
-- column would not, on either engine. Created BEFORE the historical index is
-- dropped (0022) so uniqueness never lapses between the two statements. The table
-- is small on every deployment and the boot-time migration lock serializes this
-- DDL, so a plain (transactional) CREATE is used rather than CONCURRENTLY.
CREATE UNIQUE INDEX IF NOT EXISTS sessions_live_scope_ref_uniq
	ON public.sessions_live (tenant_id, COALESCE(observation_scope, 'legacy'), session_ref);
