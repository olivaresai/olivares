// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { RunCommandDTO } from '@/features/agentops/types'
import {
  commandLabel,
  filterCommands,
  insertText,
  slashQuery,
} from './slash-menu-model'

const cmd = (
  name: string,
  description = '',
  extra: Partial<RunCommandDTO> = {},
): RunCommandDTO => ({
  name,
  description,
  source: '',
  kind: 'command',
  ...extra,
})

describe('slashQuery', () => {
  it('is the word typed after a leading slash', () => {
    expect(slashQuery('/')).toBe('')
    expect(slashQuery('/com')).toBe('com')
    expect(slashQuery('/demoplug:plug')).toBe('demoplug:plug')
  })

  it('is null once the command word is finished or the draft is not a command', () => {
    expect(slashQuery('')).toBeNull()
    expect(slashQuery('hello')).toBeNull()
    expect(slashQuery(' /compact')).toBeNull()
    expect(slashQuery('/compact now')).toBeNull()
    expect(slashQuery('/compact\n')).toBeNull()
    expect(slashQuery('/usr/bin/env')).toBeNull()
  })
})

describe('filterCommands', () => {
  const items = [
    cmd('review-pr', 'Review a pull request'),
    cmd('compact', 'Free up context by summarizing'),
    cmd('code-review', 'Review the diff'),
    cmd('demoplug:plug-skill', 'A plugin skill'),
    cmd('Explore', 'Search agent', { kind: 'agent' }),
  ]

  it('keeps the tool order when nothing is typed', () => {
    expect(filterCommands(items, '').map((c) => c.name)).toEqual(
      items.map((c) => c.name),
    )
  })

  it('lists names that start with the query before names that contain it, then descriptions', () => {
    expect(filterCommands(items, 'rev').map((c) => c.name)).toEqual([
      'review-pr',
      'code-review',
    ])
    expect(filterCommands(items, 'diff').map((c) => c.name)).toEqual([
      'code-review',
    ])
    expect(filterCommands(items, 'co').map((c) => c.name)).toEqual([
      'compact',
      'code-review',
    ])
  })

  it('matches a plugin command by the name after its prefix, ignoring case', () => {
    expect(filterCommands(items, 'PLUG-').map((c) => c.name)).toEqual([
      'demoplug:plug-skill',
    ])
    expect(filterCommands(items, 'explore').map((c) => c.name)).toEqual([
      'Explore',
    ])
  })

  it('ranks a plugin command by the name after its prefix above a name that merely contains the query', () => {
    const list = [cmd('plain-alp'), cmd('plug:alpha')]
    expect(filterCommands(list, 'alp').map((c) => c.name)).toEqual([
      'plug:alpha',
      'plain-alp',
    ])
  })

  it('lists the name typed in full before longer names that start with it, whatever the tool order', () => {
    const list = [cmd('review-pr'), cmd('review-notes'), cmd('review')]
    expect(filterCommands(list, 'review').map((c) => c.name)).toEqual([
      'review',
      'review-pr',
      'review-notes',
    ])
    // Only a whole name is "typed in full": the part after a plugin prefix is a prefix match.
    expect(
      filterCommands([cmd('review-pr'), cmd('plug:review')], 'review').map(
        (c) => c.name,
      ),
    ).toEqual(['review-pr', 'plug:review'])
  })

  it('does not take an agent for the command that was typed in full', () => {
    // An agent is typed as @agent-name: it is never what "/review" says.
    const list = [cmd('review-pr'), cmd('review', '', { kind: 'agent' })]
    expect(filterCommands(list, 'review').map((c) => c.name)).toEqual([
      'review-pr',
      'review',
    ])
  })

  it('lists a name that contains the query before a description that does', () => {
    const list = [
      cmd('first', 'mentions deploy in its text'),
      cmd('pre-deploy-check', 'unrelated'),
    ]
    expect(filterCommands(list, 'deploy').map((c) => c.name)).toEqual([
      'pre-deploy-check',
      'first',
    ])
  })

  it('is empty when nothing matches', () => {
    expect(filterCommands(items, 'zzz')).toEqual([])
  })
})

describe('commandLabel', () => {
  it('reads as what choosing the entry types', () => {
    expect(commandLabel(cmd('compact'))).toBe('/compact')
    expect(commandLabel(cmd('Explore', '', { kind: 'agent' }))).toBe(
      '@agent-Explore',
    )
  })
})

describe('insertText', () => {
  it('types a command or skill as /name and a space', () => {
    expect(insertText(cmd('compact'))).toBe('/compact ')
  })

  it('mentions an agent the way the tool does', () => {
    expect(insertText(cmd('Explore', '', { kind: 'agent' }))).toBe(
      '@agent-Explore ',
    )
  })
})

it('inserts the native Codex skill spelling reported by the route', () => {
  const skill = { ...cmd('review'), insert_text: '$review' }
  expect(insertText(skill)).toBe('$review ')
  expect(commandLabel(skill)).toBe('$review')
})
