// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { BoxedList } from '@/components/ui/boxed-list'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { http, type RequestOptions } from '@/lib/api'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { SettingsRow } from './settings-row'

// The additive API contract; the SDK is regenerated with the integration batch.
interface IdleSettings {
  idle_stop_minutes: number
}
export const sessionIdleApi = {
  get: () => http.get<IdleSettings>('/v1/system/session-idle'),
  save: (
    choice: IdleSettings,
    authority: Pick<RequestOptions, 'signal' | 'dispatchGuard'>,
  ) => http.put<IdleSettings>('/v1/system/session-idle', choice, authority),
}

export function SessionIdleSettings() {
  const { t } = useTranslation('settings')
  const { activeTenant, principal } = useAuth()
  const key = [
    'settings',
    'session-idle',
    activeTenant,
    principal?.actor,
  ] as const
  const qc = useQueryClient()
  const query = useQuery({
    queryKey: key,
    queryFn: () => sessionIdleApi.get(),
    retry: false,
  })
  const [draft, setDraft] = useState<number | null>(null)
  const save = usePrivilegedMutation<IdleSettings, IdleSettings>({
    mutationFn: sessionIdleApi.save,
    successMessage: t('sessionIdle.saved'),
    onDone: (state) => {
      qc.setQueryData(key, state)
      setDraft(null)
    },
  })
  if (query.isPending) return <Spinner />
  if (query.isError)
    return (
      <QueryErrorState error={query.error} retry={() => void query.refetch()} />
    )
  const stored = query.data.idle_stop_minutes
  const minutes = draft ?? stored
  const valid =
    minutes === 0 ||
    (Number.isInteger(minutes) && minutes > 5 && minutes <= 10080)
  return (
    <form
      className="flex max-w-xl flex-col gap-4"
      onSubmit={(event) => {
        event.preventDefault()
        if (valid) save.mutate({ idle_stop_minutes: minutes })
      }}
    >
      <section aria-labelledby="idle-stop" className="flex flex-col gap-2">
        <h2 id="idle-stop" className="text-body-l font-semibold text-text">
          {t('sessionIdle.group')}
        </h2>
        <BoxedList>
          <SettingsRow
            name={t('sessionIdle.stop')}
            description=""
            control={
              // On asks for the minutes when none were saved: 0 is off.
              <Switch
                aria-label={t('sessionIdle.stop')}
                checked={minutes !== 0}
                onCheckedChange={(on) => setDraft(on ? stored || NaN : 0)}
              />
            }
          />
          <SettingsRow
            name={t('sessionIdle.minutes')}
            description={t('sessionIdle.hint')}
            descriptionId="idle-stop-hint"
            control={
              <Input
                aria-label={t('sessionIdle.minutes')}
                className="w-28"
                type="number"
                min={0}
                max={10080}
                step={1}
                required
                value={Number.isFinite(minutes) ? minutes : ''}
                aria-describedby="idle-stop-hint"
                aria-invalid={!valid}
                onChange={(event) => setDraft(event.target.valueAsNumber)}
              />
            }
          />
        </BoxedList>
      </section>
      <Button type="submit" disabled={save.isPending || !valid}>
        {t('tracing.apply')}
      </Button>
    </form>
  )
}
