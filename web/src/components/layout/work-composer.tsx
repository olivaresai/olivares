// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ArrowUp } from 'lucide-react'
import {
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { toolName } from '@/features/agentops/tool-names'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import '@/features/agentops/i18n'
import { launchFailureMessage } from '@/features/agentops/launch-readiness'
import {
  sendBlockedReason,
  sessionTurnBody,
} from '@/features/agentops/session-turn'
import type { RunDTO } from '@/features/agentops/types'
import { currentControlFence } from '@/features/agentops/work-fence'
import { DictateButton } from '@/features/dictation/dictate-button'
import { joinDictation } from '@/features/dictation/join'
import { APPROVAL_READ } from '@/features/governance/use-pending-approvals'
import { useViewAccess } from '@/features/navigation/authorization'
import { viewById } from '@/features/navigation/model'
import { runAwaitedApproval } from '@/features/sessions/provenance'
import type { WorkGroupId } from '@/features/sessions/session-groups'
import { useAuth } from '@/lib/auth/context'
import { resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { cn } from '@/lib/utils'
import { useTenantLabel } from './tenant-label'
import { approvalDestination } from './session-rail-model'
import { COMPOSER_INPUT_ID } from './work-composer-id'

const PAGE = 200
/** Eight rows of 24 px and the field's own padding. */
const MAX_FIELD_PX = 8 * 24 + 16

function AdvancedDisclosure({
  children,
  label,
}: {
  children: ReactNode
  label: string
}) {
  const detailsRef = useRef<HTMLDetailsElement>(null)
  const summaryRef = useRef<HTMLElement>(null)

  function onKeyDown(event: KeyboardEvent<HTMLDetailsElement>) {
    if (event.key !== 'Escape') return
    // A handled Escape belongs to the child control.
    if (event.defaultPrevented) return
    const details = detailsRef.current
    if (!details?.open) return
    event.preventDefault()
    event.stopPropagation()
    details.open = false
    summaryRef.current?.focus()
  }

  return (
    <details ref={detailsRef} className="shrink-0" onKeyDown={onKeyDown}>
      <summary
        ref={summaryRef}
        data-testid="composer-advanced"
        className="cursor-pointer list-none text-caption text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden"
      >
        {label}
      </summary>
      {children}
    </details>
  )
}

type ScopePart = { label: string; value: string; identifier: boolean }

type OrgLabel = ReturnType<typeof useTenantLabel>

type ScopeInput = {
  workspace: string | null
  environment: string | null

  workspaceId?: string | null

  environmentId?: string | null
}

function environmentFace(
  t: TFunction,
  profile: { local_environment: boolean; environment_ref: string } | undefined,
  fallbackRef?: string | null,
): Pick<ScopeInput, 'environment' | 'environmentId'> {
  const environmentId = profile?.environment_ref || fallbackRef || null
  if (profile?.local_environment) {
    return { environment: t('nav:scope.thisNode'), environmentId }
  }
  if (profile) {
    return {
      environment: t('agentops:profiles.environment.foreign'),
      environmentId,
    }
  }
  return { environment: fallbackRef || null, environmentId }
}

function scopeFacts(
  t: TFunction,
  org: OrgLabel,
  { workspace, environment, workspaceId, environmentId }: ScopeInput,
): { parts: ScopePart[]; title: string } {
  const parts: ScopePart[] = [
    {
      label: t('scope.tenant'),
      value: org.name || t('scope.noTenant'),
      identifier: !org.named,
    },
    ...(workspace
      ? [
          {
            label: t('scope.workspace'),
            value: workspace,
            identifier: Boolean(workspaceId && workspace === workspaceId),
          },
        ]
      : []),
    {
      label: t('scope.environment'),
      value: environment || t('scope.noEnvironment'),
      identifier: Boolean(environmentId && environment === environmentId),
    },
  ]
  // Keep full identifiers available when the visible scope is truncated.
  const ids = [org.tenant, workspaceId, environmentId]
    .filter(Boolean)
    .join(' · ')
  const stated = parts.map((p) => `${p.label}: ${p.value}`).join(' · ')
  return {
    parts,
    title: `${t('scope.hint')} ${stated}${ids ? ` · ${ids}` : ''}`,
  }
}

export function ScopeLine({
  className,
  ...input
}: ScopeInput & { className?: string }) {
  const { t } = useTranslation('nav')
  const org = useTenantLabel()
  const { parts, title } = scopeFacts(t, org, input)
  return (
    <p
      data-testid="work-scope-line"
      title={title}
      className={cn(
        'min-w-0 truncate text-caption leading-4 text-muted-foreground',
        className,
      )}
    >
      {parts.map((part, i) => (
        <span key={part.label}>
          {i > 0 ? <span aria-hidden> · </span> : null}
          {part.label}{' '}
          <span
            className={cn('text-foreground', part.identifier && 'font-mono')}
          >
            {part.value}
          </span>
        </span>
      ))}
    </p>
  )
}

export type ComposerFrame = 'panel' | 'docked'

export type ComposerAttached = {
  run: RunDTO
  group: WorkGroupId
  /** The session's name, which the field's placeholder addresses ("Message <title>…"). */
  title?: string
}

export function WorkComposer({
  frame = 'panel',
  attached,
}: {
  frame?: ComposerFrame
  attached: ComposerAttached
}) {
  const { t } = useTranslation('nav')
  const { activeTenant, can } = useAuth()
  const { navigable } = useViewAccess()
  const boundary = useAuthBoundary()
  const org = useTenantLabel()

  const canWrite = can('sessions:run:write')
  const canReadProfiles = can('sessions:profile:read')
  const canReadWorkspaces = can('sessions:workspace:read')

  const profilesQuery = useQuery({
    queryKey: agentOpsKeys.profiles(activeTenant, boundary.epoch, {
      state: 'active',
    }),
    queryFn: ({ signal }) =>
      agentOpsApi.listProfiles({ state: 'active', limit: PAGE }, { signal }),
    enabled: canWrite && canReadProfiles && !!activeTenant,
  })
  const profiles = profilesQuery.data?.items ?? []

  const workspacesQuery = useQuery({
    queryKey: agentOpsKeys.workspaces(activeTenant),
    queryFn: () => agentOpsApi.listWorkspaces({ limit: PAGE }),
    enabled: canWrite && canReadWorkspaces && !!activeTenant,
  })
  const workspaces = (workspacesQuery.data?.items ?? []).filter(
    (w) => w.state === 'active',
  )

  const [turn, setTurn] = useState('')
  const fieldRef = useRef<HTMLTextAreaElement>(null)
  // The field grows with its text from 2 to 8 rows, then scrolls.
  useLayoutEffect(() => {
    const el = fieldRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, MAX_FIELD_PX)}px`
  }, [turn])
  const [wire, setWire] = useState('')
  const sendReasonId = useId()
  const wireReasonId = useId()
  const sendTurn = useMutation({
    mutationFn: async (payload: { value: string; asWire: boolean }) => {
      const run = attached.run
      const body = sessionTurnBody(run, payload.value, payload.asWire)
      // Only an active work lease contributes a control fence.
      const fence = await currentControlFence(run)
      return 'text' in body
        ? agentOpsApi.inputText(run.run_ref, body.text, fence)
        : agentOpsApi.input(run.run_ref, body.line, fence)
    },
    retry: false,
    onSuccess: (_ok, payload) => {
      if (payload.asWire) setWire('')
      else setTurn('')
    },
    onError: (error: unknown) => {
      toast.error(launchFailureMessage(error, t('nav:launcher.turnFailed')))
    },
  })

  const runLive =
    attached.run.state === 'running' || attached.run.state === 'idle'
  const waitingApproval = attached.run.state === 'waiting_approval'
  // A disabled Send says why, from the state that disables it.
  const sendReason = sendBlockedReason({
    live: runLive,
    draft: turn,
    sending: sendTurn.isPending,
    waitingApproval,
  })
  const wireReason = sendBlockedReason({
    live: runLive,
    draft: wire,
    sending: sendTurn.isPending,
    waitingApproval,
  })
  const sendSentence = () => {
    const value = turn.trim()
    if (!value || sendTurn.isPending || !runLive) return
    sendTurn.mutate({ value, asWire: false })
  }
  const sendWire = () => {
    const value = wire.trim()
    if (!value || sendTurn.isPending || !runLive) return
    sendTurn.mutate({ value, asWire: true })
  }

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    // Enter sends and Shift+Enter breaks a line (the binding table matches Enter alone);
    // a key that is choosing a character of an IME composition is not a send.
    if (e.nativeEvent.isComposing) return
    const command = resolveBinding(KEYBINDINGS, e, { launcherFocused: true })
    if (command !== 'launcher.start' && command !== 'launcher.startBackground')
      return
    e.preventDefault()
    sendSentence()
  }

  const attachedWorkspace = workspaces.find(
    (w) => w.workspace_ref === attached.run.workspace_ref,
  )
  const attachedProfile = profiles.find(
    (p) => p.profile_ref === attached.run.provider_profile_ref,
  )
  const attachedScope = {
    workspace: attachedWorkspace?.name || attached.run.workspace_ref || null,
    workspaceId: attached.run.workspace_ref || null,
    ...environmentFace(
      t,
      attachedProfile,
      attached.run.provider_environment_ref || null,
    ),
  }
  // Read-only operators still see the scope declared by the run.
  if (!canWrite) {
    return <ScopeLine {...attachedScope} className="px-3 py-2" />
  }

  // The one state word the composer ever says is the link to the approval a person may open.
  const status = t('nav:launcher.attachedWaiting')
  const approvalsView = viewById('permissions')
  const awaited = runAwaitedApproval(attached.run)
  const approval =
    attached.group === 'attention' &&
    approvalsView &&
    navigable(approvalsView) &&
    can(APPROVAL_READ) &&
    awaited
      ? approvalDestination(awaited)
      : null
  const hasText = turn.trim().length > 0
  const driver = attached.run.provider_driver
  const toolModel = [
    driver ? toolName(driver) : null,
    attached.run.model_ref || attached.run.usage_model_ref || null,
  ]
    .filter(Boolean)
    .join(' · ')
  // A session that cannot take a turn says why where the sentence would go: the
  // placeholder is the one place a disabled field already is read.
  const blockedText =
    sendReason === 'live.blocked.notLive' ||
    sendReason === 'live.blocked.approval'
      ? t(`agentops:${sendReason}`)
      : null
  const placeholder =
    blockedText ??
    (attached.title
      ? t('nav:launcher.messagePlaceholder', { title: attached.title })
      : t('nav:launcher.turnPlaceholder'))
  const card = (
    <div
      data-testid="work-composer"
      data-attached="true"
      data-session-state={attached.group}
      title={scopeFacts(t, org, attachedScope).title}
      className={cn(
        // THE COMPOSER IS A CARD: raised, 14 px radius, a 1 px line; its ring is the
        // card's, so the field inside carries none of its own.
        'relative flex flex-col rounded-[14px] border border-line bg-raised',
        'focus-within:border-line-strong focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-focus',
      )}
    >
      {approval ? (
        <p
          data-testid="composer-approval"
          className="px-4 pt-3 text-caption text-text-2"
        >
          <Link
            to={approval.to as never}
            search={approval.search as never}
            className="underline underline-offset-[3px] hover:text-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
          >
            {status}
          </Link>
        </p>
      ) : null}
      <textarea
        ref={fieldRef}
        id={COMPOSER_INPUT_ID}
        value={turn}
        rows={2}
        onChange={(e) => setTurn(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder={placeholder}
        aria-label={t('nav:launcher.turnAria')}
        data-testid="launcher-input"
        disabled={!runLive || sendTurn.isPending}
        className="max-h-48 min-h-14 w-full resize-none bg-transparent px-4 pt-3 pb-1 text-body-l text-text outline-none placeholder:text-text-3 disabled:cursor-not-allowed"
      />
      <div className="flex min-w-0 items-center gap-2 px-3 pb-3">
        {toolModel ? (
          <span
            data-testid="composer-tool-model"
            title={toolModel}
            className="min-w-0 max-w-48 shrink truncate rounded-[6px] bg-muted px-1.5 py-0.5 text-overline font-normal text-text-2"
          >
            {toolModel}
          </span>
        ) : null}
        <AdvancedDisclosure label={t('nav:launcher.advanced')}>
          <div className="absolute inset-x-0 bottom-full z-20 mb-1 flex flex-col gap-2 rounded-md border border-border bg-elevated p-2 shadow-lg">
            <p className="text-caption text-muted-foreground">
              {t('nav:launcher.advancedHint')}
            </p>
            <div className="flex items-center gap-2">
              <Input
                value={wire}
                onChange={(e) => setWire(e.target.value)}
                placeholder={t('nav:launcher.wirePlaceholder')}
                aria-label={t('nav:launcher.wireAria')}
                data-testid="composer-wire-input"
                disabled={!runLive || sendTurn.isPending}
                mono
                className="h-8 min-w-0 flex-1"
              />
              <Button
                type="button"
                variant="secondary"
                size="sm"
                className="shrink-0"
                disabled={wireReason !== null}
                aria-describedby={wireReason ? wireReasonId : undefined}
                onClick={sendWire}
                data-testid="composer-wire-send"
              >
                {t('nav:launcher.send')}
              </Button>
            </div>
            {wireReason && (
              <p
                id={wireReasonId}
                className="text-caption text-muted-foreground"
              >
                {t(`agentops:${wireReason}`)}
              </p>
            )}

            <ScopeLine {...attachedScope} />
          </div>
        </AdvancedDisclosure>
        {/* SEND IS A 32 px ROUND ICON: orange only while there is something to send (the
            one primary action of the thread), a quiet ghost otherwise. It is never
            dashed: a control that cannot act says so by being quiet, and why in its
            name. */}
        <div className="ml-auto flex shrink-0 items-center gap-2">
          <DictateButton
            disabled={!runLive || sendTurn.isPending}
            field={fieldRef}
            onText={(text) => setTurn((draft) => joinDictation(draft, text))}
          />
          <Button
            type="button"
            variant={hasText && sendReason === null ? 'primary' : 'ghost'}
            size="icon"
            className="rounded-full disabled:border-solid disabled:border-transparent"
            disabled={sendReason !== null}
            aria-label={t('nav:launcher.send')}
            aria-describedby={sendReason ? sendReasonId : undefined}
            title={
              sendReason
                ? t(`agentops:${sendReason}`)
                : `${t('nav:launcher.send')} ⏎`
            }
            onClick={sendSentence}
            data-testid="composer-send"
          >
            <ArrowUp />
          </Button>
        </div>
        {sendReason ? (
          <span id={sendReasonId} className="sr-only">
            {t(`agentops:${sendReason}`)}
          </span>
        ) : null}
      </div>
    </div>
  )
  // DOCKED: the card sits at the foot of the thread, in the transcript's own column.
  return frame === 'docked' ? (
    <div className="shrink-0 px-4 pt-2 pb-4">
      <div className="mx-auto w-full max-w-[760px]">{card}</div>
    </div>
  ) : (
    card
  )
}
