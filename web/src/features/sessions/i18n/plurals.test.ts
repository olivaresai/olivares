// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'

import { formatInt } from '@/lib/format'
import i18n from '@/lib/i18n'
import './index'

const frenchCompactedTimes = 'Compacté 1\u202f000\u202f000 fois' // language-data: French expected translation
// prettier-ignore
const frenchUnreadable = '1\u202f000\u202f000 enregistrements illisibles ne sont pas affichés.' // language-data: French expected translation

describe('session activity plurals', () => {
  it.each([
    ['es', 'compactedTimes', 'Compactado 1.000.000 veces'],
    ['fr', 'compactedTimes', frenchCompactedTimes],
    [
      'es',
      'unreadable',
      '1.000.000 registros no se han podido leer y no se muestran.',
    ],
    ['fr', 'unreadable', frenchUnreadable],
    ['es', 'toolUses', '1.000.000 llamadas a herramientas'],
    ['fr', 'toolUses', '1\u202f000\u202f000 appels d’outils'],
  ])('resolves %s activity.%s_many without fallback', (lng, key, expected) => {
    const result = i18n.t(`activity.${key}`, {
      ns: 'sessions',
      lng,
      count: 1_000_000,
      n: formatInt(1_000_000, lng),
      returnDetails: true,
    })

    expect(result.usedLng).toBe(lng)
    expect(result.exactUsedKey).toBe(`activity.${key}_many`)
    expect(result.res).toBe(expected)
  })
})
