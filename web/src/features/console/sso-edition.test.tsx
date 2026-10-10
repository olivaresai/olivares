// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The public console carries the single-provider SSO form only (docs/editions.md: no paid
// UI in the public tree, not even hidden). The provider selector, require-SSO, the network
// allow-list, group mapping and home-realm domains belong to the Business console, which
// replaces this tab through PANEL_EXTENSIONS.ssoTab.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import type { ComponentType, ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, panels, auth } = vi.hoisted(() => ({
  api: {
    getSSO: vi.fn(),
    listIdPs: vi.fn(),
    putSSO: vi.fn(),
    deleteSSO: vi.fn(),
    testSSO: vi.fn(),
  },
  panels: { ssoTab: undefined as ComponentType | undefined },
  auth: { isSuperadmin: true },
}))

vi.mock('@/features/extensions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/extensions')>()
  return {
    ...actual,
    PANEL_EXTENSIONS: {
      ...actual.PANEL_EXTENSIONS,
      get ssoTab() {
        return panels.ssoTab
      },
    },
  }
})
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    activeRole: 'owner',
    isSuperadmin: auth.isSuperadmin,
    principal: { aal: 3 },
    can: () => true,
  }),
}))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: { ...actual.consoleApi, ...api } }
})

import { SSOTab } from './sso-tab'

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

// The strongest server answer: every signal says the engine serves the paid capability
// and a posture is stored. The public console still offers none of it.
const stored = {
  configured: true,
  provider_available: true,
  protocol: 'oidc',
  status: 'active',
  redirect_uri: 'https://panel.example/cb',
  oidc_issuer: 'https://idp.example',
  oidc_client_id: 'cid',
  oidc_groups_claim: 'groups',
  require_sso: true,
  network_allowlist: ['10.0.0.0/8'],
  enforced_by: 'enterprise',
  groups_mapped_by: 'enterprise',
  scim_authoritative: false,
  claimed_domains: ['corp.example'],
  routed_by: 'enterprise',
}

beforeEach(() => {
  vi.clearAllMocks()
  panels.ssoTab = undefined
  auth.isSuperadmin = true
  api.getSSO.mockResolvedValue(stored)
  api.listIdPs.mockResolvedValue({ idps: [stored] })
  api.putSSO.mockResolvedValue(stored)
})

const BUSINESS_LABELS = [
  /^scope/i,
  /^identity provider$/i,
  /require sso/i,
  /network allow-list/i,
  /groups claim/i,
  /groups attribute/i,
  /claimed email domains/i,
]

describe('public SSO tab: one provider, no Business controls', () => {
  it('offers no provider choice, posture, group mapping or domains, even when the server serves them', async () => {
    const user = userEvent.setup()
    wrap(<SSOTab />)
    await user.click(
      await screen.findByRole('button', { name: /edit configuration/i }),
    )
    expect(await screen.findByLabelText(/issuer url/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/scim is authoritative/i)).toBeInTheDocument()
    for (const label of BUSINESS_LABELS) {
      expect(screen.queryByLabelText(label), String(label)).toBeNull()
    }
    expect(screen.queryByText(/login enforcement/i)).toBeNull()
    expect(screen.queryByText(/add an identity provider/i)).toBeNull()
    expect(screen.queryByText(/enforced by this build/i)).toBeNull()
    expect(api.listIdPs).not.toHaveBeenCalled()
  })

  it('a Business build replaces the whole tab through PANEL_EXTENSIONS.ssoTab', async () => {
    panels.ssoTab = () => <p>Business SSO</p>
    wrap(<SSOTab />)
    expect(await screen.findByText('Business SSO')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /edit configuration/i }),
    ).toBeNull()
    expect(api.getSSO).not.toHaveBeenCalled()
  })

  it('keeps the superadmin gate in front of a Business replacement', async () => {
    auth.isSuperadmin = false
    panels.ssoTab = () => <p>Business SSO</p>
    wrap(<SSOTab />)
    expect(await screen.findByText(/only a superadmin/i)).toBeInTheDocument()
    expect(screen.queryByText('Business SSO')).toBeNull()
    expect(api.getSSO).not.toHaveBeenCalled()
  })
})

// The build check: the public console source and its words carry none of the Business
// SSO UI. It reads every non-test source file under web/src, so moving the UI to another
// public file fails here as well. The generated API types (*.gen.ts) are skipped: they
// describe the engine's published API, which keeps every field.
const SRC = resolve(__dirname, '../..')
const BUSINESS_MARKERS = [
  'sso.scope.',
  'sso.idp.',
  'sso.enforcement.',
  'sso.domains.',
  'sso.groupsClaim',
  'sso.groupsAttr',
  'sso.groups.title',
  'sso.groups.caption',
  'routed_by',
  'groups_mapped_by',
]

function sources(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (statSync(full).isDirectory()) sources(full, out)
    else if (/\.tsx?$/.test(name) && !/\.(test|gen)\.tsx?$/.test(name))
      out.push(full)
  }
  return out
}

describe('public console build: no Business SSO UI', () => {
  it('no public source names a Business SSO control or signal', () => {
    const files = sources(SRC)
    // A scan that read nothing would pass by vacuity.
    expect(files.some((f) => f.endsWith('sso-tab.tsx'))).toBe(true)
    const found = files.flatMap((f) => {
      const text = readFileSync(f, 'utf8')
      return BUSINESS_MARKERS.filter((m) => text.includes(m)).map(
        (m) => `${relative(SRC, f)}: ${m}`,
      )
    })
    expect(found).toEqual([])
  })

  it('the public console words carry no Business SSO string, in any language', () => {
    const dir = join(__dirname, 'i18n')
    const locales = readdirSync(dir).filter((f) => f.endsWith('.json'))
    expect(locales).toHaveLength(7)
    const found = locales.flatMap((file) => {
      const words = JSON.parse(readFileSync(join(dir, file), 'utf8')) as {
        sso: Record<string, unknown>
      }
      const sso = words.sso
      const groups = (sso.groups ?? {}) as Record<string, unknown>
      return [
        'scope' in sso && 'scope',
        'idp' in sso && 'idp',
        'enforcement' in sso && 'enforcement',
        'domains' in sso && 'domains',
        'groupsClaim' in sso && 'groupsClaim',
        'groupsAttr' in sso && 'groupsAttr',
        'title' in groups && 'groups.title',
        'caption' in groups && 'groups.caption',
      ]
        .filter(Boolean)
        .map((key) => `${file}: sso.${key}`)
    })
    expect(found).toEqual([])
  })
})
