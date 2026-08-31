// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ClipboardCopy, Play, Plus, X } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { CodeEditor } from '@/components/ui/code-editor'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { cn } from '@/lib/utils'
import type { ParsedEndpoint } from './openapi-parser'
import { generateSchemaExample } from './openapi-parser'
import { generateCurl } from './curl-export'
import { usePlayground, type ResponseState } from './use-playground'

interface RequestPanelProps {
  endpoint: ParsedEndpoint
}

interface ResponseMetadata {
  status: number
  statusText: string
  headers: Record<string, string>
}

const MANAGED_HEADERS = new Set([
  'authorization',
  'x-olivares-tenant',
  'content-type',
])

function isManagedHeader(name: string): boolean {
  return MANAGED_HEADERS.has(name.trim().toLowerCase())
}

class BlockedPlaygroundDestinationError extends Error {
  constructor() {
    super('API Playground destination is outside this control plane')
    this.name = 'BlockedPlaygroundDestinationError'
  }
}

/**
 * Resolve an OpenAPI operation to the only transport shape the embedded console
 * supports: a root-relative path on this document's HTTP(S) origin.
 *
 * The OpenAPI document normally comes from the same engine, but treating every
 * `paths` key as trusted would turn a compromised/stale document into a bearer-token
 * exfiltration primitive. The explicit protocol check matters independently of the
 * origin check: a same-origin `blob:` URL reports the creator origin. Requiring one
 * leading slash also rejects absolute and scheme-relative URLs, matching the original
 * playground contract instead of silently broadening it.
 */
function resolvePlaygroundRequestPath(
  candidate: string,
  origin: string,
): string {
  let base: URL
  let target: URL
  try {
    base = new URL(origin)
    target = new URL(candidate, base)
  } catch {
    throw new BlockedPlaygroundDestinationError()
  }

  const rootRelative = candidate.startsWith('/') && !candidate.startsWith('//')
  const canonicalRootRelative =
    target.pathname.startsWith('/') && !target.pathname.startsWith('//')
  const httpOrigin = base.protocol === 'http:' || base.protocol === 'https:'
  if (
    !rootRelative ||
    !canonicalRootRelative ||
    !httpOrigin ||
    target.protocol !== base.protocol ||
    target.origin !== base.origin ||
    target.username !== '' ||
    target.password !== '' ||
    target.hash !== ''
  ) {
    throw new BlockedPlaygroundDestinationError()
  }

  // Never hand fetch the original string: this canonical path cannot carry an
  // authority even if URL normalization exposed one after resolving dot segments.
  return target.pathname + target.search
}

function buildResponseState(
  metadata: ResponseMetadata,
  body: string,
  durationMs: number,
): ResponseState {
  return {
    ...metadata,
    body,
    durationMs,
    size: new Blob([body]).size,
  }
}

function isAbortError(error: unknown): boolean {
  return (
    typeof error === 'object' &&
    error !== null &&
    'name' in error &&
    error.name === 'AbortError'
  )
}

