-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- Provider accounts: a name is unique per (tenant, execution environment), across
-- every driver and every state, archived included. A profile with no name keys on
-- its own profile_ref, which is already unique per tenant, so no existing row can
-- collide and nothing is rewritten. driver is deliberately absent from the key: a
-- Codex account cannot take a name a Claude account holds. The columns are added by
-- the descriptor reconciler before this runs; the boot-time migration lock
-- serializes the DDL, so a plain (transactional) CREATE is used.
CREATE UNIQUE INDEX IF NOT EXISTS sessions_provider_account_name_uniq
	ON public.sessions_provider_profile (tenant_id, environment_ref, COALESCE(account_name, profile_ref));
