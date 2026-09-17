// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CONSOLE AND THE ENGINE MUST AGREE ABOUT WHAT A HOST IS, and the way this
// suite guarantees that is to drive the console's classifier from the table the
// Go package EMITS rather than from a second opinion written here. Change the Go
// classification without regenerating and the Go side goes red; change it and
// regenerate without touching the TypeScript and this file goes red.
//
// The second suite in this file is a DIFFERENTIAL test against the WHATWG URL
// implementation installed in this repository. It measures that implementation
// live — it never replays a recorded verdict — so it cannot go stale, and every
// row where the two deliberately disagree has to carry the reason in the row.
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi, afterEach } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { isRelyingPartyUnusable } from './api'
import { en, es, fr, de, ja, ru, zh } from './i18n'
import {
  classifyConsoleHost,
  passkeyAddressProblem,
  sameDeviceLocalhostURL,
  PasskeyAddressNotice,
} from './passkey-address'

interface ClassificationRow {
  host: string
  kind: string
  ip: boolean
  loopback: boolean
  relyingParty: boolean
}

interface OriginRow {
  input: string
  accepted: boolean
  origin?: string
  host?: string
  port?: string
  kind?: string
  code?: string
  divergence?: string
  why?: string
}

// Read from disk rather than imported: `new URL(..., import.meta.url)` would make
// Vite treat the file as an asset of the web bundle, and these tables belong to
// the Go package that emits them — they are not part of the shipped console.
const goldenDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  'core',
  'webaddr',
  'testdata',
)

// The measurement itself: the WHATWG URL implementation installed in this
// repository, asked live on every run. Nothing here replays a recorded verdict,
// so this comparison cannot quietly describe a Node version nobody has.
function parseWithTheInstalledURLImplementation(input: string): URL | null {
  try {
    return new URL(input)
  } catch {
    return null
  }
}

function golden<T>(name: string): T {
  return JSON.parse(readFileSync(join(goldenDir, name), 'utf8')) as T
}

describe('the console classifies a host exactly as the engine does', () => {
  const rows = golden<ClassificationRow[]>('address-classification.json')

  it('has a table to drive it', () => {
    // Without this, an empty or unreadable golden file would make every
    // assertion below pass vacuously.
    expect(rows.length).toBeGreaterThan(10)
    expect(rows.some((r) => r.relyingParty)).toBe(true)
    expect(rows.some((r) => !r.relyingParty)).toBe(true)
    expect(rows.some((r) => r.ip)).toBe(true)
  })

  it.each(rows)(
    'agrees with the engine about $host',
    ({ host, kind, ip, loopback, relyingParty }) => {
      // The engine stores an IPv6 literal without brackets; a browser reports
      // window.location.hostname WITH them, which is the form this side sees.
      const asBrowserReportsIt = kind === 'ipv6' ? `[${host}]` : host
      const got = classifyConsoleHost(asBrowserReportsIt)
      expect(got.kind).toBe(kind)
      expect(got.ip).toBe(ip)
      expect(got.loopback).toBe(loopback)
      expect(got.relyingParty).toBe(relyingParty)
    },
  )
})

