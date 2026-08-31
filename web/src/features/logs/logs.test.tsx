// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act, renderIntel, screen, userEvent, waitFor } from '@/test/intel'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { UseLiveStreamOptions } from '@/features/shared/sse'
import type { LogEntry } from './types'

const api = vi.hoisted(() => ({
  buffer: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, logsApi: api }
})

const liveStream = vi.hoisted(() =>
  vi.fn((_options: UseLiveStreamOptions<LogEntry>) => ({
    status: 'open' as const,
  })),
)
vi.mock('@/features/shared/sse', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/shared/sse')>()
  return { ...actual, useLiveStream: liveStream }
})

import { LogsView } from './logs-view'

const debugEntry: LogEntry = {
  timestamp: '2026-07-24T10:00:00Z',
  level: 'DEBUG',
  module: 'database',
  message: 'debug database probe',
}
const infoEntry: LogEntry = {
  timestamp: '2026-07-24T10:01:00Z',
  level: 'INFO',
  module: 'Core',
  message: 'info core ready',
}
const warnEntry: LogEntry = {
  timestamp: '2026-07-24T10:02:00Z',
  level: 'WARN',
  module: 'worker',
  message: 'warn worker retry',
}
const errorEntry: LogEntry = {
  timestamp: '2026-07-24T10:03:00Z',
  level: 'ERROR',
  module: 'CoreAPI',
  message: 'error core failed',
}
const allEntries = [debugEntry, infoEntry, warnEntry, errorEntry]

function bufferResponse(items: LogEntry[] = allEntries, captureLevel = 'info') {
  return {
    items,
    total: items.length,
    capture_level: captureLevel,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  liveStream.mockReturnValue({ status: 'open' })
  api.buffer.mockResolvedValue(bufferResponse())
})

