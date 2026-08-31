// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { History, Pencil, Plus, Trash2 } from 'lucide-react'
import { type ReactNode, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { KvList, KvRow } from '@/components/ui/kv'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { SecretRef } from '@/components/data/secret-ref'
import { StatusBadge } from '@/components/data/badges'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { RelTimeLabel } from '@/features/shared'
import { ToolAnnotations } from './annotations'
import { capabilitiesApi, capabilitiesKeys } from './api'
import { ConfigEditorDialog } from './config-editor'
import { RevisionsSheet } from './revisions'
import './i18n'
import type { ServerDetailDTO } from './types'

const CONNECTION_VARIANT: Record<string, BadgeVariant> = {
  connected: 'success',
  degraded: 'warning',
  down: 'danger',
  unknown: 'neutral',
}

function Section({
  title,
  caption,
  action,
  children,
}: {
  title: ReactNode
  caption?: ReactNode
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <section className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-medium text-foreground">{title}</h3>
        {action}
      </div>
      {caption && <p className="text-xs text-muted-foreground">{caption}</p>}
      {children}
    </section>
  )
}

export interface ServerDetailSheetProps {
  serverId: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function ServerDetailSheet({
  serverId,
  open,
  onOpenChange,
}: ServerDetailSheetProps) {
  const { t } = useTranslation(['capabilities', 'common'])
  const { activeTenant, can } = useAuth()
  const canWrite = can('capabilities:config:write')
  const canReadConfig = can('capabilities:config:read')

  const [editorOpen, setEditorOpen] = useState(false)
  const [revisionsOpen, setRevisionsOpen] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const query = useQuery({
    queryKey: capabilitiesKeys.server(activeTenant, serverId ?? ''),
    queryFn: () => capabilitiesApi.getServer(serverId!),
    enabled: open && !!serverId,
    refetchInterval: open ? 30_000 : false,
  })
  const detail = query.data

  const remove = usePrivilegedMutation({
    mutationFn: () => capabilitiesApi.deleteConfig(detail!.config!.id!),
    invalidateKeys: () => [
      capabilitiesKeys.server(activeTenant, serverId ?? ''),
      capabilitiesKeys.servers(activeTenant),
      capabilitiesKeys.configs(activeTenant),
    ],
    successMessage: t('remove.done'),
    onDone: () => setConfirmDelete(false),
  })

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{detail?.name ?? t('detail.title')}</SheetTitle>
          {detail && (
            <SheetDescription className="flex flex-wrap items-center gap-1.5">
              <Badge variant="neutral">{detail.transport}</Badge>
              {detail.version && (
                <Badge variant="outline">{detail.version}</Badge>
              )}
              <StatusBadge status={detail.status} />
              <Badge
                variant={CONNECTION_VARIANT[detail.connection] ?? 'neutral'}
                title={t('connection.caption')}
              >
                {t(`connection.${detail.connection}`, {
                  defaultValue: detail.connection,
                })}
              </Badge>
            </SheetDescription>
          )}
        </SheetHeader>

        <ScrollArea className="-mr-4 flex-1 pr-4">
          {query.isLoading ? (
            <div className="flex flex-col gap-3">
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-16 w-full" />
              ))}
            </div>
          ) : query.error instanceof ApiError &&
            query.error.isStepUpRequired ? (
            // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
            // (lib/api/errors.ts:59-61) y un `step_up_required` lo satisface también, así que leerlo
            // primero sustituía la pantalla por «no tienes autorización» —falso, y sin salida—.
            //
            // DEFENSA EN PROFUNDIDAD, y lo digo porque en esta campaña ya presenté dos veces como
            // «camino vivo» algo que no lo era: HOY esta ruta no emite el código. Los emisores medidos
            // son las dos escrituras de `modules/governance` y las 21 llamadas a `requireAAL3` de
            // `core/api`, todas cubiertas ya. Esto se arregla porque el defecto es de FORMA y sobrevive
            // al día en que el gate llegue aquí, no porque alguien lo esté sufriendo ahora.
            <StepUpRequiredState
              action="generic"
              onElevated={() => void query.refetch()}
            />
          ) : query.error instanceof ApiError && query.error.isForbidden ? (
            <ForbiddenState />
          ) : query.error || !detail ? (
            <ErrorState retry={() => query.refetch()} />
          ) : (
            <div className="flex flex-col gap-5">
              <DetailBody
                detail={detail}
                canWrite={canWrite}
                canReadConfig={canReadConfig}
                onEdit={() => setEditorOpen(true)}
                onManage={() => setEditorOpen(true)}
                onDelete={() => setConfirmDelete(true)}
                onRevisions={() => setRevisionsOpen(true)}
              />
            </div>
          )}
        </ScrollArea>
      </SheetContent>

      {/* Config editor (create when no config, edit when present). */}
      {detail && (
        <ConfigEditorDialog
          open={editorOpen}
          onOpenChange={setEditorOpen}
          config={detail.config ?? undefined}
          serverRef={detail.name}
        />
      )}

      {/* Revision history. */}
      {detail?.config?.id && (
        <RevisionsSheet
          open={revisionsOpen}
          onOpenChange={setRevisionsOpen}
          configId={detail.config.id}
          serverRef={detail.config.server_ref}
        />
      )}

      {/* Delete confirmation (high risk). */}
      {detail?.config && (
        <ConfirmDialog
          open={confirmDelete}
          onOpenChange={setConfirmDelete}
          title={t('remove.title')}
          description={t('remove.body', { server: detail.config.server_ref })}
          tone="danger"
          confirmLabel={t('remove.confirm')}
          pending={remove.isPending}
          onConfirm={() => remove.mutate(undefined)}
        />
      )}
    </Sheet>
  )
}

