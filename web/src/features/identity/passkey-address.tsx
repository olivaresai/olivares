// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NOTICE THAT ARRIVES BEFORE THE CLICK.
//
// A passkey ceremony can be refused by the BROWSER, before any request is made:
// the API is absent, the origin is not a secure context, or the host cannot be a
// relying party. None of those refusals reaches the engine, so no server log will
// ever explain them, and the operator sees a step-up prompt that simply does not
// work. This component says which of the three it is, at the address they are
// actually on, on the screens where a passkey is about to matter.
//
// FOUR STATES, KEPT APART ON PURPOSE. "The browser has no passkey API", "this
// origin is not a secure context", "this host cannot be a relying party" and "the
// ceremony was refused" have four different remedies, and an operator handed the
// wrong one changes the wrong thing. The fourth is NOT this component's business:
// a failed assertion, a declined prompt or an expired challenge belong to the
// ceremony panel, which already reports them.
//
// WHAT IT DOES NOT DO. It never navigates and never changes the origin: the
// same-device alternative is an offer the operator may take. It never claims that
// an address IS reachable, and it never infers secure-context trust from a host
// name — window.isSecureContext is the browser's own answer about the machine in
// front of the operator, and it is the only authority this component consults for
// that question.
import { useTranslation } from 'react-i18next'
import { CaveatNotice } from '@/features/_intel'
import { isWebAuthnSupported } from './webauthn'
// Rendered from chunks that never import the identity view, so it registers the
// namespace it translates with rather than trusting its caller.
import './i18n'

/** What a browser's host parser decided a host is. Mirrors core/webaddr.Kind. */
export type HostKind = 'domain' | 'ipv4' | 'ipv6' | 'none'

export interface HostClassification {
  kind: HostKind
  ip: boolean
  loopback: boolean
  /** Can this host be the relying-party ID of a passkey for the engine's verifier? */
  relyingParty: boolean
}

/** Why a passkey cannot run at this address, or 'none'. */
export type PasskeyAddressProblem =
  'none' | 'unsupported' | 'insecure-context' | 'not-relying-party'

/** The already-normalized parts of a location this module reasons about. */
export interface ConsoleLocation {
  hostname: string
  protocol: string
  port: string
}

const IPV4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/

// classifyConsoleHost mirrors core/webaddr's classification and is driven, in the
// tests, by the table that package EMITS. That is the whole design: the engine,
// the startup panel and this notice must agree about what a host is, and the way
// to guarantee that is to check one against the other rather than to write the
// parser twice.
//
// It is deliberately NOT a URL parser. window.location.hostname has already been
// through the browser's own host parser, so every shorthand — 127.1, 0x7f000001,
// BÜCHER.example — has already been resolved to a canonical dotted quad or an
// A-label domain by the time this sees it. All that is left is to classify an
// already-canonical host, which is a decidable question a regular expression can
// answer honestly. Handing it a raw operator-typed string would NOT be.
export function classifyConsoleHost(hostname: string): HostClassification {
  const none: HostClassification = {
    kind: 'none',
    ip: false,
    loopback: false,
    relyingParty: false,
  }
  if (!hostname) return none
  if (hostname.startsWith('[') && hostname.endsWith(']')) {
    const literal = hostname.slice(1, -1).toLowerCase()
    return {
      kind: 'ipv6',
      ip: true,
      loopback: isIPv6Loopback(literal),
      relyingParty: false,
    }
  }
  const quad = IPV4.exec(hostname)
  if (quad) {
    const parts = quad.slice(1).map(Number)
    if (parts.every((n) => n <= 255)) {
      return {
        kind: 'ipv4',
        ip: true,
        loopback: parts[0] === 127,
        relyingParty: false,
      }
    }
    // A dotted group a browser would never have produced: classify it as nothing
    // rather than guessing, and let the caller treat it as unusable.
    return none
  }
  return {
    kind: 'domain',
    ip: false,
    loopback: isLocalhostFamily(hostname),
    relyingParty: isRelyingPartyDomain(hostname),
  }
}

