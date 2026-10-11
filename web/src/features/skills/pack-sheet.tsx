// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// One catalog pack: its skills and revisions, the targets it is pinned to (the engine lists
// only those the operator can read) and the assign/unassign actions. Directory names come
// from the console's own workspace, agent group and agent lists; an unreadable or deleted
// target shows its ID.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { type FormEvent, type ReactNode, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Field } from '@/components/ui/field'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { ListTruncationBadge } from '@/features/_intel'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { skillsApi, skillsKeys } from './api'
import type {
  SkillAssignment,
  SkillPack,
  SkillRevision,
  SkillTargetKind,
} from './types'
import './i18n'

const DIRECTORY_PAGE = { limit: 1000 }
/** The directory targets the console assigns to; templates and sessions stay in the CLI. */
const ASSIGNABLE: SkillTargetKind[] = ['workspace', 'agent_group', 'agent']
const selectClass =
  'h-9 w-full min-w-0 rounded-md border border-border bg-background px-3 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'

interface DirectoryList {
  items: Array<{ id: string; name: string }>
  isLoading: boolean
  error: unknown
}
type Directory = Partial<Record<SkillTargetKind, DirectoryList>>

function useDirectory(tenant: string | null): Directory {
  const { can } = useAuth()
  const workspaces = useQuery({
    queryKey: consoleKeys.workspaces(tenant, DIRECTORY_PAGE),
    queryFn: () => consoleApi.listWorkspaces(DIRECTORY_PAGE),
    enabled: can('tenant:read'),
  })
  const groups = useQuery({
    queryKey: consoleKeys.agentGroups(tenant, DIRECTORY_PAGE),
    queryFn: () => consoleApi.listAgentGroups(DIRECTORY_PAGE),
    enabled: can('agent:read'),
  })
  const agents = useQuery({
    queryKey: consoleKeys.agents(tenant, DIRECTORY_PAGE),
    queryFn: () => consoleApi.listAgents(DIRECTORY_PAGE),
    enabled: can('agent:read'),
  })
  const entry = (q: typeof workspaces | typeof groups | typeof agents) => ({
    items: q.data?.items ?? [],
    isLoading: q.isLoading,
    error: q.error,
  })
  return {
    workspace: entry(workspaces),
    agent_group: entry(groups),
    agent: entry(agents),
  }
}

export function PackState({ state }: { state: string }) {
  const { t } = useTranslation('skills')
  return (
    <Badge variant={state === 'enabled' ? 'success' : 'neutral'}>
      {state === 'enabled' ? t('packs.enabled') : t('packs.retired')}
    </Badge>
  )
}

export function PackSheet({
  pack,
  onOpenChange,
}: {
  pack: SkillPack | null
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Sheet open={pack !== null} onOpenChange={onOpenChange}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        {pack && <PackDetail key={pack.id} pack={pack} />}
      </SheetContent>
    </Sheet>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-label font-medium text-foreground">{title}</h3>
      {children}
    </section>
  )
}

