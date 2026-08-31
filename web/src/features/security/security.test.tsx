// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, within } from '@/test/intel'
import '@/features/_intel' // register the shared `intel` namespace for badges/notices

const { findingsMock, exportMock, toastMock, authState } = vi.hoisted(() => ({
  findingsMock: vi.fn(),
  exportMock: vi.fn(),
  toastMock: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  authState: { can: (_p: string): boolean => true, activeTenant: 't1' },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/components/ui/toaster', () => ({ toast: toastMock, Toaster: () => null }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    securityApi: { ...actual.securityApi, findings: findingsMock, exportFindings: exportMock },
  }
})
import {
  AnomalyList,
  EnforcementTable,
  FindingsTable,
  GuardrailVerdict,
  IntegrityPanel,
} from './components'
import {
  anomaliesFixture,
  enforcementEmptyFixture,
  enforcementFixture,
  findingsFixture,
  inspectFlagFixture,
  integrityBrokenFixture,
  integrityEmptyFixture,
  integrityPendingFixture,
  integrityUnavailableFixture,
} from './fixtures'
import { SecurityView } from './security-view'
import './i18n'

/** Captured browser-download side effects: the test asserts on the EXACT bytes
 *  that reached the blob, since re-encoding a SARIF run is the failure to catch. */
let downloadedBlob: Blob | null = null
let anchorClicks = 0

/** renderIntel already wraps the providers the shared kit needs (query client +
 *  TooltipProvider); the view's HashChip tooltips fail without the latter. */
function wrapView(ui: ReactElement) {
  return renderIntel(ui)
}

beforeEach(() => {
  vi.clearAllMocks()
  downloadedBlob = null
  anchorClicks = 0
  authState.can = () => true
  findingsMock.mockResolvedValue({ items: findingsFixture, total: findingsFixture.length })
  exportMock.mockResolvedValue({
    filename: 'olivares-findings.sarif.json',
    content_type: 'application/json',
    text: '{"$schema":"sarif","runs":[]}',
    truncated: false,
  })
  vi.stubGlobal('URL', {
    ...URL,
    createObjectURL: (blob: Blob) => {
      downloadedBlob = blob
      return 'blob:x'
    },
    revokeObjectURL: () => {},
  })
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {
    anchorClicks += 1
  })
})

describe('FindingsTable — evidence is a fingerprint, never a payload', () => {
  it('renders a finding with its severity and the detail_hash as a fingerprint', () => {
    renderIntel(<FindingsTable findings={findingsFixture} />)
    const table = screen.getByRole('grid')
    // severity badge for the critical finding
    expect(within(table).getByText(/Critical/i)).toBeInTheDocument()
    expect(
      within(table).getByText(/Indirect prompt injection/i),
    ).toBeInTheDocument()
    // the detail_hash is shown TRUNCATED (a fingerprint), not the full digest…
    expect(within(table).getByText(/9f1c2b7d…2a4c6b/)).toBeInTheDocument()
    // …and the full 64-char digest is NOT in the rendered table text (no payload)
    expect(
      within(table).queryByText(/9f1c2b7d4e6a8c0f3b5d7e9a/),
    ).not.toBeInTheDocument()
  })

  it('does not render a triage control unless the caller allows it', () => {
    renderIntel(<FindingsTable findings={findingsFixture} />)
    expect(
      screen.queryByRole('button', { name: /Triage/i }),
    ).not.toBeInTheDocument()
  })

  //the local connector emits one posture per model Ollama is holding in memory
  // RIGHT NOW (connectors/local/local.go:185 residencyPosture, subject kind
  // `local.residency`), and the severity IS the placement: fully in VRAM is info, CPU
  // or split is medium, because that is the latency the operator pays unannounced.
  // The row existed and nothing painted it — subject_kind was dumped raw.
  it('paints the local-residency subject kind and shows the severity the CONNECTOR set', () => {
    const resident = {
      id: 'f-res-1',
      tenant: 'acme',
      kind: 'posture',
      severity: 'medium' as const,
      status: 'open' as const,
      source: 'olivares.local',
      subject_kind: 'local.residency',
      subject_ref: 'llama3:8b',
      title:
        'Ollama model resident on split gpu/cpu: llama3:8b (3221225472 of 8589934592 bytes in VRAM)',
      detail_hash: 'a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90',
      occurred_at: '2026-08-09T10:00:00Z',
    }
    renderIntel(<FindingsTable findings={[resident]} />)
    const table = screen.getByRole('grid')

    // Painted, not dumped.
    expect(within(table).getByText(/Resident local model/i)).toBeInTheDocument()
    expect(within(table).queryByText(/^local\.residency:/)).not.toBeInTheDocument()

    // The severity is the connector's — PAINTED, never recomputed here. A `split
    // gpu/cpu` placement is medium; rendering it as informational would erase exactly
    // the signal the connector emitted this row to carry.
    expect(within(table).getByText(/Medium/i)).toBeInTheDocument()
    expect(within(table).queryByText(/^Info$/i)).not.toBeInTheDocument()

    // The raw kind stays reachable — it is what the subject_kind filter takes.
    expect(
      within(table).getByTitle('local.residency: llama3:8b'),
    ).toBeInTheDocument()
  })

  // The subject kind that BROKE the first version of this: `local` is the parent of
  // the old nested `local.residency` key, so interpolating the kind straight into an
  // i18n key made i18next resolve an OBJECT and render its own error text — "returned
  // an object instead of string" — into the table. Flat keys cannot nest.
  it('renders a kind that is the prefix of a known one as itself, not an i18n error', () => {
    const bare = {
      id: 'f-bare-1',
      tenant: 'acme',
      kind: 'posture',
      severity: 'low' as const,
      status: 'open' as const,
      source: 'olivares.local',
      subject_kind: 'local',
      subject_ref: 'host-1',
      title: 'A posture whose subject kind is the bare prefix',
      detail_hash: 'c1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90',
      occurred_at: '2026-08-09T10:00:00Z',
    }
    renderIntel(<FindingsTable findings={[bare]} />)
    const text = screen.getByRole('grid').textContent ?? ''
    expect(text).toContain('local: host-1')
    expect(text).not.toMatch(/returned an object/i)
  })

  // The NON-FIRING direction: a kind this console has never heard of must degrade to
  // the raw value it always showed. A label map that blanked unknown kinds would pass
  // the test above and silently empty every other posture row.
  it('falls back to the raw subject kind for a kind it does not know', () => {
    const unknown = {
      id: 'f-unknown-1',
      tenant: 'acme',
      kind: 'posture',
      severity: 'low' as const,
      status: 'open' as const,
      source: 'some.future.connector',
      subject_kind: 'future.thing',
      subject_ref: 'widget-7',
      title: 'A posture from a connector this console predates',
      detail_hash: 'b1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90',
      occurred_at: '2026-08-09T10:00:00Z',
    }
    renderIntel(<FindingsTable findings={[unknown]} />)
    expect(
      screen.getByRole('grid').textContent?.includes('future.thing: widget-7'),
    ).toBe(true)
  })
})

