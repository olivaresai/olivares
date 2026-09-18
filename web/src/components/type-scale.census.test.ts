// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE TYPE LADDER IS THE ONLY WAY A CONSOLE SURFACE NAMES A SIZE.
//
// ⛔ WHAT THIS EXISTS TO STOP, measured on 2026-09-18 over `web/src` (comments stripped,
//    which is what `stripComments` below is for: a plain grep reads the doc comments that
//    QUOTE the sizes they explain away):
//
//                              before   after the first pass   after the second
//      the ladder                       62        236      2 627
//      hand-written Tailwind         2 576      2 408         16
//
//    A scale nothing reads is not a scale. Every one of those 2 576 is a `font-size`
//    decided on its own, and the four decisions a step carries — size, leading, tracking,
//    weight — drift apart the moment they are spelled separately, which is the defect
//    the page header's own records name for the six page headings that had already drifted.
//
//    The 16 that remain are all in `features/onboarding/onboarding-view.tsx`, the one
//    surface the ladder has not been carried across yet; nothing in it was edited.
//
// ⛔ AND IT IS A RATCHET BY DIRECTORY, NOT ONE VERDICT OVER THE TREE. `AT_THE_STANDARD`
//    lists the directories the ladder has been carried across, whole; adding a row is how a family
//    is closed, and a row cannot be added while the directory still spells a raw size.
//    A single tree-wide assertion would have to be `.skip`ped on the first day and would
//    then measure nothing — the lava flow the console met when `EmptyState.description`
//    became a type rather than a published list of 94.
//
// ⛔ COMMENTS ARE STRIPPED, and that is not caution: the empty-state census reported two
//    bare states inside the doc comment EXPLAINING the rule, on its own first run. The
//    files this scans are the ones whose headers quote `text-xl` and `text-2xl` while
//    describing why they no longer use them. `stripComments` is pinned by its own case.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { TYPE_SCALE_STEPS } from '@/lib/utils'

const SRC = resolve(__dirname, '..')

/**
 * Directories carried across to the ladder, whole. Adding one is the deliverable of a
 * family pass; the assertion below then holds it at zero for good.
 */
const AT_THE_STANDARD = [
  'components/ui',
  'components/data',
  'components/layout',
  // The shell-adjacent features, carried across in the same pass: the nine area
  // directories, the chrome the 27 intelligence views compose with (`IntelPage`,
  // `SectionCard`, `Metric`, the notices), the front door and the work surface.
  'features/navigation',
  'features/_intel',
  'features/home',
  'features/sessions',
  // THE LIST FAMILY, whole. 38 list routes and 7 list-with-tiles routes, which
  // between them are 45 of the console's 75 screens: 1 864 size-preserving swaps
  // (`text-xs`/`text-sm`, which keep their px and gain the ladder's leading) and 53 judged
  // ones, where the step also carries a weight and a tracking. The count is the reason
  // this is a codemod with a control positive and not 204 hand edits.
  'features/adoption',
  'features/agent-artifacts',
  'features/agentops',
  'features/alerting',
  'features/attestation',
  'features/audit',
  'features/automations',
  'features/backups',
  'features/capabilities',
  'features/catalog',
  'features/communications',
  'features/compliance',
  'features/console',
  'features/deploy',
  'features/evals',
  'features/finops',
  'features/governance',
  'features/health',
  'features/identity',
  'features/inventory',
  'features/killswitch',
  'features/knowledge',
  'features/model-ops',
  'features/models',
  'features/observability',
  'features/orchestration',
  'features/platforms',
  'features/rate-limits',
  'features/recordings',
  'features/redteam',
  'features/reporting',
  'features/residency',
  'features/sandbox',
  'features/security',
  'features/team-costs',
  'features/voice',
  // THE DETAIL, FORM, DIALOG and DASHBOARD families, and the shared surfaces under them.
  // `features/onboarding` is the ONE directory still open: all 16 of its remaining raw
  // sizes are in `onboarding-view.tsx`, the onboarding wizard's own screen.
  //
  // DIALOGS ARE NOT A DIRECTORY, and saying so is the honest version of "the dialog
  // family is closed": `components/ui/dialog.tsx`, `sheet.tsx` and `confirm-dialog.tsx`
  // went across in the first pass, and the 91 views that mount one carry their dialog CONTENT inside
  // their own feature directory — so a dialog reaches the ladder with the screen that
  // owns it, and the rows below are where that happened.
  'features/access-map',
  'features/api-playground',
  'features/logs',
  'features/posture-export',
  'features/session-viewer',
  'features/tenants',
  'features/work',
  'features/inference-proxy',
  'features/protocol-bindings',
  'features/workspace-templates',
  'features/claude-policy',
  'features/eventing',
  'features/executive',
  'features/workspace-dashboard',
  'features/shared',
  'features/saved-views',
  'components/charts',
  'app',
] as const

