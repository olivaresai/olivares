// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE GLYPH AND THE WORD OF A SESSION'S STATUS, DEFINED ONCE. The classifier
// (`sessionDisplayState`) says which of the five a session is in; this file says how each
// is drawn and named, for the Sessions list, Now, the palette and the Business cockpit.
//
// ⛔ THE SHAPE DIFFERS PER STATUS AND THE WORD IS ALWAYS THERE, so colour never carries a
//    state alone, and colour appears only where a person must look: Needs you and Failed.
import { CircleCheck, CircleX, Pause } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import type { SessionStatus } from './session-display-state'
import './i18n'
import '@/features/agentops/i18n'

/** The word of each status: the catalogs' existing words, pinned equal in every language
 * by `lib/i18n/session-state-words.test.ts`. */
const WORD_KEY: Record<SessionStatus, string> = {
  working: 'agentops:state.running',
  'needs-you': 'sessions:list.group.attention',
  done: 'agentops:state.done',
  failed: 'agentops:state.failed',
  idle: 'agentops:state.idle',
}

/** The word of a status, in the person's language. */
export function useStatusWord(): (status: SessionStatus) => string {
  const { t } = useTranslation(['sessions', 'agentops'])
  return (status) => t(WORD_KEY[status])
}

export function SessionStatusMark({
  status,
  decorative = false,
  className,
}: {
  status: SessionStatus
  /** Beside the word it draws: out of the accessibility tree, since the word names it. */
  decorative?: boolean
  className?: string
}) {
  const word = useStatusWord()(status)
  const name = decorative
    ? ({ 'aria-hidden': true } as const)
    : ({ role: 'img', 'aria-label': word } as const)
  const common = { 'data-status': status, ...name }
  if (status === 'working')
    return (
      <span
        {...common}
        data-shape="disc"
        className={cn(
          'm-0.5 inline-block size-2.5 shrink-0 rounded-full bg-text',
          className,
        )}
      />
    )
  if (status === 'needs-you')
    return (
      <span
        {...common}
        data-shape="diamond"
        className={cn(
          'm-0.5 inline-block size-2.5 shrink-0 rotate-45 rounded-[2px] bg-warning',
          className,
        )}
      />
    )
  const Icon =
    status === 'done' ? CircleCheck : status === 'failed' ? CircleX : Pause
  const shape =
    status === 'done' ? 'check' : status === 'failed' ? 'cross' : 'pause'
  return (
    <Icon
      {...common}
      data-shape={shape}
      className={cn(
        'size-3.5 shrink-0',
        status === 'failed' ? 'text-danger' : 'text-text-3',
        className,
      )}
    />
  )
}
