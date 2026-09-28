// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The console's type is self-hosted: Geist Variable for the interface and JetBrains Mono
// 400/500 for machine text, both under the SIL Open Font License 1.1. This pins what ships:
// the exact font files, their license texts next to them in the built console, the notice
// that credits them, and the first-paint budget — two files, at most 95 KB, the medium mono
// weight declared but loaded only when a page asks for it.
/// <reference types="node" />
import { createHash } from 'node:crypto'
import { existsSync, readFileSync, statSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

// vitest runs with cwd = web/.
const FONTS = 'src/assets/fonts'
const GEIST = `${FONTS}/geist/Geist-Variable.woff2`
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

describe('self-hosted type: Geist and JetBrains Mono', () => {
  it('ships the three font files, byte for byte the published releases', () => {
    // Geist 1.7.2 (geist-sans/Geist-Variable.woff2) and JetBrains Mono 5.2.8 Latin 400/500.
    expect(sha256(GEIST)).toBe(
      'a369fcf5628ea2aa4e1b9e2ec6a5b3624e365bda588e1f0f2f12b564f728fbb8',
    )
    expect(sha256(MONO_400)).toBe(
      '14425ba9c695763c1547f48a206b7aa60350a33ae23de09f0407877f3fcd89eb',
    )
    expect(sha256(MONO_500)).toBe(
      'cb182feeed4d798ff6961d3c79f7026279448fca0676438aaecb21f3fc39553a',
    )
  })

  it('ships each font license in the built console and credits both in NOTICE', () => {
    const geist = readFileSync(`${LICENSES}/Geist-OFL.txt`, 'utf8')
    const mono = readFileSync(`${LICENSES}/JetBrainsMono-OFL.txt`, 'utf8')
    for (const text of [geist, mono]) {
      expect(text).toContain('SIL Open Font License, Version 1.1')
      expect(text).toContain(
        'SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007',
      )
    }
    expect(geist).toContain(
      'Copyright (c) 2023 Vercel, in collaboration with basement.studio',
    )
    expect(mono).toContain('Copyright 2020 The JetBrains Mono Project Authors')
    const notice = readFileSync('../NOTICE', 'utf8')
    expect(notice).toContain('Geist')
    expect(notice).toContain('JetBrains Mono')
    expect(notice).toContain('web/public/licenses/fonts/Geist-OFL.txt')
    expect(notice).toContain('web/public/licenses/fonts/JetBrainsMono-OFL.txt')
  })

  it('declares Geist as one variable face and JetBrains Mono at 400 and 500', () => {
    const faces = fontFaces(readFileSync('src/styles/fonts.css', 'utf8'))
    expect(faces).toHaveLength(3)
    const geist = faces.filter((f) => f['font-family'] === "'Geist'")
    const mono = faces.filter((f) => f['font-family'] === "'JetBrains Mono'")
    expect(geist).toHaveLength(1)
    expect(geist[0]['font-weight']).toBe('100 900')
    expect(geist[0].src).toContain('../assets/fonts/geist/Geist-Variable.woff2')
    expect(mono.map((f) => f['font-weight']).sort()).toEqual(['400', '500'])
    for (const f of faces) {
      expect(f['font-display']).toBe('swap')
      expect(f['font-style']).toBe('normal')
      expect(f.src).toMatch(/format\('woff2'\)$/)
    }
    // The mono files are the Latin subset: other scripts fall back to the system mono.
    for (const f of mono) expect(f['unicode-range']).toMatch(/^U\+0000-00FF, /)
  })

  it('loads two files on first paint, within 95 KB, and the mono 500 only on demand', () => {
    const html = readFileSync('index.html', 'utf8')
    const preloads = [
      ...html.matchAll(/<link\b[^>]*\brel="preload"[^>]*>/g),
    ].map((m) => m[0])
    const fonts = preloads.filter((l) => /\bas="font"/.test(l))
    expect(fonts).toHaveLength(2)
    for (const l of fonts) {
      expect(l).toContain('type="font/woff2"')
      expect(l).toMatch(/\bcrossorigin\b/)
    }
    const hrefs = fonts.map((l) => /href="([^"]+)"/.exec(l)![1]).sort()
    expect(hrefs).toEqual([
      '/src/assets/fonts/geist/Geist-Variable.woff2',
      '/src/assets/fonts/jetbrains-mono/jetbrains-mono-latin-400-normal.woff2',
    ])
    const firstPaint = statSync(GEIST).size + statSync(MONO_400).size
    expect(firstPaint).toBeLessThanOrEqual(95_000)
    expect(html).not.toContain('jetbrains-mono-latin-500-normal')
  })

  it('stops loading Inter, Space Grotesk and the old variable mono', () => {
    const main = readFileSync('src/main.tsx', 'utf8')
    expect(main).not.toMatch(/@fontsource/)
    const index = readFileSync('src/index.css', 'utf8')
    expect(stripComments(index)).toContain("@import './styles/fonts.css';")
    for (const css of [index, readFileSync('src/styles/fonts.css', 'utf8')])
      for (const old of ['Inter', 'Space Grotesk', 'fontsource'])
        expect(stripComments(css)).not.toContain(old)
    expect(
      existsSync(GEIST) && existsSync(MONO_400) && existsSync(MONO_500),
    ).toBe(true)
  })
})
