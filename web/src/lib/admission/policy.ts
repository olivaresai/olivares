// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** Shared admission-policy logic.
 *
 *  The engine enforces the SAME trust-anchor contract for every admission policy it
 *  serves — signed models (`modules/models/admission.go`) and catalog MCP/connector
 *  entries (`modules/catalog/{mcpadmission,connectoradmission}.go` `validate()`). The
 *  DTOs differ by one boolean and one list; the RULES do not.
 *
 *  This module exists because those rules were written once, inside one screen's editor.
 *  A second copy is how a fixed rule stays broken in the other surface: the PEM residual
 *  check below is subtle enough that nobody would notice it drifting. */

/** The four fields that decide which signing mechanisms a policy configures. Structural,
 *  so any policy DTO or draft satisfies it. */
export interface AdmissionAnchors {
  allowed_identities?: string[]
  allowed_issuers?: string[]
  trusted_keys?: string[]
  trusted_roots?: string[]
}

/** The derived trust mode of a policy (computed from field presence, for display). */
export type AdmissionMode = 'keyless' | 'certificate-pki' | 'bare-key' | 'empty'

/** The signing method(s) a policy configures are DERIVED from field presence, never
 *  authored (there is no method allowlist). A policy can enable MORE THAN ONE mechanism
 *  at once (e.g. certificate roots AND bare keys) — every active method is returned so
 *  the UI never hides a configured trust path behind a single precedence-picked label.
 *  Empty = admits nothing (deny-closed). */
export function deriveAdmissionModes(p: AdmissionAnchors): AdmissionMode[] {
  const roots = (p.trusted_roots ?? []).length > 0
  const keys = (p.trusted_keys ?? []).length > 0
  const identity =
    (p.allowed_identities ?? []).length > 0 ||
    (p.allowed_issuers ?? []).length > 0
  const modes: AdmissionMode[] = []
  // Roots are pinned to identities/issuers → keyless; roots alone → chain-only PKI.
  if (roots) modes.push(identity ? 'keyless' : 'certificate-pki')
  if (keys) modes.push('bare-key')
  if (modes.length === 0) modes.push('empty')
  return modes
}

/** One entry per non-blank line, trimmed. For identity/issuer lists. */
export function splitLines(text: string): string[] {
  return text
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
}

const PEM_BLOCK = /-----BEGIN [^-]+-----[\s\S]*?-----END [^-]+-----/g

/** Extract full PEM blocks (BEGIN…END inclusive). A non-empty textarea with no block is
 *  passed through as a single entry so the engine — the parsing authority — decides. */
export function splitPemBlocks(text: string): string[] {
  const blocks = text.match(PEM_BLOCK)
  if (blocks && blocks.length > 0) return blocks.map((b) => b.trim())
  return text.trim() ? [text.trim()] : []
}

/** True when the textarea contains complete PEM block(s) AND leftover non-whitespace
 *  outside them (e.g. a second, truncated block). splitPemBlocks would silently drop
 *  that residue, so we flag it as an input error rather than saving a partial anchor set. */
export function pemHasResidual(text: string): boolean {
  const blocks = text.match(PEM_BLOCK)
  if (!blocks || blocks.length === 0) return false
  let residual = text
  for (const b of blocks) residual = residual.replace(b, '')
  return residual.trim() !== ''
}

/** What the operator typed, before it becomes a write body. `rootsText`/`keysText` are the
 *  RAW textareas on purpose: the private-key and residual checks read text the parsed
 *  lists no longer carry. */
export interface AdmissionDraft extends AdmissionAnchors {
  require_signed: boolean
  rootsText: string
  keysText: string
  allowed_predicates?: string[]
}

/** The engine's 400s, in the engine's own precedence. Returns a STABLE key suffix — each
 *  surface maps it to its own i18n namespace — or null when the draft is postable.
 *
 *  ⛔ This is a MIRROR, not the authority: it exists to surface the error before a
 *  round-trip, and the engine re-checks every one of these. Never relax a rule here to
 *  make a form easier; that just moves the rejection later. */
export type AdmissionDraftError =
  | 'privateKey'
  | 'pemResidual'
  | 'emptyPredicate'
  | 'anchorMissing'
  | 'identityIssuer'

export function admissionDraftError(
  d: AdmissionDraft,
): AdmissionDraftError | null {
  // 1-2 · a private key pasted into a PUBLIC anchor field. Read from the raw text so a
  // block the splitter dropped is still caught.
  if (/PRIVATE KEY/.test(d.rootsText) || /PRIVATE KEY/.test(d.keysText)) {
    return 'privateKey'
  }
  if (pemHasResidual(d.rootsText) || pemHasResidual(d.keysText)) {
    return 'pemResidual'
  }
  // 3 · catalog only; models never sends predicates, so this never fires there.
  if ((d.allowed_predicates ?? []).some((p) => p.trim() === '')) {
    return 'emptyPredicate'
  }
  // 4 · enforcement with no anchor admits NOTHING — a deny-closed gate by accident.
  if (
    d.require_signed &&
    (d.trusted_roots ?? []).length === 0 &&
    (d.trusted_keys ?? []).length === 0
  ) {
    return 'anchorMissing'
  }
  // 5 · cosign-style keyless needs BOTH halves; one alone verifies nothing.
  if (
    (d.allowed_identities ?? []).length > 0 !==
    (d.allowed_issuers ?? []).length > 0
  ) {
    return 'identityIssuer'
  }
  return null
}
