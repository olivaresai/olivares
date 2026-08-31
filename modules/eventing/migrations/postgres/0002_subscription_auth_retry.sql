-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- per-subscription auth headers and retry policy. Fresh installs get
-- these columns from the entity descriptor; this migration covers upgraded
-- estates (the engine never ALTERs an already-created module table).

ALTER TABLE eventing_subscription ADD COLUMN IF NOT EXISTS auth_type TEXT NOT NULL DEFAULT 'none';
ALTER TABLE eventing_subscription ADD COLUMN IF NOT EXISTS auth_value_sealed TEXT;
ALTER TABLE eventing_subscription ADD COLUMN IF NOT EXISTS auth_value_hint TEXT;
ALTER TABLE eventing_subscription ADD COLUMN IF NOT EXISTS auth_header_name TEXT;
ALTER TABLE eventing_subscription ADD COLUMN IF NOT EXISTS max_attempts INTEGER;
ALTER TABLE eventing_subscription ADD COLUMN IF NOT EXISTS initial_interval_seconds INTEGER;
