// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// E5a: the audit-spool budget the API emits is typed
// and rendered — present = a card with status/mode/usage; absent = silence
// (never a fabricated "OK"), like the OTA indicator.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { consoleKeys } from '@/features/console/api'
import type { HealthSummaryDTO, UpdateStatusDTO } from '@/features/console/api'
import './i18n'

const { api } = vi.hoisted(() => ({
  api: {
    healthSummary: vi.fn(),
    keyCustody: vi.fn(),
    effectiveConfig: vi.fn(),
    busSnapshot: vi.fn(),
    updateCheck: vi.fn(),
  },
}))

vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return {
    ...actual,
    consoleApi: { ...actual.consoleApi, ...api },
  }
})

vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))

import { SystemHealthTab } from './system-health-tab'

const baseSummary = {
  healthy: true,
  ready: true,
  store_engine: 'sqlite',
  connectors_available: 101,
  connectors_configured: 2,
  connectors_running: 2,
  connectors_error: 0,
  users: 3,
  sso_configured: false,
  version: '26.6.0',
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return {
    ...render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>),
    queryClient: qc,
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

beforeEach(() => {
  vi.clearAllMocks()
  api.keyCustody.mockResolvedValue({ keys: [] })
  api.effectiveConfig.mockResolvedValue({
    entries: [],
    strict_violations: [],
  })
  api.busSnapshot.mockResolvedValue({
    subscribers: [],
    publish_blocked: 0,
    dropped: 0,
    dropped_telemetry: 0,
    dropped_notify: 0,
    handler_errors: 0,
    enqueued: 0,
    handled: 0,
  })
  api.updateCheck.mockResolvedValue({
    enabled: true,
    available: false,
    up_to_date: true,
    channel: 'stable',
    current_version: '26.6.0',
  })
  vi.stubGlobal('fetch', vi.fn())
  vi.stubGlobal(
    'URL',
    Object.assign(URL, {
      createObjectURL: vi.fn(() => 'blob:support-bundle'),
      revokeObjectURL: vi.fn(),
    }),
  )
})

describe('SystemHealthTab audit spool', () => {
  it('stays silent when no budget is declared', async () => {
    api.healthSummary.mockResolvedValue(baseSummary)
    wrap(<SystemHealthTab />)
    await screen.findByText('sqlite')
    expect(screen.queryByText(/audit spool/i)).not.toBeInTheDocument()
  })

  it('renders the budget card with usage and mode', async () => {
    api.healthSummary.mockResolvedValue({
      ...baseSummary,
      audit_spool: {
        max_bytes: 1073741824, // 1 GiB
        used_bytes: 536870912, // 512 MiB → 50%
        mode: 'block',
        engaged: false,
      },
    })
    wrap(<SystemHealthTab />)
    expect(await screen.findByText(/audit spool/i)).toBeInTheDocument()
    expect(screen.getByText(/within budget/i)).toBeInTheDocument()
    expect(screen.getByText(/block on full/i)).toBeInTheDocument()
    expect(screen.getByText(/512 MiB.*1 GiB.*50%/)).toBeInTheDocument()
  })

  it('flags an engaged budget and unsealed evidence drops', async () => {
    api.healthSummary.mockResolvedValue({
      ...baseSummary,
      audit_spool: {
        max_bytes: 1024,
        used_bytes: 1024,
        mode: 'degrade',
        engaged: true,
        pending_drop_tenants: 2,
        pending_drops: 7,
      },
    })
    wrap(<SystemHealthTab />)
    expect(await screen.findByText(/budget engaged/i)).toBeInTheDocument()
    expect(screen.getByText(/degrade on full/i)).toBeInTheDocument()
    expect(
      screen.getByText(/7 unsealed evidence drops across 2 tenants/i),
    ).toBeInTheDocument()
  })

  it('warns when usage crosses the near-full threshold before engaging', async () => {
    api.healthSummary.mockResolvedValue({
      ...baseSummary,
      audit_spool: {
        max_bytes: 1000,
        used_bytes: 850, // 85% > 80% threshold, not yet engaged
        mode: 'block',
        engaged: false,
      },
    })
    wrap(<SystemHealthTab />)
    expect(await screen.findByText(/near budget/i)).toBeInTheDocument()
  })
})

// The connectors card used to render the CATALOG size (the ~100 kinds this build
// can wire) as "N active", so a clean install claimed a hundred live connectors
// while nothing was configured and nothing was running.
describe('SystemHealthTab connectors card', () => {
  it('reads as ready to configure on a clean install, never as a live fleet', async () => {
    api.healthSummary.mockResolvedValue({
      ...baseSummary,
      connectors_available: 101,
      connectors_configured: 0,
      connectors_running: 0,
    })
    wrap(<SystemHealthTab />)

    expect(await screen.findByText('No connectors configured')).toBeVisible()
    expect(screen.getByText('101 available to configure')).toBeVisible()
    // The catalog number must never appear as a runtime count.
    expect(screen.queryByText('101 running')).not.toBeInTheDocument()
    expect(screen.queryByText('101 configured')).not.toBeInTheDocument()
  })

  it('offers no catalog line when the build advertises no connector kinds', async () => {
    api.healthSummary.mockResolvedValue({
      ...baseSummary,
      connectors_available: 0,
      connectors_configured: 0,
      connectors_running: 0,
    })
    wrap(<SystemHealthTab />)

    expect(await screen.findByText('No connectors configured')).toBeVisible()
    expect(
      screen.queryByText('0 available to configure'),
    ).not.toBeInTheDocument()
  })

  it('headlines what is RUNNING and states separately what is configured', async () => {
    api.healthSummary.mockResolvedValue({
      ...baseSummary,
      connectors_available: 101,
      connectors_configured: 3,
      connectors_running: 1,
      connectors_error: 2,
    })
    wrap(<SystemHealthTab />)

    expect(await screen.findByText('1 running')).toBeVisible()
    expect(screen.getByText('3 configured')).toBeVisible()
    expect(screen.getByText('2 in error')).toBeVisible()
    expect(screen.queryByText('101 running')).not.toBeInTheDocument()
    expect(
      screen.queryByText('No connectors configured'),
    ).not.toBeInTheDocument()
  })

  it('says zero running when every configured connector is down', async () => {
    api.healthSummary.mockResolvedValue({
      ...baseSummary,
      connectors_available: 101,
      connectors_configured: 2,
      connectors_running: 0,
      connectors_error: 2,
    })
    wrap(<SystemHealthTab />)

    expect(await screen.findByText('0 running')).toBeVisible()
    expect(screen.getByText('2 configured')).toBeVisible()
    // Configured-but-dead is not the same as nothing configured.
    expect(
      screen.queryByText('No connectors configured'),
    ).not.toBeInTheDocument()
  })
})

describe('SystemHealthTab infrastructure cards', () => {
  it('renders TLS expiry, advisory links, and saturated bus subscribers', async () => {
    api.healthSummary.mockResolvedValue({
      ...baseSummary,
      tls_not_after: '2026-08-13T10:00:00Z',
      tls_days_left: 20,
      update: {
        enabled: true,
        available: true,
        up_to_date: false,
        channel: 'security',
        current_version: '26.6.0',
        latest_version: '26.6.1',
        security: true,
        advisories: ['CVE-2026-1234', 'GHSA-abcd-efgh-ijkl'],
      },
    })
    api.busSnapshot.mockResolvedValue({
      subscribers: [
        { name: 'audit-writer', class: 'critical', depth: 8, capacity: 10 },
      ],
      publish_blocked: 1,
      dropped: 2,
      dropped_telemetry: 3,
      dropped_notify: 4,
      handler_errors: 5,
      enqueued: 100,
      handled: 90,
    })

    wrap(<SystemHealthTab />)

    const tlsBadge = await screen.findByText('20 days left')
    expect(tlsBadge.className).toMatch(/warning/)
    expect(screen.getByRole('link', { name: /CVE-2026-1234/ })).toHaveAttribute(
      'href',
      'https://osv.dev/vulnerability/CVE-2026-1234',
    )
    expect(
      screen.getByRole('link', { name: /GHSA-abcd-efgh-ijkl/ }),
    ).toHaveAttribute(
      'href',
      'https://osv.dev/vulnerability/GHSA-abcd-efgh-ijkl',
    )
    const progress = await screen.findByRole('progressbar', {
      name: 'Queue depth for audit-writer',
    })
    expect(progress.firstElementChild).toHaveClass('bg-warning')
    expect(screen.getByText('Publish blocked')).toBeInTheDocument()
  })

  it('renders custody metadata, sealer presence, and redacted config', async () => {
    api.healthSummary.mockResolvedValue(baseSummary)
    api.keyCustody.mockResolvedValue({
      keys: [
        {
          purpose: 'audit',
          algorithm: 'Ed25519',
          custody_mode: 'file',
          kek: 'vault',
          created: '2026-07-20T00:00:00Z',
          fingerprint: '0123456789abcdef0123456789abcdef',
          prior_count: 2,
        },
        {
          purpose: 'secret-store',
          source: 'env',
          present: true,
        },
      ],
    })
    api.effectiveConfig.mockResolvedValue({
      entries: [
        {
          key: 'OLIVARES_SIGNING_KEY',
          value: '<redacted>',
          redacted: true,
          source: 'env',
        },
      ],
      strict_violations: [],
    })

    wrap(<SystemHealthTab />)

    expect(await screen.findByText('Ed25519')).toBeInTheDocument()
    expect(screen.getByText('0123456789…abcdef')).toBeInTheDocument()
    expect(screen.getByText('secret-store')).toBeInTheDocument()
    expect(screen.getByText('OLIVARES_SIGNING_KEY')).toBeInTheDocument()
    expect(screen.getByText('Redacted')).toBeInTheDocument()
  })

  it('offers local recovery instructions without checking on an unconfigured default', async () => {
    api.healthSummary.mockResolvedValue(baseSummary)
    const user = userEvent.setup()
    wrap(<SystemHealthTab />)

    const check = await screen.findByRole('button', { name: 'Check now' })
    await waitFor(() => expect(check).toBeDisabled())
    expect(check).toHaveAccessibleDescription(
      /Update checking is not configured/,
    )
    await user.click(screen.getByText('Update instructions'))
    expect(screen.getByText(/embedded verification key/)).toBeVisible()
    expect(screen.getByText(/OLIVARES_UPDATE_ENDPOINT/)).toBeVisible()
    expect(screen.getByText(/restart Olivares/)).toBeVisible()
    expect(api.updateCheck).not.toHaveBeenCalled()
    expect(screen.queryByText('Up to date')).not.toBeInTheDocument()
  })

  it('offers recovery after a 501 without retaining a stale up-to-date claim', async () => {
    api.healthSummary.mockResolvedValue({
      ...baseSummary,
      update: {
        enabled: true,
        up_to_date: true,
        available: false,
        channel: 'stable',
        current_version: '1.0',
      },
    })
    api.updateCheck.mockRejectedValue(
      new ApiError(501, 'not_implemented', 'update checking not configured'),
    )
    const user = userEvent.setup()
    wrap(<SystemHealthTab />)

    await user.click(await screen.findByRole('button', { name: 'Check now' }))

    expect(
      await screen.findByText(/Update checking is not configured/),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Check now' })).toBeDisabled()
    await user.click(screen.getByText('Update instructions'))
    expect(screen.getByText(/embedded verification key/)).toBeVisible()
    expect(screen.queryByText('Up to date')).not.toBeInTheDocument()
  })

  it('checks a configured channel and displays the fresh result', async () => {
    api.healthSummary.mockResolvedValue({
      ...baseSummary,
      update: {
        enabled: true,
        up_to_date: false,
        available: false,
        channel: 'stable',
        current_version: '1.0',
      },
    })
    api.updateCheck.mockResolvedValue({
      enabled: true,
      up_to_date: false,
      available: true,
      channel: 'stable',
      current_version: '1.0',
      latest_version: '1.1',
    })
    const user = userEvent.setup()
    wrap(<SystemHealthTab />)

    await screen.findByText('sqlite')
    await user.click(screen.getByRole('button', { name: 'Check now' }))
    expect(await screen.findByText('1.1')).toBeVisible()
    expect(api.updateCheck).toHaveBeenCalledTimes(1)
    expect(screen.queryByText('Update instructions')).not.toBeInTheDocument()
  })

  it('allows checking after a newer health response confirms recovery from a 501', async () => {
    const configured = {
      ...baseSummary,
      update: {
        enabled: true,
        up_to_date: false,
        available: false,
        channel: 'stable',
        current_version: '1.0',
      },
    }
    api.healthSummary.mockResolvedValue(configured)
    api.updateCheck.mockRejectedValueOnce(
      new ApiError(501, 'not_implemented', 'update checking not configured'),
    )
    const user = userEvent.setup()
    const { queryClient } = wrap(<SystemHealthTab />)
    await screen.findByText('sqlite')
    await user.click(screen.getByRole('button', { name: 'Check now' }))
    await screen.findByText(/Update checking is not configured/)

    await act(async () => {
      await queryClient.refetchQueries({
        queryKey: consoleKeys.healthSummary(),
      })
    })

    expect(api.healthSummary).toHaveBeenCalledTimes(2)
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Check now' })).toBeEnabled(),
    )
    await user.click(screen.getByRole('button', { name: 'Check now' }))
    expect(await screen.findByText('Up to date')).toBeVisible()
    expect(api.updateCheck).toHaveBeenCalledTimes(2)
    expect(screen.queryByText('Update instructions')).not.toBeInTheDocument()
  })

  it('allows an explicit check when the health summary could not load', async () => {
    api.healthSummary.mockRejectedValue(new Error('Health summary unavailable'))
    const user = userEvent.setup()
    wrap(<SystemHealthTab />)
    const check = await screen.findByRole('button', { name: 'Check now' })
    await waitFor(() => expect(check).toBeEnabled())
    await user.click(check)
    expect(await screen.findByText('Up to date')).toBeVisible()
    expect(api.updateCheck).toHaveBeenCalledTimes(1)
  })

  it('honors a 501 that arrives after an overlapping health refresh', async () => {
    api.healthSummary.mockResolvedValue({
      ...baseSummary,
      update: {
        enabled: true,
        up_to_date: true,
        available: false,
        channel: 'stable',
        current_version: '1.0',
      },
    })
    let rejectCheck!: (reason: Error) => void
    api.updateCheck.mockReturnValue(
      new Promise((_, reject) => {
        rejectCheck = reject
      }),
    )
    const user = userEvent.setup()
    const { queryClient } = wrap(<SystemHealthTab />)
    await screen.findByText('sqlite')
    await user.click(screen.getByRole('button', { name: 'Check now' }))
    await act(async () => {
      await queryClient.refetchQueries({
        queryKey: consoleKeys.healthSummary(),
      })
    })
    expect(api.healthSummary).toHaveBeenCalledTimes(2)
    await act(async () => {
      rejectCheck(
        new ApiError(501, 'not_implemented', 'update checking not configured'),
      )
    })

    expect(
      await screen.findByText(/Update checking is not configured/),
    ).toBeVisible()
    expect(screen.getByRole('button', { name: 'Check now' })).toBeDisabled()
    expect(screen.queryByText('Up to date')).not.toBeInTheDocument()
  })

  describe.each(['health first', 'check first'] as const)(
    'overlapping refresh: %s',
    (order) => {
      it.each(['501', 'verification error', 'available release'] as const)(
        'preserves the newer %s result and allows a later health read',
        async (outcome) => {
          const staleSummary = {
            ...baseSummary,
            update: {
              enabled: true,
              available: false,
              up_to_date: true,
              channel: 'stable',
              current_version: '1.0',
            },
          }
          const health = deferred<HealthSummaryDTO>()
          const check = deferred<UpdateStatusDTO>()
          api.healthSummary
            .mockResolvedValue(staleSummary)
            .mockResolvedValueOnce(staleSummary)
            .mockReturnValueOnce(health.promise)
          api.updateCheck.mockReturnValueOnce(check.promise)
          const user = userEvent.setup()
          const { queryClient } = wrap(<SystemHealthTab />)
          await screen.findByText('Up to date')
          await user.click(screen.getByRole('button', { name: 'Check now' }))

          const refresh = queryClient.refetchQueries({
            queryKey: consoleKeys.healthSummary(),
          })
          await waitFor(() =>
            expect(api.healthSummary).toHaveBeenCalledTimes(2),
          )
          const completeHealth = async () => {
            await act(async () => {
              health.resolve(staleSummary)
              await refresh
              // Query notifications are scheduled after the response settles.
              await new Promise((resolve) => setTimeout(resolve, 0))
            })
          }
          if (order === 'health first') await completeHealth()
          await act(async () => {
            if (outcome === '501') {
              check.reject(
                new ApiError(
                  501,
                  'not_implemented',
                  'update checking not configured',
                ),
              )
            } else {
              check.resolve({
                ...staleSummary.update,
                up_to_date: false,
                available: outcome === 'available release',
                ...(outcome === 'verification error'
                  ? { error: 'signature does not verify' }
                  : { latest_version: '1.1' }),
              })
            }
          })
          const expectCheckResult = async () => {
            if (outcome === '501') {
              expect(
                await screen.findByText(/Update checking is not configured/),
              ).toBeVisible()
              expect(screen.getByText('Update instructions')).toBeVisible()
              expect(
                screen.getByRole('button', { name: 'Check now' }),
              ).toBeDisabled()
            } else if (outcome === 'verification error') {
              expect(await screen.findByRole('alert')).toHaveTextContent(
                'signature does not verify',
              )
            } else {
              expect(await screen.findByText('1.1')).toBeVisible()
              expect(screen.getByText('Update available')).toBeVisible()
            }
            expect(screen.queryByText('Up to date')).not.toBeInTheDocument()
          }
          await expectCheckResult()
          if (order === 'check first') {
            await completeHealth()
            await expectCheckResult()
          }

          await act(async () => {
            await queryClient.refetchQueries({
              queryKey: consoleKeys.healthSummary(),
            })
          })
          expect(api.healthSummary).toHaveBeenCalledTimes(3)
          expect(await screen.findByText('Up to date')).toBeVisible()
          expect(screen.queryByRole('alert')).not.toBeInTheDocument()
          expect(screen.queryByText('1.1')).not.toBeInTheDocument()
          expect(
            screen.queryByText('Update instructions'),
          ).not.toBeInTheDocument()
          expect(
            screen.getByRole('button', { name: 'Check now' }),
          ).toBeEnabled()
        },
      )
    },
  )

  it('keeps a failed configured check retryable and surfaces its error', async () => {
    api.healthSummary.mockResolvedValue({
      ...baseSummary,
      update: {
        enabled: true,
        up_to_date: false,
        available: false,
        channel: 'stable',
        current_version: '1.0',
      },
    })
    api.updateCheck.mockRejectedValue(
      new ApiError(503, 'unavailable', 'Channel unreachable'),
    )
    const user = userEvent.setup()
    wrap(<SystemHealthTab />)

    await screen.findByText('sqlite')
    await user.click(screen.getByRole('button', { name: 'Check now' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Channel unreachable',
    )
    expect(screen.getByRole('button', { name: 'Check now' })).toBeEnabled()
    expect(screen.queryByText('Update instructions')).not.toBeInTheDocument()
  })

  it('confirms and downloads the support bundle through raw fetch', async () => {
    api.healthSummary.mockResolvedValue(baseSummary)
    const rawFetch = vi.mocked(fetch)
    rawFetch.mockResolvedValue(
      new Response(new Blob(['bundle']), {
        status: 200,
        headers: {
          'Content-Type': 'application/octet-stream',
          'Content-Disposition':
            'attachment; filename="olivares-support-test.tar.gz"',
        },
      }),
    )
    const click = vi
      .spyOn(HTMLAnchorElement.prototype, 'click')
      .mockImplementation(() => undefined)
    const user = userEvent.setup()
    wrap(<SystemHealthTab />)

    await user.click(
      await screen.findByRole('button', {
        name: 'Download support bundle',
      }),
    )
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent(
      'redacted effective configuration, operational status and logs',
    )
    await user.click(
      within(dialog).getByRole('button', {
        name: 'Generate and download',
      }),
    )

    await waitFor(() =>
      expect(rawFetch).toHaveBeenCalledWith(
        '/v1/console/support-bundle',
        expect.objectContaining({
          method: 'POST',
          credentials: 'same-origin',
        }),
      ),
    )
    await waitFor(() => expect(click).toHaveBeenCalled())
  })
})
