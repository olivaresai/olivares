// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A VIEW READS A REFUSAL THROUGH `ApiError`, NEVER BY ITS STATUS. A 403 is not one thing: a
// role refusal, a step-up demand, a tenant not admitted and an edition refusal all arrive as
// 403. `isForbidden` is the role refusal alone; `isForbiddenOrEdition` is every 403, for
// logic that treats them alike. A view that reads `status === 403` itself (or a code
// `'forbidden'`) decides «not authorized» words for an answer that may be the edition's, which
// is how three views said «not authorized» beside the edition line. This census parses every
// view file (comments and strings are not code) and fails on such a read outside the list.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { relative, resolve } from 'node:path'
import ts from 'typescript'
import { describe, expect, it } from 'vitest'
import { walk } from '@/test/empty-state-census'

const SRC = resolve(__dirname, '../..')

/** Files that still read a 403 by its status, each with its reason. */
const EXCEPTIONS: Record<string, string> = {
  'features/identity/totp.tsx':
    'its 403 sentence names the extra check for administrative actions, never a permission',
  'features/sessions/terminal-store.ts':
    'held by the Sessions owner: its `refused` kind takes 403 and 404 alike',
}

const EQUALITY = new Set<ts.SyntaxKind>([
  ts.SyntaxKind.EqualsEqualsEqualsToken,
  ts.SyntaxKind.EqualsEqualsToken,
  ts.SyntaxKind.ExclamationEqualsEqualsToken,
  ts.SyntaxKind.ExclamationEqualsToken,
])

function field(node: ts.Expression): string | undefined {
  return ts.isPropertyAccessExpression(node) ? node.name.text : undefined
}

/** `<x>.status` against 403, or `<x>.code` against 'forbidden', either way round. */
function readsTheStatus(a: ts.Expression, b: ts.Expression): boolean {
  return (
    (field(a) === 'status' && ts.isNumericLiteral(b) && b.text === '403') ||
    (field(a) === 'code' && ts.isStringLiteralLike(b) && b.text === 'forbidden')
  )
}

/** The lines (1-based) of `name` that read a 403 by its status. */
function rawChecks(name: string, text: string): number[] {
  const source = ts.createSourceFile(name, text, ts.ScriptTarget.Latest, true)
  const lines: number[] = []
  const at = (node: ts.Node) =>
    lines.push(source.getLineAndCharacterOfPosition(node.getStart()).line + 1)
  const visit = (node: ts.Node) => {
    if (
      ts.isBinaryExpression(node) &&
      EQUALITY.has(node.operatorToken.kind) &&
      (readsTheStatus(node.left, node.right) ||
        readsTheStatus(node.right, node.left))
    )
      at(node)
    if (
      ts.isCaseClause(node) &&
      ts.isNumericLiteral(node.expression) &&
      node.expression.text === '403'
    )
      at(node)
    ts.forEachChild(node, visit)
  }
  visit(source)
  return lines
}

const read = (rel: string) => readFileSync(resolve(SRC, rel), 'utf8')

const views = () =>
  ['features', 'components']
    .flatMap((dir) => walk(resolve(SRC, dir), ['.ts', '.tsx']))
    .map((file) => relative(SRC, file))
    .filter((rel) => !/\.test\.tsx?$/.test(rel))

describe('a 403 is read through ApiError, never by its status', () => {
  it('sees every form it forbids, and no comment or string', () => {
    const sample = [
      'if (err.status === 403) a()',
      'if (403 == e.status) a()',
      "if (error.code === 'forbidden') a()",
      'switch (e.status) { case 403: a() }',
      '// err.status === 403 in a comment',
      "const s = 'status === 403'",
      'if (err.isForbidden || err.status === 404) a()',
    ].join('\n')
    expect(rawChecks('sample.tsx', sample)).toEqual([1, 2, 3, 4])
  })

  it('no view reads a 403 by its status outside the list', () => {
    const found = views()
      .filter((rel) => !(rel in EXCEPTIONS))
      .flatMap((rel) => rawChecks(rel, read(rel)).map((n) => `${rel}:${n}`))
    expect(found).toEqual([])
  })

  it('every listed file still reads one, so the list cannot go stale', () => {
    for (const rel of Object.keys(EXCEPTIONS))
      expect(rawChecks(rel, read(rel)).length, rel).toBeGreaterThan(0)
  })
})
