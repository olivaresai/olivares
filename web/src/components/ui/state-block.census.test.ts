// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CENSUS OF THE STATE COMPONENTS' CALL SITES, driven by planted mutants before it is
// believed about the tree (the empty-state census's method, and its scanner).
//
// The types already demand the text each component needs, and each component throws at
// render on a blank one. What neither can see is a call site that is never rendered by a
// test: a `reason=""` written in a branch no test reaches ships a crash. This census reads
// every call site in `src` and fails on the literal forms of "nothing":
//   - `<DisabledReason>` without `reason=`, or with an empty literal reason;
//   - `<StatusGlyph>` without `status=`, or with an empty literal `label`;
//   - `<CodeLine>` without `command=`, or with an empty literal command;
//   - `<StateBlock>` without `state=`.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import {
  endOfOpeningTag,
  stripComments,
  topLevelProps,
  walk,
} from '@/test/empty-state-census'

interface Rule {
  component: string
  required: string
  /** A prop whose empty literal is the defect. */
  notEmpty: string
}

const RULES: Rule[] = [
  { component: 'DisabledReason', required: 'reason', notEmpty: 'reason' },
  { component: 'StatusGlyph', required: 'status', notEmpty: 'label' },
  { component: 'CodeLine', required: 'command', notEmpty: 'command' },
  { component: 'StateBlock', required: 'state', notEmpty: 'state' },
]

interface Site {
  where: string
  component: string
  props: string[]
  attrs: string
}

/** True when `prop` is written as an empty or blank literal: "", '', {''}, {""}, {``}. */
export function emptyLiteral(attrs: string, prop: string): boolean {
  const re = new RegExp(
    `(^|\\s)${prop}=(?:"\\s*"|'\\s*'|\\{\\s*(?:'\\s*'|"\\s*"|\`\\s*\`)\\s*\\})`,
  )
  return re.test(attrs)
}

export function sitesIn(
  src: string,
  where: string,
): { sites: Site[]; unparsed: string[] } {
  const sites: Site[] = []
  const unparsed: string[] = []
  const clean = stripComments(src)
  for (const { component } of RULES) {
    const re = new RegExp(`<${component}(?=[\\s/>])`, 'g')
    let m: RegExpExecArray | null
    while ((m = re.exec(clean)) !== null) {
      const line = clean.slice(0, m.index).split('\n').length
      const end = endOfOpeningTag(clean, m.index)
      if (end === -1) {
        unparsed.push(`${where}:${line}`)
        continue
      }
      const attrs = clean.slice(m.index + component.length + 1, end - 1)
      sites.push({
        where: `${where}:${line}`,
        component,
        props: topLevelProps(attrs),
        attrs,
      })
    }
  }
  return { sites, unparsed }
}

export function defects(sites: Site[]): string[] {
  const out: string[] = []
  for (const s of sites) {
    const rule = RULES.find((r) => r.component === s.component)!
    // A spread may carry the prop; it is not a literal nothing, so it is not judged here.
    const spread = /\{\s*\.\.\./.test(s.attrs)
    if (!s.props.includes(rule.required) && !spread)
      out.push(`${s.where} <${s.component}> without ${rule.required}=`)
    if (emptyLiteral(s.attrs, rule.notEmpty))
      out.push(`${s.where} <${s.component}> with an empty ${rule.notEmpty}`)
  }
  return out
}

describe('the state-component census instrument (mutants first)', () => {
  it('sees each literal form of nothing', () => {
    for (const form of [`""`, `''`, `{''}`, `{""}`, '{``}', `" "`, `{' '}`]) {
      expect(emptyLiteral(` disabled reason=${form} `, 'reason'), form).toBe(
        true,
      )
    }
    expect(emptyLiteral(` reason={t('x')} `, 'reason')).toBe(false)
    expect(emptyLiteral(` reason="Approve the plan first." `, 'reason')).toBe(
      false,
    )
    // `data-reason=""` is another prop.
    expect(emptyLiteral(` data-reason="" `, 'reason')).toBe(false)
  })

  it('finds a planted defect of every rule, and passes the planted good ones', () => {
    const planted = [
      `<DisabledReason disabled><Button /></DisabledReason>`,
      `<DisabledReason disabled reason=""><Button /></DisabledReason>`,
      `<StatusGlyph label="Working" />`,
      `<StatusGlyph status="done" label={''} />`,
      `<CodeLine inline />`,
      `<CodeLine command=" " />`,
      `<StateBlock title="x" />`,
      `<DisabledReason disabled reason={t('a')}><Button /></DisabledReason>`,
      `<StatusGlyph status="working" detail={n > 0 ? a : b} />`,
      `<CodeLine command="olivares setup" />`,
      `<StateBlock state="loading" />`,
      `<StateBlock {...props} />`,
    ].join('\n')
    const { sites, unparsed } = sitesIn(planted, 'planted')
    expect(unparsed).toEqual([])
    expect(sites).toHaveLength(12)
    expect(defects(sites)).toEqual([
      'planted:1 <DisabledReason> without reason=',
      'planted:2 <DisabledReason> with an empty reason',
      'planted:3 <StatusGlyph> without status=',
      'planted:4 <StatusGlyph> with an empty label',
      'planted:5 <CodeLine> without command=',
      'planted:6 <CodeLine> with an empty command',
      'planted:7 <StateBlock> without state=',
    ])
  })

  it('does not count a type or a longer name as a call site', () => {
    const { sites } = sitesIn(
      `type P = DisabledReasonProps\nconst a = <StateBlockRow />\n// <StateBlock />`,
      'x',
    )
    expect(sites).toEqual([])
  })
})

describe('every state-component call site in the console carries its text', () => {
  const all = walk('src')
    .filter((f) => !/\.test\.tsx?$/.test(f))
    .map((file) => sitesIn(readFileSync(file, 'utf8'), file))
  const sites = all.flatMap((r) => r.sites)

  it('parsed every opening tag it found', () => {
    expect(all.flatMap((r) => r.unparsed)).toEqual([])
  })

  it('has no call site without its text', () => {
    const found = defects(sites)
    expect(found, found.join('\n')).toEqual([])
  })

  it('is looking at the call sites, not at a shrunken tree', () => {
    // Measured 2026-09-27: 16 call sites, in the components page and in StateBlock itself.
    // The floor guards against zero, not drift.
    for (const { component } of RULES)
      expect(
        sites.filter((s) => s.component === component).length,
        component,
      ).toBeGreaterThanOrEqual(1)
    expect(sites.length).toBeGreaterThanOrEqual(12)
  })
})
