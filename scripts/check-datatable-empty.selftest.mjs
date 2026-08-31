// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Red-case battery for check-datatable-empty.mjs. A gate nobody has watched fail is a
// gate that reports green because it looked at nothing, so this builds throwaway source
// trees under TMPDIR and asserts the scan reports EXACTLY the sites it should — and
// stays silent on the ones it should not. Run with
// `node scripts/check-datatable-empty.mjs --self-test`; it touches nothing outside its
// temp dir.
//
// Every case here is a real mistake, not an invented one: the first group are errors
// made while MEASURING the defect, the second group are holes the closing adversarial
// contrast found in the FIRST version of this gate, each one a way a live call site
// could have slipped past a green run:
//
//   - a LOCAL ALIAS (`import { DataTable as Grid }`) or a namespace import made the
//     site count as zero sites;
//   - the tag was walked on RAW source, so a comment inside it could inject a second
//     `empty=` that overrode the real one, and a `>` inside a string attribute ended
//     the tag early;
//   - a `{...spread}` after `empty` can overwrite it at runtime and was recorded but
//     never consulted;
//   - a local `const x = <></>` passed by name defeated the inert check.
//
// And the clause-direction check is now what its name always claimed: it DISABLES each
// clause in turn and requires that red cases stop being caught — by IDENTITY, not by
// count. Counting per kind let a detection MOVE between fixtures unnoticed, which a
// second contrast demonstrated with a mutation that kept every cardinal identical. The previous version
// re-ran three red cases with the scanner intact and asked whether their kinds
// appeared — which stays green even if a clause does no work. That is the exact
// fail-open shape this repo has closed five times elsewhere; it had been reintroduced
// here, in the battery meant to prevent it.

import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

function tree(files) {
  const dir = fs.mkdtempSync(
    path.join(process.env.TMPDIR || os.tmpdir(), 'datatable-empty-selftest-'),
  )
  for (const [name, body] of Object.entries(files)) {
    const full = path.join(dir, name)
    fs.mkdirSync(path.dirname(full), { recursive: true })
    fs.writeFileSync(full, body)
  }
  return dir
}

/** `<file>:<line>:<kind>` for every problem, sorted — what the shape assertions compare. */
function shape(result) {
  return result.problems.map((p) => `${p.file}:${p.line}:${p.kind}`).sort()
}

const HONEST = `empty={
        <EmptyState
          title={t('empty.agents.title')}
          description={t('empty.agents.description')}
        />
      }`

const IMPORT = `import { DataTable } from '@/components/data/data-table'\n`

