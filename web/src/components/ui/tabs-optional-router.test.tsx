// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The strip reads the router through `useRouter({ warn: false })`, whose installed
// no-provider contract is to answer undefined. These two controls pin the boundary of that
// contract, and are adopted from the independent review's fault-injection probe
// (assessments/implementation/console-tab-scroll-restoration/independent-review/
// optional-router-fault.test.tsx, 2026-09-06): the real hook is kept for its normal
// behaviour, and an explicitly injected Error stands in for "some other failure of the
// hook". Injection proves what the strip does with such an error; it is not a report of a
// naturally occurring one. Reintroducing only this file's subject — a `try { … } catch {
// return undefined }` around the hook — makes the second control fail: the reviewed commit's
// catch relabelled the injected error as an absent router, so the strip rendered normally and
// no boundary output appeared. Removing the catch is what makes that control pass.
import { render, screen } from '@testing-library/react'
import { Component, type ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

const probe = vi.hoisted(() => ({
  failure: undefined as Error | undefined,
  calls: 0,
}))
vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-router')>()
  return {
    ...actual,
    useRouter: (opts?: { warn?: boolean }) => {
      probe.calls++
      if (probe.failure) throw probe.failure
      return actual.useRouter(opts)
    },
  }
})

import { Tabs, TabsList, TabsTrigger } from './tabs'

class Boundary extends Component<
  { children: ReactNode },
  { error: Error | null }
> {
  state = { error: null as Error | null }
  static getDerivedStateFromError(error: Error) {
    return { error }
  }
  render() {
    return this.state.error ? (
      <div role="alert">{this.state.error.message}</div>
    ) : (
      this.props.children
    )
  }
}

function strip() {
  return (
    <Boundary>
      <Tabs defaultValue="a">
        <TabsList aria-label="Standalone">
          <TabsTrigger value="a">Alpha</TabsTrigger>
          <TabsTrigger value="b">Beta</TabsTrigger>
        </TabsList>
      </Tabs>
    </Boundary>
  )
}

afterEach(() => {
  probe.failure = undefined
  probe.calls = 0
})

describe('Tabs — the router hook is consulted as installed, never wrapped', () => {
  it('real useRouter without a RouterProvider: the hook answers undefined and the standalone strip works', () => {
    // jsdom logs React's error-boundary output for a thrown render; keep it quiet here.
    render(strip())
    expect(probe.calls).toBeGreaterThan(0)
    expect(screen.getByRole('tab', { name: 'Alpha' })).toHaveAttribute(
      'aria-selected',
      'true',
    )
    expect(screen.getByRole('tab', { name: 'Beta' })).toHaveAttribute(
      'aria-selected',
      'false',
    )
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('an injected hook error reaches the error boundary instead of being read as "no router"', () => {
    probe.failure = new Error('injected router hook failure')
    const quiet = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      render(strip())
    } finally {
      quiet.mockRestore()
    }
    expect(screen.getByRole('alert')).toHaveTextContent(
      'injected router hook failure',
    )
    expect(screen.queryByRole('tab')).toBeNull()
  })
})
