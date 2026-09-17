// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE TRANSPORT WITNESS, IN A REAL BROWSER. This entry is bundled by build.mjs against
// the UNMODIFIED product modules (`@/lib/api/client`, `@/features/communications`,
// `@/stores/session`, React Query) and served by probe.mjs to a real Chromium with a
// loopback HTTP receiver that counts the POSTs it actually reads. It is the shape of
// the independent review's witness of d8c5ad3e9e (F1), kept here so the causal
// controls can be re-run on any later source: the four refresh controls, the queued
// mutation with and without a same-turn rotation, and — through build.mjs --mutant —
// the same cases with the transport's dispatch guard removed.
//
// A synthetic fixture for the TRANSPORT only: no product authorization is claimed,
// and no engine is involved. Credentials are synthetic strings; none is logged.
import {
  QueryClient,
  QueryClientProvider,
  useMutation,
} from '@tanstack/react-query'
import { createElement, useLayoutEffect } from 'react'
import { createRoot } from 'react-dom/client'
import { sendNotice } from '@/features/communications/api'
import {
  buildSendIntent,
  useIntentGuard,
  type IntentGuard,
  type SendIntent,
} from '@/features/communications/intent'
import { __resetRefreshState, configureApiClient } from '@/lib/api/client'
import { useSessionStore } from '@/stores/session'

const tick = () =>
  new Promise<void>((r) =>
    requestAnimationFrame(() => requestAnimationFrame(() => r())),
  )
const session = (token: string, ttlMs: number) => ({
  token,
  sessionId: 'same-local-fixture-session',
  expiresAt: new Date(Date.now() + ttlMs).toISOString(),
})
const CRED_A = 'synthetic-review-credential-A'
const CRED_B = 'synthetic-review-credential-B'
const body = (subject: string) => ({
  channel_id: 'channel',
  recipient: { kind: 'user' as const, ref: 'recipient' },
  content: {
    subject,
    blocks: [
      {
        type: 'text' as const,
        text: 'local inert fixture',
        format: 'plain' as const,
      },
    ],
  },
})
const outcomeOf = (p: Promise<unknown>) =>
  p.then(
    () => 'resolved',
    (e: unknown) => (e instanceof Error ? e.name : 'other'),
  )

// ─── the four refresh controls: begin() before the send, the store rotates during the refresh
let guard: IntentGuard | null = null
function Harness() {
  const generation = useSessionStore((s) => s.credentialGeneration)
  const current = useIntentGuard({
    allowed: true,
    boundary: `tenant|user|c${generation}`,
  })
  useLayoutEffect(() => {
    guard = current
  })
  return null
}

declare global {
  interface Window {
    runCase: (replay: boolean, rotate: boolean) => Promise<unknown>
    runQueuedCase: (rotate: boolean, viaBegin: boolean) => Promise<unknown>
  }
}

window.runCase = async (replay: boolean, rotate: boolean) => {
  __resetRefreshState()
  guard = null
  await fetch('/fixture/case', {
    method: 'POST',
    body: JSON.stringify({ replay, rotate }),
  })
  useSessionStore
    .getState()
    .setSession(session(CRED_A, replay ? 3_600_000 : 10_000))
  const initialGeneration = useSessionStore.getState().credentialGeneration
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  root.render(createElement(Harness))
  await tick()
  configureApiClient({
    getToken: () => useSessionStore.getState().token,
    getTenant: () => 'tenant',
    onUnauthorized: () => {},
    getExpiresAt: () => useSessionStore.getState().expiresAt,
    refreshSession: async () => {
      const next = await (await fetch('/fixture/refresh')).json()
      useSessionStore.getState().setSession(next)
      return true
    },
  })
  const signal = guard!.begin()
  const intent = buildSendIntent(
    {
      tenant: 'tenant',
      workspace: 'workspace',
      boundary: `tenant|user|c${initialGeneration}`,
    },
    'channel',
    body('review'),
  )
  const outcome = await outcomeOf(
    sendNotice(intent, { tenant: 'tenant' }, signal ?? undefined),
  )
  await tick()
  const server = await (await fetch('/fixture/result')).json()
  const result = {
    case: 'refresh_control',
    replay,
    rotate,
    outcome,
    ...server,
    generationAdvanced:
      useSessionStore.getState().credentialGeneration !== initialGeneration,
    signalAbortedAtEnd: signal?.aborted ?? null,
  }
  root.unmount()
  container.remove()
  await tick()
  return result
}

// ─── the queued mutation: mutate(intent), then the store rotates IN THE SAME TURN
let currentMutation: { mutate: (i: SendIntent) => void } | null = null
let mutationResolved: ((x: string) => void) | null = null
function QueuedHarness({
  generation,
  viaBegin,
}: {
  generation: number
  viaBegin: boolean
}) {
  const current = useIntentGuard({
    allowed: true,
    boundary: `tenant|user|c${generation}`,
  })
  const mutation = useMutation({
    mutationFn: (intent: SendIntent) => {
      if (viaBegin) {
        const signal = current.begin()
        if (!signal) throw new Error('AuthorityLostError')
        return sendNotice(
          intent,
          { tenant: intent.scope.tenant, guard: current.check },
          signal,
        )
      }
      // No begin(), no signal, no surface guard: the transport alone decides.
      return sendNotice(intent, { tenant: intent.scope.tenant })
    },
    onSuccess: () => mutationResolved?.('resolved'),
    onError: (e: Error) => mutationResolved?.(e.name),
  })
  useLayoutEffect(() => {
    currentMutation = mutation
  })
  return null
}
function QueuedBoundary({ viaBegin }: { viaBegin: boolean }) {
  const generation = useSessionStore((s) => s.credentialGeneration)
  return createElement(QueuedHarness, { key: generation, generation, viaBegin })
}

window.runQueuedCase = async (rotate: boolean, viaBegin: boolean) => {
  __resetRefreshState()
  currentMutation = null
  await fetch('/fixture/case', {
    method: 'POST',
    body: JSON.stringify({ replay: false, rotate }),
  })
  useSessionStore.getState().setSession(session(CRED_A, 3_600_000))
  const generation = useSessionStore.getState().credentialGeneration
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  const qc = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  })
  root.render(
    createElement(
      QueryClientProvider,
      { client: qc },
      createElement(QueuedBoundary, { viaBegin }),
    ),
  )
  await tick()
  configureApiClient({
    getToken: () => useSessionStore.getState().token,
    getTenant: () => 'tenant',
    onUnauthorized: () => {},
    getExpiresAt: () => useSessionStore.getState().expiresAt,
    refreshSession: async () => false,
  })
  const intent = buildSendIntent(
    {
      tenant: 'tenant',
      workspace: 'workspace',
      boundary: `tenant|user|c${generation}`,
    },
    'channel',
    body('review queued'),
  )
  const settled = new Promise<string>((r) => (mutationResolved = r))
  currentMutation!.mutate(intent)
  // The store changes before React Query resumes its mutationFn. No fake role,
  // hook rerender, flushSync, held callback or test scheduler is used.
  if (rotate) useSessionStore.getState().setSession(session(CRED_B, 3_600_000))
  const outcome = await settled
  await tick()
  const server = await (await fetch('/fixture/result')).json()
  const result = {
    case: 'queued_mutation_boundary_remount',
    viaBegin,
    rotate,
    outcome,
    ...server,
    generationAdvanced:
      useSessionStore.getState().credentialGeneration !== generation,
  }
  root.unmount()
  container.remove()
  qc.clear()
  await tick()
  return result
}
