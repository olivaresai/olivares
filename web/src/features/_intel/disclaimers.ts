// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE DEFECT THIS CLOSES, and it is not where it first appeared to be.
//
// The Compliance console renders, at the top of its front page, an English sentence
// under Spanish copy: "Technical control-status mapping derived from observed
// platform evidence. NOT a certification and NOT legal advice."
//
// It looked like a hardcoded literal in web/src/features/compliance/fixtures.ts. It
// is not: that file is a TEST MIRROR and says so. The string is
// modules/compliance/report.go reportDisclaimer — a BACKEND constant stamped on every
// reporting response, which the console renders verbatim (DisclaimerNote, 20 call
// sites). Putting a translated key in a catalog and stopping there would have changed
// nothing a user sees: the console would still paint whatever the server sent.
//
// Why the console translates it rather than the engine:
//
//   - The engine has NO internationalisation at all. Measured 2026-08-08: zero Go
//     files read Accept-Language, and there is no message catalog. Translating there
//     means building that plumbing AND rendering sixteen legal paragraphs into six
//     languages — which our own rules route to a Codex sol-max pass, not to a
//     session's spare hour. That work is declared, not silently skipped.
//   - The reader's language is a console concern. The console already resolves it.
//
// What this must NOT become is the console INVENTING legal text. The rule the
// compliance views state (components.tsx: "the framework's OWN disclaimer from the
// engine, never invented") is preserved by three constraints:
//
//   1. Matching is EXACT against the engine's canonical English. Anything the engine
//      sends that is not byte-identical to a string below renders untouched. A
//      composed disclaimer (the OSCAL export appends its own sentence to this one) is
//      therefore NOT half-translated: it stays whole, in English.
//   2. scripts/check-i18n-disclaimers.mjs fails if a canonical string here stops
//      appearing verbatim in modules/compliance, so an edit to the Go wording cannot
//      leave a translation quietly claiming to be that text.
//   3. The same gate counts every engine disclaimer that has NO entry here and fails
//      when that number grows, so the residue is a number we publish rather than a
//      thing we forgot.

/**
 * KNOWN_DISCLAIMERS maps an engine disclaimer's canonical English, exactly as
 * modules/compliance emits it, to its key in the `intel` namespace under
 * `disclaimers.`.
 *
 * Adding an entry is a two-file change plus seven catalogs, and the gate enforces
 * both halves. Do not add one whose translation has not been reviewed for POLARITY:
 * every sentence here exists to say what the product is NOT.
 */
export const KNOWN_DISCLAIMERS: Readonly<Record<string, string>> = {
  'Technical control-status mapping derived from observed platform evidence. NOT a certification and NOT legal advice.':
    'report',
}

/** The catalog key for an engine disclaimer, or null when we do not know it. */
export function disclaimerKey(text: string | undefined): string | null {
  if (!text) return null
  return KNOWN_DISCLAIMERS[text.trim()] ?? null
}
