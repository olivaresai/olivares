// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Labelled consumer states for EntitlementMatrixCard. Synthetic API responses
// only — not claimed as an actual Enterprise engine. Retry is a GET re-read.
import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createTestQueryClient, renderIntel } from '@/test/intel'
import { ApiError } from '@/lib/api/errors'
import '@/features/_intel'
import './i18n'

const { api } = vi.hoisted(() => ({
  api: {
    getLicense: vi.fn(),
    getActivation: vi.fn(),
    installLicense: vi.fn(),
  },
}))

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { consoleKeys } from './api'
import { EntitlementMatrixCard } from './entitlement-matrix'

const communityLicense = {
  edition: 'community',
  hot_apply: true,
  status: 'none',
  source: 'none',
  managed_externally: false,
  max_users: 0,
  seat_limit: 0,
  seat_limited: false,
  active_users: 1,
}

const seam = new ApiError(
  501,
  'activation_unavailable',
  'Activation is not wired on this deployment.',
)

beforeEach(() => {
  vi.clearAllMocks()
  api.getLicense.mockResolvedValue(communityLicense)
})

describe('EntitlementMatrixCard — labelled consumer states', () => {
  it('labelled loading: spinner, not an empty catalogue', async () => {
    api.getLicense.mockReturnValue(new Promise(() => {}))
    api.getActivation.mockReturnValue(new Promise(() => {}))
    renderIntel(<EntitlementMatrixCard />)
    expect(
      await screen.findByText(/loading the add-on catalog/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/empty add-on catalog/i)).toBeNull()
  })

  it('labelled community 501: unavailable copy, known license facts, not empty', async () => {
    api.getActivation.mockRejectedValue(seam)
    renderIntel(<EntitlementMatrixCard />)
    expect(
      await screen.findByText(/unavailable in this build/i),
    ).toBeInTheDocument()
    expect(screen.getByText(/License status:/i)).toBeInTheDocument()
    expect(screen.getByText(/Edition: Community/i)).toBeInTheDocument()
    expect(screen.queryByText(/empty add-on catalog/i)).toBeNull()
    expect(screen.queryByText('addon_airs')).toBeNull()
    const help = document.querySelector(
      '[data-slot="entitlement-source-help"]',
    ) as HTMLDetailsElement | null
    expect(help).toBeInstanceOf(HTMLDetailsElement)
    expect(help?.open).toBe(false)
    expect(help?.textContent).toMatch(/depends on the installed build/i)
    expect(help?.textContent).not.toMatch(/not a loading failure/i)
    expect(help?.textContent).toMatch(
      /not enough information to determine that status/i,
    )
  })

  it('labelled community 501: technical detail names endpoint and status, not the body', async () => {
    api.getActivation.mockRejectedValue(seam)
    const user = userEvent.setup()
    renderIntel(<EntitlementMatrixCard />)
    await screen.findByText(/unavailable in this build/i)
    await user.click(screen.getByText(/technical detail/i))
    expect(
      screen.getByText(/GET \/v1\/console\/activation/i),
    ).toBeInTheDocument()
    expect(screen.getByText(/HTTP 501/i)).toBeInTheDocument()
    expect(screen.getByText(/activation_unavailable/i)).toBeInTheDocument()
    expect(
      screen.queryByText(/Activation is not wired on this deployment/i),
    ).toBeNull()
  })

  it('labelled error: failed read offers retry; retry only re-reads activation', async () => {
    api.getActivation.mockRejectedValue(new ApiError(500, 'internal', 'boom'))
    const user = userEvent.setup()
    renderIntel(<EntitlementMatrixCard />)
    expect(await screen.findByText(/could not be loaded/i)).toBeInTheDocument()
    expect(screen.queryByText(/empty add-on catalog/i)).toBeNull()
    expect(api.getActivation).toHaveBeenCalledTimes(1)
    const licenseCalls = api.getLicense.mock.calls.length
    await user.click(screen.getByRole('button', { name: /^retry$/i }))
    await waitFor(() => expect(api.getActivation).toHaveBeenCalledTimes(2))
    expect(api.getLicense).toHaveBeenCalledTimes(licenseCalls)
    expect(api.installLicense).not.toHaveBeenCalled()
  })

  it('labelled empty: successful addons=[] is known empty, not 501', async () => {
    api.getActivation.mockResolvedValue({
      edition: 'community',
      restart_required: true,
      addons: [],
      presets: [],
    })
    renderIntel(<EntitlementMatrixCard />)
    expect(await screen.findByText(/empty add-on catalog/i)).toBeInTheDocument()
    expect(screen.queryByText(/unavailable in this build/i)).toBeNull()
    expect(screen.queryByText(/HTTP 501/i)).toBeNull()
  })

  it('labelled catalog success + partial missing features: matrix, entitlement unknown', async () => {
    api.getActivation.mockResolvedValue({
      edition: 'community',
      restart_required: true,
      addons: [
        {
          key: 'addon_airs',
          title: 'AIRS',
          summary: 'synthetic',
          env: '',
          preset: 'starter',
          state: 'available',
        },
      ],
      presets: [],
    })
    renderIntel(<EntitlementMatrixCard />)
    expect(await screen.findByText('addon_airs')).toBeInTheDocument()
    expect(
      screen.getByText(/unknown until a verified license/i),
    ).toBeInTheDocument()
    expect(
      screen.queryAllByText('entitled').filter((n) => n.tagName !== 'TH'),
    ).toHaveLength(0)
  })

  it('labelled community 501 retry is a catalogue re-read only', async () => {
    api.getActivation.mockRejectedValue(seam)
    const user = userEvent.setup()
    renderIntel(<EntitlementMatrixCard />)
    await screen.findByText(/unavailable in this build/i)
    expect(api.getActivation).toHaveBeenCalledTimes(1)
    const licenseCalls = api.getLicense.mock.calls.length
    await user.click(screen.getByRole('button', { name: /^retry$/i }))
    await waitFor(() => expect(api.getActivation).toHaveBeenCalledTimes(2))
    expect(api.getLicense).toHaveBeenCalledTimes(licenseCalls)
    expect(api.installLicense).not.toHaveBeenCalled()
  })
})