describe('IntegrityPanel — unavailable is calm, broken is loud', () => {
  it('renders an unverified checkpoint key as "unavailable", never a failure', () => {
    renderIntel(<IntegrityPanel integrity={integrityUnavailableFixture} />)
    // the chain itself is verified…
    expect(screen.getByText(/Integrity verified/i)).toBeInTheDocument()
    // …and the missing signing key reads "signing not available", NOT a failure
    expect(screen.getByText(/Signing not available/i)).toBeInTheDocument()
    expect(screen.queryByText(/Integrity failed/i)).not.toBeInTheDocument()
  })

  it('renders a genuine tamper as an integrity failure', () => {
    renderIntel(<IntegrityPanel integrity={integrityBrokenFixture} />)
    expect(screen.getAllByText(/Integrity failed/i).length).toBeGreaterThan(0)
  })

  // The first-boot case: nothing attested YET is not a tamper finding. The engine
  // sends checkpoints_ok=false for this AND for a forged checkpoint, so a panel
  // that reads the boolean paints a healthy new install red.
  it('renders an unattested young ledger as pending, never a failure', () => {
    renderIntel(<IntegrityPanel integrity={integrityPendingFixture} />)
    expect(screen.queryByText(/Integrity failed/i)).not.toBeInTheDocument()
    expect(screen.getByText(/Not attested yet/i)).toBeInTheDocument()
    // Distinct from the "no signing key wired" answer — a key IS wired here.
    expect(screen.queryByText(/Signing not available/i)).not.toBeInTheDocument()
    // …and the chain half still reports its own green verdict.
    expect(screen.getByText(/Integrity verified/i)).toBeInTheDocument()
  })

  // The control: the calm state above must not be reachable for a ledger whose
  // checkpoints exist and do not verify.
  it('does not soften a failed checkpoint into pending', () => {
    renderIntel(<IntegrityPanel integrity={integrityBrokenFixture} />)
    expect(screen.queryByText(/Not attested yet/i)).not.toBeInTheDocument()
    expect(screen.getAllByText(/Integrity failed/i).length).toBeGreaterThan(0)
  })
  /**
   * ⛔ EL LEDGER VACÍO NO ES UNA CADENA ROTA, y salía ROJO. `Verify`
   * (`core/internal/store/sqlstore/audit.go:623-629`) deja `OK: false` a propósito con
   * `Checked == 0` — «An empty range proves nothing … must not be able to turn an absent ledger
   * into "verified" evidence through vacuous truth». El motor acierta; la consola no puede leer
   * ese `false` como FALLO.
   *
   * EL MUTANTE: pasar sólo `ok` a la insignia, como estaba. Una instalación recién levantada abre
   * la vista de seguridad y lo primero que lee es que **su cadena de evidencia está rota**. Es el
   * rojo que la propia insignia documenta como «the red that teaches operators to ignore red», y
   * la salida (`pending`) ya existía: se usaba dos tarjetas más abajo, para los checkpoints.
   */
  it('un ledger sin eventos no se pinta como cadena rota', () => {
    renderIntel(<IntegrityPanel integrity={integrityEmptyFixture} />)
    expect(screen.queryByText(/Integrity failed/i)).not.toBeInTheDocument()
    expect(screen.getByText(/no events/i)).toBeInTheDocument()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR, y es la que protege el arreglo: una cadena REALMENTE rota
   * —con eventos comprobados y un corte— sigue siendo roja. Sin esta casilla, ablandar el vacío
   * podría ablandar también la manipulación, que es el único fallo que esta pantalla existe para
   * gritar.
   */
  it('una cadena con eventos y un corte sigue siendo un fallo', () => {
    renderIntel(<IntegrityPanel integrity={integrityBrokenFixture} />)
    expect(screen.getAllByText(/Integrity failed/i).length).toBeGreaterThan(0)
    expect(screen.queryByText(/no events/i)).not.toBeInTheDocument()
  })
})

describe('AnomalyList — approximate is never titled a firm violation', () => {
  it('hedges an approximate-confidence anomaly and shows the unreconciled note', () => {
    renderIntel(<AnomalyList anomalies={anomaliesFixture} />)
    // the approximate badge is shown for the access_drift entry
    expect(screen.getByText(/Approximate/i)).toBeInTheDocument()
    // its title is hedged ("Suspected: …"), not asserted as a firm violation
    expect(screen.getByText(/Suspected:/i)).toBeInTheDocument()
    expect(screen.getByText(/Unreconciled drift/i)).toBeInTheDocument()
    // a high-confidence, attributed anomaly keeps its plain title
    expect(
      screen.getByText('Suspected egress to an external endpoint'),
    ).toBeInTheDocument()
  })

  it('keeps the backend priority order (highest first)', () => {
    renderIntel(<AnomalyList anomalies={anomaliesFixture} />)
    const priorities = screen
      .getAllByText(/^(94|81|47)$/)
      .map((el) => Number(el.textContent))
    expect(priorities).toEqual([94, 81, 47])
  })
})

describe('EnforcementTable — detective by default, ungoverned flagged', () => {
  it('reads an empty posture as fully detective (the safe default)', () => {
    renderIntel(<EnforcementTable entries={enforcementEmptyFixture} />)
    expect(screen.getByText(/fully detective/i)).toBeInTheDocument()
  })

  it('flags a class enabled without human governance', () => {
    renderIntel(<EnforcementTable entries={enforcementFixture} />)
    const table = screen.getByRole('grid')
    // the governed class shows "Governed"…
    expect(within(table).getByText(/^Governed$/)).toBeInTheDocument()
    // …and the ungoverned-but-enabled class shows the warning, never hidden
    expect(
      within(table).getByText(/Enabled without human governance/i),
    ).toBeInTheDocument()
  })
})

describe('GuardrailVerdict — detective plane does not block on its own', () => {
  it('explains that a flag came from the detective plane, not a block', () => {
    renderIntel(<GuardrailVerdict result={inspectFlagFixture} />)
    expect(screen.getByText(/^Flag$/)).toBeInTheDocument()
    expect(screen.getByText(/detective plane/i)).toBeInTheDocument()
    // redacted excerpt is shown as a label, never the secret
    expect(screen.getByText(/\[redacted: email\]/i)).toBeInTheDocument()
    expect(screen.queryByText(/^Block$/)).not.toBeInTheDocument()
  })
})

describe('Findings SARIF export — the server bytes, and an honest cap', () => {
  it('disables the action while there is nothing to export', async () => {
    findingsMock.mockResolvedValue({ items: [], total: 0 })
    wrapView(<SecurityView />)
    const button = await screen.findByRole('button', { name: /Export SARIF/i })
    await waitFor(() => expect(button).toBeDisabled())
    expect(exportMock).not.toHaveBeenCalled()
  })

  it('downloads the exact bytes the server returned', async () => {
    const user = userEvent.setup()
    wrapView(<SecurityView />)

    await user.click(await screen.findByRole('button', { name: /Export SARIF/i }))

    await waitFor(() => expect(exportMock).toHaveBeenCalled())
    // The blob carries the server's body verbatim — the client never re-encodes
    // a SARIF run it is about to hand to a code-scanning consumer.
    expect(await downloadedBlob?.text()).toBe('{"$schema":"sarif","runs":[]}')
    expect(anchorClicks).toBe(1)
    expect(toastMock.warning).not.toHaveBeenCalled()
  })

  it('says so when the export stopped at the server cap', async () => {
    const user = userEvent.setup()
    exportMock.mockResolvedValue({
      filename: 'olivares-findings.sarif.json',
      content_type: 'application/json',
      text: '{"$schema":"sarif","runs":[]}',
      truncated: true,
    })
    wrapView(<SecurityView />)

    await user.click(await screen.findByRole('button', { name: /Export SARIF/i }))

    await waitFor(() => expect(toastMock.warning).toHaveBeenCalled())
    expect(toastMock.success).not.toHaveBeenCalled()
    // A capped export still downloads: it is a valid run, just not the whole set.
    expect(anchorClicks).toBe(1)
  })
})
