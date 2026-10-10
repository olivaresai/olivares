// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { firstHourKeys } from '@/features/first-hour/api'
import { useId, useState, type FormEvent } from 'react'
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
import { LOCAL_HTTP_KINDS, PROVIDER_KINDS } from './kinds'
import type {
  CreateProviderRequest,
  ProviderKind,
  ProviderRecordDTO,
} from './types'
import './i18n'

/** The address a local Ollama listens on: the form fills it in whenever the provider is
 * ollama, whether the form opens on it (the setup wizard's "Add a local model") or the
 * operator picks it. */
const OLLAMA_BASE_URL = 'http://127.0.0.1:11434'
const defaultBaseURL = (kind: ProviderKind | '') =>
  kind === 'ollama' ? OLLAMA_BASE_URL : ''

/**
 * ProviderCreateDialog — THREE typed fields and one optional, and that is the whole
 * design decision.
 *
 * The reference product solves this with a free-form table of environment variables
 * per instance, which means the operator has to know that Claude reads
 * ANTHROPIC_BASE_URL, that the credential goes in ANTHROPIC_AUTH_TOKEN, and that
 * ANTHROPIC_API_KEY must then be set to an explicitly empty value. That is three
 * pieces of provider trivia to type into text boxes, and it is the console this screen
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
  initialKind = PROVIDER_KINDS[0],
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  /** The provider chosen when the form opens: the first one offered, or the one the
   * tool that sent the person here runs on (HU2-18). The form never opens on nothing. */
  initialKind?: ProviderKind
  /** Called with the new record so the caller can offer the next action — testing
   * it — instead of leaving the operator on a list with nothing to do. */
  onCreated?: (record: ProviderRecordDTO) => void
}) {
  const { t } = useTranslation('providers')
  const { activeTenant } = useAuth()
  const boundary = useProviderBoundary()

  const [kind, setKind] = useState<ProviderKind | ''>(initialKind)
  const [displayName, setDisplayName] = useState('')
  const [baseURL, setBaseURL] = useState(() => defaultBaseURL(initialKind))
  const [apiKey, setApiKey] = useState('')
  const [defaultModel, setDefaultModel] = useState('')
  const [advanced, setAdvanced] = useState(false)
  const missingFieldsId = useId()

  const reset = () => {
    setKind(initialKind)
    setDisplayName('')
    setBaseURL(defaultBaseURL(initialKind))
    setApiKey('')
    setDefaultModel('')
    setAdvanced(false)
  }

  const create = usePrivilegedMutation<void, ProviderRecordDTO>({
    mutationFn: () => {
      const body: CreateProviderRequest = {
        kind: kind as ProviderKind,
        // The name is the operator's label; with none typed it is the provider's own.
        display_name: displayName.trim() || t(`kinds.${kind}`),
        ...(kind === 'ollama' ? {} : { api_key: apiKey }),
        ...(baseURL.trim() ? { base_url: baseURL.trim() } : {}),
        ...(defaultModel.trim() ? { default_model: defaultModel.trim() } : {}),
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
      reset()
      onOpenChange(false)
      if (record) onCreated?.(record)
    },
  })

  // openai_compatible has no official endpoint to assume, so the engine requires
  // one. The form says so before the request rather than after the refusal.
  const endpointRequired = kind === 'openai_compatible' || kind === 'ollama'
  const missingFields = [
    ...(kind === '' ? [t('create.kind')] : []),
    ...(endpointRequired && !baseURL.trim() ? [t('create.baseURL')] : []),
    ...(kind !== 'ollama' && !apiKey.trim() ? [t('create.apiKey')] : []),
  ]
  const ready = missingFields.length === 0

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
          <DialogDescription>
            {kind ? t(`kindHints.${kind}`) : t('create.description')}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="flex flex-col gap-3">
          <Field label={t('create.kind')} required>
            <Select
              value={kind}
              onValueChange={(v) => {
                setKind(v as ProviderKind)
                setDefaultModel('')
                if (v === 'ollama') {
                  setBaseURL(OLLAMA_BASE_URL)
                  setApiKey('')
                } else if (kind === 'ollama' || v === 'gemini') {
                  setBaseURL('')
                }
              }}
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
          <Field
            label={t('create.name')}
            description={t(
              kind === 'ollama' ? 'create.localNameHint' : 'create.nameHint',
            )}
          >
            <Input
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder={
                kind ? t(`kinds.${kind}`) : t('create.namePlaceholder')
              }
              autoComplete="off"
            />
          </Field>
          {kind !== 'gemini' && (endpointRequired || advanced) ? (
            <Field
              label={t('create.baseURL')}
              required={endpointRequired}
              description={
                kind === 'ollama'
                  ? undefined
                  : t(
                      endpointRequired
                        ? 'create.requiredEndpointHint'
                        : LOCAL_HTTP_KINDS.includes(kind)
                          ? 'create.baseURLLocalHint'
                          : 'create.baseURLHint',
                    )
              }
            >
              <Input
                value={baseURL}
                onChange={(e) => setBaseURL(e.target.value)}
                placeholder={t('create.baseURLPlaceholder')}
                autoComplete="off"
                mono
              />
            </Field>
          ) : kind !== '' && kind !== 'gemini' ? (
            <Button
              type="button"
              variant="link"
              className="self-start"
              onClick={() => setAdvanced(true)}
            >
              {t('create.advanced')}
            </Button>
          ) : null}
          {kind !== 'ollama' && (
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
                // A credential must not reach the browser's own stores: no
                // autocomplete entry, no spellcheck dictionary, no autocapitalise.
                autoComplete="off"
                spellCheck={false}
                mono
              />
            </Field>
          )}
          {(advanced || endpointRequired) && (
            <Field
              label={t('defaultModel.title')}
              description={t('defaultModel.createHint')}
            >
              <Input
                value={defaultModel}
                onChange={(e) => setDefaultModel(e.target.value)}
                maxLength={64}
                autoComplete="off"
                mono
              />
            </Field>
          )}
          {!ready && (
            <p
              id={missingFieldsId}
              role="status"
              className="text-caption text-muted-foreground"
            >
              {t('create.missingFields', { fields: missingFields.join(', ') })}
            </p>
          )}
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
              aria-describedby={!ready ? missingFieldsId : undefined}
            >
              {create.isPending && <Spinner className="size-3.5" />}
              {create.isPending ? t('create.registering') : t('create.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
