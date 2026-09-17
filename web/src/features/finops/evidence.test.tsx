// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import { BudgetCard, AlertsTable } from './components'
import { alertAmount, budgetAmount, formatEvidenceMoney } from './evidence'
import type { Alert, BudgetStatus } from './types'
import raw from './evidence-fixtures.json'
import './i18n'

const fixtures = raw as unknown as Record<
  string,
  { status: BudgetStatus; alerts: { items: Alert[] } }
>

describe('handler evidence in the console', () => {
  it('wide handler amount survives in the main budget amount', () => {
    const { container } = renderIntel(
      <BudgetCard status={fixtures.wide.status} />,
    )
    expect(
      container.querySelector('[data-amount-class="exact"]'),
    ).toHaveTextContent('$9,007,199,254.740993')
    expect(
      container.querySelector('[data-amount-class="exact"]'),
    ).not.toHaveTextContent('740992')
  })
  it.each(['exact', 'lower_bound', 'unknown', 'zero'])(
    '%s status uses the handler classification',
    (name) => {
      const status = fixtures[name].status
      const { container } = renderIntel(<BudgetCard status={status} />)
      const kind = name === 'zero' ? 'exact' : name
      expect(budgetAmount(status).class).toBe(kind)
      expect(
        container.querySelector(`[data-amount-class="${kind}"]`),
      ).not.toBeNull()
      if (name === 'lower_bound') {
        expect(screen.getByText('At least $12.00')).toBeInTheDocument()
        expect(screen.queryByRole('progressbar')).toBeNull()
        expect(screen.getByText(/Over limit/i)).toBeInTheDocument()
      }
      if (name === 'unknown')
        expect(screen.queryByRole('progressbar')).toBeNull()
      if (name === 'zero')
        expect(screen.getByRole('progressbar')).toHaveAttribute(
          'aria-valuenow',
          '0',
        )
    },
  )
  it.each([
    'exact',
    'lower_bound',
    'wide',
    'historical',
    'malformed',
    'future',
  ])('%s alert is the same real handler response', (name) => {
    const alerts = fixtures[name].alerts.items
    const { container } = renderIntel(<AlertsTable alerts={alerts} />)
    const kind = ['exact', 'wide'].includes(name)
      ? 'exact'
      : name === 'lower_bound'
        ? 'lower_bound'
        : name === 'historical'
          ? 'historical'
          : 'unknown'
    expect(alertAmount(alerts[0]).class).toBe(kind)
    expect(
      container.querySelector(`[data-amount-class="${kind}"]`),
    ).not.toBeNull()
    if (name === 'wide')
      expect(
        container.querySelector('[data-amount-class="exact"]'),
      ).toHaveTextContent('$9,007,199,254.740993')
    if (name === 'historical')
      expect(
        screen.getByText('Originally reported: $12.00'),
      ).toBeInTheDocument()
  })
  it('broken present status cannot borrow legacy zero, over or forecast risk', () => {
    const status = {
      ...fixtures.unknown.status,
      over: true,
      projected_pct: 999,
      spend_micro_usd: 0,
    }
    const { container } = renderIntel(<BudgetCard status={status} />)
    expect(
      container.querySelector('[data-amount-class="unknown"]'),
    ).not.toBeNull()
    expect(screen.queryByText(/Over limit|On track to exceed/i)).toBeNull()
    expect(screen.queryByRole('progressbar')).toBeNull()
    expect(screen.getByText(/Forecast \(not certified\)/)).toBeInTheDocument()
    expect(
      budgetAmount({
        ...status,
        amount: { ...fixtures.exact.status.amount!, effective_micro_usd: '01' },
      }).class,
    ).toBe('unknown')
  })
  it('present future, malformed and tenant-mismatched alert extensions never fall back', () => {
    const historical = fixtures.historical.alerts.items[0]
    for (const extension of [{ envelope: null }, { evidence_hash: '' }]) {
      expect(
        alertAmount({
          ...historical,
          amount_evidence: { ...historical.amount_evidence, ...extension },
        } as unknown as Alert).class,
      ).toBe('unknown')
    }
    const alert = fixtures.exact.alerts.items[0]
    for (const envelope of [
      { ...alert.amount_evidence.envelope!, schema_version: 2 },
      { ...alert.amount_evidence.envelope!, amount: undefined },
      { ...alert.amount_evidence.envelope!, components: {} },
    ]) {
      expect(
        alertAmount({
          ...alert,
          amount_evidence: { ...alert.amount_evidence, envelope },
        } as Alert).class,
      ).toBe('unknown')
    }
    expect(alertAmount(alert, 'another-tenant').class).toBe('unknown')
    expect(
      alertAmount({ ...alert, amount_evidence: undefined } as unknown as Alert)
        .class,
    ).toBe('unknown')
  })
})
describe('bounded lossless micro-USD formatter', () => {
  it.each(['en', 'es', 'de', 'fr', 'ja', 'ru', 'zh'])(
    'retains all micro digits in %s',
    (locale) => {
      const rendered = formatEvidenceMoney('9007199254740993', locale)!
      expect(rendered.replace(/\D/g, '')).toBe('9007199254740993')
      expect(formatEvidenceMoney('-1', locale)!.replace(/\D/g, '')).toBe(
        '0000001',
      )
    },
  )
  it.each([null, undefined, 0, '01', '-0', '1.2', '1e6', '9'.repeat(129)])(
    'refuses noncanonical/unbounded %s',
    (value) => expect(formatEvidenceMoney(value)).toBeNull(),
  )
})
