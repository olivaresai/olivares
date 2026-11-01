-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- The registered folder and its Resource anchor share an ID. Existing references
-- and registration versions are unchanged; no filesystem operation is performed.
-- Forward-only: retain the anchors on downgrade so stored protections survive.
-- Use the same class exclusion, tenant binding and enrollment as the store's
-- System lineage writer. The normal triggers retain their epoch checks.
DO $workspace_resources$
DECLARE
    target record;
    prior_tenant text := pg_catalog.current_setting('app.tenant_id', true);
BEGIN
    PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('core.lineage.writer.class.v8', 0));
    -- The lineage seed ledger is not RLS-scoped. Enumerate its initialized
    -- tenant generations, then bind each before querying either protected table.
    -- Unseeded generations cannot perform Resource writes through the store.
    FOR target IN
        SELECT tenant_id FROM public.core_lineage_seeded ORDER BY tenant_id
    LOOP
        PERFORM pg_catalog.set_config('app.tenant_id', target.tenant_id::text, true);
        PERFORM public.olivares_lineage_begin(target.tenant_id::text);
        INSERT INTO public.resources (id, tenant_id, created_at, updated_at, version, name, kind, uri, path)
        SELECT w.id, w.tenant_id, w.created_at, w.updated_at, 1,
               COALESCE(NULLIF(w.name, ''), w.workspace_ref), 'folder',
               'workspace:' || w.workspace_ref, '/' || w.id::text
        FROM public.sessions_workspace w
        WHERE w.tenant_id = target.tenant_id
          AND NOT EXISTS (SELECT 1 FROM public.resources r WHERE r.id = w.id AND r.tenant_id = w.tenant_id);
    END LOOP;
    PERFORM public.olivares_lineage_finish();
    PERFORM pg_catalog.set_config('app.tenant_id', COALESCE(prior_tenant, ''), true);
END;
$workspace_resources$;
