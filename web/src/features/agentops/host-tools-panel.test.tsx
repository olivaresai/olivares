// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const auth = vi.hoisted(() => ({
  tenant: 't1' as string | null,
  can: ((p: string) => p === 'sessions:profile:read') as (p: string) => boolean,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: auth.tenant,
    can: (p: string) => auth.can(p),
    principal: { user_id: 'u1', aal: 1 },
  }),
}))

import { HostToolsPanel } from './host-tools-panel'
import {
  launchRequestPermission,
  type LaunchReadinessCheck,
  type SessionLaunchReadiness,
} from './launch-readiness'
import {
  LaunchReadinessPanel,
  useProfileLaunchReadiness,
} from './launch-readiness-panel'
import { fixtureReadiness } from './launch-readiness.fixture'

// The real typed API method and HTTP client run against this fetch: the tests see the
// exact URL, method, query and abort signal the console sends.
type Call = { url: URL; init: RequestInit }
let calls: Call[] = []
let hostTools: (call: Call) => Promise<Response> | Response
let readinessBody: () => SessionLaunchReadiness

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
const apiError = (status: number, code: string) =>
  json({ error: { code, message: code } }, status)
const hostCalls = () =>
  calls.filter((c) => c.url.pathname.endsWith('/host-tools'))
const tick = (ms = 40) => new Promise((r) => setTimeout(r, ms))

function unresolved(
  program: 'not_configured' | 'unknown' = 'not_configured',
  over: Partial<SessionLaunchReadiness> = {},
): SessionLaunchReadiness {
  const check: LaunchReadinessCheck =
    program === 'unknown'
      ? { check: 'program', state: 'unknown', code: 'program_missing' }
      : { check: 'program', state: 'not_configured', code: 'program_missing' }
  return fixtureReadiness({
    configuration_state: 'not_configured',
    evaluated_environment_ref: 'xenv_1',
    checks: fixtureReadiness().checks.map((c) =>
      c.check === 'program' ? check : c,
    ),
    ...over,
  })
}

function observation(over: Record<string, unknown> = {}) {
  return {
    profile_ref: 'ppf_a',
    profile_version: 1,
    driver: 'claude',
    environment_ref: 'xenv_1',
    evaluated_environment_ref: 'xenv_1',
    observed_at: '2026-09-13T05:00:00Z',
    state: 'observed',
    groups: [
      {
        origin: 'path',
        match: 'unregistered-observed',
        executable: true,
        configured: 'same',
        count: 1,
      },
      {
        origin: 'managed',
        match: 'unverified',
        executable: false,
        configured: 'different',
        count: 2,
      },
    ],
    ...over,
  }
}

function holdHostTools() {
  const held: { call: Call; resolve: (r: Response) => void }[] = []
  hostTools = (call) =>
    new Promise<Response>((resolve, reject) => {
      call.init.signal?.addEventListener('abort', () =>
        reject(new DOMException('Aborted', 'AbortError')),
      )
      held.push({ call, resolve })
    })
  return held
}

function wrap(ui: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: 3, retryDelay: 1 } },
  })
  const view = render(
    <QueryClientProvider client={qc}>{ui}</QueryClientProvider>,
  )
  return {
    ...view,
    rerender: (next: ReactNode) =>
      view.rerender(
        <QueryClientProvider client={qc}>{next}</QueryClientProvider>,
      ),
  }
}

function Mounted() {
  const q = useProfileLaunchReadiness({
    enabled: true,
    profileRef: 'ppf_a',
    transport: 'stream-json',
    isolation: 'native',
  })
  return (
    <LaunchReadinessPanel
      query={q}
      profileRef="ppf_a"
      transport="stream-json"
      isolation="native"
    />
  )
}

