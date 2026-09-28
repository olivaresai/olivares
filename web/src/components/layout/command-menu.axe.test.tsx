// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The open palette must satisfy axe-core's aria-required-children rule. A listbox
// may own only groups and options. The negative fixture is a list with a child
// that is neither, so a checker that always reports clean fails here.
import axe from 'axe-core'
import { screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  useNavigate: () => vi.fn(),
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'tnt-1',
    can: () => true,
    logout: vi.fn(),
  }),
}))

import { CommandMenu } from './command-menu'
import { useCommandStore } from '@/stores/command'

async function requiredChildrenViolations(root: Element) {
  const results = await axe.run(root, {
    runOnly: { type: 'rule', values: ['aria-required-children'] },
  })
  return results.violations.filter((v) => v.id === 'aria-required-children')
}

afterEach(() => {
  useCommandStore.getState().setOpen(false)
})

describe('command palette list structure', () => {
  it('has no aria-required-children violation while the palette is open', async () => {
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    const list = await screen.findByRole('listbox')
    expect(list.getAttribute('id')).toMatch(/^radix-/)

    const violations = await requiredChildrenViolations(document.body)
    const detail = violations
      .flatMap((v) => v.nodes)
      .map(
        (n) =>
          `${n.target.join(' ')} ${n.html.slice(0, 180)} ${n.failureSummary}`,
      )
      .join('\n')
    expect(violations, detail).toEqual([])
  })

  it('catches a listbox that owns a child which is not an option', async () => {
    const host = document.createElement('div')
    host.innerHTML =
      '<div role="listbox" aria-label="Broken fixture"><div role="option">Allowed</div><div role="separator"></div></div>'
    document.body.appendChild(host)
    const violations = await requiredChildrenViolations(host)
    host.remove()
    expect(violations).not.toEqual([])
    expect(violations[0]?.id).toBe('aria-required-children')
  })
})
