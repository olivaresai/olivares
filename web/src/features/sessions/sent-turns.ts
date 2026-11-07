// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMemo } from 'react'
import { create } from 'zustand'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import type { ConversationItem } from './conversation-frames'

/** A message the console sent to a session: the first one of a launch, or one from the
 * message bar. */
export interface SentTurn {
  text: string
  /** The attach `seq` of the newest frame the session had shown when this one was sent:
   * only a row a later frame started can be its echo. A seq never moves, where a row's
   * place in the shown window does (review of). Absent on a launch's first
   * message, which any row can echo. */
  after?: number
  /** How many rows of its words the session showed when it was sent. More of them now
   * means the tool showed it, even where the frame that did is gone: a Codex thread read
   * back from its store after a remount, or after the run left this node (review round 2
   * of). Absent with `after`.*/
  shown?: number
  /** Why the engine did not take it; absent when it did. */
  refused?: string
  /** Held until its launch is approved or its worktree setup ends, and the session runs; absent once sent. */
  waiting?: boolean
}

/** A note as kept: the turn and the sign-in and organization it belongs to. */
interface OwnedTurn extends SentTurn {
  partition: string
}

function partitionOf(sessionId: string | null, tenant: string | null): string {
  return `${sessionId ?? ''}\n${tenant ?? ''}`
}

/** The session and organization a note belongs to. A session id is one sign-in (a sign-out,
 * a sign-in or another person changes it), so a note is never shown to anyone but the
 * session that started the run, in the organization it started in (review on 3cf9f18a).
 * A renewal of the same session keeps its notes: its CSRF token moves, the session does
 * not. */
export function sentTurnsPartition(): string {
  return partitionOf(
    useSessionStore.getState().sessionId,
    useTenantStore.getState().activeTenant,
  )
}

/** The first messages this console sent, by run, each with its owner. A tool
 * shows the person's message only once it takes the turn (Codex when the turn starts),
 * and a message the engine refused never reaches it: the session showed no trace of
 * what was asked. Kept in memory only; after a reload the tool's own record is what the
 * session shows. */
export const useSentTurns = create<{
  byRun: Record<string, readonly OwnedTurn[]>
  /** The attach `seq` of the newest frame each run's conversation has shown. Kept across
   * a remount: the frames it names do not change. */
  seen: Record<string, number>
  /** How many operator rows of each words each run's conversation showed, as of `seen`. */
  said: Record<string, Readonly<Record<string, number>>>
  /** A held first message a session change dropped: the new session says so once, with no
   * words of it. Set by that change, cleared by dismissDiscarded when the conversation
   * shows it. */
  discarded: boolean
  dismissDiscarded: () => void
  see: (
    runRef: string,
    seq: number,
    said: Readonly<Record<string, number>>,
  ) => void
  /** Forgets the notes the tool has shown: its own rows tell them from now on. A note of
   * the same words still waiting needs a row after that one. */
  drop: (runRef: string, echoed: readonly Echo[]) => void
  /** `partition` is sentTurnsPartition() taken when the start BEGAN: a start that
   * completes after a sign-out or an organization change notes nothing. A note settles
   * the run's held turn with the same words. */
  note: (partition: string, runRef: string, turn: SentTurn) => void
}>((set) => ({
  byRun: {},
  seen: {},
  said: {},
  discarded: false,
  dismissDiscarded: () => set({ discarded: false }),
  see: (runRef, seq, said) =>
    set((s) =>
      s.seen[runRef] === seq && s.said[runRef] === said
        ? s
        : {
            seen: { ...s.seen, [runRef]: seq },
            said: { ...s.said, [runRef]: said },
          },
    ),
  drop: (runRef, echoed) =>
    set((s) => {
      const partition = sentTurnsPartition()
      let left = [...(s.byRun[runRef] ?? [])]
      for (const { turn, seq } of echoed) {
        const same = (t: OwnedTurn) =>
          t.partition === partition &&
          t.text === turn.text &&
          !t.refused &&
          !t.waiting
        const at = left.findIndex(same)
        if (at < 0) continue
        left.splice(at, 1)
        left = left.map((t) =>
          same(t)
            ? {
                ...t,
                ...(t.after !== undefined && seq !== undefined && seq > t.after
                  ? { after: seq }
                  : {}),
                ...(t.shown === undefined ? {} : { shown: t.shown + 1 }),
              }
            : t,
        )
      }
      if (left.length === (s.byRun[runRef] ?? []).length) return s
      return { byRun: { ...s.byRun, [runRef]: left } }
    }),
  note: (partition, runRef, turn) => {
    if (partition !== sentTurnsPartition()) return
    set((s) => ({
      byRun: {
        ...s.byRun,
        [runRef]: [
          ...(s.byRun[runRef] ?? []).filter(
            (t) => !(t.waiting && t.text === turn.text),
          ),
          {
            ...turn,
            partition,
          },
        ],
      },
    }))
  },
}))

