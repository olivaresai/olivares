// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, waitFor, within } from '@/test/intel'
import '@/features/_intel' // register the shared `intel` namespace for badges/notices
import {
  AirgapPanel,
  ArtifactTable,
  BinaryPanel,
  ReleaseStatePanel,
  SbomPanel,
  ScorecardPanel,
  SlaPanel,
  SlsaPanel,
  VexPanel,
} from './components'
import { AttestationView } from './attestation-view'
import {
  binaryAttestationFixture,
  exampleReleaseFixture,
  publishedBinaryAttestationFixture,
} from './fixtures'
import './i18n'

// RBAC: the view gates the whole privileged read on `compliance:read`. A hoisted,
// mutable auth lets the view tests flip `can` while the pure-component tests (which
// never touch useAuth) are unaffected. vi.mock is hoisted above the imports by Vitest.
const auth = vi.hoisted(() => ({
  activeTenant: 'demo' as string | null,
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

// The live running-binary read: mock only the api object, keep keys real.
const api = vi.hoisted(() => ({ binary: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, attestationApi: api }
})

const release = exampleReleaseFixture

afterEach(() => {
  auth.can = () => true
  vi.clearAllMocks()
})

describe('ArtifactTable', () => {
  it('renders the declared artifacts with no secret leaked', () => {
    renderIntel(<ArtifactTable artifacts={release.artifacts} />)
    const grid = screen.getByRole('grid')
    expect(within(grid).getByText('checksums.txt')).toBeInTheDocument()
    expect(
      within(grid).getByText(/ghcr\.io\/olivaresai\/charts/),
    ).toBeInTheDocument()
    // No credential / signing key value is ever rendered.
    expect(screen.queryByText(/sk-ant/)).not.toBeInTheDocument()
    expect(screen.queryByText(/-----BEGIN/)).not.toBeInTheDocument()
  })

  it('shows declared status, never a fabricated "verified"', () => {
    renderIntel(<ArtifactTable artifacts={release.artifacts} />)
    // every status reads "Declared" — never a live "Integrity verified".
    expect(screen.getAllByText(/Declared/).length).toBeGreaterThan(0)
    expect(screen.queryByText(/Integrity verified/i)).not.toBeInTheDocument()
  })
})

describe('SlsaPanel — honesty', () => {
  it('keeps the semver-tag (not SHA) pin disclaimer', () => {
    renderIntel(<SlsaPanel slsa={release.slsa} />)
    expect(screen.getByText(/SLSA Build L3/)).toBeInTheDocument()
    // The load-bearing honesty: pinned by semver tag, not all deps SHA-pinned.
    expect(
      screen.getByText(/pinned by semver tag \(not SHA\)/i),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/Not every dependency is SHA-pinned/i),
    ).toBeInTheDocument()
  })
})

describe('SbomPanel — CISA DRAFT', () => {
  it('labels the CISA checklist as a draft, not law', () => {
    renderIntel(<SbomPanel sbom={release.sbom} />)
    expect(
      screen.getByText(/NOT law or a finalized federal/i),
    ).toBeInTheDocument()
    // hard vs soft enforcement both present in the grid.
    const grid = screen.getByRole('grid')
    expect(within(grid).getAllByText(/Hard/).length).toBeGreaterThan(0)
    expect(within(grid).getAllByText(/Soft/).length).toBeGreaterThan(0)
  })
})

describe('VexPanel', () => {
  it('renders the OpenVEX status enum + justification', () => {
    renderIntel(<VexPanel vex={release.vex} />)
    const grid = screen.getByRole('grid')
    expect(within(grid).getByText(/Not affected/i)).toBeInTheDocument()
    expect(within(grid).getByText(/Under investigation/i)).toBeInTheDocument()
    expect(
      within(grid).getByText(/vulnerable_code_not_in_execute_path/),
    ).toBeInTheDocument()
  })
})

describe('ScorecardPanel — external note', () => {
  it('renders the four checks and the external/api.scorecard.dev honesty note', () => {
    renderIntel(<ScorecardPanel scorecard={release.scorecard} />)
    expect(screen.getAllByText('Pinned-Dependencies').length).toBeGreaterThan(0)
    expect(screen.getAllByText('Branch-Protection').length).toBeGreaterThan(0)
    // Honest: external, not engine telemetry.
    expect(screen.getByText(/api\.scorecard\.dev/)).toBeInTheDocument()
    expect(
      screen.getByText(/EXTERNAL data, not engine telemetry/i),
    ).toBeInTheDocument()
  })

  it('exposes the chart-equivalent table via AccessibleChart (1.4.1)', () => {
    renderIntel(<ScorecardPanel scorecard={release.scorecard} />)
    // AccessibleChart wraps the gauge with a "Show as table" toggle.
    expect(
      screen.getAllByRole('button', { name: /Show as table/i }).length,
    ).toBeGreaterThan(0)
  })
})

describe('SlaPanel', () => {
  it('renders the CVE SLA tiers + the KEV→Critical rule', () => {
    renderIntel(<SlaPanel pv={release.patch_velocity} />)
    const grid = screen.getByRole('grid')
    expect(within(grid).getByText(/Critical/)).toBeInTheDocument()
    expect(within(grid).getByText('7 days')).toBeInTheDocument()
    expect(within(grid).getByText('14 days')).toBeInTheDocument()
    expect(screen.getByText(/treated as Critical/i)).toBeInTheDocument()
  })
})

describe('AirgapPanel', () => {
  it('renders zero-phone-home + dual-signing chart facts', () => {
    renderIntel(<AirgapPanel airgap={release.airgap} helm={release.helm} />)
    // ⛔ ESTA CELDA FIJABA UNA PROMESA QUE EL PRODUCTO NO SOSTIENE. Afirmaba que la pantalla dice
    //    «nothing phones home», y el contacto con el fabricante está APROBADO por decisión firmada:
    //    `olivares upgrade` alcanza `olivares.ai/updates` (`cmd/olivares/cmd_upgrade.go:60-61`).
    //    Una celda que exige que la copy siga siendo falsa impide corregirla — es la segunda vez
    //    hoy que me encuentro una así, y las dos eran mías.
    //
    //    Lo que fija ahora son las DOS mitades: que se diga lo acotado y cierto —sin llamadas
    //    obligatorias al arranque— y que la promesa absoluta NO esté. Una sola de las dos deja
    //    pasar la mitad del defecto.
    expect(
      screen.getByText(/no mandatory outbound calls at boot/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/nothing phones home/i)).toBeNull()
    expect(
      screen.getByText(/oci:\/\/ghcr\.io\/olivaresai\/charts/),
    ).toBeInTheDocument()
    // Dual-signing: cosign manifest + the Helm-native GPG .prov (maintainer path).
    expect(screen.getByText(/Helm-native GPG \.prov/)).toBeInTheDocument()
  })
})

describe('BinaryPanel — measured honesty', () => {
  it('renders the ldflags values VERBATIM (dev/none/unknown) with no invented claim', () => {
    renderIntel(
      <BinaryPanel
        binary={binaryAttestationFixture.binary}
        capturedAt={binaryAttestationFixture.captured_at}
      />,
    )
    // Defaults render verbatim — never replaced with a fabricated version.
    expect(screen.getByText('dev')).toBeInTheDocument()
    expect(screen.getByText('none')).toBeInTheDocument()
    expect(screen.getByText('unknown')).toBeInTheDocument()
    // FIPS mode is stated explicitly, off is calm (not an error).
    expect(screen.getByText(/FIPS 140-3: off/i)).toBeInTheDocument()
    // The go.work caveat: no vcs.* stamp, with the backend's reason verbatim.
    expect(screen.getByText(/No VCS stamp/i)).toBeInTheDocument()
    expect(
      screen.getByText(/Go stamps no vcs\.\* settings/i),
    ).toBeInTheDocument()
    // Self-hash is a fingerprint for OFFLINE comparison — the hint says so.
    expect(
      screen.getByText(/Compare it OFFLINE against a future signed/i),
    ).toBeInTheDocument()
  })
})

describe('ReleaseStatePanel — honest not-published state', () => {
  it('renders not_published / not_verified explicitly, never a fake verification', () => {
    renderIntel(
      <ReleaseStatePanel
        release={binaryAttestationFixture.release}
        pipeline={binaryAttestationFixture.pipeline}
      />,
    )
    expect(screen.getByText(/Not published/i)).toBeInTheDocument()
    // IntegrityBadge `unavailable` (calm neutral), NEVER a fake "verified".
    expect(screen.getByText(/Signing not available/i)).toBeInTheDocument()
    expect(screen.queryByText(/Integrity verified/i)).not.toBeInTheDocument()
    // The verifier claim is the compile-time-proven one, with its coordinates.
    expect(screen.getByText(/Native verifier compiled in/i)).toBeInTheDocument()
    // No fabricated Rekor-inclusion claim.
    expect(
      screen.getByText(/never claims Rekor inclusion/i),
    ).toBeInTheDocument()
    // The 5 pipeline workflows render verbatim, status declared.
    expect(screen.getByText(/release\.yml/)).toBeInTheDocument()
    // The declared block must NOT answer the question it declares unobservable.
    // It used to end "it has never been fired (beta)" — a repository-history claim
    // rendered next to a live badge that could say Published.
    expect(screen.queryByText(/has never been fired/i)).not.toBeInTheDocument()
    expect(
      screen.getByText(/cannot say whether that has ever happened/i),
    ).toBeInTheDocument()
    // The provenance qualifier is on the NEGATIVE side too: a consumer must not
    // learn about self-declaration only when the answer turns positive.
    expect(
      screen.getByText(/SELF-DECLARED build provenance/i),
    ).toBeInTheDocument()
  })

  // The other polarity, and the reason this file changed at all: until 2026-08-13 the
  // backend could not emit published=true for ANY build (measuredRelease() ignored its
  // own binary and returned not_published unconditionally), so this branch of the panel
  // was unreachable in production and untested here. The badge was already data-driven,
  // so the component needed no change — but "no change needed" is a claim, and this is
  // the measurement behind it.
  it('renders Published — and drops the not-published badge — when the backend says so', () => {
    renderIntel(
      <ReleaseStatePanel
        release={publishedBinaryAttestationFixture.release}
        pipeline={publishedBinaryAttestationFixture.pipeline}
      />,
    )
    // Exact string, not /Published/i: "Not published" CONTAINS "published", so a
    // regex here would pass on the very payload this test exists to distinguish.
    expect(screen.getByText('Published')).toBeInTheDocument()
    expect(screen.queryByText(/Not published/i)).not.toBeInTheDocument()
    // The integrity posture must NOT ride along with the release verdict: the
    // process still cannot verify its own signature, so the calm unavailable state
    // stays and no "Integrity verified" is ever fabricated.
    expect(screen.getByText(/Signing not available/i)).toBeInTheDocument()
    expect(screen.queryByText(/Integrity verified/i)).not.toBeInTheDocument()
    // And the reason renders verbatim, naming the release it decided on. Matched on
    // the version rather than on "OTA verification anchor", which the reason and the
    // signature reason BOTH carry — a query that resolves to two nodes is not a
    // measurement of either.
    expect(screen.getByText(/release-stamped 26\.8\.0/i)).toBeInTheDocument()
  })

  // THE 2026-08-14 HALF. A binary forged with two -ldflags values — no `-tags
  // release`, no signature, no tag, no publication — produces exactly the payload
  // above (compiled and run against this tree). The panel therefore may not present
  // it as proof that a release was published, and this test is what says so.
  it('qualifies the positive state as self-declared, and never as a verified one', () => {
    renderIntel(
      <ReleaseStatePanel
        release={publishedBinaryAttestationFixture.release}
        pipeline={publishedBinaryAttestationFixture.pipeline}
      />,
    )
    // The qualifier arrives with the badge, from the backend, needing no locale string.
    expect(
      screen.getByText(/SELF-DECLARED build provenance/i),
    ).toBeInTheDocument()
    expect(screen.getByText(/not an attestation/i)).toBeInTheDocument()
    // The word the old reason used, and the claim the evidence does not support.
    expect(screen.queryByText(/published release/i)).not.toBeInTheDocument()
    // And the chip is not the design system's verified-green. Asserted on the class
    // the variant emits, because "the badge is not success" is the actual claim —
    // matching on the label would pass with any colour.
    expect(screen.getByText('Published').className).not.toMatch(/success/)
  })
})

// --- container view: RBAC gating + live measured panel + declared tabs ---------

describe('AttestationView — RBAC + live flip', () => {
  it('hides everything behind a calm boundary when compliance:read is denied', () => {
    auth.can = () => false
    renderIntel(<AttestationView />)
    // The privileged read is gated — no grid, no live fetch.
    expect(screen.queryByRole('grid')).not.toBeInTheDocument()
    expect(screen.getByText(/Not authorized/i)).toBeInTheDocument()
    expect(api.binary).not.toHaveBeenCalled()
  })

  it('renders the LIVE measured panels plus the declared tabs when permitted', async () => {
    auth.can = () => true
    api.binary.mockResolvedValue(binaryAttestationFixture)
    renderIntel(<AttestationView />)
    await waitFor(() => expect(api.binary).toHaveBeenCalled())
    // Live half: measured binary facts + the honest not-published state.
    expect(await screen.findByText('dev')).toBeInTheDocument()
    expect(screen.getByText(/Not published/i)).toBeInTheDocument()
    expect(screen.getByText(/Signing not available/i)).toBeInTheDocument()
    // Declared half: the artifact rows still render from attestation.data.ts
    // (the example-release fixture is gone from the view).
    expect(screen.getByText('checksums.txt')).toBeInTheDocument()
    // The beta disclaimer STAYS on a not-published binary — it is true for it.
    expect(
      screen.getByText(/no releases exist yet and the release pipeline/i),
    ).toBeInTheDocument()
    // And no red error anywhere.
    expect(screen.queryByText(/Something went wrong/i)).not.toBeInTheDocument()
  })

  // FINDING 2 (2026-08-14 contrast): the static beta disclaimer says "no releases
  // exist yet"; the live panel beside it can say Published. Both were rendered
  // unconditionally, so production showed two mutually exclusive propositions on one
  // screen. They are now driven by the SAME fact and cannot disagree.
  it('drops the beta disclaimer when the live read reports a release-stamped binary', async () => {
    auth.can = () => true
    api.binary.mockResolvedValue(publishedBinaryAttestationFixture)
    renderIntel(<AttestationView />)
    await waitFor(() => expect(api.binary).toHaveBeenCalled())
    // The live panel has answered…
    expect(await screen.findByText('Published')).toBeInTheDocument()
    // …so the sentence that denies any release exists is gone.
    expect(
      screen.queryByText(/no releases exist yet and the release pipeline/i),
    ).not.toBeInTheDocument()
    // NOTHING EVERGREEN WAS LOST, and this is the measurement behind that claim:
    // the never-re-verify-in-the-browser honesty is in the page description, which
    // renders unconditionally…
    expect(
      screen.getByText(/Nothing is re-verified in the browser/i),
    ).toBeInTheDocument()
    // …and the positive state still arrives qualified as self-declared.
    expect(
      screen.getByText(/SELF-DECLARED build provenance/i),
    ).toBeInTheDocument()
  })

  // The gate is DENY-CLOSED: only an actual `published` answer removes the notice.
  // While the live read is in flight the console knows nothing, and "nothing" must
  // not be rendered as "a release exists".
  it('keeps the beta disclaimer while the live read is still in flight', () => {
    auth.can = () => true
    api.binary.mockReturnValue(new Promise(() => {})) // never settles
    renderIntel(<AttestationView />)
    expect(
      screen.getByText(/no releases exist yet and the release pipeline/i),
    ).toBeInTheDocument()
  })
})
