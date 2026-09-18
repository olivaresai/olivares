// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE PLACE TO SAY WHAT TO RUN, AND ONE LINE SAYING WHAT IT WILL APPLY TO.
//
// The defect was one sentence: *"I am not able to launch a session easily without
// filling in configuration by hand in an ugly, old console."* A review turned it into
// two rules — a permanent action surface, and a status line that always names the scope
// of the next action — and this is both, in the shell, on every route.
//
// ⛔ IT IS NOT A CHAT COMPOSER, AND PRETENDING OTHERWISE WOULD BE CARGO CULT. A chat
//    composer sends a message to a model. This one starts a governed session on a
//    control plane: the field is what the session is CALLED, the pill is the provider
//    profile it runs as, and the scope beside it is what it will touch. A surface
//    without the product behind it is the anti-pattern that review named.
//
// ⛔ NOTHING IS OFFERED THAT IS NOT AUTHORIZED. Without `sessions:run:write` there is
//    nothing to start, so the launcher is not rendered at all — an offer that ends in a
//    403 is the *magic pushbutton* the front door no longer offers. The SCOPE LINE is
//    rendered either way: knowing what the next action would apply to is not a
//    privilege, and a shell that only tells you where you are when you may act there is
//    telling you least when you can do least.
//
// ⛔ AND WITH NO PROFILE REGISTERED IT SAYS SO AND OFFERS THE ONE ACTION. A disabled
//    field with no reason is the console this one replaces. B2 makes the profile mandatory
//    server-side (`resolveLaunchProfileInto` answers 400 "select a provider profile
//    before launching a session"), so with none there is nothing to type INTO — and the
//    honest screen is the sentence plus the door to the plane that creates one.
//
// ⛔ THE LAUNCH PATH IS THE ENGINE'S, NOT A SECOND COPY OF IT. `agentOpsApi.createRun`
//    is the call the full dialog makes; the refusal an operator sees is
//    `launchFailureMessage`, the engine's own answer, not a sentence composed here. The
//    dialog keeps the readiness PREFLIGHT and every other field, one click away: this
//    is the fast path, not a replacement for it.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { IdCard, Layers, Plus, Send } from 'lucide-react'
import { useState, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Kbd } from '@/components/ui/kbd'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { launchFailureMessage } from '@/features/agentops/launch-readiness'
import type { CreateRunRequest } from '@/features/agentops/types'
import { SESSION_PARAM } from '@/features/sessions/session-address'
import { useAuth } from '@/lib/auth/context'
import { resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { cn } from '@/lib/utils'
import { LAUNCHER_INPUT_ID } from './shell-launcher-id'

/** Sentinel for "no workspace", which is a legal launch and not an unset field. */
const NO_WORKSPACE = '__none__'
/** The page the two pickers ask for. Both planes cap far above this. */
const PAGE = 200

/**
 * THE SCOPE OF THE NEXT ACTION, always on screen.
 *
 * ⛔ THE WORD "WORKSPACE" NAMES TWO DIFFERENT THINGS IN THIS CONSOLE, and this line is
 *    about the second one. The topbar switcher selects a CORE tenant workspace — a
 *    filter the views read. The one here is the SESSION workspace a run mounts
 *    (`workspace_ref`, the agentops plane). They are different nouns that share a word,
 *    so this line says which one it means by standing next to the control that will use
 *    it, and by naming the next ACTION rather than the current page.
 */
export function ScopeLine({
  tenant,
  workspace,
  environment,
  className,
}: {
  tenant: string | null
  workspace: string | null
  environment: string | null
  className?: string
}) {
  const { t } = useTranslation('nav')
  const parts: [string, string][] = [
    [t('scope.tenant'), tenant || t('scope.noTenant')],
    [t('scope.workspace'), workspace || t('scope.noWorkspace')],
    [t('scope.environment'), environment || t('scope.noEnvironment')],
  ]
  return (
    <p
      data-testid="shell-scope-line"
      title={t('scope.hint')}
      className={cn(
        'flex min-w-0 flex-wrap items-center gap-x-1.5 text-caption text-muted-foreground',
        className,
      )}
    >
      {parts.map(([label, value], i) => (
        <span key={label} className="flex min-w-0 items-center gap-1">
          {i > 0 ? <span aria-hidden>·</span> : null}
          <span>{label}</span>
          <span className="truncate font-mono text-foreground">{value}</span>
        </span>
      ))}
    </p>
  )
}

export function ShellLauncher() {
  const { t } = useTranslation('nav')
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const navigate = useNavigate()
  const qc = useQueryClient()

  const canWrite = can('sessions:run:write')
  const canReadProfiles = can('sessions:profile:read')
  const canReadWorkspaces = can('sessions:workspace:read')

  const [name, setName] = useState('')
  const [profileRef, setProfileRef] = useState('')
  const [workspaceRef, setWorkspaceRef] = useState<string>(NO_WORKSPACE)

  const profilesQuery = useQuery({
    queryKey: agentOpsKeys.profiles(activeTenant, boundary.epoch, {
      state: 'active',
    }),
    queryFn: ({ signal }) =>
      agentOpsApi.listProfiles({ state: 'active', limit: PAGE }, { signal }),
    enabled: canWrite && canReadProfiles && !!activeTenant,
  })
  const profiles = profilesQuery.data?.items ?? []

  // The choice was learned under ONE authority boundary. When the boundary moves — a
  // renewal rotates the credential under the same session id — it is forgotten with the
  // list it came from. Adjusted during render, the way the launch dialog does it.
  const [seenBoundary, setSeenBoundary] = useState(boundary.key)
  if (seenBoundary !== boundary.key) {
    setSeenBoundary(boundary.key)
    setProfileRef('')
    setWorkspaceRef(NO_WORKSPACE)
  }

  const workspacesQuery = useQuery({
    queryKey: agentOpsKeys.workspaces(activeTenant),
    queryFn: () => agentOpsApi.listWorkspaces({ limit: PAGE }),
    enabled: canWrite && canReadWorkspaces && !!activeTenant,
  })
  const workspaces = (workspacesQuery.data?.items ?? []).filter(
    (w) => w.state === 'active',
  )

  const profile = profiles.find((p) => p.profile_ref === profileRef)
  const workspace = workspaces.find((w) => w.workspace_ref === workspaceRef)

  const start = useMutation({
    mutationFn: (background: boolean) => {
      const body: CreateRunRequest = {
        name: name.trim(),
        transport: 'stream-json',
        permission_mode: 'default',
        effort: '',
        model: '',
        workspace_ref: workspaceRef === NO_WORKSPACE ? '' : workspaceRef,
        isolation: 'native',
        env_allow: [],
        // Only the REFERENCE leaves the browser: the server resolves the homes.
        provider_profile_ref: profileRef,
      }
      return agentOpsApi
        .createRun(body)
        .then((run) => ({ run, background }) as const)
    },
    retry: false,
    onSuccess: ({ run, background }) => {
      void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
      setName('')
      if (background) {
        // "Start in the background": the draft is cleared, the scope is kept and the
        // operator is left where they were, ready for the next one. Launching must not
        // block the next launch (§3.14 adopt).
        toast.success(t('nav:launcher.startedBackground'))
        return
      }
      // A run the plane has not yet proved a managed row for is addressed BY ITS RUN —
      // the third shape `session-address` resolves, and the card resolves the rest from
      // the engine. So the address is valid the instant the launch answers.
      void navigate({
        to: '/sessions' as never,
        search: { [SESSION_PARAM]: `run:${run.run_ref}` } as never,
      } as never)
    },
    onError: (error: unknown) => {
      // The ENGINE's own refusal — budget cap, pending approval, unwired credential —
      // never a sentence composed here.
      toast.error(launchFailureMessage(error, t('nav:launcher.startFailed')))
    },
  })

  if (!canWrite)
    return (
      <ScopeLine
        tenant={activeTenant}
        workspace={null}
        environment={null}
        className="px-3 py-2"
      />
    )

  const ready = !!profileRef && !start.isPending

  /**
   * WHY THERE IS NOTHING TO TYPE INTO, WHEN THERE IS NOTHING TO TYPE INTO.
   *
   * ⛔ A DISABLED FIELD WITH NO REASON IS THE CONSOLE FRAN REFUSED, and there are three
   *    distinct ways to arrive at one — a first version of this component produced all
   *    three by accident, because it only knew about the third:
   *      · no tenant is established, so a launch has nowhere to happen at all;
   *      · this principal may start runs but may not READ provider profiles, so they
   *        cannot choose the one field the server cannot default;
   *      · the plane is readable and holds none.
   *    Only the last one has an action to offer. The other two are sentences.
   */
  const blocked:
    | 'no-tenant'
    | 'no-profile-read'
    | 'asking'
    | 'read-failed'
    | 'no-profiles'
    | null = !activeTenant
    ? 'no-tenant'
    : !canReadProfiles
      ? 'no-profile-read'
      : profilesQuery.isError
        ? 'read-failed'
        : !profilesQuery.isSuccess
          ? 'asking'
          : profiles.length === 0
            ? 'no-profiles'
            : null

  const submit = (background: boolean) => {
    if (!ready) return
    start.mutate(background)
  }

  /**
   * BOTH CHORDS COME FROM THE DECLARED TABLE, under `launcherFocused` — which is true
   * here and nowhere else, so neither can fire while the operator is doing something
   * different. The shell's own handler never sees them: it refuses every key typed into
   * a field except the palette.
   */
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    const command = resolveBinding(KEYBINDINGS, e, { launcherFocused: true })
    if (command !== 'launcher.start' && command !== 'launcher.startBackground')
      return
    e.preventDefault()
    submit(command === 'launcher.startBackground')
  }

  return (
    <div
      data-testid="shell-launcher"
      className="flex flex-col gap-1.5 border-t border-border bg-surface px-3 py-2"
    >
      {/* ⛔ `text-danger`, NOT `text-destructive`, AND THE GATE IS WHY. The a11y sweep
          derives a contrast pairing from every colour class it finds in source, and it
          has no colour rule for `.text-destructive` — a shadcn holdover this palette
          replaced with `danger` (`ErrorState` uses `bg-danger-soft text-danger` for
          exactly this case). An UNMEASURED pairing is a blocking finding, and rightly:
          a colour nobody can measure is a colour nobody has checked. */}
      {/* ⛔ WHILE THE PROFILE PLANE IS STILL ANSWERING, THE FIELD IS NOT RENDERED YET,
          and that is a correction with a measured cause: the first version treated
          "not answered" as "there are some", so a deployment with NO profile painted a
          usable field and then replaced it with the empty-state sentence a moment
          later. The live spec caught it as a `locator.focus` timing out on an element
          that had just been removed — i.e. the console offered a control it was about
          to take away. `asking` renders the scope line and nothing else. */}
      {blocked === 'asking' ? null : blocked === 'no-tenant' ||
        blocked === 'no-profile-read' ||
        blocked === 'read-failed' ? (
        <p
          className={
            blocked === 'read-failed'
              ? 'text-caption text-danger'
              : 'text-caption text-muted-foreground'
          }
          data-testid="launcher-blocked"
        >
          {t(
            blocked === 'no-tenant'
              ? 'launcher.noTenant'
              : blocked === 'read-failed'
                ? 'launcher.readFailed'
                : 'launcher.noProfileRead',
          )}
        </p>
      ) : blocked === 'no-profiles' ? (
        // THE HONEST STATE: not a disabled field, and not an invented reason. B2 makes
        // the profile mandatory, so there is one thing to do and this is it.
        <div className="flex flex-wrap items-center gap-2">
          <p className="min-w-0 flex-1 text-caption text-muted-foreground">
            {t('nav:launcher.noProfiles')}
          </p>
          <Button
            variant="primary"
            size="sm"
            onClick={() =>
              void navigate({ to: '/provider-profiles' as never } as never)
            }
            data-testid="launcher-add-provider"
          >
            <Plus className="size-3.5" />
            {t('nav:launcher.addProvider')}
          </Button>
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <Input
            id={LAUNCHER_INPUT_ID}
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder={t('nav:launcher.placeholder')}
            aria-label={t('nav:launcher.label')}
            data-testid="launcher-input"
            className="h-8 min-w-[12rem] flex-1"
          />

          {/* THE LOW-KEY PILL, beside the field it qualifies — and it is the one field
              the server cannot default for us. */}
          <Select value={profileRef} onValueChange={setProfileRef}>
            <SelectTrigger
              className="h-8 w-auto min-w-[10rem] text-caption"
              aria-label={t('nav:launcher.profile')}
              data-testid="launcher-profile"
            >
              <IdCard className="size-3.5 text-muted-foreground" />
              <SelectValue placeholder={t('nav:launcher.profilePlaceholder')} />
            </SelectTrigger>
            <SelectContent>
              {profiles.map((p) => (
                <SelectItem key={p.profile_ref} value={p.profile_ref}>
                  {p.display_name || p.profile_ref}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select value={workspaceRef} onValueChange={setWorkspaceRef}>
            <SelectTrigger
              className="h-8 w-auto min-w-[10rem] text-caption"
              aria-label={t('nav:launcher.workspace')}
              data-testid="launcher-workspace"
            >
              <Layers className="size-3.5 text-muted-foreground" />
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={NO_WORKSPACE}>
                {t('nav:scope.noWorkspace')}
              </SelectItem>
              {workspaces.map((w) => (
                <SelectItem key={w.workspace_ref} value={w.workspace_ref}>
                  {w.name || w.workspace_ref}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Button
            variant="primary"
            size="sm"
            disabled={!ready}
            onClick={() => submit(false)}
            data-testid="launcher-start"
          >
            <Send className="size-3.5" />
            {t('nav:launcher.start')}
            <Kbd className="ml-1 hidden sm:inline-flex">⏎</Kbd>
          </Button>
        </div>
      )}

      <div className="flex flex-wrap items-center justify-between gap-2">
        <ScopeLine
          tenant={activeTenant}
          workspace={workspace?.name || workspace?.workspace_ref || null}
          // The environment is the PROFILE's, because that is what the launch will run
          // in. With no profile chosen there is none to report, and the line says so
          // rather than borrowing a value from somewhere else on screen.
          environment={profile?.environment_ref || null}
        />
        {blocked === null ? (
          <p className="text-caption text-muted-foreground">
            {t('nav:launcher.backgroundHint')}
          </p>
        ) : null}
      </div>
    </div>
  )
}