/**
 * Sites that name a raw size ON PURPOSE, each with the reason. A row here is a decision
 * someone has to defend in review — which is the point of writing them down instead of
 * loosening the pattern.
 */
const DECLARED: ReadonlyArray<{ file: string; why: string }> = []

/** Tailwind's own font-size scale — the names `cn` resolved before the ladder existed. */
const RAW_SIZES = [
  'text-xs',
  'text-sm',
  'text-base',
  'text-lg',
  'text-xl',
  'text-2xl',
  'text-3xl',
  'text-4xl',
  'text-5xl',
] as const

/** Line and block comments removed, so prose about a size is not counted as one. */
export function stripComments(source: string): string {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:])\/\/.*$/gm, '$1')
}

function sources(dir: string): string[] {
  const out: string[] = []
  const walk = (d: string) => {
    for (const name of readdirSync(d)) {
      const p = join(d, name)
      if (statSync(p).isDirectory()) {
        walk(p)
        continue
      }
      if (!/\.tsx?$/.test(name)) continue
      if (/\.(test|census\.test)\.tsx?$/.test(name)) continue
      out.push(p)
    }
  }
  walk(resolve(SRC, dir))
  return out
}

/** Every raw size a directory still spells, as `file:line  utility`. */
function rawSizes(dir: string): string[] {
  const found: string[] = []
  for (const file of sources(dir)) {
    const rel = relative(SRC, file)
    if (DECLARED.some((d) => d.file === rel)) continue
    const lines = stripComments(readFileSync(file, 'utf8')).split('\n')
    lines.forEach((line, i) => {
      for (const size of RAW_SIZES) {
        if (new RegExp(`\\b${size}\\b`).test(line))
          found.push(`${rel}:${i + 1}  ${size}`)
      }
    })
  }
  return found
}

describe('the type ladder, across the directories carried to it', () => {
  it.each(AT_THE_STANDARD)('%s names no size outside the ladder', (dir) => {
    expect(rawSizes(dir)).toEqual([])
  })

  it('finds the prose it must NOT count, and the code it must', () => {
    // The control positive. Without `stripComments` the first two lines are findings and
    // this file's own subject becomes unmeasurable.
    const sample = [
      '// the header said `text-xl` before the ladder consolidated it',
      '/* and `text-2xl` in settings */',
      "const a = cn('text-sm')",
    ].join('\n')
    const stripped = stripComments(sample)
    expect(stripped).not.toContain('text-xl')
    expect(stripped).not.toContain('text-2xl')
    expect(stripped).toContain('text-sm')
  })

  it('scans a real number of files, so an empty sweep cannot read as clean', () => {
    // A glob that matches nothing passes every assertion above. This is the floor that
    // says the sweep happened: measured 2026-09-18 after the family passes, the 61
    // directories below hold 703 of `web/src`'s 706 non-test sources — everything but
    // `features/onboarding`, the onboarding wizard's own screen.
    const total = AT_THE_STANDARD.reduce((n, d) => n + sources(d).length, 0)
    expect(total).toBeGreaterThanOrEqual(650)
  })

  it('offers a step for every role a surface needs to name', () => {
    // Seven steps, and the census below is the evidence that they are enough: seven
    // directories were carried across without one exception row.
    expect(TYPE_SCALE_STEPS).toHaveLength(7)
    expect(DECLARED).toEqual([])
  })
})
