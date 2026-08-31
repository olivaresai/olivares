// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import type { ParsedEndpoint } from './openapi-parser'

export interface HistoryEntry {
  timestamp: number
  method: string
  path: string
  status: number
  durationMs: number
}

export interface ResponseState {
  status: number
  statusText: string
  headers: Record<string, string>
  body: string
  durationMs: number
  size: number
}

interface PlaygroundState {
  selectedEndpoint: ParsedEndpoint | null
  headers: Record<string, string>
  body: string
  pathParams: Record<string, string>
  queryParams: Record<string, string>
  response: ResponseState | null
  isLoading: boolean
  isStreaming: boolean
  history: HistoryEntry[]
  /** Monotonic token for the request whose lifecycle may write response state.
   * Bumped by beginRequest AND by selectEndpoint, so the async completion of a
   * request aborted by an endpoint switch can never contaminate the new
   * endpoint's panel (review finding).*/
  requestGeneration: number

  selectEndpoint: (ep: ParsedEndpoint | null) => void
  /** Marks a new in-flight request; only the returned generation may apply
   * response/loading/streaming state afterwards. */
  beginRequest: () => number
  setHeader: (key: string, value: string) => void
  removeHeader: (key: string) => void
  setBody: (body: string) => void
  setPathParam: (key: string, value: string) => void
  setQueryParam: (key: string, value: string) => void
  setResponse: (resp: ResponseState | null) => void
  setLoading: (loading: boolean) => void
  setStreaming: (streaming: boolean) => void
  addHistoryEntry: (entry: HistoryEntry) => void
  clearHistory: () => void
  reset: () => void
}

const MAX_HISTORY = 50

export const usePlayground = create<PlaygroundState>()(
  persist(
    (set, get) => ({
      selectedEndpoint: null,
      headers: {},
      body: '',
      pathParams: {},
      queryParams: {},
      response: null,
      isLoading: false,
      isStreaming: false,
      history: [],
      requestGeneration: 0,

      selectEndpoint: (ep) =>
        set((s) => ({
          selectedEndpoint: ep,
          body: '',
          pathParams: {},
          queryParams: {},
          response: null,
          // A pending request belongs to the PREVIOUS endpoint: invalidate its
          // generation and clear the flags it left behind.
          isLoading: false,
          isStreaming: false,
          requestGeneration: s.requestGeneration + 1,
        })),
      beginRequest: () => {
        set((s) => ({ requestGeneration: s.requestGeneration + 1 }))
        return get().requestGeneration
      },
      setHeader: (key, value) =>
        set((s) => ({ headers: { ...s.headers, [key]: value } })),
      removeHeader: (key) =>
        set((s) => {
          const h = { ...s.headers }
          delete h[key]
          return { headers: h }
        }),
      setBody: (body) => set({ body }),
      setPathParam: (key, value) =>
        set((s) => ({ pathParams: { ...s.pathParams, [key]: value } })),
      setQueryParam: (key, value) =>
        set((s) => ({ queryParams: { ...s.queryParams, [key]: value } })),
      setResponse: (response) => set({ response }),
      setLoading: (isLoading) => set({ isLoading }),
      setStreaming: (isStreaming) => set({ isStreaming }),
      addHistoryEntry: (entry) =>
        set((s) => ({
          history: [entry, ...s.history].slice(0, MAX_HISTORY),
        })),
      clearHistory: () => set({ history: [] }),
      reset: () =>
        set({
          selectedEndpoint: null,
          headers: {},
          body: '',
          pathParams: {},
          queryParams: {},
          response: null,
          isLoading: false,
          isStreaming: false,
        }),
    }),
    {
      name: 'olivares:api-playground',
      partialize: (s) => ({ history: s.history }),
    },
  ),
)
