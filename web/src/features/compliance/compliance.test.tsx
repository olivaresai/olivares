// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it, vi } from 'vitest'
import {
  DEFAULT_AUTH,
  renderIntel,
  screen,
  userEvent,
  within,
} from '@/test/intel'
import '@/features/_intel' // register the shared `intel` namespace for badges/notices
import { DisclaimerNote } from '@/features/_intel'
import {
  ControlList,
  EvidenceCard,
  FrameworkDisclaimerBanner,
  FrameworkRollupList,
  GapList,
  OscalFindingsPreview,
  ResidencyCard,
  RiskTable,
} from './components'
import { frameworkGroup, isInDevelopmentFramework } from './types'
import {
  crosswalkStatusFixture,
  evidenceFixture,
  gapsFixture,
  oscalExportFixture,
  residencyFixture,
  riskFixture,
  statusFixture,
  summaryFixture,
} from './fixtures'
import './i18n'

describe('ControlList — satisfied is NOT by_design (honesty invariant)', () => {
  it('renders distinct copy AND color for satisfied vs by_design', () => {
    renderIntel(<ControlList controls={statusFixture.assessment.controls} />)
    // Distinct COPY: never "compliant"/"certified"; by_design reads "Design-ready".
    const satisfied = screen.getByText('Satisfied')
    const byDesign = screen.getByText('Design-ready')
    expect(satisfied).toBeInTheDocument()
    expect(byDesign).toBeInTheDocument()
    // Distinct COLOR: satisfied → success token, by_design → info token.
    expect(satisfied.className).toMatch(/text-success/)
    expect(byDesign.className).toMatch(/text-info/)
    expect(satisfied.className).not.toEqual(byDesign.className)
    // The product NEVER says "compliant"/"certified".
    expect(document.body.textContent ?? '').not.toMatch(/compliant|certified/i)
  })

  it('always renders a control coverage note when present', () => {
    renderIntel(<ControlList controls={statusFixture.assessment.controls} />)
    expect(
      screen.getByText(/no per-tenant operational telemetry attests this yet/i),
    ).toBeInTheDocument()
  })

  it('keeps unmapped as an honest value, not a load error', () => {
    renderIntel(<ControlList controls={statusFixture.assessment.controls} />)
    expect(screen.getByText('Unmapped')).toBeInTheDocument()
  })
})

describe('DisclaimerNote — always rendered', () => {
  it('renders the disclaimer string verbatim', () => {
    renderIntel(<DisclaimerNote text={statusFixture.disclaimer} />)
    expect(
      screen.getByText(
        /NOT a certification or a statement of legal compliance/i,
      ),
    ).toBeInTheDocument()
  })
})

describe('EvidenceCard — shows its integrity badge', () => {
  it('renders a verified integrity badge for a sealed package', () => {
    renderIntel(<EvidenceCard pkg={evidenceFixture[0]} />)
    expect(screen.getByText(/Integrity verified/i)).toBeInTheDocument()
    // It anchors to the ledger by fingerprint, never as a "certificate".
    expect(screen.getByText(/seq 12/i)).toBeInTheDocument()
    expect(document.body.textContent ?? '').not.toMatch(
      /certificate|certified/i,
    )
  })

  it('renders a failed integrity badge with no faked seal', () => {
    renderIntel(<EvidenceCard pkg={evidenceFixture[1]} />)
    expect(screen.getByText(/Integrity failed/i)).toBeInTheDocument()
  })
})

describe('FrameworkRollupList', () => {
  it('renders every framework with its status mix', () => {
    renderIntel(<FrameworkRollupList frameworks={summaryFixture.frameworks} />)
    expect(screen.getByText('EU AI Act')).toBeInTheDocument()
    expect(screen.getByText('NIST AI RMF')).toBeInTheDocument()
    expect(screen.getByText('GDPR')).toBeInTheDocument()
  })
})

describe('GapList — what to fix', () => {
  it('lists missing capabilities for an open gap', () => {
    renderIntel(<GapList gaps={gapsFixture.gaps} />)
    expect(screen.getByText('Data and data governance')).toBeInTheDocument()
    // the missing capability is shown as the actionable thing to turn on
    expect(screen.getByText(/Data residency/i)).toBeInTheDocument()
  })
})

