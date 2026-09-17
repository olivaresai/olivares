// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  HANDOFF_REFERENCE_FIELD_MAX_BYTES,
  type HandoffArtifactRef,
} from './types'

/**
 * ONE typed reference as the operator edits it: three plain strings, so a half-typed
 * row is a half-typed row and not a malformed wire object. The kind and the ref are
 * required, the hash is optional, and NOTHING here follows, fetches or resolves any
 * of them — a reference is a label the recipient reads, exactly as in `ContentView`.
 *
 * These live apart from the editor component on purpose: the offer and the rejection
 * both collect references, and the collector is where the ENGINE's rule is mirrored,
 * which is a thing to test directly rather than through a form.
 */
export interface ReferenceDraft {
  id: string
  kind: string
  ref: string
  hash: string
}

let referenceSeq = 0
export const newReferenceDraft = (): ReferenceDraft => ({
  id: `r${++referenceSeq}`,
  kind: '',
  ref: '',
  hash: '',
})

const bytes = (value: string) => new TextEncoder().encode(value).length

/** `validateOpaqueRef` (modules/sessions/communication_state.go) as the form can
 * check it: 1..512 bytes, already trimmed, and free of NUL, CR and LF. */
export function isOpaqueRef(value: string): boolean {
  const size = bytes(value)
  return (
    size >= 1 &&
    size <= HANDOFF_REFERENCE_FIELD_MAX_BYTES &&
    value.trim() === value &&
    !/[\0\r\n]/.test(value)
  )
}

/**
 * The drafted rows as the wire wants them, and whether any row is unusable.
 *
 * An ENTIRELY empty row is DROPPED: adding a row and changing one's mind is not an
 * error. A PARTIALLY filled row is an ERROR, and that asymmetry is the whole point —
 * silently dropping a half-typed reference would send an offer the operator believes
 * carries something it does not.
 */
export function collectReferences(drafts: readonly ReferenceDraft[]): {
  refs: HandoffArtifactRef[]
  invalid: boolean
} {
  const refs: HandoffArtifactRef[] = []
  let invalid = false
  for (const draft of drafts) {
    const kind = draft.kind.trim()
    const ref = draft.ref.trim()
    const hash = draft.hash.trim()
    if (kind === '' && ref === '' && hash === '') continue
    if (!isOpaqueRef(kind) || !isOpaqueRef(ref)) {
      invalid = true
      continue
    }
    if (hash !== '' && !isOpaqueRef(hash)) {
      invalid = true
      continue
    }
    refs.push(hash === '' ? { kind, ref } : { kind, ref, hash })
  }
  return { refs, invalid }
}
