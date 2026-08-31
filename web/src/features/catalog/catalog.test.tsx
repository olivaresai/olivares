// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { EntryDTO, InstanceDTO, PubkeyDTO, VerifyDTO } from './types'

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
  listEntries: vi.fn(),
  getEntry: vi.fn(),
  createEntry: vi.fn(),
  updateEntry: vi.fn(),
  deleteEntry: vi.fn(),
  submitEntry: vi.fn(),
  approveEntry: vi.fn(),
  deprecateEntry: vi.fn(),
  verifyEntry: vi.fn(),
  instantiateEntry: vi.fn(),
  listAdmissions: vi.fn(),
  admitEntry: vi.fn(),
  pubkey: vi.fn(),
  listInstances: vi.fn(),
  getInstance: vi.fn(),
  transitionInstance: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, catalogApi: api }
})

import { catalogKeys } from './api'
import CatalogView from './catalog-view'
import { EntryDetailSheet } from './entry-detail'
import { EntryEditorDialog } from './entry-editor'
import { InstanceDetailSheet } from './instance-detail'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

// --- fixtures ----------------------------------------------------------------

const draftEntry: EntryDTO = {
  id: 'e-draft',
  kind: 'mcp',
  name: 'GitHub MCP',
  slug: 'github-mcp',
  version: '1.0.0',
  status: 'draft',
  summary: 'A GitHub MCP server',
  spec: { command: 'npx', token_ref: '$GITHUB_TOKEN' },
  owner_ref: 'platform-team',
  signed: false,
}

const approvedEntry: EntryDTO = {
  id: 'e-approved',
  kind: 'agent',
  name: 'Triage Agent',
  slug: 'triage-agent',
  version: '2.1.0',
  status: 'approved',
  summary: 'Approved triage agent',
  spec: { model: 'claude' },
  owner_ref: 'platform-team',
  content_hash: 'a'.repeat(64),
  signed: true,
  sig_alg: 'ed25519',
  signed_by: 'deadbeefcafe0001',
  approved_by: 'user:1234',
  approved_at: '2026-06-01T10:00:00Z',
}

const verifyOk: VerifyDTO = {
  status: 'approved',
  hash_ok: true,
  signed: true,
  signature_ok: true,
  verified: true,
  signed_by: 'deadbeefcafe0001',
  content_hash: 'a'.repeat(64),
  recomputed_hash: 'a'.repeat(64),
  reason: 'signature verified over pinned hash',
}

const pubkeyEnabled: PubkeyDTO = {
  signing_enabled: true,
  algorithm: 'ed25519',
  public_key: 'BASE64PUBLICKEY==',
  fingerprint: 'deadbeefcafe0001',
}

const requestedInstance: InstanceDTO = {
  id: 'i-req',
  entry_id: 'e-approved',
  entry_kind: 'agent',
  entry_slug: 'triage-agent',
  entry_version: '2.1.0',
  name: 'triage-prod',
  target_ref: 'env:prod',
  status: 'requested',
  requested_by: 'user:1234',
}

beforeEach(() => {
  authState.can = () => true
  for (const fn of Object.values(api)) fn.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
  // Sensible defaults so panels that fetch on open don't reject.
  api.verifyEntry.mockResolvedValue(verifyOk)
  api.pubkey.mockResolvedValue(pubkeyEnabled)
  // The admission panel/editor query for mcp/connector entries — default to no verdict.
  api.listAdmissions.mockResolvedValue({ items: [] })
})
afterEach(() => vi.clearAllMocks())

// --- (a) the main list renders rows ------------------------------------------

describe('CatalogView — entries list', () => {
  it('lists catalog entries with kind, version and status', async () => {
    api.listEntries.mockResolvedValue({
      items: [draftEntry, approvedEntry],
      has_more: false,
    })
    wrap(<CatalogView />)
    expect(await screen.findByText('GitHub MCP')).toBeInTheDocument()
    expect(screen.getByText('Triage Agent')).toBeInTheDocument()
    expect(screen.getByText('2.1.0')).toBeInTheDocument()
    // Status badges localized via the catalog/common namespaces.
    expect(screen.getByText('Draft')).toBeInTheDocument()
    expect(screen.getByText('Approved')).toBeInTheDocument()
  })

  it('hides the New entry button when the role cannot write entries', async () => {
    authState.can = (p) => p !== 'catalog:entry:write'
    api.listEntries.mockResolvedValue({ items: [draftEntry], has_more: false })
    wrap(<CatalogView />)
    await screen.findByText('GitHub MCP')
    expect(screen.queryByRole('button', { name: /new entry/i })).toBeNull()
  })
})

