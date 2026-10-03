// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The sentence of a conversation's system line, shared by the session view and the live
// console so a failure reads the same in both.
import type { TFunction } from 'i18next'
import type { ConversationItem } from './conversation-frames'

/** A provider call refused for its credential: retrying cannot fix it (HU2-13). */
function refusedKey(item: ConversationItem): boolean {
  return (
    item.systemKind === 'apiRetry' &&
    (item.httpStatus === 401 || item.summary === 'authentication_failed')
  )
}

/** System lines that report a failure, painted as one (HU2-12, HU2-13, HU2-23). */
export function systemFailed(item: ConversationItem): boolean {
  return (
    item.systemKind === 'turnFailed' ||
    item.systemKind === 'error' ||
    item.systemKind === 'protocolError' ||
    item.resultFailed === true ||
    refusedKey(item)
  )
}

/** The sentence of a system line. */
export function systemText(t: TFunction, item: ConversationItem): string {
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
