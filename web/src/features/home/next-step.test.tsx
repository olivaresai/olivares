// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE FRONT DOOR offers the next action, to the principals who may take it.
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import { expectNoRawI18nKeys } from '@/test/i18n-keys'
import { FEATURE_VIEWS } from '@/features/registry'
import { NextStep } from './next-step'
import { NEXT_STEPS, stepView } from './next-step-catalog'
import './i18n'

// The anchor mock FORWARDS every prop, unlike the older copies of it in this
// directory. Those drop `data-testid`, so a test that asks for a link by test id
// finds nothing and the failure looks like a missing element rather than a missing
// mock — measured here, twice, before this comment existed.
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({
    children,
    to,
    ...rest
  }: { children: ReactNode; to: string } & Record<string, unknown>) => (
    <a href={to} {...rest}>
      {children}
    </a>
  ),
}))

const authState = vi.hoisted(() => ({
  can: (_p: string): boolean => true,
  activeTenant: 'demo' as string | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

afterEach(() => {
  vi.restoreAllMocks()
  authState.can = () => true
})

describe('NextStep', () => {
  it('every declared step still resolves to a registry view', () => {
    // The drift guard. The paths are NOT typed in the component; if a view id is
    // renamed or retired, the card would silently disappear from the front door
    // instead of failing here.
    for (const step of NEXT_STEPS) {
      expect(
        stepView(step.viewId),
        `${step.viewId} is not in FEATURE_VIEWS`,
      ).toBeDefined()
    }
  })

  it('links each verb to the path the registry publishes for it', () => {
    renderIntel(<NextStep />)
    for (const step of NEXT_STEPS) {
      const view = FEATURE_VIEWS.find((v) => v.id === step.viewId)!
      expect(screen.getByTestId(`home-next-step-${step.id}`)).toHaveAttribute(
        'href',
        view.path,
      )
    }
  })

  it('offers nothing at all to a principal who may write nothing', () => {
    // The honest answer for a read-only role: there is no next step FOR THEM, and a
    // row of cards that all end in 403 is worse than no row.
    authState.can = () => false
    const { container } = renderIntel(<NextStep />)
    expect(container).toBeEmptyDOMElement()
  })

  it('decides on the WRITE permission, not the route read permission', () => {
    // `sessions:profile:read` opens /provider-profiles. It does not authorize adding
    // one, and specification04 §1 says so in as many words: "read permission for its
    // page does not authorize it".
    authState.can = (p) => p === 'sessions:profile:read'
    const { container } = renderIntel(<NextStep />)
    expect(container).toBeEmptyDOMElement()
  })

  it('offers only the verbs this principal holds', () => {
    authState.can = (p) => p === 'sessions:run:write'
    renderIntel(<NextStep />)
    expect(screen.getByTestId('home-next-step-session')).toBeInTheDocument()
    expect(screen.queryByTestId('home-next-step-provider')).toBeNull()
    expect(screen.queryByTestId('home-next-step-agent')).toBeNull()
  })

  it('renders real copy, not raw i18n keys', () => {
    const { container } = renderIntel(<NextStep />)
    expect(screen.getByText('Add a provider')).toBeInTheDocument()
    expectNoRawI18nKeys(container)
  })

  it('every truncated line carries the full text on title=', () => {
    const { container } = renderIntel(<NextStep />)
    const truncated = [...container.querySelectorAll('.truncate')]
    expect(truncated.length).toBeGreaterThan(0)
    for (const el of truncated) {
      expect(
        el.getAttribute('title'),
        `truncated without title=: ${el.textContent}`,
      ).toBeTruthy()
    }
    const agent = screen
      .getByTestId('home-next-step-agent')
      .querySelector('.truncate')
    expect(agent?.getAttribute('title')).toMatch(/Deploy an agent/)
    expect(agent?.getAttribute('title')).toMatch(/Roll an agent out/)
  })
})