describe('RiskTable — register & honesty', () => {
  it('renders tiers and NIST functions; unacceptable only appears post-review', () => {
    renderIntel(<RiskTable rows={riskFixture} />)
    const table = screen.getByRole('grid')
    // The overridden row carries the unacceptable tier (a human decision).
    expect(within(table).getByText('Unacceptable')).toBeInTheDocument()
    expect(within(table).getAllByText('GOVERN').length).toBeGreaterThan(0)
    // The suggested tier for that row is "high" — the heuristic never suggests unacceptable.
    const overridden = riskFixture.find((r) => r.state === 'overridden')
    expect(overridden?.suggested_tier).not.toBe('unacceptable')
  })
})

describe('ResidencyCard', () => {
  it('flags an observed violation in danger', () => {
    renderIntel(<ResidencyCard region={residencyFixture[1]} />)
    expect(screen.getByText(/2 violations observed/i)).toBeInTheDocument()
  })

  it('marks a self-hosted region as staying in perimeter', () => {
    renderIntel(<ResidencyCard region={residencyFixture[0]} />)
    expect(screen.getByText(/Data stays in perimeter/i)).toBeInTheDocument()
    expect(screen.getByText(/No violations observed/i)).toBeInTheDocument()
  })
})

// --- framework grouping (regulatory vs design-toward crosswalk) ---------

describe('framework grouping — pure classifier', () => {
  it('splits regulatory frameworks from design-toward crosswalks', () => {
    expect(frameworkGroup('eu_ai_act')).toBe('regulatory')
    expect(frameworkGroup('gdpr')).toBe('regulatory')
    expect(frameworkGroup('csa_maestro')).toBe('crosswalk')
    expect(frameworkGroup('owasp_agentic_tm')).toBe('crosswalk')
    expect(frameworkGroup('nist_cosais')).toBe('crosswalk')
  })
  it('flags only nist_cosais as in development', () => {
    expect(isInDevelopmentFramework('nist_cosais')).toBe(true)
    expect(isInDevelopmentFramework('eu_ai_act')).toBe(false)
  })
})

describe('FrameworkRollupList — groups + crosswalk caveat', () => {
  it('renders the regulatory and crosswalk group headers with a no-conformance caveat', () => {
    renderIntel(<FrameworkRollupList frameworks={summaryFixture.frameworks} />)
    expect(
      screen.getByText(/Regulatory frameworks & standards/i),
    ).toBeInTheDocument()
    expect(screen.getByText(/Design-toward crosswalks/i)).toBeInTheDocument()
    // The crosswalk group ALWAYS carries the "not conformance standards" caveat.
    expect(screen.getByText(/NOT conformance standards/i)).toBeInTheDocument()
    // The in-development framework is tagged on its card.
    expect(screen.getByText('In development')).toBeInTheDocument()
    // Each design-toward crosswalk is tagged per-card (not only on the group header).
    expect(screen.getAllByText('Design-toward').length).toBeGreaterThan(0)
  })
})

// --- prominent design-toward / no-conformance-claim banner --------------

describe('FrameworkDisclaimerBanner — prominent honesty', () => {
  it('renders the no-conformance disclaimer for an in-development crosswalk', () => {
    renderIntel(
      <FrameworkDisclaimerBanner
        framework={crosswalkStatusFixture.assessment.framework}
        disclaimer={crosswalkStatusFixture.assessment.disclaimer}
      />,
    )
    // The prominent banner prefix (its own span).
    expect(
      screen.getByText('In development — not a final standard.'),
    ).toBeInTheDocument()
    // The framework's OWN disclaimer text rides alongside, verbatim.
    expect(
      screen.getByText(/IN DEVELOPMENT and NOT a final standard/i),
    ).toBeInTheDocument()
    // AIIM produces no specifications — the caveat must survive.
    expect(screen.getByText(/produces no specifications/i)).toBeInTheDocument()
  })
  it('renders nothing for a regulatory framework (no banner needed)', () => {
    const { container } = renderIntel(
      <FrameworkDisclaimerBanner framework="eu_ai_act" disclaimer="anything" />,
    )
    expect(container).toBeEmptyDOMElement()
  })
})

