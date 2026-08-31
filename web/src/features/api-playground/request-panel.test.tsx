// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import type { ParsedEndpoint } from './openapi-parser'
import { RequestPanel } from './request-panel'
import { ResponsePanel } from './response-panel'
import { usePlayground } from './use-playground'
import './i18n'

const baseEndpoint: ParsedEndpoint = {
  method: 'GET',
  path: '/v1/m/sessions/stream',
  operationId: 'streamSessions',
  summary: 'Stream sessions',
  tag: 'sessions',
  secured: true,
  requiredPermission: 'sessions:live:read',
  stability: 'beta',
  namespace: 'sessions',
  parameters: [],
  requestBody: null,
  responseSchema: null,
}

function PlaygroundHarness({ endpoint }: { endpoint: ParsedEndpoint }) {
  const response = usePlayground((state) => state.response)
  const isLoading = usePlayground((state) => state.isLoading)
  const isStreaming = usePlayground((state) => state.isStreaming)
  return (
    <TooltipProvider delayDuration={0}>
      <RequestPanel endpoint={endpoint} />
      <ResponsePanel
        response={response}
        isLoading={isLoading}
        isStreaming={isStreaming}
      />
    </TooltipProvider>
  )
}

function renderRequestPanel(endpoint: ParsedEndpoint) {
  return render(
    <TooltipProvider delayDuration={0}>
      <RequestPanel endpoint={endpoint} />
    </TooltipProvider>,
  )
}

beforeEach(() => {
  useSessionStore.setState({
    token: 'olvs_test',
    sessionId: 'session-test',
    expiresAt: '2099-01-01T00:00:00Z',
  })
  useTenantStore.setState({ activeTenant: 'tenant-test' })
  usePlayground.setState({
    selectedEndpoint: null,
    headers: {},
    body: '',
    pathParams: {},
    queryParams: {},
    response: null,
    isLoading: false,
    isStreaming: false,
    history: [],
  })
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('RequestPanel request bodies', () => {
  const deleteEndpoint: ParsedEndpoint = {
    ...baseEndpoint,
    method: 'DELETE',
    path: '/v1/m/sessions/archive',
    operationId: 'archiveSessions',
    requestBody: {
      type: 'object',
      properties: { reason: { type: 'string' } },
      required: [],
    },
  }

  it('sends a non-empty DELETE body as JSON', async () => {
    const fetchMock = vi.fn(
      async (_url: string, _init?: RequestInit) =>
        new Response('{"ok":true}', {
          status: 200,
          statusText: 'OK',
          headers: { 'Content-Type': 'application/json' },
        }),
    )
    vi.stubGlobal('fetch', fetchMock)
    renderRequestPanel(deleteEndpoint)

    act(() => usePlayground.getState().setBody('{"reason":"cleanup"}'))
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce())
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit
    expect(init.method).toBe('DELETE')
    expect(init.body).toBe('{"reason":"cleanup"}')
    expect(init.headers).toMatchObject({ 'Content-Type': 'application/json' })
  })

  it('does not send an empty DELETE body', async () => {
    const fetchMock = vi.fn(
      async (_url: string, _init?: RequestInit) =>
        new Response(null, {
          status: 204,
          statusText: 'No Content',
        }),
    )
    vi.stubGlobal('fetch', fetchMock)
    renderRequestPanel(deleteEndpoint)

    act(() => usePlayground.getState().setBody('   '))
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce())
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit
    expect(init.body).toBeUndefined()
  })
})

