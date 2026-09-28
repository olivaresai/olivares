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

/** The opening tag of every raw <input>, <select> and <textarea> in the console sources. */
function rawFormElements(): { where: string; tag: string }[] {
  const out: { where: string; tag: string }[] = []
  const walk = (d: string) => {
    for (const n of readdirSync(d)) {
      const p = join(d, n)
      if (statSync(p).isDirectory()) walk(p)
      else if (/\.tsx$/.test(n) && !/\.test\.tsx$/.test(n)) {
        const lines = readFileSync(p, 'utf8').split('\n')
        lines.forEach((line, i) => {
          if (!/<(input|select|textarea)\b/.test(line)) return
          const tag: string[] = []
          for (let j = i; j < lines.length && j < i + 30; j++) {
            tag.push(lines[j])
            if (/\/?>\s*$/.test(lines[j].trim()) && j > i) break
            if (j === i && /\/?>\s*$/.test(lines[j].trim())) break
          }
          out.push({
            where: `${relative(SRC, p)}:${i + 1}`,
            tag: tag.join('\n'),
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
    const wrong = found
      .filter(({ tag }) => !/type="(hidden|file|checkbox|radio)"/.test(tag))
      .filter(({ tag }) => /className="[^"]*\bborder\b/.test(tag))
      .filter(({ tag }) => !/className="[^"]*\bborder-ctl-border\b/.test(tag))
      .map(({ where }) => where)
    expect(wrong).toEqual([])
  })

  it('ctl-border holds 3:1 against every surface a control sits on, and under the switch thumb', () => {
    const css = readFileSync(join(SRC, 'src/styles/tokens.css'), 'utf8')
    for (const [name, t] of [
      ['light', block(css, ':root')],
      ['dark', block(css, '.dark')],
    ] as const) {
      for (const bg of ['background', 'surface', 'elevated', 'raised'])
        expect(
          ratio(t['ctl-border'], t[bg]),
          `${name}: ctl-border on ${bg}`,
        ).toBeGreaterThanOrEqual(3)
      expect(
        ratio(t.surface, t['ctl-border']),
        `${name}: switch thumb on track`,
      ).toBeGreaterThanOrEqual(3)
    }
  })
})