describe('the engine agrees with the installed WHATWG URL implementation', () => {
  const rows = golden<OriginRow[]>('browser-origins.json')

  it('has a corpus to drive it', () => {
    expect(rows.length).toBeGreaterThan(40)
    expect(rows.some((r) => r.accepted)).toBe(true)
    expect(rows.some((r) => !r.accepted)).toBe(true)
  })

  it.each(rows)('$input', (row) => {
    const node = parseWithTheInstalledURLImplementation(row.input)
    // PARSING and having a USABLE ORIGIN are different facts, and the corpus
    // needs both: "panel.example.com:8443" parses in a browser and yields the
    // origin `null`, which is not an address anybody can be sent to. Collapsing
    // the two would make that row read as "a browser refuses this too".
    const origin = node && node.origin !== 'null' ? node.origin : null
    if (row.divergence) {
      // The reason field is the only thing that makes a divergence legitimate,
      // so it is asserted, and so is the direction: every recorded divergence is
      // one where a browser accepts and this parser refuses. The opposite
      // direction — accepting an address a browser rejects — is never allowed.
      expect(
        row.why,
        `divergent row ${row.input} carries no reason`,
      ).toBeTruthy()
      expect(['console-origin-restriction', 'library-difference']).toContain(
        row.divergence,
      )
      expect(
        row.accepted,
        `${row.input}: recorded as divergent but accepted`,
      ).toBe(false)
      expect(
        node,
        `${row.input}: recorded as "a browser accepts this and we refuse it", but the installed implementation refuses it too — the divergence is stale`,
      ).not.toBeNull()
      return
    }
    if (!row.accepted) {
      expect(
        origin,
        `${row.input}: refused here and given a usable origin by the installed URL implementation, with no reason recorded`,
      ).toBeNull()
      return
    }
    expect(
      origin,
      `${row.input}: accepted here and refused by the installed URL implementation`,
    ).not.toBeNull()
    expect(origin).toBe(row.origin)
  })
})

describe('passkeyAddressProblem separates the three browser-side refusals', () => {
  const at = (hostname: string, protocol = 'https:', port = '8443') => ({
    hostname,
    protocol,
    port,
  })

  it('names an insecure context BEFORE a missing API', () => {
    // Browsers withhold PublicKeyCredential outside a secure context. Asking
    // "is the API there" first would tell an operator to change their browser
    // when what they have to change is the scheme.
    expect(
      passkeyAddressProblem(at('olivares.example.com', 'http:'), {
        secureContext: false,
        apiAvailable: false,
      }),
    ).toBe('insecure-context')
  })

  it('names a missing API in a secure context', () => {
    expect(
      passkeyAddressProblem(at('olivares.example.com'), {
        secureContext: true,
        apiAvailable: false,
      }),
    ).toBe('unsupported')
  })

  it.each([
    ['127.0.0.1', 'not-relying-party'],
    ['192.168.1.10', 'not-relying-party'],
    ['[::1]', 'not-relying-party'],
    ['olivares', 'not-relying-party'],
    ['my_host.example.com', 'not-relying-party'],
    ['olivares.example.com', 'none'],
    ['localhost', 'none'],
    ['panel.example.com.', 'none'],
  ])('classifies %s as %s', (hostname, want) => {
    expect(
      passkeyAddressProblem(at(hostname), {
        secureContext: true,
        apiAvailable: true,
      }),
    ).toBe(want)
  })
})

describe('the same-device alternative', () => {
  it('keeps the scheme and the port', () => {
    expect(
      sameDeviceLocalhostURL({
        hostname: '127.0.0.1',
        protocol: 'https:',
        port: '8477',
      }),
    ).toBe('https://localhost:8477')
    expect(
      sameDeviceLocalhostURL({
        hostname: '127.0.0.1',
        protocol: 'http:',
        port: '8080',
      }),
    ).toBe('http://localhost:8080')
  })

  it('is offered only for a loopback location', () => {
    // "localhost" resolves on the operator's own machine and nowhere else, so
    // offering it from a routable address would be offering a URL that does not
    // answer.
    for (const hostname of [
      '192.168.1.10',
      'olivares.example.com',
      'olivares',
    ]) {
      expect(
        sameDeviceLocalhostURL({ hostname, protocol: 'https:', port: '8443' }),
      ).toBeNull()
    }
  })

  it('is not offered when the address already works', () => {
    expect(
      sameDeviceLocalhostURL({
        hostname: 'localhost',
        protocol: 'https:',
        port: '8443',
      }),
    ).toBeNull()
  })

  it('omits the port when the location has none', () => {
    expect(
      sameDeviceLocalhostURL({
        hostname: '127.0.0.1',
        protocol: 'https:',
        port: '',
      }),
    ).toBe('https://localhost')
  })
})

