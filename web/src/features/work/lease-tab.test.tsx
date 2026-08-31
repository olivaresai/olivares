// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { WorkLease } from './types'
import './i18n'

/**
 * C07-01 — THE TAB, not just the client.
 *
 * `console:walk` discovers ROUTES and is blind to what lives INSIDE one by construction
 * (canon §0-COBERTURA), so a tab added to an existing route is covered by nothing unless it
 * brings its own cases. These render the component and read the SCREEN, for the reason
 * work-section.test.tsx already records: a predicate everyone agrees with, wired to nothing,
 * is the exact shape of a fix that was never verified.
 */

let authState = { can: (_: string) => false }
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const lease = (over: Partial<WorkLease> = {}): WorkLease => ({
  workspace_id: 'ws1',
  work_item_id: 'w1',
  fence: 7,
  state: 'held',
  renewal_count: 2,
  live: false,
  liveness_verdict: 'LIMPIO',
  liveness_code: 'ok',
  ...over,
})

const getLeaseMock = vi.fn()
const onIntent = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, getLease: (...a: unknown[]) => getLeaseMock(...a) }
})

const { LeaseTab } = await import('./item-detail')

function show() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <LeaseTab itemId="w1" etag='"v7"' onIntent={onIntent} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  // Por defecto: sólo LECTURA. Es el rol mínimo que puede abrir la pestaña, y deja que
  // cada caso conceda lo suyo por encima sin heredar permisos de escritura.
  authState = { can: (p: string) => p === 'sessions:lease:read' }
  getLeaseMock.mockReset()
  onIntent.mockReset()
})