// --- (b)/(c) verification posture is honest; signing shown honestly ----------

describe('EntryDetailSheet — honest verification posture', () => {
  it('renders an approved+signed entry as "signature verified" (not an alarm)', async () => {
    api.getEntry.mockResolvedValue(approvedEntry)
    wrap(<EntryDetailSheet entryId="e-approved" open onOpenChange={() => {}} />)
    // The honest status label (exact match — the reason string also contains the
    // phrase, so match the standalone label only).
    expect(await screen.findByText('Signature verified')).toBeInTheDocument()
    // The signer fingerprint is shown — never a raw public key in the entry itself.
    expect(screen.getAllByText('deadbeefcafe0001').length).toBeGreaterThan(0)
  })

  it('renders verified=false plainly as "not verified", never as approved/safe', async () => {
    api.getEntry.mockResolvedValue(approvedEntry)
    api.verifyEntry.mockResolvedValue({
      ...verifyOk,
      hash_ok: false,
      signature_ok: false,
      verified: false,
      recomputed_hash: 'b'.repeat(64),
      reason: 'recomputed hash does not match the pinned hash',
    })
    wrap(<EntryDetailSheet entryId="e-approved" open onOpenChange={() => {}} />)
    expect(await screen.findByText(/not verified/i)).toBeInTheDocument()
    expect(screen.queryByText(/signature verified/i)).toBeNull()
  })

  it('does not render a raw secret value: the spec is shown as a read-only reference', async () => {
    api.getEntry.mockResolvedValue(draftEntry)
    wrap(<EntryDetailSheet entryId="e-draft" open onOpenChange={() => {}} />)
    await screen.findByText(/specification/i)
    // The spec viewer is read-only (a <pre>), never an editable input here.
    const editbox = screen
      .queryAllByRole('textbox')
      .find((el) =>
        (el as HTMLTextAreaElement).value?.includes('$GITHUB_TOKEN'),
      )
    expect(editbox).toBeUndefined()
    // The secret is referenced by locator inside the read-only spec.
    expect(screen.getByText(/\$GITHUB_TOKEN/)).toBeInTheDocument()
  })
})

// --- (d) privileged action: gated by can(), confirmed via ConfirmDialog ------

describe('EntryDetailSheet — privileged lifecycle actions', () => {
  it('hides admin-only Approve when the role lacks catalog:entry:admin', async () => {
    authState.can = (p) => p !== 'catalog:entry:admin'
    api.getEntry.mockResolvedValue(draftEntry)
    wrap(<EntryDetailSheet entryId="e-draft" open onOpenChange={() => {}} />)
    await screen.findByText(/specification/i)
    expect(screen.queryByRole('button', { name: /^approve$/i })).toBeNull()
    // A writer can still see Submit and Edit.
    expect(
      screen.getByRole('button', { name: /submit for review/i }),
    ).toBeInTheDocument()
  })

  it('asks for confirmation (with typed phrase) before approving', async () => {
    api.getEntry.mockResolvedValue(draftEntry)
    wrap(<EntryDetailSheet entryId="e-draft" open onOpenChange={() => {}} />)
    await userEvent.click(
      await screen.findByRole('button', { name: /^approve$/i }),
    )
    // The danger confirm requires typing the exact phrase; confirm starts disabled.
    const confirm = await screen.findByRole('button', {
      name: /approve and freeze/i,
    })
    expect(confirm).toBeDisabled()
    expect(api.approveEntry).not.toHaveBeenCalled()
  })
})

// --- (e) ONE full mutation flow: create draft entry --------------------------

