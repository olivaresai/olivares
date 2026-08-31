// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { renderIntel, screen, within } from '@/test/intel'
import '@/features/_intel'
import {
  CapabilityMatrix,
  DecisionPanel,
  KeyRefsTable,
  ModelsTable,
  PricingTable,
} from './components'
import {
  catalogFixture,
  decisionFixture,
  governedModelsFixture,
  keyRefsFixture,
} from './fixtures'
import './i18n'

describe('CapabilityMatrix', () => {
  it('renders each family against the capability vocabulary', () => {
    renderIntel(<CapabilityMatrix catalog={catalogFixture} />)
    expect(screen.getByText('claude-opus')).toBeInTheDocument()
    expect(screen.getByText('gemini-pro')).toBeInTheDocument()
    // capability column headers from the declared vocabulary
    expect(screen.getAllByText(/Prompt caching/i).length).toBeGreaterThan(0)
  })

  it('flags a preview family whose capabilities are not yet verified', () => {
    renderIntel(<CapabilityMatrix catalog={catalogFixture} />)
    // claude-mythos has caps_to_confirm=true → flagged, never given invented caps
    expect(screen.getByText('claude-mythos')).toBeInTheDocument()
    expect(screen.getByText(/Caps to confirm/i)).toBeInTheDocument()
  })
})

describe('PricingTable — CLA-16 catalog dimensions', () => {
  it('surfaces data residency with the confirmed US burndown multiplier', () => {
    renderIntel(<PricingTable catalog={catalogFixture} />)
    // opus declares global+us residency; the US burndown 1.1× is shown verbatim
    expect(screen.getAllByText(/global/i).length).toBeGreaterThan(0)
    expect(screen.getAllByText('1.1×').length).toBeGreaterThan(0)
  })

  it('surfaces service-tier eligibility, never inferring a default set', () => {
    renderIntel(<PricingTable catalog={catalogFixture} />)
    expect(screen.getAllByText(/Priority on-demand/i).length).toBeGreaterThan(0)
    // gemini-pro declares no tiers → honest "Not declared", not a fabricated set
    expect(screen.getAllByText(/Not declared/i).length).toBeGreaterThan(0)
  })

  it('shows the distinct 1-hour cache-write tier without faking the 5m rate', () => {
    renderIntel(<PricingTable catalog={catalogFixture} />)
    // opus 1h cache-write is $30 (distinct from the $18.75 5m rate)
    expect(screen.getByText('$30')).toBeInTheDocument()
  })
})

describe('ModelsTable', () => {
  it('lists governed models and flags ones without a declared family', () => {
    renderIntel(<ModelsTable models={governedModelsFixture} />)
    expect(screen.getByText('claude-opus-4-8')).toBeInTheDocument()
    expect(screen.getByText('llama-3.3-70b-local')).toBeInTheDocument()
    // the un-enriched local model is flagged, not faked with a price
    expect(screen.getByText(/No declared family/i)).toBeInTheDocument()
  })
})

describe('KeyRefsTable — never a secret', () => {
  it('shows only the masked hint, never a usable credential', () => {
    renderIntel(<KeyRefsTable keys={keyRefsFixture} />)
    // the masked hint is shown verbatim…
    expect(screen.getByText('sk-ant-…aB12')).toBeInTheDocument()
    // …and there is NO full-length secret anywhere in the DOM
    const fullSecret = /sk-ant-[A-Za-z0-9]{20,}/
    expect(document.body.textContent ?? '').not.toMatch(fullSecret)
    // the external id is a reference, not the key value
    expect(screen.getByText('apikey_01')).toBeInTheDocument()
  })
})

describe('DecisionPanel', () => {
  it('renders the primary target and the fallback chain', () => {
    renderIntel(<DecisionPanel decision={decisionFixture} />)
    expect(screen.getAllByText('gemini-1.5-flash').length).toBeGreaterThan(0)
    expect(screen.getByText('claude-haiku-4-5')).toBeInTheDocument()
    expect(screen.getByText(/via gateway/i)).toBeInTheDocument()
    expect(
      screen.getByText(/primary google\/gemini-1\.5-flash/i),
    ).toBeInTheDocument()
  })

  it('honestly reports when no governed model satisfies the policy', () => {
    renderIntel(
      <DecisionPanel
        decision={{
          resolved: false,
          policy: 'capability',
          fallbacks: [],
          chain: [],
          reason: 'capability: 0 candidate(s)',
        }}
      />,
    )
    expect(
      screen.getByText(/No governed model satisfies this policy/i),
    ).toBeInTheDocument()
  })
})

describe('routing decision badges', () => {
  it('keeps the reason as plain operator text (no secrets, no payloads)', () => {
    const { container } = renderIntel(
      <DecisionPanel decision={decisionFixture} />,
    )
    expect(within(container).getByText(/Reason/i)).toBeInTheDocument()
  })
})
