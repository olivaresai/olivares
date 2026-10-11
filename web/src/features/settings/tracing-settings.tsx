// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useState } from 'react'
import { PANEL_EXTENSIONS } from '@/features/extensions'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Spinner } from '@/components/ui/spinner'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { http, type RequestOptions } from '@/lib/api'
import type { components } from '@/lib/api/openapi.gen'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'

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
      <div className="flex items-center justify-between gap-4">
        <label htmlFor="tracing-enabled">{t('tracing.enabled')}</label>
        <Switch
          id="tracing-enabled"
          checked={choices.enabled}
          onCheckedChange={(value) => change('enabled', value)}
        />
      </div>
      <label className="flex flex-col gap-1" htmlFor="tracing-endpoint">
        {t('tracing.endpoint')}
        <Input
          id="tracing-endpoint"
          value={choices.endpoint}
          onChange={(event) => change('endpoint', event.target.value)}
          required={choices.enabled}
          autoComplete="off"
        />
      </label>
      <label className="flex flex-col gap-1" htmlFor="tracing-protocol">
        {t('tracing.protocol')}
        <select
          id="tracing-protocol"
          className="h-9 rounded-md border border-ctl-border bg-canvas px-3"
          value={choices.protocol}
          onChange={(event) => change('protocol', event.target.value)}
        >
          <option value="grpc">OTLP/gRPC</option>
          <option value="http/protobuf">OTLP/HTTP (protobuf)</option>
        </select>
      </label>
      <div className="flex items-center justify-between gap-4">
        <label htmlFor="tracing-insecure">{t('tracing.insecure')}</label>
        <Switch
          id="tracing-insecure"
          checked={choices.insecure}
          onCheckedChange={(value) => change('insecure', value)}
        />
      </div>
      <label className="flex flex-col gap-1" htmlFor="tracing-sample">
        {t('tracing.sampleRatio')}
        <Input
          id="tracing-sample"
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
      </label>
      <label className="flex flex-col gap-1" htmlFor="tracing-service">
        {t('tracing.serviceName')}
        <Input
          id="tracing-service"
          value={choices.service_name}
          required
          maxLength={256}
          onChange={(event) => change('service_name', event.target.value)}
        />
      </label>
      <div className="flex items-center justify-between gap-4">
        <label htmlFor="tracing-compat">{t('tracing.compat')}</label>
        <Switch
          id="tracing-compat"
          checked={choices.genai_compat}
          onCheckedChange={(value) => change('genai_compat', value)}
        />
      </div>
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
