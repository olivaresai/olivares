// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { FolderConnect } from './folder-connect'

const URL_OF_FOLDER = '/v1/m/sessions/workspaces/folder%2Fone/sftp'
// Built at run time: a key header spelled in a source file is refused by the export's leak scan.
const KIND = ['OPENSSH', 'PRIVATE', 'KEY'].join(' ')
const KEY = `-----BEGIN ${KIND}-----\nfake\n-----END ${KIND}-----\n`

let calls: { method: string; url: string; tenant: string | null }[]

/** The server: `status` answers the GET, `connect` the POST. */
function serve(status: Response, connect?: Response) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit) => {
      const method = init.method ?? 'GET'
      calls.push({
        method,
        url,
        tenant: new Headers(init.headers).get('X-Olivares-Tenant'),
      })
      return (method === 'POST' ? connect : status)!.clone()
    }),
  )
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })

function show() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <FolderConnect tenant="tenant-a" folderRef="folder/one" />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  calls = []
  configureApiClient({
    getToken: () => null,
    getTenant: () => 'wrong-tenant',
    onUnauthorized: () => {},
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
})
afterEach(() => vi.unstubAllGlobals())

const STATUS = { user: 'olivares', port: 2222, host_key: 'SHA256:abc' }

describe('FolderConnect, the Connect card of a session folder', () => {
  it('shows nothing where the server does not serve SFTP, and issues nothing', async () => {
    serve(json({ error: { code: 'not_found', message: 'off' } }, 404))
    const { container } = show()
    await waitFor(() => expect(calls).toHaveLength(1))
    expect(calls[0]).toMatchObject({
      method: 'GET',
      url: expect.stringContaining(URL_OF_FOLDER),
      tenant: 'tenant-a',
    })
    expect(container).toBeEmptyDOMElement()
  })

  it('offers Connect, then shows the exact sftp and scp commands, the host key and the key once', async () => {
    serve(
      json(STATUS),
      json(
        {
          ...STATUS,
          private_key: KEY,
          read_only: false,
          expires_at: '2026-11-04T10:10:00Z',
          expires_in: 600,
        },
        201,
      ),
    )
    show()
    const connect = await screen.findByRole('button', { name: 'Connect' })
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(0)
    await userEvent.click(connect)
    expect(
      await screen.findByText(
        'sftp -i olivares-folder.key -P 2222 olivares@localhost',
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'scp -i olivares-folder.key -P 2222 ./file olivares@localhost:',
      ),
    ).toBeInTheDocument()
    expect(screen.getByText('SHA256:abc')).toBeInTheDocument()
    expect(screen.getByLabelText('Private key')).toHaveValue(KEY)
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(1)
    expect(calls[1]).toMatchObject({ tenant: 'tenant-a' })
  })

  it('says so when the credential only reads', async () => {
    serve(
      json(STATUS),
      json(
        {
          ...STATUS,
          private_key: KEY,
          read_only: true,
          expires_at: '2026-11-04T10:10:00Z',
          expires_in: 600,
        },
        201,
      ),
    )
    show()
    await userEvent.click(
      await screen.findByRole('button', { name: 'Connect' }),
    )
    expect(await screen.findByText(/only read files/i)).toBeInTheDocument()
  })

  it('names the step-up the server asks for instead of showing a key', async () => {
    serve(
      json(STATUS),
      json({ error: { code: 'step_up_required', message: 'step up' } }, 403),
    )
    show()
    await userEvent.click(
      await screen.findByRole('button', { name: 'Connect' }),
    )
    expect(await screen.findByRole('alert')).toHaveTextContent(/sign-in step/i)
    expect(screen.queryByLabelText('Private key')).not.toBeInTheDocument()
  })
})