const CASES = [
  {
    name: 'a table with NO `empty` prop at all',
    expect: ['view.tsx:1:missing'],
    files: { 'view.tsx': `<DataTable columns={columns} data={rows} />\n` },
  },
  {
    name: 'a GENERIC-TYPED table with no `empty` — the `>` of `<Row>` is not the tag end',
    expect: ['view.tsx:1:missing'],
    files: { 'view.tsx': `<DataTable<Row> columns={columns} data={rows} />\n` },
  },
  {
    name: 'a generic-typed table that DOES pass `empty` after the type argument',
    expect: [],
    files: {
      'view.tsx': `<DataTable<Row>\n  columns={columns}\n  data={rows}\n  ${HONEST}\n/>\n`,
    },
  },
  {
    name: '`empty={undefined}` — passes a line-grep and a required ReactNode, renders the generic',
    expect: ['view.tsx:1:inert'],
    files: { 'view.tsx': `<DataTable columns={columns} data={rows} empty={undefined} />\n` },
  },
  {
    name: 'an empty fragment — "no decision" spelled as a value',
    expect: ['view.tsx:1:inert'],
    files: { 'view.tsx': `<DataTable columns={columns} data={rows} empty={<></>} />\n` },
  },
  {
    name: 'the generic hand-rolled back in by the caller',
    expect: ['view.tsx:2:generic'],
    files: {
      'view.tsx':
        `const x = 1\n<DataTable columns={c} data={d} empty={<EmptyState title={t('states.noResults')} />} />\n`,
    },
  },
  {
    name: 'the generic hand-rolled in as a bare literal',
    expect: ['view.tsx:1:generic'],
    files: {
      'view.tsx': `<DataTable columns={c} data={d} empty={<EmptyState title="No results" />} />\n`,
    },
  },
  {
    name: 'a multi-line honest empty state — the shape a line-oriented grep cannot see',
    expect: [],
    files: {
      'view.tsx': `<DataTable\n  columns={columns}\n  data={rows}\n  ${HONEST}\n/>\n`,
    },
  },
  {
    name: 'the component NAMED IN A COMMENT is not a call site',
    expect: [],
    files: {
      'view.tsx':
        `// Wrap charts in <AccessibleChart> and lists in <DataTable columns={c} />.\n` +
        `/* <DataTable /> is the tabular primitive. */\n` +
        `const doc = 'use <DataTable /> here'\n`,
    },
  },
  {
    name: 'AccessibleChart is guarded too — the wrapper must not park a new default',
    expect: ['chart.tsx:1:missing'],
    files: { 'chart.tsx': `<AccessibleChart title={t('x')} columns={c} data={d} />\n` },
  },
  {
    name: 'test files are out of scope — the component bench is not a product surface',
    expect: [],
    files: { 'view.test.tsx': `<DataTable columns={c} data={d} />\n` },
  },
  {
    name: 'a table nested inside another expression still reports its own line',
    expect: ['view.tsx:3:missing'],
    files: {
      'view.tsx': `const V = () => (\n  <Card>\n    <DataTable columns={c} data={d} />\n  </Card>\n)\n`,
    },
  },
  // --- holes the closing contrast found in the first version of this gate ----------
  {
    name: 'a LOCAL ALIAS import — matching the literal name counted this as zero sites',
    expect: ['view.tsx:2:missing'],
    files: {
      'view.tsx':
        `import { DataTable as Grid } from '@/components/data/data-table'\n` +
        `<Grid columns={c} data={d} />\n`,
    },
  },
  {
    name: 'an aliased site with an honest empty state is still clean',
    expect: [],
    files: {
      'view.tsx':
        `import { DataTable as Grid } from '@/components/data/data-table'\n` +
        `<Grid columns={c} data={d} ${HONEST} />\n`,
    },
  },
  {
    name: 'a NAMESPACE import — `<T.DataTable>` is a call site too',
    expect: ['view.tsx:2:missing'],
    files: {
      'view.tsx':
        `import * as T from '@/components/data/data-table'\n` + `<T.DataTable columns={c} data={d} />\n`,
    },
  },
  {
    name: 'a COMMENT INSIDE the tag cannot inject an `empty` that hides the real one',
    expect: ['view.tsx:1:generic'],
    files: {
      'view.tsx':
        `<DataTable empty={<EmptyState title={t('states.noResults')} />} /* empty={<Specific/>} */ columns={c} />\n`,
    },
  },
  {
    name: 'a `>` inside a string attribute does not end the tag early',
    expect: ['view.tsx:1:inert'],
    files: {
      'view.tsx': `<DataTable label="a > b" columns={c} data={d} empty={<></>} />\n`,
    },
  },
  {
    name: 'a `{...spread}` AFTER `empty` can overwrite it at runtime — unverifiable is not clean',
    expect: ['view.tsx:1:opaque'],
    files: {
      'view.tsx': `<DataTable columns={c} data={d} ${HONEST} {...rest} />\n`,
    },
  },
  {
    name: 'a spread BEFORE `empty` cannot overwrite it — that site stays clean',
    expect: [],
    files: {
      'view.tsx': `<DataTable {...rest} columns={c} data={d} ${HONEST} />\n`,
    },
  },
  {
    name: 'an opening tag this gate cannot read to its end is reported, not skipped',
    expect: ['view.tsx:1:unterminated'],
    files: { 'view.tsx': `<DataTable columns={c} data={d} ${HONEST}\n` },
  },
  {
    name: 'a local `const` holding an empty fragment, passed by name',
    expect: ['view.tsx:2:inert'],
    files: {
      'view.tsx': `const emptyNode = <></>\n<DataTable columns={c} data={d} empty={emptyNode} />\n`,
    },
  },
  {
    name: 'a local `const` holding an HONEST element, passed by name, stays clean',
    expect: [],
    files: {
      'view.tsx':
        `const emptyNode = <EmptyState title={t('empty.x.title')} />\n` +
        `<DataTable columns={c} data={d} empty={emptyNode} />\n`,
    },
  },
  {
    name: 'a local `const` holding the generic, passed by name, is still the generic',
    expect: ['view.tsx:2:generic'],
    files: {
      'view.tsx':
        `const emptyNode = <EmptyState title={t('states.noResults')} />\n` +
        `<DataTable columns={c} data={d} empty={emptyNode} />\n`,
    },
  },
  {
    name: 'an import from an unrelated module does not silently drop the literal name',
    expect: ['view.tsx:3:missing'],
    files: {
      'view.tsx': `import { Card } from '@/components/ui/card'\n${IMPORT}<DataTable columns={c} data={d} />\n`,
    },
  },
]

