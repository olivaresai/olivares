// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { providerKeys, providersApi } from './api'
import { useProviderBoundary } from './auth-boundary'
import { PROVIDER_KINDS } from './kinds'
import type {
  CreateProviderRequest,
  ProviderKind,
  ProviderRecordDTO,
} from './types'
import './i18n'

/**
 * ProviderCreateDialog — THREE typed fields and one optional, and that is the whole
 * design decision.
 *
 * The reference product solves this with a free-form table of environment variables
 * per instance, which means the operator has to know that Claude reads
 * ANTHROPIC_BASE_URL, that the credential goes in ANTHROPIC_AUTH_TOKEN, and that
 * ANTHROPIC_API_KEY must then be set to an explicitly empty value. That is three
 * pieces of provider trivia to type into text boxes, and it is the console this lane
 * exists to replace. Here the operator chooses a provider and pastes a key.
 *
 * The key leaves the browser ONCE, in this request. It is not stored in component
 * state after the call, it is not echoed back by the engine, and there is no read
 * that returns it — including immediately after writing it.
 */
export function ProviderCreateDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  /** Called with the new record so the caller can offer the next action — testing
   * it — instead of leaving the operator on a list with nothing to do. */
  onCreated?: (record: ProviderRecordDTO) => void
}) {
  const { t } = useTranslation('providers')
  const { activeTenant } = useAuth()
  const boundary = useProviderBoundary()

  const [kind, setKind] = useState<ProviderKind | ''>('')
  const [displayName, setDisplayName] = useState('')
  const [baseURL, setBaseURL] = useState('')
  const [apiKey, setApiKey] = useState('')

  const reset = () => {
    setKind('')
    setDisplayName('')
    setBaseURL('')
    setApiKey('')
  }

  const create = usePrivilegedMutation<void, ProviderRecordDTO>({
    mutationFn: () => {
      const body: CreateProviderRequest = {
        kind: kind as ProviderKind,
        display_name: displayName.trim(),
        api_key: apiKey,
        ...(baseURL.trim() ? { base_url: baseURL.trim() } : {}),
      }
      return providersApi.create(body)
    },
    invalidateKeys: () => [providerKeys.list(activeTenant, boundary.epoch)],
    successMessage: t('create.success'),
    stepUpAction: 'providers',
    onDone: (record) => {
      reset()
      onOpenChange(false)
      if (record) onCreated?.(record)
    },
  })

  // openai_compatible has no official endpoint to assume, so the engine requires
  // one. The form says so before the request rather than after the refusal.
  const endpointRequired = kind === 'openai_compatible'
  const ready =
    kind !== '' &&
    displayName.trim() !== '' &&
    apiKey.trim() !== '' &&
    (!endpointRequired || baseURL.trim() !== '')

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (ready && !create.isPending) create.mutate()
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => (create.isPending ? undefined : onOpenChange(o))}
    >
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('create.title')}</DialogTitle>
          <DialogDescription>{t('create.description')}</DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="flex flex-col gap-3">
          <Field
            label={t('create.kind')}
            description={kind ? t(`kindHints.${kind}`) : undefined}
          >
            <Select
              value={kind}
              onValueChange={(v) => setKind(v as ProviderKind)}
            >
              <SelectTrigger>
                <SelectValue placeholder={t('create.kindPlaceholder')} />
              </SelectTrigger>
              <SelectContent>
                {PROVIDER_KINDS.map((k) => (
                  <SelectItem key={k} value={k}>
                    {t(`kinds.${k}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label={t('create.name')} description={t('create.nameHint')}>
            <Input
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder={t('create.namePlaceholder')}
              autoComplete="off"
            />
          </Field>
          <Field
            label={t('create.baseURL')}
            description={t('create.baseURLHint')}
          >
            <Input
              value={baseURL}
              onChange={(e) => setBaseURL(e.target.value)}
              placeholder={t('create.baseURLPlaceholder')}
              autoComplete="off"
              mono
            />
          </Field>
          <Field
            label={t('create.apiKey')}
            description={t('create.apiKeyHint')}
          >
            <Input
              type="password"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder={t('create.apiKeyPlaceholder')}
              // A credential must not reach the browser's own stores: no
              // autocomplete entry, no spellcheck dictionary, no autocapitalise.
              autoComplete="off"
              spellCheck={false}
              mono
            />
          </Field>
          <DialogFooter>
            <Button
              type="button"
              variant="secondary"
              onClick={() => onOpenChange(false)}
              disabled={create.isPending}
            >
              {t('create.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!ready || create.isPending}
            >
              {create.isPending && <Spinner className="size-3.5" />}
              {create.isPending ? t('create.submitting') : t('create.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