describe('RequestPanel destination and credential policy', () => {
  const unsafeDestinations = [
    ['absolute cross-origin URL', 'https://collector.invalid/v1/leak'],
    ['scheme-relative authority', '//collector.invalid/v1/leak'],
    ['non-HTTP data URL', 'data:text/plain,leak'],
    ['same-creator blob URL', `blob:${window.location.origin}/leak`],
    ['backslash authority', '/\\collector.invalid/v1/leak'],
    [
      'authority exposed by dot-segment normalization',
      '/..//collector.invalid/leak',
    ],
    ['absolute same-origin URL', `${window.location.origin}/v1/agents`],
  ] as const

  it.each(unsafeDestinations)(
    'blocks %s before fetch',
    async (caseName, path) => {
      const fetchMock = vi.fn(async () =>
        Promise.resolve(new Response('{"unexpected":true}')),
      )
      vi.stubGlobal('fetch', fetchMock)
      renderRequestPanel({ ...baseEndpoint, path })

      fireEvent.click(screen.getByRole('button', { name: 'Send' }))

      await waitFor(() =>
        expect(usePlayground.getState().isLoading).toBe(false),
      )
      expect(
        fetchMock,
        `deny-closed: ${caseName} must make zero fetch calls`,
      ).not.toHaveBeenCalled()
      await waitFor(() =>
        expect(usePlayground.getState().response?.statusText).toBe(
          'Blocked destination',
        ),
      )
      expect(
        usePlayground.getState().response?.body,
        'deny-closed: a blocked destination must explain the local refusal',
      ).toBe('API Playground can only send requests to this control plane.')
      expect(
        usePlayground.getState().history,
        'deny-closed: a request that never left must not look like an engine response',
      ).toHaveLength(0)
    },
  )

  it('blocks an unsafe curl export before copying managed credentials', async () => {
    const writeText = vi.fn(async (_command: string) => undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    renderRequestPanel({
      ...baseEndpoint,
      path: '@collector.invalid/v1/leak',
    })

    fireEvent.click(screen.getByRole('button', { name: 'curl' }))

    expect(
      writeText,
      'deny-closed: unsafe destination must make zero clipboard writes',
    ).not.toHaveBeenCalled()
    await waitFor(() =>
      expect(usePlayground.getState().response?.statusText).toBe(
        'Blocked destination',
      ),
    )
  })

  it('exports a canonical same-origin curl command after the destination check', async () => {
    const writeText = vi.fn(async (_command: string) => undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    renderRequestPanel(baseEndpoint)

    fireEvent.click(screen.getByRole('button', { name: 'curl' }))

    await waitFor(() => expect(writeText).toHaveBeenCalledOnce())
    const command = writeText.mock.calls[0]?.[0]
    expect(
      command,
      'allowed curl export must retain the canonical control-plane origin',
    ).toContain(`${window.location.origin}/v1/m/sessions/stream`)
    expect(command).toContain('Authorization: Bearer olvs_test')
    expect(command).not.toContain('@collector.invalid')
  })

  it('canonicalizes an allowed request and pins managed credentials', async () => {
    const endpoint: ParsedEndpoint = {
      ...baseEndpoint,
      method: 'POST',
      path: '/v1/m/governance/policies/{id}',
      parameters: [
        {
          name: 'id',
          in: 'path',
          required: true,
          description: '',
          schema: { type: 'string' },
        },
        {
          name: 'filter',
          in: 'query',
          required: false,
          description: '',
          schema: { type: 'string' },
        },
      ],
      requestBody: {
        type: 'object',
        properties: { name: { type: 'string' } },
        required: ['name'],
      },
    }
    usePlayground.setState({
      headers: {
        Authorization: 'Bearer forged-exact',
        authorization: 'Bearer forged-lowercase',
        'X-Olivares-Tenant': 'forged-tenant',
        'x-olivares-tenant': 'forged-tenant-lowercase',
        'Content-Type': 'text/plain',
        'content-type': 'application/xml',
        'X-Trace': 'trace-1',
      },
    })
    const fetchMock = vi.fn(async () =>
      Promise.resolve(
        new Response('{"created":true}', {
          status: 201,
          statusText: 'Created',
          headers: { 'Content-Type': 'application/json' },
        }),
      ),
    )
    vi.stubGlobal('fetch', fetchMock)
    renderRequestPanel(endpoint)

    act(() => {
      usePlayground.getState().setPathParam('id', 'policy/one')
      usePlayground.getState().setQueryParam('filter', 'active only')
      usePlayground.getState().setBody('{"name":"guarded"}')
    })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce())
    const [url, init] = fetchMock.mock.calls[0] as unknown as [
      string,
      RequestInit,
    ]
    expect(
      url,
      'allowed destination must be a canonical same-origin path',
    ).toBe('/v1/m/governance/policies/policy%2Fone?filter=active+only')
    expect(
      init.credentials,
      'allowed request must pin browser credentials to same-origin',
    ).toBe('same-origin')
    expect(
      init.redirect,
      'allowed request must reject redirects after the same-origin check',
    ).toBe('error')
    const sentHeaders = init.headers as Record<string, string>
    expect(
      sentHeaders.Authorization,
      'secured same-origin request lost the active bearer',
    ).toBe('Bearer olvs_test')
    expect(
      sentHeaders['X-Olivares-Tenant'],
      'same-origin request lost the active tenant',
    ).toBe('tenant-test')
    expect(
      sentHeaders['Content-Type'],
      'JSON request must retain its managed content type',
    ).toBe('application/json')
    expect(sentHeaders['X-Trace']).toBe('trace-1')
    for (const managed of [
      'authorization',
      'x-olivares-tenant',
      'content-type',
    ]) {
      expect(
        Object.keys(sentHeaders).filter(
          (name) => name.toLowerCase() === managed,
        ),
        `managed header ${managed} must have exactly one canonical value`,
      ).toHaveLength(1)
    }
    expect(init.body).toBe('{"name":"guarded"}')
  })

  it('does not attach the console bearer to an unsecured endpoint', async () => {
    usePlayground.setState({
      headers: {
        Authorization: 'Bearer forged-exact',
        authorization: 'Bearer forged-lowercase',
      },
    })
    const fetchMock = vi.fn(
      async (_url: string, _init?: RequestInit) =>
        new Response('{"status":"ok"}', { status: 200 }),
    )
    vi.stubGlobal('fetch', fetchMock)
    renderRequestPanel({
      ...baseEndpoint,
      path: '/healthz',
      secured: false,
      requiredPermission: undefined,
    })

    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce())

    const init = fetchMock.mock.calls[0]?.[1] as RequestInit
    const sentHeaders = init.headers as Record<string, string>
    expect(
      Object.keys(sentHeaders).some(
        (name) => name.toLowerCase() === 'authorization',
      ),
      'unsecured endpoint must not receive the console bearer token',
    ).toBe(false)
    expect(sentHeaders['X-Olivares-Tenant']).toBe('tenant-test')
  })

  it('refuses a case-variant managed header entered through the UI', async () => {
    renderRequestPanel({
      ...baseEndpoint,
      path: '/v1/agents/{id}',
      parameters: [
        {
          name: 'id',
          in: 'path',
          required: true,
          description: '',
          schema: { type: 'string' },
        },
      ],
    })
    await userEvent.click(screen.getByRole('tab', { name: 'Headers' }))
    fireEvent.change(screen.getByPlaceholderText('Header name'), {
      target: { value: 'aUtHoRiZaTiOn' },
    })
    fireEvent.change(screen.getByPlaceholderText('Value'), {
      target: { value: 'Bearer forged-from-ui' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add custom header' }))

    expect(
      usePlayground.getState().headers,
      'managed header input must not enter request state under alternate casing',
    ).toEqual({})
  })

  it.each([
    [401, 'Unauthorized', 'unauthenticated'],
    [403, 'Forbidden', 'forbidden'],
  ] as const)(
    'preserves an engine %i rejection as a response',
    async (status, statusText, code) => {
      const body = JSON.stringify({
        error: { code, message: `engine says ${code}` },
      })
      const fetchMock = vi.fn(async () =>
        Promise.resolve(
          new Response(body, {
            status,
            statusText,
            headers: { 'Content-Type': 'application/json' },
          }),
        ),
      )
      vi.stubGlobal('fetch', fetchMock)
      render(<PlaygroundHarness endpoint={baseEndpoint} />)

      fireEvent.click(screen.getByRole('button', { name: 'Send' }))

      await waitFor(() =>
        expect(
          usePlayground.getState().response?.status,
          'engine rejection was flattened instead of rendered verbatim',
        ).toBe(status),
      )
      expect(fetchMock).toHaveBeenCalledOnce()
      expect(usePlayground.getState().response?.statusText).toBe(statusText)
      expect(usePlayground.getState().response?.body).toBe(body)
      expect(
        usePlayground.getState().history[0]?.status,
        'engine rejection must remain visible in request history',
      ).toBe(status)
      expect(
        useSessionStore.getState().token,
        'an exploratory engine rejection must not clear the operator session',
      ).toBe('olvs_test')
      expect(usePlayground.getState().isLoading).toBe(false)
    },
  )

  it('distinguishes a transport failure from a local policy block', async () => {
    const fetchMock = vi.fn(async () => {
      throw new TypeError('socket offline')
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<PlaygroundHarness endpoint={baseEndpoint} />)

    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() =>
      expect(
        usePlayground.getState().response?.statusText,
        'transport failure must say Network Error, not Blocked destination',
      ).toBe('Network Error'),
    )
    expect(fetchMock).toHaveBeenCalledOnce()
    expect(usePlayground.getState().response?.status).toBe(0)
    expect(usePlayground.getState().response?.body).toBe('socket offline')
    expect(
      usePlayground.getState().history,
      'transport failure without an engine response must not fabricate history',
    ).toHaveLength(0)
    expect(useSessionStore.getState().token).toBe('olvs_test')
    expect(usePlayground.getState().isLoading).toBe(false)
  })
})

describe('RequestPanel SSE responses', () => {
  it('appends streamed events and keeps received content when cancelled', async () => {
    const encoder = new TextEncoder()
    let streamController!: ReadableStreamDefaultController<Uint8Array>
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        streamController = controller
      },
    })
    const fetchMock = vi.fn(
      async (_url: string, init?: RequestInit): Promise<Response> => {
        init?.signal?.addEventListener(
          'abort',
          () => {
            streamController.error(new DOMException('Aborted', 'AbortError'))
          },
          { once: true },
        )
        return new Response(stream, {
          status: 200,
          statusText: 'OK',
          headers: { 'Content-Type': 'text/event-stream; charset=utf-8' },
        })
      },
    )
    vi.stubGlobal('fetch', fetchMock)
    render(<PlaygroundHarness endpoint={baseEndpoint} />)

    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(await screen.findByText('Streaming…')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeInTheDocument()

    const first = 'event: progress\ndata: {"step":1}\n\n'
    await act(async () => {
      streamController.enqueue(encoder.encode(first))
      await Promise.resolve()
    })
    await waitFor(() =>
      expect(usePlayground.getState().response?.body).toBe(first),
    )

    const second = 'event: progress\ndata: {"step":2}\n\n'
    await act(async () => {
      streamController.enqueue(encoder.encode(second))
      await Promise.resolve()
    })
    await waitFor(() =>
      expect(usePlayground.getState().response?.body).toBe(first + second),
    )
    expect(
      screen.getByRole('textbox', { name: 'Response body' }),
    ).toHaveTextContent('"step":2')

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    await waitFor(() => expect(usePlayground.getState().isLoading).toBe(false))
    expect(usePlayground.getState().isStreaming).toBe(false)
    expect(usePlayground.getState().response?.body).toBe(first + second)
    expect(usePlayground.getState().history).toHaveLength(1)
    expect(usePlayground.getState().history[0]?.status).toBe(200)
    const signal = fetchMock.mock.calls[0]?.[1]?.signal
    expect(signal?.aborted).toBe(true)
  })
})
