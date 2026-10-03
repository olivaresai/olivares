// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMemo } from 'react'
import { create } from 'zustand'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import type { ConversationItem } from './conversation-frames'

/** A first message the console sent to a session it started. */
export interface SentTurn {
  text: string
  /** Why the engine did not take it; absent when it did. */
  refused?: string
}

/** A note as kept: the turn and the sign-in and organization it belongs to. */
interface OwnedTurn extends SentTurn {
  partition: string
}

/** The sign-in and organization a note belongs to. Any credential change (a sign-out, a
 * sign-in, another person) moves the generation, so a note is never shown to anyone but
 * the session that started the run, in the organization it started in (SR4C on
 * 3cf9f18a). */
export function sentTurnsPartition(): string {
  const generation = useSessionStore.getState().credentialGeneration
  const tenant = useTenantStore.getState().activeTenant ?? ''
  return `${generation}\n${tenant}`
}

/** The first messages this console sent, by run, each with its owner (HU2-27). A tool
 * shows the person's message only once it takes the turn (Codex when the turn starts),
 * and a message the engine refused never reaches it: the session showed no trace of
 * what was asked. Kept in memory only; after a reload the tool's own record is what the
 * session shows. */
export const useSentTurns = create<{
  byRun: Record<string, readonly OwnedTurn[]>
  /** `partition` is sentTurnsPartition() taken when the start BEGAN: a start that
   * completes after a sign-out or an organization change notes nothing. */
  note: (partition: string, runRef: string, turn: SentTurn) => void
}>((set) => ({
  byRun: {},
  note: (partition, runRef, turn) => {
    if (partition !== sentTurnsPartition()) return
    set((s) => ({
      byRun: {
        ...s.byRun,
        [runRef]: [...(s.byRun[runRef] ?? []), { ...turn, partition }],
      },
    }))
  },
}))

// Every note is forgotten when the credential or the organization changes: a sign-out
// leaves no person's words behind in this tab.
useSessionStore.subscribe((s, prev) => {
  if (s.credentialGeneration !== prev.credentialGeneration)
    useSentTurns.setState({ byRun: {} })
})
useTenantStore.subscribe((s, prev) => {
  if (s.activeTenant !== prev.activeTenant) useSentTurns.setState({ byRun: {} })
})

function owned(
  turns: readonly OwnedTurn[] | undefined,
  partition: string,
): readonly SentTurn[] | undefined {
  const own = turns
    ?.filter((t) => t.partition === partition)
    .map(({ text, refused }) =>
      refused === undefined ? { text } : { text, refused },
    )
  return own?.length ? own : undefined
}

/** The notes of one run, for the sign-in and organization on screen now. */
export function sentTurnsOf(runRef: string): readonly SentTurn[] | undefined {
  return owned(useSentTurns.getState().byRun[runRef], sentTurnsPartition())
}

/** sentTurnsOf, subscribed: re-read when a note, the credential or the organization
 * changes. */
export function useRunSentTurns(
  runRef: string,
): readonly SentTurn[] | undefined {
  const generation = useSessionStore((s) => s.credentialGeneration)
  const tenant = useTenantStore((s) => s.activeTenant ?? '')
  const turns = useSentTurns((s) => s.byRun[runRef])
  return useMemo(
    () => owned(turns, `${generation}\n${tenant}`),
    [turns, generation, tenant],
  )
}

const words = (value: string) => value.replace(/\s+/g, ' ').trim()

/** The sent messages the tool has not shown yet: one the tool echoed (the same words as
 * one of its operator rows, each row counted once) is shown by that row instead. A
 * refused message never reached the tool, so it stays. */
export function unechoedTurns(
  items: readonly ConversationItem[],
  sent: readonly SentTurn[],
): SentTurn[] {
  const echoed = items
    .filter((i) => i.kind === 'operator')
    .map((i) => words(i.text ?? i.summary))
  return sent.filter((turn) => {
    if (turn.refused) return true
    const at = echoed.indexOf(words(turn.text))
    if (at < 0) return true
    echoed.splice(at, 1)
    return false
  })
}
