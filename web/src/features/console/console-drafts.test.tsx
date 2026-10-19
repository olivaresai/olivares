// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Settings } from 'lucide-react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { RequirePermission } from '@/components/layout/require-permission'
import type { FeatureView } from '@/features/registry'
import {
  capabilityQuestion,
  isPositive,
  useCapability,
  type CapabilityAccess,
} from '@/lib/auth/capabilities'
import { configureApiClient, __resetRefreshState } from '@/lib/api/client'
import { queryKeys } from '@/lib/api/query'
import { useWorkspaceStore } from '@/stores/workspace'
import { useTenantStore } from '@/stores/tenant'
import {
  AGENT_CREATION,
  GROUP_CREATION,
  GRANT_CREATION,
  useConsoleOperation,
} from './console-authority'
import { ConsoleDraftBoundary, useConsoleDraft } from './console-drafts'

const WS = '0192f2c0-aaaa-7000-8000-000000000001'
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    principal: { kind: 'user', actor: 'user:u1', user_id: 'u1' },
    activeTenant: 't1',
    can: () => false,
  }),
}))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({ select }: { select: (s: unknown) => unknown }) =>
    select({ location: { searchStr: '', pathname: '/console' } }),
}))
vi.mock('@/features/extensions', async (original) => ({
  ...(await original<typeof import('@/features/extensions')>()),
  PANEL_EXTENSIONS: {
    consoleAccess: {
      useOperation: (operation: string) => {
        const observation = useCapability(question(operation))
        return {
          ...observation,
          workspace: WS,
          current: isPositive(observation.access),
        }
      },
    },
  },
}))

const creations = [
  [AGENT_CREATION, 'agent:create:'],
  [GROUP_CREATION, 'group:create:'],
  [GRANT_CREATION, 'grant:create:'],
] as const
function question(operation: string) {
  return capabilityQuestion({ kind: 'surface', operation, workspaceId: WS })
}
const view: FeatureView = {
  id: 'console',
  path: '/console',
  icon: Settings,
  element: () => null,
  helpHref: '/reference/console',
  permission: 'tenant:admin',
  navigation: {
    kind: 'feature',
    areaId: 'system',
    sectionId: 'administration',
  },
  capability: {
    surface: () => question(AGENT_CREATION),
    navigation: () => [question(GROUP_CREATION), question(GRANT_CREATION)],
  },
  continuity: ConsoleDraftBoundary,
}

function Creation({
  operation,
  prefix,
}: {
  operation: string
  prefix: string
}) {
  const { current } = useConsoleOperation(operation, 'unused')
  const [open, setOpen] = useConsoleDraft(`${prefix}open`, false)
  return current ? (
    <>
      <button onClick={() => setOpen(true)}>{prefix}</button>
      {open && <Fields prefix={prefix} />}
    </>
  ) : null
}
function Fields({ prefix }: { prefix: string }) {
  const [name, setName] = useConsoleDraft(`${prefix}name`, '')
  return (
    <label>
      {prefix}name
      <input value={name} onChange={(event) => setName(event.target.value)} />
    </label>
  )
}

let client: QueryClient
const states: Record<string, CapabilityAccess> = {}
const errors = new Set<string>()
beforeEach(() => {
  for (const [operation] of creations) states[operation] = 'reachable'
  errors.clear()
  useWorkspaceStore.setState({ activeWorkspace: WS })
  useTenantStore.setState({ activeTenant: 't1' })
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    onUnauthorized: () => {},
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData(queryKeys.whoami, {
    kind: 'user',
    actor: 'user:u1',
    user_id: 'u1',
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url, options: RequestInit) => {
      const request = JSON.parse(String(options.body))
      if (errors.has(request.questions[0].operation))
        return new Response('{}', { status: 503 })
      return new Response(
        JSON.stringify({
          schema_version: 2,
          results: request.questions.map(
            (q: { id: string; kind: string; operation: string }) => ({
              id: q.id,
              kind: q.kind,
              state:
                states[q.operation] === 'step_up_required'
                  ? 'unknown'
                  : states[q.operation],
              code:
                states[q.operation] === 'reachable'
                  ? 'admitted'
                  : states[q.operation] === 'undisclosed'
                    ? 'not_disclosed'
                    : states[q.operation] === 'step_up_required'
                      ? 'step_up_required'
                      : 'not_permitted',
              ...(states[q.operation] === 'reachable'
                ? { refresh_after_ms: 30_000 }
                : {}),
            }),
          ),
        }),
        { headers: { 'Content-Type': 'application/json' } },
      )
    }),
  )
})
afterEach(() => {
  cleanup()
  client.clear()
  __resetRefreshState()
  vi.unstubAllGlobals()
})

