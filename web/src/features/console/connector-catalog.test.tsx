// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the three base providers (openai, gemini, local) have been composed into the
// binary and served by GET /v1/console/connectors all along; what was missing was a
// screen that ENUMERATES them. Measured in Chromium against a live engine on
// 2026-08-09: the catalog was rendered only inside the AAL3-gated add dialog, so a
// password-only operator clicking "Add connector" got a hardware-key wall and never saw
// the list.
//
// THE STEP-UP MOCK HERE IS THE OPPOSITE OF THE ONE IN connectors-tab.test.tsx, and that
// is the point. That file mocks RequireAssurance to render its children, which makes
// the AAL3 gate invisible to it — so it could not have caught this and cannot guard the
// fix. Here RequireAssurance renders a WALL instead, reproducing the sub-AAL3 session,
// and the catalog must still be readable.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    listConnectors: vi.fn(),
    listSources: vi.fn(),
    putConnector: vi.fn(),
    testConnector: vi.fn(),
    deleteConnector: vi.fn(),
    reloadRuntime: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'owner' as string | null,
    isSuperadmin: true,
    // AAL1: password only. This is the session the wall exists for.
    principal: { aal: 1 } as { aal?: number } | null,
    can: (_p: string) => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  // The WALL: below AAL3 the guarded subtree is replaced, never rendered.
  RequireAssurance: () => <div>step-up required</div>,
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  Toaster: () => null,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { ConnectorsTab } from './connectors-tab'

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

// The five kinds an operator actually confuses, with the hosting the ENGINE derives
// (cmd/olivares/connectoronboard.go hostingFromFields, measured against all 104 kinds).
const catalog = {
  connectors: [
    {
      kind: 'openai',
      title: 'OpenAI',
      description: 'Reads OpenAI usage/cost and the model catalog.',
      transport: 'in_process',
      fields_known: true,
      hosting: 'vendor_hosted',
      fields: [
        { key: 'api_key', type: 'string', required: true, secret: true },
      ],
    },
    {
      kind: 'gemini',
      title: 'Gemini API',
      description: 'Reads the Gemini (Google) model catalog.',
      transport: 'in_process',
      fields_known: true,
      hosting: 'vendor_hosted',
      fields: [
        { key: 'api_key', type: 'string', required: true, secret: true },
      ],
    },
    {
      kind: 'gemini-cli',
      title: 'Gemini CLI (governance)',
      description:
        "Governs Google's Gemini CLI agent from its settings.json layers.",
      transport: 'in_process',
      fields_known: true,
      hosting: 'unknown',
      fields: [],
    },
    {
      kind: 'vertex',
      title: 'Gemini Enterprise Agent Platform (formerly Vertex AI)',
      description: 'Read-only Gemini Enterprise Agent Platform.',
      transport: 'in_process',
      fields_known: true,
      hosting: 'vendor_hosted',
      fields: [],
    },
    {
      kind: 'local',
      title: 'Local inference (Ollama + vLLM)',
      description: 'Reads local model catalogs (Ollama, vLLM).',
      transport: 'in_process',
      fields_known: true,
      hosting: 'self_hosted',
      fields: [
        { key: 'ollama_url', type: 'string', required: false, secret: false },
      ],
    },
  ],
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.isSuperadmin = true
  api.listConnectors.mockResolvedValue(catalog)
  api.listSources.mockResolvedValue({ sources: [] })
})

describe('connector catalog — what this build supports, readable without a step-up', () => {
  it('lists the three base providers even though the step-up wall is closed', async () => {
    wrap(<ConnectorsTab />)

    // The wall IS up — proving the assertions below are made from a sub-AAL3 session
    // and not from an accidentally-open one. Without this control the test would pass
    // just as well against the pre console with a permissive mock.
    await screen.findByText(/supported connectors/i)
    await userEvent.click(
      screen.getByRole('button', { name: /add connector/i }),
    )
    expect(await screen.findByText('step-up required')).toBeInTheDocument()

    // …and the catalog is readable anyway.
    expect(screen.getByText('OpenAI')).toBeInTheDocument()
    expect(screen.getByText('Gemini API')).toBeInTheDocument()
    expect(
      screen.getByText('Local inference (Ollama + vLLM)'),
    ).toBeInTheDocument()
  })

  it('presents local as SELF-HOSTED and the hosted providers as vendor cloud', async () => {
    wrap(<ConnectorsTab />)
    const local = (
      await screen.findByText('Local inference (Ollama + vLLM)')
    ).closest('li')!
    expect(within(local).getByText(/self-hosted/i)).toBeInTheDocument()
    // The direction that matters: it must NOT read as the vendor's cloud.
    expect(within(local).queryByText(/vendor cloud/i)).not.toBeInTheDocument()

    const openai = screen.getByText('OpenAI').closest('li')!
    expect(within(openai).getByText(/vendor cloud/i)).toBeInTheDocument()
    expect(within(openai).queryByText(/self-hosted/i)).not.toBeInTheDocument()
  })

  it('never renders an undeclared hosting as vendor cloud', async () => {
    wrap(<ConnectorsTab />)
    const cli = (await screen.findByText('Gemini CLI (governance)')).closest(
      'li',
    )!
    // `unknown` is the engine's honest third answer. Painting it as "vendor cloud"
    // — or as nothing, which a reader beside vendor rows takes for the same thing —
    // is the failure this asserts against.
    expect(within(cli).getByText(/not declared/i)).toBeInTheDocument()
    expect(within(cli).queryByText(/vendor cloud/i)).not.toBeInTheDocument()
    expect(within(cli).queryByText(/self-hosted/i)).not.toBeInTheDocument()
  })

  it('keeps gemini, gemini-cli and vertex as three distinct, labelled rows', async () => {
    wrap(<ConnectorsTab />)
    await screen.findByText('Gemini API')

    // Searching "gemini" must surface all THREE — the API, the CLI governance
    // connector and the Vertex platform. A picker that showed one of them (or that
    // matched `\bgemini\b` and swallowed `gemini-cli`) is the confusion this guards.
    await userEvent.type(
      screen.getByRole('textbox', { name: /filter by name or kind/i }),
      'gemini',
    )
    expect(screen.getByText('Gemini API')).toBeInTheDocument()
    expect(screen.getByText('Gemini CLI (governance)')).toBeInTheDocument()
    expect(
      screen.getByText('Gemini Enterprise Agent Platform (formerly Vertex AI)'),
    ).toBeInTheDocument()
    // openai is not a gemini and must have dropped out — otherwise the filter is
    // inert and the three hits above prove nothing.
    expect(screen.queryByText('OpenAI')).not.toBeInTheDocument()

    // Each row carries its KIND, which is what disambiguates three similar titles.
    expect(screen.getByText('gemini')).toBeInTheDocument()
    expect(screen.getByText('gemini-cli')).toBeInTheDocument()
    expect(screen.getByText('vertex')).toBeInTheDocument()
  })
})
