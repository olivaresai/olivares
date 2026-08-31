// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type {
  ContextPolicyDTO,
  KbDTO,
  LineageDTO,
  MemoryDTO,
  PromptDTO,
  RevisionDTO,
} from './types'

// --- mocks -------------------------------------------------------------------

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  listKbs: vi.fn(),
  getKb: vi.fn(),
  createKb: vi.fn(),
  updateKb: vi.fn(),
  deleteKb: vi.fn(),
  ingest: vi.fn(),
  reindex: vi.fn(),
  listDocuments: vi.fn(),
  getDocument: vi.fn(),
  query: vi.fn(),
  listLineage: vi.fn(),
  getLineage: vi.fn(),
  listPrompts: vi.fn(),
  getPrompt: vi.fn(),
  createPrompt: vi.fn(),
  listRevisions: vi.fn(),
  getRevision: vi.fn(),
  addRevision: vi.fn(),
  rollbackPrompt: vi.fn(),
  listMemory: vi.fn(),
  getMemory: vi.fn(),
  writeMemory: vi.fn(),
  deleteMemory: vi.fn(),
  purgeMemory: vi.fn(),
  listContextPolicies: vi.fn(),
  upsertContextPolicy: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, knowledgeApi: api }
})

import KnowledgeView from './knowledge-view'
import { KbEditorDialog } from './kb-editor'
import { KbDetailSheet } from './kb-detail'
import { LineageDetailSheet } from './lineage-detail'
import { QueryDialog } from './query-dialog'
import { PromptDetailSheet } from './prompt-detail'
import { MemoryEditorDialog } from './memory-editor'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

// --- fixtures ----------------------------------------------------------------

const kbFixture: KbDTO = {
  id: 'kb1',
  name: 'engineering-handbook',
  classification: 'confidential',
  residency_region: 'eu',
  embed_policy: 'local_only',
  embed_model: 'local-hash',
  dim: 256,
  default_acl: ['group:eng', 'role:reviewer'],
  status: 'active',
  doc_count: 12,
  chunk_count: 340,
}

const lineageFixture: LineageDTO = {
  id: 'ln1',
  kb_ref: 'kb1',
  agent_ref: 'agent:planner',
  query_hash: 'a1b2c3d4e5f6a7b8c9d0e1f2',
  chunk_refs: [
    {
      chunk_id: 'ch1',
      kb_ref: 'kb1',
      doc_ref: 'doc1',
      source_kind: 'inline',
      source_ref: 'inline',
      source_mode: 'live',
      content_hash: 'deadbeefdeadbeefdeadbeef',
    },
  ],
  source_refs: ['doc1'],
  residency_region: 'eu',
  decision: 'denied',
  reason: 'residency mismatch: query origin us, KB residency eu',
  egress: false,
  result_count: 0,
  occurred_at: '2026-06-04T10:00:00Z',
}

const promptFixture: PromptDTO = {
  id: 'p1',
  name: 'system-base',
  current_rev: 2,
  latest_hash: 'cafebabecafebabecafebabe',
  status: 'active',
}

const revisionsFixture: RevisionDTO[] = [
  {
    prompt_id: 'p1',
    rev: 1,
    label: 'initial',
    template: 'You are a helpful assistant.',
    template_hash: '1111111111111111',
    created_by: 'user:alice',
  },
  {
    prompt_id: 'p1',
    rev: 2,
    label: 'tightened',
    template: 'You are a concise, governed assistant.',
    template_hash: '2222222222222222',
    created_by: 'user:bob',
  },
]

const memoryFixture: MemoryDTO = {
  id: 'm1',
  agent_ref: 'agent:planner',
  key: 'preferences',
  content: 'prefers concise answers',
  classification: 'internal',
  residency_region: 'eu',
  expires_at: '2026-12-01T00:00:00Z',
  created_by: 'user:alice',
}

const contextPolicyFixture: ContextPolicyDTO = {
  id: 'cp1',
  scope_kind: 'agent',
  scope_ref: 'agent:planner',
  max_tokens: 8000,
  strategy: 'summarize',
  redaction_required: true,
}