it.each(
  creations.flatMap(([operation, prefix]) =>
    (['not_reachable', 'undisclosed', 'step_up_required'] as const).flatMap(
      (answer) => [
        { operation, prefix, mounted: true, answer },
        { operation, prefix, mounted: false, answer },
      ],
    ),
  ),
)(
  'retires $operation drafts on $answer with mounted=$mounted and admitted default-route siblings',
  async ({ operation, prefix, mounted, answer }) => {
    const user = userEvent.setup()
    let hidden: string | null = null
    const room = () => (
      <QueryClientProvider client={client}>
        <RequirePermission view={view}>
          <p>Admitted Console</p>
          {creations
            .filter(([op]) => op !== hidden)
            .map(([op, key]) => (
              <Creation key={op} operation={op} prefix={key} />
            ))}
        </RequirePermission>
      </QueryClientProvider>
    )
    const rendered = render(room())
    for (const [, key] of creations) {
      await user.click(await screen.findByRole('button', { name: key }))
      await user.type(screen.getByLabelText(`${key}name`), `Unsent ${key}`)
    }
    if (!mounted) {
      hidden = operation
      rendered.rerender(room())
    }
    states[operation] = answer
    await act(async () => {
      await client.invalidateQueries({ queryKey: ['auth-capabilities'] })
    })
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: prefix })).toBeNull(),
    )
    expect(screen.getByText('Admitted Console')).toBeInTheDocument()
    states[operation] = 'reachable'
    await act(async () => {
      await client.invalidateQueries({ queryKey: ['auth-capabilities'] })
    })
    hidden = null
    rendered.rerender(room())
    await screen.findByRole('button', { name: prefix })
    expect(screen.queryByLabelText(`${prefix}name`)).toBeNull()
    await user.click(screen.getByRole('button', { name: prefix }))
    expect(screen.getByLabelText(`${prefix}name`)).toHaveValue('')
    for (const [, key] of creations)
      if (key !== prefix)
        expect(screen.getByLabelText(`${key}name`)).toHaveValue(`Unsent ${key}`)
  },
)

it.each(
  creations.flatMap(([operation, prefix]) => [
    { operation, prefix, error: false },
    { operation, prefix, error: true },
  ]),
)(
  'preserves $operation drafts across an unestablished answer with transport-error=$error',
  async ({ operation, prefix, error }) => {
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={client}>
        <RequirePermission view={view}>
          <Creation operation={operation} prefix={prefix} />
        </RequirePermission>
      </QueryClientProvider>,
    )
    await user.click(await screen.findByRole('button', { name: prefix }))
    await user.type(screen.getByLabelText(`${prefix}name`), 'Unsent input')
    if (error) errors.add(operation)
    else states[operation] = 'unknown'
    await act(async () => {
      await client.invalidateQueries({ queryKey: ['auth-capabilities'] })
    })
    await waitFor(() =>
      expect(screen.queryByLabelText(`${prefix}name`)).toBeNull(),
    )
    errors.clear()
    states[operation] = 'reachable'
    await act(async () => {
      await client.invalidateQueries({ queryKey: ['auth-capabilities'] })
    })
    expect(await screen.findByLabelText(`${prefix}name`)).toHaveValue(
      'Unsent input',
    )
  },
)
