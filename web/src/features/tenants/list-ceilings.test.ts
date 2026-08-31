// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { leer, rutasSinTecho } from '@/test/list-ceiling-ratchet'

const NO_PAGINAN: Record<string, string> = {
  list: 'handleListOrgs calls authoritative SystemScope.ListOrgs without pagination (core/api/handlers_core.go)',
}

describe('tenants · the authoritative estate list is deliberately complete', () => {
  it('the sole list route is a measured non-paginated exception', () => {
    const { rutas, sinTecho } = rutasSinTecho(
      leer('features/tenants/api.ts'),
      NO_PAGINAN,
    )
    expect(rutas).toEqual(['list'])
    expect(Object.keys(NO_PAGINAN)).toEqual(rutas)
    expect(sinTecho).toEqual([])
  })
})
