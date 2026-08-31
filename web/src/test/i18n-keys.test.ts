// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The raw-key detector is itself a gate, so it has to be shown to discriminate in
// BOTH directions: it must catch the shapes that actually shipped (a declared key
// whose namespace was never registered; a key no bundle declares at all) and it must
// stay quiet on the dotted text a console legitimately prints — versions, hosts,
// filenames, sentences. A detector that only ever says "clean" is not a gate.
import { describe, expect, it } from 'vitest'
import { findRawI18nKeys } from './i18n-keys'

function dom(html: string): HTMLElement {
  const root = document.createElement('div')
  root.innerHTML = html
  return root
}

describe('findRawI18nKeys', () => {
  it('catches a DECLARED key rendered verbatim (namespace not registered)', () => {
    // The exact string the first-boot wizard printed: it IS a key path of
    // features/identity/i18n/en.json, so no shape heuristic is needed to be sure.
    const hits = findRawI18nKeys(dom('<h2>assurance.stepUpTitle</h2>'))
    expect(hits).toEqual([
      { kind: 'exact', text: 'assurance.stepUpTitle', where: '<h2>' },
    ])
  })

  it('catches a key NO bundle declares (the stale/mistyped case)', () => {
    // Nothing to match against, so only the shape rule can see this one.
    const hits = findRawI18nKeys(dom('<span>absentNamespace.someKey</span>'))
    expect(hits).toEqual([
      { kind: 'shape', text: 'absentNamespace.someKey', where: '<span>' },
    ])
  })

  it('catches a raw nav key whether or not a bundle happens to declare it', () => {
    // The sidebar/breadcrumb/⌘K class: `t('items.<id>')` built from the registry id.
    // Whether that key is declared decides only WHICH rule fires — never whether the
    // operator is looking at an identifier, so the kind is deliberately not pinned.
    const hits = findRawI18nKeys(dom('<a>items.routinePolicies</a>'))
    expect(hits.map((h) => h.text)).toEqual(['items.routinePolicies'])
  })

  it('reads the attributes an assistive technology announces', () => {
    const hits = findRawI18nKeys(
      dom('<button aria-label="graph.controls.zoomIn"></button>'),
    )
    expect(hits.map((h) => h.text)).toEqual(['graph.controls.zoomIn'])
    expect(hits[0].where).toContain('[aria-label]')
  })

  it('stays quiet on resolved copy and on legitimate dotted text', () => {
    const hits = findRawI18nKeys(
      dom(`
        <p>Step-up authentication required</p>
        <p>Managing identity requires AAL3 (hardware, phishing-resistant).</p>
        <code>v26.8.0</code>
        <code>10.0.0.1</code>
        <span>12.5</span>
        <span>Resolve 2 validation issues before saving.</span>
      `),
    )
    expect(hits).toEqual([])
  })

  it('honours a narrow allowlist for text that must contain a dot', () => {
    const html = '<code>olivares.ai</code>'
    expect(findRawI18nKeys(dom(html)).map((h) => h.text)).toEqual([
      'olivares.ai',
    ])
    expect(findRawI18nKeys(dom(html), ['olivares.ai'])).toEqual([])
  })

  it('does NOT flag a single-segment key — a documented blind spot, not an oversight', () => {
    // `refresh` is a real key path in several bundles, and also a plausible label.
    // Flagging it would make the detector cry wolf on ordinary copy; the namespaced
    // form (the class that shipped) always carries a dot.
    expect(findRawI18nKeys(dom('<span>refresh</span>'))).toEqual([])
  })
})
