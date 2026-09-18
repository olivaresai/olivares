// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE FILTER ROW KEEPS FILTERS, AND THE HEADER KEEPS THE VERB.
//
// ⛔ WHAT THIS EXISTS TO STOP, measured on 2026-09-18 over `web/src`:
//      `PageHeader.primaryAction`, the header's named verb slot  —  0 views of 61
//      page headers whose LAST control is a ghost *Refresh*     — 10
//      create/act verbs sitting inside a `DataTable` filter row — 12, in 11 rows
//    A screen that ends its header with *Refresh* and hides *New entry* between two
//    status filters has told the operator that refreshing is what this page is for.
//    The named slot exists precisely so the difference can be MEASURED — a single
//    `actions` bag looks the same to a census whether it holds a range picker or a verb
//    — and until this pass nothing used it.
//
// ⛔ AND IT IS TWO ASSERTIONS, NOT ONE, because the defect has two halves and closing
//    only the first is the cargo-cult version: a verb can leave the filter row and still
//    land nowhere an operator looks. `filter rows` below is the mechanical half (zero
//    exceptions, it is a syntactic property). `NO_VERB` is the judged half: a screen
//    whose engine offers nothing to create is CORRECT to carry no primary action, and
//    saying so in a list with a reason is the only way that is distinguishable from
//    having forgotten.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const SRC = resolve(__dirname, '../..')

/**
 * Feature directories whose page header offers NO primary action, each with the reason
 * it offers none. A row here is a decision someone has to defend in review; the list is
 * the report's decision table, in the tree, where it cannot drift from the code.
 */
const NO_VERB: ReadonlyArray<{ dir: string; why: string }> = [
  {
    dir: 'features/access-map',
    why: 'a computed reachability graph; the engine offers no create',
  },
  {
    dir: 'features/adoption',
    why: 'a read-only adoption report over a date range',
  },
  {
    dir: 'features/agentops',
    why: 'a provider administration read; every write is per row',
  },
  {
    dir: 'features/api-playground',
    why: 'the verb IS the request the operator composes in the form',
  },
  {
    dir: 'features/attestation',
    why: 'attestations are produced by the engine, never by the console',
  },
  {
    dir: 'features/audit',
    why: 'an append-only ledger: nothing on this screen creates a row',
  },
  {
    dir: 'features/automations',
    why: 'runs start from a workflow row; the page creates nothing',
  },
  {
    dir: 'features/claude-policy',
    why: 'one policy document, edited in place',
  },
  {
    dir: 'features/communications',
    why: 'composing is a route of its own (/communications/new)',
  },
  {
    dir: 'features/compliance',
    why: 'seven regimes in one screen; a page verb could not say which',
  },
  {
    dir: 'features/console',
    why: 'eleven tabs of administration; every write is scoped to a row',
  },
  { dir: 'features/evals', why: 'an evaluation is launched from a suite row' },
  {
    dir: 'features/eventing',
    why: 'the subscriptions tab holds three sections; the verb stays with its card',
  },
  {
    dir: 'features/executive',
    why: 'a printed report; its controls are a range and an export',
  },
  {
    dir: 'features/finops',
    why: 'the cost-centre and budget tabs hold two lists each; a single slot could not say which',
  },
  {
    dir: 'features/health',
    why: 'a live status read; the engine offers no create',
  },
  {
    dir: 'features/home',
    why: 'the front door sends the operator somewhere; it acts nowhere itself',
  },
  {
    dir: 'features/identity',
    why: 'four tabs of federated estate; every write is per row',
  },
  {
    dir: 'features/inference-proxy',
    why: 'the DLP rule belongs to its section, one of five on the page',
  },
  {
    dir: 'features/inventory',
    why: 'a discovered estate: rows come from the engine, never from here',
  },
  {
    dir: 'features/logs',
    why: 'a live tail of what the engine writes; the console creates no log line',
  },
  {
    dir: 'features/navigation',
    why: 'an area directory lists routes; it owns no resource',
  },
  { dir: 'features/observability', why: 'a read of signals the engine emits' },
  {
    dir: 'features/onboarding',
    why: 'a wizard: the verb is the step the operator is on',
  },
  {
    dir: 'features/platforms',
    why: 'a platform inventory the engine discovers',
  },
  {
    dir: 'features/posture-export',
    why: 'the verb is the export the form composes',
  },
  {
    dir: 'features/rate-limits',
    why: 'limits are read from configuration, not created here',
  },
  {
    dir: 'features/recordings',
    why: 'recordings are produced by sessions, never by this screen',
  },
  { dir: 'features/redteam', why: 'a campaign starts from a scenario row' },
  {
    dir: 'features/reporting',
    why: 'a report is generated from a template row',
  },
  {
    dir: 'features/residency',
    why: 'a residency registry read; the write is a per-region control',
  },
  {
    dir: 'features/security',
    why: 'five tabs of findings; the engine offers no create',
  },
  {
    dir: 'features/session-viewer',
    why: 'a detail screen: verify and summarise are secondary, and it has no list',
  },
  {
    dir: 'features/team-costs',
    why: 'a cost read; its controls are a saved view and an export',
  },
  {
    dir: 'features/tenants',
    why: 'tenants are created by the control plane, not by this screen',
  },
  {
    dir: 'features/work',
    why: 'work items arrive from sessions; the console decides them per row',
  },
  {
    dir: 'features/workspace-dashboard',
    why: 'a dashboard over one workspace',
  },
]

