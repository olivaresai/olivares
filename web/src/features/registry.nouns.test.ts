// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SECOND LANGUAGE. The sidebar is headed by five VERBS's question —
// "¿puede un ingeniero VER y GESTIONAR … sesiones, agentes, conexiones, identidades,
// modelos, reglas, automatizaciones, grupos, estados, workflows, tareas, protocolos e
// infraestructura?" (canon §0, answered in §5 of the audit) — is thirteen NOUNS.
//
// A navigation explicable in only one of the two has lost the other, and the way that
// loss happens is silent: a noun stops being pointable when the last view claiming it
// is renamed, and no heading changes. These assertions are the tripwire.
//
// The hub↔noun spans are DECLARED here, not asserted away. Two nouns reach three hubs
// each; that is a real finding about the vocabulary (written up in
// an internal design note (not shipped)) and pinning it means a
// future change that widens or narrows a span has to say so on purpose.
import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { LANGUAGE_CODES } from '@/lib/i18n'
import {
  FEATURE_VIEWS,
  HUB_ORDER,
  PRODUCT_NOUNS,
  nounsForView,
  type HubId,
} from './registry'

const LOCALES_ROOT = resolve(__dirname, '../lib/i18n/locales')
const viewIds = new Set(FEATURE_VIEWS.map((v) => v.id))
const hubOf = new Map(FEATURE_VIEWS.map((v) => [v.id, v.hub]))

/**
 * Nouns that legitimately live under more than one hub, and every hub they reach.
 *
 * This IS the "trece sustantivos contra cinco hubs" tension, held as data. Only four of
 * the thirteen sit in a single hub; `agents` reaches FOUR. That is the measurement, not
 * a defect to tidy away — a noun cutting across four job-shaped headings is precisely
 * why the nouns cannot BE the headings and have to be the search index instead.
 */
const DECLARED_SPANS: Record<string, HubId[]> = {
  // Run one, administer the roster, contain it, prove what it shipped, attack it.
  agents: ['operate', 'govern', 'prove', 'connect'],
  // The CLIENT's estate, OUR plane's own runtime, and where bytes may legally sit.
  infrastructure: ['connect', 'operate', 'govern'],
  // Catalogue/route them, cap them, price them, score them.
  models: ['connect', 'govern', 'prove'],
  sessions: ['operate', 'prove'],
  connections: ['connect', 'govern'],
  groups: ['govern', 'operate'],
  tasks: ['operate', 'automate'],
  protocols: ['connect', 'operate'],
}

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

  it('sends each noun to the hub its PRIMARY view actually sits in', () => {
    const wrong = PRODUCT_NOUNS.filter(
      (n) => hubOf.get(n.views[0]) !== n.hub,
    ).map((n) => `${n.id}: declared ${n.hub}, primary view ${n.views[0]} is in ${hubOf.get(n.views[0])}`)
    expect(
      wrong,
      `A noun advertises a hub that does not hold its first view:\n  ${wrong.join('\n  ')}`,
    ).toEqual([])
  })

  it('declares every hub a noun spans, so a widened span is deliberate', () => {
    const actual = Object.fromEntries(
      PRODUCT_NOUNS.map((n) => [
        n.id,
        [...new Set(n.views.map((v) => hubOf.get(v)!))],
      ]).filter(([, hubs]) => (hubs as HubId[]).length > 1),
    )
    expect(
      actual,
      `The hub spread of the nouns changed. This is the "trece sustantivos contra cinco\n` +
        `hubs" tension, and it is meant to be visible: update DECLARED_SPANS *and* the\n` +
        `audit note if a noun genuinely moved, rather than filing it under whichever hub\n` +
        `objects least.`,
    ).toEqual(DECLARED_SPANS)
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
      postureExport: 'evidence',
      attestation: 'evidence',
      reporting: 'evidence',
      // "value" — what it costs and whether it is being used. Neither the thirteen
      // nouns nor the five hub verbs contain a word for money.
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
    ).toEqual(Object.keys(UNNAMED).sort())
  })

  it('translates every hub and every noun in all seven languages', () => {
    const missing: string[] = []
    for (const lng of LANGUAGE_CODES) {
      const nav = JSON.parse(
        readFileSync(join(LOCALES_ROOT, lng, 'nav.json'), 'utf8'),
      ) as { hubs?: Record<string, string>; nouns?: Record<string, string> }
      for (const h of HUB_ORDER)
        if (!nav.hubs?.[h]?.trim()) missing.push(`${lng}: hubs.${h}`)
      for (const n of PRODUCT_NOUNS)
        if (!nav.nouns?.[n.id]?.trim()) missing.push(`${lng}: nouns.${n.id}`)
    }
    expect(
      missing,
      `Untranslated hub headings / noun search terms:\n  ${missing.join('\n  ')}\n` +
        `A missing hub label paints the raw key as a section heading; a missing noun\n` +
        `label silently removes that word from the sidebar filter in that language.`,
    ).toEqual([])
  })
})

describe('the five hubs', () => {
  it('assigns every registered view to a hub in the render order', () => {
    const stray = FEATURE_VIEWS.filter((v) => !HUB_ORDER.includes(v.hub)).map(
      (v) => `${v.id}: ${v.hub}`,
    )
    expect(stray, `Views in a hub that HUB_ORDER never renders — invisible in the sidebar:\n  ${stray.join('\n  ')}`).toEqual([])
  })

  it('leaves no hub empty', () => {
    const empty = HUB_ORDER.filter(
      (h) => !FEATURE_VIEWS.some((v) => v.hub === h && !v.hideInNav),
    )
    expect(empty, `Hubs that would render as a heading with nothing under it: ${empty.join(', ')}`).toEqual([])
  })
})
