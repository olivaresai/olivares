// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE RULE THAT KEEPS A CALL SITE FROM RESTYLING A PRIMITIVE (#3135).
//
// A primitive in `components/ui` decides its own colour, size and padding, and offers
// variants for the choices a screen may make. A call site that adds `bg-accent`, `h-9` or
// `px-2` to it is a second decision about the same thing, and the console drifts one such
// class at a time. `eslint-rules/no-primitive-restyle.js` flags it, and a counted baseline
// holds the sites that already did: a count can only go down, because the rule fails on a
// file that is over its count AND on a file that is under it (lower the number).
//
// This file drives the rule with `RuleTester` (what it flags and what it leaves alone) and pins
// the baseline file (sorted, real files, positive counts). Whether ESLint resolves the rule for
// the product's files is a todo at the end: see the note there.
/// <reference types="node" />
import { existsSync, readFileSync } from 'node:fs'
import { relative, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { ESLint, RuleTester, type Rule } from 'eslint'
import { describe, expect, it, vi } from 'vitest'

// vitest runs with cwd = web/.
const WEB = process.cwd()
const RULE_FILE = resolve(WEB, 'eslint-rules/no-primitive-restyle.js')
const BASELINE_FILE = resolve(
  WEB,
  'eslint-rules/primitive-restyle-baseline.json',
)

// A dynamic import of a computed URL: the rule is plain JavaScript beside the other lint
// rules, outside `src`, and the type checker has no declaration for it (nor needs one).
const mod = (await import(pathToFileURL(RULE_FILE).href)) as {
  default: Rule.RuleModule
  restyleClasses: (
    className: string,
    options?: { boxSize?: boolean },
  ) => string[]
}
const rule = mod.default

const tester = new RuleTester({
  languageOptions: {
    ecmaVersion: 'latest',
    sourceType: 'module',
    parserOptions: { ecmaFeatures: { jsx: true } },
  },
})

const IMPORTS = `
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import * as Dialog from '@/components/ui/dialog'
import { Mine } from './mine'
`
const jsx = (body: string) => `${IMPORTS}\nexport const X = () => (${body})`

describe('restyleClasses: colour, size and padding utilities of a class string', () => {
  const found = (s: string) => mod.restyleClasses(s)

  it('names the colour, size and padding utilities', () => {
    expect(found('bg-accent text-text-2 border-bad ring-focus')).toEqual([
      'bg-accent',
      'text-text-2',
      'border-bad',
      'ring-focus',
    ])
    expect(found('text-caption text-[11px] text-metadata size-4 h-9')).toEqual([
      'text-caption',
      'text-[11px]',
      'text-metadata',
      'size-4',
      'h-9',
    ])
    expect(found('p-2 px-3 py-1 pt-0 pb-1 pl-2 pr-2 ps-1 pe-1')).toHaveLength(9)
  })

  it('sees through variants, the important marker and arbitrary variants', () => {
    expect(found('hover:bg-hover dark:text-text !p-0 md:px-4')).toEqual([
      'hover:bg-hover',
      'dark:text-text',
      '!p-0',
      'md:px-4',
    ])
    expect(found('[&_svg]:size-4 data-[state=open]:bg-active')).toEqual([
      '[&_svg]:size-4',
      'data-[state=open]:bg-active',
    ])
  })

  it('leaves layout, shape, weight and the text alignment classes alone', () => {
    expect(
      found(
        'w-full min-w-0 max-w-sm flex-1 shrink-0 gap-2 m-2 mt-1 truncate font-mono font-medium ' +
          'text-left text-center text-wrap text-ellipsis border border-0 border-t border-dashed ' +
          'ring-0 ring-offset-2 rounded-card uppercase tabular-nums h-full h-auto size-full size-auto',
      ),
    ).toEqual([])
  })
})

describe('restyleClasses: the edges of each family', () => {
  const found = (s: string, o?: { boxSize?: boolean }) =>
    mod.restyleClasses(s, o)

  it('leaves the settings of a background, a stroke, a border and a ring alone', () => {
    expect(
      found(
        'bg-cover bg-center bg-no-repeat bg-clip-text stroke-2 border-x border-[2px] border-spacing-2 ' +
          'ring-inset ring-offset-2 ring-offset-[3px] text-balance text-shadow-sm',
      ),
    ).toEqual([])
  })

  it('names the colour of a background, a fill, a stroke, a ring and its offset', () => {
    expect(
      found(
        'bg-accent fill-bad stroke-accent ring-offset-bad border-t-bad p-0!',
      ),
    ).toEqual([
      'bg-accent',
      'fill-bad',
      'stroke-accent',
      'ring-offset-bad',
      'border-t-bad',
      'p-0!',
    ])
  })

  it('leaves the box size to a primitive the caller sizes, and still names its colour', () => {
    expect(found('h-4 size-4 w-32', { boxSize: false })).toEqual([])
    expect(found('h-4 bg-accent px-2', { boxSize: false })).toEqual([
      'bg-accent',
      'px-2',
    ])
    expect(found('h-4 size-4', { boxSize: true })).toEqual(['h-4', 'size-4'])
  })
})

tester.run('olivares/no-primitive-restyle', rule, {
  valid: [
    {
      name: 'a placeholder sized by the caller, as its own contract says',
      code: `import { Skeleton } from '@/components/ui/skeleton'\nexport const X = () => <Skeleton className="h-4 w-32" />`,
    },
    {
      name: 'a component from components/layout is not a primitive',
      code: `import { Sidebar } from '@/components/layout/sidebar'\nexport const X = () => <Sidebar className="bg-surface p-3" />`,
    },
    {
      name: 'a primitive that takes its look from its variants',
      code: jsx(`<Button variant="primary" size="sm">Save</Button>`),
    },
    {
      name: 'layout classes on a primitive',
      code: jsx(
        `<Button className="w-full min-w-0 shrink-0 truncate">Save</Button>`,
      ),
    },
    {
      name: 'colour, size and padding on an element that is not a primitive',
      code: jsx(
        `<div className="bg-surface p-3 text-text-2 text-caption">x</div>`,
      ),
    },
    {
      name: 'a component that is not from components/ui',
      code: jsx(`<Mine className="bg-surface p-3">x</Mine>`),
    },
    {
      name: 'a primitive with no class at all',
      code: jsx(`<Badge variant="success" />`),
    },
    {
      name: 'an inherited baseline key is not an allowance for this file',
      code: jsx(`<Button variant="primary">x</Button>`),
      filename: 'src/features/a/inherited.tsx',
      options: [
        {
          baseline: Object.create({
            'src/features/a/inherited.tsx': 1,
          }) as Record<string, number>,
        },
      ],
    },
    {
      name: 'a file under its baseline count is quiet while the count is exact',
      code: jsx(`<Button className="px-2">x</Button>`),
      filename: 'src/features/a/one.tsx',
      options: [{ baseline: { 'src/features/a/one.tsx': 1 } }],
    },
  ],
  invalid: [
    {
      name: 'a placeholder keeps its own colour and padding',
      code: `import { Skeleton } from '@/components/ui/skeleton'\nexport const X = () => <Skeleton className="h-4 bg-accent" />`,
      errors: [
        {
          messageId: 'restyle',
          data: { name: 'Skeleton', classes: 'bg-accent' },
        },
      ],
    },
    {
      name: 'a primitive imported under another name',
      code: `import { Button as Action } from '@/components/ui/button'\nexport const X = () => <Action className="bg-accent" />`,
      errors: [
        {
          messageId: 'restyle',
          data: { name: 'Action', classes: 'bg-accent' },
        },
      ],
    },
    {
      name: 'a colour on a primitive',
      code: jsx(`<Button className="bg-accent">x</Button>`),
      errors: [
        {
          messageId: 'restyle',
          data: { name: 'Button', classes: 'bg-accent' },
        },
      ],
    },
    {
      name: 'a padding on a primitive',
      code: jsx(`<Badge className="px-2">x</Badge>`),
      errors: [{ messageId: 'restyle' }],
    },
    {
      name: 'a size on a primitive',
      code: jsx(`<Button className="h-9 text-caption">x</Button>`),
      errors: [
        {
          messageId: 'restyle',
          data: { name: 'Button', classes: 'h-9, text-caption' },
        },
      ],
    },
    {
      name: 'one report per element, however many classes restyle it',
      code: jsx(`<Button className="bg-accent px-2 h-9 text-bad">x</Button>`),
      errors: [{ messageId: 'restyle' }],
    },
    {
      name: 'a variant prefix does not hide it',
      code: jsx(`<Button className="hover:bg-hover dark:text-text">x</Button>`),
      errors: [{ messageId: 'restyle' }],
    },
    {
      name: 'a class inside cn() with a condition',
      code: jsx(
        `<Button className={cn('w-full', open && 'bg-active')}>x</Button>`,
      ),
      errors: [
        {
          messageId: 'restyle',
          data: { name: 'Button', classes: 'bg-active' },
        },
      ],
    },
    {
      name: 'a class inside a template literal',
      code: jsx('<Button className={`flex ${gap} p-2`}>x</Button>'),
      errors: [{ messageId: 'restyle' }],
    },
    {
      name: 'a class in a ternary',
      code: jsx(`<Button className={on ? 'text-accent' : 'w-full'}>x</Button>`),
      errors: [{ messageId: 'restyle' }],
    },
    {
      name: 'a compound primitive member (Dialog.Content)',
      code: jsx(`<Dialog.Content className="p-0">x</Dialog.Content>`),
      errors: [
        {
          messageId: 'restyle',
          data: { name: 'Dialog.Content', classes: 'p-0' },
        },
      ],
    },
    {
      name: 'a file over its baseline count reports the site beyond the count',
      code: jsx(
        `<>\n<Button className="px-2">a</Button>\n<Button className="px-3">b</Button>\n</>`,
      ),
      filename: 'src/features/a/two.tsx',
      options: [{ baseline: { 'src/features/a/two.tsx': 1 } }],
      errors: [{ messageId: 'restyle', line: 9 }],
    },
    {
      name: 'a file under its baseline count must lower it',
      code: jsx(`<Button className="px-2">a</Button>`),
      filename: 'src/features/a/three.tsx',
      options: [{ baseline: { 'src/features/a/three.tsx': 3 } }],
      errors: [
        { messageId: 'baselineStale', data: { allowed: '3', found: '1' } },
      ],
    },
    {
      name: 'a file with a baseline row and no site left must drop the row',
      code: jsx(`<Button variant="primary">a</Button>`),
      filename: 'src/features/a/four.tsx',
      options: [{ baseline: { 'src/features/a/four.tsx': 2 } }],
      errors: [
        { messageId: 'baselineStale', data: { allowed: '2', found: '0' } },
      ],
    },
  ],
})

describe('the baseline lookup', () => {
  it('does not accept an inherited count as the file allowance', () => {
    const file = 'src/features/a/inherited.tsx'
    const baseline = Object.create({ [file]: 1 }) as Record<string, number>
    const report = vi.fn()
    const visitors = rule.create({
      options: [{ baseline }],
      cwd: WEB,
      filename: file,
      report,
    } as unknown as Rule.RuleContext)
    visitors['Program:exit']?.({} as never)
    expect(report).not.toHaveBeenCalled()
  })
})

describe('the baseline of sites that already restyle a primitive', () => {
  const baseline = JSON.parse(readFileSync(BASELINE_FILE, 'utf8')) as Record<
    string,
    number
  >
  const files = Object.keys(baseline)

  it('is sorted, so a diff shows a row moving and not a reshuffle', () => {
    expect(files).toEqual([...files].sort())
  })

  it('holds only positive whole counts', () => {
    for (const [file, n] of Object.entries(baseline))
      expect(Number.isInteger(n) && n > 0, `${file}: ${n}`).toBe(true)
  })

  it('names files that exist, are not tests and are not primitives themselves', () => {
    for (const file of files) {
      expect(existsSync(resolve(WEB, file)), file).toBe(true)
      expect(file, file).toMatch(/^src\/.+\.tsx$/)
      expect(file, file).not.toMatch(/\.test\.tsx$/)
      expect(file, file).not.toMatch(/^src\/components\/ui\//)
    }
  })

  it('names files that still import a primitive, so a row cannot outlive its cause', () => {
    for (const file of files)
      expect(readFileSync(resolve(WEB, file), 'utf8'), file).toMatch(
        /from '@\/components\/ui\//,
      )
  })
})

// The baseline against the tree it was measured on. Without this the file could drift from the
// code (a row deleted, a count lowered, fifty new sites) and every test above would stay green:
// the rule is run in memory, under a plugin name of its own, over every component that is not a
// primitive or a test, with the committed baseline, and it must report nothing.
describe('the rule against the real tree and the committed baseline', () => {
  it('reports nothing: every count in the baseline is exact', async () => {
    const baseline = JSON.parse(readFileSync(BASELINE_FILE, 'utf8'))
    const eslint = new ESLint({
      cwd: WEB,
      overrideConfig: [
        {
          files: ['src/**/*.{ts,tsx}'],
          ignores: ['src/components/ui/**', 'src/**/*.test.{ts,tsx}'],
          plugins: { prv: { rules: { 'no-primitive-restyle': rule } } },
          rules: { 'prv/no-primitive-restyle': ['error', { baseline }] },
        },
      ],
    })
    const results = await eslint.lintFiles(['src/**/*.tsx'])
    expect(results.length).toBeGreaterThan(400)
    const reports = results.flatMap((r) =>
      r.messages
        .filter((m) => m.ruleId === 'prv/no-primitive-restyle')
        .map((m) => `${relative(WEB, r.filePath)}:${m.line} ${m.messageId}`),
    )
    expect(reports).toEqual([])
  }, 240_000)
})

// ponytail: the rule is not registered in eslint.config.js yet. That file is write-protected
// for the worker that wrote the rule (the config-protection hook refuses any edit to it), so
// registering it is the one step left for whoever may touch the config. When it lands, replace
// this todo with the two checks below, which ask ESLint itself how it resolves the rule:
//   - an error with `{ baseline }` (the committed file) for src/features/home/live-cards.tsx
//     and src/components/layout/app-layout.tsx;
//   - off for src/components/ui/button.tsx and src/features/home/live-cards.test.tsx;
// using `new ESLint({ cwd: WEB }).calculateConfigForFile(file)`.
describe('the rule as ESLint resolves it for this repository', () => {
  it.todo(
    'is an error on the product files with the committed baseline, and off for the primitives and the tests (blocked: eslint.config.js is write-protected for this worker)',
  )
})
