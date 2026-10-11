// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SEARCH WORDS. A page's place is its area and section; the question this console
// answers — "¿puede un ingeniero VER y GESTIONAR … sesiones, agentes, conexiones,
// identidades, modelos, reglas, automatizaciones, grupos, estados, workflows, tareas,
// protocolos e infraestructura?" — is thirteen NOUNS, and the palette and the sidebar
// filter find pages by them.
//
// The way that loss happens is silent: a noun stops being findable when the last view
// claiming it is renamed, and no heading changes. These assertions are the tripwire.
import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { LANGUAGE_CODES } from '@/lib/i18n'
import { FEATURE_VIEWS, PRODUCT_NOUNS, nounsForView } from './registry'

const LOCALES_ROOT = resolve(__dirname, '../lib/i18n/locales')
const viewIds = new Set(FEATURE_VIEWS.map((v) => v.id))

describe('the thirteen nouns', () => {
  it('carries exactly the thirteen of the product question, in order', () => {
    expect(PRODUCT_NOUNS.map((n) => n.id)).toEqual([
      'sessions',
      'agents',
      'connections',
      'identities',
      'models',
      'rules',
      'automations',
      'groups',
      'states',
      'workflows',
      'tasks',
      'protocols',
      'infrastructure',
    ])
  })

  it('points every noun at views that exist', () => {
    const broken = PRODUCT_NOUNS.flatMap((n) =>
      n.views.filter((v) => !viewIds.has(v)).map((v) => `${n.id} → ${v}`),
    )
    expect(
      broken,
      `A noun points at a view id no longer in FEATURE_VIEWS:\n  ${broken.join('\n  ')}\n` +
        `The noun just became unpointable — an operator asking for it by name lands nowhere.\n` +
        `Repoint it at the view that took the work over; do not delete the noun.`,
    ).toEqual([])
  })

  it('accounts for every view the thirteen nouns cannot name', () => {
    // ⚠ THE MEASURED GAP IN THE PRODUCT'S OWN VOCABULARY, and the reason this is an
    // allowlist instead of an empty array: TEN views answer to NONE of the thirteen
    // nouns, and they are not a ragbag — they fall into exactly THREE classes the noun
    // list has no word for. Padding them into a near-enough noun would hide a real
    // finding AND poison the filter, since every noun would then return the evidence
    // surfaces. Named here, the gap is visible and pinned.
    //
    // Written up in an internal design note (not shipped) The
    // thirteen nouns are the MANAGED OBJECTS; these views are about them rather than
    // being them, which is why no noun fits.
    //
    // The list SHRANK from thirteen to ten after the adversarial contrast: `evals` and
    // `redteam` do not merely record evidence, they take agents and models as subjects
    // you select, authorise and score; and `finops` owns the model-rate catalogue
    // (features/finops/api.ts:154-161, full CRUD). Calling those three pure evidence was
    // wrong, and it made a search for "models" miss the surface that prices them.
    const UNNAMED: Record<string, 'evidence' | 'value' | 'setup'> = {
      // "setup" — the guided first run is a path THROUGH several nouns, not a surface
      // FOR one. Filing it under "connections" would return the wizard every time an
      // operator searched for their connectors.
      onboarding: 'setup',
      // "evidence" — proving what the estate did. The nouns name the things; nothing
      // names the record OF the things.
      audit: 'evidence',
      security: 'evidence',
      compliance: 'evidence',
      attestation: 'evidence',
      reporting: 'evidence',
      ...(viewIds.has('postureExport')
        ? { postureExport: 'evidence' as const }
        : {}),
      // Compares repository revisions; it does not administer the connection.
      sourceDiff: 'evidence',
      // "value" — what it costs and whether it is being used. The thirteen nouns
      // contain no word for money.
      'team-costs': 'value',
      dashboards: 'value',
      adoption: 'value',
    }

    const unnamed = FEATURE_VIEWS.filter((v) => nounsForView(v.id).length === 0)
      .map((v) => v.id)
      .sort()
    expect(
      unnamed,
      `The set of views no noun can name changed.\n` +
        `If a view GAINED a noun, drop it from UNNAMED. If one LOST its last noun, it\n` +
        `just became unfindable by any word an operator already knows — give it back a\n` +
        `noun, or add it here with the class it belongs to and say so in the audit note.`,
    ).toEqual(
      Object.keys(UNNAMED)
        .filter((id) => FEATURE_VIEWS.some((view) => view.id === id))
        .sort(),
    )
  })

  it('translates every noun in all seven languages', () => {
    const missing: string[] = []
    for (const lng of LANGUAGE_CODES) {
      const nav = JSON.parse(
        readFileSync(join(LOCALES_ROOT, lng, 'nav.json'), 'utf8'),
      ) as { nouns?: Record<string, string> }
      for (const n of PRODUCT_NOUNS)
        if (!nav.nouns?.[n.id]?.trim()) missing.push(`${lng}: nouns.${n.id}`)
    }
    expect(
      missing,
      `Untranslated noun search terms:\n  ${missing.join('\n  ')}\n` +
        `A missing noun label silently removes that word from the sidebar filter and\n` +
        `the palette in that language.`,
    ).toEqual([])
  })
})
