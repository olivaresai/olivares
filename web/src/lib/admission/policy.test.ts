// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import {
  admissionDraftError,
  deriveAdmissionModes,
  pemHasResidual,
  splitLines,
  splitPemBlocks,
  type AdmissionDraft,
} from './policy'

const ROOT = `-----BEGIN CERTIFICATE-----
QUJD
-----END CERTIFICATE-----`
const KEY = `-----BEGIN PUBLIC KEY-----
REVG
-----END PUBLIC KEY-----`

/** A postable draft. Each test perturbs ONE axis so the assertion names its own cause. */
function draft(over: Partial<AdmissionDraft> = {}): AdmissionDraft {
  return {
    require_signed: false,
    rootsText: '',
    keysText: '',
    ...over,
  }
}

describe('deriveAdmissionModes', () => {
  it('empty is deny-closed, never silence', () => {
    expect(deriveAdmissionModes({})).toEqual(['empty'])
  })

  it('roots alone are chain-only PKI', () => {
    expect(deriveAdmissionModes({ trusted_roots: [ROOT] })).toEqual([
      'certificate-pki',
    ])
  })

  it('roots pinned to an identity are keyless', () => {
    expect(
      deriveAdmissionModes({
        trusted_roots: [ROOT],
        allowed_identities: ['ci@olivares.ai'],
      }),
    ).toEqual(['keyless'])
  })

  // The ISSUER half must reach keyless on its own, or the OR is only half fixed.
  it('an issuer alone also pins the roots to keyless', () => {
    expect(
      deriveAdmissionModes({
        trusted_roots: [ROOT],
        allowed_issuers: ['https://token.actions.githubusercontent.com'],
      }),
    ).toEqual(['keyless'])
  })

  it('returns EVERY active mechanism, never a precedence-picked label', () => {
    expect(
      deriveAdmissionModes({ trusted_roots: [ROOT], trusted_keys: [KEY] }),
    ).toEqual(['certificate-pki', 'bare-key'])
  })
})

describe('text splitting', () => {
  it('splitLines drops blanks and trims', () => {
    expect(splitLines('  a \n\n b  \n')).toEqual(['a', 'b'])
  })

  it('splitPemBlocks extracts whole blocks', () => {
    expect(splitPemBlocks(`${ROOT}\n\n${KEY}`)).toEqual([ROOT, KEY])
  })

  it('passes unparseable text through so the ENGINE decides', () => {
    expect(splitPemBlocks('not a pem')).toEqual(['not a pem'])
    expect(splitPemBlocks('   ')).toEqual([])
  })

  it('pemHasResidual flags a truncated second block', () => {
    expect(pemHasResidual(`${ROOT}\n-----BEGIN CERTIFICATE-----\nQQ==`)).toBe(
      true,
    )
  })

  it('pemHasResidual is false for clean input and for no-block input', () => {
    expect(pemHasResidual(`${ROOT}\n\n${KEY}`)).toBe(false)
    // No block at all is NOT residue — splitPemBlocks passes it through whole.
    expect(pemHasResidual('not a pem')).toBe(false)
  })
})

describe('admissionDraftError mirrors the engine', () => {
  it('a plain observe-mode draft is postable', () => {
    expect(admissionDraftError(draft())).toBeNull()
  })

  it('an enforcing draft with an anchor is postable', () => {
    expect(
      admissionDraftError(
        draft({
          require_signed: true,
          trusted_roots: [ROOT],
          rootsText: ROOT,
        }),
      ),
    ).toBeNull()
  })

  // Both anchor fields must be checked, or half the guard is decoration.
  it('rejects a private key in ROOTS', () => {
    expect(
      admissionDraftError(draft({ rootsText: '-----BEGIN PRIVATE KEY-----' })),
    ).toBe('privateKey')
  })

  it('rejects a private key in KEYS', () => {
    expect(
      admissionDraftError(draft({ keysText: '-----BEGIN PRIVATE KEY-----' })),
    ).toBe('privateKey')
  })

  it('rejects PEM residue in ROOTS', () => {
    expect(
      admissionDraftError(
        draft({ rootsText: `${ROOT}\n-----BEGIN CERTIFICATE-----\nQQ==` }),
      ),
    ).toBe('pemResidual')
  })

  it('rejects PEM residue in KEYS', () => {
    expect(
      admissionDraftError(
        draft({ keysText: `${KEY}\n-----BEGIN PUBLIC KEY-----\nQQ==` }),
      ),
    ).toBe('pemResidual')
  })

  it('rejects a blank predicate entry', () => {
    expect(
      admissionDraftError(draft({ allowed_predicates: ['slsa', '  '] })),
    ).toBe('emptyPredicate')
  })

  it('accepts non-blank predicates', () => {
    expect(
      admissionDraftError(draft({ allowed_predicates: ['slsa'] })),
    ).toBeNull()
  })

  it('rejects enforcement with NO anchor — it would admit nothing', () => {
    expect(admissionDraftError(draft({ require_signed: true }))).toBe(
      'anchorMissing',
    )
  })

  it('a bare KEY is anchor enough, not only a root', () => {
    expect(
      admissionDraftError(
        draft({ require_signed: true, trusted_keys: [KEY], keysText: KEY }),
      ),
    ).toBeNull()
  })

  // The mismatch is asymmetric: each direction is its own case.
  it('rejects identities without issuers', () => {
    expect(
      admissionDraftError(draft({ allowed_identities: ['ci@olivares.ai'] })),
    ).toBe('identityIssuer')
  })

  it('rejects issuers without identities', () => {
    expect(admissionDraftError(draft({ allowed_issuers: ['https://x'] }))).toBe(
      'identityIssuer',
    )
  })

  it('accepts both halves together', () => {
    expect(
      admissionDraftError(
        draft({
          allowed_identities: ['ci@olivares.ai'],
          allowed_issuers: ['https://x'],
        }),
      ),
    ).toBeNull()
  })

  it('reports the FIRST rule in the engine order when several are broken', () => {
    // Private key AND no anchor AND a lone identity: the private key wins.
    expect(
      admissionDraftError(
        draft({
          require_signed: true,
          keysText: '-----BEGIN PRIVATE KEY-----',
          allowed_identities: ['ci@olivares.ai'],
        }),
      ),
    ).toBe('privateKey')
  })
})
