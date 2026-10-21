// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useIsMutating, useMutation, useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ArrowUp } from 'lucide-react'
import {
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type KeyboardEvent,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { AttachButton, AttachmentTray } from '@/features/agentops/attachment-ui'
import { imagesIn, useImageDraft } from '@/features/agentops/attachments'
import { toolName } from '@/features/agentops/tool-names'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import '@/features/agentops/i18n'
import { launchFailureMessage } from '@/features/agentops/launch-readiness'
import { LaunchSpeedFields } from '@/features/agentops/launch-speed-fields'
import {
  effortAfterModel,
  useModelChoices,
} from '@/features/agentops/model-choices'
import {
  EffortSelect,
  ModelList,
} from '@/features/agentops/model-effort-picker'
import {
  sendBlockedReason,
  sessionTurnBody,
} from '@/features/agentops/session-turn'
import type { RunCommandDTO, RunDTO } from '@/features/agentops/types'
import { currentControlFence } from '@/features/agentops/work-fence'
import { DictateButton } from '@/features/dictation/dictate-button'
import { joinDictation } from '@/features/dictation/join'
import { useNewSessionDialog } from '@/features/first-hour/new-session-store'
import { APPROVAL_READ } from '@/features/governance/use-pending-approvals'
import { useViewAccess } from '@/features/navigation/authorization'
import { viewById } from '@/features/navigation/model'
import { runAwaitedApproval } from '@/features/sessions/provenance'
import type { WorkGroupId } from '@/features/sessions/session-groups'
import { useAuth } from '@/lib/auth/context'
import {
  readSessionDraft,
  sessionDraftKey,
  sessionDraftRefused,
  sessionDraftSnapshot,
  subscribeSessionDrafts,
  writeSessionDraft,
} from '@/lib/session-drafts'
import { resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { cn } from '@/lib/utils'
import { useTenantLabel } from './tenant-label'
import { approvalDestination } from './session-rail-model'
import { SlashMenu } from './slash-menu'
import {
  commandLabel,
  filterCommands,
  insertText,
  slashOptionId,
  slashQuery,
  type SlashMenuEmpty,
} from './slash-menu-model'
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
  inputId = COMPOSER_INPUT_ID,
}: {
  frame?: ComposerFrame
  attached: ComposerAttached
  inputId?: string
}) {
  const { activeTenant, principal } = useAuth()
  const draftKey = sessionDraftKey(
    activeTenant,
    principal,
    attached.run.run_ref,
  )
  // Scope all local composer state at once, including an in-flight send's callbacks.
  return (
    <SessionComposer
      key={draftKey ?? JSON.stringify([activeTenant, attached.run.run_ref])}
      frame={frame}
      attached={attached}
      draftKey={draftKey}
      inputId={inputId}
    />
  )
}