// --- new capability honesty hints ---------------------------------------

describe('CapabilityRow honesty hints (via ControlList)', () => {
  it('shows the compute-accounting-only / audit-evidence / claim-vs-verified hints', async () => {
    const user = userEvent.setup()
    renderIntel(<ControlList controls={statusFixture.assessment.controls} />)
    // Expand every control so its capabilities render.
    for (const btn of screen.getAllByRole('button', {
      name: /Show capabilities/i,
    })) {
      await user.click(btn)
    }
    // resource_accounting is mapped to two controls, so its hint renders more than once.
    expect(
      screen.getAllByText(/Operational compute\/cost accounting only/i).length,
    ).toBeGreaterThan(0)
    expect(
      screen.getByText(
        /it is recorded activity evidence, NOT a security alert/i,
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        /is a CLAIM, not evidence, unless it has been operator-verified/i,
      ),
    ).toBeInTheDocument()
  })
})

// --- OSCAL export honesty (by_design is NEVER laundered to satisfied) ----

describe('OscalFindingsPreview — by_design never laundered', () => {
  it('shows OSCAL state AND the real product status; by_design rides not-satisfied', () => {
    renderIntel(<OscalFindingsPreview oscal={oscalExportFixture} />)
    // The OSCAL 2-value states are shown verbatim.
    expect(screen.getByText('satisfied')).toBeInTheDocument()
    expect(screen.getAllByText('not-satisfied').length).toBeGreaterThan(0)
    // The real product status rides alongside — by_design reads "Design-ready".
    expect(screen.getByText('Design-ready')).toBeInTheDocument()
    expect(screen.getByText('Gap')).toBeInTheDocument()
    // The disclaimer carries the not-satisfied mapping rule.
    expect(
      screen.getByText(/by_design\/partial\/gap map to OSCAL not-satisfied/i),
    ).toBeInTheDocument()
    // NEVER "compliant"/"certified".
    expect(document.body.textContent ?? '').not.toMatch(/compliant|certified/i)
  })
})

// --- evidence export action + RBAC + no-secret-leak ----------------------

describe('EvidenceCard — export action', () => {
  it('renders the json/csv/oscal export buttons and the OSCAL honesty hint when permitted', () => {
    const onExport = vi.fn()
    renderIntel(
      <EvidenceCard pkg={evidenceFixture[0]} canExport onExport={onExport} />,
    )
    expect(screen.getByRole('button', { name: 'JSON' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'CSV' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'OSCAL' })).toBeInTheDocument()
    expect(
      screen.getByText(/OSCAL finding status is a 2-value enum/i),
    ).toBeInTheDocument()
  })

  it('invokes onExport with the package id and chosen format', async () => {
    const user = userEvent.setup()
    const onExport = vi.fn()
    renderIntel(
      <EvidenceCard pkg={evidenceFixture[0]} canExport onExport={onExport} />,
    )
    await user.click(screen.getByRole('button', { name: 'OSCAL' }))
    expect(onExport).toHaveBeenCalledWith(evidenceFixture[0].id, 'oscal')
  })

  it('hides the export action when the caller lacks compliance:framework:read', () => {
    renderIntel(<EvidenceCard pkg={evidenceFixture[0]} canExport={false} />)
    expect(screen.queryByRole('button', { name: 'OSCAL' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'CSV' })).toBeNull()
  })

  it('never leaks a secret/API key value', () => {
    renderIntel(
      <EvidenceCard pkg={evidenceFixture[0]} canExport onExport={vi.fn()} />,
    )
    expect(screen.queryByText(/sk-ant/)).toBeNull()
  })
})

// DEFAULT_AUTH is referenced to document the RBAC contract the container gates on
// (can('compliance:framework:read')); the pure EvidenceCard test above proves the
// gate by toggling the canExport prop the container derives from it.
void DEFAULT_AUTH
