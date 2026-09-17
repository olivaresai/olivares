// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The presentational half of the rate catalog, fed by the real reader over payloads shaped like
// modules/finops/ratecatalog.go `modelRateDTO`. The query, its truncation badge and AsyncSection's
// transport states belong to TarifasCard and are exercised through FinOpsView in
// costcentres-view.test.tsx.
import { afterEach, describe, expect, it, vi } from 'vitest'
import i18n from 'i18next'
import { act, renderIntel, screen, userEvent, within } from '@/test/intel'
import './i18n'
import { ModelRateCatalog } from './model-rate-catalog'
import { readModelRateCatalog } from './model-rate-validation'

/** One entry exactly as the engine serializes `modelRateDTO` (canonical store timestamps). */
const SERVER_RATE = {
  id: 'mr-1',
  provider: 'anthropic',
  model: 'claude-opus-5',
  input_rate_micro_usd: 15_000_000,
  output_rate_micro_usd: 75_000_000,
  cache_read_rate_micro_usd: 1_500_000,
  cache_creation_rate_micro_usd: 18_750_000,
  effective_from: '2026-01-01T00:00:00.000000000Z',
  notes: 'list price',
  created_at: '2026-01-02T08:00:00.000000000Z',
  updated_at: '2026-01-02T08:00:00.000000000Z',
}

const page = (items: unknown[], hasMore = false) => ({
  items,
  has_more: hasMore,
})
const rate = (over: Record<string, unknown>) => ({ ...SERVER_RATE, ...over })
const without = (key: string, over: Record<string, unknown> = {}) => {
  const r: Record<string, unknown> = rate(over)
  delete r[key]
  return r
}

function show(payload: unknown, reloading = false) {
  const onReload = vi.fn()
  renderIntel(
    <ModelRateCatalog
      read={readModelRateCatalog(payload)}
      reloading={reloading}
      onReload={onReload}
    />,
  )
  return onReload
}

const entries = () =>
  Array.from(
    document.querySelectorAll<HTMLElement>('[data-slot="model-rate-entry"]'),
  )

/** The `<dd>` that a visible `<dt>` label describes inside one entry. */
function fact(entry: HTMLElement, label: string): HTMLElement {
  const term = within(entry).getByText(label, { selector: 'dt' })
  const value = term.nextElementSibling
  if (!(value instanceof HTMLElement)) throw new Error(`no value for ${label}`)
  return value
}

afterEach(async () => {
  await act(async () => {
    await i18n.changeLanguage('en')
  })
})

