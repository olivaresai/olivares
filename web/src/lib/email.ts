// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ENGINE'S EMAIL RULE, ONCE, FOR EVERY FORM (Root, ID's account fix 2026-10-02). The
// engine accepts a sign-in identifier with core/auth ValidateEmail: normalizeEmail (trim,
// lower case), then Go's net/mail ParseAddress, keeping only a bare mailbox whose parsed
// address is the input itself (no display name, no list, no comment, no quoting). Internal
// domains (name@corp.internal, name@host) and plus-addresses are valid; delivery is never
// checked. The browser's own rule (zod `email()`) refused internal domains the engine and
// the CLI accept, so sign-in, setup and onboarding use this one instead.

/** The engine's normalization: trimmed and lower case (core/auth normalizeEmail). */
export function normalizeEmail(input: string): string {
  return input.trim().toLowerCase()
}

/** net/mail isAtext (dot handled by the caller): visible ASCII except the RFC 5322
 * specials, and any non-ASCII character. */
function isAtext(ch: string): boolean {
  const c = ch.codePointAt(0) ?? 0
  if (c >= 0x80) return true
  if (c < 0x21 || c > 0x7e) return false
  return !'()<>[]:;@\\,".'.includes(ch)
}

/** net/mail consumeAtom(dot=true): atext and dots, with no leading, trailing or double dot. */
function isDotAtom(s: string): boolean {
  if (!s || s.startsWith('.') || s.endsWith('.') || s.includes('..'))
    return false
  for (const ch of s) if (ch !== '.' && !isAtext(ch)) return false
  return true
}

/** net/mail consumeDomainLiteral: "[" an IP address "]". */
const IPV4 =
  /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/
const IPV6 = /^[0-9a-f:.]*:[0-9a-f:.]*$/

/** Whether the engine accepts `input` as a native sign-in email. */
export function isEngineEmail(input: string): boolean {
  const email = normalizeEmail(input)
  const at = email.indexOf('@')
  // The local part cannot hold an "@" (not atext, and quoting is refused), so one "@".
  if (at <= 0 || at !== email.lastIndexOf('@')) return false
  const local = email.slice(0, at)
  const domain = email.slice(at + 1)
  if (!isDotAtom(local)) return false
  if (domain.startsWith('[')) {
    if (!domain.endsWith(']')) return false
    const literal = domain.slice(1, -1)
    return IPV4.test(literal) || IPV6.test(literal)
  }
  return isDotAtom(domain)
}