function sources(dir: string): string[] {
  const out: string[] = []
  const walk = (d: string) => {
    for (const name of readdirSync(d)) {
      const p = join(d, name)
      if (statSync(p).isDirectory()) {
        walk(p)
        continue
      }
      if (!/\.tsx$/.test(name) || /\.test\.tsx$/.test(name)) continue
      out.push(p)
    }
  }
  walk(resolve(SRC, dir))
  return out
}

/** The brace-balanced value of every `toolbar=` prop in a source. */
export function toolbarValues(source: string): string[] {
  const out: string[] = []
  let from = 0
  for (;;) {
    const at = source.indexOf('toolbar=', from)
    if (at < 0) break
    const open = at + 'toolbar='.length
    if (source[open] !== '{') {
      from = open
      continue
    }
    let depth = 0
    let j = open
    for (; j < source.length; j++) {
      if (source[j] === '{') depth++
      else if (source[j] === '}') {
        depth--
        if (depth === 0) break
      }
    }
    out.push(source.slice(open + 1, j))
    from = j + 1
  }
  return out
}

/** `<Button` but not `<ButtonGroup`: a control that ACTS, inside the row that filters. */
const BUTTON = /<Button(?![A-Za-z])/g

const FEATURES = sources('features')

describe('the page header owns the verb, the filter row owns the filters', () => {
  it('no filter row holds a button', () => {
    const found: string[] = []
    let rows = 0
    for (const file of FEATURES) {
      const source = readFileSync(file, 'utf8')
      for (const value of toolbarValues(source)) {
        rows++
        const n = (value.match(BUTTON) ?? []).length
        if (n > 0) found.push(`${relative(SRC, file)}  ${n} button(s)`)
      }
    }
    // The floor: a parser that returns nothing passes the assertion above for free.
    // 15 filter rows measured 2026-09-18, after this pass emptied eleven of them.
    expect(rows).toBeGreaterThanOrEqual(12)
    expect(found).toEqual([])
  })

  it('finds a button in a filter row when one is there', () => {
    // The control positive for the assertion above. Without it, a `toolbarValues` that
    // silently stopped matching would read as a clean console.
    const planted = `<DataTable toolbar={<><Select /><Button>New</Button></>} />`
    const values = toolbarValues(planted)
    expect(values).toHaveLength(1)
    expect(values[0].match(BUTTON)).toHaveLength(1)
  })

  it('every screen with a page header either offers a verb or says why not', () => {
    const declared = new Set(NO_VERB.map((r) => r.dir))
    const missing: string[] = []
    const stale: string[] = []
    const withHeader = new Set<string>()
    for (const file of FEATURES) {
      const source = readFileSync(file, 'utf8')
      if (!/<(PageHeader|IntelPage)(?=[\s/>])/.test(source)) continue
      const dir = relative(SRC, dirname(file))
      // `_intel` IS the primitive that forwards the slot, not a screen.
      if (dir === 'features/_intel') continue
      withHeader.add(dir.split('/').slice(0, 2).join('/'))
    }
    for (const dir of withHeader) {
      const offers = sources(dir).some((f) => {
        const s = readFileSync(f, 'utf8')
        return /primaryAction=/.test(s) || /<PagePrimaryAction>/.test(s)
      })
      if (!offers && !declared.has(dir)) missing.push(dir)
      if (offers && declared.has(dir)) stale.push(dir)
    }
    expect(missing).toEqual([])
    // A screen that GAINED a verb must lose its row, or the list stops meaning anything.
    expect(stale).toEqual([])
    expect(withHeader.size).toBeGreaterThanOrEqual(50)
  })

  it('every declared screen carries a reason', () => {
    expect(NO_VERB.filter((r) => r.why.trim().length < 20)).toEqual([])
  })
})