export function RequestPanel({ endpoint }: RequestPanelProps) {
  const { t } = useTranslation('apiPlayground')
  const token = useSessionStore((s) => s.token)
  const tenant = useTenantStore((s) => s.activeTenant)
  const {
    headers,
    body,
    pathParams,
    queryParams,
    setHeader,
    removeHeader,
    setBody,
    setPathParam,
    setQueryParam,
    setResponse,
    setLoading,
    setStreaming,
    isLoading,
    isStreaming,
    addHistoryEntry,
  } = usePlayground()

  const [customHeaderKey, setCustomHeaderKey] = useState('')
  const [customHeaderValue, setCustomHeaderValue] = useState('')
  const requestControllerRef = useRef<AbortController | null>(null)

  useEffect(() => {
    if (endpoint.requestBody) {
      setBody(generateSchemaExample(endpoint.requestBody))
    }
  }, [endpoint, setBody])

  useEffect(
    () => () => {
      requestControllerRef.current?.abort()
    },
    [endpoint.method, endpoint.path],
  )

  const pathParamNames = useMemo(
    () => endpoint.parameters.filter((p) => p.in === 'path').map((p) => p.name),
    [endpoint],
  )

  const queryParamDefs = useMemo(
    () => endpoint.parameters.filter((p) => p.in === 'query'),
    [endpoint],
  )

  const resolvedPath = useMemo(() => {
    let p = endpoint.path
    for (const [key, value] of Object.entries(pathParams)) {
      p = p.replace(`{${key}}`, encodeURIComponent(value || `{${key}}`))
    }
    return p
  }, [endpoint.path, pathParams])

  const resolvedUrl = useMemo(() => {
    const qs = new URLSearchParams()
    for (const [k, v] of Object.entries(queryParams)) {
      if (v) qs.append(k, v)
    }
    const qStr = qs.toString()
    return qStr ? `${resolvedPath}?${qStr}` : resolvedPath
  }, [resolvedPath, queryParams])

  const effectiveHeaders = useMemo(() => {
    // Custom headers are applied first and managed headers last. Filtering by
    // case-insensitive name is load-bearing: Headers normalises casing, so keeping
    // both `Authorization` and `authorization` would combine two credentials.
    const h: Record<string, string> = Object.fromEntries(
      Object.entries(headers).filter(([key]) => !isManagedHeader(key)),
    )
    if (endpoint.secured && token) {
      h['Authorization'] = `Bearer ${token}`
    }
    if (tenant) {
      h['X-Olivares-Tenant'] = tenant
    }
    if (endpoint.requestBody && endpoint.method !== 'GET') {
      h['Content-Type'] = 'application/json'
    }
    return h
  }, [endpoint, token, tenant, headers])

  const showBlockedDestination = useCallback(
    (durationMs = 0) => {
      const blockedBody = t('request.blockedDestinationHint')
      setResponse({
        status: 0,
        statusText: t('request.blockedDestination'),
        headers: {},
        body: blockedBody,
        durationMs,
        size: new Blob([blockedBody]).size,
      })
    },
    [setResponse, t],
  )

  const sendRequest = useCallback(async () => {
    requestControllerRef.current?.abort()
    const controller = new AbortController()
    requestControllerRef.current = controller
    // Generation guard (review): an endpoint switch bumps the generation,
    // so this request's async completion must stop touching shared state the
    // moment it is no longer the live request.
    const gen = usePlayground.getState().beginRequest()
    const live = () => usePlayground.getState().requestGeneration === gen
    setLoading(true)
    setStreaming(false)
    setResponse(null)
    const start = performance.now()
    let responseMetadata: ResponseMetadata | null = null
    let responseText = ''

    try {
      const init: RequestInit = {
        method: endpoint.method,
        headers: effectiveHeaders,
        signal: controller.signal,
      }

      if (body.trim() && endpoint.method !== 'GET') {
        init.body = body
      }

      const requestPath = resolvePlaygroundRequestPath(
        resolvedUrl,
        window.location.origin,
      )
      const resp = await fetch(requestPath, {
        ...init,
        credentials: 'same-origin',
        // Validation applies to the complete transport, not only its first hop.
        // Refuse 30x so a same-origin handler cannot relay console headers/body
        // to a destination that never passed resolvePlaygroundRequestPath.
        redirect: 'error',
      })
      const respHeaders: Record<string, string> = {}
      resp.headers.forEach((v, k) => {
        respHeaders[k] = v
      })
      responseMetadata = {
        status: resp.status,
        statusText: resp.statusText,
        headers: respHeaders,
      }

      const contentType = resp.headers.get('content-type')?.toLowerCase() ?? ''
      if (contentType.includes('text/event-stream')) {
        if (live()) {
          setStreaming(true)
          setResponse(
            buildResponseState(
              responseMetadata,
              responseText,
              Math.round(performance.now() - start),
            ),
          )
        }

        if (resp.body) {
          const reader = resp.body.getReader()
          const decoder = new TextDecoder()
          try {
            for (;;) {
              const { value, done } = await reader.read()
              if (done) break
              responseText += decoder.decode(value, { stream: true })
              if (live()) {
                setResponse(
                  buildResponseState(
                    responseMetadata,
                    responseText,
                    Math.round(performance.now() - start),
                  ),
                )
              }
            }
            const tail = decoder.decode()
            if (tail) responseText += tail
          } finally {
            reader.releaseLock()
          }
        } else {
          responseText = await resp.text()
        }
      } else {
        responseText = await resp.text()
      }

      const result = buildResponseState(
        responseMetadata,
        responseText,
        Math.round(performance.now() - start),
      )
      if (live()) setResponse(result)
      addHistoryEntry({
        timestamp: Date.now(),
        method: endpoint.method,
        path: endpoint.path,
        status: resp.status,
        durationMs: result.durationMs,
      })
    } catch (err) {
      const durationMs = Math.round(performance.now() - start)
      if (isAbortError(err)) {
        const result = responseMetadata
          ? buildResponseState(responseMetadata, responseText, durationMs)
          : buildResponseState(
              { status: 0, statusText: t('cancel'), headers: {} },
              responseText,
              durationMs,
            )
        if (live()) setResponse(result)
        addHistoryEntry({
          timestamp: Date.now(),
          method: endpoint.method,
          path: endpoint.path,
          status: result.status,
          durationMs,
        })
      } else if (err instanceof BlockedPlaygroundDestinationError && live()) {
        showBlockedDestination(durationMs)
      } else if (live()) {
        setResponse({
          status: 0,
          statusText: t('request.networkError'),
          headers: {},
          body:
            err instanceof Error ? err.message : t('request.failedNetworkHint'),
          durationMs,
          size: 0,
        })
      }
    } finally {
      if (requestControllerRef.current === controller) {
        requestControllerRef.current = null
      }
      if (live()) {
        setStreaming(false)
        setLoading(false)
      }
    }
  }, [
    endpoint,
    effectiveHeaders,
    body,
    resolvedUrl,
    setLoading,
    setStreaming,
    setResponse,
    addHistoryEntry,
    showBlockedDestination,
    t,
  ])

  const cancelRequest = useCallback(() => {
    requestControllerRef.current?.abort()
  }, [])

  const copyCurl = useCallback(() => {
    const origin = window.location.origin
    try {
      const requestPath = resolvePlaygroundRequestPath(resolvedUrl, origin)
      const curl = generateCurl({
        method: endpoint.method,
        url: new URL(requestPath, origin).href,
        headers: effectiveHeaders,
        body: body.trim() && endpoint.method !== 'GET' ? body : null,
      })
      void navigator.clipboard.writeText(curl)
    } catch (err) {
      if (err instanceof BlockedPlaygroundDestinationError) {
        showBlockedDestination()
        return
      }
      throw err
    }
  }, [endpoint, resolvedUrl, effectiveHeaders, body, showBlockedDestination])

  const addCustomHeader = useCallback(() => {
    const key = customHeaderKey.trim()
    if (!key || isManagedHeader(key)) return
    setHeader(key, customHeaderValue)
    setCustomHeaderKey('')
    setCustomHeaderValue('')
  }, [customHeaderKey, customHeaderValue, setHeader])

  return (
    <div className="flex h-full flex-col">
      {/* Method + URL bar */}
      <div className="flex items-center gap-2 border-b p-3">
        <span
          className={cn(
            'rounded px-2 py-1 font-mono text-xs font-bold',
            endpoint.method === 'GET' &&
              'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400',
            endpoint.method === 'POST' &&
              'bg-blue-500/15 text-blue-600 dark:text-blue-400',
            endpoint.method === 'PUT' &&
              'bg-amber-500/15 text-amber-600 dark:text-amber-400',
            endpoint.method === 'PATCH' &&
              'bg-orange-500/15 text-orange-600 dark:text-orange-400',
            endpoint.method === 'DELETE' &&
              'bg-red-500/15 text-red-600 dark:text-red-400',
          )}
        >
          {endpoint.method}
        </span>
        {endpoint.requiredPermission && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Badge
                variant="info"
                className="max-w-56 truncate font-mono"
                tabIndex={0}
                aria-label={`${t('requiredPermission')}: ${endpoint.requiredPermission}`}
              >
                {endpoint.requiredPermission}
              </Badge>
            </TooltipTrigger>
            <TooltipContent>
              {t('requiredPermission')}: {endpoint.requiredPermission}
            </TooltipContent>
          </Tooltip>
        )}
        <code className="min-w-0 flex-1 truncate text-sm">{resolvedUrl}</code>
        <Button
          size="sm"
          variant="outline"
          onClick={copyCurl}
          title={t('copyAsCurl')}
        >
          <ClipboardCopy className="mr-1.5 h-3.5 w-3.5" />
          {t('request.curlCommand')}
        </Button>
        {isStreaming ? (
          <Button size="sm" variant="outline" onClick={cancelRequest}>
            <X className="mr-1.5 h-3.5 w-3.5" />
            {t('cancel')}
          </Button>
        ) : (
          <Button size="sm" onClick={sendRequest} disabled={isLoading}>
            <Play className="mr-1.5 h-3.5 w-3.5" />
            {isLoading ? t('sending') : t('send')}
          </Button>
        )}
      </div>

      <Tabs defaultValue="params" className="flex-1 overflow-hidden">
        <TabsList className="mx-3 mt-2">
          {pathParamNames.length > 0 && (
            <TabsTrigger value="params">{t('pathParams')}</TabsTrigger>
          )}
          {queryParamDefs.length > 0 && (
            <TabsTrigger value="query">{t('queryParams')}</TabsTrigger>
          )}
          <TabsTrigger value="headers">{t('headers')}</TabsTrigger>
          {endpoint.requestBody && (
            <TabsTrigger value="body">{t('body')}</TabsTrigger>
          )}
        </TabsList>

        {pathParamNames.length > 0 && (
          <TabsContent
            value="params"
            className="mx-3 space-y-2 overflow-y-auto"
          >
            {pathParamNames.map((name) => (
              <div key={name} className="space-y-1">
                <Label className="text-xs font-mono">{`{${name}}`}</Label>
                <Input
                  value={pathParams[name] || ''}
                  onChange={(e) => setPathParam(name, e.target.value)}
                  placeholder={t('request.enterPathParam', { name })}
                  className="h-8 font-mono text-xs"
                />
              </div>
            ))}
          </TabsContent>
        )}

        {queryParamDefs.length > 0 && (
          <TabsContent value="query" className="mx-3 space-y-2 overflow-y-auto">
            {queryParamDefs.map((p) => (
              <div key={p.name} className="space-y-1">
                <Label className="text-xs">
                  <span className="font-mono">{p.name}</span>
                  {p.required && (
                    <span className="ml-1 text-destructive">*</span>
                  )}
                  {p.description && (
                    <span className="ml-2 font-normal text-muted-foreground">
                      {p.description}
                    </span>
                  )}
                </Label>
                <Input
                  value={queryParams[p.name] || ''}
                  onChange={(e) => setQueryParam(p.name, e.target.value)}
                  placeholder={
                    (
                      p.schema as Record<string, unknown>
                    )?.default?.toString() || ''
                  }
                  className="h-8 font-mono text-xs"
                />
              </div>
            ))}
          </TabsContent>
        )}

        <TabsContent value="headers" className="mx-3 space-y-2 overflow-y-auto">
          {Object.entries(effectiveHeaders).map(([key, value]) => {
            const isAuto = isManagedHeader(key)
            return (
              <div key={key} className="flex items-center gap-2">
                <code className="w-40 shrink-0 truncate text-xs text-muted-foreground">
                  {key}
                </code>
                <Input
                  value={value}
                  onChange={(e) => setHeader(key, e.target.value)}
                  disabled={isAuto}
                  className="h-7 flex-1 font-mono text-xs"
                />
                {!isAuto && (
                  <Button
                    size="icon"
                    variant="ghost"
                    className="h-7 w-7"
                    onClick={() => removeHeader(key)}
                    aria-label={t('request.removeHeaderAria', {
                      header: key,
                    })}
                  >
                    <X className="h-3 w-3" />
                  </Button>
                )}
              </div>
            )
          })}
          <div className="flex items-center gap-2 pt-1">
            <Input
              value={customHeaderKey}
              onChange={(e) => setCustomHeaderKey(e.target.value)}
              placeholder={t('request.headerNamePlaceholder')}
              className="h-7 w-40 text-xs"
            />
            <Input
              value={customHeaderValue}
              onChange={(e) => setCustomHeaderValue(e.target.value)}
              placeholder={t('request.headerValuePlaceholder')}
              className="h-7 flex-1 text-xs"
              onKeyDown={(e) => e.key === 'Enter' && addCustomHeader()}
            />
            <Button
              size="icon"
              variant="ghost"
              className="h-7 w-7"
              onClick={addCustomHeader}
              aria-label={t('request.addHeaderAria')}
            >
              <Plus className="h-3 w-3" />
            </Button>
          </div>
        </TabsContent>

        {endpoint.requestBody && (
          <TabsContent
            value="body"
            className="flex-1 overflow-hidden px-3 pb-3"
          >
            <div className="h-full rounded border">
              <CodeEditor
                value={body}
                onChange={setBody}
                language="json"
                ariaLabel={t('request.bodyAria')}
                jsonLint
              />
            </div>
          </TabsContent>
        )}
      </Tabs>
    </div>
  )
}