describe('LogsView', () => {
  it('sends the selected level CSV and raw module to both buffer and stream', async () => {
    renderIntel(<LogsView />)
    await waitFor(() =>
      expect(api.buffer).toHaveBeenCalledWith({ limit: 1000 }),
    )
    expect(liveStream.mock.calls.at(-1)?.[0].query).toEqual({})

    await userEvent.click(
      screen.getByRole('button', { name: /Filter DEBUG logs/i }),
    )
    await userEvent.click(
      screen.getByRole('button', { name: /Filter ERROR logs/i }),
    )
    await userEvent.type(screen.getByLabelText(/Filter by module/i), 'Core')

    await waitFor(() =>
      expect(api.buffer).toHaveBeenLastCalledWith({
        levels: 'debug,error',
        module: 'Core',
        limit: 1000,
      }),
    )
    expect(liveStream.mock.calls.at(-1)?.[0].query).toEqual({
      levels: 'debug,error',
      module: 'Core',
    })
  })

  it('shows the DEBUG capture note only when DEBUG is selected and not captured', async () => {
    const first = renderIntel(<LogsView />)
    await userEvent.click(
      screen.getByRole('button', { name: /Filter DEBUG logs/i }),
    )
    expect(
      await screen.findByText(/set OLIVARES_LOG_LEVEL=debug/i),
    ).toBeInTheDocument()

    await userEvent.click(
      screen.getByRole('button', { name: /Filter DEBUG logs/i }),
    )
    await waitFor(() =>
      expect(
        screen.queryByText(/set OLIVARES_LOG_LEVEL=debug/i),
      ).not.toBeInTheDocument(),
    )

    first.unmount()
    vi.clearAllMocks()
    liveStream.mockReturnValue({ status: 'open' })
    api.buffer.mockResolvedValue(bufferResponse(allEntries, 'debug'))
    renderIntel(<LogsView />)
    await userEvent.click(
      screen.getByRole('button', { name: /Filter DEBUG logs/i }),
    )
    await waitFor(() =>
      expect(api.buffer).toHaveBeenLastCalledWith({
        levels: 'debug',
        limit: 1000,
      }),
    )
    expect(
      screen.queryByText(/set OLIVARES_LOG_LEVEL=debug/i),
    ).not.toBeInTheDocument()
  })

  it('re-seeds from the buffer when the selected set content changes', async () => {
    api.buffer.mockImplementation(({ levels }: { levels?: string }) =>
      Promise.resolve(
        levels === 'debug'
          ? bufferResponse([debugEntry])
          : levels === 'debug,info'
            ? bufferResponse([debugEntry, infoEntry])
            : levels === 'info'
              ? bufferResponse([infoEntry])
              : bufferResponse([]),
      ),
    )
    renderIntel(<LogsView />)

    await userEvent.click(
      screen.getByRole('button', { name: /Filter DEBUG logs/i }),
    )
    expect(await screen.findByText(debugEntry.message)).toBeInTheDocument()

    await userEvent.click(
      screen.getByRole('button', { name: /Filter INFO logs/i }),
    )
    await userEvent.click(
      screen.getByRole('button', { name: /Filter DEBUG logs/i }),
    )

    await waitFor(() =>
      expect(api.buffer).toHaveBeenLastCalledWith({
        levels: 'info',
        limit: 1000,
      }),
    )
    expect(await screen.findByText(infoEntry.message)).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.queryByText(debugEntry.message)).not.toBeInTheDocument(),
    )

    // Return to the already-cached DEBUG set. The filter-content effect must
    // clear and re-seed even though TanStack has no new network result to emit.
    await userEvent.click(
      screen.getByRole('button', { name: /Filter DEBUG logs/i }),
    )
    await userEvent.click(
      screen.getByRole('button', { name: /Filter INFO logs/i }),
    )
    expect(await screen.findByText(debugEntry.message)).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.queryByText(infoEntry.message)).not.toBeInTheDocument(),
    )
  })

  it('keeps level, case-insensitive module substring, and message filters client-side', async () => {
    // Deliberately return unfiltered rows: the view must keep its consistency
    // filter even when a buffer or live frame does not match the active query.
    api.buffer.mockResolvedValue(bufferResponse())
    renderIntel(<LogsView />)
    expect(await screen.findByText(infoEntry.message)).toBeInTheDocument()

    await userEvent.click(
      screen.getByRole('button', { name: /Filter ERROR logs/i }),
    )
    await userEvent.type(screen.getByLabelText(/Filter by module/i), 'corea')

    expect(await screen.findByText(errorEntry.message)).toBeInTheDocument()
    expect(screen.queryByText(infoEntry.message)).not.toBeInTheDocument()
    expect(screen.queryByText(debugEntry.message)).not.toBeInTheDocument()

    const latest = liveStream.mock.calls.at(-1)?.[0]
    if (!latest) throw new Error('live stream was not mounted')
    act(() => latest.onSnapshot(infoEntry, 'log'))
    await waitFor(() =>
      expect(screen.queryByText(infoEntry.message)).not.toBeInTheDocument(),
    )

    await userEvent.type(
      screen.getByLabelText(/Search in log messages/i),
      'missing',
    )
    expect(
      await screen.findByText(/No log entries to display/i),
    ).toBeInTheDocument()
  })

  //the structured attributes ARE the diagnosis. The engine redacts every
  // attribute at the log broker before publishing it, and this row painted only
  // timestamp/level/module/message, so the `err` an engine module logs crossed
  // the wire and was rendered nowhere. A viewer that shows the failure without
  // showing what failed is the same defect as a silent failure.
  it('renders the structured attributes an engine module logged', async () => {
    const entryWithAttrs: LogEntry = {
      timestamp: '2026-08-09T10:04:00Z',
      level: 'ERROR',
      module: 'governance',
      message: 'governance: roster snapshot failed',
      attrs: {
        err: 'parse "postgres://[REDACTED:url-userinfo]@db.internal.corp:5432/alma": FATAL 28P01',
        attempt: 3,
      },
    }
    api.buffer.mockResolvedValue(bufferResponse([entryWithAttrs]))
    renderIntel(<LogsView />)

    // WHAT failed, WHERE, and WHY — the three things an operator acts on.
    expect(
      await screen.findByText(/db\.internal\.corp:5432/),
    ).toBeInTheDocument()
    expect(screen.getByText(/28P01/)).toBeInTheDocument()
    // And the marker, so a scrubbed line is distinguishable from a clean one.
    expect(screen.getByText(/\[REDACTED:url-userinfo\]/)).toBeInTheDocument()
    // Non-string attribute values are shown too, not dropped. Asserting the KEY
    // alone would stay green if the value were omitted, which is the same
    // false-green this whole surface exists to avoid — so assert the pair.
    const attrs = screen.getByLabelText(/Structured log attributes/i)
    expect(attrs).toHaveTextContent('attempt=3')
  })
})
