// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE RAW COLOR GATE. The console colors through the design tokens (src/styles/tokens.css).
// A raw palette class, an arbitrary color value or a bare color string bypasses them: it
// paints the same in Light, Dark and Black, so a grey stays grey on the black theme, and the
// contrast tests in tokens.test.ts never measure it. So:
//   - the rule flags all three, and leaves token classes, var() values and prose alone;
//   - the real eslint.config.js turns it on for every console source file, off only for the
//     files whose colors must stay literal, each named;
//   - every one-line exception names its reason;
//   - the gate lints the whole source tree, and both CI workflows run the gate.
/// <reference types="node" />
import { spawnSync } from 'node:child_process'
import {
  copyFileSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { ESLint, RuleTester } from 'eslint'
import { describe, expect, it } from 'vitest'
import rule from '../../eslint-rules/no-raw-palette.js'

const WEB = join(__dirname, '..', '..')
const read = (path: string) => readFileSync(join(WEB, '..', path), 'utf8')

new RuleTester({
  languageOptions: {
    ecmaVersion: 'latest',
    sourceType: 'module',
    parserOptions: { ecmaFeatures: { jsx: true } },
  },
}).run('no-raw-palette', rule, {
  valid: [
    "const c = 'text-danger border-danger-line bg-warning-soft'",
    "const c = 'bg-[var(--color-canvas)] text-[13px] rounded-[6px]'",
    "const c = 'grid-cols-[minmax(0,1fr)_auto] shadow-[var(--shadow-1)]'",
    "const note = 'Fixed in #578, see #3160'",
    "const target = '#main-content'",
    "const notAColor = '#abcdef0'",
    'const color = `var(--color-${tone})`',
    // A color function over a token follows the token.
    'const ink = `color-mix(in oklab, var(--text) ${share}, transparent)`',
    "const c = 'bg-[color-mix(in_oklab,var(--color-accent)_20%,transparent)]'",
    // 'black' is a word here (the Black theme's stored value), not a color.
    "const theme = 'black'",
    "const s = { color: 'currentColor', fill: 'none' }",
    'const icon = <path fill="currentColor" stroke="none" />',
    // A compound value over tokens or with no color in it.
    "const s = { border: '1px solid var(--color-line)', boxShadow: 'none' }",
    "const s = { boxShadow: '0 0 0 1px color-mix(in oklab, var(--color-accent) 20%, transparent)' }",
    "const s = { border: '1px solid transparent', outline: '2px dotted currentColor' }",
    // Words in a property that holds no color, and arbitrary values that are not colors.
    "const s = { gridTemplateAreas: 'red blue' }",
    "const c = 'text-[length:var(--size)] bg-[url(/a.png)] w-[tan(1deg)]'",
    // Branches and templates that carry no raw color.
    "const s = { color: ok ? 'currentColor' : 'inherit' }",
    'const s = { border: `1px solid ${line}` }',
    'const icon = <path fill={`url(#grad-${key})`} />',
    // A one-word arbitrary value that is not a color name.
    "const c = 'bg-[auto] border-[thin]'",
  ],
  invalid: [
    {
      code: "const c = 'text-emerald-600 dark:text-emerald-400'",
      errors: [{ messageId: 'rawPalette' }, { messageId: 'rawPalette' }],
    },
    {
      code: "const hue = 'size-5 bg-[#5b8def]'",
      errors: [{ messageId: 'rawColor', data: { cls: 'bg-[#5b8def]' } }],
    },
    {
      code: 'const c = `border-[rgba(26,18,6,.25)] ${extra}`',
      errors: [
        { messageId: 'rawColor', data: { cls: 'border-[rgba(26,18,6,.25)]' } },
      ],
    },
    {
      code: "const c = 'hover:dark:text-[oklch(70%_0.1_40)]'",
      errors: [{ messageId: 'rawColor' }],
    },
    {
      code: "const sheet = { background: '#141414' }",
      errors: [{ messageId: 'rawColor', data: { cls: '#141414' } }],
    },
    {
      code: "const ink = 'rgb(0 0 0 / 0.5)'",
      errors: [{ messageId: 'rawColor' }],
    },
    {
      code: "const view = <div style={{ color: '#fff' }} />",
      errors: [{ messageId: 'rawColor', data: { cls: '#fff' } }],
    },
    {
      code: "const qr = 'rounded bg-white p-2'",
      errors: [{ messageId: 'rawPalette', data: { cls: 'bg-white' } }],
    },
    {
      code: "const badge = 'border-0 hover:bg-black/20'",
      errors: [{ messageId: 'rawPalette', data: { cls: 'hover:bg-black/20' } }],
    },
    {
      code: "const view = <div style={{ backgroundColor: 'Tomato' }} />",
      errors: [{ messageId: 'rawColor', data: { cls: 'Tomato' } }],
    },
    {
      code: 'const icon = <svg><path fill="black" /></svg>',
      errors: [{ messageId: 'rawColor', data: { cls: 'black' } }],
    },
    // A color inside a compound value of a style property.
    {
      code: "const s = { border: '1px solid #ccc' }",
      errors: [{ messageId: 'rawColor', data: { cls: '1px solid #ccc' } }],
    },
    {
      code: "const s = { boxShadow: '0 0 0 1px #000, 0 2px 4px rgba(0,0,0,.2)' }",
      errors: [{ messageId: 'rawColor' }],
    },
    {
      code: "const view = <div style={{ outline: '2px dotted tomato' }} />",
      errors: [{ messageId: 'rawColor', data: { cls: '2px dotted tomato' } }],
    },
    {
      code: "const s = { borderBottom: '1px solid white' }",
      errors: [{ messageId: 'rawColor' }],
    },
    {
      code: "const s = { textShadow: '0 1px hsl(0 0% 0% / .4)' }",
      errors: [{ messageId: 'rawColor' }],
    },
    // A named color in a Tailwind arbitrary value.
    {
      code: "const c = 'text-[red] p-2'",
      errors: [{ messageId: 'rawColor', data: { cls: 'text-[red]' } }],
    },
    {
      code: "const c = 'hover:bg-[tomato]/50'",
      errors: [{ messageId: 'rawColor' }],
    },
    // A color in a branch, an expression container or a template, not only a plain literal.
    {
      code: "const view = <div style={{ color: ok ? 'green' : 'tomato' }} />",
      errors: [
        { messageId: 'rawColor', data: { cls: 'green' } },
        { messageId: 'rawColor', data: { cls: 'tomato' } },
      ],
    },
    {
      code: "const icon = <path fill={'red'} />",
      errors: [{ messageId: 'rawColor', data: { cls: 'red' } }],
    },
    {
      code: "const s = { outline: focused && '2px solid red' }",
      errors: [{ messageId: 'rawColor', data: { cls: '2px solid red' } }],
    },
    {
      code: 'const s = { border: `1px solid #ccc` }',
      errors: [{ messageId: 'rawColor', data: { cls: '1px solid #ccc' } }],
    },
    // The edges of each list: a two-digit shade, a utility with a side or a part, and
    // every color key, each reported once.
    {
      code: "const c = 'bg-slate-50 from-rose-500 border-t-amber-500 ring-offset-white'",
      errors: [
        { messageId: 'rawPalette' },
        { messageId: 'rawPalette' },
        { messageId: 'rawPalette' },
        { messageId: 'rawPalette' },
      ],
    },
    {
      code: 'const icon = <path stroke="red" />',
      errors: [{ messageId: 'rawColor', data: { cls: 'red' } }],
    },
    {
      code: "const s = { borderTopColor: 'red', stopColor: 'navy' }",
      errors: [{ messageId: 'rawColor' }, { messageId: 'rawColor' }],
    },
    {
      code: "const s = { background: 'tomato' }",
      errors: [{ messageId: 'rawColor', data: { cls: 'tomato' } }],
    },
    {
      code: "const s = { color: 'red' || fallback }",
      errors: [{ messageId: 'rawColor', data: { cls: 'red' } }],
    },
  ],
})

describe('the raw color gate covers the console source', () => {
  const eslint = new ESLint({ cwd: WEB })
  const level = async (file: string) => {
    const config = await eslint.calculateConfigForFile(file)
    const entry = config?.rules?.['olivares/no-raw-palette'] as
      [number | string] | undefined
    return entry?.[0]
  }

  it('is on for every console source file, not only features/', async () => {
    for (const file of [
      'src/components/layout/topbar.tsx',
      'src/app/routes.tsx',
      'src/lib/format.ts',
      'src/features/sessions/action-name.tsx',
      // The legacy exceptions are gone: these two features color through the tokens now.
      'src/features/api-playground/response-panel.tsx',
      'src/features/backups/restore-dialog.tsx',
    ])
      expect(await level(file), file).toBe(2)
  })

  it('is off only where a color must stay literal, each file named', async () => {
    for (const file of [
      // The theme previews paint each theme's own colors, whichever theme is active.
      'src/app/pages/settings.tsx',
      // The chart fallback before CSS resolves, pinned to the dark tokens by tokens.test.ts.
      'src/components/charts/chart-theme.ts',
      // WebGL cannot read CSS variables; this fallback mirrors chart-theme's.
      'src/features/shared/graph/sigma-graph-build.ts',
      // The five account hues, the same in every theme by design.
      'src/components/ui/mono-mark.tsx',
    ])
      expect(await level(file), file).toBe(0)
  })

  it('is on for every other source file, so no exception glob widens', async () => {
    const off: string[] = []
    for (const f of readdirSync(join(WEB, 'src'), {
      recursive: true,
      encoding: 'utf8',
    })) {
      const file = `src/${f}`
      if (!/\.tsx?$/.test(file) || /\.test\.tsx?$/.test(file)) continue
      if (/^src\/(?:test|dev)\//.test(file)) continue
      if ((await level(file)) !== 2) off.push(file)
    }
    expect(off.sort()).toEqual([
      'src/app/pages/settings.tsx',
      'src/components/charts/chart-theme.ts',
      'src/components/ui/mono-mark.tsx',
      'src/features/shared/graph/sigma-graph-build.ts',
      // Generated API types, which eslint.config.js never lints.
      'src/lib/api/openapi.gen.ts',
    ])
  }, 120_000)

  it('leaves tests, the test setup and the dev-only components page alone', async () => {
    for (const file of [
      'src/styles/tokens.test.ts',
      'src/test/setup.ts',
      'src/dev/components-page/components-page.tsx',
    ])
      expect(await level(file), file).toBeUndefined()
  })

  it('names the reason of every one-line exception', () => {
    const src = join(WEB, 'src')
    const directives = readdirSync(src, { recursive: true, encoding: 'utf8' })
      .filter((f) => /\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f))
      .flatMap((f) =>
        readFileSync(join(src, f), 'utf8')
          .split('\n')
          .filter((l) => /eslint-disable.*olivares\/no-raw-palette/.test(l))
          .map((l) => `${f}: ${l.trim()}`),
      )
    expect(directives.length).toBeGreaterThan(0)
    for (const d of directives)
      expect(d, d).toMatch(/no-raw-palette -- \S.{10,}/)
  })

  it('has no directive that disables every rule, this one included', () => {
    const src = join(WEB, 'src')
    const bare = readdirSync(src, { recursive: true, encoding: 'utf8' })
      .filter((f) => /\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f))
      .flatMap((f) =>
        readFileSync(join(src, f), 'utf8')
          .split('\n')
          .filter((l) =>
            /eslint-disable(?:-next-line|-line)?\s*(?:\*\/|$)/.test(l),
          )
          .map((l) => `${f}: ${l.trim()}`),
      )
    expect(bare).toEqual([])
  })

  it('lints the whole source tree, and CI runs it', () => {
    expect(read('scripts/check-raw-palette.sh')).toContain(
      "eslint 'src/**/*.{ts,tsx}'",
    )
    // The step that runs the task, from its `- name:` to the next one: it must neither
    // swallow the exit code nor be allowed to fail.
    for (const workflow of ['mainline-ci.yml', 'pr-ci.yml']) {
      const text = read(`.github/workflows/${workflow}`)
      const at = text.search(/^\s+run: task lint:raw-palette\b/m)
      expect(at, workflow).toBeGreaterThan(-1)
      const next = text.indexOf('- name:', at)
      const step = text.slice(
        text.lastIndexOf('- name:', at),
        next === -1 ? undefined : next,
      )
      expect(step, workflow).not.toMatch(/continue-on-error|\|\||;\s*true/)
    }
    const task = read('Taskfile.yml').split(/^ {2}lint:raw-palette:$/m)[1]
    expect(task?.split(/^ {2}\S/m)[0]).toMatch(
      /cmds:\n\s+- bash scripts\/check-raw-palette\.sh\n/,
    )
  })
})

