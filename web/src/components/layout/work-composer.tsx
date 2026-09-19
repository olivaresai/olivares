// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE PLACE TO SAY WHAT TO RUN, AND ONE LINE SAYING WHAT IT WILL APPLY TO.
//
// The defect was one sentence: *"I am not able to launch a session easily without
// filling in configuration by hand in an ugly, old console."* A review turned it into
// two rules — an action surface one gesture away, and a status line that always names
// the scope of the next action — and this is both.
//
// ⛔ IT USED TO BE IN THE SHELL, AND THAT WAS THE WRONG UNIT. As
//    `ShellLauncher` it sat below `main` on all 77 authenticated routes, about 90 px
//    tall, of which roughly 60 routes could never use it. The requirement — one gesture
//    to start work — is satisfied for ZERO pixels by the ⌘K palette, which is already
//    always mounted; and on the two screens where starting work IS the work it is
//    satisfied far better by a composer standing IN the work, next to the list the run
//    will join. So this component did not change: its host did. Home and the session
//    work surface mount it; the shell mounts nothing.
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
//    field with no reason is the console this one replaces. The engine makes the profile mandatory
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
import type { TFunction } from 'i18next'
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
import { sessionTurnBody } from '@/features/agentops/session-turn'
import type { CreateRunRequest, RunDTO } from '@/features/agentops/types'
import { workLeaseFenceFor } from '@/features/agentops/work-fence'
import { SESSION_PARAM } from '@/features/sessions/session-address'
import type { WorkGroupId } from '@/features/sessions/session-groups'
import { useAuth } from '@/lib/auth/context'
import { resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { cn } from '@/lib/utils'
import { useIsPhone } from '@/lib/hooks/use-is-phone'
import { useTenantLabel } from './tenant-label'
import { COMPOSER_INPUT_ID } from './work-composer-id'

/** Sentinel for "no workspace", which is a legal launch and not an unset field. */
const NO_WORKSPACE = '__none__'
/** The page the two pickers ask for. Both planes cap far above this. */
const PAGE = 200

/** One of the three facts the scope line states, in the order it states them. */
type ScopePart = { label: string; value: string; identifier: boolean }

/** What to call the active organisation, as `useTenantLabel` answers it. */
type OrgLabel = ReturnType<typeof useTenantLabel>

/** The scope of the next action, as its two renderings both need it. */
type ScopeInput = {
  workspace: string | null
  environment: string | null
  /** Full workspace reference, for `title=` only. */
  workspaceId?: string | null
  /** Full environment reference, for `title=` only. */
  environmentId?: string | null
}

/**
 * THE SCOPE, DERIVED ONCE.
 *
 * ⛔ IT HAS TWO RENDERINGS AND THEY MUST NOT BE TWO DERIVATIONS — the same reason
 *    `useTenantLabel` exists. Inside 64 px the line cannot always be painted, so where
 *    it is not the box carries the whole of it on `title=`. Assembled by hand there,
 *    the tooltip drifts from the line the first time one value is resolved
 *    differently — and a tooltip that contradicts the line it stands in for is worse
 *    than no tooltip at all. The line and the box both read this.
 */
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
    {
      label: t('scope.workspace'),
      value: workspace || t('scope.noWorkspace'),
      identifier: Boolean(workspaceId && workspace === workspaceId),
    },
    {
      label: t('scope.environment'),
      value: environment || t('scope.noEnvironment'),
      identifier: Boolean(environmentId && environment === environmentId),
    },
  ]
  // The full identifiers ride on `title` as well, so a named organization or workspace
  // keeps its reference readable without painting it on the line.
  const ids = [org.tenant, workspaceId, environmentId]
    .filter(Boolean)
    .join(' · ')
  const stated = parts.map((p) => `${p.label}: ${p.value}`).join(' · ')
  return {
    parts,
    title: `${t('scope.hint')} ${stated}${ids ? ` · ${ids}` : ''}`,
  }
}

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
  className,
  ...input
}: ScopeInput & { className?: string }) {
  const { t } = useTranslation('nav')
  // ⛔ THE ORGANISATION IS SHOWN BY NAME, AND IT USED TO BE A RAW UUID. This line took
  //    a `tenant` STRING from its caller, who had only `activeTenant` — an id — while
  //    the topbar three centimetres above printed `Demo Estate`. The census
  //    logged it, on eleven routes. `useTenantLabel` is the ONE rendering both
  //    now read, so they cannot disagree; it returns the provisioned name where this
  //    principal may read it and a short id where the engine does not expose one, which
  //    is the honest answer and not a fabricated label.
  const org = useTenantLabel()
  const { parts, title } = scopeFacts(t, org, input)
  return (
    <p
      data-testid="work-scope-line"
      // ⛔ THE WHOLE LINE IS THE TOOLTIP, NOT JUST THE HINT. The line is `nowrap` and
      //    truncates (below), which is what keeps the composer inside its 64 px budget
      //    in a narrow pane — and truncation without the full text on `title` would be
      //    the console hiding the scope of the next action, which is the one thing this
      //    line exists to say.
      title={title}
      // ⛔ ONE TRUNCATION AT THE END, NOT THREE IN THE MIDDLE. This was a flex row of
      //    three groups, each allowed to shrink — and at 390 px the browser shrank all
      //    three equally: `Organization D… · Workspace No … · Environment Not…`, three
      //    facts and not one of them readable. As inline text in a truncating block the
      //    line gives way at its END, so the first fact is whole, the second usually is,
      //    and what is cut is on `title` with the rest. When a line cannot hold
      //    everything, losing the tail beats losing the middle of every part.
      className={cn(
        'min-w-0 truncate text-caption leading-4 text-muted-foreground',
        className,
      )}
    >
      {parts.map((part, i) => (
        <span key={part.label}>
          {i > 0 ? <span aria-hidden> · </span> : null}
          {part.label}{' '}
          {/* A NAME is prose. An identifier that had to stand in for a missing
              name keeps the monospace face so it cannot be mistaken for one. */}
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

/**
 * How the composer is framed by its host.
 *
 * `panel` — a bordered card, for a host that lays it out among other blocks (home).
 * `docked` — a top hairline and no rounding, for the bottom edge of a pane it belongs
 * to (the session narrative). The reference docks its composer inside the work pane
 * exactly this way, and a rounded card floating at the foot of a pane would read as a
 * separate thing rather than as that pane's own input.
 */
export type ComposerFrame = 'panel' | 'docked'

/**
 * After a launch, the same composer stays in place and becomes the session's
 * input. `group` is the rail's own membership (running · waiting for you ·
 * settled), so the word beside the field cannot disagree with the list.
 */
export type ComposerAttached = {
  run: RunDTO
  group: WorkGroupId
}

/**
 * The status word, keyed by the rail's OWN membership rather than by a second reading
 * of the run's state — so the composer and the list cannot disagree about a session
 * that has just settled.
 */
const ATTACHED_STATUS_KEY: Record<WorkGroupId, string> = {
  active: 'nav:launcher.attachedRunning',
  attention: 'nav:launcher.attachedWaiting',
  settled: 'nav:launcher.attachedSettled',
}

export function WorkComposer({
  frame = 'panel',
  attached = null,
}: { frame?: ComposerFrame; attached?: ComposerAttached | null } = {}) {
  const { t } = useTranslation('nav')
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const phone = useIsPhone()
  const org = useTenantLabel()

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

  const [turn, setTurn] = useState('')
  const [wire, setWire] = useState('')
  const sendTurn = useMutation({
    mutationFn: (payload: { value: string; asWire: boolean }) => {
      const run = attached?.run
      if (!run) throw new Error('no attached run')
      const body = sessionTurnBody(run, payload.value, payload.asWire)
      // ⛔ A WORK-BOUND SESSION HAS ONE CONTROL PLANE, AND THIS IS A CONTROL ON IT.
      //    Sent without the fence stamped on the run, the turn is refused with 409
      //    before the child sees a byte — so the composer that started the session
      //    could not speak to it, while the CLI's `--work-lease-fence` could.
      const fence = workLeaseFenceFor(run)
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

  const runLive =
    attached?.run.state === 'running' || attached?.run.state === 'idle'
  const sendSentence = () => {
    const value = turn.trim()
    if (!attached || !value || sendTurn.isPending || !runLive) return
    sendTurn.mutate({ value, asWire: false })
  }
  const sendWire = () => {
    const value = wire.trim()
    if (!attached || !value || sendTurn.isPending || !runLive) return
    sendTurn.mutate({ value, asWire: true })
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
    if (attached) {
      sendSentence()
      return
    }
    submit(command === 'launcher.startBackground')
  }

  /**
   * THE ATTACHED COMPOSER IS THE SAME 64 px BOX, AND IT IS A CONTROL ON A PLANE.
   *
   * ⛔ TWO ROUNDS MET HERE AND BOTH ARE KEPT, because they contend for different
   *    things. One measured this box at 83 px attached and 102 at 390 and made it a
   *    DECLARED 64 — one 32 px control row and one 20 px caption — since a composer
   *    that grows when the pane narrows takes its height from the narrative it is
   *    docked under. The other made the attached state the session's INPUT: a turn
   *    carried on the run's work-lease fence, refused with 409 without it. So the
   *    arithmetic here is the first round's and the controls are the second's, and what
   *    does not fit on the row — the raw line, the scope — sits in an overlay that
   *    costs the closed box nothing.
   */
  if (attached) {
    const attachedWorkspace = workspaces.find(
      (w) => w.workspace_ref === attached.run.workspace_ref,
    )
    const attachedProfile = profiles.find(
      (p) => p.profile_ref === attached.run.provider_profile_ref,
    )
    const attachedScope = {
      workspace: attachedWorkspace?.name || attached.run.workspace_ref || null,
      workspaceId: attached.run.workspace_ref || null,
      environment: attachedProfile?.local_environment
        ? t('nav:scope.thisNode')
        : attachedProfile?.environment_ref ||
          attached.run.provider_environment_ref ||
          null,
      environmentId:
        attachedProfile?.environment_ref ||
        attached.run.provider_environment_ref ||
        null,
    }
    // ⛔ THE REFUSAL IS THE CONTROLS, NOT THE FACTS — and it used to be both. This
    //    return sat ABOVE the attached branch, so a principal with `sessions:live:read`
    //    and no `sessions:run:write` opened a running session and the dock read
    //    "Workspace: No workspace · Environment: No environment" while the inspector
    //    two panes away named both. A missing WRITE grant is not a missing scope: the
    //    run declares one, this principal is permitted to read the run, and saying
    //    "none" about it is the console stating a fact nobody sent.
    //
    //    What the refusal still withholds is every control — no field, no Advanced, no
    //    Send — which is the grant's actual subject. The names come from the profile and
    //    workspace lists, whose reads ARE gated on the write grant, so a read-only
    //    reader gets the run's own references where a writer gets names: a reference is
    //    a fact, and `ScopeLine` marks it as an identifier rather than passing it off
    //    as a name.
    if (!canWrite) {
      return <ScopeLine {...attachedScope} className="px-3 py-2" />
    }

    const status = t(ATTACHED_STATUS_KEY[attached.group])
    return (
      <div
        data-testid="work-composer"
        data-attached="true"
        data-session-state={attached.group}
        // The scope will not fit on a row it shares with a field, a disclosure and a
        // verb, so the box carries the whole of it — and the overlay paints it. Both
        // come from `scopeFacts`, so neither can contradict the other.
        title={scopeFacts(t, org, attachedScope).title}
        className={cn(
          'relative flex h-16 max-h-16 shrink-0 flex-col justify-center gap-1 bg-surface px-3',
          frame === 'docked'
            ? 'border-t border-border'
            : 'rounded-lg border border-border',
        )}
      >
        <div className="flex h-8 min-w-0 items-center gap-2">
          <Input
            id={COMPOSER_INPUT_ID}
            value={turn}
            onChange={(e) => setTurn(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder={t('nav:launcher.turnPlaceholder')}
            aria-label={t('nav:launcher.turnAria')}
            data-testid="launcher-input"
            disabled={!runLive || sendTurn.isPending}
            className="h-8 min-w-0 flex-1"
          />
          {/* ⛔ THE RAW LINE OPENS OVER THE NARRATIVE, NOT UNDER IT. A `<details>` that
              grows in flow adds its open height to the box, so opening the disclosure
              would push the conversation up by however much it contains — and this
              panel holds two controls and a line of prose. Positioned absolutely it
              costs the closed box nothing and overlays the pane when open.
              ⛔ AND IT IS ANCHORED TO THE BOX, NOT TO THIS SUMMARY, which is a
              correction with a measured cause: hung off the summary with `right-0` the
              panel grew to its content's 467 px and its left edge landed 38 px OUTSIDE
              the pane — and the pane is `overflow-hidden`, so the hint and the scope
              line were cut off mid-word. Spanning the box it cannot leave the pane,
              whatever it contains. */}
          <details className="shrink-0">
            <summary
              data-testid="composer-advanced"
              className="cursor-pointer list-none text-caption text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden"
            >
              {t('nav:launcher.advanced')}
            </summary>
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
                  disabled={!runLive || !wire.trim() || sendTurn.isPending}
                  onClick={sendWire}
                  data-testid="composer-wire-send"
                >
                  {t('nav:launcher.send')}
                </Button>
              </div>
              {/* The scope is PAINTED here, not only tooltipped: what a turn will
                  apply to is a fact about the next action, and the box had no row
                  left for it. One gesture, and it is the same line home shows. */}
              <ScopeLine {...attachedScope} />
            </div>
          </details>
          <Button
            type="button"
            variant="primary"
            size="sm"
            className="shrink-0"
            disabled={!runLive || !turn.trim() || sendTurn.isPending}
            onClick={sendSentence}
            data-testid="composer-send"
          >
            <Send className="size-3.5" />
            {t('nav:launcher.send')}
            <Kbd className="ml-1 hidden sm:inline-flex">⏎</Kbd>
          </Button>
        </div>
        {/* The 20 px caption the box budgets for: the rail's own word for this
            session, truncating with the whole of it on `title`. */}
        <p
          data-testid="composer-session-state"
          className="h-5 truncate text-caption leading-5 text-muted-foreground"
          title={status}
        >
          {status}
        </p>
      </div>
    )
  }

  // Not attached and no write grant: there is no session in hand and no launch to
  // scope, so the line has nothing to report and says so.
  if (!canWrite) {
    return (
      <ScopeLine workspace={null} environment={null} className="px-3 py-2" />
    )
  }

  const draftScope = {
    workspace: workspace?.name || workspace?.workspace_ref || null,
    workspaceId: workspace?.workspace_ref || null,
    // The environment is the PROFILE's, because that is what the launch will run in.
    // With no profile chosen there is none to report, and the line says so rather than
    // borrowing a value from somewhere else on screen. A local environment is this
    // node, not its opaque reference.
    environment: profile?.local_environment
      ? t('nav:scope.thisNode')
      : profile?.environment_ref || null,
    environmentId: profile?.environment_ref || null,
  }

  const pickers = (
    <>
      <Select value={profileRef} onValueChange={setProfileRef}>
        <SelectTrigger
          className="h-8 min-w-0 flex-1 text-caption"
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
          className="h-8 min-w-0 flex-1 text-caption"
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
    </>
  )

  const scope = <ScopeLine className="min-w-0 flex-1" {...draftScope} />

  /**
   * ⛔ AT 390 THE PICKERS DO NOT SHRINK, THEY MOVE. Three controls on one 32 px row
   *    inside 390 px leaves each about 8 rem, and the one field the server cannot
   *    default becomes unreadable — so on a phone the two pickers and the scope go
   *    behind one disclosure and the row keeps the field, the door to them, and the
   *    verb. Nothing is removed: it is one gesture away, and the scope is on the box.
   */
  const advanced = (
    <details className="shrink-0">
      <summary
        data-testid="composer-advanced"
        className="cursor-pointer list-none text-caption text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden"
      >
        {t('nav:launcher.advanced')}
      </summary>
      <div
        className={cn(
          'absolute inset-x-0 z-20 flex flex-col gap-2 rounded-md border border-border bg-elevated p-2 shadow-lg',
          frame === 'docked' ? 'bottom-full mb-1' : 'top-full mt-1',
        )}
      >
        <div className="flex flex-col gap-2">{pickers}</div>
        {scope}
      </div>
    </details>
  )

  return (
    <div
      data-testid="work-composer"
      // ⛔ EVERY NUMBER IN THIS BOX IS THE 64 px BUDGET, MEASURED (bar §2, "compositor
      //    64"). It was 138 px at 1440 on the session work surface and 215 at 390: the
      //    control row wrapped because three controls declared a 10–12 rem MINIMUM
      //    width each, and the scope row wrapped under it into two more lines. A
      //    composer that grows when the pane narrows takes the height from the
      //    narrative it is docked under, which is the one thing on that screen that
      //    cannot be made shorter.
      //
      //    So the height is DECLARED and not accumulated: `h-16 max-h-16` over one
      //    32 px control row and one 20 px caption, 4 px apart, centred in the 64.
      //    A box that states its height cannot be grown by what lands inside it.
      title={phone ? scopeFacts(t, org, draftScope).title : undefined}
      className={cn(
        'relative flex h-16 max-h-16 shrink-0 flex-col justify-center gap-1 bg-surface px-3',
        frame === 'docked'
          ? 'border-t border-border'
          : 'rounded-lg border border-border',
      )}
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
          className={cn(
            // The box states its height, so a sentence that wraps is a sentence CLIPPED
            // by the pane rather than a taller composer — and `readFailed` is 108
            // characters in German. It gives way at its end with the whole of it on
            // `title`, the same rule the scope line follows two rows down.
            'min-w-0 truncate text-caption',
            blocked === 'read-failed' ? 'text-danger' : 'text-muted-foreground',
          )}
          title={t(
            blocked === 'no-tenant'
              ? 'launcher.noTenant'
              : blocked === 'read-failed'
                ? 'launcher.readFailed'
                : 'launcher.noProfileRead',
          )}
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
        // THE HONEST STATE: not a disabled field, and not an invented reason. The engine makes
        // the profile mandatory, so there is one thing to do and this is it.
        <div className="flex min-w-0 items-center gap-2">
          <p
            className="min-w-0 flex-1 truncate text-caption text-muted-foreground"
            title={t('nav:launcher.noProfiles')}
          >
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
        /* ⛔ ONE ROW, AND THE MINIMUM WIDTHS ARE GONE. A `min-w` on a flex item is a
            floor the row cannot go under, so three of them turned "narrow pane" into
            "wrap" — the 138 px measured at 390. With `min-w-0` the controls share
            whatever the pane has and each one truncates inside itself (the select
            triggers already `line-clamp-1` their value), and the row itself is `h-8`,
            so it cannot become two. */
        <div className="flex h-8 min-w-0 items-center gap-2">
          <Input
            id={COMPOSER_INPUT_ID}
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder={t('nav:launcher.placeholder')}
            aria-label={t('nav:launcher.label')}
            data-testid="launcher-input"
            className="h-8 min-w-0 flex-1"
          />
          {phone ? advanced : pickers}
          <Button
            variant="primary"
            size="sm"
            className="shrink-0"
            disabled={!ready}
            // ⛔ A DISABLED VERB WITH NO VISIBLE REASON IS THE CONSOLE THIS ONE REPLACES,
            //    and at 390 the reason is behind a disclosure. On a desktop the empty
            //    profile pill is three centimetres away and says "Choose a profile" in
            //    place; on a phone that pill moved into `Advanced`, so a operator sees
            //    a greyed Start and nothing explaining it. The pill's own sentence is
            //    the answer, so it is the one carried here rather than a second wording
            //    of the same fact.
            title={
              profileRef ? undefined : t('nav:launcher.profilePlaceholder')
            }
            onClick={() => submit(false)}
            data-testid="launcher-start"
          >
            <Send className="size-3.5" />
            {t('nav:launcher.start')}
            <Kbd className="ml-1 hidden sm:inline-flex">⏎</Kbd>
          </Button>
        </div>
      )}

      {/* ⛔ THE HINT IS WHAT GIVES WAY, AND ON A PHONE THE WHOLE ROW DOES. These two
          were the second and third of the composer's four lines in a narrow pane: the
          scope and a sentence about a keyboard chord, each wrapping. The scope is the
          fact — it says what the next action applies to — so it keeps the width and
          truncates with the whole line on `title`; the hint describes a shortcut the
          operator either knows or can find in the keyboard help, so below `lg` it is
          not painted. At 390 there is no room for a second row at all, so the line
          moves into the disclosure on the control row and the whole of it rides on the
          box. Nothing is hidden that is not said somewhere it can be read. */}
      {phone ? null : (
        <div className="flex h-5 min-w-0 items-center gap-2">
          {scope}
          {blocked === null ? (
            <p className="hidden shrink-0 text-caption leading-5 text-muted-foreground lg:block">
              {t('nav:launcher.backgroundHint')}
            </p>
          ) : null}
        </div>
      )}
    </div>
  )
}
