// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, render, screen } from '@testing-library/react'
import { createElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import i18n from '@/lib/i18n'
import {
  isTypingTarget,
  parseChord,
  resolveBinding,
} from '@/lib/keybindings/model'
import { COMMAND_GROUP, KEYBINDINGS } from '@/lib/keybindings/table'
import type { GitHostDiff, GitHostDiffQuery } from './source-diff-model'
import { SourceDiffView } from './source-diff-view'

const OURS = [
  ['sourceDiff.nextFile', 'Alt+ArrowDown'],
  ['sourceDiff.previousFile', 'Alt+ArrowUp'],
  ['sourceDiff.nextHunk', 'Alt+ArrowRight'],
  ['sourceDiff.previousHunk', 'Alt+ArrowLeft'],
] as const

const RESERVED = [
  'n',
  'N',
  'd',
  'f',
  't',
  'c',
  'a',
  'r',
  ']',
  '[',
  '/',
  '?',
  'p',
  'Mod+k',
  'Mod+b',
  'Mod+Enter',
  'Enter',
  'Space',
  'ArrowDown',
  'ArrowUp',
  'Home',
  'End',
]

const query: GitHostDiffQuery = {
  source: 'github-1',
  host: 'github',
  repository: 'acme/web',
  base: 'main',
  head: 'deadbeef',
}

function sample(): GitHostDiff {
  return {
    ...query,
    truncated: false,
    files: [
      {
        path: 'a.txt',
        previous_path: '',
        status: 'modified',
        binary: false,
        truncated: false,
        hunks: ['@@ -1 +1 @@\n-a\n+b'],
      },
      {
        path: 'b.txt',
        previous_path: '',
        status: 'added',
        binary: false,
        truncated: false,
        hunks: ['@@ -0,0 +1 @@\n+c'],
      },
    ],
  }
}

beforeEach(() => {
  i18n.addResourceBundle(
    'en',
    'source-diff',
    {
      host: 'Host',
      repository: 'Repository',
      base: 'Base',
      head: 'Head',
      swap: 'Swap',
      swapDisabled: 'Enter both refs before swapping them.',
      requires: 'Requires',
      permission: 'system:admin',
      changedFiles: 'Changed files',
      keysHint: 'Alt and an arrow move between files and hunks.',
      loading: 'Loading the comparison…',
      status: { modified: 'Modified', added: 'Added' },
      countFull: '+{{added}} −{{removed}}',
      countPartial: '+{{added}} −{{removed}}, partial',
      binaryTag: 'Binary',
      truncatedTag: 'Truncated',
      diffLabel: 'Unified diff of {{path}}',
      colOld: 'Old',
      colNew: 'New',
      colChange: 'Change',
      colText: 'Text',
      addedLine: 'Added, new line {{line}}.',
      removedLine: 'Removed, old line {{line}}.',
      contextLine: 'Context, line {{line}}.',
    },
    true,
    true,
  )
})

describe('source diff keys', () => {
  it('keeps the four alt-arrow chords off every chord already in the table', () => {
    expect(
      KEYBINDINGS.find((rule) => rule.command === 'palette.open')?.keys,
    ).toBe('Mod+k')
    expect(
      KEYBINDINGS.some((rule) => rule.command.startsWith('settings.')),
    ).toBe(false)
    for (const [command, keys] of OURS) {
      expect(parseChord(keys)?.alt).toBe(true)
      expect(COMMAND_GROUP[command]).toBe('surface')
      expect(
        KEYBINDINGS.find((rule) => rule.command === command),
      ).toMatchObject({
        keys,
        when: 'sourceDiff',
      })
      expect(
        KEYBINDINGS.filter(
          (rule) => rule.keys === keys && rule.command !== command,
        ),
      ).toEqual([])
      expect(
        resolveBinding(
          KEYBINDINGS,
          { key: parseChord(keys)!.key, altKey: true },
          {
            sourceDiff: true,
          },
        ),
      ).toBe(command)
      expect(
        resolveBinding(
          KEYBINDINGS,
          { key: parseChord(keys)!.key, altKey: true },
          {},
        ),
      ).toBeNull()
    }
    const oursKeys = OURS.map(([, keys]) => keys)
    for (const reserved of RESERVED) expect(oursKeys).not.toContain(reserved)
  })

  it('moves one file when Alt and ArrowDown start from the selected file', async () => {
    const body = sample()
    body.files.push({
      path: 'c.txt',
      previous_path: '',
      status: 'added',
      binary: false,
      truncated: false,
      hunks: ['@@ -0,0 +1 @@\n+d'],
    })
    function draw(): void {
      const client = new QueryClient({
        defaultOptions: { queries: { retry: false } },
      })
      render(
        createElement(
          QueryClientProvider,
          { client },
          createElement(SourceDiffView, {
            query,
            onChange: vi.fn(),
            reader: async () => body,
          }),
        ),
      )
    }
    draw()
    const first = await screen.findByRole('option', { name: /a.txt/ })
    first.focus()
    await act(async () => {
      first.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }),
      )
    })
    expect(screen.getByRole('option', { name: /b.txt/ })).toHaveAttribute(
      'aria-selected',
      'true',
    )
    expect(screen.getByRole('option', { name: /c.txt/ })).toHaveAttribute(
      'aria-selected',
      'false',
    )
    cleanup()
    draw()
    const start = await screen.findByRole('option', { name: /a.txt/ })
    start.focus()
    await act(async () => {
      start.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'ArrowDown',
          altKey: true,
          bubbles: true,
        }),
      )
    })
    expect(screen.getByRole('option', { name: /b.txt/ })).toHaveAttribute(
      'aria-selected',
      'true',
    )
    expect(screen.getByRole('option', { name: /c.txt/ })).toHaveAttribute(
      'aria-selected',
      'false',
    )
  })

  it('moves to the next file outside a field and ignores the chord inside one', async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(SourceDiffView, {
          query,
          onChange: vi.fn(),
          reader: async () => sample(),
        }),
      ),
    )
    const first = await screen.findByRole('option', { name: /a.txt/ })
    expect(first).toHaveAttribute('aria-selected', 'true')
    await act(async () => {
      window.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'ArrowDown',
          altKey: true,
          bubbles: true,
        }),
      )
    })
    expect(screen.getByRole('option', { name: /b.txt/ })).toHaveAttribute(
      'aria-selected',
      'true',
    )
    const field = screen.getByRole('textbox', { name: 'Repository' })
    expect(isTypingTarget(field)).toBe(true)
    await act(async () => {
      field.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'ArrowUp',
          altKey: true,
          bubbles: true,
        }),
      )
    })
    expect(screen.getByRole('option', { name: /b.txt/ })).toHaveAttribute(
      'aria-selected',
      'true',
    )
  })
})
