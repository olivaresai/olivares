// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE UNCONFIGURED CALLOUT TOLD A CONSOLE USER TO SET AN ENVIRONMENT VARIABLE.
//
// "An operator enables it by setting OLIVARES_PIV_CONFIG (agency CA,
// cert-to-role map) on the engine" was the primary — and only — thing the card
// offered to do about a smartcard that does not work. It is an engine internal:
// the person reading it in a browser cannot act on it, and the administrator who
// can does not learn the configuration surface from a status card. What the card
// owes its reader is who has to act and where it is written down.
//
// WHAT THIS PINS:
//   · no engine internal is printed as the primary action, in ANY locale;
//   · the seven locales stay complete in the existing scheme, and each says an
//     administrator has to configure it;
//   · the guide the card points at is a page that EXISTS IN THIS REPOSITORY's
//     docs tree, not a URL invented for the copy;
//   · and the distinction the correction must not damage: a typed 501 is "not
//     configured", while a transport failure is a FAILED READ and must never be
//     painted as a configuration state.
import { existsSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const authState = vi.hoisted(() => ({
  activeTenant: null as string | null,
  principal: {
    aal: 3,
    amr: ['webauthn'],
    kind: 'user',
    user_id: 'u1',
    actor: 'u1',
    authentication_configuration: { piv_configured: false },
  } as Record<string, unknown> | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  pivStatus: vi.fn(),
  webauthnCredentials: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, identityApi: { ...(real.identityApi as object), ...api } }
})

import { PrivilegedLoginTab } from './privileged-login'
import { ApiError } from '@/lib/api/errors'
import { en, es, fr, de, ja, ru, zh } from './i18n'

const LOCALES = { en, es, fr, de, ja, ru, zh }
const PIV_SETUP_GUIDE = 'https://docs.olivares.ai/reference/configuration/'

function wrap() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <PrivilegedLoginTab />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.principal = {
    aal: 3,
    amr: ['webauthn'],
    kind: 'user',
    user_id: 'u1',
    actor: 'u1',
    authentication_configuration: { piv_configured: false },
  }
  api.webauthnCredentials.mockResolvedValue({ items: [] })
  api.pivStatus.mockRejectedValue(
    new ApiError(501, 'piv_not_configured', 'piv not configured'),
  )
})

describe('the PIV callout when the deployment has no PIV', () => {
  it('explains who has to act, and points at the guide that says how', async () => {
    wrap()
    expect(
      await screen.findByText(en.login.pivNotConfigured),
    ).toBeInTheDocument()
    const link = screen.getByRole('link', { name: en.login.pivSetupGuide })
    expect(link).toHaveAttribute('href', PIV_SETUP_GUIDE)
  })

  it('prints no engine internal as the thing to do about it', () => {
    // The withdrawn sentence by its shape, not by its one instance: any
    // OLIVARES_* variable name in this copy is the same defect returning.
    for (const [code, bundle] of Object.entries(LOCALES)) {
      expect(
        bundle.login.pivNotConfigured,
        `${code} names an engine internal`,
      ).not.toMatch(/OLIVARES_[A-Z0-9_]+/)
    }
  })

  it('is a state, not a failed read: a transport failure keeps its own answer', async () => {
    // The predicate that separates them is the whole of what must survive this
    // correction. A 503 is "I could not look", and a console that answered "not
    // configured" to it would be inventing a deployment fact from a broken
    // request.
    authState.principal = {
      aal: 3,
      amr: ['webauthn'],
      kind: 'user',
      user_id: 'u1',
      actor: 'u1',
    }
    api.pivStatus.mockRejectedValue(
      new ApiError(503, 'service_unavailable', 'unavailable'),
    )
    wrap()
    await waitFor(() => expect(api.pivStatus).toHaveBeenCalled())
    expect(screen.queryByText(en.login.pivNotConfigured)).toBeNull()
    expect(
      screen.queryByRole('link', { name: en.login.pivSetupGuide }),
    ).toBeNull()
  })
})

describe('the copy itself', () => {
  it('stays complete in all seven locales', () => {
    for (const [code, bundle] of Object.entries(LOCALES)) {
      expect(bundle.login.pivNotConfigured, code).toBeTruthy()
      expect(bundle.login.pivSetupGuide, code).toBeTruthy()
      // Every locale must be its own words, not the English left in place.
      if (code !== 'en') {
        expect(bundle.login.pivSetupGuide, code).not.toBe(
          en.login.pivSetupGuide,
        )
      }
      expect(bundle.login.pivNotConfigured, code).toMatch(/PIV/)
    }
  })

  it('names a page that exists in this repository docs tree', () => {
    // The same guarantee registry-help.test.ts makes for helpHref, and the same
    // limit: this proves the page EXISTS IN THE TREE. It makes no request and
    // is not evidence that the deployed site serves it.
    const docs = resolve(
      __dirname,
      '..',
      '..',
      '..',
      '..',
      'docs-site',
      'src',
      'content',
      'docs',
    )
    expect(existsSync(docs)).toBe(true)
    const url = new URL(PIV_SETUP_GUIDE)
    expect(url.protocol).toBe('https:')
    const slug = url.pathname.replace(/^\/|\/$/g, '')
    expect(
      existsSync(join(docs, `${slug}.md`)) ||
        existsSync(join(docs, `${slug}.mdx`)) ||
        existsSync(join(docs, slug, 'index.md')),
      `${slug} is not a page in docs-site`,
    ).toBe(true)
  })
})
