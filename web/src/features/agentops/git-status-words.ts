// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** The unmerged codes git status prints for a file in conflict. */
const UNMERGED = new Set(['UU', 'AA', 'DD', 'AU', 'UA', 'DU', 'UD'])

/** The word key of each change letter git status prints (it runs with --no-renames). */
const CHANGE = new Map([
  ['M', 'modified'],
  ['A', 'added'],
  ['D', 'deleted'],
  ['T', 'typeChanged'],
  ['C', 'copied'],
])

type Translate = (key: string, values?: Record<string, string>) => string

/**
 * The words for a file's two-letter git status code (index, then working tree): «In
 * conflict», «Untracked», or the change, with «staged» and «not staged» said only where the
 * two sides differ. A code it does not know reads as its own letters, never as a wrong word.
 * Each key names its namespace, so any `t` reads it.
 */
export function gitStatusWords(code: string, t: Translate): string {
  if (UNMERGED.has(code)) return t('agentops:browser.gitCode.conflict')
  if (code === '??') return t('agentops:browser.gitCode.untracked')
  const index = code[0] ?? ' '
  const tree = code[1] ?? ' '
  const known = (c: string) => c === ' ' || CHANGE.has(c)
  if (code.trim() === '' || !known(index) || !known(tree)) return code.trim()
  const said = (c: string) => t(`agentops:browser.gitCode.${CHANGE.get(c)}`)
  if (index === tree) return said(index)
  if (tree === ' ')
    return t('agentops:browser.gitCode.staged', { change: said(index) })
  if (index === ' ')
    return t('agentops:browser.gitCode.notStaged', { change: said(tree) })
  return t('agentops:browser.gitCode.split', {
    staged: said(index),
    notStaged: said(tree),
  })
}