describe('EntryEditorDialog — create draft (full mutation flow)', () => {
  it('submits only allowed fields → api.createEntry → success toast → close', async () => {
    api.createEntry.mockResolvedValue({ ...draftEntry })
    const onOpenChange = vi.fn()
    wrap(<EntryEditorDialog open onOpenChange={onOpenChange} entry={null} />)

    // The draft/audit notices are present (the form is the confirmation surface).
    expect(screen.getByText(/created as a draft/i)).toBeInTheDocument()
    expect(screen.getByText(/tamper-evident audit ledger/i)).toBeInTheDocument()

    const create = screen.getByRole('button', { name: /create draft/i })
    expect(create).toBeDisabled()

    await userEvent.type(screen.getByLabelText(/display name/i), 'GitHub MCP')
    await userEvent.type(screen.getByLabelText(/^slug/i), 'github-mcp')
    await userEvent.type(screen.getByLabelText(/version/i), '1.0.0')
    expect(create).toBeEnabled()

    await userEvent.click(create)
    await waitFor(() => expect(api.createEntry).toHaveBeenCalledTimes(1))
    const payload = api.createEntry.mock.calls[0][0]
    expect(payload).toMatchObject({
      kind: 'agent',
      name: 'GitHub MCP',
      slug: 'github-mcp',
      version: '1.0.0',
    })
    // Server-managed lifecycle/integrity fields must NEVER be sent.
    expect(payload).not.toHaveProperty('status')
    expect(payload).not.toHaveProperty('content_hash')
    expect(payload).not.toHaveProperty('signed')
    expect(payload).not.toHaveProperty('approved_by')

    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it('warns when a spec value looks like an embedded credential and blocks save', async () => {
    const onOpenChange = vi.fn()
    wrap(<EntryEditorDialog open onOpenChange={onOpenChange} entry={null} />)
    await userEvent.type(screen.getByLabelText(/display name/i), 'Bad')
    await userEvent.type(screen.getByLabelText(/^slug/i), 'bad')
    await userEvent.type(screen.getByLabelText(/version/i), '1.0.0')

    const create = screen.getByRole('button', { name: /create draft/i })
    expect(create).toBeEnabled()

    // A spec embedding a password=… assignment trips the client guard.
    await userEvent.type(
      screen.getByLabelText(/specification/i),
      '{{"url": "https://user:secretpw@host"}',
    )
    expect(
      screen.getAllByText(/looks like a credential/i).length,
    ).toBeGreaterThan(0)
    expect(create).toBeDisabled()
  })
})

describe('EntryEditorDialog — admission-stale warning', () => {
  const verdict = {
    entry_ref: 'e-draft',
    signature_verified: true,
    artifact_verified: true,
    tlog_present: true,
    tlog_verified: true,
  }

  // Property: editing the spec of an admitted mcp draft warns that saving invalidates
  // the recorded verdict (forcing a re-admit) — surfaced BEFORE the operator saves.
  it('warns when a changed spec would invalidate a recorded admission verdict', async () => {
    api.listAdmissions.mockResolvedValue({ items: [verdict] })
    wrap(<EntryEditorDialog open onOpenChange={() => {}} entry={draftEntry} />)

    const spec = await screen.findByLabelText(/specification/i)
    // No warning until the spec actually changes.
    expect(screen.queryByText(/recorded admission verdict/i)).toBeNull()
    fireEvent.change(spec, {
      target: { value: '{"transport":"stdio","endpoint":"npx OTHER"}' },
    })
    expect(await screen.findByRole('alert')).toHaveTextContent(
      /recorded admission verdict/i,
    )
    // queried the mcp endpoint for this entry.
    expect(api.listAdmissions).toHaveBeenCalledWith('mcp', 'e-draft')
  })

  // Property: no verdict → no warning (nothing to invalidate).
  it('does not warn when there is no recorded verdict', async () => {
    api.listAdmissions.mockResolvedValue({ items: [] })
    wrap(<EntryEditorDialog open onOpenChange={() => {}} entry={draftEntry} />)

    const spec = await screen.findByLabelText(/specification/i)
    fireEvent.change(spec, {
      target: { value: '{"transport":"stdio","endpoint":"npx OTHER"}' },
    })
    await waitFor(() => expect(api.listAdmissions).toHaveBeenCalled())
    expect(screen.queryByText(/recorded admission verdict/i)).toBeNull()
  })

  // Property: a verdict but an unchanged spec → no warning (cosmetic edits are safe).
  it('does not warn when the spec is unchanged', async () => {
    api.listAdmissions.mockResolvedValue({ items: [verdict] })
    wrap(<EntryEditorDialog open onOpenChange={() => {}} entry={draftEntry} />)

    await waitFor(() => expect(api.listAdmissions).toHaveBeenCalled())
    expect(screen.queryByText(/recorded admission verdict/i)).toBeNull()
  })

  const connectorDraft: EntryDTO = {
    id: 'e-conn-draft',
    kind: 'connector',
    name: 'Acme Connector',
    slug: 'acme-conn',
    version: '1.0.0',
    status: 'draft',
    spec: { artifact_digest: 'a'.repeat(64), publisher: 'acme' },
    signed: false,
  }

  // Property: for a CONNECTOR the approve gate re-binds only on spec.artifact_digest,
  // so editing a non-digest field must NOT warn (no false "re-admit" alarm).
  it('does not warn on a connector non-digest spec edit', async () => {
    api.listAdmissions.mockResolvedValue({
      items: [{ ...verdict, entry_ref: 'e-conn-draft' }],
    })
    wrap(
      <EntryEditorDialog open onOpenChange={() => {}} entry={connectorDraft} />,
    )
    const spec = await screen.findByLabelText(/specification/i)
    fireEvent.change(spec, {
      target: {
        value: `{"artifact_digest":"${'a'.repeat(64)}","publisher":"OTHER"}`,
      },
    })
    await waitFor(() => expect(api.listAdmissions).toHaveBeenCalled())
    expect(screen.queryByText(/recorded admission verdict/i)).toBeNull()
  })

  // Property: changing a connector's artifact_digest DOES warn (it invalidates the gate).
  it('warns on a connector artifact_digest change', async () => {
    api.listAdmissions.mockResolvedValue({
      items: [{ ...verdict, entry_ref: 'e-conn-draft' }],
    })
    wrap(
      <EntryEditorDialog open onOpenChange={() => {}} entry={connectorDraft} />,
    )
    const spec = await screen.findByLabelText(/specification/i)
    fireEvent.change(spec, {
      target: {
        value: `{"artifact_digest":"${'b'.repeat(64)}","publisher":"acme"}`,
      },
    })
    expect(await screen.findByRole('alert')).toHaveTextContent(
      /recorded admission verdict/i,
    )
  })

  // Property: a no-op reformat (same content, different whitespace/key order) does NOT
  // warn — the check is canonical, mirroring the backend's marshalSpec, not raw text.
  it('does not warn on a no-op spec reformat', async () => {
    api.listAdmissions.mockResolvedValue({ items: [verdict] })
    wrap(<EntryEditorDialog open onOpenChange={() => {}} entry={draftEntry} />)
    const spec = await screen.findByLabelText(/specification/i)
    // draftEntry.spec is {command:'npx', token_ref:'$GITHUB_TOKEN'} — re-serialize with
    // reordered keys + compact whitespace: same canonical content, must not warn.
    fireEvent.change(spec, {
      target: { value: '{"token_ref":"$GITHUB_TOKEN","command":"npx"}' },
    })
    await waitFor(() => expect(api.listAdmissions).toHaveBeenCalled())
    expect(screen.queryByText(/recorded admission verdict/i)).toBeNull()
  })

  // Property (fix #2): after an mcp edit, the admissions query is invalidated — so the
  // panel can never keep showing a "verified" verdict the server deleted on spec change.
  it('invalidates the admissions query on an mcp edit', async () => {
    api.listAdmissions.mockResolvedValue({ items: [verdict] })
    api.updateEntry.mockResolvedValue({ ...draftEntry })
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const invalidate = vi.spyOn(qc, 'invalidateQueries')
    render(
      <QueryClientProvider client={qc}>
        <EntryEditorDialog open onOpenChange={() => {}} entry={draftEntry} />
      </QueryClientProvider>,
    )
    const spec = await screen.findByLabelText(/specification/i)
    fireEvent.change(spec, {
      target: { value: '{"transport":"stdio","endpoint":"npx OTHER"}' },
    })
    await userEvent.click(screen.getByRole('button', { name: /save changes/i }))
    await waitFor(() => expect(api.updateEntry).toHaveBeenCalled())
    await waitFor(() =>
      expect(invalidate).toHaveBeenCalledWith({
        queryKey: catalogKeys.admissions('t1', 'mcp', 'e-draft'),
      }),
    )
  })
})

// --- governance transition: gated by admin, confirmed -------------------------

describe('InstanceDetailSheet — governance transition', () => {
  it('hides transition actions for a non-admin and shows them for an admin', async () => {
    api.getInstance.mockResolvedValue(requestedInstance)
    authState.can = (p) => p !== 'catalog:instance:admin'
    const { unmount } = wrap(
      <InstanceDetailSheet instanceId="i-req" open onOpenChange={() => {}} />,
    )
    await screen.findByText(/provenance/i)
    expect(screen.queryByRole('button', { name: /^approve$/i })).toBeNull()
    unmount()

    authState.can = () => true
    wrap(
      <InstanceDetailSheet instanceId="i-req" open onOpenChange={() => {}} />,
    )
    await screen.findByText(/provenance/i)
    expect(
      screen.getByRole('button', { name: /^approve$/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /^reject$/i }),
    ).toBeInTheDocument()
  })

  it('applies a governance decision through the confirm dialog (approve flow)', async () => {
    api.getInstance.mockResolvedValue(requestedInstance)
    api.transitionInstance.mockResolvedValue({
      ...requestedInstance,
      status: 'approved',
      decided_by: 'user:9',
    })
    wrap(
      <InstanceDetailSheet instanceId="i-req" open onOpenChange={() => {}} />,
    )
    await userEvent.click(
      await screen.findByRole('button', { name: /^approve$/i }),
    )
    // The danger ConfirmDialog opens (Radix marks the underlying sheet aria-hidden,
    // so only the dialog's "Approve" is in the a11y tree). Confirm runs the mutation.
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /^approve$/i }),
    )
    await waitFor(() =>
      expect(api.transitionInstance).toHaveBeenCalledWith('i-req', {
        status: 'approved',
      }),
    )
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })
})