// isIPv6Loopback covers ::1 AND the IPv4-mapped form. The mapped case is not
// hypothetical pedantry: the engine's classification unmaps before asking, so a
// console that did not would disagree with it about [::ffff:7f00:1], and the
// golden table this file's test drives would catch that — it did.
function isIPv6Loopback(literal: string): boolean {
  if (literal === '::1') return true
  const mapped =
    /^::ffff:(?:([0-9a-f]{1,4}):([0-9a-f]{1,4})|(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}))$/.exec(
      literal,
    )
  if (!mapped) return false
  if (mapped[3]) return Number(mapped[3].split('.')[0]) === 127
  return parseInt(mapped[1], 16) >> 8 === 127
}

/** The localhost NAME family, per Secure Contexts: localhost, localhost.,
 *  *.localhost and *.localhost. — used ONLY to decide whether a same-device
 *  alternative exists, never to claim that a browser trusts the origin. */
function isLocalhostFamily(hostname: string): boolean {
  const h = hostname.toLowerCase().replace(/\.$/, '')
  return h === 'localhost' || h.endsWith('.localhost')
}

// isRelyingPartyDomain mirrors core/webaddr.IsRelyingPartyDomain for a host the
// browser has already canonicalized: the URL Standard's STRICT domain (which is
// what WebAuthn means by "a valid domain") plus the rule the engine's verifier
// actually applies — a dot, or exactly "localhost".
//
// The single-label rule is the LIBRARY's, not the specification's, and the copy
// this component shows says so. Writing "a single-label name is invalid under
// WebAuthn" would send an operator to the wrong document.
function isRelyingPartyDomain(hostname: string): boolean {
  const host = hostname.toLowerCase()
  const probe = host.replace(/\.$/, '')
  // One trailing dot is the root label; a second is an empty label that is not
  // the root, and a domain may not carry one.
  if (!probe || probe.endsWith('.') || probe.length > 253) return false
  for (const label of probe.split('.')) {
    if (!label || label.length > 63) return false
    if (!/^[a-z0-9-]+$/.test(label)) return false
    if (label.startsWith('-') || label.endsWith('-')) return false
    // CheckHyphens: "--" in the third and fourth positions is reserved for the
    // punycode prefix, and a label that is not punycode may not use it.
    if (label.slice(2, 4) === '--' && !label.startsWith('xn--')) return false
  }
  // THE DOT TEST IS ON THE UNTRIMMED HOST, and that is not a detail: the
  // installed verifier's rule for a non-IP is
  // `value != "localhost" && !strings.Contains(rpid.Path, ".")`, so the ROOT DOT
  // satisfies it and "olivares." is a relying party where "olivares" is not.
  // Testing the trimmed form made this console disagree with its own engine on
  // exactly that host — found by an independent review, which the golden table
  // could not catch because it had no such row.
  return host === 'localhost' || host.includes('.')
}

/**
 * passkeyAddressProblem answers "will the browser even let a passkey ceremony
 * start here", from the three facts that decide it.
 *
 * ORDER IS LOAD-BEARING. An insecure context is reported BEFORE a missing API,
 * because browsers withhold PublicKeyCredential outside a secure context: asking
 * "is the API there" first would report "this browser cannot do passkeys" to
 * someone whose browser can, and send them to replace the browser instead of the
 * scheme.
 */
export function passkeyAddressProblem(
  loc: ConsoleLocation,
  facts: { secureContext: boolean; apiAvailable: boolean },
): PasskeyAddressProblem {
  if (!facts.secureContext) return 'insecure-context'
  if (!facts.apiAvailable) return 'unsupported'
  return classifyConsoleHost(loc.hostname).relyingParty
    ? 'none'
    : 'not-relying-party'
}

/**
 * addressCannotBeRelyingParty — can the address this console is on be a relying
 * party AT ALL? It is the one question an ACTION has to ask before it offers to
 * start a ceremony, and it is answered from classifyConsoleHost, so the console
 * withdraws exactly what the engine's verifier would refuse. There is no second
 * list of acceptable hosts anywhere in this module.
 *
 * IT CONSULTS NEITHER BROWSER FACT, and that is the decision, not an oversight:
 *
 *   · isWebAuthnSupported() is about the BROWSER. "This browser exposes no
 *     WebAuthn API" is answered by the ceremony panel when the attempt is made,
 *     and its remedy is another browser. Folding it in here would withdraw a
 *     working action from a working address because of the client in front of
 *     it, and send an operator to change their deployment instead.
 *
 *   · window.isSecureContext is the browser's own answer, and a WRONG one costs
 *     the action. Measured: jsdom reports false at http://localhost, where every
 *     real browser reports true, because localhost is potentially trustworthy.
 *     An action gated on it disappears wherever that flag is unimplemented or
 *     mis-set, at an address that works. The notice still reports an insecure
 *     context — explaining is safe, withdrawing is not.
 *
 * The host classification has neither weakness: it is decided from the host the
 * browser already canonicalized, by the table core/webaddr emits, and it is the
 * same answer in every environment. False when there is no location to read:
 * this module cannot claim an address is unusable without seeing one, and the
 * engine verifies the origin, the relying party and the step-up regardless.
 */
