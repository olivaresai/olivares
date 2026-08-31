// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// the honesty invariants of the capability catalog.
//
// Every cell below pins a sentence that would be a LIE if the code changed under it,
// and each is written so that exactly one mutation kills it. The three that matter
// are the three ways this screen can look right and mislead: a count that is really
// an absence, a count that is really a floor, and an "I did not measure" drawn as
// "there is nothing".
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_AUTH, renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'
import type { CapabilityCatalogResponse, CapabilityEvidence } from './types'

const api = {
  capabilities: vi.fn(),
  // Consumed by the PARENT container in the wiring block below.
  summary: vi.fn(),
  frameworks: vi.fn(),
}

vi.mock('@/lib/auth/context', () => ({ useAuth: () => ({ ...DEFAULT_AUTH }) }))
vi.mock('./api', async () => {
  const actual = await vi.importActual<typeof import('./api')>('./api')
  return { ...actual, complianceApi: { ...actual.complianceApi, ...api } }
})

// Sin esto el contador de llamadas es ACUMULADO entre celdas y la última cuenta seis:
// un mock sin limpiar hace que las casillas se contesten por ORDEN, no por su sujeto.
beforeEach(() => {
  api.capabilities.mockReset()
})

const { CapabilityCatalog } = await import('./capability-catalog')
const { ComplianceView } = await import('./compliance-view')
await import('./i18n')

// --- fixtures mirroring modules/compliance/types.go:121-140 -------------------

/** Architectural: cited to the design docs. The engine's own field doc says Count is
 *  "0 for architectural or absent", and `omitempty` erases the difference. */
const ARCH: CapabilityEvidence = {
  key: 'tenant_isolation',
  class: 'architectural',
  state: 'present',
  detail: 'Tenant isolation — every query is scoped by tenant.',
  refs: [{ kind: 'design', detail: 'docs/05 §4' }],
}

/** Operational AND truncated: `more` says the count stopped at the page cap. */
const TRUNCATED: CapabilityEvidence = {
  key: 'session_recording',
  class: 'operational',
  state: 'present',
  detail: 'Privileged session recording — frames on the chain.',
  count: 5,
  more: true,
}

/** Evaluated and empty: an honest gap, and the one case where "0" is the truth. */
const EMPTY_OP: CapabilityEvidence = {
  key: 'dlp_scanning',
  class: 'operational',
  state: 'absent',
  detail: 'DLP scanning — no rows back it.',
}

/** Could NOT be evaluated. Not the same as absent, and the difference is the point. */
const UNKNOWN: CapabilityEvidence = {
  key: 'siem_forwarding',
  class: 'operational',
  state: 'unknown',
  detail: 'SIEM forwarding — the evidence source did not answer.',
}

const DISCLAIMER =
  'This is control status and evidence. It is not a certification.'

function body(items: CapabilityEvidence[]): CapabilityCatalogResponse {
  return { capabilities: items, disclaimer: DISCLAIMER }
}

describe('capability catalog — what a count is allowed to claim', () => {
  it('draws an architectural capability as NOT COUNTED, never as zero', async () => {
    api.capabilities.mockResolvedValue(body([ARCH]))
    renderIntel(<CapabilityCatalog />)

    const row = (await screen.findByRole('row', {
      name: /tenant_isolation/,
    })) as HTMLElement
    expect(row.textContent).toContain('not counted')
    // The mutation this kills: rendering `cap.count ?? 0` for every class, which
    // turns "there is nothing to count" into a measured zero.
    expect(row.textContent).not.toMatch(/(^|\D)0(\D|$)/)
  })

  it('draws a TRUNCATED count as a floor, and says it was truncated', async () => {
    api.capabilities.mockResolvedValue(body([TRUNCATED]))
    renderIntel(<CapabilityCatalog />)

    const row = (await screen.findByRole('row', {
      name: /session_recording/,
    })) as HTMLElement
    expect(row.textContent).toContain('at least 5')
    expect(row.textContent).toContain('truncated')
  })

  it('still prints 0 for an OPERATIONAL capability with no rows — there it is the truth', async () => {
    api.capabilities.mockResolvedValue(body([EMPTY_OP]))
    renderIntel(<CapabilityCatalog />)

    const row = (await screen.findByRole('row', {
      name: /dlp_scanning/,
    })) as HTMLElement
    expect(row.textContent).toMatch(/(^|\D)0(\D|$)/)
  })
})

describe('capability catalog — a class this build has no label for', () => {
  /** `CapabilityClass` is widened with `| string` on purpose (types.ts:21): the engine
   *  catalog can add a class. Both failure modes are pinned here. */
  const FUTURE: CapabilityEvidence = {
    key: 'confidential_compute',
    class: 'cryptographic',
    state: 'present',
    detail: 'A class this build predates.',
  }

  it('shows the raw class instead of a missing i18n key, and does not count it', async () => {
    api.capabilities.mockResolvedValue(body([FUTURE]))
    renderIntel(<CapabilityCatalog />)

    const row = (await screen.findByRole('row', {
      name: /confidential_compute/,
    })) as HTMLElement
    expect(row.textContent).toContain('cryptographic')
    expect(row.textContent).not.toContain('capabilities.class.')
    // Not operational ⇒ nothing to count. Counting by default would make a class the
    // engine just added claim "0 rows".
    expect(row.textContent).toContain('not counted')
  })
})

describe('capability catalog — unknown is not absent', () => {
  it('separates "not evaluated" from "no evidence"', async () => {
    api.capabilities.mockResolvedValue(body([UNKNOWN, EMPTY_OP]))
    renderIntel(<CapabilityCatalog />)

    const unknown = (await screen.findByRole('row', {
      name: /siem_forwarding/,
    })) as HTMLElement
    const absent = screen.getByRole('row', { name: /dlp_scanning/ })
    expect(unknown.textContent).toContain('Not evaluated')
    // Not inside waitFor on purpose: a negative assertion there passes on the first
    // tick and would hold with the defect in place.
    expect(unknown.textContent).not.toContain('No evidence')
    expect(absent.textContent).toContain('No evidence')
  })
})

describe('capability catalog — the disclaimer rides the body', () => {
  it('renders the engine disclaimer as a note', async () => {
    api.capabilities.mockResolvedValue(body([ARCH]))
    renderIntel(<CapabilityCatalog />)

    const note = await screen.findByRole('note')
    expect(note.textContent).toBe(DISCLAIMER)
  })

  it('asks the engine for the catalog exactly once', async () => {
    api.capabilities.mockResolvedValue(body([ARCH]))
    renderIntel(<CapabilityCatalog />)

    await screen.findByRole('row', { name: /tenant_isolation/ })
    expect(api.capabilities).toHaveBeenCalledTimes(1)
  })
})

// --- the wiring ----------------------------------------------------------------
//
// A component nobody renders passes every component-level cell it has. This block
// starts in the PARENT and presses the trigger, because deleting a tab's wiring is
// invisible to everything above.

describe('the catalog is reachable from the compliance view', () => {
  it('renders the catalog after a click on its tab trigger', async () => {
    api.capabilities.mockResolvedValue(body([ARCH]))
    api.summary.mockResolvedValue({
      frameworks: [],
      disclaimer: 'Not a certification.',
    })
    api.frameworks.mockResolvedValue({ items: [] })
    const user = userEvent.setup()
    renderIntel(<ComplianceView />)

    await user.click(await screen.findByRole('tab', { name: 'Capabilities' }))
    expect(
      await screen.findByRole('row', { name: /tenant_isolation/ }),
    ).toBeInTheDocument()
  })
})
