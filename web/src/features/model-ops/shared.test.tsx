// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Tests for the shared model-ops admission primitives. Each pins one honesty invariant:
// the signing mode is derived from field presence; the verdict posture is never
// "verified" when it is not; and the evidence body shows transparency-log PRESENT and
// VERIFIED as distinct truths, renders signer_roots as full markers, and prints the
// engine's reason verbatim.
import { describe, expect, it } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import '@/features/_intel'
import type { ModelAdmission } from '@/features/models/types'
import { VerdictBody, deriveAdmissionModes, verdictPosture } from './shared'
import './i18n'

describe('deriveAdmissionModes — every active method is derived, never chosen', () => {
  it('is empty (admits nothing) with no anchors', () => {
    expect(deriveAdmissionModes({})).toEqual(['empty'])
  })
  it('is bare-key with only keys', () => {
    expect(
      deriveAdmissionModes({ trusted_keys: ['-----BEGIN PUBLIC KEY-----'] }),
    ).toEqual(['bare-key'])
  })
  it('is certificate-pki with roots but no identity/issuer', () => {
    expect(
      deriveAdmissionModes({ trusted_roots: ['-----BEGIN CERTIFICATE-----'] }),
    ).toEqual(['certificate-pki'])
  })
  it('is keyless with roots + an identity (or issuer)', () => {
    expect(
      deriveAdmissionModes({
        trusted_roots: ['r'],
        allowed_identities: ['ci@acme.io'],
      }),
    ).toEqual(['keyless'])
    expect(
      deriveAdmissionModes({
        trusted_roots: ['r'],
        allowed_issuers: ['https://x'],
      }),
    ).toEqual(['keyless'])
  })
  it('reports BOTH methods when roots and keys are configured together', () => {
    // A policy can enable more than one mechanism at once — neither is hidden.
    expect(
      deriveAdmissionModes({
        trusted_roots: ['r'],
        allowed_identities: ['ci@acme.io'],
        trusted_keys: ['k'],
      }),
    ).toEqual(['keyless', 'bare-key'])
  })
})

describe('verdictPosture — honest tri-state', () => {
  const base: ModelAdmission = {
    id: 'a1',
    version_ref: 'v1',
    signature_verified: false,
    artifact_verified: false,
    tlog_present: false,
    tlog_verified: false,
    resource_count: 0,
  }
  it('is denied when the signature did not verify', () => {
    expect(verdictPosture(base)).toBe('denied')
  })
  it('is signed-unbound when signed but the artifact is not bound', () => {
    expect(
      verdictPosture({
        ...base,
        signature_verified: true,
        artifact_verified: false,
      }),
    ).toBe('signed-unbound')
  })
  it('is verified only when both signature and artifact verified', () => {
    expect(
      verdictPosture({
        ...base,
        signature_verified: true,
        artifact_verified: true,
      }),
    ).toBe('verified')
  })
})

describe('VerdictBody — evidence renders each truth independently', () => {
  const verdict: ModelAdmission = {
    id: 'a1',
    version_ref: 'v1',
    method: 'certificate-pki',
    signer_identity: 'ci@acme.io',
    signer_roots: [`root:${'a'.repeat(64)}`, `root:${'b'.repeat(64)}`],
    signature_verified: true,
    artifact_verified: false,
    tlog_present: true,
    tlog_verified: false,
    resource_count: 2,
    reason: 'artifact digest not re-checked (no resolved digests supplied)',
    attested_at: '2026-07-01T10:00:00Z',
  }

  it('shows transparency-log PRESENT, never a verification success', () => {
    renderIntel(<VerdictBody verdict={verdict} />)
    // tlog_present && !tlog_verified → the honest "present, inclusion not checked" copy.
    expect(
      screen.getByText(/present \(inclusion not checked\)/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/^Verified$/)).not.toBeInTheDocument()
  })

  it('renders the verifier reason verbatim', () => {
    renderIntel(<VerdictBody verdict={verdict} />)
    expect(
      screen.getByText(
        'artifact digest not re-checked (no resolved digests supplied)',
      ),
    ).toBeInTheDocument()
  })

  it('renders each signer_root as a full copyable marker', async () => {
    renderIntel(<VerdictBody verdict={verdict} />)
    // HashChip copies the WHOLE value; the accessible copy button carries it verbatim.
    const chips = screen.getAllByRole('button', { name: /copy/i })
    expect(chips.length).toBeGreaterThanOrEqual(2)
  })

  it('shows a warning posture for signed-but-unbound, not verified', () => {
    renderIntel(<VerdictBody verdict={verdict} />)
    expect(screen.getByText(/signed, artifact unbound/i)).toBeInTheDocument()
  })
})
