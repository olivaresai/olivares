// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// The retention screen says when the engine composes the retention sweep but
// cannot run it (server-info jobs_not_running: a PostgreSQL database that cannot
// list every tenant), so its schedules are never mistaken for applied ones.
import { describe, expect, it, vi } from 'vitest'
import { DEFAULT_AUTH, renderIntel, screen } from '@/test/intel'
import '@/features/_intel'
import type { ServerInfo } from '@/lib/api/types'

let info: Partial<ServerInfo> | undefined
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({ data: info }),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...DEFAULT_AUTH }),
}))
const api = {
  dataClasses: vi.fn(),
  retentionPolicies: vi.fn(),
  retentionRuns: vi.fn(),
}
vi.mock('./api', async () => {
  const actual = await vi.importActual<typeof import('./api')>('./api')
  return { ...actual, complianceApi: api }
})

const { RetentionTab } = await import('./retention-view')
await import('./i18n')

function seed(next: Partial<ServerInfo> | undefined) {
  info = next
  api.dataClasses.mockResolvedValue({ items: [] })
  api.retentionPolicies.mockResolvedValue({ items: [] })
  api.retentionRuns.mockResolvedValue({ items: [] })
}

describe('retention says when the engine cannot run it', () => {
  it('says the database cannot list every tenant and links how to enable it', async () => {
    seed({
      jobs_not_running: [{ job: 'retention', reason: 'no_tenant_inventory' }],
    })
    renderIntel(<RetentionTab canAdmin canRead />)
    expect(
      await screen.findByText(
        /Retention is not running: this database cannot list every tenant\./,
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('link', { name: 'How to enable it' }),
    ).toHaveAttribute(
      'href',
      'https://docs.olivares.ai/reference/configuration/',
    )
  })

  it('says nothing when retention runs', async () => {
    seed({
      jobs_not_running: [
        { job: 'audit_checkpoints', reason: 'no_tenant_inventory' },
      ],
    })
    renderIntel(<RetentionTab canAdmin canRead />)
    await screen.findAllByText(/./)
    expect(screen.queryByText(/Retention is not running/)).toBeNull()
  })
})