// --- (h) el recorte se DECLARA, y el par prueba que el aviso no está pintado siempre -------

describe('CatalogView — la lista de entradas declara su recorte', () => {
  // ⛔ SE MONTA LA VISTA, no se mira el fuente. Un `{false && <ListTruncationBadge …/>}` dejaría
  //    el aviso escrito y NO alcanzable, y cualquier sonda de texto lo daría por bueno.
  it('con has_more, el aviso nombra las filas CARGADAS, no el techo pedido', async () => {
    api.listEntries.mockResolvedValue({
      items: [draftEntry, approvedEntry],
      has_more: true,
    })
    wrap(<CatalogView />)
    // 2, no 1000: se pide el techo y se enseña lo que llegó. Interpolar la constante convertiría
    // el aviso en una medida inventada.
    expect(
      await screen.findByText('Loaded 2 catalog entries; there are more'),
    ).toBeVisible()
  })

  // La otra mitad del par: sin ella, un aviso incondicional pasaría el caso de arriba.
  it('dirección NO disparadora: sin has_more no hay aviso', async () => {
    api.listEntries.mockResolvedValue({
      items: [draftEntry, approvedEntry],
      has_more: false,
    })
    wrap(<CatalogView />)
    await screen.findByText('GitHub MCP')
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })
})