describe('EntitlementMatrixCard — LQ-F1 license read freshness', () => {
  const validLicense = {
    ...communityLicense,
    status: 'valid',
    source: 'data_dir',
    features: ['addon_airs'],
  }
  const catalogueReady = {
    edition: 'community',
    restart_required: true,
    addons: [
      {
        key: 'addon_airs',
        title: 'AIRS',
        summary: 'synthetic',
        env: '',
        preset: 'starter',
        state: 'available',
      },
    ],
    presets: [],
  }
  const licenseFail = new ApiError(500, 'internal', 'synthetic licence failure')
  const activationFail = new ApiError(
    500,
    'internal',
    'synthetic activation failure',
  )
  const positives = () =>
    screen.queryAllByText('entitled').filter((n) => n.tagName !== 'TH')

  it('activation refetch failure removes previously rendered rows and labels the prior catalogue', async () => {
    api.getLicense.mockResolvedValue(validLicense)
    api.getActivation.mockResolvedValue(catalogueReady)
    const queryClient = createTestQueryClient()
    renderIntel(<EntitlementMatrixCard />, { queryClient })
    expect(await screen.findByText('addon_airs')).toBeInTheDocument()
    const licenseCalls = api.getLicense.mock.calls.length
    api.getActivation.mockRejectedValue(activationFail)
    await act(() =>
      queryClient.refetchQueries({ queryKey: consoleKeys.activation() }),
    )
    await waitFor(() =>
      expect(queryClient.getQueryState(consoleKeys.activation())?.status).toBe(
        'error',
      ),
    )
    expect(await screen.findByText(/could not be loaded/i)).toBeInTheDocument()
    expect(screen.queryByText('addon_airs')).toBeNull()
    expect(
      screen.getByText(
        /Previously loaded add-on information is no longer current/i,
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/Edition: Community · License status: Valid/i),
    ).toBeInTheDocument()
    expect(api.getLicense).toHaveBeenCalledTimes(licenseCalls)
    expect(api.installLicense).not.toHaveBeenCalled()
    queryClient.clear()
  })

  it('successful licence refresh to missing features removes the prior positive entitlement', async () => {
    api.getLicense.mockResolvedValue(validLicense)
    api.getActivation.mockResolvedValue(catalogueReady)
    const queryClient = createTestQueryClient()
    renderIntel(<EntitlementMatrixCard />, { queryClient })
    expect(await screen.findByText('entitled')).toBeInTheDocument()
    api.getLicense.mockResolvedValue({
      ...validLicense,
      features: undefined,
      status: 'none',
    })
    await act(() =>
      queryClient.refetchQueries({ queryKey: consoleKeys.license() }),
    )
    await waitFor(() =>
      expect(queryClient.getQueryState(consoleKeys.license())?.status).toBe(
        'success',
      ),
    )
    expect(
      await screen.findByText(/unknown until a verified license/i),
    ).toBeInTheDocument()
    expect(positives()).toHaveLength(0)
    expect(screen.getByText('addon_airs')).toBeInTheDocument()
    queryClient.clear()
  })

  it('license 500 after success with catalogue 200 does not keep an unqualified entitled cell', async () => {
    api.getLicense.mockResolvedValue(validLicense)
    api.getActivation.mockResolvedValue(catalogueReady)
    const queryClient = createTestQueryClient()
    renderIntel(<EntitlementMatrixCard />, { queryClient })
    expect(await screen.findByText('entitled')).toBeInTheDocument()
    expect(queryClient.getQueryState(consoleKeys.license())?.status).toBe(
      'success',
    )
    const activationCalls = api.getActivation.mock.calls.length
    api.getLicense.mockRejectedValue(licenseFail)
    await act(() =>
      queryClient.refetchQueries({ queryKey: consoleKeys.license() }),
    )
    await waitFor(() =>
      expect(queryClient.getQueryState(consoleKeys.license())?.status).toBe(
        'error',
      ),
    )
    expect(positives()).toHaveLength(0)
    expect(screen.getByText('addon_airs')).toBeInTheDocument()
    expect(screen.getByText(/could not be refreshed/i)).toBeInTheDocument()
    expect(screen.getByText(/Last retrieved/i)).toBeInTheDocument()
    expect(screen.queryByText(/unknown until a verified license/i)).toBeNull()
    expect(api.getActivation).toHaveBeenCalledTimes(activationCalls)
    expect(api.installLicense).not.toHaveBeenCalled()
    const help = document.querySelector(
      '[data-slot="entitlement-source-help"]',
    ) as HTMLDetailsElement | null
    expect(help?.textContent).not.toMatch(/not a loading failure/i)
    expect(help?.textContent).toMatch(
      /not enough information to determine that status/i,
    )
    queryClient.clear()
  })

  it('license 500 after success with catalogue 501 qualifies cached licence facts', async () => {
    api.getLicense.mockResolvedValue(validLicense)
    api.getActivation.mockRejectedValue(seam)
    const queryClient = createTestQueryClient()
    renderIntel(<EntitlementMatrixCard />, { queryClient })
    expect(
      await screen.findByText(/Edition: Community · License status: Valid/i),
    ).toBeInTheDocument()
    expect(queryClient.getQueryState(consoleKeys.license())?.status).toBe(
      'success',
    )
    api.getLicense.mockRejectedValue(licenseFail)
    await act(() =>
      queryClient.refetchQueries({ queryKey: consoleKeys.license() }),
    )
    await waitFor(() =>
      expect(queryClient.getQueryState(consoleKeys.license())?.status).toBe(
        'error',
      ),
    )
    expect(
      screen.queryByText(/Edition: Community · License status: Valid/i),
    ).toBeNull()
    expect(screen.getByText(/could not be refreshed/i)).toBeInTheDocument()
    expect(screen.getByText(/Last retrieved/i)).toBeInTheDocument()
    expect(screen.getByText(/unavailable in this build/i)).toBeInTheDocument()
    expect(screen.queryByText(/unknown until a verified license/i)).toBeNull()
    queryClient.clear()
  })

  it('failed licence with no prior success does not claim there is no license', async () => {
    api.getLicense.mockRejectedValue(licenseFail)
    api.getActivation.mockResolvedValue(catalogueReady)
    const queryClient = createTestQueryClient()
    renderIntel(<EntitlementMatrixCard />, { queryClient })
    await waitFor(() =>
      expect(queryClient.getQueryState(consoleKeys.license())?.status).toBe(
        'error',
      ),
    )
    expect(await screen.findByText('addon_airs')).toBeInTheDocument()
    expect(
      screen.getByText(/Current license information is unavailable/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/unknown until a verified license/i)).toBeNull()
    expect(screen.queryByText(/Last retrieved/i)).toBeNull()
    expect(positives()).toHaveLength(0)
    queryClient.clear()
  })

  it('both failed reads state both limitations and do not use one as the other', async () => {
    api.getLicense.mockResolvedValue(validLicense)
    api.getActivation.mockResolvedValue(catalogueReady)
    const queryClient = createTestQueryClient()
    renderIntel(<EntitlementMatrixCard />, { queryClient })
    expect(await screen.findByText('entitled')).toBeInTheDocument()
    api.getLicense.mockRejectedValue(licenseFail)
    api.getActivation.mockRejectedValue(activationFail)
    await act(async () => {
      await queryClient.refetchQueries({ queryKey: consoleKeys.license() })
      await queryClient.refetchQueries({ queryKey: consoleKeys.activation() })
    })
    await waitFor(() => {
      expect(queryClient.getQueryState(consoleKeys.license())?.status).toBe(
        'error',
      )
      expect(queryClient.getQueryState(consoleKeys.activation())?.status).toBe(
        'error',
      )
    })
    expect(screen.queryByText('addon_airs')).toBeNull()
    expect(screen.getByText(/could not be loaded/i)).toBeInTheDocument()
    expect(
      screen.getByText(
        /Previously loaded add-on information is no longer current/i,
      ),
    ).toBeInTheDocument()
    expect(screen.getByText(/could not be refreshed/i)).toBeInTheDocument()
    expect(screen.getByText(/Last retrieved/i)).toBeInTheDocument()
    expect(positives()).toHaveLength(0)
    expect(screen.getByRole('button', { name: /^retry$/i })).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /retry license status/i }),
    ).toBeInTheDocument()
    queryClient.clear()
  })

  it('successful recovery takes entitled features only from the latest success', async () => {
    api.getLicense.mockResolvedValue(validLicense)
    api.getActivation.mockResolvedValue(catalogueReady)
    const queryClient = createTestQueryClient()
    renderIntel(<EntitlementMatrixCard />, { queryClient })
    expect(await screen.findByText('entitled')).toBeInTheDocument()
    const activationCalls = api.getActivation.mock.calls.length
    api.getLicense.mockRejectedValue(licenseFail)
    await act(() =>
      queryClient.refetchQueries({ queryKey: consoleKeys.license() }),
    )
    await waitFor(() =>
      expect(queryClient.getQueryState(consoleKeys.license())?.status).toBe(
        'error',
      ),
    )
    expect(positives()).toHaveLength(0)

    const licenseCallsBeforeRetry = api.getLicense.mock.calls.length
    api.getLicense.mockResolvedValue({
      ...validLicense,
      features: ['addon_reg'],
    })
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: /retry license status/i }),
    )
    await waitFor(() =>
      expect(queryClient.getQueryState(consoleKeys.license())?.status).toBe(
        'success',
      ),
    )
    expect(api.getLicense.mock.calls.length).toBeGreaterThan(
      licenseCallsBeforeRetry,
    )
    expect(api.getActivation).toHaveBeenCalledTimes(activationCalls)
    expect(api.installLicense).not.toHaveBeenCalled()
    expect(positives()).toHaveLength(0)
    expect(screen.getByText('addon_airs')).toBeInTheDocument()
    expect(screen.queryByText(/could not be refreshed/i)).toBeNull()
    expect(
      screen.queryByRole('button', { name: /retry license status/i }),
    ).toBeNull()

    api.getLicense.mockResolvedValue(validLicense)
    await act(() =>
      queryClient.refetchQueries({ queryKey: consoleKeys.license() }),
    )
    await waitFor(() =>
      expect(queryClient.getQueryState(consoleKeys.license())?.status).toBe(
        'success',
      ),
    )
    expect(await screen.findByText('entitled')).toBeInTheDocument()
    queryClient.clear()
  })
})