describe('model rate catalog — identity and values as served', () => {
  it('shows the model the engine sends in `model`, its provider and the exact integer rates', () => {
    show(page([SERVER_RATE]))
    const [entry] = entries()
    expect(entries()).toHaveLength(1)
    expect(fact(entry, 'Model')).toHaveTextContent(/^claude-opus-5$/)
    expect(fact(entry, 'Provider')).toHaveTextContent(/^anthropic$/)
    expect(fact(entry, 'Input rate')).toHaveTextContent(/^15,000,000$/)
    expect(fact(entry, 'Output rate')).toHaveTextContent(/^75,000,000$/)
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('states the monetary unit and offers no action on a readable page', () => {
    show(page([SERVER_RATE]))
    expect(
      screen.getByText(
        'Input and output rates are in micro-USD per 1M tokens (integer).',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('keeps the order the server sent', () => {
    show(
      page([
        rate({
          id: 'mr-2',
          model: 'claude-sonnet-5',
          effective_from: '2026-07-01T00:00:00.000000000Z',
        }),
        SERVER_RATE,
      ]),
    )
    expect(entries().map((e) => fact(e, 'Model').textContent)).toEqual([
      'claude-sonnet-5',
      'claude-opus-5',
    ])
  })

  it('shows an explicit zero rate as 0', () => {
    show(page([rate({ output_rate_micro_usd: 0 })]))
    const [entry] = entries()
    expect(fact(entry, 'Output rate')).toHaveTextContent(/^0$/)
  })

  it('shows the largest exact integer digit for digit', () => {
    show(page([rate({ input_rate_micro_usd: Number.MAX_SAFE_INTEGER })]))
    const [entry] = entries()
    expect(fact(entry, 'Input rate')).toHaveTextContent(
      /^9,007,199,254,740,991$/,
    )
  })

  it('reads in the active locale: labels, digit grouping and the no-end fact', async () => {
    await act(async () => {
      await i18n.changeLanguage('es')
    })
    show(page([SERVER_RATE]))
    const [entry] = entries()
    expect(fact(entry, 'Modelo')).toHaveTextContent('claude-opus-5')
    expect(fact(entry, 'Tarifa de entrada')).toHaveTextContent(/^15\.000\.000$/)
    expect(fact(entry, 'Fin de vigencia')).toHaveTextContent('Sin fecha de fin')
  })
})

describe('model rate catalog — a refused payload is unavailable, never zero or empty', () => {
  it('an absent rate makes the catalog unavailable instead of showing 0', () => {
    show(page([without('input_rate_micro_usd')]))
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent('Rate catalogue unavailable')
    expect(alert).toHaveTextContent('Entry 1 is missing input_rate_micro_usd.')
    expect(screen.queryByText('0')).toBeNull()
    expect(screen.queryByText('claude-opus-5')).toBeNull()
  })

  it('the legacy `model_ref` shape is unavailable, not a rate without a model', () => {
    show(page([without('model', { model_ref: 'claude-opus-5' })]))
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent('Entry 1 is missing model.')
    expect(screen.queryByText('anthropic')).toBeNull()
    expect(entries()).toHaveLength(0)
  })

  it.each([
    [
      'a rate beyond 2^53 - 1',
      rate({ input_rate_micro_usd: 2 ** 53 }),
      'Entry 1 has input_rate_micro_usd too large to show exactly.',
    ],
    [
      'a negative rate',
      rate({ output_rate_micro_usd: -5 }),
      'Entry 1 has a negative output_rate_micro_usd.',
    ],
    [
      'a fractional rate',
      rate({ input_rate_micro_usd: 0.5 }),
      'Entry 1 has input_rate_micro_usd that is not a whole number.',
    ],
    [
      'a rate sent as text',
      rate({ input_rate_micro_usd: '15000000' }),
      'Entry 1 has input_rate_micro_usd of the wrong type.',
    ],
    [
      'a blank provider',
      rate({ provider: ' ' }),
      'Entry 1 has an empty provider.',
    ],
    ['no id', without('id'), 'Entry 1 is missing id.'],
    [
      'no effective_from',
      without('effective_from'),
      'Entry 1 is missing effective_from.',
    ],
    [
      'a prose effective_from',
      rate({ effective_from: 'January 1, 2026' }),
      'Entry 1 has effective_from that is not an RFC 3339 timestamp.',
    ],
    [
      'an impossible effective_until',
      rate({ effective_until: '2026-06-31T00:00:00Z' }),
      'Entry 1 has effective_until that is not an RFC 3339 timestamp.',
    ],
  ])('%s is unavailable, with a localized reason', (_what, entry, reason) => {
    show(page([entry]))
    expect(screen.getByRole('alert')).toHaveTextContent(reason)
    expect(entries()).toHaveLength(0)
    expect(screen.queryByText('No rates in the catalogue')).toBeNull()
    expect(screen.queryByText(/9,007,199,254,740,992/)).toBeNull()
  })

  it.each([
    ['a page without items', { has_more: false }],
    ['items: null', { items: null }],
    ['an array payload', []],
    ['a null payload', null],
    ['has_more as text', { items: [], has_more: 'false' }],
    ['has_more true with no rows', { items: [], has_more: true }],
  ])('%s is unavailable, not the empty catalog', (_what, payload) => {
    show(payload)
    expect(screen.getByRole('alert')).toHaveTextContent(
      'The response does not contain a readable list of rate entries.',
    )
    expect(screen.queryByText('No rates in the catalogue')).toBeNull()
  })

  it('keeps only a safe description: no payload value and no other row is shown', () => {
    show(
      page([
        rate({ model: 'visible-model-a' }),
        rate({
          id: 'mr-2',
          provider: 'secret-provider-b',
          model: 'secret-model-b',
          input_rate_micro_usd: '123456789',
        }),
      ]),
    )
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Entry 2 has input_rate_micro_usd of the wrong type.',
    )
    const text = document.body.textContent ?? ''
    for (const value of [
      'visible-model-a',
      'secret-provider-b',
      'secret-model-b',
      '123456789',
      'anthropic',
    ])
      expect(text).not.toContain(value)
    const state = document.querySelector(
      '[data-slot="model-rate-catalog-invalid"]',
    )
    expect(state).toHaveAttribute('data-reason', 'type')
    expect(state).toHaveAttribute('data-entry', '2')
    expect(state).toHaveAttribute('data-field', 'input_rate_micro_usd')
  })

  it('an actual empty items array is the empty catalog', () => {
    show(page([]))
    expect(screen.getByText('No rates in the catalogue')).toBeInTheDocument()
    expect(
      screen.getByText('No list prices are recorded for this organization.'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
  })
})

describe('model rate catalog — validity dates are facts, not a current claim', () => {
  it('an entry without an end date says so and is not called current', () => {
    show(page([SERVER_RATE]))
    const [entry] = entries()
    expect(fact(entry, 'Effective until')).toHaveTextContent('No end date')
    expect(fact(entry, 'Effective from').querySelector('time')).toHaveAttribute(
      'datetime',
      '2026-01-01T00:00:00.000Z',
    )
    expect(screen.queryByText(/\bcurrent\b/i)).toBeNull()
  })

  it('a future start without an end is shown by its dates only', () => {
    show(page([rate({ effective_from: '2099-01-01T00:00:00.000000000Z' })]))
    const [entry] = entries()
    expect(fact(entry, 'Effective from').querySelector('time')).toHaveAttribute(
      'datetime',
      '2099-01-01T00:00:00.000Z',
    )
    expect(fact(entry, 'Effective until')).toHaveTextContent('No end date')
    expect(entry.textContent).not.toMatch(/current|upcoming|scheduled|active/i)
  })

  it('an ended entry shows its end instant, formatted, with the server text kept', () => {
    show(page([rate({ effective_until: '2026-06-30T00:00:00.000000000Z' })]))
    const [entry] = entries()
    const until = fact(entry, 'Effective until').querySelector('time')
    expect(until).toHaveAttribute('datetime', '2026-06-30T00:00:00.000Z')
    expect(until).toHaveAttribute('title', '2026-06-30T00:00:00.000000000Z')
    expect(until?.textContent).toMatch(/2026/)
    expect(until?.textContent).not.toContain('T00:00')
    expect(entry).not.toHaveTextContent('No end date')
  })

  it('an offset timestamp is shown at its instant', () => {
    show(page([rate({ effective_from: '2026-09-11T14:30:00+02:00' })]))
    const [entry] = entries()
    expect(fact(entry, 'Effective from').querySelector('time')).toHaveAttribute(
      'datetime',
      '2026-09-11T12:30:00.000Z',
    )
  })
})

describe('model rate catalog — the only action is a read', () => {
  it('offers exactly one keyboard-reachable reload, which only asks for the read again', async () => {
    const user = userEvent.setup()
    const onReload = show(page([without('model', { model_ref: 'x' })]))
    const buttons = within(screen.getByRole('alert')).getAllByRole('button')
    expect(buttons).toHaveLength(1)
    expect(screen.getAllByRole('button')).toHaveLength(1)
    expect(buttons[0]).toHaveAccessibleName('Retry')
    await user.tab()
    expect(buttons[0]).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(onReload).toHaveBeenCalledTimes(1)
  })

  it('while the reload is in flight it says so and keeps the explanation', () => {
    show(page([without('model')]), true)
    expect(screen.getByRole('status')).toHaveTextContent('Loading…')
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Entry 1 is missing model.',
    )
  })
})

describe('model rate catalog — readable at narrow widths and by keyboard', () => {
  it('lets a long exact amount wrap only after a group separator, never inside a group', () => {
    show(page([rate({ input_rate_micro_usd: Number.MAX_SAFE_INTEGER })]))
    const value = fact(entries()[0], 'Input rate')
    const breaks = [...value.querySelectorAll('wbr')]
    expect(breaks).toHaveLength(5)
    for (const wbr of breaks) expect(wbr.previousSibling?.textContent).toBe(',')
    expect(value).toHaveTextContent(/^9,007,199,254,740,991$/)
  })

  const unreadable = () => readModelRateCatalog(page([without('model')]))
  const readable = () => readModelRateCatalog(page([SERVER_RATE]))
  const result = () =>
    document.querySelector('[data-slot="model-rate-catalog-result"]')

  it('a readable page returned by the reload the operator asked for takes focus', async () => {
    const user = userEvent.setup()
    const onReload = vi.fn()
    const { rerender } = renderIntel(
      <ModelRateCatalog
        read={unreadable()}
        reloading={false}
        onReload={onReload}
      />,
    )
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    rerender(
      <ModelRateCatalog read={unreadable()} reloading onReload={onReload} />,
    )
    rerender(
      <ModelRateCatalog
        read={readable()}
        reloading={false}
        onReload={onReload}
      />,
    )
    expect(result()).not.toBeNull()
    expect(document.activeElement).toBe(result())
    expect(onReload).toHaveBeenCalledTimes(1)
  })

  it('a reload that is still unreadable keeps focus on Retry, and a later unrequested page does not take it', async () => {
    const user = userEvent.setup()
    const onReload = vi.fn()
    const { rerender } = renderIntel(
      <ModelRateCatalog
        read={unreadable()}
        reloading={false}
        onReload={onReload}
      />,
    )
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    rerender(
      <ModelRateCatalog read={unreadable()} reloading onReload={onReload} />,
    )
    rerender(
      <ModelRateCatalog
        read={unreadable()}
        reloading={false}
        onReload={onReload}
      />,
    )
    expect(screen.getByRole('button', { name: 'Retry' })).toHaveFocus()
    rerender(
      <ModelRateCatalog
        read={readable()}
        reloading={false}
        onReload={onReload}
      />,
    )
    expect(result()).not.toBeNull()
    expect(document.activeElement).not.toBe(result())
  })
})
