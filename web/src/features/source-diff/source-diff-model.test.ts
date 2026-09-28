// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import {
  DIFF_WINDOW,
  countChangedLines,
  fileTitle,
  lineCount,
  parseHunks,
  truncationBound,
  windowBounds,
} from './source-diff-model'

const PATCH =
  [
    '@@ -14,3 +14,4 @@',
    ' class FrameQueue:',
    '-    def put(self, frame):',
    '-        self.disk.write(frame)',
    '+    async def put(self, frame):',
    '+        await self._q.put(frame)',
    '+        return None',
  ].join('\n') + '\n'

describe('source diff line counts', () => {
  it('counts added and removed lines and ignores file headers', () => {
    expect(countChangedLines([`--- a/q.py\n+++ b/q.py\n${PATCH}`])).toEqual({
      added: 3,
      removed: 2,
    })
  })

  it('shows no count for an empty hunk list', () => {
    expect(lineCount({ binary: false, truncated: false, hunks: [] })).toBeNull()
  })

  it('marks a truncated file partial and never calls the count a total', () => {
    const count = lineCount({
      binary: false,
      truncated: true,
      hunks: [PATCH],
    })
    expect(count).toEqual({ added: 3, removed: 2, partial: true })
    expect(JSON.stringify(count)).not.toContain('total')
  })

  it('does not treat a removed file with no patch as binary', () => {
    const count = lineCount({
      binary: false,
      truncated: false,
      hunks: [],
    })
    expect(count).toBeNull()
  })
})

describe('rename title', () => {
  it('shows previous_path then path', () => {
    expect(
      fileTitle({ previous_path: 'old/notes.txt', path: 'notes/now.txt' }),
    ).toBe('old/notes.txt → notes/now.txt')
  })

  it('shows only the path when there is no previous path', () => {
    expect(fileTitle({ previous_path: '', path: 'a.go' })).toBe('a.go')
  })
})

describe('unified rows', () => {
  it('gives an added line a plus marker and its new number', () => {
    const added = parseHunks([PATCH]).find((row) => row.kind === 'added')
    expect(added).toMatchObject({
      kind: 'added',
      newLine: 15,
      oldLine: null,
      marker: '+',
    })
  })

  it('drops a cut last line, a trailing blank, and a backslash line', () => {
    expect(countChangedLines(['@@ -1 +1 @@\n+kept\n+cut'])).toEqual({
      added: 1,
      removed: 0,
    })
    expect(countChangedLines(['@@ -1 +1 @@\n+kept\n+also\n'])).toEqual({
      added: 2,
      removed: 0,
    })
    const marked = parseHunks([
      '@@ -1 +1 @@\n a\n\\ No newline at end of file\n',
    ])
    expect(marked.filter((row) => row.kind === 'context')).toEqual([
      expect.objectContaining({ text: 'a', oldLine: 1, newLine: 1 }),
    ])
    const blank = parseHunks(['@@ -1 +1 @@\n a\n'])
    expect(blank.filter((row) => row.kind === 'context')).toHaveLength(1)
  })

  it('gives a removed line a minus marker and its old number', () => {
    const removed = parseHunks([PATCH]).find((row) => row.kind === 'removed')
    expect(removed).toMatchObject({
      kind: 'removed',
      oldLine: 15,
      newLine: null,
      marker: '−',
    })
  })
})

describe('source-diff locales', () => {
  const languages = ['en', 'es', 'de', 'fr', 'ja', 'ru', 'zh']

  function keys(value: unknown, prefix = ''): string[] {
    if (!value || typeof value !== 'object' || Array.isArray(value))
      return [prefix]
    return Object.entries(value as Record<string, unknown>).flatMap(
      ([key, child]) => keys(child, prefix ? `${prefix}.${key}` : key),
    )
  }

  it('has the same non-plural keys in all locales, including the permission sentence', () => {
    const bundles = languages.map(
      (language) =>
        JSON.parse(
          readFileSync(
            resolve('src/features/source-diff/i18n', `${language}.json`),
            'utf8',
          ),
        ) as { forbidden: string },
    )
    const regularKeys = (bundle: unknown) =>
      keys(bundle)
        .filter((key) => !key.startsWith('fileCount_'))
        .sort()
    const english = regularKeys(bundles[0])
    for (const bundle of bundles.slice(1)) {
      expect(regularKeys(bundle)).toEqual(english)
    }
    expect(bundles[0].forbidden).toMatch(/superadmin/)
    expect(bundles[0].forbidden).toMatch(/cannot grant/)
    expect(english).toEqual(
      expect.arrayContaining(['compare', 'loading', 'tooLarge', 'rateLimited']),
    )
  })

  it('has every cardinal file-count form required by each locale', () => {
    const bundles = languages.map(
      (language) =>
        JSON.parse(
          readFileSync(
            resolve('src/features/source-diff/i18n', `${language}.json`),
            'utf8',
          ),
        ) as Record<string, string>,
    )
    for (const [index, bundle] of bundles.entries()) {
      const required = new Intl.PluralRules(languages[index])
        .resolvedOptions()
        .pluralCategories.map((category) => `fileCount_${category}`)
        .sort()
      const actual = Object.keys(bundle)
        .filter((key) => key.startsWith('fileCount_'))
        .sort()
      expect(actual, languages[index]).toEqual(required)
      for (const key of required) {
        expect(bundle[key], `${languages[index]}: ${key}`).toContain(
          '{{count}}',
        )
      }
    }
    expect(bundles[0]?.fileCount_one).toBe('{{count}} file')
    expect(bundles[0]?.fileCount_other).toBe('{{count}} files')
  })
})

describe('bounds and window', () => {
  it('names the file bound at 100 files and the byte bound below that', () => {
    expect(truncationBound(100)).toBe('files')
    expect(truncationBound(99)).toBe('bytes')
  })

  it('windows only above 200 lines', () => {
    expect(windowBounds(200, 0)).toEqual({ start: 0, end: 200 })
    expect(windowBounds(201, 0)).toEqual({ start: 0, end: DIFF_WINDOW })
    expect(windowBounds(250, 200)).toEqual({ start: 50, end: 250 })
  })
})
