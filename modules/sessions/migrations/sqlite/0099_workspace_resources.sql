-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- The registered folder and its Resource anchor share an ID. Existing references
-- and registration versions are unchanged; no filesystem operation is performed.
-- Forward-only: retain the anchors on downgrade so stored protections survive.
-- Participate in the existing lineage writer protocol. The Resource triggers
-- advance the tenant epochs; deleting this writer also clears its touched rows.
INSERT INTO main.core_lineage_writer (writer_id, tenant_id)
SELECT DISTINCT 'sessions.workspace.resources.v1', w.tenant_id
FROM main.sessions_workspace w
WHERE NOT EXISTS (SELECT 1 FROM main.resources r WHERE r.id = w.id AND r.tenant_id = w.tenant_id);

INSERT INTO main.resources (id, tenant_id, created_at, updated_at, version, name, kind, uri, path)
SELECT w.id, w.tenant_id, w.created_at, w.updated_at, 1,
       COALESCE(NULLIF(w.name, ''), w.workspace_ref), 'folder',
       'workspace:' || w.workspace_ref, '/' || w.id
FROM main.sessions_workspace w
WHERE NOT EXISTS (SELECT 1 FROM main.resources r WHERE r.id = w.id AND r.tenant_id = w.tenant_id);

DELETE FROM main.core_lineage_writer WHERE writer_id = 'sessions.workspace.resources.v1';