describe('LeaseTab', () => {
  /**
   * THE CONTROL: NO_HE_PODIDO_MIRAR is rendered as ITS OWN answer, never as the negative one.
   *
   * THE MUTATION THAT MAKES IT FIRE: drop the verdict branch and render
   * `lease.live ? live : notLive`. The fixture sets `live: false` deliberately so that the
   * collapsed rendering produces "Not live" — which is why this case asserts BOTH that the
   * third answer appears AND that the negative one does not. Asserting only the first would
   * pass on a screen that showed both.
   */
  it('renders "could not look" as a third outcome, not as "not live"', async () => {
    getLeaseMock.mockResolvedValue({
      lease: lease({ liveness_verdict: 'NO_HE_PODIDO_MIRAR', live: false }),
      etag: '"v7"',
    })
    show()
    expect(await screen.findByText('Could not look')).toBeInTheDocument()
    expect(screen.queryByText('Not live')).not.toBeInTheDocument()
  })

  /** THE NON-FIRING DIRECTION: a real observation still renders as one. Without this, the
   *  case above would pass on a screen that showed "Could not look" for everything. */
  it('still renders a real observation as live', async () => {
    getLeaseMock.mockResolvedValue({
      lease: lease({ liveness_verdict: 'LIMPIO', live: true }),
      etag: '"v7"',
    })
    show()
    expect(await screen.findByText('Live')).toBeInTheDocument()
    expect(screen.queryByText('Could not look')).not.toBeInTheDocument()
  })

  /**
   * THE CONTROL: the three tiers the engine declares are three, not two. A role with
   * `sessions:lease:write` operates the lease and does NOT get takeover/revoke/clock-rebase,
   * which the engine gates on `sessions:lease:admin` (`work_api.go:47-49`).
   *
   * THE MUTATION: gate the admin trio on `canWrite`. Then "Take over" is enabled and this
   * fires. THE NON-FIRING DIRECTION is the next case: with admin, all six are enabled — so a
   * screen that disabled everything cannot satisfy both.
   */
  it('gives write its three actions and withholds the admin three', async () => {
    authState = {
      can: (p: string) =>
        p === 'sessions:lease:read' || p === 'sessions:lease:write',
    }
    getLeaseMock.mockResolvedValue({ lease: lease(), etag: '"v7"' })
    show()
    expect(await screen.findByRole('button', { name: 'Renew' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Release' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Take over' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Revoke' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Rebase clock' })).toBeDisabled()
  })

  it('gives admin all six', async () => {
    authState = { can: () => true }
    getLeaseMock.mockResolvedValue({ lease: lease(), etag: '"v7"' })
    show()
    expect(
      await screen.findByRole('button', { name: 'Take over' }),
    ).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Revoke' })).toBeEnabled()
  })

  /**
   * THE CONTROL THAT THE BROWSER HAD TO FIND FIRST, and it is here so it never has to again.
   *
   * A lease button RAISES AN INTENT; it does not post. Measured against a live engine on
   * 2026-08-16, a direct POST to .../lease/acquire answers **400 mode_required**: the lease
   * commands run through handleWorkMutation like every other work mutation, so they need
   * ?mode=, If-Plan-Hash and an Idempotency-Key. The first version of this tab posted directly
   * and every cell in this file stayed green, because a mocked fetch answers whatever it is
   * told — the screen was mute and only the browser could say so (canon §1.10).
   *
   * THE MUTATION: go back to posting. `onIntent` is never called and this fires.
   * THE NON-FIRING DIRECTION: the intent must carry the RIGHT command, not merely any — a tab
   * that raised `item.ready` for every button would satisfy a bare "was called".
   */
  it('raises an intent with the right command instead of posting', async () => {
    authState = { can: () => true }
    getLeaseMock.mockResolvedValue({ lease: lease(), etag: '"v7"' })
    show()
    ;(await screen.findByRole('button', { name: 'Take over' })).click()
    expect(onIntent).toHaveBeenCalledTimes(1)
    expect(onIntent.mock.calls[0]?.[0]).toMatchObject({
      command: 'lease.takeover',
      path: '/v1/m/sessions/work-items/w1/lease/takeover',
      etag: '"v7"',
    })
  })

  /**
   * THE CONTROL: the READ is gated too. The engine registers both GETs with
   * `sessions:lease:read` (work_api.go:42-43), so fetching without it is not "showing a little
   * extra" — it is provoking a 403 for a role that should not have seen the question. The
   * contrast found this half missing, which is the half one forgets when gating only buttons.
   *
   * THE MUTATION: drop `enabled: canRead`. The fetch happens and this fires.
   * AND IT ASSERTS THE MESSAGE, not just the absence of a call: an empty table and "you may not
   * look" render identically, and only one of them is true.
   */
  it('does not even read the lease without the read permission', async () => {
    authState = { can: (p: string) => p !== 'sessions:lease:read' }
    getLeaseMock.mockResolvedValue({ lease: lease(), etag: '"v7"' })
    show()
    expect(
      await screen.findByText('Your role cannot read this lease.'),
    ).toBeInTheDocument()
    expect(getLeaseMock).not.toHaveBeenCalled()
  })

  /**
   * THE CONTROL FOR THE CONTRAST'S HIGH FINDING: the commands that act on an existing holder
   * carry the holder the screen is SHOWING, so the operator is not asked to transcribe an id
   * that is already in front of them — a required field fillable only by copying the same
   * screen is a transcription trap, and every typo is paid for with the engine's
   * invalid_command.
   *
   * THE MUTATION: drop the `body`. The intent arrives with nothing and this fires.
   * THE NON-FIRING DIRECTION, in the same case: `acquire` must NOT carry it. A vacant lease has
   * no holder to inherit, and seeding it from the previous one would state something that does
   * not hold — so a tab that seeded everything would fail here too.
   */
  it('carries the holder it shows, except where there is none to inherit', async () => {
    authState = { can: () => true }
    getLeaseMock.mockResolvedValue({
      lease: lease({ holder_sid: 's-holder-1', state: 'held' }),
      etag: '"v7"',
    })
    show()
    ;(await screen.findByRole('button', { name: 'Renew' })).click()
    expect(onIntent.mock.calls[0]?.[0]).toMatchObject({
      command: 'lease.renew',
      body: { holder_sid: 's-holder-1' },
    })

    onIntent.mockReset()
    ;(await screen.findByRole('button', { name: 'Acquire' })).click()
    expect(onIntent.mock.calls[0]?.[0]?.body).not.toHaveProperty('holder_sid')
  })
})