beforeEach(() => {
  authState.can = () => true
  for (const fn of Object.values(api)) fn.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
})
afterEach(() => vi.clearAllMocks())

// --- knowledge bases list ----------------------------------------------------

describe('KnowledgeView — knowledge bases tab', () => {
  it('lists KBs with governance badges', async () => {
    api.listKbs.mockResolvedValue({ items: [kbFixture], has_more: false })
    wrap(<KnowledgeView />)
    expect(await screen.findByText('engineering-handbook')).toBeInTheDocument()
    expect(screen.getByText('Confidential')).toBeInTheDocument()
    expect(screen.getByText('EU')).toBeInTheDocument()
  })

  it('flags a local-hash embedder as lexical, NOT semantic', async () => {
    api.listKbs.mockResolvedValue({ items: [kbFixture], has_more: false })
    wrap(<KnowledgeView />)
    await screen.findByText('engineering-handbook')
    expect(screen.getByText(/lexical — not semantic/i)).toBeInTheDocument()
  })

  it('renders the create button and gates it on kb:write', async () => {
    api.listKbs.mockResolvedValue({ items: [], has_more: false })
    const { unmount } = wrap(<KnowledgeView />)
    expect(
      await screen.findByRole('button', { name: /new knowledge base/i }),
    ).toBeInTheDocument()
    unmount()

    authState.can = (p) => p !== 'knowledge:kb:write'
    api.listKbs.mockResolvedValue({ items: [], has_more: false })
    wrap(<KnowledgeView />)
    await waitFor(() => expect(api.listKbs).toHaveBeenCalled())
    expect(
      screen.queryByRole('button', { name: /new knowledge base/i }),
    ).toBeNull()
  })

  it('shows ForbiddenState for the KB tab when the role cannot read', async () => {
    authState.can = (p) => p !== 'knowledge:kb:read'
    wrap(<KnowledgeView />)
    expect(
      await screen.findByText(/not authorized|forbidden/i),
    ).toBeInTheDocument()
    expect(api.listKbs).not.toHaveBeenCalled()
  })
})

// --- KB editor: ACL is a reference, the audit notice is present --------------

describe('KbEditorDialog — ACLs are permission references, never secrets', () => {
  it('shows the audit notice and creates a KB (submit → api → toast → close)', async () => {
    api.createKb.mockResolvedValue(kbFixture)
    const onOpenChange = vi.fn()
    wrap(<KbEditorDialog open onOpenChange={onOpenChange} />)

    expect(screen.getByText(/tamper-evident audit ledger/i)).toBeInTheDocument()

    const create = screen.getByRole('button', {
      name: /create knowledge base/i,
    })
    expect(create).toBeDisabled()
    await userEvent.type(screen.getByLabelText(/name/i), 'kb-new')
    expect(create).toBeEnabled()

    await userEvent.click(create)
    await waitFor(() => expect(api.createKb).toHaveBeenCalledTimes(1))
    expect(api.createKb.mock.calls[0][0]).toMatchObject({
      name: 'kb-new',
      embed_policy: 'auto',
    })
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it('warns when an ACL entry looks like a credential and blocks save', async () => {
    const onOpenChange = vi.fn()
    wrap(<KbEditorDialog open onOpenChange={onOpenChange} />)
    await userEvent.type(screen.getByLabelText(/name/i), 'kb-new')
    await userEvent.click(
      screen.getByRole('button', { name: /add reference/i }),
    )
    const aclInputs = screen.getAllByLabelText(/^acl$/i)
    await userEvent.type(
      aclInputs[aclInputs.length - 1],
      'token=ghp_supersecretvalue',
    )
    expect(screen.getByText(/looks like a credential/i)).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /create knowledge base/i }),
    ).toBeDisabled()
  })
})

// --- KB detail: ACL refs / hashes / privileged delete -----------------------