beforeEach(() => {
  calls = []
  auth.tenant = 't1'
  auth.can = (p: string) => p === 'sessions:profile:read'
  hostTools = () => json(observation())
  readinessBody = () => unresolved()
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const call = { url: new URL(String(input), 'http://engine.test'), init }
      calls.push(call)
      if (call.url.pathname.endsWith('/host-tools')) return hostTools(call)
      if (call.url.pathname.endsWith('/launch-readiness')) {
        return json(readinessBody())
      }
      return apiError(404, 'not_found')
    }),
  )
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('HC1 host-tool observation — selected-profile readiness panel', () => {
  it.each(['not_configured', 'unknown'] as const)(
    'observes once, without a query, when the program check is %s, and leaves readiness unchanged',
    async (program) => {
      readinessBody = () => unresolved(program)
      wrap(<Mounted />)
      expect(await screen.findByText('Service PATH')).toBeInTheDocument()
      expect(hostCalls()).toHaveLength(1)
      const [call] = hostCalls()
      expect(call.url.pathname).toBe(
        '/v1/m/sessions/provider-profiles/ppf_a/host-tools',
      )
      expect(call.url.search).toBe('')
      expect(call.init.method ?? 'GET').toBe('GET')
      expect(screen.getByText('Missing configuration')).toBeInTheDocument()
      expect(screen.getByText(/Advisory only/)).toBeInTheDocument()
      expect(launchRequestPermission(unresolved(program))).toBe('block')
      expect(
        screen
          .getAllByRole('button')
          .map((b) => b.textContent)
          .filter((name) => name !== 'Observe again'),
      ).toEqual([])
      await tick()
      window.dispatchEvent(new Event('focus'))
      await tick()
      expect(hostCalls()).toHaveLength(1)
    },
  )

  it('does not observe when the program check is resolved', async () => {
    readinessBody = () =>
      fixtureReadiness({ evaluated_environment_ref: 'xenv_1' })
    wrap(<Mounted />)
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    await tick()
    expect(hostCalls()).toHaveLength(0)
    expect(screen.queryByTestId('host-tools')).not.toBeInTheDocument()
  })

  it('does not request without sessions:profile:read, and drops the observation when it is lost', async () => {
    auth.can = () => false
    const first = wrap(<HostToolsPanel readiness={unresolved()} />)
    await tick()
    expect(hostCalls()).toHaveLength(0)
    expect(screen.queryByTestId('host-tools')).not.toBeInTheDocument()
    first.unmount()

    auth.can = (p: string) => p === 'sessions:profile:read'
    const { rerender } = wrap(<HostToolsPanel readiness={unresolved()} />)
    expect(await screen.findByText('Service PATH')).toBeInTheDocument()
    auth.can = () => false
    rerender(<HostToolsPanel readiness={unresolved()} />)
    expect(screen.queryByTestId('host-tools')).not.toBeInTheDocument()
    await tick()
    expect(hostCalls()).toHaveLength(1)
  })
})

