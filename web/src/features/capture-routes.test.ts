// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { rutasExtraDeCaptura } from './capture-routes'

describe('rutasExtraDeCaptura', () => {
  it('drops a parametric route, which would navigate to the literal $id', () => {
    const extra = rutasExtraDeCaptura(
      [{ id: 'session-viewer', path: '/session-viewer/$id' }],
      [],
    )

    expect(extra).toEqual([])
  })

  it('drops an extension route whose path a hand entry already captures, under another id', () => {
    // Enterprise declares postureExport at /posture-export; the hand list captures that page
    // as posture-export, with its export run.
    const extra = rutasExtraDeCaptura(
      [{ id: 'postureExport', path: '/posture-export' }],
      [{ id: 'posture-export', path: '/posture-export' }],
    )

    expect(extra).toEqual([])
  })

  it('keeps an extension route that no hand entry captures', () => {
    const ldap = { id: 'ldap', path: '/ldap' }

    const extra = rutasExtraDeCaptura(
      [ldap],
      [{ id: 'deploy', path: '/deploy' }],
    )

    expect(extra).toEqual([ldap])
  })
})