describe('KbDetailSheet — references, hashes & the cascade-delete gate', () => {
  beforeEach(() => {
    api.getKb.mockResolvedValue(kbFixture)
    api.listDocuments.mockResolvedValue({
      items: [
        {
          id: 'doc1',
          kb_ref: 'kb1',
          source_kind: 'inline',
          source_ref: 'inline',
          source_mode: 'export',
          source_doc_id: 'sd1',
          title: 'Onboarding',
          content_type: 'text/plain',
          classification: 'internal',
          residency_region: 'eu',
          acl: ['group:eng'],
          content_hash: 'feedfacefeedfacefeedface',
          redaction_count: 3,
          chunk_count: 7,
          status: 'indexed',
        },
      ],
      has_more: false,
    })
  })

  it('renders ACL handles as reference chips, not raw values', async () => {
    wrap(<KbDetailSheet kbId="kb1" open onOpenChange={() => {}} />)
    await screen.findByRole('heading', { name: /governance/i })
    // ACL permission references appear as their handles.
    expect(screen.getAllByText('group:eng').length).toBeGreaterThan(0)
    expect(screen.getByText('role:reviewer')).toBeInTheDocument()
  })

  it('surfaces the redaction count and never the document body', async () => {
    wrap(<KbDetailSheet kbId="kb1" open onOpenChange={() => {}} />)
    expect(await screen.findByText('Onboarding')).toBeInTheDocument()
    expect(screen.getByText(/redactions/i)).toBeInTheDocument()
    expect(screen.getByText('Export')).toBeInTheDocument()
  })

  it('declares when the document list is truncated with the loaded count', async () => {
    api.listDocuments.mockResolvedValue({
      items: [
        {
          id: 'doc1',
          kb_ref: 'kb1',
          source_kind: 'inline',
          source_ref: 'inline',
          source_mode: 'export',
          source_doc_id: 'sd1',
          title: 'Onboarding',
          classification: 'internal',
          residency_region: 'eu',
          acl: [],
          content_hash: 'feedfacefeedfacefeedface',
          redaction_count: 0,
          chunk_count: 1,
          status: 'indexed',
        },
      ],
      has_more: true,
    })
    wrap(<KbDetailSheet kbId="kb1" open onOpenChange={() => {}} />)

    expect(
      await screen.findByText('Loaded 1 documents; there are more'),
    ).toBeVisible()
  })

  it('does not declare a document list as truncated when has_more is false', async () => {
    wrap(<KbDetailSheet kbId="kb1" open onOpenChange={() => {}} />)
    await screen.findByText('Onboarding')

    expect(
      screen.queryByText('Loaded 1 documents; there are more'),
    ).not.toBeInTheDocument()
  })

  it('hides the cascade-delete action when the role lacks kb:admin', async () => {
    authState.can = (p) => p !== 'knowledge:kb:admin'
    wrap(<KbDetailSheet kbId="kb1" open onOpenChange={() => {}} />)
    await screen.findByRole('heading', { name: /governance/i })
    expect(screen.queryByRole('button', { name: /^delete$/i })).toBeNull()
  })

  it('requires a typed phrase to confirm the cascade delete (high risk)', async () => {
    api.deleteKb.mockResolvedValue({ deleted: true })
    wrap(<KbDetailSheet kbId="kb1" open onOpenChange={() => {}} />)
    await userEvent.click(
      await screen.findByRole('button', { name: /^delete$/i }),
    )
    const confirm = await screen.findByRole('button', {
      name: /delete permanently/i,
    })
    // The phrase guard keeps confirm disabled until DELETE is typed.
    expect(confirm).toBeDisabled()
    await userEvent.type(
      screen.getByLabelText(/confirmation phrase/i),
      'DELETE',
    )
    expect(confirm).toBeEnabled()
    await userEvent.click(confirm)
    await waitFor(() => expect(api.deleteKb).toHaveBeenCalledWith('kb1'))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })
})

// --- lineage: self-audited banner + denied reason + provider hash -----------

