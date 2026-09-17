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
import { Spinner } from '@/components/ui/spinner'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { providerKeys, providersApi } from './api'
import { useProviderBoundary } from './auth-boundary'
import type { ProviderRecordDTO } from './types'
import './i18n'

/**
 * ProviderRotateDialog — replaces the credential under the SAME reference.
 *
 * The dialog states the two consequences that are easy to get wrong, because both
 * of them are about time rather than about the form:
 *  · every profile bound to this provider keeps working, and the NEXT launch uses
 *    the new value — a session already running keeps the one it started with;
 *  · the previous connection test is cleared, because a green measured on a
 *    credential that no longer exists is not evidence about the one replacing it.
 */
export function ProviderRotateDialog({
  record,
  open,
  onOpenChange,
}: {
  record: ProviderRecordDTO
  open: boolean
  onOpenChange: (o: boolean) => void
}) {
  const { t } = useTranslation('providers')
  const { activeTenant } = useAuth()
  const boundary = useProviderBoundary()
  const [apiKey, setApiKey] = useState('')

  const rotate = usePrivilegedMutation<void, ProviderRecordDTO>({
    mutationFn: () =>
      providersApi.patch(record.provider_ref, { api_key: apiKey }),
    invalidateKeys: () => [providerKeys.list(activeTenant, boundary.epoch)],
    successMessage: t('rotate.success'),
    stepUpAction: 'providers',
    onDone: () => {
      setApiKey('')
      onOpenChange(false)
    },
  })

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (apiKey.trim() !== '' && !rotate.isPending) rotate.mutate()
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => (rotate.isPending ? undefined : onOpenChange(o))}
    >
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('rotate.title')}</DialogTitle>
          <DialogDescription>{t('rotate.description')}</DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="flex flex-col gap-3">
          <p className="text-xs text-muted-foreground">
            {t('rotate.verdictCleared')}
          </p>
          <Field
            label={t('create.apiKey')}
            description={t('create.apiKeyHint')}
          >
            <Input
              type="password"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder={t('create.apiKeyPlaceholder')}
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
              disabled={rotate.isPending}
            >
              {t('create.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={apiKey.trim() === '' || rotate.isPending}
            >
              {rotate.isPending && <Spinner className="size-3.5" />}
              {rotate.isPending ? t('rotate.submitting') : t('rotate.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