describe('PasskeyAddressNotice', () => {
  const original = {
    href: window.location.href,
    secure: window.isSecureContext,
  }

  function atLocation(href: string, secureContext: boolean) {
    // jsdom's location is not writable; replacing the getter is the supported
    // way to drive a component that reads it.
    vi.spyOn(window, 'location', 'get').mockReturnValue({
      ...window.location,
      href,
    } as Location)
    Object.defineProperty(window, 'isSecureContext', {
      value: secureContext,
      configurable: true,
    })
    vi.stubGlobal('PublicKeyCredential', function () {} as unknown)
    Object.defineProperty(navigator, 'credentials', {
      value: {
        get: () => Promise.resolve(null),
        create: () => Promise.resolve(null),
      },
      configurable: true,
    })
  }

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    Object.defineProperty(window, 'isSecureContext', {
      value: original.secure,
      configurable: true,
    })
  })

  it('renders nothing at an address where a passkey works', () => {
    atLocation('https://olivares.example.com:8443/login', true)
    const { container } = render(<PasskeyAddressNotice />)
    expect(container).toBeEmptyDOMElement()
  })

  it('offers localhost on the SERVED port at a loopback IP', () => {
    atLocation('https://127.0.0.1:8477/login', true)
    render(<PasskeyAddressNotice />)
    const link = screen.getByTestId('passkey-address-localhost')
    expect(link).toHaveTextContent('https://localhost:8477')
    expect(link).toHaveAttribute('href', 'https://localhost:8477')
  })

  it('offers no alternative at a routable IP, and still says why', () => {
    atLocation('https://10.0.0.7:8443/login', true)
    render(<PasskeyAddressNotice />)
    expect(screen.queryByTestId('passkey-address-localhost')).toBeNull()
    expect(screen.getByText(/relying party/i)).toBeInTheDocument()
  })

  it('says "not a secure context" rather than "no passkey support"', () => {
    atLocation('http://olivares.example.com/login', false)
    render(<PasskeyAddressNotice />)
    expect(screen.getByText(/not a secure context/i)).toBeInTheDocument()
    expect(screen.queryByText(/no WebAuthn API/i)).toBeNull()
  })
})

// THE TYPED 503 IS A DEPLOYMENT STATE, NOT A CEREMONY OUTCOME.
//
// Rendered as the generic failure it read "Step-up did not complete", which tells
// an operator something went wrong with their click. Nothing went wrong with the
// click: no click at this address can succeed until the declared address changes.
// An independent review routed operators into exactly this case (its R4) and
// found the console had nothing to say when they arrived.
describe('a relying party the engine cannot build', () => {
  it('is classified apart from an ordinary denied ceremony', () => {
    const unusable = new ApiError(
      503,
      'webauthn_relying_party_unusable',
      'Passkeys cannot be used at the address this console was reached on.',
    )
    const denied = new ApiError(
      403,
      'webauthn_verification_failed',
      'verification failed',
    )
    expect(isRelyingPartyUnusable(unusable)).toBe(true)
    // The non-firing direction: a refused ceremony must NOT take this branch, or
    // a bad signature would be reported as a misconfiguration.
    expect(isRelyingPartyUnusable(denied)).toBe(false)
    expect(isRelyingPartyUnusable(new Error('network'))).toBe(false)
  })

  it('has a translated message in every living locale', () => {
    for (const [name, bundle] of Object.entries({
      en,
      es,
      fr,
      de,
      ja,
      ru,
      zh,
    })) {
      // Through `unknown`: the bundles are structurally typed from their JSON, so
      // a direct cast to a looser shape is a type error rather than a narrowing.
      const text = (bundle as unknown as { assurance: Record<string, string> })
        .assurance.relyingPartyUnusable
      expect(text, `${name} is missing the message`).toBeTruthy()
      expect(text.trim().length, `${name} is empty`).toBeGreaterThan(20)
    }
  })
})
