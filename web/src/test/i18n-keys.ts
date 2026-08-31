// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Assert that a rendered subtree shows no UNRESOLVED i18n key. When i18next cannot
// resolve a key it returns the key itself, so the screen prints `assurance.stepUpBody`
// where a sentence belongs — and every existing assertion (`getByRole('button')`,
// snapshot-free text queries on OTHER strings) stays green while the operator reads
// an identifier. This is the DOM-side companion of scripts/check-i18n-namespaces.mjs:
// the script proves a chunk registers what it translates, this proves what actually
// reached the screen.
//
// Two independent detections, because either alone has a hole:
//   1. EXACT — the text equals a key path present in some en.json bundle. Zero false
//      positives, and it catches a key whose namespace was never registered.
//   2. SHAPE — the text LOOKS like a key (`items.routinePolicies`) even though no
//      bundle declares it, which is what a stale/mistyped key looks like: nothing to
//      match against, so (1) is blind to it.
// Legitimate dotted text (a hostname, a filename, a version) is passed via `allow`.
import { expect } from 'vitest'

/** Every key path declared by an English bundle: `stepUpTitle`, `assurance.aal.aal1`, … */
const declaredKeys: Set<string> = (() => {
  const bundles = import.meta.glob(
    ['@/lib/i18n/locales/en/*.json', '@/features/**/i18n/en.json'],
    { eager: true, import: 'default' },
  ) as Record<string, unknown>
  const keys = new Set<string>()
  const walk = (value: unknown, prefix: string) => {
    if (prefix) keys.add(prefix)
    if (!value || typeof value !== 'object' || Array.isArray(value)) return
    for (const [key, child] of Object.entries(value as Record<string, unknown>))
      walk(child, prefix ? `${prefix}.${key}` : key)
  }
  for (const bundle of Object.values(bundles)) walk(bundle, '')
  return keys
})()

// A dotted lowerCamelCase identifier: `assurance.stepUpTitle`, `items.routinePolicies`.
// Every segment after the first must START with a letter, so versions (`v26.8.0`),
// decimals and IPs are not keys. No whitespace anywhere.
const KEY_SHAPE = /^[a-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)+$/

/** Attributes an assistive technology reads aloud — a raw key hides there too. */
const TEXTUAL_ATTRIBUTES = ['aria-label', 'title', 'placeholder', 'alt']

export interface RawKeyHit {
  /** 'exact' = a key some bundle declares; 'shape' = it merely looks like one. */
  kind: 'exact' | 'shape'
  text: string
  where: string
}

/** Every raw-key-looking string rendered inside `root` (text nodes + a11y attributes). */
export function findRawI18nKeys(
  root: HTMLElement,
  allow: readonly string[] = [],
): RawKeyHit[] {
  const allowed = new Set(allow)
  const hits: RawKeyHit[] = []
  const consider = (raw: string, where: string) => {
    const text = raw.trim()
    // A dot is required by BOTH rules: a single-segment key that failed to resolve
    // ("refresh", "pending") is indistinguishable from legitimate copy, and treating
    // every bare word that happens to be a key path as a hit would be noise, not a
    // gate. Namespaced keys — the class this catches — always carry one.
    if (!text || !text.includes('.') || allowed.has(text)) return
    if (declaredKeys.has(text)) hits.push({ kind: 'exact', text, where })
    else if (KEY_SHAPE.test(text)) hits.push({ kind: 'shape', text, where })
  }

  const walker = document.createTreeWalker(
    root,
    NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT,
  )
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    if (node.nodeType === Node.TEXT_NODE) {
      const owner = node.parentElement
      consider(node.textContent ?? '', owner ? describe(owner) : '<text>')
      continue
    }
    const element = node as Element
    for (const attribute of TEXTUAL_ATTRIBUTES) {
      const value = element.getAttribute(attribute)
      if (value !== null) consider(value, `${describe(element)}[${attribute}]`)
    }
  }
  return hits
}

function describe(element: Element): string {
  const classes = element.className
  return typeof classes === 'string' && classes
    ? `<${element.tagName.toLowerCase()} class="${classes.slice(0, 60)}">`
    : `<${element.tagName.toLowerCase()}>`
}

/**
 * Fail if the subtree shows any unresolved i18n key. Pass legitimately dotted copy
 * (hostnames, filenames) in `allow` — narrowly, one literal at a time.
 */
export function expectNoRawI18nKeys(
  root: HTMLElement,
  allow: readonly string[] = [],
): void {
  const hits = findRawI18nKeys(root, allow)
  expect(
    hits.map((h) => `${h.kind}: "${h.text}" in ${h.where}`),
    'unresolved i18n key(s) rendered — the namespace is not registered in this chunk',
  ).toEqual([])
}
