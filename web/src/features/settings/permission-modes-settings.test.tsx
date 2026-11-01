// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import {
  DEFAULT_PERMISSION,
  PERMISSION_CHOICES,
  PERMISSION_MODE,
  PERMISSION_NAME,
} from '@/features/agentops/session-launch'
import firstHour from '@/features/first-hour/i18n/en.json'
import { renderIntel } from '@/test/intel'
import { PermissionModesSettings } from './permission-modes-settings'
import table from './permission-modes.gen.json'

const rowOf = (name: string) => table.modes.find((m) => m.name === name)

// The engine writes this table from the code that builds each tool's launch (the Go test
// TestPermissionTable_PublishedFilesAreCurrent keeps the file current). What the console
// sends and says has to be what the table says, or a person is told one thing and
// handed another.
describe('the permissions table and the console', () => {
  it('lists every choice the picker offers once, under the engine name', () => {
    expect(table.modes.map((m) => m.name)).toEqual(
      PERMISSION_CHOICES.map((c) => PERMISSION_NAME[c]),
    )
  })

  it('sends the permission_mode the table names for each choice', () => {
    for (const choice of PERMISSION_CHOICES)
      expect(PERMISSION_MODE[choice], choice).toBe(
        rowOf(PERMISSION_NAME[choice])?.mode,
      )
  })

  it('says the names and what each does in the words the engine writes', () => {
    for (const choice of PERMISSION_CHOICES) {
      const row = rowOf(PERMISSION_NAME[choice])
      expect(firstHour.permission[choice], choice).toBe(row?.label)
      expect(firstHour.permissionSummary[choice], choice).toBe(row?.summary)
    }
  })

  it('names every permission of the table, and what it does, in every language', () => {
    const locales = import.meta.glob<{
      permission: Record<string, string>
      permissionSummary: Record<string, string>
    }>('@/features/first-hour/i18n/*.json', { eager: true, import: 'default' })
    expect(Object.keys(locales).length).toBeGreaterThanOrEqual(9)
    for (const [file, bundle] of Object.entries(locales)) {
      const labels = PERMISSION_CHOICES.map((c) => bundle.permission?.[c])
      const summaries = PERMISSION_CHOICES.map(
        (c) => bundle.permissionSummary?.[c],
      )
      for (const [i, choice] of PERMISSION_CHOICES.entries()) {
        expect(labels[i]?.trim(), `${file} permission.${choice}`).toBeTruthy()
        expect(
          summaries[i]?.trim(),
          `${file} permissionSummary.${choice}`,
        ).toBeTruthy()
      }
      // Six names a person tells apart: no two permissions share one.
      expect(new Set(labels).size, file).toBe(table.modes.length)
    }
  })

  it('marks the quick-start default, and it is never Full', () => {
    expect(
      table.modes.filter((m) => 'default' in m).map((m) => m.name),
    ).toEqual([PERMISSION_NAME[DEFAULT_PERMISSION]])
    expect(DEFAULT_PERMISSION).not.toBe('full')
  })
})

describe('Settings → Permissions', () => {
  // The table is wider than a phone, so it scrolls sideways; a keyboard has to reach it.
  it('scrolls inside a labelled region that the keyboard can focus', () => {
    renderIntel(<PermissionModesSettings />)
    const region = screen.getByRole('region', { name: /permission/i })
    expect(region).toHaveAttribute('tabindex', '0')
    expect(within(region).getByRole('table')).toBeInTheDocument()
  })

  it('has a row for each permission and a column for each tool', () => {
    renderIntel(<PermissionModesSettings />)
    expect(screen.getAllByRole('row')).toHaveLength(1 + table.modes.length)
    expect(
      screen.getAllByRole('columnheader').map((h) => h.textContent),
    ).toEqual([
      'Permission',
      'What it does',
      'Claude Code',
      'Codex',
      'OpenCode',
      'Grok Build',
      'Gemini CLI',
    ])
  })

  it('says what a tool receives in its own words', () => {
    renderIntel(<PermissionModesSettings />)
    const supervised = screen.getByTestId('permission-row-supervised')
    expect(
      within(supervised).getByText('--permission-mode default'),
    ).toBeVisible()
    expect(within(supervised).getByText('approval untrusted')).toBeVisible()
  })

  it('says where a tool has no such mode and asks instead', () => {
    renderIntel(<PermissionModesSettings />)
    const auto = screen.getByTestId('permission-row-auto')
    // Claude Code has an automatic mode; the other four tools ask.
    expect(within(auto).getByText('--permission-mode auto')).toBeVisible()
    expect(
      within(auto).getAllByText('No automatic mode: asks as in Supervised'),
    ).toHaveLength(4)
    expect(
      within(screen.getByTestId('permission-row-supervised')).queryByText(
        /No automatic mode/,
      ),
    ).toBeNull()
  })

  it('says that a tool other than Claude Code asks Olivares, not you', () => {
    renderIntel(<PermissionModesSettings />)
    expect(
      screen.getByText(
        /For tools other than Claude Code, Olivares answers each approval the tool asks for, by the permission/,
      ),
    ).toBeInTheDocument()
  })

  it('names the default and shows Full in the warning style', () => {
    renderIntel(<PermissionModesSettings />)
    const defaults = screen.getAllByText('Default')
    expect(defaults).toHaveLength(1)
    expect(
      screen.getByTestId('permission-row-edits-and-commands'),
    ).toContainElement(defaults[0])
    const full = screen.getByTestId('permission-row-full')
    expect(within(full).getByText('Full')).toHaveClass('text-warning')
  })
})
