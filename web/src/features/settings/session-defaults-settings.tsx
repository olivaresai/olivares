// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ListTruncationBadge } from '@/features/_intel/notices'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { useProfileList } from '@/features/agent-tools/use-profiles'
import { useModelChoices, modelLabel } from '@/features/agentops/model-choices'
import { useReadyTools } from '@/features/first-hour/first-hour'
import { sessionTools, toolName } from '@/features/first-hour/api'
import {
  useReuseProfile,
  useSessionProfiles,
} from '@/features/first-hour/session-profiles'
import { useAuth } from '@/lib/auth/context'
import { useClientSettings, type SessionDefaults } from './preferences'
import { SettingsRow } from './settings-row'

const AUTOMATIC = ' automatic'

/** Choices come from the same live lists as New session, never from typed references. */
export function SessionDefaultsSettings() {
  const { t } = useTranslation('settings')
  const { t: firstHour } = useTranslation('firstHour')
  const defaults = useClientSettings((s) => s.sessionDefaults)
  const save = useClientSettings((s) => s.setSessionDefaults)
  const boundary = useAuthBoundary()
  const { can } = useAuth()
  const tools = useReadyTools()
  const list = useProfileList()
  const availableTools = sessionTools().filter(
    (d) =>
      tools.ready.includes(d) ||
      list.data?.accounts.some((a) => a.driver === d),
  )
  const tool =
    availableTools.find((d) => d === defaults.tool) ?? availableTools[0]
  const profiles = useSessionProfiles(
    tool,
    [],
    !!tool && tools.ready.includes(tool),
  )
  const account =
    profiles?.choices.find((p) => p.value === defaults.account && p.ready)
      ?.value ?? ''
  const key = tool && !account ? tools.boundKey(tool) : undefined
  const reuse = useReuseProfile(
    tool,
    !!tool && !account && !key && can('sessions:profile:read'),
  )
  const models = useModelChoices({
    profileRef: account || reuse.data?.profile?.profile_ref || '',
    driver: tool ?? '',
    model: '',
    enabled: !!tool,
  })
  const folders = useQuery({
    queryKey: [
      ...agentOpsKeys.boundaryScope(boundary.tenant, boundary.epoch),
      'workspaces',
      { state: 'active', limit: 200 },
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.listWorkspaces(
        { state: 'active', limit: 200 },
        { tenant: boundary.tenant, signal },
      ),
    enabled: !!boundary.tenant && can('sessions:workspace:read'),
  })
  const choices: Record<
    keyof SessionDefaults,
    { value: string; label: string }[]
  > = {
    tool: availableTools.map((d) => ({ value: d, label: toolName(d) })),
    account: (profiles?.choices ?? [])
      .filter((p) => p.ready && p.value)
      .map((p) => ({ value: p.value, label: p.label })),
    model: key
      ? (key.record.models ?? []).map((m) => ({ value: m, label: m }))
      : (models.catalog?.models ?? []).map((m) => ({
          value: m.id,
          label: modelLabel(m),
        })),
    folder: (folders.data?.items ?? [])
      .filter((w) => w.state === 'active' && w.root_path)
      .map((w) => ({
        value: w.workspace_ref,
        label:
          w.name && w.name !== w.root_path
            ? `${w.name} (${w.root_path})`
            : w.root_path,
      })),
  }
  return (
    <>
      {list.isError || folders.isError ? (
        <p role="status" className="text-body text-danger">
          {t('newSessions.readFailed')}
        </p>
      ) : null}
      {(['tool', 'account', 'model', 'folder'] as const).map((field) => {
        const name = t(`newSessions.${field}`)
        const value = defaults[field]
        const items = choices[field]
        const change = (value: string) =>
          save({
            [field]: value,
            ...(field === 'tool'
              ? { account: '', model: '' }
              : field === 'account'
                ? { model: '' }
                : {}),
          })
        return (
          <SettingsRow
            key={field}
            name={name}
            description={t('newSessions.automatic')}
            changed={!!value}
            onReset={() => save({ [field]: '' })}
            resetLabel={t('newSessions.reset', { name })}
            control={
              <Select
                value={value || AUTOMATIC}
                onValueChange={(v) => change(v === AUTOMATIC ? '' : v)}
              >
                <SelectTrigger aria-label={name} className="w-64 max-w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={AUTOMATIC}>
                    {t('newSessions.automatic')}
                  </SelectItem>
                  {value && !items.some((item) => item.value === value) ? (
                    <SelectItem value={value}>
                      {t('newSessions.unavailable')}
                    </SelectItem>
                  ) : null}
                  {items.map((item) => (
                    <SelectItem key={item.value} value={item.value}>
                      {item.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            }
          />
        )
      })}
      <ListTruncationBadge
        query={folders}
        filas={folders.data?.items.length ?? 0}
        label={firstHour('lists.foldersTruncated', {
          count: folders.data?.items.length ?? 0,
        })}
        hint={firstHour('lists.foldersHint')}
        className="p-0 [&_span]:leading-relaxed [&_span]:whitespace-normal"
      />
    </>
  )
}
