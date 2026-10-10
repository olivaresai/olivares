// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// One row per profile of a tool: its name, how it signs in and whether it is ready, then
// one state word or one Sign in button, then an arrow that opens the profile.
import { ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { useProfileProviders } from '@/features/agentops/profile-authentication'
import type { SignInTool } from '@/features/first-hour/api'
import { useAccountSignInStatus } from '@/features/agentops/provider-account-signin'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import {
  useToolReadiness,
  useToolStatus,
} from '@/features/first-hour/first-hour'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'

/** What a row says about a profile, in the words the page prints. */
export type ProfileState =
  | { kind: 'account'; signedIn: boolean | undefined; failed?: boolean }
  | { kind: 'key'; name?: string; hint?: string }

export interface ProfileRowModel {
  /** The row's stable id: the account's name, or `default` for the default login. */
  id: string
  /** What the row is called: the account's name, or "Default sign-in". */
  name: string
  state: ProfileState
  /** Ready to start a session. */
  ready: boolean
  /** The profile cannot run: switched off, or a key it needs is gone. */
  off?: boolean
}

const DOT: Record<'ready' | 'wait' | 'off', string> = {
  ready: 'bg-ok',
  wait: 'bg-warn',
  off: 'bg-text-3',
}

/** A dot and a word, no box. The word carries the meaning; the dot only repeats it. */
export function StateChip({
  tone,
  children,
}: {
  tone: keyof typeof DOT
  children: string
}) {
  return (
    <span className="inline-flex shrink-0 items-center gap-1.5 text-overline text-text-2">
      <span aria-hidden className={cn('size-1.5 rounded-full', DOT[tone])} />
      {children}
    </span>
  )
}

/** "Account · signed in", "API key · Local stub (sk-…7f2a)". */
export function useProfileDetail(state: ProfileState): string {
  const { t } = useTranslation('agentTools')
  if (state.kind === 'key') {
    if (!state.name) return t('profiles.keyNone')
    return state.hint
      ? t('profiles.keyDetail', { name: state.name, hint: state.hint })
      : t('profiles.keyName', { name: state.name })
  }
  if (state.failed) return t('profiles.accountUnreadable')
  if (state.signedIn === undefined) return t('profiles.accountChecking')
  return t(
    state.signedIn ? 'profiles.accountSignedIn' : 'profiles.accountNotSignedIn',
  )
}

export function ProfileRowView({
  model,
  onOpen,
  onSignIn,
}: {
  model: ProfileRowModel
  onOpen: () => void
  onSignIn: () => void
}) {
  const { t } = useTranslation('agentTools')
  const detail = useProfileDetail(model.state)
  const needsSignIn =
    model.state.kind === 'account' && model.state.signedIn === false
  // The whole row opens the profile (the name's button covers it); the state, or the
  // Sign in button above it, comes before the arrow, which ends the row.
  return (
    <li
      className="relative flex min-h-14 items-center gap-3 px-4 py-2 hover:bg-hover"
      data-testid={`profile-${model.id}`}
    >
      <button
        type="button"
        onClick={onOpen}
        className={cn(
          'flex min-w-0 flex-1 flex-col rounded-ctl py-1 text-left outline-none',
          'after:absolute after:inset-0 after:content-[""]',
          'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus',
        )}
      >
        <span className="truncate text-body font-medium text-text">
          {model.name}
        </span>
        <span className="truncate text-caption text-text-2">{detail}</span>
      </button>
      <span className="relative z-10 flex shrink-0 items-center">
        {needsSignIn ? (
          <Button variant="secondary" size="sm" onClick={onSignIn}>
            {t('profiles.signIn')}
            <span className="sr-only"> {model.name}</span>
          </Button>
        ) : model.off ? (
          <StateChip tone="off">{t('profiles.off')}</StateChip>
        ) : model.ready ? (
          <StateChip tone="ready">{t('profiles.ready')}</StateChip>
        ) : (
          <StateChip tone="wait">{t('profiles.needsKey')}</StateChip>
        )}
      </span>
      <ChevronRight
        aria-hidden
        data-slot="row-arrow"
        className="size-4 shrink-0 text-text-3"
      />
    </li>
  )
}

/** The key a profile runs on, by name and hint, when it runs on one. */
function useKeyOf(account: ProviderAccountDTO): {
  name?: string
  hint?: string
} {
  const keyed =
    account.auth_source === 'managed_injection' && !!account.provider_record_ref
  // The key is named whatever its kind: the engine already checked it serves the tool.
  const { records } = useProfileProviders(null, keyed)
  if (!keyed) return {}
  const record = records.find(
    (r) => r.provider_ref === account.provider_record_ref,
  )
  return record
    ? { name: record.display_name || record.kind, hint: record.key_hint }
    : {}
}

/** The model of an account row: its sign-in read from the tool, or the key it runs on. */
export function useAccountRow(account: ProviderAccountDTO): ProfileRowModel {
  const { activeTenant } = useAuth()
  const keyed = account.auth_source === 'managed_injection'
  const status = useAccountSignInStatus(
    account,
    activeTenant ?? '',
    !keyed && !!activeTenant && account.state === 'active',
  )
  const key = useKeyOf(account)
  const off = account.state !== 'active'
  if (keyed)
    return {
      id: account.name,
      name: account.name,
      state: { kind: 'key', ...key },
      ready: !off && !!account.provider_record_ref,
      off,
    }
  const signedIn = status.data ? status.data.signed_in : undefined
  return {
    id: account.name,
    name: account.name,
    state: { kind: 'account', signedIn, failed: status.isError },
    ready: !off && signedIn === true,
    off,
  }
}

/** The tenant's default login: the first profile of a tool until a named profile is it. It
 * has no name of its own, so it is called what it is, never the tool's key. It signs in
 * with the tool's own login, or runs on the key the tool resolves to. */
export function useDefaultRow(driver: string): {
  model: ProfileRowModel
  /** The sign-in route refused the tool: it has no login of its own. */
  noSignIn: boolean
} {
  const { t } = useTranslation('agentTools')
  const status = useToolStatus(driver as SignInTool)
  const answer = useToolReadiness(driver)
  const signedIn = status.data ? status.data.signed_in : undefined
  // A key the provider refused runs nothing: the tool's own sign-in stays the way in.
  const key =
    signedIn === false && answer?.reason === 'api_key' && answer.ready
      ? answer.provider
      : undefined
  // The readiness names the key; its hint is on the record.
  const { records } = useProfileProviders(driver, !!key)
  const hint = records.find(
    (r) => r.provider_ref === key?.provider_ref,
  )?.key_hint
  const noSignIn =
    status.isError &&
    typeof status.error === 'object' &&
    (status.error as { status?: number }).status === 400
  const model: ProfileRowModel = key
    ? {
        id: 'default',
        name: t('profiles.default'),
        state: {
          kind: 'key',
          name: key.display_name || key.kind,
          hint,
        },
        ready: true,
      }
    : {
        id: 'default',
        name: t('profiles.default'),
        state: { kind: 'account', signedIn, failed: status.isError },
        ready: signedIn === true,
      }
  return { model, noSignIn }
}
