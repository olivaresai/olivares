// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  useIsMutating,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { ArrowUp, ChevronDown, MoreHorizontal, Shield } from 'lucide-react'
import {
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type KeyboardEvent,
  type MutableRefObject,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuShortcut,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { toast } from '@/components/ui/toaster'
import {
  PERMISSION_CHOICES,
  PERMISSION_MODE,
  PERMISSION_NAME,
  mayChoosePermission,
} from '@/features/agentops/session-launch'
import { usePermissionRestriction } from '@/features/agentops/permission-restriction'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { AttachButton, AttachmentTray } from '@/features/agentops/attachment-ui'
import { imagesIn, useImageDraft } from '@/features/agentops/attachments'
import { modelName } from '@/features/agentops/model-name'
import { SessionMode, useAccessWords } from '@/features/sessions/session-meter'
import { sessionsKeys } from '@/features/sessions/api'
import { ContextControls } from '@/features/sessions/context-controls'
import { ContextRing } from '@/features/sessions/context-ring'
import { compactSession } from '@/features/sessions/compact-session'
import { toolName } from '@/features/agentops/engine-tools'
import { ToolMark } from '@/features/agentops/tool-mark'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { useTurnInterrupt } from '@/features/agentops/turn-interrupt'
import '@/features/agentops/i18n'
import { launchFailureMessage } from '@/features/agentops/launch-readiness'
import { LaunchSpeedFields } from '@/features/agentops/launch-speed-fields'
import { findModel } from '@/features/agentops/model-catalog'
import {
  effortAfterModel,
  useModelChoices,
} from '@/features/agentops/model-choices'
import {
  EffortSelect,
  ModelList,
} from '@/features/agentops/model-effort-picker'
import {
  sendMode,
  useQueueTurn,
  useRunQueue,
} from '@/features/agentops/run-queue'
import { sendBlockedReason, sendTurn } from '@/features/agentops/session-turn'
import { BUILD_MODE, PLAN_MODE, type RunDTO } from '@/features/agentops/types'
import { isWorkBound } from '@/features/agentops/work-fence'
import { DictateButton } from '@/features/dictation/dictate-button'
import { BackgroundTaskStrip } from '@/features/sessions/background-task-strip'
import { joinDictation } from '@/features/dictation/join'
import { useNewSessionDialog } from '@/features/first-hour/new-session-store'
import { governanceApi, governanceKeys } from '@/features/governance/api'
import {
  useDecision,
  useMayDecide,
} from '@/features/governance/decision-dialog'
import '@/features/sessions/i18n'
import {
  isResumableRun,
  startsAgain,
  runAwaitedApproval,
  runLabel,
} from '@/features/sessions/provenance'
import {
  sentPlace,
  sentTurnsPartition,
  useSentTurns,
  type SentTurn,
} from '@/features/sessions/sent-turns'
import type { WorkGroupId } from '@/features/sessions/session-groups'
import { useClientSettings } from '@/features/settings/preferences'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { formatBytes } from '@/lib/format'
import {
  STASH_MAX_BYTES,
  readPromptHistory,
  readSessionDraft,
  rememberPrompt,
  sessionDraftKey,
  sessionDraftRefused,
  sessionDraftSnapshot,
  stashDraft,
  stashedDrafts,
  subscribeSessionDrafts,
  unstashDraft,
  writeSessionDraft,
} from '@/lib/session-drafts'
import { chordHint, resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { cn } from '@/lib/utils'
import { useIsPhone } from '@/lib/hooks/use-is-phone'
import { useTenantLabel } from './tenant-label'
import { SlashMenu } from './slash-menu'
import { WorkComposerQueue } from './work-composer-queue'
import {
  PICKER,
  PlanToggle,
  StashButton,
  StopButton,
} from './work-composer-controls'
import {
  commandLabel,
  filterCommands,
  insertText,
  menuKeyOf,
  mentionEntries,
  mentionFolder,
  mentionQuery,
  slashOptionId,
  slashQuery,
  type SlashMenuEmpty,
  type MenuCommand,
} from './slash-menu-model'
import {
  transcriptText,
  CONTEXT_MAX_BYTES,
} from '@/features/sessions/session-context'
import { sourceItemLabel } from '@/features/sessions/source-item'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { COMPOSER_INPUT_ID } from './work-composer-id'

const PAGE = 200

/** A picker's chevron, which a phone does without. */
const CHEVRON = 'size-3 shrink-0 max-[760px]:hidden'
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
  useEffect(() => {
    if (inputId === COMPOSER_INPUT_ID) document.getElementById(inputId)?.focus()
  }, [inputId])
  // The field of a composer that is replaced (below) leaves with its focus; the next
  // one takes it only if the person was in the field, never from another control.
  const fieldHadFocus = useRef(false)
  // Scope all local composer state at once, including an in-flight send's callbacks.
  return (
    <SessionComposer
      key={JSON.stringify([
        draftKey,
        activeTenant,
        attached.run.run_ref,
        runAwaitedApproval(attached.run),
      ])}
      frame={frame}
      attached={attached}
      draftKey={draftKey}
      inputId={inputId}
      fieldHadFocus={fieldHadFocus}
    />
  )
}

function SessionComposer({
  frame,
  attached,
  draftKey,
  inputId,
  fieldHadFocus,
}: {
  frame: ComposerFrame
  attached: ComposerAttached
  draftKey: string | null
  inputId: string
  fieldHadFocus: MutableRefObject<boolean>
}) {
  const { t } = useTranslation('nav')
  const sessionLaunch = {
    workspaceRef: attached.run.workspace_ref || undefined,
    profileRef: attached.run.provider_profile_ref,
  }
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const org = useTenantLabel()

  const approvalId = runAwaitedApproval(attached.run)
  const approvalQuery = useQuery({
    queryKey: governanceKeys.approval(activeTenant, approvalId ?? ''),
    queryFn: () => governanceApi.getApproval(approvalId!),
    enabled: !!approvalId && can('governance:approval:read'),
    refetchInterval: 12_000,
  })
  const { mayDecide } = useMayDecide(approvalQuery.data)
  const decline = useDecision(approvalId ?? '', 'reject')
  const canDecline =
    !!approvalId && mayDecide && can('governance:approval:read')
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
  useLayoutEffect(() => {
    const field = fieldRef.current
    if (fieldHadFocus.current && field) {
      field.focus()
      // The new field starts with its caret at the front; the person was at the end.
      field.setSelectionRange(field.value.length, field.value.length)
    }
    fieldHadFocus.current = false
    // React runs this before it removes the old field, so the focus is still readable.
    return () => {
      fieldHadFocus.current = document.activeElement === field
    }
  }, [fieldHadFocus])
  // The field grows with its text from 2 to 8 rows, then scrolls.
  useLayoutEffect(() => {
    const el = fieldRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, MAX_FIELD_PX)}px`
  }, [turn])
  const [accessChoice, setAccessChoice] = useState<string | null>(null)
  const accessRestriction = usePermissionRestriction()
  const [wire, setWire] = useState('')
  // The images of the message being written; they are stored with the session as they
  // are attached and go with the message by id.
  const images = useImageDraft(attached.run.run_ref)
  const sendReasonId = useId()
  const slashListId = useId()
  const wireReasonId = useId()
  // A session's sends are one queue: a send that outlives its composer instance
  // (a round trip through another session) still blocks the field it came from.
  const queryClient = useQueryClient()
  const [compactBeforeSend, setCompactBeforeSend] = useState(false)
  const compactionAbort = useRef(new AbortController())
  useEffect(() => {
    compactionAbort.current = new AbortController()
    return () => compactionAbort.current.abort()
  }, [])
  const turnQueue = ['session-turn', activeTenant, attached.run.run_ref]
  const sendMutation = useMutation({
    mutationKey: turnQueue,
    mutationFn: async (payload: {
      run: string
      value: string
      asWire: boolean
      compactOnly?: boolean
      compactFirst?: boolean
      draftSnapshot?: string | null
      attachments?: string[]
      /** The sign-in and organization the message was sent under. */
      partition?: string
      /** Where the session's conversation stood when it was sent. */
      place?: Pick<SentTurn, 'after' | 'shown'>
    }) => {
      let run = attached.run
      if (accessChoice) {
        run = await agentOpsApi.resume(run.run_ref, accessChoice)
        setAccessChoice(null)
        void queryClient.invalidateQueries({
          queryKey: agentOpsKeys.all(activeTenant),
        })
        void queryClient.invalidateQueries({
          queryKey: sessionsKeys.all(activeTenant),
        })
      }
      const signal = compactionAbort.current.signal
      if (payload.compactOnly || payload.compactFirst) {
        await compactSession(run, signal, t('sessions:compact.failed'))
        signal.throwIfAborted()
        void queryClient.invalidateQueries({
          queryKey: agentOpsKeys.runActivity(activeTenant, run.run_ref),
        })
        if (payload.compactOnly) return { accepted: true }
      }
      // A message with images is one body for every tool: the engine encodes it.
      if (payload.attachments?.length)
        return agentOpsApi.inputWithAttachments(
          run.run_ref,
          payload.value,
          payload.attachments,
        )
      return sendTurn(
        run,
        payload.value,
        payload.asWire,
        payload.compactFirst ? signal : undefined,
      )
    },
    retry: false,
    onSuccess: (_ok, payload) => {
      if (payload.compactOnly) return
      // A message that reached the tool starts its turn: the queue's hold is now "turn".
      void queryClient.invalidateQueries({
        queryKey: agentOpsKeys.runQueue(activeTenant, payload.run),
      })
      if (payload.asWire) setWire('')
      else {
        rememberPrompt(draftKey, payload.value)
        // The conversation shows the message now; the tool shows it when it takes the
        // turn, and then only its own row stays.
        // A slash command is answered by the tool, not shown as a message.
        if (
          payload.partition &&
          payload.value.trim() &&
          !payload.value.trimStart().startsWith('/')
        )
          useSentTurns.getState().note(payload.partition, payload.run, {
            text: payload.value,
            ...payload.place,
          })
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
  const sending =
    sendMutation.isPending || sendsInQueue > 0 || decline.isPending
  // THE QUEUE: while the tool works, Enter lines the message up on the server
  // and Ctrl+Enter steers it into the turn. A session started by a work item takes
  // input through its lease, so it has no queue here.
  const queueBound = canWrite && runLive && !isWorkBound(attached.run)
  const queueRead = useRunQueue(attached.run, queueBound)
  const queue = queueRead.data
  const queueTurn = useQueueTurn(attached.run, (text) => {
    rememberPrompt(draftKey, text)
    if (turnRef.current.trim() === text) changeTurn('')
  })
  const deciding = useRef(false)

  // THE "/" MENU: while the first word of the message is a slash command being typed,
  // the session's own tool says what it offers (commands, skills, agents), asked again
  // each time the menu opens so a skill added meanwhile is there. Escape closes it for
  // the word typed so far; the next character brings it back. The same menu lists, after
  // an "@" at the end of the draft, the files and folders of the session's folder.
  const slash = slashQuery(turn)
  const mention = slash === null ? mentionQuery(turn) : null
  const menuKey = menuKeyOf(turn)
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
    if (menuKeyOf(next) !== slashDismissed) setSlashDismissed(null)
  }
  // While a send is pending the field is locked and the list would only be a target
  // for a click that rewrites the draft under it.
  const menuOpen =
    canWrite &&
    !approvalId &&
    runLive &&
    !sending &&
    !!activeTenant &&
    menuKey !== null &&
    slashDismissed !== menuKey
  const commandsQuery = useQuery({
    queryKey: agentOpsKeys.runCommands(activeTenant, attached.run.run_ref),
    queryFn: ({ signal }) =>
      agentOpsApi.commands(attached.run.run_ref, { signal }),
    enabled: menuOpen && mention === null,
    // Every opening asks again (the engine caches for a few seconds); the last answer
    // shows meanwhile.
    staleTime: 0,
    // A refocused window does not re-ask while the menu is open: that would flicker the
    // one-line state and re-announce it.
    refetchOnWindowFocus: false,
    retry: false,
  })
  const catalog = commandsQuery.data
  // "@": one folder of the session's workspace at a time, read through the jailed files API.
  const workspaceRef = attached.run.workspace_ref ?? ''
  const folder = mention === null ? '' : mentionFolder(mention).folder
  const filesQuery = useQuery({
    // Its own key under the folder's: the Files browser pages the same folder with an
    // infinite query, whose cached data has another shape.
    queryKey: [
      ...agentOpsKeys.files(activeTenant, workspaceRef, folder),
      'mention',
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.listFiles(
        workspaceRef,
        { path: folder || undefined, limit: PAGE },
        { tenant: activeTenant, signal },
      ),
    enabled: menuOpen && mention !== null && workspaceRef !== '',
    refetchOnWindowFocus: false,
    retry: false,
  })
  const contextRuns = useInfiniteQuery({
    queryKey: [
      ...agentOpsKeys.boundaryScope(activeTenant, boundary.epoch),
      'context-runs',
    ],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ signal, pageParam }) =>
      agentOpsApi.listRuns(
        { limit: PAGE, pagination: 'cursor', cursor: pageParam },
        { tenant: activeTenant, signal },
      ),
    getNextPageParam: (page) => (page.has_more ? page.cursor : undefined),
    enabled:
      menuOpen &&
      mention !== null &&
      can('sessions:run:read') &&
      can('sessions:run:output'),
    retry: false,
  })
  const contextEntries = (
    contextRuns.data?.pages.flatMap((page) => page.items) ?? []
  )
    .filter((r) => r.run_ref !== attached.run.run_ref)
    .flatMap((r) => {
      const name = runLabel(r)
      const entries: MenuCommand[] = [
        {
          name,
          description: r.run_ref,
          source: '',
          context: 'session',
          kind: 'command' as const,
          insert_text: r.run_ref,
        },
      ]
      if (r.source_url?.includes('/pull/') && sourceItemLabel(r.source_url))
        entries.push({
          name: sourceItemLabel(r.source_url)!,
          description: r.source_url,
          source: '',
          context: 'pull_request',
          kind: 'command',
          insert_text: r.run_ref,
        })
      return entries
    })
    .filter((e) => e.name.toLowerCase().includes((mention ?? '').toLowerCase()))
  const slashMatches = useMemo<MenuCommand[]>(() => {
    if (!menuOpen) return []
    if (mention !== null)
      return [
        ...contextEntries,
        ...(contextRuns.hasNextPage
          ? [
              {
                name: t('common:table.loadMore'),
                description: '',
                source: '' as const,
                context: 'more' as const,
                kind: 'command' as const,
                insert_text: t('common:table.loadMore'),
              },
            ]
          : []),
        ...(filesQuery.data
          ? mentionEntries(filesQuery.data.entries, mention)
          : []),
      ]
    if (!catalog?.reported) return []
    return filterCommands(catalog.commands, slash ?? '')
  }, [
    menuOpen,
    mention,
    filesQuery.data,
    contextEntries,
    contextRuns.hasNextPage,
    t,
    catalog,
    slash,
  ])
  const slashIndex = Math.min(slashActive, Math.max(slashMatches.length - 1, 0))
  // Why there is nothing to choose: still reading (also while an old "nothing reported"
  // is being asked again), not readable, nothing reported, or nothing matches.
  const readFailed = commandsQuery.isError && !commandsQuery.isFetching
  const contextReadError =
    mention !== null && contextRuns.isError
      ? contextRuns.error instanceof ApiError
        ? contextRuns.error.message
        : t('sessions:context.readFailed')
      : ''
  const contextLoading =
    mention !== null && contextRuns.isFetching && slashMatches.length === 0
  let slashEmpty: SlashMenuEmpty = 'nomatch'
  if (mention !== null) {
    if (!filesQuery.data)
      slashEmpty =
        filesQuery.isError || workspaceRef === '' ? 'failed' : 'loading'
  } else if (!catalog) slashEmpty = readFailed ? 'failed' : 'loading'
  else if (!catalog.reported || catalog.commands.length === 0)
    slashEmpty = readFailed
      ? 'failed'
      : commandsQuery.isFetching
        ? 'loading'
        : 'unreported'
  const slashEmptyText =
    contextReadError ||
    (contextLoading
      ? t('common:states.loading')
      : mention !== null
        ? t(`nav:launcher.mention.${slashEmpty}`, { query: mention })
        : slashEmpty === 'nomatch'
          ? t('nav:launcher.slashNoMatch', { query: slash ?? '' })
          : t(`nav:launcher.slash.${slashEmpty}`))
  // What is listed may be older than the tool: the engine served what the tool reported
  // before (stale), or this console's own refresh failed and the previous list stays.
  // The one line already says the read failed when there is nothing else to show.
  const slashStale =
    menuOpen &&
    mention === null &&
    !!catalog?.reported &&
    slashEmpty !== 'failed' &&
    (catalog.stale === true || readFailed)
  const slashNotice =
    contextReadError && slashMatches.length > 0
      ? contextReadError
      : slashStale
        ? t('nav:launcher.slash.stale')
        : ''
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
  const pickCommand = async (command: MenuCommand) => {
    if (command.context === 'more') {
      if (!contextRuns.isFetchingNextPage) await contextRuns.fetchNextPage()
      return
    }
    if (
      mention !== null &&
      (command.context === 'session' || command.context === 'pull_request')
    ) {
      const original = turn
      const generation = boundary.credentialGeneration
      const guard = () => {
        if (
          useSessionStore.getState().credentialGeneration !== generation ||
          useTenantStore.getState().activeTenant !== activeTenant
        )
          throw new Error('authority changed')
      }
      setSlashDismissed(menuKey)
      try {
        guard()
        const source = contextRuns.data?.pages
          .flatMap((page) => page.items)
          .find((r) => r.run_ref === command.insert_text)
        if (!source) throw new Error('session no longer listed')
        let text: string
        if (command.context === 'pull_request') {
          if (!workspaceRef || !source.source_url)
            throw new Error('workspace required')
          const item = await agentOpsApi.sourceItem(
            workspaceRef,
            source.source_url,
            { tenant: activeTenant, dispatchGuard: guard },
          )
          text = `${item.url}\n${item.title}\n\n${item.body}`
        } else {
          const transcript = await transcriptText(source, 'User', 'Assistant', {
            tenant: activeTenant,
            dispatchGuard: guard,
          })
          if (transcript.clipped || transcript.unreadable)
            throw new Error('incomplete context')
          text = `Context from session ${source.run_ref}:\n\n${transcript.text}`
        }
        guard()
        if (new TextEncoder().encode(text).length > CONTEXT_MAX_BYTES)
          throw new Error('context too large')
        if (turnRef.current !== original) return
        changeTurn(
          original.slice(0, original.length - (mention?.length ?? 0) - 1) +
            text +
            '\n\n',
        )
        fieldRef.current?.focus()
      } catch (error) {
        if (
          useSessionStore.getState().credentialGeneration === generation &&
          useTenantStore.getState().activeTenant === activeTenant
        )
          toast.error(
            error instanceof ApiError
              ? error.message
              : t('sessions:context.readFailed'),
          )
      }
      return
    }
    if (mention === null) changeTurn(insertText(command))
    else {
      // The mention replaces the "@path" typed at the end; a folder stays open for its own.
      const text = command.name.endsWith('/')
        ? (command.insert_text ?? '')
        : insertText(command)
      changeTurn(turn.slice(0, turn.length - mention.length - 1) + text)
    }
    fieldRef.current?.focus()
  }
  // A disabled Send says why, from the state that disables it.
  const sendReason = sendBlockedReason({
    live: approvalId ? canDecline : runLive,
    draft: turn,
    sending,
    uploading: images.uploading,
    waitingApproval: !!approvalId || waitingApproval,
  })
  const wireReason = sendBlockedReason({
    live: runLive && !approvalId,
    draft: wire,
    sending,
    waitingApproval: !!approvalId || waitingApproval,
  })
  // `steer` is the person's other choice while the tool works (Ctrl+Enter): into the
  // running turn now, where the plain Enter lines the message up behind it.
  const sendSentence = async (steer = false) => {
    const value = turn.trim()
    if (!value || sendReason !== null) return
    if (approvalId) {
      const snapshot = sessionDraftSnapshot(draftKey)
      decline.mutate(
        { decision: 'reject', note: value },
        {
          onSuccess: () => {
            if (sessionDraftSnapshot(draftKey) === snapshot) changeTurn('')
          },
        },
      )
      return
    }
    if (queueTurn.isPending || deciding.current) return
    const sendNow = () =>
      sendMutation.mutate({
        run: attached.run.run_ref,
        value,
        compactFirst: compactBeforeSend,
        asWire: false,
        draftSnapshot: sessionDraftSnapshot(draftKey),
        attachments: images.ids,
        partition: sentTurnsPartition(),
        place: sentPlace(attached.run.run_ref, value),
      })
    // Images go with a direct send, as does a session no queue serves.
    if (accessChoice || !queueBound || images.ids.length > 0) return sendNow()
    // The run's state on screen may be seconds old (a turn begun a moment ago still reads
    // idle), so the engine is asked, not the card: a turn that just ended is not queued
    // behind and one that just began is not sent into. A session with nothing running or
    // waiting takes the message as it always did.
    deciding.current = true
    try {
      const fresh = await queryClient
        .fetchQuery({
          queryKey: agentOpsKeys.runQueue(activeTenant, attached.run.run_ref),
          queryFn: ({ signal }) =>
            agentOpsApi.queue(attached.run.run_ref, { signal }),
          staleTime: 0,
        })
        .catch(() => undefined)
      if (sendMode(fresh, steer) === 'queue') queueTurn.mutate(value)
      else sendNow()
    } finally {
      deciding.current = false
    }
  }
  const sendWire = () => {
    const value = wire.trim()
    if (!value || sending || !runLive || approvalId) return
    sendMutation.mutate({ run: attached.run.run_ref, value, asWire: true })
  }

  const sendKey = useClientSettings((s) => s.sendKey)
  const sendWhileRunning = useClientSettings((s) => s.sendWhileRunning)
  const turnInterrupt = useTurnInterrupt(attached.run)
  const stashed = useSyncExternalStore(subscribeSessionDrafts, () =>
    stashedDrafts(draftKey),
  )
  // ON A PHONE, ONE ROW IN EVERY STATE: Context and setting a draft aside share
  // one menu. Its Context opens the panel, which then takes focus from the menu.
  const phone = useIsPhone()
  const contextChosen = useRef(false)
  const moreRef = useRef<HTMLButtonElement>(null)

  // ARROW UP in an empty field brings back the last prompt sent, again the one before;
  // ARROW DOWN walks back toward empty. A recalled prompt of several lines keeps both
  // keys for its caret, and an edited one is the person's own text again.
  const recalled = useRef<number | null>(null)
  function recall(key: 'ArrowUp' | 'ArrowDown'): boolean {
    const history = readPromptHistory(draftKey)
    const at = recalled.current
    const showing = at !== null && history[at] === turn
    if (key === 'ArrowUp') {
      if (turn !== '' && (!showing || turn.includes('\n'))) return false
      const next = showing ? at + 1 : 0
      if (next >= history.length) return showing
      recalled.current = next
      changeTurn(history[next]!)
      return true
    }
    if (!showing || turn.includes('\n')) return false
    recalled.current = at === 0 ? null : at - 1
    changeTurn(at === 0 ? '' : history[at - 1]!)
    return true
  }

  // CTRL+S sets the draft aside, words and attachments together; in an empty field it
  // brings the last one back. The files stay stored with the session: the draft keeps
  // their ids, and a file the engine has let go since is named beside the words. What
  // became of it is one sentence, said while the field holds what it held then.
  const [stashNote, setStashNote] = useState<{
    said: string
    turn: string
    alert?: boolean
  } | null>(null)
  const stashShown = stashNote?.turn === turn ? stashNote : null
  function stashOrRestore() {
    if (draftKey === null || sending) return
    setStashNote(null)
    if (turn.trim() || images.images.length > 0) {
      if (images.uploading) return
      const attachments = images.images.flatMap((i) =>
        i.id ? [{ id: i.id, name: i.name }] : [],
      )
      const kept = stashDraft(draftKey, { text: turn, attachments })
      // Not kept: the draft stays in the field, and the person is told why.
      if (!kept.ok) {
        const said = {
          'too-large': t('nav:launcher.stashTooLarge', {
            limit: formatBytes(STASH_MAX_BYTES),
          }),
          'no-storage': t('nav:launcher.stashNotKept'),
          full: t('nav:launcher.stashNoRoom'),
        }[kept.why]
        setStashNote({ said, turn, alert: true })
        return
      }
      changeTurn('')
      images.discard(images.ids)
      if (kept.dropped.length > 0)
        setStashNote({
          said: t('nav:launcher.stashDropped', {
            words: kept.dropped.join(' · '),
          }),
          turn: '',
        })
      return
    }
    const back = unstashDraft(draftKey)
    if (!back) {
      // Said only where a draft was offered: another tab took it back, or it
      // aged out, since this one counted it.
      if (stashed > 0)
        setStashNote({ said: t('nav:launcher.stashTaken'), turn })
      return
    }
    changeTurn(back.text)
    void images.restore(back.attachments).then((names) => {
      // Said once the files are read, whatever was typed meanwhile.
      if (names.length > 0)
        setStashNote({
          said: t('nav:launcher.stashGone', { names: names.join(', ') }),
          turn: turnRef.current,
        })
    })
  }

  // PLAN FIRST is the session's plan permission mode, applied with the next message as
  // the access picker applies any mode. Pressed again, it returns to the mode it left.
  const buildBeforePlan = useRef<string | null>(null)
  const effectiveMode = accessChoice ?? attached.run.permission_mode
  const planning = effectiveMode === PLAN_MODE
  function togglePlan() {
    const left = planning ? (buildBeforePlan.current ?? BUILD_MODE) : PLAN_MODE
    if (!planning) buildBeforePlan.current = effectiveMode || BUILD_MODE
    setAccessChoice(left === attached.run.permission_mode ? null : left)
  }

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    // Enter sends and Shift+Enter breaks a line (the binding table matches Enter alone);
    // a key that is choosing a character of an IME composition is not a send.
    if (e.nativeEvent.isComposing) return
    if (menuOpen && e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      setSlashDismissed(menuKey)
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
      const typedInFull = !active.context && commandLabel(active) === turn
      if (e.key === 'Tab' || (e.key === 'Enter' && !typedInFull)) {
        e.preventDefault()
        pickCommand(active)
        return
      }
    }
    if (
      plain &&
      (e.key === 'ArrowUp' || e.key === 'ArrowDown') &&
      recall(e.key)
    ) {
      e.preventDefault()
      return
    }
    const command = resolveBinding(KEYBINDINGS, e, { launcherFocused: true })
    if (command === 'launcher.stash') {
      e.preventDefault()
      stashOrRestore()
      return
    }
    if (command !== 'launcher.start' && command !== 'launcher.startBackground')
      return
    // The person's choice (Settings › General): which key sends, and what it does while
    // the tool works. With Ctrl+Enter chosen, a plain Enter breaks the line.
    const withMod = command === 'launcher.startBackground'
    if (sendKey === 'modEnter' && !withMod) return
    e.preventDefault()
    const primarySteers = sendWhileRunning === 'steer'
    void sendSentence(
      sendKey === 'modEnter' ? primarySteers : withMod !== primarySteers,
    )
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
    enabled: canWrite,
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
  const speedChanged =
    chosenSpeed.fastMode !== attached.run.fast_mode ||
    (chosenSpeed.serviceTier ?? '') !== (attached.run.service_tier ?? '')
  // A Claude session records the exact id its catalog selector launched on
  // (claude-opus-5-5 for a launch with no model). The catalog names that id as the
  // selector's launch_model, so a listed session takes its selector's Fast mode support.
  // An id the catalog neither lists nor launches from has unknown support: its own Fast
  // mode was checked at that launch, so Off stays offered and On waits for a listed model.
  const ownModelUnlisted =
    !!sessionModel &&
    chosen.model === sessionModel &&
    !!modelChoices.catalog &&
    !findModel(modelChoices.catalog, sessionModel)
  const ownFastUnlisted =
    ownModelUnlisted && chosenSpeed.fastMode === attached.run.fast_mode
  const speedUnavailable =
    (chosenSpeed.fastMode === true &&
      !ownFastUnlisted &&
      speedModel?.supports_fast_mode !== true) ||
    (!!chosenSpeed.serviceTier &&
      !speedModel?.service_tiers?.includes(chosenSpeed.serviceTier))
  const accessText = useAccessWords(
    accessChoice
      ? { ...attached.run, permission_mode: accessChoice, tool_mode: '' }
      : attached.run,
  )
  // Read-only operators still see the scope declared by the run.
  if (!canWrite) {
    return <ScopeLine {...attachedScope} className="px-3 py-2" />
  }

  const hasText = turn.trim().length > 0
  const driver = attached.run.provider_driver
  // The same catalog name as the picker, with the exact id one hover away.
  const effortText = sessionEffort
    ? t(`agentops:effort.${sessionEffort}`, { defaultValue: sessionEffort })
    : ''
  const chipParts = (model: string) =>
    [driver ? toolName(driver) : null, model || null, effortText || null]
      .filter(Boolean)
      .join(' · ')
  const toolModel = chipParts(
    sessionModel && modelName(sessionModel, modelChoices.catalog?.models),
  )
  const chipText = toolModel || t('agentops:create.model')
  const chipTitle = sessionModel ? chipParts(sessionModel) : chipText
  // TWO PICKERS: the model by the tool's mark and its name with its effort in a word,
  // as one, and the access in the words New session offers.
  const modelText = sessionModel
    ? modelName(sessionModel, modelChoices.catalog?.models)
    : t('agentops:picker.defaultUnnamed', { tool: toolName(driver ?? '') })
  // A session that cannot take a turn says why where the sentence would go: the
  // placeholder is the one place a disabled field already is read.
  // A session that is not running offers one step, named as the header's button names it:
  // Resume, or Start again for a run that never got going. One the engine cannot resume
  // has ended, and one still starting says so.
  const notLive =
    sendReason !== 'live.blocked.notLive'
      ? null
      : attached.run.state === 'pending'
        ? t('nav:launcher.starting')
        : !isResumableRun(attached.run)
          ? t('nav:launcher.ended')
          : startsAgain(attached.run)
            ? t('nav:launcher.startAgain')
            : null
  const blockedText =
    notLive ??
    (sendReason === 'live.blocked.notLive' ||
    sendReason === 'live.blocked.approval'
      ? t(`agentops:${sendReason}`)
      : null)
  // The field teaches what can be typed: the pane header names the session.
  // While the tool works the field says what Enter does.
  // The keys it names are the person's choice (Settings › General).
  const mod = chordHint('Mod+Enter')
  const stashChord = chordHint(
    KEYBINDINGS.find((rule) => rule.command === 'launcher.stash')?.keys ?? '',
  )
  const hasDraft = hasText || images.images.length > 0
  const stashDisabled =
    !runLive || sending || images.uploading || draftKey === null
  const planDisabled =
    !runLive ||
    sending ||
    isWorkBound(attached.run) ||
    (planning
      ? attached.run.permission_mode === PLAN_MODE &&
        !accessRestriction.allows(PERMISSION_NAME.editsAndCommands)
      : !accessRestriction.allows(PERMISSION_NAME.readOnly))
  const primaryKey = sendKey === 'modEnter' ? mod : 'Enter'
  const otherKey = sendKey === 'modEnter' ? null : mod
  const queueKey = sendWhileRunning === 'steer' ? otherKey : primaryKey
  const steerKey = sendWhileRunning === 'steer' ? primaryKey : otherKey
  const customKeys = sendKey !== 'enter' || sendWhileRunning !== 'queue'
  const whileWorking = !queue
    ? ''
    : customKeys
      ? [
          queueKey
            ? t('nav:launcher.queue.placeholderQueueKey', { key: queueKey })
            : '',
          steerKey && queue.can_steer
            ? t('nav:launcher.queue.placeholderSteerKey', { key: steerKey })
            : '',
        ]
          .filter(Boolean)
          .join('; ')
      : t(
          queue.can_steer
            ? 'nav:launcher.queue.placeholderSteer'
            : 'nav:launcher.queue.placeholder',
        )
  const placeholder =
    blockedText ??
    ((queue?.hold === 'turn' && whileWorking) ||
      (attached.title
        ? t('nav:launcher.messagePlaceholder', { title: attached.title })
        : t('nav:launcher.askPlaceholder')))
  // The phone's menu: the Context panel opens from it, it sets a draft aside, and
  // Plan waits in it while it is off.
  const moreActions = (openContext: () => void) => (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          ref={moreRef}
          type="button"
          variant="ghost"
          size="sm"
          className="relative"
          aria-label={
            stashed > 0
              ? t('nav:launcher.moreActionsStashed')
              : t('nav:launcher.moreActions')
          }
          title={t('nav:launcher.moreActions')}
        >
          <MoreHorizontal aria-hidden />
          {stashed > 0 ? (
            <span
              aria-hidden
              data-testid="composer-more-dot"
              className="absolute top-1 right-1 size-1.5 rounded-full bg-accent-text"
            />
          ) : null}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        onCloseAutoFocus={(event) => {
          if (contextChosen.current) event.preventDefault()
          contextChosen.current = false
        }}
      >
        <DropdownMenuItem
          inset
          onSelect={() => {
            contextChosen.current = true
            openContext()
          }}
        >
          {t('nav:launcher.contextAction')}
          {/* How full the context is, as the Context button shows it from sm up. */}
          <span className="ml-auto">
            <ContextRing run={attached.run} />
          </span>
        </DropdownMenuItem>
        <DropdownMenuItem
          inset
          disabled={stashDisabled || (!hasDraft && stashed === 0)}
          onSelect={stashOrRestore}
        >
          {/* Bring back only names a draft that is there. */}
          {hasDraft || stashed === 0
            ? t('nav:launcher.stash')
            : t('nav:launcher.stashRestore')}
          <DropdownMenuShortcut>{stashChord}</DropdownMenuShortcut>
        </DropdownMenuItem>
        <DropdownMenuCheckboxItem
          checked={planning}
          disabled={planDisabled}
          title={accessRestriction.reason || t('nav:launcher.planHint')}
          onCheckedChange={togglePlan}
        >
          {t('nav:launcher.plan')}
        </DropdownMenuCheckboxItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
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
        // THE COMPOSER IS A CARD: raised, 14 px radius, a boundary anyone can see (the
        // control border, 3:1); its focus ring is drawn on that boundary, so the field
        // inside carries none of its own.
        'relative flex flex-col rounded-panel border border-ctl-border bg-surface shadow-[var(--shadow-raised)] hover:border-text-2',
        'focus-within:border-focus focus-within:outline-2 focus-within:-outline-offset-2 focus-within:outline-focus',
      )}
    >
      {menuOpen ? (
        <SlashMenu
          listId={slashListId}
          label={t(
            mention !== null
              ? 'nav:launcher.mentionLabel'
              : 'nav:launcher.slashLabel',
          )}
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
      {/* What the tool runs on its own, each with its Stop. */}
      <BackgroundTaskStrip run={attached.run} />
      <WorkComposerQueue run={attached.run} queue={queue} />
      {/* What became of a draft set aside: a region there from the start, so the
          sentence is said when it comes, and empty takes no room. A draft that
          could not be set aside is refused aloud instead. */}
      <p
        role="status"
        data-testid="stash-status"
        className="px-4 pt-3 text-caption text-text-2 empty:p-0"
      >
        {stashShown?.alert ? null : stashShown?.said}
      </p>
      {stashShown?.alert ? (
        <p role="alert" className="px-4 pt-3 text-caption text-text-2">
          {stashShown.said}
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
        data-session-draft-key={draftKey ?? undefined}
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
          if (menuOpen) setSlashDismissed(menuKey)
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
        disabled={!runLive && !waitingApproval}
        // A pending send locks the message in the field without taking focus from it: a
        // disabled field is blurred by the browser and nothing would give focus back.
        readOnly={sending}
        className="max-h-48 min-h-14 w-full resize-none bg-transparent px-4 pt-3.5 pb-1.5 text-body-l text-text outline-none placeholder:text-text-3 disabled:cursor-not-allowed"
      />
      {/* One row: on a phone the pickers shed their chevrons and the access its
          words, and the model's words go whole where there is no room for them,
          its mark staying. Context, setting a draft aside and Plan while it is off
          share one menu. It wraps only when it cannot hold everything. */}
      <div className="flex min-w-0 flex-wrap items-center gap-0.5 px-2 pt-1 pb-2">
        <AttachButton
          disabled={!runLive || !!approvalId || sending}
          onFiles={images.add}
        />
        {/* The tool default keeps model choices and the next action scope reachable. */}
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
              data-session-picker="model"
              aria-label={
                effortText
                  ? t('nav:launcher.modelEffortPicker', {
                      model: modelText,
                      effort: effortText,
                    })
                  : t('nav:launcher.modelPicker', { value: modelText })
              }
              title={chipTitle}
              // As wide as its words, with no cap of its own; on a phone it takes the
              // room the row leaves, never less than its mark.
              className={cn(
                PICKER,
                'max-w-none',
                'max-[760px]:min-w-11 max-[760px]:basis-0 max-[760px]:grow',
              )}
            >
              {/* THE WORDS SHOW WHOLE OR NOT AT ALL, never cut: one line as tall as
                  the mark, where words the room cannot hold wrap below, out of
                  sight, the effort first, and the mark stands alone. */}
              <span className="flex h-5 min-w-0 flex-wrap content-start items-center gap-x-1 overflow-hidden">
                <ToolMark driver={driver ?? null} />
                <span className="whitespace-nowrap">{modelText}</span>
                {effortText ? (
                  <span className="whitespace-nowrap">
                    {` · ${effortText}`}
                  </span>
                ) : null}
              </span>
              <ChevronDown aria-hidden className={CHEVRON} />
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
                supportUnknown={
                  ownModelUnlisted && !attachedProfile?.provider_record_ref
                }
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
                        ...sessionLaunch,
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
        {/* Access is applied at process start before the next turn. */}
        <Popover>
          {accessText ? (
            <PopoverTrigger asChild>
              <button
                type="button"
                data-testid="composer-access"
                data-session-picker="access"
                aria-label={t('nav:launcher.accessPicker', {
                  value: accessText,
                })}
                className={PICKER}
              >
                <Shield aria-hidden className="size-3.5 shrink-0" />
                {/* A phone shows the shield alone; its name says the words. */}
                <span className="truncate max-[760px]:hidden">
                  {accessText}
                </span>
                <ChevronDown aria-hidden className={CHEVRON} />
              </button>
            </PopoverTrigger>
          ) : null}
          <PopoverContent
            side="top"
            align="start"
            className="w-80 max-w-[calc(100vw-2rem)] p-3"
          >
            <div
              data-testid="composer-access-panel"
              className="flex flex-col items-start gap-2"
            >
              <SessionMode run={attached.run} />
              {PERMISSION_CHOICES.filter((p) =>
                mayChoosePermission(p, can('sessions:run:admin')),
              ).map((p) => (
                <Button
                  key={p}
                  type="button"
                  variant="ghost"
                  size="sm"
                  aria-pressed={
                    (accessChoice ?? attached.run.permission_mode) ===
                    (PERMISSION_MODE[p] || 'dontAsk')
                  }
                  disabled={
                    sending ||
                    isWorkBound(attached.run) ||
                    !accessRestriction.allows(PERMISSION_NAME[p])
                  }
                  title={accessRestriction.reason}
                  onClick={() =>
                    setAccessChoice(PERMISSION_MODE[p] || 'dontAsk')
                  }
                >
                  {t(`firstHour:permission.${p}`)}
                </Button>
              ))}
              <p className="text-caption text-muted-foreground">
                {t('nav:launcher.accessKeeps')}
              </p>
              <Button
                type="button"
                variant="secondary"
                size="sm"
                onClick={() =>
                  useNewSessionDialog.getState().openAdvanced({
                    ...sessionLaunch,
                    model: attached.run.model_ref ?? '',
                    effort: sessionEffort,
                  })
                }
              >
                {t('nav:launcher.newWithOtherAccess')}
              </Button>
            </div>
          </PopoverContent>
        </Popover>
        <DictateButton
          disabled={!runLive || sending}
          field={fieldRef}
          onText={(text) => changeTurn(joinDictation(turnRef.current, text))}
        />
        {/* PLAN CHANGES WHAT SEND DOES, SO IT IS NEVER HIDDEN WHILE IT IS ON: on a
            phone it waits in More actions while it is off, and shows here, pressed,
            while it is on; pressed off, it leaves and focus goes to that menu. */}
        {!phone || planning ? (
          <PlanToggle
            planning={planning}
            disabled={planDisabled}
            reason={accessRestriction.reason}
            onToggle={() => {
              if (phone) moreRef.current?.focus()
              togglePlan()
            }}
          />
        ) : null}
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
        {/* SEND IS THE SHARED ICON BUTTON, ROUND: 34 px, 44 px on a phone. Orange only
            while there is something to send (the one primary action of the thread), a
            quiet ghost otherwise. It is never dashed: a control that cannot act says so
            by being quiet, and why in its name. */}
        <div className="ml-auto flex shrink-0 items-center gap-0.5">
          <ContextControls
            run={attached.run}
            busy={sending}
            beforeSend={compactBeforeSend}
            onBeforeSend={setCompactBeforeSend}
            onCompact={() => {
              if (!canWrite || sending || attached.run.state !== 'idle') return
              sendMutation.mutate({
                run: attached.run.run_ref,
                value: '/compact',
                asWire: false,
                compactOnly: true,
              })
            }}
            more={phone ? moreActions : undefined}
          />
          {phone ? null : (
            <StashButton
              hasDraft={hasDraft}
              stashed={stashed}
              disabled={stashDisabled}
              onClick={stashOrRestore}
            />
          )}
          <StopButton turn={turnInterrupt} />
          <Button
            type="button"
            variant={
              canDecline
                ? 'danger'
                : hasText && sendReason === null
                  ? 'primary'
                  : 'ghost'
            }
            size={canDecline ? 'base' : 'icon'}
            className="max-w-full rounded-full disabled:border-solid disabled:border-transparent"
            disabled={sendReason !== null}
            aria-label={
              canDecline
                ? t('sessions:approval.declineSend')
                : t('nav:launcher.send')
            }
            aria-describedby={sendReason ? sendReasonId : undefined}
            title={
              sendReason
                ? t(`agentops:${sendReason}`)
                : `${approvalId ? t('sessions:approval.declineSend') : t('nav:launcher.send')} ${sendKey === 'modEnter' ? mod : '⏎'}`
            }
            onClick={() => void sendSentence()}
            data-testid="composer-send"
          >
            {canDecline ? t('sessions:approval.declineSend') : <ArrowUp />}
          </Button>
        </div>
        {sendReason ? (
          <span id={sendReasonId} className="sr-only">
            {notLive ?? t(`agentops:${sendReason}`)}
          </span>
        ) : null}
      </div>
    </div>
  )
  // DOCKED: the card sits at the foot of the thread, in the transcript's own column,
  // 780 px as the drawing has it.
  return frame === 'docked' ? (
    <div className="shrink-0 px-4 pt-2 pb-4">
      <div className="mx-auto w-full max-w-[780px]">{card}</div>
    </div>
  ) : (
    card
  )
}
