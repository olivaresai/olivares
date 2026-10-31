// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The sentence of a conversation's system line, shared by the session view and the live
// console so a failure reads the same in both.
import type { TFunction } from 'i18next'
import { modelName } from '@/features/agentops/model-name'
import { formatShortDateTime } from '@/lib/format'
import type { ConversationItem } from './conversation-frames'

/** A provider call refused for its credential: retrying cannot fix it. */
function refusedKey(item: ConversationItem): boolean {
  return (
    item.systemKind === 'apiRetry' &&
    (item.httpStatus === 401 || item.summary === 'authentication_failed')
  )
}

/** System lines that report a failure, painted as one (acceptance, acceptance, acceptance). */
export function systemFailed(item: ConversationItem): boolean {
  return (
    item.systemKind === 'turnFailed' ||
    item.systemKind === 'error' ||
    item.systemKind === 'protocolError' ||
    item.resultFailed === true ||
    refusedKey(item)
  )
}

/** The windows Claude Code names a limit by (`rateLimitType`), as the catalog words them. */
const LIMIT_WINDOWS: Record<string, string> = {
  five_hour: 'fiveHour',
  seven_day: 'weekly',
  seven_day_opus: 'weeklyOpus',
  seven_day_sonnet: 'weeklySonnet',
  overage: 'extra',
}

/** When a limit resets: the time when it is today, else the weekday and the time
 * ("Mon 03:00"); every window resets within a week. */
function resetTime(iso: string | undefined, now: Date): string | undefined {
  const at = iso ? new Date(iso) : null
  if (!at || Number.isNaN(at.getTime())) return undefined
  return at.toLocaleString(
    undefined,
    at.toDateString() === now.toDateString()
      ? { hour: '2-digit', minute: '2-digit' }
      : { weekday: 'short', hour: '2-digit', minute: '2-digit' },
  )
}

/** The turn a limit stopped, in words: which limit, how much of it was used and when it
 * resets, as far as the tool said. */
function limitText(t: TFunction, item: ConversationItem, now: Date): string {
  const window = t(
    `sessions:conversation.limit.window.${
      Object.hasOwn(LIMIT_WINDOWS, item.limitWindow ?? '')
        ? LIMIT_WINDOWS[item.limitWindow!]
        : 'usage'
    }`,
  )
  const used =
    item.limitUtilization === undefined
      ? undefined
      : Math.round(item.limitUtilization * 100)
  const time = resetTime(item.limitResetsAt, now)
  const key = `${used === undefined ? 'reached' : 'reachedUsed'}${time ? 'Until' : ''}`
  return t(`sessions:conversation.limit.${key}`, { window, used, time })
}

/** The sentence of a system line. */
export function systemText(
  t: TFunction,
  item: ConversationItem,
  now: Date = new Date(),
): string {
  if (item.systemKind === 'rate_limit') return limitText(t, item, now)
  if (item.systemKind === 'output')
    return t('sessions:conversation.output', {
      count: item.raw.length,
      first: item.summary,
    })
  if (item.systemKind === 'protocol')
    return t('sessions:conversation.protocol', { count: item.raw.length })
  if (item.systemKind === 'turnFailed')
    return t('sessions:conversation.turnFailed', { message: item.summary })
  if (refusedKey(item)) return t('sessions:conversation.keyRefused')
  if (item.systemKind === 'model') {
    // A model is named in words; its id stays on the row's hover.
    const words = {
      from: item.fromModel && modelName(item.fromModel),
      to: item.model && modelName(item.model),
    }
    return item.at
      ? t('sessions:conversation.modelChangedAt', {
          ...words,
          time: formatShortDateTime(item.at),
        })
      : t('sessions:conversation.modelChanged', words)
  }
  if (item.systemKind === 'init') {
    const parts = [t('sessions:conversation.started')]
    if (item.model) parts.push(modelName(item.model))
    if (item.toolCount)
      parts.push(
        t('sessions:conversation.startedTools', { count: item.toolCount }),
      )
    return parts.join(' · ')
  }
  if (item.systemKind === 'apiRetry')
    return t('sessions:conversation.retrying', {
      count: item.attempts ?? 1,
      message:
        item.httpStatus !== undefined
          ? `${item.summary} (${item.httpStatus})`
          : item.summary,
    })
  if (item.systemKind === 'retrying')
    return t('sessions:conversation.retrying', {
      count: item.attempts ?? 1,
      message: item.summary,
    })
  return item.summary
}
