// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A form control is identified by its boundary: an input, a select, a combobox, a text area and
// a checkbox by their border, a switch by its track. WCAG 2.2 SC 1.4.11 asks 3:1 for that
// boundary against what surrounds it, so it is drawn with `ctl-border`, the palette's control
// boundary. The hairline tokens (`border`, `border-strong`, the `*-line` set) are separators and
// container edges only, where no control depends on them to be found.
/// <reference types="node" />
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Checkbox } from './checkbox'
import { Combobox } from './combobox'
import { Input } from './input'
import { Select, SelectTrigger, SelectValue } from './select'
import { Switch } from './switch'
import { Textarea } from './textarea'

const SRC = resolve(__dirname, '../../..')

function block(css: string, selector: string): Record<string, string> {
  const clean = css.replace(/\/\*[\s\S]*?\*\//g, '')
  const head = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const body =
    new RegExp(`(?:^|\\n)${head}\\s*\\{([\\s\\S]*?)\\n\\}`).exec(clean)?.[1] ??
    ''
  const out: Record<string, string> = {}
  for (const m of body.matchAll(/--([\w-]+):\s*([^;]+);/g))
    out[m[1]] = m[2].trim()
  return out
}

function ratio(a: string, b: string): number {
  const lum = (hex: string) => {
    const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255)
    const [r, g, bl] = c.map((v) =>
      v <= 0.04045 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4),
    )
    return 0.2126 * r + 0.7152 * g + 0.0722 * bl
  }
  const [x, y] = [lum(a), lum(b)]
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05)
}

/** `color-mix(in srgb, a p, b)` of two opaque hex colours, as a hex colour. */
function mix(a: string, b: string, p: number): string {
  const ch = (hex: string, i: number) => parseInt(hex.slice(i, i + 2), 16)
  const v = (i: number) => Math.round(ch(a, i) * p + ch(b, i) * (1 - p))
  return '#' + [1, 3, 5].map((i) => v(i).toString(16).padStart(2, '0')).join('')
}

/** The opening tag of every raw <input>, <select> and <textarea> in the console sources. */
function rawFormElements(): { where: string; tag: string }[] {
  const out: { where: string; tag: string }[] = []
  const walk = (d: string) => {
    for (const n of readdirSync(d)) {
      const p = join(d, n)
      if (statSync(p).isDirectory()) walk(p)
      else if (/\.tsx$/.test(n) && !/\.test\.tsx$/.test(n)) {
        const text = readFileSync(p, 'utf8')
        const lines = text.split('\n')
        // `className={selectClass}` is judged by the file's own `const selectClass = '…'`.
        const classes = new Map(
          [...text.matchAll(/^const (\w+) =\s*'([^']*)'/gm)].map((m) => [
            m[1],
            m[2],
          ]),
        )
        lines.forEach((line, i) => {
          if (!/<(input|select|textarea)\b/.test(line)) return
          if (/^\s*(\/\/|\/?\*)/.test(line)) return
          const tag: string[] = []
          for (let j = i; j < lines.length && j < i + 30; j++) {
            tag.push(lines[j])
            // The tag closes at `>` or `/>`; an arrow `=>` inside a handler does not close it.
            if (/(?<!=)\/?>\s*$/.test(lines[j].trim())) break
          }
          out.push({
            where: `${relative(SRC, p)}:${i + 1}`,
            tag: tag
              .join('\n')
              .replace(/className=\{(\w+)\}/, (whole, id: string) =>
                classes.has(id) ? `className="${classes.get(id)}"` : whole,
              ),
          })
        })
      }
    }
  }
  walk(join(SRC, 'src'))
  return out
}

