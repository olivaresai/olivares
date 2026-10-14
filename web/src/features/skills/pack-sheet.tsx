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
import { Checkbox } from '@/components/ui/checkbox'
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
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { useAuth } from '@/lib/auth/context'
import { useModuleOn } from '@/stores/modules'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { skillsApi, skillsKeys } from './api'
import type {
  SkillAssignment,
  SkillInstallResult,
  SkillPack,
  SkillRevision,
  SkillTargetKind,
  SkillUpdateCheck,
} from './types'
import './i18n'

const DIRECTORY_PAGE = { limit: 1000 }
/** The targets the console assigns to; templates and sessions stay in the CLI. */
const ASSIGNABLE: SkillTargetKind[] = [
  'workspace',
  'folder',
  'agent_group',
  'agent',
]
const FOLDER_PAGE = { state: 'active', limit: 200 } as const
const selectClass =
  'h-9 w-full min-w-0 rounded-md border border-ctl-border bg-background px-3 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'

interface DirectoryList {
  items: Array<{ id: string; name: string }>
  isLoading: boolean
  error: unknown
}
type Directory = Partial<Record<SkillTargetKind, DirectoryList>>

function useDirectory(tenant: string | null): Directory {
  const { can } = useAuth()
  // Session folders belong to the sessions module: without it there are none.
  const sessionsOn = useModuleOn('sessions')
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
  const folders = useQuery({
    queryKey: agentOpsKeys.workspaces(tenant, FOLDER_PAGE),
    queryFn: ({ signal }) =>
      agentOpsApi.listWorkspaces(FOLDER_PAGE, { tenant, signal }),
    enabled: sessionsOn && can('sessions:workspace:read'),
  })
  const entry = (q: typeof workspaces | typeof groups | typeof agents) => ({
    items: q.data?.items ?? [],
    isLoading: q.isLoading,
    error: q.error,
  })
  return {
    workspace: entry(workspaces),
    // A folder is named by its workspace_ref; its label is the name it was
    // registered under, else the last segment of its path.
    folder: {
      items: (folders.data?.items ?? []).map((w) => ({
        id: w.workspace_ref,
        name:
          w.name || w.root_path.split('/').filter(Boolean).pop() || w.root_path,
      })),
      isLoading: folders.isLoading,
      error: folders.error,
    },
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
  const canPublish = can('skills:catalog:write')
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

  // A pin keeps its revision when the pack is updated; moving it is explicit. A
  // revision whose skills act when loaded needs the operator's approval first.
  const [repinning, setRepinning] = useState<SkillAssignment | null>(null)
  const repin = usePrivilegedMutation<{
    assignment: SkillAssignment
    approve: boolean
  }>({
    mutationFn: ({ assignment, approve }) =>
      skillsApi.repin(assignment, pack.latest_revision_id, approve, {
        tenant: activeTenant,
      }),
    invalidateKeys: () => [skillsKeys.packAssignments(activeTenant, pack.id)],
    successMessage: t('uses.moved'),
    onDone: () => setRepinning(null),
    onError: () => {
      void queryClient.invalidateQueries({
        queryKey: skillsKeys.packAssignments(activeTenant, pack.id),
      })
      setRepinning(null)
      return false
    },
  })

  const revisions = detail.data?.revisions ?? []
  const latest = revisions.find((r) => r.id === pack.latest_revision_id)
  const actsOnLoad = (a: SkillAssignment) =>
    (latest?.members ?? []).filter(
      (m) =>
        (m.active_content?.length ?? 0) > 0 &&
        (!a.members || a.members.includes(m.name)),
    )
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
                    {m.active_content && m.active_content.length > 0 && (
                      <Badge variant="warning" className="ml-2">
                        {t('detail.activeContent', {
                          items: m.active_content.join(', '),
                        })}
                      </Badge>
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

        {canPublish && pack.state === 'enabled' && (
          <UpdateSection pack={pack} pins={pins} />
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
                    <div className="flex shrink-0 items-center gap-1">
                      {a.pack_revision_id !== pack.latest_revision_id && (
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={t('uses.useLatestFor', {
                            target: targetName(a),
                          })}
                          disabled={repin.isPending || !latest}
                          onClick={() =>
                            actsOnLoad(a).length > 0
                              ? setRepinning(a)
                              : repin.mutate({ assignment: a, approve: false })
                          }
                        >
                          {t('uses.useLatest')}
                        </Button>
                      )}
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
                    </div>
                  )}
                </li>
              ))}
            </ul>
          )}
        </Section>

        {canAssign && pack.state === 'enabled' && detail.data && (
          <AssignForm
            key={pack.latest_revision_id}
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
      <ConfirmDialog
        open={repinning !== null}
        onOpenChange={(open) => !open && setRepinning(null)}
        title={t('uses.approveTitle')}
        description={t('uses.approveDescription', {
          target: repinning ? targetName(repinning) : '',
          skills: repinning
            ? actsOnLoad(repinning)
                .map((m) => `${m.name} (${m.active_content?.join(', ')})`)
                .join('; ')
            : '',
        })}
        confirmLabel={t('uses.approve')}
        pending={repin.isPending}
        onConfirm={() =>
          repinning && repin.mutate({ assignment: repinning, approve: true })
        }
      />
    </>
  )
}