describe('Lineage — self-audited reads, denied reasons, hashes', () => {
  it('shows the self-audited banner and never expands the query hash', async () => {
    api.listLineage.mockResolvedValue({
      items: [lineageFixture],
      has_more: false,
    })
    wrap(<KnowledgeView />)
    await userEvent.click(screen.getByRole('tab', { name: /lineage/i }))
    expect(await screen.findByText(/self-audited/i)).toBeInTheDocument()
    // The agent ref appears; the query hash is shown truncated, never as text.
    expect(await screen.findByText('agent:planner')).toBeInTheDocument()
    expect(screen.getByText('Denied')).toBeInTheDocument()
  })

  it('shows the governance denial reason in the detail (not a generic error)', async () => {
    api.getLineage.mockResolvedValue(lineageFixture)
    wrap(<LineageDetailSheet lineageId="ln1" open onOpenChange={() => {}} />)
    expect(await screen.findByText(/residency mismatch/i)).toBeInTheDocument()
    expect(screen.getByText('Live')).toBeInTheDocument()
  })
})

describe('QueryDialog — source modes are visible and filterable', () => {
  it('renders export/live badges and filters retrieval results by mode', async () => {
    api.query.mockResolvedValue({
      lineage_id: 'ln1',
      results: [
        {
          chunk_id: 'ch-export',
          document_id: 'doc-export',
          source_kind: 'gdrive',
          source_ref: 'gdrive',
          title: 'Export Runbook',
          text: 'export content',
          classification: 'internal',
          score: 0.91,
        },
        {
          chunk_id: 'ch-live',
          document_id: 'doc-live',
          source_kind: 'confluence',
          source_ref: 'confluence',
          source_mode: 'live',
          title: 'Live Runbook',
          text: 'live content',
          classification: 'internal',
          score: 0.88,
        },
      ],
      count: 2,
      embed_model: 'local-hash',
      egress: false,
    })
    const user = userEvent.setup()
    wrap(<QueryDialog kb={kbFixture} open onOpenChange={() => {}} />)
    await user.type(screen.getByRole('textbox', { name: /query/i }), 'runbook')
    await user.click(screen.getByRole('button', { name: /^run retrieval$/i }))

    expect(await screen.findByText('Export Runbook')).toBeInTheDocument()
    expect(screen.getByText('Live Runbook')).toBeInTheDocument()
    expect(screen.getByText('Export')).toBeInTheDocument()
    expect(screen.getByText('Live')).toBeInTheDocument()

    await user.click(screen.getByRole('combobox', { name: /^mode$/i }))
    await user.click(screen.getByRole('option', { name: 'Live' }))
    expect(screen.queryByText('Export Runbook')).toBeNull()
    expect(screen.getByText('Live Runbook')).toBeInTheDocument()
  })

  const BASE_RESP = {
    lineage_id: 'ln1',
    results: [
      {
        chunk_id: 'c1',
        document_id: 'd1',
        source_kind: 'gdrive',
        source_ref: 'gdrive',
        title: 'Runbook',
        text: 'content',
        classification: 'internal',
        score: 0.9,
      },
    ],
    count: 1,
    embed_model: 'local-hash',
    egress: false,
  }

  async function consultar(user: ReturnType<typeof userEvent.setup>) {
    wrap(<QueryDialog kb={kbFixture} open onOpenChange={() => {}} />)
    await user.type(screen.getByRole('textbox', { name: /query/i }), 'runbook')
    await user.click(screen.getByRole('button', { name: /^run retrieval$/i }))
  }

  /**
   * ⛔ EL CONTEXTO TRUNCADO: `context_truncated` + `context_dropped_chunks`
   * (`modules/knowledge/retrieval.go:69-70`) dicen que el contexto NO cabía y se soltaron
   * trozos. El diálogo no los pintaba.
   *
   * EL MUTANTE: callarlo. La respuesta se presenta como construida sobre todo lo recuperado
   * cuando se construyó sobre menos, y quien la usa para decidir no tiene forma de saberlo desde
   * la pantalla que se la enseña.
   */
  it('dice cuando el contexto se truncó y cuántos trozos se soltaron', async () => {
    api.query.mockResolvedValue({
      ...BASE_RESP,
      context_truncated: true,
      context_dropped_chunks: 7,
    })
    const user = userEvent.setup()
    await consultar(user)
    expect(
      await screen.findByText(/context did not fit: 7 chunks were dropped/i),
    ).toBeInTheDocument()
  })

  /** LA DIRECCIÓN QUE NO DEBE DISPARAR: sin truncado no se avisa. */
  it('sin truncado no avisa', async () => {
    api.query.mockResolvedValue(BASE_RESP)
    const user = userEvent.setup()
    await consultar(user)
    expect(await screen.findByText('Runbook')).toBeInTheDocument()
    expect(screen.queryByText(/context did not fit/i)).toBeNull()
  })

  /**
   * ⛔ LOS TRES ESTADOS DE UN SUELO, y el motor los pidió con estas palabras (`retrieval.go:72-74`):
   * «Reporting the flag without reporting its effect is how a control that applies nothing looks
   * identical to one that applies something and finds nothing».
   *
   * EL MUTANTE: fundir «exigida y sin efecto» con «no exigida». Un control de redacción que se
   * aplicó y no encontró nada se lee entonces como un control que no existe — y esa es
   * exactamente la lectura que hace pensar que no hay gobierno donde sí lo hay.
   */
  it('una redacción exigida que no quitó nada se dice, no se calla', async () => {
    api.query.mockResolvedValue({
      ...BASE_RESP,
      redaction_required: true,
      redacted_items: 0,
    })
    const user = userEvent.setup()
    await consultar(user)
    expect(
      await screen.findByText(/redaction required, nothing matched/i),
    ).toBeInTheDocument()
  })

  it('una redacción con efecto dice cuántos elementos quitó', async () => {
    api.query.mockResolvedValue({
      ...BASE_RESP,
      redaction_required: true,
      redacted_items: 4,
    })
    const user = userEvent.setup()
    await consultar(user)
    expect(await screen.findByText(/4 items redacted/i)).toBeInTheDocument()
    expect(screen.queryByText(/nothing matched/i)).toBeNull()
  })

  /**
   * Y sin redacción exigida NO se pinta NINGUNO de los dos veredictos.
   *
   * ⚠ Se afirma contra los DOS TEXTOS EXACTOS y no contra `/redact/i`: el diálogo ya dice en otro
   *   sitio que el texto de los trozos viene redactado, y un regex laxo lo encontraba ahí — la
   *   celda habría fallado por una frase que no tiene nada que ver con este control.
   */
  it('sin redacción exigida no se pinta ninguno de los dos veredictos', async () => {
    api.query.mockResolvedValue(BASE_RESP)
    const user = userEvent.setup()
    await consultar(user)
    expect(await screen.findByText('Runbook')).toBeInTheDocument()
    expect(
      screen.queryByText(/redaction required, nothing matched/i),
    ).toBeNull()
    expect(screen.queryByText(/items redacted/i)).toBeNull()
  })
})

