// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// "Use an API key instead" (HU2-18): the sign-in sheet itself turns into the key
// form, on the provider the tool runs on. The page and the dialog stay (#2551), the
// field takes the focus, Cancel returns to the sign-in with nothing kept, and the key
// leaves the browser once, in the create request.
import { useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Field, FieldError } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { providerKeys, providersApi } from '@/features/providers/api'
import { useProviderBoundary } from '@/features/providers/auth-boundary'
import type {
  CreateProviderRequest,
  ProviderKind,
  ProviderRecordDTO,
} from '@/features/providers/types'
import '@/features/providers/i18n'
import { useAuth } from '@/lib/auth/context'
import {
  failureText,
  usePrivilegedMutation,
} from '@/lib/hooks/use-privileged-mutation'
import { firstHourKeys, type SignInTool } from './api'

/** The API key each sign-in tool can run on instead of its own login (HU2-18). */
const KEY_KIND: Partial<Record<SignInTool, ProviderKind>> = {
  claude: 'anthropic',
  codex: 'openai',
  grok: 'xai',
  opencode: 'openai',
  'gemini-cli': 'gemini',
}

/**
 * The provider the key form adds for this sign-in, or none: only the organization's
 * default login runs on a key from Providers, and only a role that may add one
 * (sessions:provider:write, as on the Providers page) is offered the form.
 */
export function useKeyKind(
  driver: SignInTool,
  accountRef?: string,
): ProviderKind | undefined {
  const { can } = useAuth()
  if (accountRef || !can('sessions:provider:write')) return undefined
  return KEY_KIND[driver]
}

/** Names the key form's create in the mutation cache, so its host sheet can find it. */
const KEY_CREATE = ['first-hour', 'key-instead'] as const

/**
 * Whether a key form's write is in flight: its host sheet holds its own close (X,
 * Escape, overlay) meanwhile, because once the form unmounts its step-up owner is
 * retired and the write would finish with no toast, no refresh and no error report.
 * Read at the moment of the close, from the cache: a value from the last render misses
 * an Escape pressed right after the submit.
 */
export function useKeyBeingStored(): () => boolean {
  const qc = useQueryClient()
  return () => qc.isMutating({ mutationKey: KEY_CREATE }) > 0
}

/**
 * The trigger that re-renders when the key form unmounts: without help the browser
 * would drop focus to the body, so Cancel hands it back to the trigger it replaced.
 */
export function useKeyTriggerFocus(open: boolean) {
  const triggerRef = useRef<HTMLButtonElement>(null)
  const wasOpen = useRef(false)
  useEffect(() => {
    if (open) {
      wasOpen.current = true
      return
    }
    if (wasOpen.current) {
      wasOpen.current = false
      triggerRef.current?.focus()
    }
  }, [open])
  return triggerRef
}

/**
 * KeyInstead — the sign-in sheet's key form: the provider is the tool's own
 * (preselected, not chosen), one required field for the key, one optional name. The
 * create request and the invalidation are the same as ProviderCreateDialog's: the key
 * is sealed by the engine and never returned.
 */
export function KeyInstead({
  driver,
  onCancel,
  onCreated,
}: {
  driver: SignInTool
  /** Cancel returns to the sign-in sheet; the typed key is discarded. */
  onCancel: () => void
  /** After the key is stored: the caller shows what comes next. */
  onCreated?: (record: ProviderRecordDTO) => void
}) {
  const { t } = useTranslation(['providers', 'errors'])
  const { activeTenant } = useAuth()
  const boundary = useProviderBoundary()
  const kind = KEY_KIND[driver] as ProviderKind
  const [displayName, setDisplayName] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [error, setError] = useState<string>()
  const missingFieldsId = useId()

  const create = usePrivilegedMutation<void, ProviderRecordDTO>({
    // The host sheet holds its close while this runs (useKeyBeingStored).
    mutationKey: KEY_CREATE,
    mutationFn: () => {
      const body: CreateProviderRequest = {
        kind,
        // The name is the operator's label; with none typed it is the provider's own.
        display_name: displayName.trim() || t(`kinds.${kind}`),
        api_key: apiKey,
      }
      return providersApi.create(body)
    },
    invalidateKeys: () => [
      providerKeys.list(activeTenant, boundary.epoch),
      // What a tool can start on may change with the keys (the first-hour readiness).
      firstHourKeys.all(activeTenant),
    ],
    successMessage: t('create.success'),
    stepUpAction: 'providers',
    onDone: (record) => {
      setDisplayName('')
      setApiKey('')
      onCreated?.(record)
    },
    // The host sheet hides the toast from screen readers: say it here.
    onError: (err) => {
      setError(failureText(err) ?? t('errors:generic'))
      return true
    },
  })

  const missingFields = apiKey.trim() ? [] : [t('create.apiKey')]
  const ready = missingFields.length === 0

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (!ready || create.isPending) return
    setError(undefined)
    create.mutate()
  }

  return (
    <form
      onSubmit={onSubmit}
      className="flex flex-col gap-3 rounded-[8px] bg-surface p-3"
    >
      {/* The provider is fixed, not chosen: an informational row, not a control
       * (a required Field clones aria-required onto a non-labelable span). */}
      <div className="flex flex-col gap-1.5">
        <span className="text-body font-medium text-foreground select-none">
          {t('create.kind')}
        </span>
        <span className="text-body text-text">{t(`kinds.${kind}`)}</span>
      </div>
      <Field label={t('create.name')} description={t('create.nameHint')}>
        <Input
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
          placeholder={t(`kinds.${kind}`)}
          autoComplete="off"
        />
      </Field>
      <Field
        label={t('create.apiKey')}
        required
        description={t('create.apiKeyHint')}
      >
        <Input
          type="password"
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          placeholder={t('create.apiKeyPlaceholder')}
          autoFocus
          // A credential must not reach the browser's own stores: no
          // autocomplete entry, no spellcheck dictionary, no autocapitalise.
          autoComplete="off"
          spellCheck={false}
          mono
        />
      </Field>
      {!ready && (
        <p
          id={missingFieldsId}
          role="status"
          className="text-caption text-muted-foreground"
        >
          {t('create.missingFields', { fields: missingFields.join(', ') })}
        </p>
      )}
      <FieldError>{error}</FieldError>
      <div className="flex items-center justify-end gap-2">
        <Button
          type="button"
          variant="secondary"
          onClick={onCancel}
          disabled={create.isPending}
        >
          {t('create.cancel')}
        </Button>
        <Button
          type="submit"
          variant="primary"
          disabled={!ready || create.isPending}
          aria-describedby={!ready ? missingFieldsId : undefined}
        >
          {create.isPending && <Spinner className="size-3.5" />}
          {create.isPending ? t('create.registering') : t('create.submit')}
        </Button>
      </div>
    </form>
  )
}