// The gate's verdict, through the real script, the real ESLint, the real rule and the real
// eslint.config.js on a three-file tree: a clean file passes (0), a raw color fails (1), and
// a file ESLint cannot parse fails (2), since the rule never ran on it.
describe('scripts/check-raw-palette.sh', () => {
  const gate = (src: Record<string, string>) => {
    const root = mkdtempSync(join(tmpdir(), 'raw-palette-gate-'))
    try {
      mkdirSync(join(root, 'scripts'))
      // The script runs pnpm from the repository root, whose package.json pins pnpm.
      copyFileSync(join(WEB, '..', 'package.json'), join(root, 'package.json'))
      copyFileSync(
        join(WEB, '..', 'scripts', 'check-raw-palette.sh'),
        join(root, 'scripts', 'check-raw-palette.sh'),
      )
      const web = join(root, 'web')
      mkdirSync(join(web, 'src'), { recursive: true })
      for (const f of ['package.json', 'eslint.config.js'])
        copyFileSync(join(WEB, f), join(web, f))
      for (const d of ['node_modules', 'eslint-rules'])
        symlinkSync(join(WEB, d), join(web, d))
      for (const [name, text] of Object.entries(src))
        writeFileSync(join(web, 'src', name), text)
      const run = spawnSync('bash', ['scripts/check-raw-palette.sh'], {
        cwd: root,
        encoding: 'utf8',
      })
      return { rc: run.status, out: run.stdout + run.stderr }
    } finally {
      rmSync(root, { recursive: true, force: true })
    }
  }
  // An unused variable, which other rules report: only this rule's findings fail the gate.
  const clean = {
    'ok.tsx': "export const ok = 'text-danger bg-surface'\n",
    'unused.ts': 'const unused = 1\n',
  }

  it('passes a tree with no raw color', () => {
    expect(gate(clean)).toMatchObject({ rc: 0 })
  }, 120_000)

  it('fails on a raw color and names it', () => {
    const { rc, out } = gate({
      ...clean,
      'bad.tsx': "export const bad = 'bg-[#123456]'\n",
    })
    expect(rc).toBe(1)
    expect(out).toContain('bad.tsx:1')
  }, 120_000)

  it('fails with 2 on a file ESLint cannot parse, never a clean pass', () => {
    const { rc, out } = gate({ ...clean, 'broken.ts': 'export const = ;\n' })
    expect(rc).toBe(2)
    expect(out).toContain('1 unparsed')
    expect(out).toContain('broken.ts')
  }, 120_000)
})
