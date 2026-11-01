// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'

/**
 * The active request scope, resolved from authenticated membership. A principal with
 * several memberships can choose one; the client sends it as X-Olivares-Tenant.
 * AuthProvider restores the user's engine preference before publishing the principal.
 */
interface TenantState {
  activeTenant: string | null
  setActiveTenant: (tenant: string | null) => void
  clear: () => void
}

export const useTenantStore = create<TenantState>((set) => ({
  activeTenant: null,
  setActiveTenant: (tenant) => set({ activeTenant: tenant }),
  clear: () => set({ activeTenant: null }),
}))