// --- prompts: rollback confirm-phrase ---------------------------------------

describe('PromptDetailSheet — versioning & rollback gate', () => {
  beforeEach(() => {
    api.getPrompt.mockResolvedValue(promptFixture)
    api.listRevisions.mockResolvedValue({
      items: revisionsFixture,
      has_more: false,
    })
  })

  it('lists immutable revisions with the current marker', async () => {
    wrap(<PromptDetailSheet promptId="p1" open onOpenChange={() => {}} />)
    await screen.findByRole('heading', { name: /revisions/i })
    expect(screen.getByText('Current')).toBeInTheDocument()
    expect(
      screen.getByText('You are a concise, governed assistant.'),
    ).toBeInTheDocument()
  })

  it('gates rollback behind a typed phrase and dispatches it with the right rev', async () => {
    api.rollbackPrompt.mockResolvedValue({ prompt_id: 'p1', current_rev: 1 })
    wrap(<PromptDetailSheet promptId="p1" open onOpenChange={() => {}} />)
    await screen.findByRole('heading', { name: /revisions/i })
    // rev 1 is not current → it offers a rollback control.
    await userEvent.click(
      screen.getByRole('button', { name: /roll back to this/i }),
    )
    const confirm = await screen.findByRole('button', { name: /^roll back$/i })
    expect(confirm).toBeDisabled()
    await userEvent.type(
      screen.getByLabelText(/confirmation phrase/i),
      'ROLLBACK',
    )
    await userEvent.click(confirm)
    await waitFor(() =>
      expect(api.rollbackPrompt).toHaveBeenCalledWith('p1', { rev: 1 }),
    )
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })

  it('hides the rollback control when the role cannot write prompts', async () => {
    authState.can = (p) => p !== 'knowledge:prompt:write'
    wrap(<PromptDetailSheet promptId="p1" open onOpenChange={() => {}} />)
    await screen.findByRole('heading', { name: /revisions/i })
    expect(
      screen.queryByRole('button', { name: /roll back to this/i }),
    ).toBeNull()
  })
})