// Every note is forgotten when the session or the organization changes: a sign-out leaves
// no person's words behind in this tab. A session change that drops a held first message
// leaves a word-free notice instead. A renewal of the same session keeps its notes.
useSessionStore.subscribe((s, prev) => {
  if (s.sessionId === prev.sessionId) return
  const { byRun, discarded } = useSentTurns.getState()
  const held = Object.values(byRun).some((turns) =>
    turns.some((t) => t.waiting),
  )
  useSentTurns.setState({
    byRun: {},
    seen: {},
    said: {},
    discarded: discarded || held,
  })
})
useTenantStore.subscribe((s, prev) => {
  if (s.activeTenant !== prev.activeTenant)
    useSentTurns.setState({ byRun: {}, seen: {}, said: {} })
})

function owned(
  turns: readonly OwnedTurn[] | undefined,
  partition: string,
): readonly SentTurn[] | undefined {
  const own = turns
    ?.filter((t) => t.partition === partition)
    .map(({ text, after, shown, refused, waiting }) => ({
      text,
      ...(after === undefined ? {} : { after }),
      ...(shown === undefined ? {} : { shown }),
      ...(refused === undefined ? {} : { refused }),
      ...(waiting ? { waiting } : {}),
    }))
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
  const sessionId = useSessionStore((s) => s.sessionId)
  const tenant = useTenantStore((s) => s.activeTenant)
  const turns = useSentTurns((s) => s.byRun[runRef])
  return useMemo(
    () => owned(turns, partitionOf(sessionId, tenant)),
    [turns, sessionId, tenant],
  )
}

const words = (value: string) => value.replace(/\s+/g, ' ').trim()

/** How many operator rows of each words a conversation shows. */
export function wordsShown(
  items: readonly ConversationItem[],
): Record<string, number> {
  const said: Record<string, number> = {}
  for (const item of items)
    if (item.kind === 'operator') {
      const w = words(item.text ?? item.summary)
      said[w] = (said[w] ?? 0) + 1
    }
  return said
}

/** Where a message sent now goes in its run's conversation: read at the send, because
 * the tool's echo can render before the engine's answer. */
export function sentPlace(
  runRef: string,
  text: string,
): Pick<SentTurn, 'after' | 'shown'> {
  const { seen, said } = useSentTurns.getState()
  const after = seen[runRef]
  if (after === undefined) return {}
  return { after, shown: said[runRef]?.[words(text)] ?? 0 }
}

/** A sent message the tool showed, and the `seq` of the row that shows it. */
export interface Echo {
  turn: SentTurn
  seq?: number
}

/** The sent messages the tool has not shown yet, and the ones it has: one the tool
 * echoed is shown by that row instead, each row counted once. Its echo is a row of the
 * same words started by a frame after the newest one shown when it was sent, or, where
 * that frame is gone, one more row of its words than were shown then. A refused or held
 * message never reached the tool, so it stays. */
export function settleTurns(
  items: readonly ConversationItem[],
  sent: readonly SentTurn[],
): { pending: SentTurn[]; echoed: Echo[] } {
  const rows: ({ seq?: number; words: string } | null)[] = items
    .filter((i) => i.kind === 'operator')
    .map((i) => ({ seq: i.seq, words: words(i.text ?? i.summary) }))
  const pending: SentTurn[] = []
  const echoed: Echo[] = []
  for (const turn of sent) {
    if (turn.refused || turn.waiting) {
      pending.push(turn)
      continue
    }
    const { after, shown } = turn
    const same = rows.flatMap((row, i) =>
      row?.words === words(turn.text) ? [i] : [],
    )
    let at = same.find((i) => {
      const seq = rows[i]!.seq
      return after === undefined || (seq !== undefined && seq > after)
    })
    if (at === undefined && shown !== undefined && same.length > shown)
      at = same[same.length - 1]
    if (at === undefined) {
      pending.push(turn)
      continue
    }
    echoed.push({ turn, seq: rows[at]!.seq })
    rows[at] = null
  }
  return { pending, echoed }
}
