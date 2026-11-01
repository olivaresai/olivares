// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The console's type is self-hosted and is the website's: Inter for text, Space Grotesk for
// titles and figures, JetBrains Mono 400/500 for machine text, all under the SIL Open Font
// License 1.1. This pins what ships: the exact font files, their license texts next
// to them in the built console, the notice that credits them, and the first-paint budget —
// three files, at most 95 KB, the medium mono weight declared but loaded only when a page asks.
/// <reference types="node" />
import { createHash } from 'node:crypto'
import { existsSync, readFileSync, statSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

// vitest runs with cwd = web/.
const FONTS = 'src/assets/fonts'
const INTER = `${FONTS}/inter/inter-latin-wght-normal.woff2`
const GROTESK = `${FONTS}/space-grotesk/space-grotesk-latin-wght-normal.woff2`
const MONO_400 = `${FONTS}/jetbrains-mono/jetbrains-mono-latin-400-normal.woff2`
const MONO_500 = `${FONTS}/jetbrains-mono/jetbrains-mono-latin-500-normal.woff2`
const LICENSES = 'public/licenses/fonts'

const sha256 = (p: string) =>
  createHash('sha256').update(readFileSync(p)).digest('hex')
const stripComments = (css: string) => css.replace(/\/\*[\s\S]*?\*\//g, '')

/** Every @font-face rule of a stylesheet, as a property map. */
function fontFaces(css: string): Record<string, string>[] {
  return [...stripComments(css).matchAll(/@font-face\s*\{([^}]*)\}/g)].map(
    (m) => {
      const out: Record<string, string> = {}
      for (const d of m[1].matchAll(/([\w-]+)\s*:\s*([^;]+);/g))
        out[d[1]] = d[2].replace(/\s+/g, ' ').trim()
      return out
    },
  )
}

describe('self-hosted type: Inter, Space Grotesk and JetBrains Mono', () => {
  it('ships the four font files, byte for byte the published releases', () => {
    // Fontsource 5.3.0 variable builds of Inter and Space Grotesk (Latin, wght axis) and
    // JetBrains Mono 5.2.8 Latin 400/500.
    expect(sha256(INTER)).toBe(
      '3100e775e8616cd2611beecfa23a4263d7037586789b43f035236a2e6fbd4c62',
    )
    expect(sha256(GROTESK)).toBe(
      '0640890476fc1198ab4de571fb658de443c4d85b66466ec09534a8737ab1ce9d',
    )
    expect(sha256(MONO_400)).toBe(
      '14425ba9c695763c1547f48a206b7aa60350a33ae23de09f0407877f3fcd89eb',
    )
    expect(sha256(MONO_500)).toBe(
      'cb182feeed4d798ff6961d3c79f7026279448fca0676438aaecb21f3fc39553a',
    )
  })

  it('ships each font license in the built console and credits all three in NOTICE', () => {
    const inter = readFileSync(`${LICENSES}/Inter-OFL.txt`, 'utf8')
    const grotesk = readFileSync(`${LICENSES}/SpaceGrotesk-OFL.txt`, 'utf8')
    const mono = readFileSync(`${LICENSES}/JetBrainsMono-OFL.txt`, 'utf8')
    for (const text of [inter, grotesk, mono]) {
      expect(text).toContain('SIL Open Font License, Version 1.1')
      expect(text).toContain(
        'SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007',
      )
    }
    expect(inter).toContain('Copyright 2016 The Inter Project Authors')
    expect(grotesk).toContain(
      'Copyright 2020 The Space Grotesk Project Authors',
    )
    expect(mono).toContain('Copyright 2020 The JetBrains Mono Project Authors')
    const notice = readFileSync('../NOTICE', 'utf8')
    for (const name of ['Inter', 'Space Grotesk', 'JetBrains Mono'])
      expect(notice).toContain(name)
    for (const file of [
      'Inter-OFL.txt',
      'SpaceGrotesk-OFL.txt',
      'JetBrainsMono-OFL.txt',
    ])
      expect(notice).toContain(`web/public/licenses/fonts/${file}`)
  })

  it('declares Inter and Space Grotesk as one variable face each and JetBrains Mono at 400 and 500', () => {
    const faces = fontFaces(readFileSync('src/styles/fonts.css', 'utf8'))
    expect(faces).toHaveLength(4)
    const family = (name: string) =>
      faces.filter((f) => f['font-family'] === `'${name}'`)
    const [inter] = family('Inter')
    const [grotesk] = family('Space Grotesk')
    expect(family('Inter')).toHaveLength(1)
    expect(family('Space Grotesk')).toHaveLength(1)
    expect(inter['font-weight']).toBe('100 900')
    expect(inter.src).toContain(
      '../assets/fonts/inter/inter-latin-wght-normal.woff2',
    )
    expect(grotesk['font-weight']).toBe('300 700')
    expect(grotesk.src).toContain(
      '../assets/fonts/space-grotesk/space-grotesk-latin-wght-normal.woff2',
    )
    const mono = family('JetBrains Mono')
    expect(mono.map((f) => f['font-weight']).sort()).toEqual(['400', '500'])
    for (const f of faces) {
      expect(f['font-display']).toBe('swap')
      expect(f['font-style']).toBe('normal')
      expect(f.src).toMatch(/format\('woff2'\)$/)
      // Every file is the Latin subset: other scripts fall back to the system face.
      expect(f['unicode-range']).toMatch(/^U\+0000-00FF, /)
    }
  })

  it('loads three files on first paint, within 95 KB, and the mono 500 only on demand', () => {
    const html = readFileSync('index.html', 'utf8')
    const preloads = [
      ...html.matchAll(/<link\b[^>]*\brel="preload"[^>]*>/g),
    ].map((m) => m[0])
    const fonts = preloads.filter((l) => /\bas="font"/.test(l))
    expect(fonts).toHaveLength(3)
    for (const l of fonts) {
      expect(l).toContain('type="font/woff2"')
      expect(l).toMatch(/\bcrossorigin\b/)
    }
    const hrefs = fonts.map((l) => /href="([^"]+)"/.exec(l)![1]).sort()
    expect(hrefs).toEqual([`/${INTER}`, `/${MONO_400}`, `/${GROTESK}`].sort())
    const firstPaint =
      statSync(INTER).size + statSync(GROTESK).size + statSync(MONO_400).size
    expect(firstPaint).toBeLessThanOrEqual(95_000)
    expect(html).not.toContain('jetbrains-mono-latin-500-normal')
  })

  it('loads no other face: no Geist, no fontsource import', () => {
    const main = readFileSync('src/main.tsx', 'utf8')
    expect(main).not.toMatch(/@fontsource/)
    const index = readFileSync('src/index.css', 'utf8')
    expect(stripComments(index)).toContain("@import './styles/fonts.css';")
    for (const css of [index, readFileSync('src/styles/fonts.css', 'utf8')])
      for (const old of ['Geist', 'fontsource'])
        expect(stripComments(css)).not.toContain(old)
    expect(existsSync(`${FONTS}/geist`)).toBe(false)
    expect(
      [INTER, GROTESK, MONO_400, MONO_500].every((f) => existsSync(f)),
    ).toBe(true)
  })
})