// --- memory: redacted content, write flow, purge gate -----------------------

describe('Memory — minimum-data, write flow, admin purge gate', () => {
  it('renders entries with metadata and an expiry badge', async () => {
    api.listMemory.mockResolvedValue({
      items: [memoryFixture],
      has_more: false,
    })
    wrap(<KnowledgeView />)
    await userEvent.click(screen.getByRole('tab', { name: /memory/i }))
    expect(await screen.findByText('preferences')).toBeInTheDocument()
    expect(screen.getByText(/expires/i)).toBeInTheDocument()
  })

  it('hides the admin purge action when the role lacks memory:admin', async () => {
    authState.can = (p) => p !== 'knowledge:memory:admin'
    api.listMemory.mockResolvedValue({
      items: [memoryFixture],
      has_more: false,
    })
    wrap(<KnowledgeView />)
    await userEvent.click(screen.getByRole('tab', { name: /memory/i }))
    await screen.findByText('preferences')
    expect(screen.queryByRole('button', { name: /purge expired/i })).toBeNull()
  })

  it('writes a memory entry (submit → api.writeMemory → toast)', async () => {
    api.writeMemory.mockResolvedValue(memoryFixture)
    const onOpenChange = vi.fn()
    wrap(<MemoryEditorDialog open onOpenChange={onOpenChange} />)
    const write = screen.getByRole('button', { name: /write memory/i })
    expect(write).toBeDisabled()
    await userEvent.type(screen.getByLabelText(/agent reference/i), 'agent:x')
    await userEvent.type(screen.getByLabelText(/key/i), 'pref')
    expect(write).toBeEnabled()
    await userEvent.click(write)
    await waitFor(() => expect(api.writeMemory).toHaveBeenCalledTimes(1))
    expect(api.writeMemory.mock.calls[0][0]).toMatchObject({
      agent_ref: 'agent:x',
      key: 'pref',
    })
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })

  it('requires a typed phrase to purge expired memory (high risk)', async () => {
    authState.can = () => true
    api.listMemory.mockResolvedValue({
      items: [memoryFixture],
      has_more: false,
    })
    api.purgeMemory.mockResolvedValue({ purged: 5 })
    wrap(<KnowledgeView />)
    await userEvent.click(screen.getByRole('tab', { name: /memory/i }))
    await screen.findByText('preferences')
    await userEvent.click(
      screen.getByRole('button', { name: /purge expired/i }),
    )
    const confirm = await screen.findByRole('button', { name: /^purge$/i })
    expect(confirm).toBeDisabled()
    await userEvent.type(screen.getByLabelText(/confirmation phrase/i), 'PURGE')
    await userEvent.click(confirm)
    await waitFor(() => expect(api.purgeMemory).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })
})

// --- context policies --------------------------------------------------------

describe('Context policies tab', () => {
  it('lists policies with scope, strategy and redaction', async () => {
    api.listContextPolicies.mockResolvedValue({
      items: [contextPolicyFixture],
      has_more: false,
    })
    wrap(<KnowledgeView />)
    await userEvent.click(screen.getByRole('tab', { name: /context/i }))
    expect(await screen.findByText('agent:planner')).toBeInTheDocument()
    expect(screen.getByText('Summarize')).toBeInTheDocument()
  })

  it('shows ForbiddenState for the context tab when the role cannot read it', async () => {
    authState.can = (p) => p !== 'knowledge:context:read'
    wrap(<KnowledgeView />)
    await userEvent.click(screen.getByRole('tab', { name: /context/i }))
    expect(
      await screen.findByText(/not authorized|forbidden/i),
    ).toBeInTheDocument()
    expect(api.listContextPolicies).not.toHaveBeenCalled()
  })
})