function PackDetail({ pack }: { pack: SkillPack }) {
  const { t } = useTranslation('skills')
  const { activeTenant, can } = useAuth()
  const queryClient = useQueryClient()
  const canAssign = can('skills:assignment:write')
  const directory = useDirectory(activeTenant)
  const detail = useQuery({
    queryKey: skillsKeys.pack(activeTenant, pack.id),
    queryFn: () => skillsApi.pack(pack.id),
  })
  const uses = useQuery({
    queryKey: skillsKeys.packAssignments(activeTenant, pack.id),
    queryFn: () => skillsApi.packAssignments(pack.id),
  })
  const [removing, setRemoving] = useState<SkillAssignment | null>(null)
  const unassign = usePrivilegedMutation<SkillAssignment>({
    mutationFn: (a) => skillsApi.unassign(a, { tenant: activeTenant }),
    invalidateKeys: () => [skillsKeys.packAssignments(activeTenant, pack.id)],
    successMessage: t('uses.unassigned'),
    onDone: () => setRemoving(null),
    // A changed or removed pin answers 409/404: reread the list so a retry carries
    // the current version, and let the shared handler report the refusal.
    onError: () => {
      void queryClient.invalidateQueries({
        queryKey: skillsKeys.packAssignments(activeTenant, pack.id),
      })
      setRemoving(null)
      return false
    },
  })

  const revisions = detail.data?.revisions ?? []
  const latest = revisions.find((r) => r.id === pack.latest_revision_id)
  const revisionNumber = new Map(revisions.map((r) => [r.id, r.number]))
  const targetName = (a: SkillAssignment) =>
    directory[a.target_kind]?.items.find((d) => d.id === a.target_id)?.name ??
    a.target_id
  const pins = uses.data?.items ?? []

  return (
    <>
      <SheetHeader>
        <SheetTitle className="font-mono">{pack.name}</SheetTitle>
        <SheetDescription className="flex items-center gap-2">
          <PackState state={pack.state} />
          {t('detail.revision', { n: pack.latest_revision })}
        </SheetDescription>
      </SheetHeader>
      <div className="flex flex-col gap-6 px-4 pb-6">
        {detail.isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : detail.error ? (
          <QueryErrorState
            error={detail.error}
            retry={() => void detail.refetch()}
          />
        ) : (
          <>
            <Section title={t('detail.skills')}>
              <ul className="flex flex-col gap-1">
                {(latest?.members ?? []).map((m) => (
                  <li key={m.name} className="text-caption">
                    <span className="font-mono text-foreground">{m.name}</span>
                    {m.description && (
                      <span className="text-muted-foreground">
                        {' — '}
                        {m.description}
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            </Section>
            <Section title={t('detail.revisions')}>
              <ul className="flex flex-col gap-1 text-caption text-muted-foreground">
                {revisions.map((r) => (
                  <li key={r.id}>
                    {t('detail.revisionLine', {
                      n: r.number,
                      source: r.source.origin || r.source.kind,
                    })}
                  </li>
                ))}
              </ul>
            </Section>
          </>
        )}

        <Section title={t('uses.title')}>
          <ListTruncationBadge
            query={uses}
            label={t('uses.truncated', { n: pins.length })}
            hint={t('uses.truncatedHint')}
            className="px-0 pt-0"
            filas={pins.length}
          />
          {uses.isLoading ? (
            <Skeleton className="h-16 w-full" />
          ) : uses.error ? (
            <QueryErrorState
              error={uses.error}
              retry={() => void uses.refetch()}
            />
          ) : pins.length === 0 ? (
            <p className="text-caption text-muted-foreground">
              {t('uses.none')}
            </p>
          ) : (
            <ul className="flex flex-col divide-y divide-border rounded-md border border-border">
              {pins.map((a) => (
                <li
                  key={a.id}
                  className="flex items-center justify-between gap-3 px-3 py-2"
                >
                  <div className="min-w-0 text-caption">
                    <span className="text-muted-foreground">
                      {t(`kind.${a.target_kind}`)}
                    </span>{' '}
                    <span className="font-medium break-all text-foreground">
                      {targetName(a)}
                    </span>{' '}
                    <span className="text-muted-foreground">
                      {t('detail.revision', {
                        n: revisionNumber.get(a.pack_revision_id) ?? '?',
                      })}
                    </span>
                  </div>
                  {canAssign && (
                    <Button
                      variant="ghost"
                      size="sm"
                      aria-label={t('uses.unassignFrom', {
                        target: targetName(a),
                      })}
                      onClick={() => setRemoving(a)}
                    >
                      {t('uses.unassign')}
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </Section>

        {canAssign && pack.state === 'enabled' && detail.data && (
          <AssignForm
            pack={pack}
            revisions={revisions}
            pinned={pins}
            directory={directory}
          />
        )}
      </div>
      <ConfirmDialog
        open={removing !== null}
        onOpenChange={(open) => !open && setRemoving(null)}
        title={t('uses.confirmTitle')}
        description={t('uses.confirmDescription', {
          target: removing ? targetName(removing) : '',
        })}
        confirmLabel={t('uses.unassign')}
        tone="danger"
        pending={unassign.isPending}
        onConfirm={() => removing && unassign.mutate(removing)}
      />
    </>
  )
}

interface AssignVars {
  target_kind: SkillTargetKind
  target_id: string
  pack_revision_id: string
}

function AssignForm({
  pack,
  revisions,
  pinned,
  directory,
}: {
  pack: SkillPack
  revisions: SkillRevision[]
  pinned: SkillAssignment[]
  directory: Directory
}) {
  const { t } = useTranslation('skills')
  const { activeTenant } = useAuth()
  const [kind, setKind] = useState<SkillTargetKind>('workspace')
  const [target, setTarget] = useState('')
  const [revision, setRevision] = useState(pack.latest_revision_id)
  const assign = usePrivilegedMutation<AssignVars>({
    mutationFn: (vars) => skillsApi.assign(vars, { tenant: activeTenant }),
    invalidateKeys: () => [skillsKeys.packAssignments(activeTenant, pack.id)],
    successMessage: t('assign.done'),
    onDone: () => setTarget(''),
  })
  // The engine keeps one pin per target and pack; a pinned target is changed or
  // unassigned from its row, so it is not offered again here.
  const list = directory[kind]
  const options = (list?.items ?? []).filter(
    (o) => !pinned.some((p) => p.target_kind === kind && p.target_id === o.id),
  )
  // A pack can hold more revisions than one page; the latest is always offered.
  const choices = revisions.some((r) => r.id === pack.latest_revision_id)
    ? revisions
    : [
        ...revisions,
        { id: pack.latest_revision_id, number: pack.latest_revision },
      ]
  const targetHint = list?.error
    ? t('assign.directoryFailed')
    : !list?.isLoading && options.length === 0
      ? t('assign.noTargets')
      : undefined

  function submit(e: FormEvent) {
    e.preventDefault()
    if (target)
      assign.mutate({
        target_kind: kind,
        target_id: target,
        pack_revision_id: revision,
      })
  }

  return (
    <Section title={t('assign.title')}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        <Field label={t('assign.kind')}>
          <select
            className={selectClass}
            value={kind}
            onChange={(e) => {
              setKind(e.target.value as SkillTargetKind)
              setTarget('')
            }}
          >
            {ASSIGNABLE.map((k) => (
              <option key={k} value={k}>
                {t(`kind.${k}`)}
              </option>
            ))}
          </select>
        </Field>
        <Field label={t('assign.target')} description={targetHint}>
          <select
            className={selectClass}
            value={target}
            onChange={(e) => setTarget(e.target.value)}
          >
            <option value="">{t('assign.choose')}</option>
            {options.map((o) => (
              <option key={o.id} value={o.id}>
                {o.name}
              </option>
            ))}
          </select>
        </Field>
        <Field label={t('assign.revision')}>
          <select
            className={selectClass}
            value={revision}
            onChange={(e) => setRevision(e.target.value)}
          >
            {choices.map((r) => (
              <option key={r.id} value={r.id}>
                {t('detail.revision', { n: r.number })}
              </option>
            ))}
          </select>
        </Field>
        <Button
          type="submit"
          variant="primary"
          size="sm"
          className="self-start"
          disabled={!target || assign.isPending}
        >
          {t('assign.submit')}
        </Button>
      </form>
    </Section>
  )
}
