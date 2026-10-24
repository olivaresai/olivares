// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WCAG 2.5.3 Label in Name, as axe-core's label-content-name-mismatch measures it: a
// control's visible text is part of its accessible name. These are the controls the
// console audit found failing it on every page: the account button, the workspace
// card, the recent-work rows of Now and the fingerprint chips of the audit table.
// The negative fixture proves the rule runs here and is not clean by construction.
import axe from 'axe-core'
import type { ReactNode } from 'react'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import { HashChip } from '@/features/_intel'
import { RecentWork } from '@/features/home/recent-work'
import { mergeSessions } from '@/features/sessions/provenance'
import { UserMenu } from './user-menu'
import { WorkspaceSwitcher } from './workspace-switcher'

import '@/features/home/i18n'
import '@/features/sessions/i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    principal: { actor: 'user:admin', display_name: 'Admin', superadmin: true },
    isSuperadmin: true,
    activeRole: null,
    activeTenant: 'tenant-one',
    can: () => true,
    logout: vi.fn(),
  }),
}))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useRouterState: () => '',
  Link: ({ children, to, ...rest }: { children: ReactNode; to: string }) => (
    <a href={to} {...rest}>
      {children}
    </a>
  ),
}))
const { listWorkspaces } = vi.hoisted(() => ({ listWorkspaces: vi.fn() }))
vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return { ...actual, consoleApi: { ...actual.consoleApi, listWorkspaces } }
})
listWorkspaces.mockResolvedValue({
  items: [
    {
      id: 'ws-1',
      name: 'Default',
      slug: 'default',
      status: 'active',
      is_default: true,
      tenant_id: 'tenant-one',
      created_at: '2026-09-30T00:00:00Z',
      updated_at: '2026-09-30T00:00:00Z',
      version: 1,
    },
  ],
  has_more: false,
})

// jsdom has no canvas, and the rule measures each text node on one to skip icon
// ligatures: without a context it throws and reports nothing. This one draws every
// glyph as ink and measures every character the same, so no text is a ligature.
beforeAll(() => {
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(
    function (this: HTMLCanvasElement) {
      return {
        canvas: this,
        measureText: (s: string) => ({ width: s.length * 15 }),
        fillText: () => {},
        clearRect: () => {},
        getImageData: (_x: number, _y: number, w: number, h: number) => ({
          data: new Uint8ClampedArray(Math.ceil(w) * Math.ceil(h) * 4).fill(
            255,
          ),
        }),
      } as unknown as CanvasRenderingContext2D
    } as unknown as typeof HTMLCanvasElement.prototype.getContext,
  )
})

async function mismatches(root: Element) {
  const results = await axe.run(root, {
    runOnly: { type: 'rule', values: ['label-content-name-mismatch'] },
  })
  return results.violations
    .flatMap((v) => v.nodes)
    .map((n) => n.html.slice(0, 160))
}

describe('visible text is part of the accessible name (WCAG 2.5.3)', () => {
  it('the account button', async () => {
    const { container } = renderIntel(<UserMenu />)
    expect(
      screen.getByRole('button', { name: 'AD, Account' }),
    ).toHaveTextContent('AD')
    expect(await mismatches(container)).toEqual([])
  })

  it('the workspace card', async () => {
    const { container } = renderIntel(<WorkspaceSwitcher variant="card" />)
    // The name is the card's own text, so it still says which workspace is current.
    await screen.findByRole('button', { name: /^All workspaces/ })
    expect(await mismatches(container)).toEqual([])
  })

  it('the recent-work row', async () => {
    const sessions = mergeSessions(
      [
        {
          session_ref: 'sess-coder-7a3f',
          live_ref: '01a0b0b2-e581-71b8-a163-5f73c7ed6aaa',
          attribution: 'legacy',
          cc_state: 'idle',
          summary: 'Filed PR 7723',
          event_count: 3,
          tool_call_count: 2,
          first_event_at: '2026-09-17T18:47:59Z',
          last_event_at: '2026-09-17T18:48:14Z',
          duration_seconds: 15,
          input_tokens: 0,
          output_tokens: 0,
          cost_micro_usd: 0,
        },
      ],
      [],
    )
    const { container } = renderIntel(
      <RecentWork sessions={sessions} state="ready" />,
    )
    expect(await screen.findByTestId('home-recent-row')).toHaveAccessibleName(
      /Filed PR 7723/,
    )
    expect(await mismatches(container)).toEqual([])
  })

  it.each([undefined, 'seq 12'])(
    'the fingerprint chip (label %s)',
    async (label) => {
      const { container } = renderIntel(
        <HashChip hash={'ab12cd34'.repeat(8)} label={label} />,
      )
      expect(screen.getByRole('button')).toHaveAccessibleName(
        new RegExp(`^${label ? `${label} ` : ''}ab12.*, Copy full value$`),
      )
      expect(await mismatches(container)).toEqual([])
    },
  )

  it('catches a button whose name leaves out its visible text', async () => {
    const host = document.createElement('div')
    host.innerHTML = '<button type="button" aria-label="Account">AD</button>'
    document.body.append(host)
    expect(await mismatches(host)).toHaveLength(1)
    host.remove()
  })
})
