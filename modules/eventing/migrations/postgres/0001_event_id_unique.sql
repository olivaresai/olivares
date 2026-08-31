-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
-- exactly-once capture per bus event id (the NATS bridge can hand the
-- same event to two nodes inside the leader-failover overlap). IF NOT EXISTS
-- because a fresh install already created it from the entity descriptor.
CREATE UNIQUE INDEX IF NOT EXISTS eventing_event_id_uniq ON eventing_event (tenant_id, event_id)