describe('HC1 host-tool observation — closed contract', () => {
  it('shows groups, counts, environment identity and configured match without raw fields', async () => {
    hostTools = () =>
      json(
        observation({
          notes: '/home/svc/.local/bin/claude',
          groups: [
            {
              origin: 'path',
              match: 'unregistered-observed',
              executable: true,
              configured: 'same',
              count: 1,
              path: '/usr/local/bin/claude',
            },
            {
              origin: 'teleport',
              match: 'blessed',
              executable: true,
              configured: 'maybe',
              count: 2,
            },
            {
              origin: 'unknown',
              match: 'unknown',
              executable: true,
              configured: 'unknown',
              count: 1,
            },
            {
              origin: 'managed',
              match: 'unverified',
              executable: false,
              configured: 'different',
              count: 2,
            },
          ],
        }),
      )
    wrap(<HostToolsPanel readiness={unresolved()} />)
    const list = await screen.findByRole('list', {
      name: 'Observed candidate groups',
    })
    expect(
      within(list)
        .getAllByRole('listitem')
        .map((li) => li.textContent),
    ).toEqual([
      'Managed installation location · Signature not verified · Not executable · Not the configured program · Candidates: 2',
      'Service PATH · Observed, not registered · Executable · Same file as the configured program · Candidates: 1',
      'Unknown location · Verification status unknown · Executable · Comparison with the configured program is unknown · Candidates: 3',
    ])
    expect(
      screen.getByText(
        'Candidates observed in the configured search locations: 6',
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByText('Profile environment: xenv_1 · Answered by: xenv_1'),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('region', {
        name: 'Official CLI candidates on the service host',
      }),
    ).toBeInTheDocument()
    const text = screen.getByTestId('host-tools').textContent ?? ''
    expect(text).not.toMatch(/\/usr|\/home|\.local|teleport|blessed|maybe/)
    expect(text).not.toMatch(/\binstalled\b|\bVerified\b/)
  })

  it.each([
    [
      'none_observed',
      /No candidate was observed in the configured search locations/,
    ],
    ['unsupported_driver', /not available for this driver yet/],
    ['unknown', /could not complete this observation/],
    [
      'not_checked_in_this_environment',
      /Not checked: the service that answered/,
    ],
    ['a_future_state', /could not complete this observation/],
  ])('renders %s with accurate copy and no groups', async (state, copy) => {
    hostTools = () => json(observation({ state, groups: [] }))
    wrap(<HostToolsPanel readiness={unresolved()} />)
    expect(await screen.findByText(copy)).toBeInTheDocument()
    expect(
      screen.queryByRole('list', { name: 'Observed candidate groups' }),
    ).not.toBeInTheDocument()
  })

  it('names a node without a persistent environment identity', async () => {
    hostTools = () =>
      json(
        observation({
          state: 'not_checked_in_this_environment',
          evaluated_environment_ref: '',
          groups: [],
        }),
      )
    wrap(
      <HostToolsPanel
        readiness={unresolved('not_configured', {
          evaluated_environment_ref: '',
        })}
      />,
    )
    expect(await screen.findByText(/Not checked/)).toBeInTheDocument()
    expect(
      screen.getByText(
        'Profile environment: xenv_1 · Answered by: a node without a persistent environment identity',
      ),
    ).toBeInTheDocument()
  })

  it.each([
    [
      'a zero count',
      observation({
        groups: [
          {
            origin: 'path',
            match: 'unverified',
            executable: true,
            configured: 'same',
            count: 0,
          },
        ],
      }),
      /could not be read/,
    ],
    [
      'observed without groups',
      observation({ groups: [] }),
      /could not be read/,
    ],
    [
      'a missing groups array',
      observation({ groups: undefined }),
      /could not be read/,
    ],
    [
      'groups on a non-observed state',
      observation({ state: 'none_observed' }),
      /could not be read/,
    ],
    [
      'another profile',
      observation({ profile_ref: 'ppf_b' }),
      /belonged to another/,
    ],
    [
      'another profile version',
      observation({ profile_version: 2 }),
      /belonged to another/,
    ],
    ['another driver', observation({ driver: 'codex' }), /belonged to another/],
    [
      'another profile environment',
      observation({ environment_ref: 'xenv_2' }),
      /belonged to another/,
    ],
    [
      'another answering environment',
      observation({ evaluated_environment_ref: 'xenv_9' }),
      /belonged to another/,
    ],
  ])('discards %s', async (_name, body, copy) => {
    hostTools = () => json(body)
    wrap(<HostToolsPanel readiness={unresolved()} />)
    expect(await screen.findByText(copy)).toBeInTheDocument()
    expect(
      screen.queryByRole('list', { name: 'Observed candidate groups' }),
    ).not.toBeInTheDocument()
    await tick()
    expect(hostCalls()).toHaveLength(1)
  })
})

describe('HC1 host-tool observation — errors, refresh and stale answers', () => {
  it.each([
    [401, 'unauthenticated', /no longer authenticated/],
    [403, 'forbidden', /cannot read this profile’s host tool observation/],
    [404, 'not_found', /not available in the current tenant/],
    [409, 'profile_changed', /profile changed during observation/],
    [503, 'unavailable', /could not be read/],
  ] as const)(
    'shows %s as a notice without retrying, and recovers on a manual refresh',
    async (status, code, copy) => {
      let n = 0
      hostTools = () =>
        n++ === 0 ? apiError(status, code) : json(observation())
      wrap(<HostToolsPanel readiness={unresolved()} />)
      expect(await screen.findByText(copy)).toBeInTheDocument()
      await tick()
      expect(hostCalls()).toHaveLength(1)
      await userEvent
        .setup()
        .click(screen.getByRole('button', { name: 'Observe again' }))
      expect(await screen.findByText('Service PATH')).toBeInTheDocument()
      expect(screen.queryByText(copy)).not.toBeInTheDocument()
      expect(hostCalls()).toHaveLength(2)
    },
  )

  it('hides the previous observation while a manual refresh is in flight', async () => {
    wrap(<HostToolsPanel readiness={unresolved()} />)
    expect(await screen.findByText('Service PATH')).toBeInTheDocument()
    const held = holdHostTools()
    await userEvent
      .setup()
      .click(screen.getByRole('button', { name: 'Observe again' }))
    expect(
      await screen.findByText(/Observing official CLI candidates/),
    ).toBeInTheDocument()
    expect(screen.queryByText('Service PATH')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Observing…/ })).toBeDisabled()
    await act(async () => {
      held[0].resolve(
        json(
          observation({
            groups: [
              {
                origin: 'named',
                match: 'damaged',
                executable: false,
                configured: 'different',
                count: 1,
              },
            ],
          }),
        ),
      )
    })
    expect(
      await screen.findByText('Named program location'),
    ).toBeInTheDocument()
  })

  it('profile A→B→A aborts the late B read and never repaints A from an earlier answer', async () => {
    const A = unresolved()
    const B = unresolved('not_configured', { profile_ref: 'ppf_b' })
    const { rerender } = wrap(<HostToolsPanel readiness={A} />)
    expect(await screen.findByText('Service PATH')).toBeInTheDocument()

    const held = holdHostTools()
    rerender(<HostToolsPanel readiness={B} />)
    expect(screen.queryByText('Service PATH')).not.toBeInTheDocument()
    await waitFor(() => expect(held).toHaveLength(1))
    expect(held[0].call.url.pathname).toContain('/ppf_b/')

    rerender(<HostToolsPanel readiness={A} />)
    expect(screen.queryByText('Service PATH')).not.toBeInTheDocument()
    await waitFor(() => expect(held).toHaveLength(2))
    await waitFor(() => expect(held[0].call.init.signal?.aborted).toBe(true))
    await act(async () => {
      held[0].resolve(json(observation({ profile_ref: 'ppf_b' })))
    })
    expect(screen.queryByText('Service PATH')).not.toBeInTheDocument()

    await act(async () => {
      held[1].resolve(json(observation()))
    })
    expect(await screen.findByText('Service PATH')).toBeInTheDocument()
    expect(hostCalls()).toHaveLength(3)
  })

  it('a new profile version is a new observation, not the old one relabeled', async () => {
    const { rerender } = wrap(<HostToolsPanel readiness={unresolved()} />)
    expect(await screen.findByText('Service PATH')).toBeInTheDocument()
    rerender(
      <HostToolsPanel
        readiness={unresolved('not_configured', { profile_version: 2 })}
      />,
    )
    expect(screen.queryByText('Service PATH')).not.toBeInTheDocument()
    expect(await screen.findByText(/belonged to another/)).toBeInTheDocument()
    expect(hostCalls()).toHaveLength(2)
  })

  it('tenant A→B→A cancels the in-flight read and never repaints the earlier tenant', async () => {
    const { rerender } = wrap(<HostToolsPanel readiness={unresolved()} />)
    expect(await screen.findByText('Service PATH')).toBeInTheDocument()

    const held = holdHostTools()
    auth.tenant = 't2'
    rerender(<HostToolsPanel readiness={unresolved()} />)
    expect(screen.queryByText('Service PATH')).not.toBeInTheDocument()
    await waitFor(() => expect(held).toHaveLength(1))

    auth.tenant = 't1'
    rerender(<HostToolsPanel readiness={unresolved()} />)
    expect(screen.queryByText('Service PATH')).not.toBeInTheDocument()
    await waitFor(() => expect(held).toHaveLength(2))
    await waitFor(() => expect(held[0].call.init.signal?.aborted).toBe(true))

    await act(async () => {
      held[1].resolve(json(observation()))
    })
    expect(await screen.findByText('Service PATH')).toBeInTheDocument()
  })

  it('answering environment A→B→A never repaints the earlier engine answer', async () => {
    const onA = unresolved()
    const onB = unresolved('not_configured', {
      evaluated_environment_ref: 'xenv_2',
    })
    const { rerender } = wrap(<HostToolsPanel readiness={onA} />)
    expect(await screen.findByText('Service PATH')).toBeInTheDocument()

    const held = holdHostTools()
    rerender(<HostToolsPanel readiness={onB} />)
    expect(screen.queryByText('Service PATH')).not.toBeInTheDocument()
    await waitFor(() => expect(held).toHaveLength(1))

    rerender(<HostToolsPanel readiness={onA} />)
    expect(screen.queryByText('Service PATH')).not.toBeInTheDocument()
    await waitFor(() => expect(held).toHaveLength(2))
    await waitFor(() => expect(held[0].call.init.signal?.aborted).toBe(true))

    await act(async () => {
      held[1].resolve(json(observation()))
    })
    expect(await screen.findByText('Service PATH')).toBeInTheDocument()
    expect(
      screen.getByText('Profile environment: xenv_1 · Answered by: xenv_1'),
    ).toBeInTheDocument()
  })

  it('unmount aborts the in-flight read', async () => {
    const held = holdHostTools()
    const { unmount } = wrap(<HostToolsPanel readiness={unresolved()} />)
    await waitFor(() => expect(held).toHaveLength(1))
    unmount()
    await waitFor(() => expect(held[0].call.init.signal?.aborted).toBe(true))
  })
})
