// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery } from '@tanstack/react-query'
import { useState } from 'react'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { ProviderProfileDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { providerKeys } from './api'
import { useProviderBoundary } from './auth-boundary'
import { KIND_DRIVERS } from './kinds'
import type { ProviderRecordDTO } from './types'
import './i18n'

export function ProviderBindDialog({
  record,
  open,
  onOpenChange,
}: {
  record: ProviderRecordDTO
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation('providers')
  const { can, activeTenant } = useAuth()
  const boundary = useProviderBoundary()
  const [selected, setSelected] = useState('')
  const allowed = can('sessions:profile:read') && can('sessions:profile:write')
  const profiles = useInfiniteQuery({
    queryKey: [
      ...providerKeys.boundaryScope(activeTenant, boundary.epoch),
      'bind-profiles',
      record.provider_ref,
    ],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ signal, pageParam }) =>
      agentOpsApi.listProfiles(
        { state: 'active', cursor: pageParam },
        { signal },
      ),
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
    enabled: open && allowed,
  })
  const candidates = (
    profiles.data?.pages.flatMap((page) => page.items) ?? []
  ).filter(
    (profile) =>
      profile.state === 'active' &&
      KIND_DRIVERS[record.kind].includes(profile.driver),
  )
  const bind = usePrivilegedMutation<void, ProviderProfileDTO>({
    mutationFn: async (_vars, authority) => {
      if (
        !can('sessions:profile:write') ||
        !can('sessions:profile:read') ||
        record.state !== 'active'
      )
        throw Error(t('bind.unavailable'))
      const profile = await agentOpsApi.getProfile(selected, {
        signal: authority.signal,
      })
      if (
        profile.state !== 'active' ||
        !KIND_DRIVERS[record.kind].includes(profile.driver)
      )
        throw Error(t('bind.unavailable'))
      authority.dispatchGuard()
      return agentOpsApi.patchProfile(
        selected,
        {
          provider_record_ref: record.provider_ref,
          auth_source: 'managed_injection',
        },
        authority,
      )
    },
    stepUpAction: 'providers',
    successMessage: t('bind.success'),
    invalidateKeys: () => [agentOpsKeys.profiles(activeTenant, boundary.epoch)],
    onDone: () => onOpenChange(false),
  })
  return (
    <Dialog
      open={open}
      onOpenChange={(value) => {
        if (!bind.isPending) onOpenChange(value)
      }}
    >
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('bind.title')}</DialogTitle>
          <DialogDescription>
            {t('bind.description', { name: record.display_name })}
          </DialogDescription>
        </DialogHeader>
        {!allowed ? (
          <p role="alert">{t('bind.unavailable')}</p>
        ) : profiles.isPending ? (
          <Spinner />
        ) : profiles.isError ? (
          <p role="alert">{t('bind.loadError')}</p>
        ) : (
          <>
            <Field label={t('bind.profile')}>
              <Select
                value={selected}
                onValueChange={setSelected}
                disabled={bind.isPending}
              >
                <SelectTrigger>
                  <SelectValue placeholder={t('bind.choose')} />
                </SelectTrigger>
                <SelectContent>
                  {candidates.map((profile) => (
                    <SelectItem
                      key={profile.profile_ref}
                      value={profile.profile_ref}
                    >
                      {profile.display_name || profile.profile_ref} ·{' '}
                      {profile.driver}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            {candidates.length === 0 && (
              <p className="text-sm text-muted-foreground">{t('bind.empty')}</p>
            )}
            {profiles.hasNextPage && (
              <Button
                variant="secondary"
                disabled={profiles.isFetchingNextPage}
                onClick={() => void profiles.fetchNextPage()}
              >
                {t('bind.more')}
              </Button>
            )}
          </>
        )}
        <DialogFooter>
          <Button
            variant="secondary"
            disabled={bind.isPending}
            onClick={() => onOpenChange(false)}
          >
            {t('create.cancel')}
          </Button>
          <Button
            disabled={
              !allowed ||
              !candidates.some((p) => p.profile_ref === selected) ||
              bind.isPending
            }
            onClick={() => bind.mutate()}
          >
            {bind.isPending && <Spinner className="size-3.5" />}
            {t('bind.title')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
