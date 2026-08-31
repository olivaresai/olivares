// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'
import { persist } from 'zustand/middleware'

/**
 * Active-tenant store. When a principal can act in more than one tenant, the UI
 * sends the chosen tenant as X-Olivares-Tenant on every request (the engine
 * resolves a single canonical tenant — core/api/middleware.go). A single-membership
 * principal needs none (the engine defaults it), but we still persist the explicit
 * choice for a stable cross-reload experience. The id is a tenant UUID, never the
 * reserved system tenant.
 */
interface TenantState {
  activeTenant: string | null
  setActiveTenant: (tenant: string | null) => void
  clear: () => void
}

export const useTenantStore = create<TenantState>()(
  persist(
    (set) => ({
      activeTenant: null,
      setActiveTenant: (tenant) => set({ activeTenant: tenant }),
      clear: () => set({ activeTenant: null }),
    }),
    { name: 'olivares.tenant' },
  ),
)