interface AssignVars {
  target_kind: SkillTargetKind
  target_id: string
  pack_revision_id: string
  approve_active_content?: boolean
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
  const [approved, setApproved] = useState(false)
  const assign = usePrivilegedMutation<AssignVars>({
    mutationFn: (vars) => skillsApi.assign(vars, { tenant: activeTenant }),
    invalidateKeys: () => [skillsKeys.packAssignments(activeTenant, pack.id)],
    successMessage: t('assign.done'),
    onDone: () => {
      setTarget('')
      setApproved(false)
    },
  })
  // What a tool acts on when the skill loads is pinned only with approval.
  const acting = (
    revisions.find((r) => r.id === revision)?.members ?? []
  ).filter((m) => (m.active_content?.length ?? 0) > 0)
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
    if (target && (acting.length === 0 || approved))
      assign.mutate({
        target_kind: kind,
        target_id: target,
        pack_revision_id: revision,
        ...(acting.length > 0 ? { approve_active_content: true } : {}),
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
            onChange={(e) => {
              setRevision(e.target.value)
              setApproved(false)
            }}
          >
            {choices.map((r) => (
              <option key={r.id} value={r.id}>
                {t('detail.revision', { n: r.number })}
              </option>
            ))}
          </select>
        </Field>
        {acting.length > 0 && (
          <label className="flex items-start gap-2 text-caption">
            <Checkbox
              checked={approved}
              onCheckedChange={(v) => setApproved(v === true)}
              className="mt-0.5"
            />
            <span>
              {t('assign.approve', {
                skills: acting
                  .map((m) => `${m.name} (${m.active_content?.join(', ')})`)
                  .join('; '),
              })}
            </span>
          </label>
        )}
        <Button
          type="submit"
          variant="primary"
          size="sm"
          className="self-start"
          disabled={
            !target || assign.isPending || (acting.length > 0 && !approved)
          }
        >
          {t('assign.submit')}
        </Button>
      </form>
    </Section>
  )
}

/**
 * The pack's recorded source read again. A check publishes nothing; applying
 * publishes exactly the candidate whose digest was shown. Pins keep their
 * revision until each one is moved from its row.
 */
function UpdateSection({
  pack,
  pins,
}: {
  pack: SkillPack
  pins: SkillAssignment[]
}) {
  const { t } = useTranslation('skills')
  const { activeTenant } = useAuth()
  const [result, setResult] = useState<SkillUpdateCheck | null>(null)
  // One key per candidate: a retried apply replays the published revision.
  const [key, setKey] = useState('')
  const refresh = () => [
    skillsKeys.pack(activeTenant, pack.id),
    skillsKeys.packs(activeTenant),
    skillsKeys.packAssignments(activeTenant, pack.id),
  ]
  const check = usePrivilegedMutation<void, SkillUpdateCheck>({
    mutationFn: () => skillsApi.updateCheck(pack.id, { tenant: activeTenant }),
    invalidateKeys: () => [],
    successMessage: (data) =>
      data.state === 'current'
        ? t('update.current', { n: data.current.number })
        : t('update.availableToast'),
    onDone: (data) => {
      setResult(data)
      setKey(crypto.randomUUID())
    },
  })
  const apply = usePrivilegedMutation<
    { digest: string; key: string },
    SkillInstallResult
  >({
    mutationFn: ({ digest, key }) =>
      skillsApi.updateApply(pack.id, digest, key, { tenant: activeTenant }),
    invalidateKeys: refresh,
    successMessage: (data) => t('update.applied', { n: data.revision.number }),
    onDone: () => setResult(null),
    // A refused apply (the source moved, or nothing is new) leaves the panel with
    // a digest that can never pass: clear it so the operator checks again.
    onError: () => {
      setResult(null)
      return false
    },
  })
  const available = result?.state === 'available' ? result : null
  const members = available?.diff?.members ?? []
  const count = (c: string) => members.filter((m) => m.change === c).length
  const acting = members.filter((m) => (m.new_active_content?.length ?? 0) > 0)

  return (
    <Section title={t('update.title')}>
      <div className="flex flex-wrap items-center gap-2">
        <Button
          variant="secondary"
          size="sm"
          disabled={check.isPending || apply.isPending}
          onClick={() => check.mutate()}
        >
          {check.isPending ? t('update.checking') : t('update.check')}
        </Button>
        {result?.state === 'current' && (
          <span role="status" className="text-caption text-muted-foreground">
            {t('update.current', { n: result.current.number })}
          </span>
        )}
      </div>
      {available && available.candidate && (
        <div
          role="status"
          className="flex flex-col gap-2 rounded-md border border-border p-3"
        >
          <p className="text-caption text-foreground">
            {t('update.available', { n: available.current.number })}
          </p>
          <p className="text-caption text-muted-foreground">
            {t('update.summary', {
              added: count('added'),
              changed: count('changed'),
              removed: count('removed'),
              files: available.diff?.files.length ?? 0,
            })}
          </p>
          <ul className="flex flex-col gap-1 text-caption">
            {members.map((m) => (
              <li key={m.name}>
                <span className="font-mono">{m.name}</span>{' '}
                <span className="text-muted-foreground">
                  {t(`update.change.${m.change}`)}
                </span>
              </li>
            ))}
          </ul>
          {acting.map((m) => (
            <p key={m.name} className="text-caption text-warning">
              {t('update.acts', {
                skill: m.name,
                items: (m.new_active_content ?? []).join(', '),
              })}
            </p>
          ))}
          {pins.length > 0 && (
            <p className="text-caption text-muted-foreground">
              {t('update.pinsKeep')}
            </p>
          )}
          <Button
            variant="primary"
            size="sm"
            className="self-start"
            disabled={apply.isPending}
            onClick={() =>
              available.candidate &&
              apply.mutate({ digest: available.candidate.manifest_digest, key })
            }
          >
            {t('update.apply')}
          </Button>
        </div>
      )}
    </Section>
  )
}
