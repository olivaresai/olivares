// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE MACHINE FACE IS FOR CODE, COMMANDS, PATHS AND IDENTIFIERS (#3135).
//
// On Now, the "workspace" chip on every live card, the last-tool fragment ("Bash · cd") and the
// count beside "Needs you" were set in the mono face. None of them is code: a folder is shown by
// its NAME, a fragment is a word the engine chose for a person, a count is a number. Mono on
// them makes the page read as a log, which is the heaviness the references do not have.
//
// Now shows no code, command, path or identifier, so no file of `features/home` may name the
// mono face. The day it shows one (a command in a card, a path), the file is added to ALLOWED
// below with the reason; an identifier that is a path goes through `PathText`, which owns its
// own face.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { stripComments, walk } from '@/test/empty-state-census'

const HOME = resolve(__dirname)

/** Files of Now that may set text in the machine face, each with the reason. */
const ALLOWED: ReadonlyArray<{ file: string; why: string }> = []

/** The utilities that set the machine face or its two sizes. */
const MONO = /(?<![\w-])(?:font-mono|text-mono(?:-s)?)(?![\w-])/g

/** The machine-face utilities a source file names, comments aside. */
export function monoClasses(source: string): string[] {
  return [...stripComments(source).matchAll(MONO)].map((m) => m[0])
}

const sources = walk(HOME, ['.ts', '.tsx']).filter(
  (f) => !/\.test\.tsx?$/.test(f),
)

describe('Now sets nothing in the machine face', () => {
  it('names no mono utility outside the declared files', () => {
    const used: string[] = []
    for (const file of sources) {
      const rel = relative(HOME, file).split('\\').join('/')
      if (ALLOWED.some((a) => a.file === rel)) continue
      for (const cls of monoClasses(readFileSync(file, 'utf8')))
        used.push(`${rel}: ${cls}`)
    }
    expect(used).toEqual([])
  })

  it('declares only files that still set the machine face', () => {
    const stale = ALLOWED.filter(
      (a) =>
        monoClasses(readFileSync(resolve(HOME, a.file), 'utf8')).length === 0,
    )
    expect(stale).toEqual([])
  })

  it('finds the utilities, and not the prose that names them', () => {
    expect(
      monoClasses(`<span className="truncate font-mono text-caption">`),
    ).toEqual(['font-mono'])
    expect(monoClasses(`cn('text-mono-s', 'md:text-mono')`)).toEqual([
      'text-mono-s',
      'text-mono',
    ])
    expect(
      monoClasses(
        `// was font-mono\n/* text-mono */ const a = 'text-monospace'`,
      ),
    ).toEqual([])
  })

  it('scans a real number of files, so an empty sweep cannot read as clean', () => {
    expect(sources.length).toBeGreaterThan(10)
  })
})
