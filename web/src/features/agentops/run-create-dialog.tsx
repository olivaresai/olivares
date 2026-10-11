// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ProviderAccent } from './provider-accent'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { AlertTriangle } from 'lucide-react'
import {
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { DisabledReason } from '@/components/ui/disabled-reason'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { ModelAvailabilityPicker } from '@/features/models/availability-picker'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { templatesApi, templatesKeys } from '@/features/workspace-templates/api'
import { ListTruncationBadge } from '@/features/_intel'
import { agentOpsApi, agentOpsKeys } from './api'
import { launchSession } from './session-launch'
import { profileOwnedEnvNames } from './provider-contract'
import { toolName } from './tool-names'
import { useAuthBoundary } from './auth-boundary'
import { useStepUpOwner, type StepUpAttempt } from '@/stores/step-up'
import { LaunchIdentityFields, useLaunchIdentities } from './launch-identity'
import {
  LaunchReadinessPanel,
  useProfileLaunchReadiness,
} from './launch-readiness-panel'
import {
  DEFAULT_READINESS_ISOLATION,
  checkHasCause,
  currentLaunchReadinessObservation,
  effectiveLaunchTransport,
  isProfileChangedError,
  launchFailureMessage,
  readinessObservationIsUnusable,
  stoppedAtStartReason,
  launchRequestPermission,
  type SessionLaunchReadiness,
} from './launch-readiness'
import {
  CRITICAL_PERMISSION_MODES,
  EFFORT_LEVELS,
  PERMISSION_MODES,
  type CreateRunRequest,
  type PermissionMode,
  type RunDTO,
  type Transport,
} from './types'
import './i18n'

const NONE = '__none__'
const DEFAULT_EFFORT = '__default__'

/**
 * RunCreateDialog — the advanced launch: the profile and a first message up front, and
 * the visual equivalent of the CLI launch (identity / name / transport / permission mode /
 * effort / model / workspace / template / env) under Advanced options, each on today's
 * default. The governance posture is explicit BEFORE launch: a
 * privileged permission mode (or a read-write classified workspace) warns that the launch
 * needs human approval and is recorded. A Start that cannot act says beside it what is
 * missing, and the backend's honest denial (402/403/429 — budget cap, pending approval) is
 * shown verbatim rather than as a generic error.
 */
export function RunCreateDialog({
  open,
  onOpenChange,
  initialTemplateId,
  initialWorktreeFrom,
  onCloseAutoFocus,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  /** Pre-select a workspace template (the templates catalog launches through here, so
   * "Apply to session" means the session actually comes up under the template). */
  initialTemplateId?: string
  /** Open with a new worktree chosen, starting at this commit id or branch (the work a
   * handoff names). The person still picks the workspace whose repository holds it. */
  initialWorktreeFrom?: string
  /** Where focus goes on close, for an opener that is gone by then (the New session
   * form hands over to this dialog and unmounts its own button). */
  onCloseAutoFocus?: (event: Event) => void
}) {
  const { t } = useTranslation('agentops')
  const navigate = useNavigate()
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const qc = useQueryClient()
  const canRunWrite = can('sessions:run:write')
  const advancedId = useId()
  const promptId = useId()

  const [prompt, setPrompt] = useState('')
  // Closed, so the first screen is the profile, the message and Start. A template opened
  // from the catalog opens it: that choice is the point of the launch.
  const [advanced, setAdvanced] = useState(
    !!initialTemplateId || !!initialWorktreeFrom,
  )
  const [name, setName] = useState('')
  const [transport, setTransport] = useState<Transport>('stream-json')
  const [permissionMode, setPermissionMode] =
    useState<PermissionMode>('default')
  const [effort, setEffort] = useState<string>(DEFAULT_EFFORT)
  const [model, setModel] = useState('')
  const [workspaceRef, setWorkspaceRef] = useState<string>(NONE)
  const [worktree, setWorktree] = useState(!!initialWorktreeFrom)
  const [envAllow, setEnvAllow] = useState('')
  const [templateId, setTemplateId] = useState<string>(
    initialTemplateId ?? NONE,
  )
  // The profile the operator picked; NONE until they pick one (see profileRef below).
  const [chosenProfile, setChosenProfile] = useState<string>(NONE)
  const [agentRef, setAgentRef] = useState('')
  const identities = useLaunchIdentities(open && agentRef !== '')
  const captureOwner = useStepUpOwner()

  // Adjust the draft when the parent opens another template. This is a guarded
  // update of this component during render, so no effect can overwrite a choice.
  const [previousSelection, setPreviousSelection] = useState({
    open,
    initialTemplateId,
    initialWorktreeFrom,
  })
  if (
    previousSelection.open !== open ||
    previousSelection.initialTemplateId !== initialTemplateId ||
    previousSelection.initialWorktreeFrom !== initialWorktreeFrom
  ) {
    setPreviousSelection({ open, initialTemplateId, initialWorktreeFrom })
    if (open) {
      setTemplateId(initialTemplateId ?? NONE)
      if (initialWorktreeFrom) setWorktree(true)
      if (initialTemplateId || initialWorktreeFrom) setAdvanced(true)
    }
  }

  const canReadWs = can('sessions:workspace:read')
  const wsQuery = useQuery({
    queryKey: agentOpsKeys.workspaces(activeTenant),
    queryFn: () => agentOpsApi.listWorkspaces({ limit: 200 }),
    enabled: open && canReadWs,
  })
  const workspaces = useMemo(
    () => (wsQuery.data?.items ?? []).filter((w) => w.state === 'active'),
    [wsQuery.data],
  )
  const selectedWs = workspaces.find((w) => w.workspace_ref === workspaceRef)

  // The profile picked here was learned under ONE authority boundary (principal,
  // tenant, credential). When the boundary moves — a renewal rotates the credential
  // under the same session id — that choice is forgotten with the list it came from;
  // the read below restarts under the current boundary's own key. Guarded update of
  // this component during render, like the template adjustment above.
  const [seenBoundary, setSeenBoundary] = useState(boundary.key)
  if (seenBoundary !== boundary.key) {
    setSeenBoundary(boundary.key)
    setChosenProfile(NONE)
    setAgentRef('')
  }

  const canReadProfiles = can('sessions:profile:read')
  const profilesQuery = useQuery({
    queryKey: agentOpsKeys.profiles(activeTenant, boundary.epoch, {
      state: 'active',
    }),
    // The signal is TanStack's: cancelling the previous boundary's scope aborts a
    // read still in flight instead of letting its answer land late.
    queryFn: ({ signal }) =>
      agentOpsApi.listProfiles({ state: 'active', limit: 200 }, { signal }),
    enabled: open && canReadProfiles,
  })
  const profiles = useMemo(
    () => profilesQuery.data?.items ?? [],
    [profilesQuery.data],
  )
  // Defaults follow what exists: the only active profile of the
  // current boundary's list is the one a session can run on, so it is the choice until
  // the operator picks another. With several, none is chosen and Start says so. Only the
  // reference still leaves the browser, and the engine still validates it.
  // A choice the list no longer holds (retired, revoked) is no choice.
  const chosen = profiles.some((p) => p.profile_ref === chosenProfile)
    ? chosenProfile
    : NONE
  const profileRef =
    chosen !== NONE
      ? chosen
      : profiles.length === 1
        ? profiles[0].profile_ref
        : NONE
  const selectedProfile = profiles.find((p) => p.profile_ref === profileRef)
  const requiresAgent =
    selectedProfile?.session_work_grant?.role === 'orchestrator'
  const agentReady =
    agentRef === ''
      ? !requiresAgent
      : identities.agents.some((a) => a.identity_ref === agentRef)
  const liveAuthority = useRef({
    key: boundary.key,
    canRunWrite,
    agentRef,
    profileRef,
  })
  // A layout effect, as for the other authority refs: the dispatch guard reads this after an
  // await, so it holds the committed authority before any promise callback can run.
  useLayoutEffect(() => {
    liveAuthority.current = {
      key: boundary.key,
      canRunWrite,
      agentRef,
      profileRef,
    }
  })
  // The server refuses an env_allow that names a variable the profile owns; say so
  // before the 400 rather than after it. The list is the server's WHOLE owned family
  // (providerHomeEnvName), not the current driver's two: the engine refuses another
  // provider's home variable as well, so a Codex operator forwarding CODEX_HOME has to
  // hear it here instead of from an avoidable 400. Ordinary names are untouched.
  const profileEnvConflict =
    profileRef !== NONE && profileOwnedEnvNames(envAllow).length > 0

  const canReadTemplates = can('sessions:template:read')
  const tplQuery = useQuery({
    queryKey: templatesKeys.list(activeTenant, { launch: true }),
    queryFn: () => templatesApi.list(),
    enabled: open && canReadTemplates,
  })
  const templates = useMemo(
    () => (tplQuery.data?.items ?? []).filter((tpl) => !tpl.archived_at),
    [tplQuery.data],
  )

  // The merge PREVIEW, from the engine — never recomputed in the browser. The whole
  // point of the pack is that the server owns this merge; a second implementation here
  // would be a second answer, and the one the launch uses is the server's.
  const preview = useQuery({
    // Every input the preview depends on is in the key. workspace_ref was missing, so
    // switching workspaces showed the previous target's answer (Codex contrast, 2026-08-11).
    queryKey: [
      ...templatesKeys.detail(activeTenant, templateId),
      'apply',
      transport,
      permissionMode,
      effort,
      model,
      workspaceRef,
    ],
    queryFn: () =>
      templatesApi.apply(templateId, {
        transport,
        permission_mode: permissionMode,
        effort: effort === DEFAULT_EFFORT ? undefined : effort,
        model: model.trim() || undefined,
        workspace_ref: workspaceRef === NONE ? undefined : workspaceRef,
      }),
    enabled: open && templateId !== NONE && canReadTemplates,
  })
  const previewData = templateId === NONE ? undefined : preview.data
  const previewReady =
    templateId === NONE || preview.isSuccess || preview.isError
  const effectiveTransport = effectiveLaunchTransport(
    transport,
    previewData?.merged?.transport,
  )
  const readinessQuery = useProfileLaunchReadiness({
    enabled: open && profileRef !== NONE && previewReady,
    profileRef: profileRef === NONE ? null : profileRef,
    transport: effectiveTransport,
    isolation: DEFAULT_READINESS_ISOLATION,
    profileState: selectedProfile?.state,
    authSource: selectedProfile?.auth_source,
    updatedAt: selectedProfile?.updated_at,
  })
  const currentReadiness = currentLaunchReadinessObservation(readinessQuery, {
    profileRef,
    transport: effectiveTransport,
    isolation: DEFAULT_READINESS_ISOLATION,
  })
  const requestPermission = launchRequestPermission(currentReadiness)

  // The warning has to describe the mode the session will actually run in. A template
  // with a tool allow-list pins dontAsk — a CRITICAL launch that needs human approval and
  // is recorded — and reading the raw form field here showed "default" with no warning at
  // all for exactly that launch (Codex contrast, 2026-08-11).
  const effectiveMode = (previewData?.merged?.permission_mode ??
    permissionMode) as PermissionMode
  const isCriticalMode = CRITICAL_PERMISSION_MODES.includes(effectiveMode)
  const isClassifiedRw =
    !!selectedWs &&
    selectedWs.dlp_mode !== 'off' &&
    selectedWs.mount_mode === 'rw'

  /** The requirement that stops this profile, in the panel's own words. */
  function readinessCause(data: SessionLaunchReadiness): string {
    const check =
      data.checks.find((c) => c.state === data.configuration_state) ??
      data.checks.find(checkHasCause)
    if (!check) return t('readiness.requestBlocked')
    const cause = t(`readiness.code.${check.code}`)
    return check.remediation
      ? `${cause} ${t(`readiness.remediation.${check.remediation}`)}`
      : cause
  }

  const reread = (
    <Button
      type="button"
      variant="link"
      size="sm"
      onClick={() => void readinessQuery.refetch()}
    >
      {t('readiness.reread')}
    </Button>
  )
  // What keeps Start from acting, first missing piece first. It is said beside the
  // button (a disabled button that says nothing is a dead end), and it is
  // the ONLY gate: the button, Enter and the mutation all read this one answer.
  const blocker = ((): { reason: string; action?: ReactNode } | null => {
    if (!canRunWrite) return { reason: t('readiness.noWrite') }
    if (!canReadProfiles)
      return { reason: t('create.blocked.profilesForbidden') }
    if (profilesQuery.isError && !profilesQuery.data)
      return {
        reason: t('create.blocked.profilesFailed'),
        action: (
          <Button
            type="button"
            variant="link"
            size="sm"
            onClick={() => void profilesQuery.refetch()}
          >
            {t('profiles.authentication.retry')}
          </Button>
        ),
      }
    if (!profilesQuery.data)
      return { reason: t('create.blocked.profilesLoading') }
    if (profiles.length === 0)
      return {
        reason: t('create.blocked.noProfile'),
        action: <a href="/providers">{t('create.blocked.openProviders')}</a>,
      }
    if (profileRef === NONE)
      return { reason: t('create.blocked.chooseProfile') }
    if (worktree && !selectedWs)
      return { reason: t('create.blocked.worktreeFolder') }
    if (requiresAgent && agentRef === '')
      return { reason: t('create.identity.required') }
    if (!agentReady) return { reason: t('create.blocked.chooseAgent') }
    if (profileEnvConflict) return { reason: t('create.blocked.envConflict') }
    if (!previewReady) return { reason: t('readiness.waitingPreview') }
    if (previewData?.applied === false)
      return { reason: t('create.blocked.templateRefused') }
    if (currentReadiness)
      return requestPermission === 'permit' || requestPermission === 'uncertain'
        ? null
        : { reason: readinessCause(currentReadiness) }
    const err = readinessQuery.error
    if (isProfileChangedError(err))
      return { reason: t('readiness.conflict'), action: reread }
    if (readinessObservationIsUnusable(err))
      return {
        reason:
          err instanceof ApiError && err.status === 401
            ? t('readiness.unauthenticated')
            : err instanceof ApiError && err.status === 403
              ? t('readiness.forbidden')
              : t('readiness.notFound'),
      }
    if (readinessQuery.isFetching) return { reason: t('readiness.loading') }
    return { reason: t('create.blocked.readinessFailed'), action: reread }
  })()

  const reset = () => {
    setPrompt('')
    setAdvanced(false)
    setName('')
    setTransport('stream-json')
    setPermissionMode('default')
    setEffort(DEFAULT_EFFORT)
    setModel('')
    setWorkspaceRef(NONE)
    setWorktree(false)
    setEnvAllow('')
    setTemplateId(NONE)
    setChosenProfile(NONE)
    setAgentRef('')
  }

  const requestBody = (): CreateRunRequest => ({
    // Without a name of its own, the launch names it by what it was asked first.
    name,
    transport,
    permission_mode: permissionMode,
    effort: effort === DEFAULT_EFFORT ? '' : effort,
    model,
    workspace_ref: workspaceRef === NONE ? '' : workspaceRef,
    isolation: 'native',
    env_allow: envAllow
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean),
    // The blocker requires a folder for any requested worktree. Keep the intent in
    // the body, rather than silently changing it to an ordinary launch.
    ...(worktree
      ? {
          worktree: true,
          // The start of a handoff's work, only when the dialog was opened with one.
          ...(initialWorktreeFrom
            ? { worktree_from: initialWorktreeFrom }
            : {}),
        }
      : {}),
    ...(templateId === NONE ? {} : { template_id: templateId }),
    // Only the REFERENCE leaves the browser: the server resolves the homes.
    ...(profileRef === NONE ? {} : { provider_profile_ref: profileRef }),
  })

  const create = useMutation({
    mutationFn: async ({
      body,
      actor,
      tenant,
      boundaryKey,
      attempt,
      message,
    }: {
      body: CreateRunRequest
      actor: string
      tenant: string | null
      boundaryKey: string
      attempt: StepUpAttempt
      message: string
    }): Promise<RunDTO> => {
      if (
        blocker ||
        profileRef === NONE ||
        body.provider_profile_ref !== profileRef ||
        actor !== agentRef
      ) {
        throw new Error(blocker?.reason ?? t('create.blocked.chooseProfile'))
      }
      const dispatchGuard = () => {
        attempt.dispatchGuard()
        if (
          liveAuthority.current.key !== boundaryKey ||
          !liveAuthority.current.canRunWrite ||
          liveAuthority.current.agentRef !== actor ||
          liveAuthority.current.profileRef !== body.provider_profile_ref
        )
          throw new Error(t('profiles.authority.lost'))
      }
      // The one launch both New session forms use, under this dialog's guard.
      return launchSession(
        { run: body, actor, message },
        { tenant, signal: attempt.signal, dispatchGuard },
      )
    },
    retry: false,
    onSuccess: (run, intent) => {
      if (!intent.attempt.current()) return
      void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
      toast.success(t('create.success'))
      reset()
      onOpenChange(false)
      // The conversation in front, as the New session form does.
      if (run?.run_ref)
        void navigate({
          to: '/sessions' as '/',
          search: { session: `run:${run.run_ref}`, pane: 'narrative' } as never,
        })
    },
  })

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (!create.isPending && !blocker)
      create.mutate({
        body: requestBody(),
        actor: agentRef,
        tenant: activeTenant,
        boundaryKey: boundary.key,
        attempt: captureOwner().begin(),
        message: prompt,
      })
  }

  // An orchestration profile needs an agent identity, so the choice comes forward; and a
  // non-default identity never hides behind a closed Advanced options.
  const identityUpFront = requiresAgent || (agentRef !== '' && !advanced)
  const identityFields = (
    <LaunchIdentityFields
      value={agentRef}
      onChange={setAgentRef}
      identities={identities}
      pending={create.isPending}
    />
  )

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => (create.isPending ? undefined : onOpenChange(o))}
    >
      {/* ONE SCROLL OWNER, AND IT IS THIS DIALOG'S. DialogContent is fixed and
          centred with no height bound (components/ui/dialog.tsx), so a fully
          expanded New session form grew past the viewport with no scrolling
          ancestor and the footer sat below the fold — unreachable by pointer,
          which is how the keyboard path came to be the only way to submit. The
          bound lives here rather than on the shared primitive: 99 files consume
          DialogContent, and a height rule applied to all of them is a redesign,
          not this repair. Header and footer stay pinned so Cancel, Close and
          Start are always on screen; only the field region scrolls. */}
      <DialogContent
        className="flex max-h-[calc(100dvh-2rem)] max-w-lg flex-col overflow-hidden"
        onCloseAutoFocus={onCloseAutoFocus}
        // The first message is what this dialog asks for: start there.
        onOpenAutoFocus={(event) => {
          event.preventDefault()
          document.getElementById(promptId)?.focus()
        }}
      >
        <DialogHeader>
          <DialogTitle>
            {/* The chosen tool, never a fixed one (HU 043: a Grok profile read "New
                Claude Code session"). */}
            {selectedProfile
              ? t('create.titleTool', {
                  tool: toolName(selectedProfile.driver),
                })
              : t('create.title')}
          </DialogTitle>
          <DialogDescription>{t('create.description')}</DialogDescription>
        </DialogHeader>

        <form
          onSubmit={onSubmit}
          className="flex min-h-0 flex-1 flex-col gap-3"
        >
          {/* min-h-0 lets this shrink inside the flex column; the negative margin
              with matching padding keeps focus rings from clipping at the edge. */}
          <div className="-mx-1 flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-1">
            {canReadProfiles && (
              <Field label={t('create.profile')}>
                {/* Only a profile of this list is a choice. Radix bubbles '' through
                    its hidden native select when the value moves to the preselected
                    profile before that option is mounted, and taking it would undo the
                    preselection. */}
                <Select
                  value={profileRef}
                  onValueChange={(v) => {
                    if (profiles.some((p) => p.profile_ref === v))
                      setChosenProfile(v)
                  }}
                >
                  <SelectTrigger aria-label={t('create.profile')}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={NONE} disabled>
                      {t('create.profileNone')}
                    </SelectItem>
                    {profiles.map((p) => (
                      <SelectItem
                        key={p.profile_ref}
                        value={p.profile_ref}
                        aria-description={p.profile_ref}
                        title={p.profile_ref}
                      >
                        <ProviderAccent accent={p.accent} />
                        {p.display_name || p.profile_ref} · {p.driver}
                        {!p.operable && ` — ${t('create.profileNotOperable')}`}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            )}

            <Field label={t('create.prompt')} htmlFor={promptId}>
              <Textarea
                id={promptId}
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                placeholder={t('create.promptPlaceholder')}
                rows={3}
              />
            </Field>

            {identityUpFront && identityFields}

            <div>
              <Button
                type="button"
                variant="link"
                aria-expanded={advanced}
                aria-controls={advanced ? advancedId : undefined}
                onClick={() => setAdvanced((v) => !v)}
              >
                {t('create.advanced')}
              </Button>
            </div>

            {advanced && (
              <div id={advancedId} className="flex flex-col gap-3">
                {!identityUpFront && identityFields}
                <Field label={t('create.name')}>
                  <Input
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder={t('create.namePlaceholder')}
                  />
                </Field>

                <div className="grid grid-cols-2 gap-3">
                  <Field label={t('create.transport')}>
                    <Select
                      value={transport}
                      onValueChange={(v) => setTransport(v as Transport)}
                    >
                      <SelectTrigger aria-label={t('create.transport')}>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="stream-json">
                          {t('transport.stream-json')}
                        </SelectItem>
                        <SelectItem value="remote-control">
                          {t('transport.remote-control')}
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </Field>

                  <Field label={t('create.permissionMode')}>
                    <Select
                      value={permissionMode}
                      onValueChange={(v) =>
                        setPermissionMode(v as PermissionMode)
                      }
                    >
                      <SelectTrigger aria-label={t('create.permissionMode')}>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {PERMISSION_MODES.map((m) => (
                          <SelectItem key={m} value={m}>
                            {t(`permissionMode.${m}`)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>
                </div>

                <div className="grid grid-cols-2 gap-3">
                  <Field label={t('create.effort')}>
                    <Select value={effort} onValueChange={setEffort}>
                      <SelectTrigger aria-label={t('create.effort')}>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value={DEFAULT_EFFORT}>
                          {t('create.effortDefault')}
                        </SelectItem>
                        {EFFORT_LEVELS.map((e) => (
                          <SelectItem key={e} value={e}>
                            {t(`effort.${e}`)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>

                  <Field label={t('create.model')}>
                    <ModelAvailabilityPicker
                      profile={selectedProfile}
                      value={model}
                      onChange={setModel}
                      enabled={open}
                    />
                  </Field>
                </div>

                <Field label={t('create.workspace')}>
                  <Select
                    value={workspaceRef}
                    onValueChange={(value) => {
                      setWorkspaceRef(value)
                      if (value === NONE && !initialWorktreeFrom)
                        setWorktree(false)
                    }}
                  >
                    <SelectTrigger aria-label={t('create.workspace')}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={NONE}>
                        {t('create.workspaceNone')}
                      </SelectItem>
                      {workspaces.map((w) => (
                        <SelectItem
                          key={w.workspace_ref}
                          value={w.workspace_ref}
                        >
                          {w.name || w.workspace_ref} (
                          {t(`workspaces.mount.${w.mount_mode}`, {
                            defaultValue: w.mount_mode,
                          })}
                          )
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>

                {(workspaceRef !== NONE || worktree || initialWorktreeFrom) && (
                  <div className="flex items-start gap-3">
                    <Checkbox
                      id="create-worktree"
                      checked={worktree}
                      onCheckedChange={(checked) =>
                        setWorktree(checked === true)
                      }
                      aria-describedby="create-worktree-hint"
                    />
                    <div className="flex flex-col gap-1">
                      <label htmlFor="create-worktree" className="text-body">
                        {t('create.worktree')}
                      </label>
                      <p
                        id="create-worktree-hint"
                        className="text-caption text-muted-foreground"
                      >
                        {t('create.worktreeHint')}
                      </p>
                      {initialWorktreeFrom && worktree ? (
                        <p
                          data-testid="create-worktree-from"
                          className="text-caption text-muted-foreground"
                        >
                          {t('create.worktreeFrom')}{' '}
                          <span className="break-all font-mono text-foreground">
                            {initialWorktreeFrom}
                          </span>
                        </p>
                      ) : null}
                    </div>
                  </div>
                )}

                {canReadTemplates && (
                  <div className="flex flex-col gap-2">
                    <ListTruncationBadge
                      query={tplQuery}
                      label={t('listTruncation.label', {
                        n: tplQuery.data?.items?.length,
                      })}
                      hint={t('listTruncation.hint')}
                      className="px-0 pt-0"
                    />
                    <Field
                      label={t('create.template')}
                      description={t('create.templateHint')}
                    >
                      <Select value={templateId} onValueChange={setTemplateId}>
                        <SelectTrigger aria-label={t('create.template')}>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value={NONE}>
                            {t('create.templateNone')}
                          </SelectItem>
                          {templates.map((tpl) => (
                            <SelectItem key={tpl.id} value={tpl.id}>
                              {tpl.name}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </Field>
                  </div>
                )}

                {/* The engine's own verdict on this template, before the launch. A
                  template that declares something the launch cannot keep REFUSES the
                  launch — so it is shown here rather than as a surprise 422. */}
                {previewData && !previewData.applied && (
                  <div className="flex items-start gap-2 rounded-md border border-danger-line bg-danger-soft px-2.5 py-2 text-caption text-danger">
                    <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                    <div className="min-w-0">
                      <p>{t('create.templateUnenforceable')}</p>
                      <ul className="mt-1 list-disc space-y-0.5 pl-4">
                        {(previewData.unenforceable ?? []).map((reason) => (
                          <li key={reason} className="break-words">
                            {reason}
                          </li>
                        ))}
                      </ul>
                    </div>
                  </div>
                )}
                {previewData?.applied && previewData.conflicts.length > 0 && (
                  <div className="flex items-start gap-2 rounded-md border border-warning-line bg-warning-soft px-2.5 py-2 text-caption text-warning">
                    <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                    <div className="min-w-0">
                      <p>
                        {t('create.templateOverrides', {
                          count: previewData.conflicts.length,
                        })}
                      </p>
                      <ul className="mt-1 list-disc space-y-0.5 pl-4">
                        {previewData.conflicts.map((c) => (
                          <li key={c.field} className="break-words">
                            <span className="font-mono">{c.field}</span>:{' '}
                            {String(c.old_value)} →{' '}
                            <span className="font-medium">
                              {String(c.new_value)}
                            </span>
                          </li>
                        ))}
                      </ul>
                    </div>
                  </div>
                )}

                <Field
                  label={t('create.envAllow')}
                  description={t('create.envAllowHint')}
                >
                  <Input
                    value={envAllow}
                    onChange={(e) => setEnvAllow(e.target.value)}
                    placeholder={t('create.envAllowPlaceholder')}
                    mono
                  />
                </Field>
                {profileEnvConflict && (
                  <div className="flex items-start gap-2 rounded-md border border-danger-line bg-danger-soft px-2.5 py-2 text-caption text-danger">
                    <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                    <span>{t('create.profileEnvConflict')}</span>
                  </div>
                )}

                <p className="text-caption text-muted-foreground">
                  {t('create.isolationNativeOnly')}
                </p>

                {/* The chosen profile's requirements in full; what stops the launch is
                    also said beside Start. */}
                {selectedProfile && (
                  <p className="font-mono text-caption text-muted-foreground">
                    {selectedProfile.profile_ref} ·{' '}
                    {selectedProfile.environment_ref}
                  </p>
                )}
                {profileRef !== NONE && previewReady && (
                  <LaunchReadinessPanel
                    query={readinessQuery}
                    profileRef={profileRef}
                    transport={effectiveTransport}
                    isolation={DEFAULT_READINESS_ISOLATION}
                    compact
                  />
                )}
                {requestPermission === 'permit' && currentReadiness && (
                  <p className="text-caption text-muted-foreground">
                    {t('readiness.requestHintReady')}
                  </p>
                )}
              </div>
            )}

            {requestPermission === 'uncertain' && currentReadiness && (
              <p className="text-caption text-muted-foreground">
                {t('readiness.requestHintUnknown')}
              </p>
            )}

            {(isCriticalMode || isClassifiedRw) && (
              <div className="flex items-start gap-2 rounded-md border border-warning-line bg-warning-soft px-2.5 py-2 text-caption text-warning">
                <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                <span>
                  {isCriticalMode
                    ? t('create.criticalWarning')
                    : t('create.classifiedWarning')}
                </span>
              </div>
            )}

            {create.isError && (
              <div
                className="flex items-start gap-2 rounded-md border border-danger-line bg-danger-soft px-2.5 py-2 text-caption text-danger"
                role="alert"
              >
                <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                <div className="min-w-0">
                  <p>
                    {stoppedAtStartReason(create.error) !== null
                      ? t('readiness.stoppedAtStart')
                      : t('readiness.failedInline')}
                  </p>
                  <p className="mt-0.5 break-words">
                    {launchFailureMessage(
                      create.error,
                      t('create.title'),
                      (reason) => reason || t('readiness.stoppedSilently'),
                    )}
                  </p>
                  {isProfileChangedError(create.error) && (
                    <Button
                      type="button"
                      variant="secondary"
                      size="sm"
                      className="mt-2"
                      onClick={() => void readinessQuery.refetch()}
                    >
                      {t('readiness.reread')}
                    </Button>
                  )}
                </div>
              </div>
            )}
          </div>

          <DialogFooter className="sm:flex-wrap">
            <Button
              type="button"
              variant="secondary"
              onClick={() => onOpenChange(false)}
              disabled={create.isPending}
            >
              {t('browser.cancel')}
            </Button>
            {/* DisabledReason checks its text even while Start can act, when it
                renders none; the label stands in then. */}
            <DisabledReason
              disabled={!!blocker}
              reason={blocker?.reason ?? t('create.submit')}
              action={blocker?.action}
              mode="native"
              // Start stays beside Cancel; the reason takes a line of its own.
              className="contents [&>[data-slot=disabled-reason-text]]:basis-full [&>[data-slot=disabled-reason-text]]:justify-end"
            >
              <Button
                type="submit"
                variant="primary"
                disabled={create.isPending}
              >
                {create.isPending && <Spinner className="size-3.5" />}
                {create.isPending ? t('create.submitting') : t('create.submit')}
              </Button>
            </DisabledReason>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