function SessionComposer({
  frame,
  attached,
  draftKey,
  inputId,
}: {
  frame: ComposerFrame
  attached: ComposerAttached
  draftKey: string | null
  inputId: string
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

  const savedTurn = useSyncExternalStore(subscribeSessionDrafts, () =>
    readSessionDraft(draftKey),
  )
  // Without a scoped address there is nowhere to save: the field holds this tab's text only.
  const [unsavedTurn, setUnsavedTurn] = useState<string | null>(null)
  const turn = unsavedTurn ?? savedTurn
  // The draft store keeps refused writes for this tab; the notice follows its verdict.
  const draftSaveFailed = sessionDraftRefused(draftKey)
  // The draft as it is NOW, for a callback that fires seconds after it was made (dictation).
  const turnRef = useRef(turn)
  useEffect(() => {
    turnRef.current = turn
  }, [turn])
  const fieldRef = useRef<HTMLTextAreaElement>(null)
  // The field grows with its text from 2 to 8 rows, then scrolls.
  useLayoutEffect(() => {
    const el = fieldRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, MAX_FIELD_PX)}px`
  }, [turn])
  const [wire, setWire] = useState('')
  // The images of the message being written; they are stored with the session as they
  // are attached and go with the message by id.
  const images = useImageDraft(attached.run.run_ref)
  const sendReasonId = useId()
  const slashListId = useId()
  const wireReasonId = useId()
  // A session's sends are one queue: a send that outlives its composer instance
  // (a round trip through another session) still blocks the field it came from.
  const turnQueue = ['session-turn', activeTenant, attached.run.run_ref]
  const sendTurn = useMutation({
    mutationKey: turnQueue,
    mutationFn: async (payload: {
      run: string
      value: string
      asWire: boolean
      draftSnapshot?: string | null
      attachments?: string[]
    }) => {
      const run = attached.run
      // A message with images is one body for every tool: the engine encodes it.
      if (payload.attachments?.length)
        return agentOpsApi.inputWithAttachments(
          run.run_ref,
          payload.value,
          payload.attachments,
        )
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
      else {
        // A→B→A can remount the field while this send is pending. Retain newer edits.
        if (sessionDraftSnapshot(draftKey) === payload.draftSnapshot)
          changeTurn('')
        // Only if the person is still in the session it went to; its images leave the
        // draft, and one attached while the message was going stays.
        if (payload.run === attached.run.run_ref)
          images.discard(payload.attachments ?? [])
      }
    },
    onError: (error: unknown) => {
      toast.error(launchFailureMessage(error, t('nav:launcher.turnFailed')))
    },
  })

  const runLive =
    attached.run.state === 'running' || attached.run.state === 'idle'
  const waitingApproval = attached.run.state === 'waiting_approval'
  // An in-flight send counts even when this instance did not start it.
  const sendsInQueue = useIsMutating({ mutationKey: turnQueue })
  const sending = sendTurn.isPending || sendsInQueue > 0

  // THE "/" MENU: while the first word of the message is a slash command being typed,
  // the session's own tool says what it offers (commands, skills, agents), asked again
  // each time the menu opens so a skill added meanwhile is there. Escape closes it for
  // the word typed so far; the next character brings it back.
  const slash = slashQuery(turn)
  const [slashDismissed, setSlashDismissed] = useState<string | null>(null)
  const [slashActive, setSlashActive] = useState(0)
  /** Every change of the draft: the choice goes back to the first entry, and an Escape lasts only while the word is the same. */
  function changeTurn(next: string) {
    if (draftKey === null) setUnsavedTurn(next)
    else {
      setUnsavedTurn(null)
      writeSessionDraft(draftKey, next)
    }
    setSlashActive(0)
    if (slashQuery(next) !== slashDismissed) setSlashDismissed(null)
  }
  // While a send is pending the field is disabled and the list would only be a target
  // for a click that rewrites the draft under it.
  const menuOpen =
    canWrite &&
    runLive &&
    !sending &&
    !!activeTenant &&
    slash !== null &&
    slashDismissed !== slash
  const commandsQuery = useQuery({
    queryKey: agentOpsKeys.runCommands(activeTenant, attached.run.run_ref),
    queryFn: ({ signal }) =>
      agentOpsApi.commands(attached.run.run_ref, { signal }),
    enabled: menuOpen,
    // Every opening asks again (the engine caches for a few seconds); the last answer
    // shows meanwhile.
    staleTime: 0,
    // A refocused window does not re-ask while the menu is open: that would flicker the
    // one-line state and re-announce it.
    refetchOnWindowFocus: false,
    retry: false,
  })
  const catalog = commandsQuery.data
  const slashMatches = useMemo<RunCommandDTO[]>(
    () =>
      menuOpen && catalog?.reported
        ? filterCommands(catalog.commands, slash ?? '')
        : [],
    [menuOpen, catalog, slash],
  )
  const slashIndex = Math.min(slashActive, Math.max(slashMatches.length - 1, 0))
  // Why there is nothing to choose: still reading (also while an old "nothing reported"
  // is being asked again), not readable, nothing reported, or nothing matches.
  const readFailed = commandsQuery.isError && !commandsQuery.isFetching
  let slashEmpty: SlashMenuEmpty = 'nomatch'
  if (!catalog) slashEmpty = readFailed ? 'failed' : 'loading'
  else if (!catalog.reported || catalog.commands.length === 0)
    slashEmpty = readFailed
      ? 'failed'
      : commandsQuery.isFetching
        ? 'loading'
        : 'unreported'
  const slashEmptyText =
    slashEmpty === 'nomatch'
      ? t('nav:launcher.slashNoMatch', { query: slash ?? '' })
      : t(`nav:launcher.slash.${slashEmpty}`)
  // What is listed may be older than the tool: the engine served what the tool reported
  // before (stale), or this console's own refresh failed and the previous list stays.
  // The one line already says the read failed when there is nothing else to show.
  const slashStale =
    menuOpen &&
    !!catalog?.reported &&
    slashEmpty !== 'failed' &&
    (catalog.stale === true || readFailed)
  const slashNotice = slashStale ? t('nav:launcher.slash.stale') : ''
  // The one live region of the menu: it exists before the menu opens and only its text
  // changes, which is what a screen reader announces. The active option is said here too,
  // because not every reader follows aria-activedescendant on a text field.
  const slashSaid = !menuOpen
    ? ''
    : slashMatches.length > 0
      ? t('nav:launcher.slashOption', {
          name: commandLabel(slashMatches[slashIndex]),
          i: slashIndex + 1,
          n: slashMatches.length,
        })
      : slashEmptyText
  const pickCommand = (command: RunCommandDTO) => {
    changeTurn(insertText(command))
    fieldRef.current?.focus()
  }
  // A disabled Send says why, from the state that disables it.
  const sendReason = sendBlockedReason({
    live: runLive,
    draft: turn,
    sending,
    uploading: images.uploading,
    waitingApproval,
  })
  const wireReason = sendBlockedReason({
    live: runLive,
    draft: wire,
    sending,
    waitingApproval,
  })
  const sendSentence = () => {
    const value = turn.trim()
    if (!value || sending || images.uploading || !runLive) return
    sendTurn.mutate({
      run: attached.run.run_ref,
      value,
      asWire: false,
      draftSnapshot: sessionDraftSnapshot(draftKey),
      attachments: images.ids,
    })
  }
  const sendWire = () => {
    const value = wire.trim()
    if (!value || sending || !runLive) return
    sendTurn.mutate({ run: attached.run.run_ref, value, asWire: true })
  }

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    // Enter sends and Shift+Enter breaks a line (the binding table matches Enter alone);
    // a key that is choosing a character of an IME composition is not a send.
    if (e.nativeEvent.isComposing) return
    if (menuOpen && e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      setSlashDismissed(slash)
      return
    }
    const plain = !e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey
    if (menuOpen && slashMatches.length > 0 && plain) {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault()
        const step = e.key === 'ArrowDown' ? 1 : -1
        setSlashActive(
          (slashIndex + step + slashMatches.length) % slashMatches.length,
        )
        return
      }
      const active = slashMatches[slashIndex]
      // A name typed in full is a command already: Enter sends it (the menu ranks it
      // first), Tab still completes. Any other Enter chooses the active entry.
      const typedInFull = commandLabel(active) === turn
      if (e.key === 'Tab' || (e.key === 'Enter' && !typedInFull)) {
        e.preventDefault()
        pickCommand(active)
        return
      }
    }
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
  // THE SESSION'S MODEL AND EFFORT. The tool cannot change either inside a running
  // session, so a different choice is handed to a new session: the person sees what this
  // session keeps and starts the other one from here, preset.
  const sessionModel =
    attached.run.model_ref || attached.run.usage_model_ref || ''
  const sessionEffort = attached.run.effort ?? ''
  const [modelOpen, setModelOpen] = useState(false)
  const [draft, setDraft] = useState<{ model: string; effort: string } | null>(
    null,
  )
  const speedScope = JSON.stringify([
    boundary.key,
    attached.run.run_ref,
    attached.run.provider_profile_ref,
    draft?.model ?? sessionModel,
    attachedProfile?.updated_at,
    attachedProfile?.auth_source,
    attachedProfile?.provider_record_ref,
  ])
  const [speedChoice, setSpeedChoice] = useState<{
    scope: string
    fastMode?: boolean
    serviceTier?: string
  }>()
  const speed = speedChoice?.scope === speedScope ? speedChoice : undefined
  const chosen = draft ?? { model: sessionModel, effort: sessionEffort }
  const modelChoices = useModelChoices({
    profileRef: attached.run.provider_profile_ref ?? '',
    driver: attached.run.provider_driver ?? '',
    model: chosen.model,
    enabled: canWrite && modelOpen,
  })
  const chosenSpeed =
    speed ??
    (chosen.model === sessionModel
      ? {
          fastMode: attached.run.fast_mode,
          serviceTier: attached.run.service_tier,
        }
      : {})
  const speedModel = attachedProfile?.provider_record_ref
    ? undefined
    : modelChoices.start
  const speedUnavailable =
    (chosenSpeed.fastMode === true &&
      speedModel?.supports_fast_mode !== true) ||
    (!!chosenSpeed.serviceTier &&
      !speedModel?.service_tiers?.includes(chosenSpeed.serviceTier))
  const speedChanged =
    chosenSpeed.fastMode !== attached.run.fast_mode ||
    (chosenSpeed.serviceTier ?? '') !== (attached.run.service_tier ?? '')
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
    sessionModel || null,
    sessionEffort || null,
  ]
    .filter(Boolean)
    .join(' · ')
  const chipText = toolModel || t('agentops:create.model')
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
      // An image dropped on the card is attached, as one pasted into the field is.
      onDragOver={(e) => {
        if (runLive && e.dataTransfer.types.includes('Files'))
          e.preventDefault()
      }}
      onDrop={(e) => {
        const files = imagesIn(e.dataTransfer)
        if (!runLive || files.length === 0) return
        e.preventDefault()
        images.add(files)
      }}
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
      {menuOpen ? (
        <SlashMenu
          listId={slashListId}
          items={slashMatches}
          active={slashIndex}
          emptyText={slashEmptyText}
          notice={slashNotice}
          onPick={pickCommand}
        />
      ) : null}
      <p role="status" data-testid="slash-status" className="sr-only">
        {slashSaid}
      </p>
      {/* Its own region, so the notice is said when it appears and not with every key. */}
      <p role="status" data-testid="slash-notice-status" className="sr-only">
        {slashNotice}
      </p>
      {draftSaveFailed ? (
        <p
          role="status"
          aria-label={t('common:status.draft')}
          className="px-4 pt-3 text-caption text-text-2"
        >
          {t('nav:launcher.draftSaveFailed')}
        </p>
      ) : null}
      <AttachmentTray
        images={images.images}
        uploading={images.uploading}
        onRemove={images.remove}
      />
      <textarea
        ref={fieldRef}
        id={inputId}
        value={turn}
        rows={2}
        onChange={(e) => changeTurn(e.target.value)}
        onKeyDown={onKeyDown}
        onPaste={(e) => {
          const files = imagesIn(e.clipboardData)
          if (files.length === 0) return
          e.preventDefault()
          images.add(files)
        }}
        // Leaving the field closes the menu: its keys only reach it while it has focus.
        onBlur={() => {
          if (menuOpen) setSlashDismissed(slash)
        }}
        placeholder={placeholder}
        aria-label={t('nav:launcher.turnAria')}
        aria-autocomplete={menuOpen ? 'list' : undefined}
        aria-controls={
          menuOpen && slashMatches.length > 0 ? slashListId : undefined
        }
        aria-activedescendant={
          menuOpen && slashMatches.length > 0
            ? slashOptionId(slashListId, slashIndex)
            : undefined
        }
        data-testid="launcher-input"
        disabled={!runLive || sending}
        className="max-h-48 min-h-14 w-full resize-none bg-transparent px-4 pt-3 pb-1 text-body-l text-text outline-none placeholder:text-text-3 disabled:cursor-not-allowed"
      />
      <div className="flex min-w-0 items-center gap-2 px-3 pb-3">
        {/* ALWAYS THERE: a session whose tool has not said its model yet still takes the
            choice, and the scope of the next action lives in this popover. */}
        <Popover
          open={modelOpen}
          onOpenChange={(next) => {
            setModelOpen(next)
            if (!next) {
              setDraft(null)
              setSpeedChoice(undefined)
            }
          }}
        >
          <PopoverTrigger asChild>
            <button
              type="button"
              data-testid="composer-tool-model"
              aria-label={t('nav:launcher.modelAria', { value: chipText })}
              title={chipText}
              className="min-w-0 max-w-64 shrink truncate rounded-[6px] bg-muted px-1.5 py-0.5 text-overline font-normal text-text-2 outline-none hover:text-text focus-visible:ring-2 focus-visible:ring-focus"
            >
              {chipText}
            </button>
          </PopoverTrigger>
          <PopoverContent
            side="top"
            align="start"
            className="w-80 max-w-[calc(100vw-2rem)] p-0"
          >
            <ModelList
              driver={attached.run.provider_driver ?? ''}
              choices={modelChoices}
              value={chosen.model}
              onPick={(next) => {
                setSpeedChoice(undefined)
                setDraft({
                  model: next,
                  effort: effortAfterModel(
                    attached.run.provider_driver ?? '',
                    modelChoices.catalog,
                    next,
                    '',
                    chosen.effort,
                  ),
                })
              }}
            />
            <div className="flex flex-col gap-2 border-t border-border p-3">
              <label className="flex flex-col gap-1 text-caption text-text-2">
                {t('agentops:create.effort')}
                <EffortSelect
                  label={t('agentops:create.effort')}
                  efforts={modelChoices.efforts}
                  value={chosen.effort}
                  onChange={(effort) =>
                    setDraft({ model: chosen.model, effort })
                  }
                  defaultEffort={modelChoices.start?.default_effort}
                />
              </label>
              <LaunchSpeedFields
                driver={attached.run.provider_driver ?? ''}
                model={speedModel}
                fastMode={chosenSpeed.fastMode}
                serviceTier={chosenSpeed.serviceTier}
                onFastMode={(fastMode) =>
                  setSpeedChoice({ scope: speedScope, fastMode })
                }
                onServiceTier={(serviceTier) =>
                  setSpeedChoice({ scope: speedScope, serviceTier })
                }
              />
              {speedUnavailable ? (
                <>
                  <p
                    role="alert"
                    className="text-caption text-muted-foreground"
                  >
                    {t('agentops:create.speedUnavailable')}
                  </p>
                  <Button
                    type="button"
                    variant="link"
                    size="sm"
                    onClick={() => setSpeedChoice({ scope: speedScope })}
                  >
                    {t('agentops:create.speedDefault')}
                  </Button>
                </>
              ) : null}
              {modelChoices.efforts === null ? (
                <p className="text-caption text-muted-foreground">
                  {t('agentops:picker.effortNeedsModel')}
                </p>
              ) : null}
              {(draft &&
                (draft.model !== sessionModel ||
                  draft.effort !== sessionEffort)) ||
              speedChanged ? (
                <>
                  <p className="text-caption text-muted-foreground">
                    {t('nav:launcher.sessionKeeps', {
                      value: chipText,
                    })}
                  </p>
                  <Button
                    type="button"
                    variant="secondary"
                    size="sm"
                    data-testid="composer-new-with-choice"
                    disabled={speedUnavailable}
                    onClick={() => {
                      useNewSessionDialog.getState().openAdvanced({
                        profileRef: attached.run.provider_profile_ref,
                        // A model the tool only reported (usage) was not chosen.
                        model:
                          chosen.model === sessionModel
                            ? (attached.run.model_ref ?? '')
                            : chosen.model,
                        effort: chosen.effort,
                        ...(chosenSpeed.fastMode !== undefined
                          ? { fastMode: chosenSpeed.fastMode }
                          : {}),
                        ...(chosenSpeed.serviceTier
                          ? { serviceTier: chosenSpeed.serviceTier }
                          : {}),
                      })
                      setModelOpen(false)
                      setDraft(null)
                      setSpeedChoice(undefined)
                    }}
                  >
                    {t('nav:launcher.startWithChoice')}
                  </Button>
                </>
              ) : null}
              <ScopeLine {...attachedScope} />
            </div>
          </PopoverContent>
        </Popover>
        {/* THE RAW LINE IS A DEVELOPER'S TOOL: one NDJSON line to the tool's stdin. A
            production console does not show it (the CLI and the API keep it), and it
            carries its own name, not "Advanced". */}
        {import.meta.env.DEV ? (
          <AdvancedDisclosure label={t('nav:launcher.rawLine')}>
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
                  disabled={!runLive || sending}
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
            </div>
          </AdvancedDisclosure>
        ) : null}
        {/* SEND IS A 32 px ROUND ICON: orange only while there is something to send (the
            one primary action of the thread), a quiet ghost otherwise. It is never
            dashed: a control that cannot act says so by being quiet, and why in its
            name. */}
        <div className="ml-auto flex shrink-0 items-center gap-2">
          <AttachButton
            disabled={!runLive || sendTurn.isPending}
            onFiles={images.add}
          />
          <DictateButton
            disabled={!runLive || sending}
            field={fieldRef}
            onText={(text) => changeTurn(joinDictation(turnRef.current, text))}
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
