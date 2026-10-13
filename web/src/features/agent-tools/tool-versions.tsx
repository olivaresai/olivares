// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { agentOpsApi } from '@/features/agentops/api'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { useTenantStore } from '@/stores/tenant'
import { agentToolsApi, agentToolsKeys } from './api'

interface Props {
  driver: string
  name: string
  installs: { version: string; state: string }[]
  disabledReason?: string
  epoch?: number
  workspacePins?: boolean
}

export function ToolVersions(props: Props) {
  const tenant = useTenantStore((s) => s.activeTenant)
  return <VersionSelection key={tenant ?? ''} {...props} tenant={tenant} />
}

function VersionSelection({
  driver,
  name,
  installs,
  disabledReason,
  workspacePins = true,
  epoch = 0,
  tenant,
}: Props & { tenant: string | null }) {
  const { t } = useTranslation('agentTools')
  const id = useId()
  const [workspace, setWorkspace] = useState('')
  const [version, setVersion] = useState('')
  const [action, setAction] = useState<{
    kind: 'pin' | 'remove'
    version: string
    workspace?: string
  } | null>(null)
  const pinKey = [...agentToolsKeys.all, epoch, 'pins', tenant]
  const pins = useQuery({
    queryKey: pinKey,
    queryFn: ({ signal }) => agentToolsApi.pins(tenant!, signal),
    enabled: !!tenant && workspacePins,
  })
  const workspaces = useQuery({
    queryKey: [...agentToolsKeys.all, epoch, 'pin-workspaces', tenant],
    queryFn: async ({ signal }) => {
      let page = await agentOpsApi.listWorkspaces(
        { limit: 200, state: 'active' },
        { tenant: tenant!, signal },
      )
      const items = [...page.items]
      const seen = new Set<string>()
      while (page.has_more) {
        if (!page.cursor || seen.has(page.cursor))
          throw new Error(t('versions.failed'))
        seen.add(page.cursor)
        page = await agentOpsApi.listWorkspaces(
          { limit: 200, state: 'active', cursor: page.cursor },
          { tenant: tenant!, signal },
        )
        items.push(...page.items)
      }
      return { items }
    },
    enabled: !!tenant && workspacePins,
  })
  const save = usePrivilegedMutation<{ workspace: string; version: string }>({
    mutationFn: (choice, authority) =>
      agentToolsApi.pin(
        tenant!,
        { driver, workspace_ref: choice.workspace, version: choice.version },
        authority,
      ),
    invalidateKeys: [pinKey],
    successMessage: t('versions.saved'),
    onDone: () => setAction(null),
    stepUpAction: 'console',
  })
  const remove = usePrivilegedMutation<string>({
    mutationFn: (release, authority) =>
      agentToolsApi.remove(driver, release, authority),
    invalidateKeys: [agentToolsKeys.all],
    successMessage: t('versions.removed'),
    onDone: () => setAction(null),
    stepUpAction: 'console',
  })
  const available = [
    ...new Set(
      installs
        .filter((row) => row.state === 'installed')
        .map((row) => row.version),
    ),
  ]
  const pending = save.isPending || remove.isPending
  const reason =
    disabledReason || (!tenant ? t('versions.chooseOrganization') : undefined)
  const error = pins.error ?? workspaces.error ?? save.error ?? remove.error
  return (
    <section
      aria-label={t('versions.title')}
      className="flex flex-col gap-3 border-t border-line pt-4"
    >
      <h3 className="text-body font-medium">{t('versions.title')}</h3>
      {workspacePins && (
        <>
          <p className="text-caption text-text-2">{t('versions.hint')}</p>
          <label htmlFor={`${id}-workspace`}>{t('versions.workspace')}</label>
          <select
            id={`${id}-workspace`}
            className="rounded border border-ctl-border bg-surface p-2"
            disabled={
              !!reason || !pins.isSuccess || !workspaces.isSuccess || pending
            }
            value={workspace}
            onChange={(e) => {
              const ref = e.target.value
              setWorkspace(ref)
              setVersion(
                pins.data?.pins.find(
                  (p) =>
                    p.driver === driver &&
                    p.workspace_ref === ref &&
                    !p.agent_ref,
                )?.version ?? '',
              )
            }}
          >
            <option value="">{t('versions.chooseWorkspace')}</option>
            {workspaces.data?.items.map((row) => (
              <option key={row.workspace_ref} value={row.workspace_ref}>
                {row.name || row.workspace_ref}
              </option>
            ))}
          </select>
          <label htmlFor={`${id}-version`}>{t('versions.version')}</label>
          <select
            id={`${id}-version`}
            className="rounded border border-ctl-border bg-surface p-2"
            value={version}
            onChange={(e) => setVersion(e.target.value)}
            disabled={!!reason || !workspace || pending}
          >
            <option value="">{t('versions.default')}</option>
            {available.map((release) => (
              <option key={release} value={release}>
                {release}
              </option>
            ))}
            {version && !available.includes(version) && (
              <option value={version}>
                {t('versions.unavailable', { version })}
              </option>
            )}
          </select>
          <Button
            disabled={!!reason || !workspace || !pins.isSuccess || pending}
            aria-describedby={reason ? `${id}-reason` : undefined}
            onClick={() => setAction({ kind: 'pin', workspace, version })}
          >
            {t('versions.save')}
          </Button>
        </>
      )}
      {workspacePins && tenant && (pins.isPending || workspaces.isPending) && (
        <p role="status">{t('versions.loading')}</p>
      )}
      {workspacePins && reason && (
        <p
          id={`${id}-reason`}
          role="status"
          className="text-caption text-text-2"
        >
          {reason}
        </p>
      )}
      {!!error && !action && (
        <p role="alert" className="text-body text-danger">
          {error instanceof Error ? error.message : t('versions.failed')}
        </p>
      )}
      {available.map((release) => (
        <Button
          key={release}
          variant="secondary"
          disabled={!!disabledReason || pending}
          aria-label={t('versions.removeName', { name, version: release })}
          onClick={() => setAction({ kind: 'remove', version: release })}
        >
          {t('versions.removeName', { name, version: release })}
        </Button>
      ))}
      <ConfirmDialog
        open={action !== null}
        onOpenChange={(open) => {
          if (!open) setAction(null)
        }}
        title={
          action?.kind === 'remove'
            ? t('versions.removeName', { name, version: action.version })
            : t('versions.save')
        }
        description={
          action?.kind === 'remove'
            ? t('versions.removeHint')
            : t('versions.pinHint', {
                name,
                version: action?.version || t('versions.default'),
              })
        }
        tone={action?.kind === 'remove' ? 'danger' : 'default'}
        confirmLabel={
          action?.kind === 'remove'
            ? t('versions.confirmRemoval')
            : t('versions.confirmPin')
        }
        pending={pending}
        confirmDisabled={!!disabledReason}
        onConfirm={() => {
          if (action?.kind === 'remove') remove.mutate(action.version)
          else if (action?.workspace)
            save.mutate({
              workspace: action.workspace,
              version: action.version,
            })
        }}
      >
        {!!error && (
          <p role="alert" className="text-body text-danger">
            {error instanceof Error ? error.message : t('versions.failed')}
          </p>
        )}
      </ConfirmDialog>
    </section>
  )
}
