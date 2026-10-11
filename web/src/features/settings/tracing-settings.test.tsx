// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { PANEL_EXTENSIONS } from '@/features/extensions'
import { afterEach, expect, it, vi } from 'vitest'
import userEvent from '@testing-library/user-event'
import { renderIntel, screen, waitFor } from '@/test/intel'
import {
  TracingSettings,
  tracingApi,
  type TracingStatus,
} from './tracing-settings'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    principal: { actor: 'admin' },
    isSuperadmin: true,
    can: () => true,
  }),
}))
afterEach(() => vi.restoreAllMocks())

it('saves the collector and enabled choice, then displays the engine effective state and overrides', async () => {
  const settings: TracingStatus['settings'] = {
    enabled: false,
    endpoint: '',
    protocol: 'grpc',
    insecure: false,
    sample_ratio: 1,
    service_name: 'olivares',
    genai_compat: false,
  }
  vi.spyOn(tracingApi, 'get').mockResolvedValue({
    settings,
    effective: settings,
    overrides: [],
  })
  const selected = {
    ...settings,
    enabled: true,
    endpoint: 'https://collector.example:4317',
  }
  const put = vi.spyOn(tracingApi, 'save').mockResolvedValue({
    settings: selected,
    effective: { ...selected, enabled: false },
    overrides: ['OLIVARES_OTEL_ENABLED'],
  })
  renderIntel(<TracingSettings />)
  await userEvent.type(
    await screen.findByLabelText('Collector endpoint'),
    selected.endpoint,
  )
  await userEvent.click(screen.getByRole('switch', { name: 'Enable tracing' }))
  await userEvent.click(screen.getByRole('button', { name: 'Apply' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      selected,
      expect.objectContaining({
        signal: expect.any(AbortSignal),
        dispatchGuard: expect.any(Function),
      }),
    ),
  )
  expect(await screen.findByText(/OLIVARES_OTEL_ENABLED/)).toBeInTheDocument()
  expect(screen.getByRole('switch', { name: 'Enable tracing' })).toBeChecked()
  expect(screen.getByText('Tracing is off.')).toBeInTheDocument()
  expect(
    screen.queryByText(
      'SIEM delivery and external telemetry export require Business. Stored settings are preserved; Community does not send them.',
    ) !== null,
  ).toBe(!PANEL_EXTENSIONS.operationsExportAvailable)
})