// --- (i) instancias: el MISMO par, y sin él el aviso no tiene testigo ----------------------
//
// ⛔ POR QUÉ ESTE BLOQUE EXISTE. El contraste Codex sol max (A-09) retiró el
//    `<ListTruncationBadge>` de instancias y la batería siguió en **60/60 verde**. Lo reproduje:
//    mutante puesto, `rc=0`. Un mutante que sobrevive deja el cambio SIN VERIFICAR, y la PR
//    afirmaba tres listas declarando su recorte con testigo para UNA.
//
// ⛔ Y HAY QUE CAMBIAR DE PESTAÑA, no basta con montar la vista: la consulta está condicionada
//    (`catalog-view.tsx:92` → `enabled: tab === 'instances' && canReadInstances`). Sin el clic,
//    `listInstances` no se llama, el aviso no se pinta ni con `has_more`, y el test pasaría
//    **sin observar nada** — verde por no haber mirado, que es la peor clase de verde.
describe('CatalogView — la lista de instancias declara su recorte', () => {
  const dosInstancias = [
    { id: 'i-1', entry_id: 'e-1', name: 'alpha' },
    { id: 'i-2', entry_id: 'e-1', name: 'beta' },
  ] as unknown as InstanceDTO[]

  it('con has_more, el aviso nombra las filas CARGADAS, no el techo pedido', async () => {
    api.listInstances.mockResolvedValue({
      items: dosInstancias,
      has_more: true,
    })
    wrap(<CatalogView />)
    await userEvent.click(
      await screen.findByRole('tab', { name: /instances/i }),
    )
    // 2, no 1000: se pide el techo y se enseña lo que llegó.
    expect(
      await screen.findByText('Loaded 2 instances; there are more'),
    ).toBeVisible()
  })

  it('dirección NO disparadora: sin has_more no hay aviso', async () => {
    api.listInstances.mockResolvedValue({
      items: dosInstancias,
      has_more: false,
    })
    wrap(<CatalogView />)
    await userEvent.click(
      await screen.findByRole('tab', { name: /instances/i }),
    )
    await screen.findByText('alpha')
    expect(
      screen.queryByText(/instances; there are more/i),
    ).not.toBeInTheDocument()
  })
})