/**
 * Each clause, DISABLED on its own, must stop catching its own red cases and leave the
 * others untouched. This mutates the scanner, which is the only way the claim means
 * anything: re-running red cases with every clause intact proves nothing about any of
 * them individually.
 */
const CLAUSES = ['missing', 'inert', 'generic', 'opaque', 'unterminated']

function clauseDirection(scan) {
  const failures = []
  const files = {
    'missing.tsx': `<DataTable columns={c} data={d} />\n`,
    'inert.tsx': `<DataTable columns={c} data={d} empty={undefined} />\n`,
    'generic.tsx': `<DataTable columns={c} data={d} empty={<EmptyState title={t('states.noResults')} />} />\n`,
    'opaque.tsx': `<DataTable columns={c} data={d} empty={<EmptyState title={t('e.t')} />} {...rest} />\n`,
    'unterminated.tsx': `<DataTable columns={c} data={d} empty={<EmptyState title={t('e.t')} />}\n`,
  }
  // IDENTITY, not cardinality. Comparing counts per kind let a detection MOVE between
  // fixtures unnoticed: a contrast mutated `generic` so that, with `inert` disabled, it
  // caught inert.tsx and let generic.tsx escape — `generic: 1` before and after, green.
  // A clause must remove exactly ITS OWN findings and leave the others byte-identical.
  const key = (p) => `${p.file}:${p.line}:${p.kind}`
  const root = tree(files)
  try {
    const baseline = scan({ src: root, root }).problems
    for (const clause of CLAUSES) {
      const mine = baseline.filter((p) => p.kind === clause).map(key).sort()
      if (mine.length === 0) {
        failures.push(`clause "${clause}" catches nothing even INTACT — it does no work`)
        continue
      }
      const after = scan({ src: root, root, disable: [clause] }).problems
      const afterMine = after.filter((p) => p.kind === clause).map(key)
      if (afterMine.length !== 0) {
        failures.push(`clause "${clause}" still fires with itself disabled: ${afterMine.join(', ')}`)
      }
      const wantRest = baseline.filter((p) => p.kind !== clause).map(key).sort()
      const gotRest = after.filter((p) => p.kind !== clause).map(key).sort()
      if (wantRest.join('|') !== gotRest.join('|')) {
        failures.push(
          `disabling "${clause}" moved OTHER findings — want ${JSON.stringify(wantRest)}, got ${JSON.stringify(gotRest)}`,
        )
      }
    }
  } finally {
    fs.rmSync(root, { recursive: true, force: true })
  }
  return failures
}

export function selfTest(scan) {
  const failures = []
  for (const testCase of CASES) {
    const root = tree(testCase.files)
    let got
    try {
      got = shape(scan({ src: root, root }))
    } finally {
      fs.rmSync(root, { recursive: true, force: true })
    }
    const want = [...testCase.expect].sort()
    const ok = got.length === want.length && got.every((v, i) => v === want[i])
    if (!ok) {
      failures.push(
        `  ${testCase.name}\n      want: ${JSON.stringify(want)}\n      got : ${JSON.stringify(got)}`,
      )
    }
  }
  failures.push(...clauseDirection(scan).map((f) => `  ${f}`))

  if (failures.length) {
    console.error(`✗ datatable empty-state SELF-TEST FAILED — ${failures.length} case(s):`)
    for (const failure of failures) console.error(failure)
    return false
  }
  console.log(
    `✓ datatable empty-state self-test OK — ${CASES.length} case(s) ` +
      `(${CASES.filter((c) => c.expect.length).length} red by construction) + ` +
      'clause-direction check that DISABLES each clause',
  )
  return true
}