export function addressCannotBeRelyingParty(): boolean {
  const loc = currentLocation()
  if (!loc) return false
  return !classifyConsoleHost(loc.hostname).relyingParty
}

/**
 * sameDeviceLocalhostURL is the same console, on the same port, at a name a
 * browser will complete a ceremony against — and null when there is no honest
 * one to offer.
 *
 * Only for a LOOPBACK location: "localhost" resolves on the operator's own
 * machine and nowhere else, so offering it from a routable address would be
 * offering a URL that does not answer. The scheme and the port are carried over
 * because dropping either produces an address that is not this console.
 */
export function sameDeviceLocalhostURL(loc: ConsoleLocation): string | null {
  const host = classifyConsoleHost(loc.hostname)
  if (!host.loopback || host.kind === 'none') return null
  if (host.relyingParty) return null
  const port = loc.port ? `:${loc.port}` : ''
  return `${loc.protocol}//localhost${port}`
}

/** The URL-normalized view of where this console actually is. */
function currentLocation(): ConsoleLocation | null {
  if (typeof window === 'undefined' || !window.location) return null
  try {
    const url = new URL(window.location.href)
    return { hostname: url.hostname, protocol: url.protocol, port: url.port }
  } catch {
    return null
  }
}

/**
 * PasskeyAddressNotice renders nothing at an address where passkeys work, which
 * is why its callers mount it unconditionally. Mounting it is not a claim that
 * anything is wrong.
 */
export function PasskeyAddressNotice({
  id,
  className,
}: {
  /** Put on the text, so a control this notice explains can point at it with
   *  aria-describedby. The explanation a sighted operator reads above the
   *  button is then the same one a screen reader announces with it. */
  id?: string
  className?: string
}) {
  const { t } = useTranslation('identity')
  const loc = currentLocation()
  if (!loc) return null
  const problem = passkeyAddressProblem(loc, {
    secureContext:
      typeof window !== 'undefined' && window.isSecureContext === true,
    apiAvailable: isWebAuthnSupported(),
  })
  if (problem === 'none') return null
  const alternative =
    problem === 'not-relying-party' ? sameDeviceLocalhostURL(loc) : null
  const host = classifyConsoleHost(loc.hostname)
  const body =
    problem === 'not-relying-party' && !host.ip
      ? t('assurance.address.nameBody')
      : t(`assurance.address.${bodyKey(problem)}`)
  return (
    <CaveatNotice tone="warning" className={className}>
      <span
        id={id}
        className="inline-flex flex-wrap items-center gap-x-2 gap-y-1"
        data-testid="passkey-address-notice"
      >
        <strong className="font-medium text-foreground">
          {t(`assurance.address.${titleKey(problem)}`)}
        </strong>
        <span>{body}</span>
        {alternative && (
          <span>
            {t('assurance.address.sameDevice')}{' '}
            <a
              href={alternative}
              className="font-mono underline underline-offset-2"
              data-testid="passkey-address-localhost"
            >
              {alternative}
            </a>
          </span>
        )}
      </span>
    </CaveatNotice>
  )
}

function titleKey(problem: Exclude<PasskeyAddressProblem, 'none'>): string {
  switch (problem) {
    case 'unsupported':
      return 'unsupportedTitle'
    case 'insecure-context':
      return 'insecureTitle'
    default:
      return 'addressTitle'
  }
}

function bodyKey(problem: Exclude<PasskeyAddressProblem, 'none'>): string {
  switch (problem) {
    case 'unsupported':
      return 'unsupportedBody'
    case 'insecure-context':
      return 'insecureBody'
    default:
      return 'ipBody'
  }
}
