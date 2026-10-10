// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import type { WorkIntent } from './api'
import './i18n'

const api = vi.hoisted(() => ({ planWork: vi.fn(), getWorkItem: vi.fn() }))
vi.mock('./api', async (original) => ({
  ...(await original<typeof import('./api')>()),
  ...api,
}))
import { ApplyFlow } from './apply-flow'

const intent: WorkIntent = {
  key: '11111111-1111-4111-8111-111111111111',
  tenant: 't1',
  command: 'item.ready',
  path: '/v1/m/sessions/work-items/w1/transitions',
  method: 'POST',
  body: { command: 'item.ready' },
  etag: '"v1"',
}

function show() {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <ApplyFlow
        open
        onOpenChange={() => {}}
        intent={intent}
        title="Mark 'Ship the fix' ready"
        itemTitle="Ship the fix"
        dependencies={[{ id: 'dep1', work_item_id: 'w1', depends_on_id: 'w2' }]}
      />
    </QueryClientProvider>,
  )
}

beforeEach(() => vi.resetAllMocks())

it('keeps the engine plan and retry key behind closed Details before Apply', async () => {
  api.planWork.mockResolvedValue({
    verdict: 'LIMPIO',
    code: 'ok',
    command: 'item.ready',
    permission: 'sessions:work:write',
    event_type: 'work.item.transitioned',
    row_effects: ['work_items:update'],
    external_calls: [],
    checks: [],
    plan_hash: 'abc123',
  })
  show()
  await screen.findByRole('button', { name: 'Apply' })
  expect(
    screen.getByRole('heading', { name: "Mark 'Ship the fix' ready" }),
  ).toBeVisible()
  const details = screen.getByText('Details').closest('details')!
  expect(details).not.toHaveAttribute('open')
  for (const text of [
    'item.ready',
    'sessions:work:write',
    'work_items:update',
    'abc123',
    intent.key,
  ])
    expect(within(details).getByText(text)).toBeInTheDocument()
})

it('names the unfinished predecessor first and retains the exact refusal behind Details', async () => {
  api.planWork.mockRejectedValue(
    new ApiError(422, 'dependency_incomplete', 'dependency_incomplete'),
  )
  api.getWorkItem.mockResolvedValue({
    snapshot: { item: { title: 'Review the patch', status: 'ready' } },
  })
  show()
  expect(
    await screen.findByText(
      "'Ship the fix' waits for 'Review the patch' to be done.",
    ),
  ).toBeVisible()
  expect(screen.queryByRole('button', { name: 'Apply' })).toBeNull()
  const details = screen.getByText('Details').closest('details')!
  expect(details).not.toHaveAttribute('open')
  expect(within(details).getByTestId('engine-reason')).toHaveTextContent(
    'dependency_incomplete',
  )
  expect(api.getWorkItem).toHaveBeenCalledWith(
    'w2',
    { tenant: 't1' },
    expect.any(AbortSignal),
  )
})

it('names the predecessor when the engine returns a broken plan on HTTP 200', async () => {
  api.planWork.mockResolvedValue({
    verdict: 'ROTO',
    code: 'dependency_incomplete',
    command: 'item.ready',
    permission: '',
    event_type: '',
    row_effects: [],
    external_calls: [],
    checks: [{ name: 'dependency_incomplete', verdict: 'ROTO' }],
    plan_hash: '',
  })
  api.getWorkItem.mockResolvedValue({
    snapshot: { item: { title: 'Review the patch', status: 'draft' } },
  })
  show()
  expect(
    await screen.findByText(
      "'Ship the fix' waits for 'Review the patch' to be done.",
    ),
  ).toBeVisible()
  expect(screen.getByRole('button', { name: 'Apply' })).toBeDisabled()
  const details = screen.getByText('Details').closest('details')!
  expect(details).not.toHaveAttribute('open')
  expect(within(details).getByText('dependency_incomplete')).toBeInTheDocument()
})
