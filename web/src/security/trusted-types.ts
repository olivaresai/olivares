// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import DOMPurify from 'dompurify'

/**
 * Trusted Types policies for the control-plane SPA (ADM-CORE-05). The CSP served by
 * cmd/olivares/webui.go carries `require-trusted-types-for 'script'` +
 * `trusted-types olivares-html default dompurify`, so every DOM injection sink
 * (innerHTML, script.src, DOMParser.parseFromString, …) must receive a Trusted*
 * value or be coerced by a named policy.
 *
 * The third name in that list is DOMPurify's OWN pass-through policy, which it
 * needs for the internal parse it performs while sanitising. Omit it and the net
 * below stops sanitising and starts returning "" — silently. The causal chain is
 * written out in buildCSP; `checkSafetyNet()` is what makes the failure audible.
 *
 * Two policies of ours, both DOMPurify-backed:
 *  - `olivares-html` — the NAMED policy for any DELIBERATE HTML sink in our code
 *    (there are currently none — the React tree has zero dangerouslySetInnerHTML —
 *    but Monaco/markdown views may add one; they must route through this).
 *  - `default` — the SAFETY NET the browser invokes automatically when a STRAY
 *    string reaches a sink from anywhere (a dependency, a polyfill). It sanitises
 *    via DOMPurify rather than letting the page hard-fail, and refuses script URLs
 *    / `createScript` (string-to-code) outright so an injection surfaces as a thrown
 *    error, never a silent execution.
 *
 * `createHTML` returns a plain STRING (RETURN_TRUSTED_TYPE:false) — the TT runtime
 * wraps it into TrustedHTML itself; returning a TrustedHTML here is the circular
 * case DOMPurify guards against (cure53/DOMPurify#939).
 */

const sanitize = (input: string): string =>
  DOMPurify.sanitize(input, {
    RETURN_TRUSTED_TYPE: false,
    // Defence in depth: never let a sanitised payload re-introduce a <style> or an
    // event-handler attribute (DOMPurify already strips scripts).
    FORBID_TAGS: ['style'],
    FORBID_ATTR: ['style'],
  })

const sameOriginScriptURL = (url: string): string => {
  const u = new URL(url, location.origin)
  if (u.origin === location.origin) return url
  throw new TypeError(`Blocked cross-origin script URL: ${url}`)
}

const refuseScript = (): string => {
  throw new TypeError(
    'Blocked string-to-script (createScript) by Trusted Types',
  )
}

/** A narrow view of the Trusted Types API (lib.dom typings vary across TS versions
 *  / Firefox lacks it entirely), so we feature-detect and cast rather than depend on
 *  the ambient `Window['trustedTypes']`. */
interface TrustedTypePolicyOptions {
  createHTML?: (input: string) => string
  createScriptURL?: (input: string) => string
  createScript?: (input: string) => string
}
interface TrustedTypesApi {
  createPolicy(
    name: string,
    options: TrustedTypePolicyOptions,
  ): { createHTML: (input: string) => unknown }
}

function getTrustedTypes(): TrustedTypesApi | undefined {
  return (window as unknown as { trustedTypes?: TrustedTypesApi }).trustedTypes
}

/** The named policy for deliberate HTML sinks. Lazily created, reusable. */
let named: { createHTML: (s: string) => unknown } | null = null

/** Sanitise + wrap HTML for a deliberate sink via the named `olivares-html` policy.
 *  Returns a TrustedHTML where supported, else the sanitised string. */
export function trustedHTML(input: string): unknown {
  if (named) return named.createHTML(input)
  return sanitize(input)
}

/** The three answers the safety net can give about itself, in the project's own
 *  vocabulary: it works / it is broken / it could not be looked at. */
export type SafetyNetVerdict = 'sanitising' | 'blanking' | 'unsupported'

/** A payload whose two halves discriminate in OPPOSITE directions: the `<b>` must
 *  survive (proves the net still passes safe markup through) and the handler must
 *  not (proves it still strips dangerous markup). A net that blanks everything
 *  passes the second half and fails the first — which is exactly the failure this
 *  check exists to catch. */
const PROBE = '<b>ok</b><img src=x onerror="alert(1)">'

/** Exercise the net once and report which of the three answers is true.
 *
 *  This is not paranoia: with `dompurify` missing from the CSP `trusted-types`
 *  allow-list, DOMPurify cannot mint a TrustedHTML for its own internal parse,
 *  falls back to a raw string, re-enters THIS policy, recurses, and swallows the
 *  overflow in its own `catch (_) {}` — so `sanitize()` returns "" and every HTML
 *  sink in the app is silently emptied. Nothing throws and nothing is logged by
 *  the library beyond a warning, so without this probe the failure is invisible.
 *  Calling it also PRIMES DOMPurify: its policy is created lazily on first use,
 *  and claiming the name here — at install, before any lazily-loaded chunk runs —
 *  is what keeps a later-loading module from claiming it first. */
export function checkSafetyNet(): SafetyNetVerdict {
  if (typeof window === 'undefined') return 'unsupported'
  const out = sanitize(PROBE)
  if (!out.includes('<b>ok</b>')) return 'blanking'
  if (out.includes('onerror')) return 'blanking'
  return 'sanitising'
}

/** Install the Trusted Types policies as early as possible (before any third-party
 *  chunk can touch a sink). No-ops where Trusted Types is unsupported (Safari <17.4,
 *  Firefox without the flag) — the CSP still hardens script-src there. */
export function installTrustedTypes(): void {
  if (typeof window === 'undefined') return
  const tt = getTrustedTypes()
  if (!tt?.createPolicy) return

  // The browser-invoked safety net. Must be named exactly `default`.
  tt.createPolicy('default', {
    createHTML: sanitize,
    createScriptURL: sameOriginScriptURL,
    createScript: refuseScript,
  })

  // The explicit policy our own code uses when it must inject HTML.
  named = tt.createPolicy('olivares-html', {
    createHTML: sanitize,
    createScriptURL: sameOriginScriptURL,
    createScript: refuseScript,
  })

  // Prime DOMPurify and verify the net actually sanitises. A wrong CSP would
  // otherwise degrade this from "sanitises" to "erases" with no symptom at all;
  // in a security product that has to be loud, so it is reported and never hidden.
  if (checkSafetyNet() === 'blanking') {
    console.error(
      'Trusted Types safety net is ERASING markup instead of sanitising it. ' +
        "The CSP `trusted-types` allow-list must include `dompurify`; see cmd/olivares/webui.go's buildCSP.",
    )
  }
}
