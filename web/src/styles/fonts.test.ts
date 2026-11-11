// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The console's type is self-hosted and is GNOME's: Adwaita Sans (one variable face, weights
// 100-900) for every text of the interface and its titles, Adwaita Mono 400/700 for machine
// text, both under the SIL Open Font License 1.1. This pins what ships: the exact font files,
// their license text next to them in the built console, the notice that credits them, and the
// first-paint budget — two files, at most 130 KB, the bold mono declared but loaded only when
// a page asks.
/// <reference types="node" />
import { createHash } from 'node:crypto'
import { existsSync, readFileSync, statSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

// vitest runs with cwd = web/.
const FONTS = 'src/assets/fonts'
const SANS = `${FONTS}/adwaita-sans/adwaita-sans-latin-wght-normal.woff2`
const MONO_400 = `${FONTS}/adwaita-mono/adwaita-mono-latin-400-normal.woff2`
const MONO_700 = `${FONTS}/adwaita-mono/adwaita-mono-latin-700-normal.woff2`
const LICENSE = 'public/licenses/fonts/AdwaitaFonts-OFL.txt'
// The Latin subset every face declares and every file holds; other scripts fall back to the
// system face of their kind.
const LATIN =
  'U+0000-00FF, U+0131, U+0152-0153, U+02BB-02BC, U+02C6, U+02DA, U+02DC, U+0304, U+0308, U+0329, U+2000-206F, U+20AC, U+2122, U+2191, U+2193, U+2212, U+2215, U+FEFF, U+FFFD'

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

describe('self-hosted type: Adwaita Sans and Adwaita Mono', () => {
  it('ships the three font files, the Latin subsets of adwaita-fonts 49.0', () => {
    // Made once from https://download.gnome.org/sources/adwaita-fonts/49/adwaita-fonts-49.0.tar.xz
    // (sha256 3157c620eb5b72b25ab156d194aa4eb223f9870d547fe83fdbdf06d3e7becb37) with fontTools
    // 4.66.1 and brotli 1.2.0, unmodified but for the subset:
    //   pyftsubset <ttf> --unicodes=<LATIN> --layout-features='*' --name-IDs='*' --flavor=woff2
    // sans/AdwaitaSans-Regular.ttf  8381c33b9a44f066f2b99dba3d416a2342891e28c956a35dfd8d16ee2987e6d4
    // mono/AdwaitaMono-Regular.ttf  3c1b7072fe32be57cd7a603513e6caf5af498011bbf12372aee5fc790a3762f5
    // mono/AdwaitaMono-Bold.ttf     0754849ae9c9c2b672ecacd4a00bfe0b1f2597b28ab4b60607848dcd9322c032
    expect(sha256(SANS)).toBe(
      'a85cd485afd1dd1c7a4df6b8bece3bdd976e971c9996a4104c26a6c9bdfc0695',
    )
    expect(sha256(MONO_400)).toBe(
      '0c91e911219e213e011a92436f4dab2e2ef08921ee37f79c7b37ac8bdc682911',
    )
    expect(sha256(MONO_700)).toBe(
      '57e3ebaab8822b71cea3e7d02951618f459921255d9fc4339bc5ec5d419b3f06',
    )
  })

  it('ships the font license in the built console and credits both faces in NOTICE', () => {
    const text = readFileSync(LICENSE, 'utf8')
    expect(text).toContain('SIL Open Font License, Version 1.1')
    expect(text).toContain(
      'SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007',
    )
    for (const line of [
      'Copyright (c) 2016, The Inter Project Authors',
      'Copyright (c) 2015-2025, Renzhi Li',
      'Copyright (c) 2025, Jamie Gravendeel',
      'Copyright (c) 2025, Florian Müllner',
    ])
      expect(text).toContain(line)
    const notice = readFileSync('../NOTICE', 'utf8')
    for (const name of ['Adwaita Sans', 'Adwaita Mono'])
      expect(notice).toContain(name)
    expect(notice).toContain(`web/${LICENSE}`)
    for (const old of [
      'Inter-OFL.txt',
      'SpaceGrotesk-OFL.txt',
      'JetBrainsMono-OFL.txt',
    ]) {
      expect(notice).not.toContain(old)
      expect(existsSync(`public/licenses/fonts/${old}`)).toBe(false)
    }
  })

  it('declares Adwaita Sans as one variable face and Adwaita Mono at 400 and 700', () => {
    const faces = fontFaces(readFileSync('src/styles/fonts.css', 'utf8'))
    expect(faces).toHaveLength(3)
    const family = (name: string) =>
      faces.filter((f) => f['font-family'] === `'${name}'`)
    const sans = family('Adwaita Sans')
    expect(sans).toHaveLength(1)
    expect(sans[0]['font-weight']).toBe('100 900')
    const mono = family('Adwaita Mono')
    expect(mono.map((f) => f['font-weight']).sort()).toEqual(['400', '700'])
    for (const f of faces) {
      expect(f['font-display']).toBe('swap')
      expect(f['font-style']).toBe('normal')
      expect(f.src).toMatch(/format\('woff2'\)$/)
      expect(f['unicode-range']).toBe(LATIN)
    }
    const srcs = faces.map((f) => /url\('\.\.\/(.+?)'\)/.exec(f.src)![1])
    expect(srcs).toEqual(
      [SANS, MONO_400, MONO_700].map((p) => p.replace('src/', '')),
    )
  })

  it('loads two files on first paint, within 130 KB, and the bold mono only on demand', () => {
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
    const hrefs = fonts.map((l) => /href="([^"]+)"/.exec(l)![1])
    expect(hrefs).toEqual([`/${SANS}`, `/${MONO_400}`])
    // 128,916 bytes measured: Adwaita Sans keeps both axes as drawn (opsz 14-32, wght
    // 100-900). The three earlier faces fit 95 KB; these two do not.
    const firstPaint = statSync(SANS).size + statSync(MONO_400).size
    expect(firstPaint).toBeLessThanOrEqual(130_000)
    expect(html).not.toContain('adwaita-mono-latin-700-normal')
  })

  it('loads no other face: no Inter, Space Grotesk, JetBrains Mono, Geist or fontsource', () => {
    const index = readFileSync('src/index.css', 'utf8')
    expect(stripComments(index)).toContain("@import './styles/fonts.css';")
    for (const file of [
      'src/styles/fonts.css',
      'src/index.css',
      'index.html',
      'src/main.tsx',
    ])
      expect(readFileSync(file, 'utf8'), file).not.toMatch(
        /\bInter\b|Space Grotesk|JetBrains Mono|Geist|fontsource|inter-latin|space-grotesk|jetbrains-mono/,
      )
    for (const old of ['inter', 'space-grotesk', 'jetbrains-mono', 'geist'])
      expect(existsSync(`${FONTS}/${old}`), old).toBe(false)
    expect([SANS, MONO_400, MONO_700].every((f) => existsSync(f))).toBe(true)
  })

  it('sets the text as drawn: no forced feature, no synthesized bold, emphasis still slanted', () => {
    // Bold comes from the variable face's real weights. No italic face ships, so <em>, the
    // italic classes and italic code comments keep the slant the browser draws.
    const index = stripComments(readFileSync('src/index.css', 'utf8'))
    const body = /\bbody\s*\{([^}]*)\}/.exec(index)![1]
    expect(body).toMatch(/font-feature-settings:\s*normal;/)
    expect(body).toMatch(/font-synthesis-weight:\s*none;/)
    expect(index).not.toMatch(/font-synthesis:\s*none/)
    expect(index).not.toMatch(/font-synthesis-style:\s*none/)
    for (const m of index.matchAll(/font-feature-settings:\s*([^;]+);/g))
      expect(m[1]).toBe('normal')
  })

  it('shows code as typed: no ligature turns >= into one glyph in machine text', () => {
    // Adwaita Mono has no ligature (HarfBuzz shapes >=, !=, -> and => in these files to one
    // glyph per character), but a fallback face of the mono stack may have one; a command or
    // a script must show the characters it holds. Commands in tool rows are spans with the
    // font-mono utility, not code elements.
    const index = stripComments(readFileSync('src/index.css', 'utf8'))
    const off = [...index.matchAll(/([^{}]+)\{([^}]*)\}/g)]
      .filter((m) => /font-variant-ligatures:\s*none;/.test(m[2]))
      .flatMap((m) => m[1].split(',').map((s) => s.trim()))
    for (const selector of ['code', 'kbd', 'samp', 'pre', '.font-mono'])
      expect(off).toContain(selector)
  })
})
