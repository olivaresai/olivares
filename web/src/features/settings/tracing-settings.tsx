// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useState } from 'react'
import { PANEL_EXTENSIONS } from '@/features/extensions'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { BoxedList } from '@/components/ui/boxed-list'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Segmented } from '@/components/ui/segmented'
import { Switch } from '@/components/ui/switch'
import { Spinner } from '@/components/ui/spinner'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { http, type RequestOptions } from '@/lib/api'
import type { components } from '@/lib/api/openapi.gen'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { SettingsRow } from './settings-row'

export type TracingChoices = components['schemas']['TracingSettings']
export type TracingStatus = components['schemas']['TracingStatus']
export const tracingApi = {
  get: () => http.get<TracingStatus>('/v1/system/tracing'),
  save: (
    settings: TracingChoices,
    authority: Pick<RequestOptions, 'signal' | 'dispatchGuard'>,
  ) => http.put<TracingStatus>('/v1/system/tracing', settings, authority),
}

export function TracingSettings() {
  const { t } = useTranslation('settings')
  const { activeTenant, principal } = useAuth()
  const key = ['settings', 'tracing', activeTenant, principal?.actor] as const
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: key,
    queryFn: () => tracingApi.get(),
    retry: false,
    refetchOnWindowFocus: false,
  })
  const [draft, setDraft] = useState<TracingChoices | null>(null)
  const save = usePrivilegedMutation<TracingChoices, TracingStatus>({
    mutationFn: tracingApi.save,
    successMessage: t('tracing.saved'),
    onDone: (state) => {
      queryClient.setQueryData(key, state)
      setDraft(null)
    },
  })
  if (query.isPending) return <Spinner />
  if (query.isError)
    return (
      <QueryErrorState error={query.error} retry={() => void query.refetch()} />
    )
  const choices = draft ?? query.data.settings
  const change = (
    field: keyof TracingChoices,
    value: string | number | boolean,
  ) => setDraft({ ...choices, [field]: value })
  return (
    <form
      className="flex max-w-xl flex-col gap-4"
      onSubmit={(event) => {
        event.preventDefault()
        save.mutate(choices)
      }}
    >
      <h2 className="text-heading-sm font-semibold">{t('nav.tracing')}</h2>
      <p className="text-body-sm text-text-2">{t('tracing.description')}</p>
      {!PANEL_EXTENSIONS.operationsExportAvailable ? (
        <p role="status" className="text-body-sm text-text-2">
          {t('tracing.businessRequired')}
        </p>
      ) : null}
      <section aria-labelledby="tracing-export" className="flex flex-col gap-2">
        <h3 id="tracing-export" className="text-body-l font-semibold text-text">
          {t('tracing.export')}
        </h3>
        <BoxedList>
          <SettingsRow
            name={t('tracing.enabled')}
            description=""
            control={
              <Switch
                aria-label={t('tracing.enabled')}
                checked={choices.enabled}
                onCheckedChange={(value) => change('enabled', value)}
              />
            }
          />
          <SettingsRow
            name={t('tracing.endpoint')}
            description=""
            control={
              <Input
                aria-label={t('tracing.endpoint')}
                className="w-72 max-w-full"
                value={choices.endpoint}
                onChange={(event) => change('endpoint', event.target.value)}
                required={choices.enabled}
                autoComplete="off"
              />
            }
          />
          <SettingsRow
            name={t('tracing.protocol')}
            description=""
            control={
              <Segmented<TracingChoices['protocol']>
                aria-label={t('tracing.protocol')}
                size="sm"
                value={choices.protocol}
                onValueChange={(value) => change('protocol', value)}
                options={[
                  { value: 'grpc', label: 'OTLP/gRPC' },
                  { value: 'http/protobuf', label: 'OTLP/HTTP (protobuf)' },
                ]}
              />
            }
          />
          <SettingsRow
            name={t('tracing.sampleRatio')}
            description=""
            control={
              <Input
                aria-label={t('tracing.sampleRatio')}
                className="w-28"
                type="number"
                min="0"
                max="1"
                step="0.01"
                required
                value={choices.sample_ratio}
                onChange={(event) =>
                  change('sample_ratio', Number(event.target.value))
                }
              />
            }
          />
          <SettingsRow
            name={t('tracing.serviceName')}
            description=""
            control={
              <Input
                aria-label={t('tracing.serviceName')}
                className="w-56 max-w-full"
                value={choices.service_name}
                required
                maxLength={256}
                onChange={(event) => change('service_name', event.target.value)}
              />
            }
          />
        </BoxedList>
      </section>
      <section
        aria-labelledby="tracing-advanced"
        className="flex flex-col gap-2"
      >
        <h3
          id="tracing-advanced"
          className="text-body-l font-semibold text-text"
        >
          {t('tracing.advanced')}
        </h3>
        <BoxedList>
          <SettingsRow
            name={t('tracing.insecure')}
            description=""
            control={
              <Switch
                aria-label={t('tracing.insecure')}
                checked={choices.insecure}
                onCheckedChange={(value) => change('insecure', value)}
              />
            }
          />
          <SettingsRow
            name={t('tracing.compat')}
            description=""
            control={
              <Switch
                aria-label={t('tracing.compat')}
                checked={choices.genai_compat}
                onCheckedChange={(value) => change('genai_compat', value)}
              />
            }
          />
        </BoxedList>
      </section>
      {query.data.overrides.length > 0 ? (
        <p role="status" className="break-words text-body-sm text-text-2">
          {t('tracing.overrides', { names: query.data.overrides.join(', ') })}
        </p>
      ) : null}
      <p className="text-body-sm text-text-2">
        {t(
          query.data.effective.enabled ? 'tracing.active' : 'tracing.inactive',
        )}
      </p>
      <Button type="submit" disabled={save.isPending}>
        {t('tracing.apply')}
      </Button>
    </form>
  )
}