function DetailBody({
  detail,
  canWrite,
  canReadConfig,
  onEdit,
  onManage,
  onDelete,
  onRevisions,
}: {
  detail: ServerDetailDTO
  canWrite: boolean
  canReadConfig: boolean
  onEdit: () => void
  onManage: () => void
  onDelete: () => void
  onRevisions: () => void
}) {
  const { t } = useTranslation(['capabilities', 'common'])
  const cfg = detail.config

  return (
    <>
      {detail.endpoint && (
        <Section title={t('detail.endpoint')}>
          <code className="block truncate rounded-md border border-border bg-muted px-2 py-1 font-mono text-xs text-foreground">
            {detail.endpoint}
          </code>
        </Section>
      )}

      <Separator />

      {/* Managed config. */}
      <Section
        title={t('detail.config')}
        action={
          cfg ? (
            <div className="flex items-center gap-1">
              {canReadConfig && (
                <Button variant="ghost" size="sm" onClick={onRevisions}>
                  <History />
                  {t('detail.revisionHistory')}
                </Button>
              )}
              {canWrite && (
                <Button variant="ghost" size="sm" onClick={onEdit}>
                  <Pencil />
                  {t('detail.editConfig')}
                </Button>
              )}
              {canWrite && (
                <Button variant="destructive" size="sm" onClick={onDelete}>
                  <Trash2 />
                  {t('detail.deleteConfig')}
                </Button>
              )}
            </div>
          ) : canWrite ? (
            <Button variant="secondary" size="sm" onClick={onManage}>
              <Plus />
              {t('detail.manageConfig')}
            </Button>
          ) : undefined
        }
      >
        {cfg ? (
          <KvList>
            <KvRow label={t('configs.transport')}>{cfg.transport}</KvRow>
            {cfg.scope && <KvRow label={t('configs.scope')}>{cfg.scope}</KvRow>}
            <KvRow label={t('configs.enabled')}>
              {t(
                cfg.enabled
                  ? 'common:status.enabled'
                  : 'common:status.disabled',
              )}
            </KvRow>
            {cfg.revision != null && (
              <KvRow label={t('configs.revision')} mono>
                {cfg.revision}
              </KvRow>
            )}
            <KvRow label={t('configs.secrets')} align="start">
              {cfg.secret_refs.length === 0 ? (
                '—'
              ) : (
                <div className="flex flex-wrap justify-end gap-1.5">
                  {cfg.secret_refs.map((s, i) => (
                    <SecretRef key={i} name={s.name} />
                  ))}
                </div>
              )}
            </KvRow>
          </KvList>
        ) : (
          <EmptyState
            title={t('detail.noConfig')}
            description={t('detail.noConfigHint')}
          />
        )}
      </Section>

      {/* Health. */}
      {detail.health && (
        <>
          <Separator />
          <Section
            title={t('detail.health')}
            caption={t('detail.healthCaption')}
          >
            <KvList>
              <KvRow label={t('servers.status')}>
                <StatusBadge status={detail.health.status} />
              </KvRow>
              {detail.health.severity && (
                <KvRow label={t('detail.severity')}>
                  {detail.health.severity}
                </KvRow>
              )}
              {detail.health.last_title && (
                <KvRow label={t('detail.lastTitle')} align="start">
                  {detail.health.last_title}
                </KvRow>
              )}
              <KvRow label={t('detail.statusAt')}>
                <RelTimeLabel ts={detail.health.status_at} />
              </KvRow>
              <KvRow label={t('detail.occurrences')} mono>
                {detail.health.occurrence_count}
              </KvRow>
            </KvList>
          </Section>
        </>
      )}

      {/* Tools (UNTRUSTED annotations). */}
      <Separator />
      <Section title={t('detail.tools')} caption={t('tools.untrustedNote')}>
        {detail.tools.length === 0 ? (
          <p className="text-sm text-muted-foreground">—</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {detail.tools.map((tool) => (
              <li
                key={tool.id}
                className="flex items-center justify-between gap-2 rounded-md border border-border bg-surface px-2.5 py-1.5"
              >
                <span className="truncate font-mono text-xs text-foreground">
                  {tool.name}
                </span>
                <ToolAnnotations tool={tool} />
              </li>
            ))}
          </ul>
        )}
      </Section>

      {/* Skills. */}
      {detail.skills.length > 0 && (
        <>
          <Separator />
          <Section title={t('detail.skills')}>
            <ul className="flex flex-col gap-1.5">
              {detail.skills.map((s) => (
                <li
                  key={s.id}
                  className="flex items-center justify-between gap-2 text-xs"
                >
                  <span className="truncate font-mono text-foreground">
                    {s.name}
                  </span>
                  <StatusBadge status={s.status} />
                </li>
              ))}
            </ul>
          </Section>
        </>
      )}

      {/* Resources. */}
      {detail.resources.length > 0 && (
        <>
          <Separator />
          <Section title={t('detail.resources')}>
            <div className="flex flex-wrap gap-1.5">
              {detail.resources.map((r) => (
                <Badge key={r} variant="outline" className="font-mono">
                  {r}
                </Badge>
              ))}
            </div>
          </Section>
        </>
      )}

      {/* Consumers. */}
      {detail.consumers.length > 0 && (
        <>
          <Separator />
          <Section
            title={t('detail.consumers')}
            caption={t('detail.consumersHint')}
          >
            <div className="flex flex-wrap gap-1.5">
              {detail.consumers.map((c) => (
                <Badge key={`${c.kind}:${c.ref}`} variant="neutral">
                  <span className="text-muted-foreground">
                    {t(`wiring.nodeKinds.${c.kind}`, { defaultValue: c.kind })}
                  </span>
                  <span className="ml-1 font-mono">{c.ref}</span>
                </Badge>
              ))}
            </div>
          </Section>
        </>
      )}
    </>
  )
}
