// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// · plan 3.6 — the routine-policy console (policy enforcement).
//
// The engine has governed Claude Code Routines since and ENFORCED that
// governance since (cmd/olivares/routinepolicygate.go), with six routes
// live at /v1/m/governance/routine-policies. Until this view there was no
// surface for any of it: an operator could not see a cadence floor they were
// subject to, let alone author one.
//
// HONESTY (ARCHITECTURE.md): the web renders the engine's DTOs and its COMPOSED
// decision. It never re-derives the composition — monotone most-restrictive
// folding (floors max, approval OR, cron allowlists INTERSECT, blocked
// environments UNION, caps a per-scope vector) lives in
// modules/governance/routinepolicy_resolve.go and reaches the browser through
// the posture endpoint's `effective` block. A second implementation here would
// eventually disagree with enforcement, and the direction it disagrees in reads
// as "unconstrained" over a policy set that denies.
import { useQuery } from '@tanstack/react-query'
import { CalendarCog, Plus, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { PageHeader } from '@/components/ui/page-header'
import { EmptyState } from '@/components/ui/empty-state'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { governanceApi, governanceKeys } from './api'
import { RoutinePolicyEditorDialog } from './routine-policy-editor'
import './i18n'
import {
  routineListState,
  type RoutineEffectiveDTO,
  type RoutineListState,
  type RoutinePolicyDTO,
} from './types'

/**
 * ⛔ ESTE AYUDANTE COLAPSABA LAS DOS RESPUESTAS POR CONSTRUCCIÓN, y era la forma más difícil de
 * ver del residuo: no leía `isForbidden` —que al menos nombra lo que mira— sino `status === 403`
 * directamente. Un `step_up_required` es TAMBIÉN un 403 (lib/api/errors.ts:59-61, :77-79), así
 * que satisfacía este predicado y la vista de rutinas se sustituía por la acusación de rol.
 *
 * Un barrido de texto que busque `isForbidden` no puede encontrar esta forma; aquí la cacé
 * porque el NOMBRE coincidía, no el criterio. Queda escrito para que el siguiente barrido de la
 * campaña busque también `status === 403`.
 */
function esNegativaDeRol(error: unknown): boolean {
  return (
    error instanceof ApiError && error.isForbidden && !error.isStepUpRequired
  )
}

/** Y el aseguramiento, que es la otra mitad del mismo 403 y tiene remedio. */
function pideAseguramiento(error: unknown): boolean {
  return error instanceof ApiError && error.isStepUpRequired
}

/**
 * The composed allowlist has the SAME three states as a stored one, but they
 * are carried by two fields rather than by null-vs-empty: `cron_allowed` is
 * always an array, and `cron_allowlist_in_force` is what separates "no
 * allowlist anywhere" from "an allowlist that admits nothing".
 */
function effectiveCronState(effective: RoutineEffectiveDTO): RoutineListState {
  if (!effective.cron_allowlist_in_force) return 'unset'
  return effective.cron_allowed.length === 0 ? 'empty' : 'listed'
}

/**
 * TriStateList renders one stored tri-state column. The three states get three
 * DIFFERENT strings on purpose: `null` is "no list authored", `[]` is "a list
 * that admits nothing", and collapsing them is the single defect this panel is
 * built to prevent.
 *
 * The empty state also carries a screen-reader caveat, because the engine
 * projects an UNREADABLE column as `[]` too (routines.go:86-98) — so `[]` is
 * genuinely ambiguous on the wire and the UI must not assert authorship.
 */
function TriStateList({
  value,
  unreadable,
  kind,
  testId,
}: {
  value: string[] | null
  unreadable: boolean
  kind: 'cron' | 'envs'
  testId: string
}) {
  const { t } = useTranslation('governance')
  const state = routineListState(value, unreadable)
  const caveatId = `${testId}-caveat`
  return (
    <>
      <span
        data-testid={testId}
        data-state={state}
        aria-describedby={state === 'unreadable' ? caveatId : undefined}
        className={
          state === 'unreadable'
            ? 'font-medium text-danger'
            : state === 'empty'
              ? 'font-medium text-warning'
              : state === 'unset'
                ? 'text-muted-foreground'
                : 'font-mono text-xs text-foreground'
        }
      >
        {state === 'listed'
          ? (value as string[]).join(', ')
          : state === 'unreadable'
            ? t('routines.list.unreadable')
            : t(`routines.list.${kind}.${state}`)}
      </span>
      {state === 'unreadable' && (
        <span id={caveatId} className="sr-only">
          {t('routines.list.unreadableCaveat')}
        </span>
      )}
    </>
  )
}

/**
 * RoutinePoliciesView is the routed panel behind /routine-policies, gated on
 * governance:routine:read. Authoring gates on governance:routine:admin and the
 * engine enforces both at the router — the checks here only hide and disable.
 */
export function RoutinePoliciesView() {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant, can } = useAuth()
  const canRead = can('governance:routine:read')
  const canAdmin = can('governance:routine:admin')

  // The scope the posture is resolved FOR. Draft state is what the operator is
  // typing; applied state is what the query is keyed on, so a half-typed ref
  // never fires a request.
  const [draftWorkspace, setDraftWorkspace] = useState('')
  const [draftUser, setDraftUser] = useState('')
  const [draftUserKnown, setDraftUserKnown] = useState(true)
  const [scope, setScope] = useState<{
    workspace_ref?: string
    user_ref?: string
    user_known?: string
  }>({})

  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<RoutinePolicyDTO | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<RoutinePolicyDTO | null>(
    null,
  )

  const postureQ = useQuery({
    queryKey: governanceKeys.routinePosture(activeTenant, scope),
    queryFn: () => governanceApi.routinePosture(scope),
    enabled: canRead,
  })
  const listQ = useQuery({
    queryKey: governanceKeys.routinePolicies(activeTenant),
    queryFn: () => governanceApi.listRoutinePolicies(),
    enabled: canRead,
  })

  const remove = usePrivilegedMutation({
    mutationFn: () => governanceApi.deleteRoutinePolicy(confirmDelete!.id),
    invalidateKeys: () => [
      governanceKeys.routinePolicies(activeTenant),
      governanceKeys.routinePosture(activeTenant, scope),
    ],
    successMessage: t('routines.remove.done'),
    onDone: () => setConfirmDelete(null),
  })

  const policies = useMemo(() => listQ.data?.items ?? [], [listQ.data])
  const effective = postureQ.data?.effective ?? null
  // True while the operator has edited the scope inputs but not re-resolved.
  const scopeDrifted =
    draftWorkspace.trim() !== (scope.workspace_ref ?? '') ||
    (draftUserKnown ? draftUser.trim() : '') !== (scope.user_ref ?? '') ||
    (draftUserKnown ? undefined : 'false') !== scope.user_known

  // Drill-down: a composed value is only actionable if the operator can get
  // from it to the rows that produced it. The posture payload carries the whole
  // policy set, so the refs resolve to names without a second request.
  const nameByID = useMemo(() => {
    const out = new Map<string, string>()
    for (const p of postureQ.data?.policies ?? []) out.set(p.id, p.name)
    for (const p of policies) if (!out.has(p.id)) out.set(p.id, p.name)
    return out
  }, [postureQ.data, policies])

  const columns = useMemo<TableColumn<RoutinePolicyDTO, unknown>[]>(() => {
    const base: TableColumn<RoutinePolicyDTO, unknown>[] = [
      {
        accessorKey: 'name',
        header: t('routines.cols.name'),
        // MEDIDO: en 212 px «nightly-compliance-scan-eu-west» salía en DOS líneas y
        // DOS fragmentos — o sea partido DENTRO del nombre, tras un guion. No es un
        // problema de política de corte: `word-break: keep-all` se midió y no cambia
        // nada. Es anchura, y 260 px es lo que el nombre ocupa entero.
        size: 260,
        cell: ({ row }) => (
          <span
            className="font-medium text-foreground"
            data-testid={`routine-policy-row-${row.original.id}`}
          >
            {row.original.name}
          </span>
        ),
      },
      {
        id: 'scope',
        header: t('routines.cols.scope'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {t(`routines.scope.${row.original.scope_kind}`, {
              defaultValue: row.original.scope_kind,
            })}
            {row.original.scope_ref ? ` · ${row.original.scope_ref}` : ''}
          </span>
        ),
      },
      {
        accessorKey: 'enabled',
        header: t('routines.cols.enabled'),
        cell: ({ row }) => (
          <Badge variant={row.original.enabled ? 'success' : 'neutral'}>
            {row.original.enabled
              ? t('routines.enabledYes')
              : t('routines.enabledNo')}
          </Badge>
        ),
      },
      {
        accessorKey: 'max_cadence_seconds',
        header: t('routines.cols.floor'),
        cell: ({ row }) =>
          row.original.max_cadence_seconds === 0 ? (
            <span className="text-muted-foreground">
              {t('routines.noFloor')}
            </span>
          ) : (
            <span className="font-mono text-xs tabular-nums">
              {t('routines.secondsValue', {
                seconds: row.original.max_cadence_seconds,
              })}
            </span>
          ),
      },
      {
        accessorKey: 'max_active_routines',
        header: t('routines.cols.cap'),
        cell: ({ row }) =>
          row.original.max_active_routines === 0 ? (
            <span className="text-muted-foreground">{t('routines.noCap')}</span>
          ) : (
            <span className="font-mono text-xs tabular-nums">
              {row.original.max_active_routines}
            </span>
          ),
      },
      {
        accessorKey: 'require_approval',
        header: t('routines.cols.approval'),
        cell: ({ row }) => (
          <Badge
            variant={row.original.require_approval ? 'warning' : 'neutral'}
          >
            {row.original.require_approval
              ? t('routines.approvalYes')
              : t('routines.approvalNo')}
          </Badge>
        ),
      },
      {
        id: 'cron',
        header: t('routines.cols.cron'),
        cell: ({ row }) => (
          <TriStateList
            value={row.original.allowed_cron_patterns}
            unreadable={row.original.allowed_cron_patterns_unreadable}
            kind="cron"
            testId={`routine-cron-${row.original.id}`}
          />
        ),
      },
      {
        id: 'envs',
        header: t('routines.cols.envs'),
        cell: ({ row }) => (
          <TriStateList
            value={row.original.blocked_environments}
            unreadable={row.original.blocked_environments_unreadable}
            kind="envs"
            testId={`routine-envs-${row.original.id}`}
          />
        ),
      },
    ]
    if (!canAdmin) return base
    return [
      ...base,
      {
        id: 'actions',
        header: '',
        cell: ({ row }) => (
          <div className="flex items-center justify-end">
            <Button
              variant="destructive"
              size="icon"
              aria-label={t('routines.remove.confirm')}
              data-testid={`routine-policy-delete-${row.original.id}`}
              onClick={(e) => {
                e.stopPropagation()
                setConfirmDelete(row.original)
              }}
            >
              <Trash2 />
            </Button>
          </div>
        ),
      } as TableColumn<RoutinePolicyDTO, unknown>,
    ]
  }, [canAdmin, t])

  if (!canRead) return <ForbiddenState data-testid="routine-forbidden" />
  // Aseguramiento ANTES que rol: si el motor ofrece la ceremonia, se ofrece.
  if (pideAseguramiento(listQ.error) || pideAseguramiento(postureQ.error)) {
    return (
      <StepUpRequiredState
        action="generic"
        onElevated={() => {
          void listQ.refetch()
          void postureQ.refetch()
        }}
      />
    )
  }
  if (esNegativaDeRol(listQ.error) || esNegativaDeRol(postureQ.error)) {
    return <ForbiddenState data-testid="routine-forbidden" />
  }

  return (
    <div className="flex flex-col gap-5 pb-10">
      <PageHeader
        title={t('routines.title')}
        description={t('routines.subtitle')}
        icon={CalendarCog}
      />

      <section
        data-testid="routine-posture"
        aria-label={t('routines.posture.title')}
        className="flex flex-col gap-4 rounded-lg border border-border p-4"
      >
        <div className="flex flex-wrap items-baseline gap-x-6 gap-y-1">
          <h2 className="text-sm font-medium text-foreground">
            {t('routines.posture.title')}
          </h2>
          {/* A failed posture read must NOT render as "0 enabled of 0": with the
              table below listing real rows, a zeroed header is a claim that the
              tenant has no governance, which is the opposite of "we could not
              ask". */}
          {postureQ.isError ? (
            <span
              role="alert"
              data-testid="posture-error"
              className="text-xs font-medium text-danger"
            >
              {t('routines.posture.failed')}
            </span>
          ) : postureQ.isLoading ? (
            <span className="text-xs text-muted-foreground">
              {t('common:states.loading')}
            </span>
          ) : (
            <span className="text-xs text-muted-foreground">
              {t('routines.posture.counts', {
                total: postureQ.data?.total_policies ?? 0,
                enabled: postureQ.data?.enabled_policies ?? 0,
              })}
            </span>
          )}
          {postureQ.isError && (
            <Button
              variant="secondary"
              size="sm"
              data-testid="posture-retry"
              onClick={() => void postureQ.refetch()}
            >
              {t('common:actions.retry')}
            </Button>
          )}
        </div>

        {/* The composition answers for ONE scope. Saying which one is not a
            nicety: an operator reading a floor without knowing whose routine it
            governs will act on the wrong number. */}
        <form
          className="flex flex-wrap items-end gap-3"
          onSubmit={(event) => {
            event.preventDefault()
            setScope({
              ...(draftWorkspace.trim()
                ? { workspace_ref: draftWorkspace.trim() }
                : {}),
              ...(draftUserKnown && draftUser.trim()
                ? { user_ref: draftUser.trim() }
                : {}),
              ...(draftUserKnown ? {} : { user_known: 'false' }),
            })
          }}
        >
          <Field label={t('routines.posture.workspaceRef')}>
            {({ id }) => (
              <Input
                id={id}
                data-testid="posture-scope-workspace"
                value={draftWorkspace}
                placeholder={t('routines.posture.defaultWorkspace')}
                onChange={(event) => setDraftWorkspace(event.target.value)}
              />
            )}
          </Field>
          <Field label={t('routines.posture.userRef')}>
            {({ id }) => (
              <Input
                id={id}
                data-testid="posture-scope-user"
                value={draftUserKnown ? draftUser : ''}
                // An unanswerable axis has no owner, so a ref alongside it is a
                // contradiction the engine refuses. Disabling is clearer than
                // letting the operator type something that will 400.
                disabled={!draftUserKnown}
                placeholder={t('routines.posture.noUser')}
                onChange={(event) => setDraftUser(event.target.value)}
              />
            )}
          </Field>
          {/* Orchestration genuinely produces an UNANSWERABLE user axis for a
              routine whose owner it cannot recognise, and there the resolution
              goes indeterminate and the fire is refused. An operator debugging
              that refusal has to be able to reproduce it here. */}
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <input
              type="checkbox"
              data-testid="posture-scope-user-unknown"
              checked={!draftUserKnown}
              onChange={(event) => {
                setDraftUserKnown(!event.target.checked)
                if (event.target.checked) setDraftUser('')
              }}
            />
            {t('routines.posture.unknownUserAxis')}
          </label>
          <Button
            type="submit"
            variant="secondary"
            size="sm"
            data-testid="posture-scope-apply"
          >
            {t('routines.posture.resolve')}
          </Button>
        </form>

        {effective && (
          // The composition answers for ONE scope, and the draft inputs above
          // change as the operator types. Without echoing what the ANSWER was
          // resolved for, a stale result sits under new refs and reads as if it
          // described them.
          <p
            data-testid="posture-applied-scope"
            data-stale={scopeDrifted ? 'true' : 'false'}
            className={
              scopeDrifted
                ? 'text-xs font-medium text-warning'
                : 'text-xs text-muted-foreground'
            }
          >
            {t('routines.posture.resolvedFor', {
              workspace:
                effective.scope_workspace_ref ||
                t('routines.posture.defaultWorkspace'),
              user: effective.scope_user_known
                ? effective.scope_user_ref || t('routines.posture.noUser')
                : t('routines.posture.unknownUser'),
            })}
            {scopeDrifted ? ` — ${t('routines.posture.stale')}` : ''}
          </p>
        )}

        {effective?.indeterminate && (
          // Enforcement DENIES CLOSED on an indeterminate resolution. Rendering
          // it as a quiet caption would invert its meaning into "nothing
          // applies here".
          <p
            role="alert"
            data-testid="posture-indeterminate"
            className="rounded-md border border-danger bg-danger/10 p-3 text-sm font-medium text-danger"
          >
            {t('routines.posture.indeterminate', {
              axis: effective.indeterminate_axis,
            })}
          </p>
        )}

        {effective && (
          <>
            {effective.indeterminate && (
              <p
                data-testid="posture-superseded"
                className="text-xs text-muted-foreground"
              >
                {t('routines.posture.supersededByIndeterminate')}
              </p>
            )}
            <dl
              data-testid="posture-values"
              data-superseded={effective.indeterminate ? 'true' : 'false'}
              className={
                effective.indeterminate
                  ? 'grid grid-cols-1 gap-3 opacity-60 sm:grid-cols-2 lg:grid-cols-4'
                  : 'grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4'
              }
            >
              <div>
                <dt className="text-xs text-muted-foreground">
                  {t('routines.posture.floor')}
                </dt>
                <dd
                  data-testid="posture-floor"
                  className="font-mono text-sm tabular-nums text-foreground"
                >
                  {effective.min_interval_seconds === 0
                    ? t('routines.noFloor')
                    : t('routines.secondsValue', {
                        seconds: effective.min_interval_seconds,
                      })}
                </dd>
              </div>
              <div>
                <dt className="text-xs text-muted-foreground">
                  {t('routines.posture.approval')}
                </dt>
                <dd data-testid="posture-approval" className="text-sm">
                  {effective.require_approval
                    ? t('routines.approvalYes')
                    : t('routines.approvalNo')}
                </dd>
              </div>
              <div>
                <dt className="text-xs text-muted-foreground">
                  {t('routines.posture.cron')}
                </dt>
                <dd>
                  <span
                    data-testid="posture-cron"
                    data-state={effectiveCronState(effective)}
                    className={
                      effectiveCronState(effective) === 'empty'
                        ? 'text-sm font-medium text-warning'
                        : 'text-sm'
                    }
                  >
                    {effectiveCronState(effective) === 'listed'
                      ? effective.cron_allowed.join(', ')
                      : t(
                          `routines.list.cron.${effectiveCronState(effective)}`,
                        )}
                  </span>
                </dd>
              </div>
              <div>
                <dt className="text-xs text-muted-foreground">
                  {t('routines.posture.envs')}
                </dt>
                <dd data-testid="posture-envs" className="text-sm">
                  {effective.blocked_environments.length === 0
                    ? t('routines.list.envs.unset')
                    : effective.blocked_environments.join(', ')}
                </dd>
              </div>
            </dl>

            {effective.active_caps.length > 0 && (
              <ul
                data-testid="posture-caps"
                className="flex flex-wrap gap-2 text-xs"
              >
                {effective.active_caps.map((cap) => (
                  <li
                    key={`${cap.scope_kind}/${cap.scope_ref}`}
                    className="rounded-md border border-border px-2 py-1"
                  >
                    {t('routines.posture.cap', {
                      scope: cap.scope_ref
                        ? `${cap.scope_kind}:${cap.scope_ref}`
                        : cap.scope_kind,
                      max: cap.max,
                    })}
                  </li>
                ))}
              </ul>
            )}

            <div className="flex flex-wrap items-center gap-2">
              <span className="text-xs text-muted-foreground">
                {/* An INDETERMINATE resolution with nothing composed is a
                    REFUSAL, not an absence of controls: the engine sets
                    Indeterminate and skips the policy without ever reaching
                    out.InForce = true (routinepolicy_resolve.go), so in_force
                    is false for both. Falling through to "No enabled policy
                    governs this scope." printed the exact opposite of the
                    role="alert" banner sitting a few lines above it, inside the
                    same <section>. */}
                {effective.in_force
                  ? t('routines.posture.origin')
                  : effective.indeterminate
                    ? t('routines.posture.noneIndeterminate')
                    : t('routines.posture.none')}
              </span>
              {effective.policy_refs.map((ref) => {
                const match = policies.find((p) => p.id === ref)
                const label = nameByID.get(ref) ?? ref
                // A focusable control that does nothing is worse than text: it
                // promises an action a read-only operator does not have.
                if (!canAdmin || !match) {
                  return (
                    <span
                      key={ref}
                      data-testid="posture-policy-ref"
                      className="rounded-md border border-border px-2 py-0.5 text-xs text-foreground"
                    >
                      {label}
                    </span>
                  )
                }
                return (
                  <button
                    key={ref}
                    type="button"
                    data-testid="posture-policy-ref"
                    className="rounded-md border border-border px-2 py-0.5 text-xs text-foreground hover:bg-muted"
                    onClick={() => {
                      setEditing(match)
                      setEditorOpen(true)
                    }}
                  >
                    {label}
                  </button>
                )
              })}
            </div>
          </>
        )}
      </section>

      <p className="text-xs text-muted-foreground">
        {t('routines.enforcementCaption')}
      </p>

      <DataTable
        columns={columns}
        data={policies}
        isLoading={listQ.isLoading}
        error={listQ.error}
        onRetry={() => void listQ.refetch()}
        getRowId={(r) => r.id}
        searchable
        searchPlaceholder={t('routines.search')}
        onRowClick={
          canAdmin
            ? (r) => {
                setEditing(r)
                setEditorOpen(true)
              }
            : undefined
        }
        label={t('routines.title')}
        toolbar={
          canAdmin ? (
            <Button
              variant="primary"
              size="sm"
              data-testid="routine-policy-new"
              onClick={() => {
                setEditing(null)
                setEditorOpen(true)
              }}
            >
              <Plus />
              {t('routines.newPolicy')}
            </Button>
          ) : undefined
        }
        empty={
          <EmptyState
            title={t('empty.routinePolicies.title')}
            description={t('empty.routinePolicies.description')}
          />
        }
      />

      <RoutinePolicyEditorDialog
        open={editorOpen}
        onOpenChange={setEditorOpen}
        policy={editing}
        postureScope={scope}
      />

      {confirmDelete && (
        <ConfirmDialog
          open={!!confirmDelete}
          onOpenChange={(o) => !o && setConfirmDelete(null)}
          title={t('routines.remove.title')}
          description={t('routines.remove.body', { name: confirmDelete.name })}
          tone="danger"
          confirmLabel={t('routines.remove.confirm')}
          pending={remove.isPending}
          onConfirm={() => remove.mutate(undefined)}
        />
      )}
    </div>
  )
}

export default RoutinePoliciesView