describe('control boundaries use ctl-border', () => {
  it('the text field, the text area and the checkbox draw their border with ctl-border', () => {
    render(
      <>
        <Input aria-label="Name" />
        <Textarea aria-label="Notes" />
        <Checkbox aria-label="Agree" />
      </>,
    )
    for (const name of ['Name', 'Notes', 'Agree']) {
      const el = screen.getByLabelText(name)
      expect(el, name).toHaveClass('border-ctl-border')
      expect(el, name).not.toHaveClass('border-border-strong')
    }
  })

  it('the select and combobox triggers draw their border with ctl-border', () => {
    render(
      <>
        <Select>
          <SelectTrigger aria-label="Region">
            <SelectValue />
          </SelectTrigger>
        </Select>
        <Combobox
          aria-label="Owner"
          options={[]}
          value={null}
          onChange={() => undefined}
        />
      </>,
    )
    const triggers = [
      screen.getByRole('combobox', { name: 'Region' }),
      screen.getByRole('button', { name: 'Owner' }),
    ]
    for (const el of triggers) {
      expect(el).toHaveClass('border-ctl-border')
      expect(el).not.toHaveClass('border-border-strong')
    }
  })

  it('an unchecked switch draws its track with ctl-border', () => {
    render(<Switch aria-label="Notify" />)
    const el = screen.getByRole('switch', { name: 'Notify' })
    expect(el).toHaveClass('bg-ctl-border')
    expect(el).not.toHaveClass('bg-border-strong')
  })

  it('every styled raw form element in the console draws its border with ctl-border', () => {
    const found = rawFormElements()
    // The sweep must see the elements it judges, or an empty census reads as clean.
    expect(found.length).toBeGreaterThanOrEqual(10)
    const judged = found.filter(
      ({ tag }) => !/type="(hidden|file|checkbox|radio)"/.test(tag),
    )
    // A class the sweep cannot resolve is skipped, not judged: name it instead.
    expect(
      judged
        .filter(({ tag }) => /className=\{\w+\}/.test(tag))
        .map(({ where }) => where),
    ).toEqual([])
    const wrong = judged
      .filter(({ tag }) => /className="[^"]*\bborder\b/.test(tag))
      .filter(({ tag }) => !/className="[^"]*\bborder-ctl-border\b/.test(tag))
      .map(({ where }) => where)
    expect(wrong).toEqual([])
  })

  it('ctl-border holds 3:1 against every surface a control sits on, and under the switch knob at rest and hovered', () => {
    const css = readFileSync(join(SRC, 'src/styles/tokens.css'), 'utf8')
    // The knob of a switch that is off is white: the surface in light, the inverse surface in
    // dark and black (the black theme's text ink is grey, 2.9:1 on the track). Under the pointer
    // the track darkens: 12 % of the text ink in light, 8 % of the frame in dark and black,
    // as `color-mix(in srgb, …)` mixes them. The hovered track is still the control's boundary,
    // so it keeps 3:1 against every surface too (12 % of the frame is 2.89:1 on a raised row).
    for (const [name, t, knob, toward, part] of [
      ['light', block(css, ':root'), 'surface', 'text', 0.12],
      ['dark', block(css, '.dark'), 'inverse', 'frame', 0.08],
      ['black', block(css, '.dark.black'), 'inverse', 'frame', 0.08],
    ] as const) {
      const hovered = mix(t[toward], t['ctl-border'], part)
      for (const bg of ['background', 'surface', 'elevated', 'raised']) {
        expect(
          ratio(t['ctl-border'], t[bg]),
          `${name}: ctl-border on ${bg}`,
        ).toBeGreaterThanOrEqual(3)
        expect(
          ratio(hovered, t[bg]),
          `${name}: hovered switch track on ${bg}`,
        ).toBeGreaterThanOrEqual(3)
      }
      expect(
        ratio(t[knob], t['ctl-border']),
        `${name}: switch knob on track`,
      ).toBeGreaterThanOrEqual(3)
      expect(
        ratio(t[knob], hovered),
        `${name}: switch knob on the hovered track`,
      ).toBeGreaterThanOrEqual(3)
    }
  })

  it('a checked checkbox and a switch that is on: an accent edge 3:1 on every surface, a graphite mark 3:1 on the orange', () => {
    // Direction A2: checked, mixed and on are the orange, edged in accent-strong, and
    // everything that sits on the orange is graphite (white on it is 2.7:1). WCAG 2.2
    // SC 1.4.11 asks 3:1 for the edge against what surrounds it, and for the check glyph
    // and the knob on the fill, in both themes.
    const css = readFileSync(join(SRC, 'src/styles/tokens.css'), 'utf8')
    for (const [name, t] of [
      ['light', block(css, ':root')],
      ['dark', block(css, '.dark')],
    ] as const) {
      for (const bg of ['background', 'surface', 'elevated', 'raised'])
        expect(
          ratio(t['accent-strong'], t[bg]),
          `${name}: accent edge on ${bg}`,
        ).toBeGreaterThanOrEqual(3)
      expect(
        ratio(t['on-accent'], t.accent),
        `${name}: graphite mark on the orange`,
      ).toBeGreaterThanOrEqual(3)
    }
  })
})
