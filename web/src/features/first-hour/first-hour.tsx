// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The first hour in three steps — install an agent tool, sign it in with its own
// login, start a session in a folder — and the one New session form every entry
// point opens. Every error says in one sentence what to do and offers the
// button that does it.
import { BrandMark } from '@/components/layout/brand'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { usePermissionRestriction } from '@/features/agentops/permission-restriction'
import {
  useMutation,
  useQuery,
  useQueryClient,
  type MutationKey,
} from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { AppLink } from '@/components/ui/app-link'
import { ArrowUp, Check, ExternalLink, Loader2 } from 'lucide-react'
import {
  Fragment,
  useEffect,
  useId,
  useRef,
  useState,
  type Dispatch,
  type FormEvent,
  type ReactNode,
  type RefObject,
  type SetStateAction,
} from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { agentToolsKeys } from '@/features/agent-tools/api'
import { SessionSkillsPreview } from '@/features/skills/selection-preview'
import { useProfileList } from '@/features/agent-tools/use-profiles'
import { listaRecortada } from '@/features/_intel/notices'
import { Badge } from '@/components/ui/badge'
import { BoxedList, BoxedListRow } from '@/components/ui/boxed-list'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogClose,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { DisabledReason } from '@/components/ui/disabled-reason'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Meter } from '@/components/ui/meter'
import { PathText } from '@/components/ui/path-text'
import { Spinner } from '@/components/ui/spinner'
import { StateBlock } from '@/components/ui/state-block'
import { StatusDot, type StatusTone } from '@/components/ui/status-chip'
import { StatusGlyph } from '@/components/ui/status-glyph'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { Kbd } from '@/components/ui/kbd'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { PANEL_EXTENSIONS, RUN_ON_EXTENSION } from '@/features/extensions'
import type { ToolReadinessDTO, WorkspaceDTO } from '@/features/agentops/types'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { ModelEffortFields } from '@/features/agentops/model-effort-picker'
import {
  AccountUsage,
  AtLimit,
  UsageMeter,
} from '@/features/agentops/account-usage'
import {
  effortAfterModel,
  useModelChoices,
} from '@/features/agentops/model-choices'
import { ProviderAccent } from '@/features/agentops/provider-accent'
import { LaunchSpeedFields } from '@/features/agentops/launch-speed-fields'
import { LaunchPreflight } from '@/features/agentops/launch-identity'
import type { ProviderKind } from '@/features/providers/types'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import type { RequestOptions } from '@/lib/api/client'
import { chordHint, resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { useStepUpOwner, type StepUpAttempt } from '@/stores/step-up'
import { useTenantStore } from '@/stores/tenant'
import { cn } from '@/lib/utils'
import { BringFoldersStep } from './bring-folders'
import { registeredFoldersQuery } from './folders-query'
import {
  useNewSessionDialog,
  type AdvancedLaunch,
  type SessionDraft,
} from './new-session-store'
import { stoppedAtStartReason } from '@/features/agentops/launch-readiness'
import {
  DEFAULT_PERMISSION,
  ensureFolder,
  launchSession,
  mayChoosePermission,
  needsApproval,
  PERMISSION_CHOICES,
  PERMISSION_NAME,
  type PermissionChoice,
} from '@/features/agentops/session-launch'
import { ToolMark } from '@/features/agentops/tool-mark'
import {
  FIRST_HOUR_TOOLS,
  sessionTools,
  toolName,
  firstHourKeys,
  installLatest,
  isSigningIn,
  lastFolder,
  signInApi,
  SIGNING_IN_POLL_MS,
  type SecretEnvChoice,
  type SessionTool,
  type SignInTool,
  type SignInFlow,
  type SignInMethod,
  type ToolKey,
} from './api'
import { SessionCredentials } from './session-credentials'
import { SessionSecrets } from './session-secrets'
import { useReuseProfile, useSessionProfiles } from './session-profiles'
import { PasskeyOffer } from './passkey-offer'
import {
  KeyInstead,
  useKeyBeingStored,
  useKeyKind,
  useKeyTriggerFocus,
} from './key-instead'
import {
  AccessPill,
  DEFAULT_MODEL,
  DEFAULT_PROFILE,
  EffortPill,
  FOLDER_NEW,
  FOLDER_TYPED,
  FolderPill,
  ModelPill,
  ToolPill,
} from './start-pills'
import {
  WorktreeChoice,
  WorktreePill,
  type StartWorktree,
} from './start-worktree'
import {
  documentLimit,
  documentsMessage,
  saveDocuments,
  StartDocuments,
  type StartDocument,
} from './start-documents'
import './i18n'
import {
  useClientSettings,
  type SessionDefaults,
} from '@/features/settings/preferences'

function errorText(err: unknown): string {
  // A session that stopped as it started: the engine's 409 carries the run, whose
  // reason is what the tool said last.
  const stopped = stoppedAtStartReason(err)
  if (stopped) return stopped
  if (err instanceof ApiError) return err.message
  if (err instanceof Error) return err.message
  return String(err)
}

/** How long the readiness answer is reused without asking (each read asks every tool's
 * own program for its sign-in). */
const READINESS_STALE_MS = 10_000

/** The setup checklist's steps: install, sign in, start. */
const STEP_COUNT = 3

/** The engine's code for a tool whose only key was refused at its last test. */
const KEY_REFUSED = 'key_refused'

/** The wizard's "Add an API key" or "Add a local model": a page link to the Providers
 * form on that provider, which brings the person back to this page once the new key
 * passes its test. The sign-in sheet's own "Use an API key instead" is KeyInstead. */
function keyHref(kind: ProviderKind): string {
  const returnTo = window.location.pathname + window.location.search
  return `/providers?${new URLSearchParams({ add: kind, returnTo })}`
}

/** What the tool itself says on this node: installed, and signed in. Only a
 * system administrator may ask (the engine answers anyone else 403), so the
 * console does not ask for anyone else (WEB on 09b: two 403s per Home load). */
export function useToolStatus(driver: SignInTool, enabled = true) {
  const tenant = useTenantStore((s) => s.activeTenant)
  const { isSuperadmin } = useAuth()
  return useQuery({
    queryKey: firstHourKeys.signIn(tenant, driver),
    queryFn: ({ signal }) => signInApi.status(driver, tenant, signal),
    enabled: isSuperadmin && enabled,
    refetchInterval: (query) =>
      isSigningIn(query.state.data) ? SIGNING_IN_POLL_MS : false,
  })
}

/** What the engine says of every tool a session can run: ready, with what it would run
 * on (its own login, or a key or local model from Providers), or the one sentence that
 * says why not (not installed, nothing to run on, a refused key). One read, and one rule
 * in the engine for the console and the CLI (ARCH.C3); a refusal is data, not an error. */
export function useToolsReadiness(enabled = true) {
  const tenant = useTenantStore((s) => s.activeTenant)
  return useQuery({
    queryKey: firstHourKeys.readiness(tenant),
    queryFn: ({ signal }) => agentOpsApi.toolsReadiness({ signal }),
    enabled,
    retry: false,
    // The screens of one dialog mount at different moments and share one answer; a
    // sign-in, an install and every Providers change ask again at once (invalidation).
    staleTime: READINESS_STALE_MS,
  })
}

/** The engine's answer for one tool; undefined while the read is out or failed. */
export function useToolReadiness(driver: string): ToolReadinessDTO | undefined {
  return useToolsReadiness().data?.tools.find((tool) => tool.driver === driver)
}

/** Which tools can start a session: every tool the engine drives (Claude Code, Codex,
 * Grok Build, OpenCode), each as the engine answers it. Now and the New session dialog both
 * read this, so they cannot disagree with each other or with the start. A tool that is
 * not ready carries the engine's own sentence. */
export function useReadyTools(enabled = true) {
  const answer = useToolsReadiness(enabled)
  const { t } = useTranslation('firstHour')
  const of = (driver: SessionTool) =>
    answer.data?.tools.find((tool) => tool.driver === driver)
  const ready = sessionTools().filter((driver) => of(driver)?.ready === true)
  const refusal = (driver: SessionTool): string | undefined => {
    const tool = of(driver)
    // The key's refusal reads in the person's language, with the key's name.
    if (tool?.code === KEY_REFUSED && tool.provider)
      return t('status.keyRefused', {
        name: tool.provider.display_name || tool.provider.kind,
      })
    if (tool && !tool.ready) return tool.message
    return answer.isError ? errorText(answer.error) : undefined
  }
  // What a tool runs on, from the same answer: the key or local model, with the models a
  // session may pick, and whether the engine says that key needs a model.
  const boundKey = (driver: SessionTool) => {
    const tool = of(driver)
    if (tool?.reason !== 'api_key' || !tool.provider) return undefined
    return {
      record: tool.provider,
      modelRequired: tool.model_required === true,
    }
  }
  // Only the FIRST answer is waited for: a refetch keeps the shown answer, and a failed
  // first read asked again at a mount stays failed, not loading (09 IP journey: waiting
  // on every refetch unmounted the form in a loop of about 1,700 GET resolve).
  return {
    ready,
    refusal,
    boundKey,
    isLoading: answer.isPending && !answer.isFetched,
  }
}

function StatusLine({ ok, children }: { ok: boolean; children: ReactNode }) {
  return (
    <Badge variant={ok ? 'success' : 'neutral'} className="gap-1">
      {ok ? <Check aria-hidden className="size-3" /> : null}
      {children}
    </Badge>
  )
}

/** One tool: install it, sign it in. `part` shows one half (the wizard's steps 1
 * and 2) or both (the New session dialog when no tool is ready yet). */
export function ToolCard({
  driver,
  part = 'all',
  primary = false,
}: {
  /** Grok Build is installed from AI tools' own review, so it shows `part="signIn"`. */
  driver: SignInTool
  part?: 'all' | 'install' | 'signIn'
  /** This card's action is the screen's one next step (WorkStart): the filled button.
   * Cards listed side by side keep it secondary. */
  primary?: boolean
}) {
  const { t } = useTranslation('firstHour')
  const qc = useQueryClient()
  const tenant = useTenantStore((s) => s.activeTenant)
  const status = useToolStatus(driver)
  const install = useMutation({
    mutationFn: () => installLatest(driver as ToolKey),
    onSettled: () =>
      Promise.all([
        qc.invalidateQueries({
          queryKey: firstHourKeys.signIn(tenant, driver),
        }),
        // What the tools can start on changes with the install.
        qc.invalidateQueries({ queryKey: firstHourKeys.readiness(tenant) }),
        // AI tools lists the installs: a finished install shows there at once, not as
        // "installed on this server, not by Olivares" until a reload.
        qc.invalidateQueries({ queryKey: agentToolsKeys.all }),
      ]),
  })
  const answer = useToolReadiness(driver)
  const installed = status.data?.installed ?? false
  const signedIn = status.data?.signed_in ?? false
  const key =
    !signedIn && answer?.reason === 'api_key' ? answer.provider : undefined
  // The provider refused this key at its last test: the tool runs on nothing.
  const keyRefused = answer?.code === KEY_REFUSED
  const ready = signedIn || (!!key && answer?.ready === true)
  const failedJob =
    install.data && install.data.state !== 'succeeded'
      ? install.data
      : undefined
  return (
    <Card data-testid={`tool-${driver}`}>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <ToolMark driver={driver} />
          <span className="text-body font-semibold text-text">
            {t(`tools.${driver}`)}
          </span>
          {status.isLoading ? (
            <Spinner />
          ) : (
            <>
              {part !== 'signIn' ? (
                <StatusLine ok={installed}>
                  {installed ? t('status.installed') : t('status.notInstalled')}
                </StatusLine>
              ) : null}
              {installed &&
              part !== 'install' &&
              !ready &&
              status.data?.reason ? (
                <span className="text-body text-text-2">
                  {status.data.reason}
                </span>
              ) : installed && part !== 'install' ? (
                <StatusLine ok={ready}>
                  {signedIn
                    ? status.data?.account
                      ? t('status.signedInAs', { account: status.data.account })
                      : t('status.signedIn')
                    : key
                      ? t(
                          keyRefused
                            ? 'status.keyRefused'
                            : // A local model server has no key (EU on RC10: "Uses your
                              // API key: EU local Ollama").
                              key.kind === 'ollama'
                              ? 'status.usesLocal'
                              : 'status.usesKey',
                          { name: key.display_name || key.kind },
                        )
                      : t('status.notSignedIn')}
                </StatusLine>
              ) : null}
            </>
          )}
        </div>
        {status.isError ? (
          <p className="text-body text-danger" role="alert">
            {t('errors.status')}{' '}
            <Button variant="link" onClick={() => status.refetch()}>
              {t('actions.retry')}
            </Button>
          </p>
        ) : null}
        {!status.isLoading && !installed && part !== 'signIn' ? (
          <div className="flex flex-col gap-2">
            {/* Secondary on purpose: several tool rows offer Install at once.
                Only the first screen's one card fills its Install. */}
            <Button
              variant={primary ? 'primary' : 'secondary'}
              className="self-start"
              onClick={() => install.mutate()}
              disabled={install.isPending}
            >
              {install.isPending ? (
                <>
                  <Loader2 aria-hidden className="animate-spin" />
                  {t('actions.installing')}
                </>
              ) : (
                t('actions.install')
              )}
            </Button>
            {install.isError || failedJob ? (
              <p className="text-body text-danger" role="alert">
                {t('errors.install', {
                  error: install.error
                    ? errorText(install.error)
                    : (failedJob?.error ?? failedJob?.state),
                })}
              </p>
            ) : null}
          </div>
        ) : null}
        {installed && !ready && part !== 'install' ? (
          <SignIn driver={driver} variant={primary ? 'primary' : 'secondary'} />
        ) : null}
      </CardContent>
    </Card>
  )
}

/** The code the person types on the vendor's page: large, one tap to copy. */
function DeviceCode({ code }: { code: string }) {
  const { t } = useTranslation('firstHour')
  const [copied, setCopied] = useState(false)
  return (
    <div className="flex items-center gap-2">
      <code
        className="rounded-card border border-line bg-frame px-3 py-2 font-mono text-title font-semibold tracking-wider text-text select-all"
        data-testid="device-code"
      >
        {code}
      </code>
      <Button
        type="button"
        variant="secondary"
        size="sm"
        onClick={() => {
          void navigator.clipboard
            ?.writeText(code)
            .then(() => setCopied(true))
            .catch(() => setCopied(false))
        }}
      >
        {copied ? <Check aria-hidden /> : null}
        {t('signIn.copy')}
      </Button>
      <span role="status" className="sr-only">
        {copied ? t('signIn.copied') : ''}
      </span>
    </div>
  )
}

/** The tool's own login, relayed: a link, and the code (Claude), the device code
 * (Codex, Grok Build) or the API key its own login asks for (OpenCode's own providers).
 * The login is stored where the tool keeps it, on the server. A tool with several
 * methods offers them first, in the engine's order, and starts the one chosen. */
export function SignIn({
  driver,
  accountRef,
  tenantId,
  methods: listedMethods,
  onSignedIn,
  mutationScope,
  requestOptions,
  autoStart = false,
  offerKey = true,
  variant = 'primary',
}: {
  driver: SignInTool
  accountRef?: string
  tenantId?: string
  /** The methods the account's own status read lists; the default row reads its own. */
  methods?: SignInMethod[]
  onSignedIn?: () => void
  mutationScope?: MutationKey
  requestOptions?: () => RequestOptions
  /** Start the login when this mounts: the person already chose to sign in (Add profile
   * continues here), so a second button would only repeat the choice. */
  autoStart?: boolean
  /** Offer "Use an API key instead" here; off where the host sheet offers it once. */
  offerKey?: boolean
  /** The one verb of a dialog is the filled primary; a list of cards, one per tool,
   * passes `secondary` so a screen shows a single filled control. */
  variant?: 'primary' | 'secondary'
}) {
  const { t } = useTranslation('firstHour')
  const qc = useQueryClient()
  const activeTenant = useTenantStore((s) => s.activeTenant)
  const tenant = tenantId ?? activeTenant
  const [flow, setFlow] = useState<SignInFlow | null>(null)
  const [code, setCode] = useState('')
  // The sheet became KeyInstead's key form (the link's replacement,).
  const [addingKey, setAddingKey] = useState(false)
  const keyTriggerRef = useKeyTriggerFocus(addingKey)
  const [error, setError] = useState<string | null>(null)
  // The poll could not reach the server (not a login that is gone): said, not awaited.
  const [lostTouch, setLostTouch] = useState(false)
  const authority = useRef<RequestOptions | undefined>(undefined)
  const captureRequest = () => {
    const options = requestOptions?.()
    authority.current = options
    return options
  }
  // acceptance: the login the server still holds survives a reload. It is taken up once, from the
  // status the row already reads; a cancelled one is not brought back. Only the default row
  // does this: the status it reads is the organization's default account, not another one's.
  const ownsDefault = !accountRef && tenant === activeTenant
  const statusRead = useToolStatus(driver, ownsDefault)
  const pending = statusRead.data?.pending
  // A resumed login is only known once the status is read: start nothing before.
  const statusSettled = !ownsDefault || !statusRead.isPending
  const methods =
    listedMethods ?? (ownsDefault ? statusRead.data?.methods : undefined) ?? []
  // The first listed method is the one to offer first (OpenCode's own key,).
  const [chosen, setChosen] = useState<string | undefined>(undefined)
  const method = methods.length > 1 ? (chosen ?? methods[0]?.id) : undefined
  const takesKey = methods.find((m) => m.id === method)?.key === true
  // Once per mounted row: after a resume, a start or a cancel, the status's snapshot of a
  // pending login is never taken up again here.
  const [resumeSpent, setResumeSpent] = useState(false)
  // The key form replaces the sheet: a login it did not ask for must not be taken
  // up, started or completed underneath it.
  if (
    ownsDefault &&
    !addingKey &&
    !resumeSpent &&
    !flow &&
    pending &&
    pending.state !== 'signed_in' &&
    pending.state !== 'failed'
  ) {
    setResumeSpent(true)
    setFlow(pending)
  }
  const start = useMutation({
    mutationKey: mutationScope && [...mutationScope, 'start'],
    gcTime: mutationScope ? 0 : undefined,
    mutationFn: (options?: RequestOptions) =>
      method
        ? signInApi.start(driver, tenant, accountRef, options, method)
        : accountRef
          ? signInApi.start(driver, tenant, accountRef, options)
          : signInApi.start(driver, tenant),
    onSuccess: (f) => {
      setResumeSpent(true)
      setFlow(f)
      setError(null)
    },
    onError: (err) => setError(errorText(err)),
  })
  const submit = useMutation({
    mutationKey: mutationScope && [...mutationScope, 'code'],
    gcTime: mutationScope ? 0 : undefined,
    mutationFn: (options?: RequestOptions) =>
      accountRef
        ? signInApi.code(flow!.id, code.trim(), options)
        : signInApi.code(flow!.id, code.trim()),
    onSuccess: (f) => setFlow(f),
    onError: (err) => setError(errorText(err)),
    // A typed key or code is not kept on the page once it was handed over.
    onSettled: () => setCode(''),
  })
  const offeredKind = useKeyKind(driver, accountRef)
  const keyKind = offerKey ? offeredKind : undefined
  const autoStarted = useRef(false)
  useEffect(() => {
    // A login a reload resumed is the one being completed: never start another over it.
    // A tool with several methods waits for the person's choice.
    if (
      !autoStart ||
      methods.length > 1 ||
      autoStarted.current ||
      flow ||
      !statusSettled ||
      addingKey
    )
      return
    autoStarted.current = true
    start.mutate(captureRequest())
    // Once per mount: a failed start offers the button, never a loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoStart, statusSettled, addingKey])
  const live = flow && flow.state !== 'signed_in' && flow.state !== 'failed'
  useEffect(() => {
    if (!flow || !live) return
    const controller = new AbortController()
    const id = setInterval(async () => {
      const options = authority.current
      try {
        const next = accountRef
          ? await signInApi.get(flow.id, controller.signal, options)
          : await signInApi.get(flow.id)
        if (
          !accountRef ||
          (!controller.signal.aborted && !options?.signal?.aborted)
        ) {
          setFlow(next)
          setLostTouch(false)
        }
      } catch (err) {
        if (
          err instanceof ApiError &&
          err.isNotFound &&
          !controller.signal.aborted &&
          !options?.signal?.aborted
        )
          setFlow((current) =>
            current?.id === flow.id
              ? { ...current, state: 'failed', message: err.message }
              : current,
          )
        else if (!controller.signal.aborted && !options?.signal?.aborted)
          setLostTouch(true)
      }
    }, 1500)
    return () => {
      clearInterval(id)
      controller.abort()
    }
  }, [flow, live, accountRef])
  useEffect(() => {
    // Not under the key form: a login that completes then must not close the sheet
    // (or the dialog) over a key the person is typing.
    if (flow?.state === 'signed_in' && !addingKey) {
      if (!accountRef) {
        void qc.invalidateQueries({
          queryKey: firstHourKeys.signIn(tenant, driver),
        })
        void qc.invalidateQueries({ queryKey: firstHourKeys.readiness(tenant) })
      }
      onSignedIn?.()
    }
  }, [flow?.state, driver, qc, tenant, accountRef, onSignedIn, addingKey])
  // Stops the tool's login on this server and forgets it here only after
  // the server confirms; a failed cancellation leaves the flow ready to retry.
  const cancel = useMutation({
    mutationKey: mutationScope && [...mutationScope, 'cancel'],
    gcTime: mutationScope ? 0 : undefined,
    mutationFn: (options?: RequestOptions) =>
      accountRef
        ? signInApi.cancel(flow!.id, options)
        : signInApi.cancel(flow!.id),
    onSuccess: () => {
      setResumeSpent(true)
      setFlow(null)
      setCode('')
      setError(null)
    },
    onError: (err) => setError(errorText(err)),
  })

  if (addingKey && keyKind) {
    return (
      <KeyInstead
        driver={driver}
        onCancel={() => setAddingKey(false)}
        // A stored key changes what is ready: back to the sign-in, which re-reads it.
        onCreated={() => setAddingKey(false)}
      />
    )
  }

  if (!flow || flow.state === 'failed') {
    return (
      <div className="flex flex-col gap-2">
        {method ? (
          <div
            className="flex flex-col gap-1.5"
            role="radiogroup"
            aria-label={t('signIn.method')}
          >
            {methods.map((m) => (
              <label
                key={m.id}
                className="flex items-center gap-2 text-body text-text"
              >
                <input
                  type="radio"
                  name={`sign-in-method-${driver}`}
                  value={m.id}
                  checked={method === m.id}
                  onChange={() => setChosen(m.id)}
                />
                {t(`signIn.methods.${m.id}`, { defaultValue: m.label })}
              </label>
            ))}
          </div>
        ) : null}
        <div className="flex flex-wrap items-center gap-3">
          <Button
            variant={variant}
            onClick={() => start.mutate(captureRequest())}
            disabled={start.isPending}
          >
            {start.isPending ? (
              <Loader2 aria-hidden className="animate-spin" />
            ) : null}
            {method
              ? t('signIn.continue')
              : driver === 'claude'
                ? t('actions.signInClaude')
                : driver === 'gemini-cli'
                  ? t('actions.signInGoogle')
                  : driver === 'grok'
                    ? t('actions.signInGrok')
                    : driver === 'cursor'
                      ? t('actions.signInCursor')
                      : driver === 'codex' || driver === 'opencode'
                        ? t('actions.signInChatGPT')
                        : t('actions.signIn')}
          </Button>
          {keyKind ? (
            <Button
              type="button"
              variant="link"
              ref={keyTriggerRef}
              onClick={() => setAddingKey(true)}
            >
              {t('actions.useApiKey')}
            </Button>
          ) : null}
        </div>
        {flow?.state === 'failed' ? (
          <p className="text-body text-danger" role="alert">
            {flow.message || t('errors.signIn')}
          </p>
        ) : null}
        {error ? (
          <p className="text-body text-danger" role="alert">
            {error}
          </p>
        ) : null}
      </div>
    )
  }
  // The engine says when the tool waits for a pasted code (state needs_code). Claude Code
  // and Gemini CLI show their field from the start, as they always did.
  const keyAsked =
    flow.state === 'needs_key' || (flow.state === 'checking' && takesKey)
  const pastesCode =
    driver === 'claude' ||
    driver === 'gemini-cli' ||
    keyAsked ||
    flow.state === 'needs_code' ||
    (flow.state === 'checking' && !flow.user_code)
  return (
    <div className="flex flex-col gap-3 rounded-[8px] bg-surface p-3">
      {flow.state === 'starting' && !flow.url ? (
        <span
          role="status"
          className="flex items-center gap-2 text-caption text-text-2"
        >
          <Loader2 aria-hidden className="size-3.5 animate-spin" />
          {t('signIn.starting')}
        </span>
      ) : null}
      {lostTouch ? (
        <p role="status" className="text-caption text-text-2">
          {t('signIn.lostTouch')}
        </p>
      ) : null}
      {flow.url ? (
        <Button asChild variant="secondary" className="self-start">
          <a href={flow.url} target="_blank" rel="noreferrer noopener">
            <ExternalLink aria-hidden />
            {keyAsked ? t('signIn.openKeyPage') : t('signIn.openLink')}
          </a>
        </Button>
      ) : null}
      {driver !== 'claude' && (flow.user_code || flow.state === 'waiting') ? (
        <div className="flex flex-col gap-1">
          {flow.user_code ? (
            <>
              <span className="text-caption text-text-2">
                {t('signIn.enterCode')}
              </span>
              <DeviceCode code={flow.user_code} />
            </>
          ) : null}
          <span
            role="status"
            className="flex items-center gap-2 text-caption text-text-2"
          >
            <Loader2 aria-hidden className="size-3.5 animate-spin" />
            {t('signIn.waiting')}
          </span>
          {flow.user_code ? (
            <span className="text-caption text-text-3">
              {t('signIn.expires')}
            </span>
          ) : null}
          <Button
            type="button"
            variant="ghost"
            className="self-start"
            onClick={() => cancel.mutate(captureRequest())}
            disabled={cancel.isPending}
          >
            {t('actions.cancel')}
          </Button>
        </div>
      ) : null}
      {pastesCode ? (
        <form
          className="flex flex-col gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            if (code.trim()) submit.mutate(captureRequest())
          }}
        >
          <Field
            label={keyAsked ? t('signIn.keyLabel') : t('signIn.codeLabel')}
            description={
              flow.message ||
              (keyAsked
                ? t('signIn.keyHint', { name: toolName(driver) })
                : t('signIn.codeHint'))
            }
          >
            <Input
              autoFocus
              type={keyAsked ? 'password' : undefined}
              value={code}
              onChange={(e) => setCode(e.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
          </Field>
          <div className="flex gap-2">
            <Button
              type="submit"
              variant="primary"
              disabled={
                !code.trim() || submit.isPending || flow.state === 'checking'
              }
            >
              {submit.isPending || flow.state === 'checking' ? (
                <Loader2 aria-hidden className="animate-spin" />
              ) : null}
              {t('signIn.continue')}
            </Button>
            <Button
              type="button"
              variant="ghost"
              onClick={() => cancel.mutate(captureRequest())}
              disabled={cancel.isPending}
            >
              {t('actions.cancel')}
            </Button>
          </div>
        </form>
      ) : null}
      {error ? (
        <p className="text-body text-danger" role="alert">
          {error}
        </p>
      ) : null}
    </div>
  )
}

/** A folder row stays on one line, with its whole label on the tooltip. */
const FOLDER_ITEM =
  '[&>span:last-child]:min-w-0 [&>span:last-child]:overflow-hidden [&>span:last-child]:whitespace-nowrap'

/** A registered folder's chosen name, or its path when it has no name. */
function folderLabel(w: Pick<WorkspaceDTO, 'name' | 'root_path'>): string {
  return w.name || w.root_path
}

function FolderLabel({
  workspace,
}: {
  workspace: Pick<WorkspaceDTO, 'name' | 'root_path'>
}) {
  return workspace.name && workspace.name !== workspace.root_path ? (
    <span className="truncate">{workspace.name}</span>
  ) : (
    <PathText value={workspace.root_path} copy={false} />
  )
}

/** How many recent sessions the "Start a session" step looks at for one that ran, and
 * the New session form for the last folder. */
const STARTED_WINDOW = 20

/** How many folders, newest first, the composer's folder pill lists before Another folder…. */
const RECENT_FOLDERS = 8

/** The one New session form: tool, folder, first message; the rest under More. */
/** The composer, then the choices. The composer is on screen from the first paint and keeps
 * what is typed; the choices wait for the engine's answer on which tools can start, and
 * Enter does nothing until the form that sends it is there. With `composer` the choices are
 * four pills under the text (tool and account, model, effort, folder) and the rest is the
 * advanced launch, behind the folder pill's More options. */
export function StartSessionForm({
  onStarted,
  onAdvanced,
  initialDraft,
  dialog = false,
  composer = false,
  toolChoice,
  toolStepId,
  startVariant = 'primary',
}: {
  initialDraft?: SessionDraft
  onStarted?: () => void
  /** The advanced launch dialog, offered inside More options: one expander (Root, 09b).
   * It opens on the sign-in, model and effort this form shows; `opener` takes the focus
   * back when it closes. */
  onAdvanced?: (launch?: AdvancedLaunch, opener?: HTMLElement | null) => void
  /** In the New session dialog: the answers go on the dialog's own band. */
  dialog?: boolean
  composer?: boolean
  /** The tool the person picked, held by a parent that shows something for it. */
  toolChoice?: ToolChoice
  /** The id of the parent's sentence for the step that gets the picked tool ready: the
   * form does not say it a second time, and its disabled Start points at that sentence. */
  toolStepId?: string
  /** Settings carry no orange: the setup guide's Start is secondary. */
  startVariant?: 'primary' | 'secondary'
}) {
  const { t } = useTranslation('firstHour')
  const formId = useId()
  const [prompt, setPrompt] = useState(initialDraft?.prompt ?? '')
  const field = useRef<HTMLTextAreaElement>(null)
  // New session (the N key, the list's +) puts the keyboard here.
  const composeRequest = useNewSessionDialog((s) => s.composeRequest)
  useEffect(() => {
    if (composer && composeRequest) field.current?.focus()
  }, [composer, composeRequest])
  // The key that starts is the person's choice (Settings › General).
  const sendKey = useClientSettings((s) => s.sendKey)
  const message = (
    <Textarea
      ref={field}
      aria-label={composer ? t('start.prompt') : undefined}
      form={formId}
      value={prompt}
      onChange={(e) => setPrompt(e.target.value)}
      // As in a session's composer, through its bindings: the chosen key starts (Enter,
      // or Ctrl+Enter with Enter breaking the line), Ctrl+Enter always does, and
      // Shift+Enter starts a new line.
      onKeyDown={(e) => {
        if (e.nativeEvent.isComposing) return
        const command = resolveBinding(KEYBINDINGS, e, {
          launcherFocused: true,
        })
        if (
          command === 'launcher.startBackground' ||
          (command === 'launcher.start' && sendKey === 'enter')
        ) {
          e.preventDefault()
          e.currentTarget.form?.requestSubmit()
        }
      }}
      placeholder={t('start.promptPlaceholder')}
      rows={3}
      className={
        composer ? 'resize-none border-0 bg-transparent shadow-none' : undefined
      }
    />
  )
  return (
    <div
      className={cn(
        'flex min-w-0 flex-col gap-4',
        composer &&
          'gap-0 rounded-lg border border-border bg-surface focus-within:border-border-strong',
      )}
    >
      {composer ? message : <Field label={t('start.prompt')}>{message}</Field>}
      <StartSessionChoices
        initialDraft={initialDraft}
        formId={formId}
        prompt={prompt}
        onStarted={onStarted}
        onAdvanced={onAdvanced}
        dialog={dialog}
        composer={composer}
        field={field}
        toolChoice={toolChoice}
        toolStepId={toolStepId}
        startVariant={startVariant}
      />
    </div>
  )
}

/** A tool picked in the composer (null: none yet), and its setter. */
type ToolChoice = [
  SessionTool | null,
  Dispatch<SetStateAction<SessionTool | null>>,
]

function StartSessionChoices({
  initialDraft,
  formId,
  prompt,
  onStarted,
  onAdvanced,
  dialog,
  composer,
  field,
  toolChoice,
  toolStepId,
  startVariant,
}: {
  initialDraft?: SessionDraft
  formId: string
  prompt: string
  onStarted?: () => void
  onAdvanced?: (launch?: AdvancedLaunch, opener?: HTMLElement | null) => void
  dialog: boolean
  composer: boolean
  field: RefObject<HTMLTextAreaElement | null>
  toolChoice?: ToolChoice
  toolStepId?: string
  startVariant: 'primary' | 'secondary'
}) {
  const { t } = useTranslation('firstHour')
  const navigate = useNavigate()
  const { ready, refusal, boundKey, isLoading } = useReadyTools()
  const { can } = useAuth()
  const tenant = useTenantStore((s) => s.activeTenant)
  const boundary = useAuthBoundary()
  const { epoch } = boundary
  const defaults = useClientSettings((s) => s.sessionDefaults)
  const sendKey = useClientSettings((s) => s.sendKey)
  const profileList = useProfileList()
  const ownChoice = useState<SessionTool | null>(
    sessionTools().find((d) => d === initialDraft?.tool) ?? null,
  )
  const [driver, setDriver] = toolChoice ?? ownChoice
  const defaultScope = JSON.stringify([
    defaults.tool,
    defaults.account,
    tenant,
    epoch,
  ])
  const [unusableDefault, setUnusableDefault] = useState<string | null>(null)
  // Every tool can be chosen; one that cannot start yet says why in ONE line, for the
  // chosen tool only (Root, 09b capture review: three "Install X first" lines at once).
  const preferredTool =
    defaults.tool ||
    profileList.data?.accounts.find((a) => a.account_ref === defaults.account)
      ?.driver
  const defaultTool = sessionTools().find(
    (d) =>
      d === preferredTool &&
      (ready.includes(d) ||
        profileList.data?.accounts.some((a) => a.driver === d)),
  )
  const tool =
    driver ??
    (unusableDefault === defaultScope ? undefined : defaultTool) ??
    ready[0] ??
    sessionTools()[0]
  const defaultAccount =
    !defaults.tool || tool === defaults.tool ? defaults.account : ''
  // Start points at the sentence under the tool choice when that tool cannot start.
  const notReadyId = useId()
  const keyModelId = useId()
  const accessId = useId()
  const typedFolder = useRef<HTMLInputElement>(null)
  const focusTyped = useRef(false)
  // The product fills the folder in, shows it, and asks for a path only after Change
  // folder: the last session's folder while it is still registered, else '' (a new folder
  // of the session's own, made by the engine). The runs are the same read as the setup
  // steps' "Start a session". Both are read afresh each time the form opens: a cached list
  // could still hold a folder deregistered a moment ago.
  const runs = useQuery({
    queryKey: agentOpsKeys.runsScoped(boundary.tenant, epoch, {
      limit: STARTED_WINDOW,
    }),
    queryFn: ({ signal }) =>
      agentOpsApi.listRuns(
        { limit: STARTED_WINDOW },
        { tenant: boundary.tenant, signal },
      ),
    enabled: !!boundary.tenant,
    staleTime: 0,
  })
  const registered = useQuery(registeredFoldersQuery(boundary.tenant, epoch))
  // A sole named account still supplies its reference. Readiness belongs to the
  // selected profile; the default login cannot qualify another home.
  const profiles = useSessionProfiles(
    tool,
    runs.data?.items ?? [],
    !!tool && ready.includes(tool),
    defaultAccount,
  )
  const profileRef = profiles?.selected ?? ''
  const selectedProfile = profiles?.choices.find((c) => c.value === profileRef)
  if (
    !driver &&
    tool === defaultTool &&
    profiles &&
    !profiles.loading &&
    !profiles.choices.some((c) => c.ready) &&
    ready.some((d) => d !== tool) &&
    unusableDefault !== defaultScope
  )
    setUnusableDefault(defaultScope)
  const toolReady =
    !profileList.isLoading &&
    !(defaultAccount && profiles?.loading) &&
    !!tool &&
    (profiles ? selectedProfile?.ready === true : ready.includes(tool))
  // Taken once the reads settle and kept, so a refetch never moves the folder under the
  // user. It belongs to the tenant and sign-in it was read for, like the model pick.
  const folderScope = `${boundary.tenant}:${epoch}`
  const [chosen, setChosen] = useState<{
    scope: string
    folder: string
    workspace_ref?: string
    /** A path typed instead of picked: set by "Another folder…". Fixed then, so a list
     * that arrives later never swaps the field under what the user is typing. */
    typing?: boolean
    selected?: boolean
  } | null>(null)
  const pickable = (registered.data?.items ?? []).filter(
    (w) => w.state === 'active' && w.root_path,
  )
  const linkedFolder = pickable.find(
    (w) => w.workspace_ref === initialDraft?.workspace,
  )
  const preferredFolder = pickable.find(
    (w) => w.workspace_ref === defaults.folder,
  )
  const recentFolder = lastFolder(
    runs.data?.items ?? [],
    registered.data?.items ?? [],
  )
  const defaultFolder =
    preferredFolder ??
    pickable.find((w) => w.workspace_ref === recentFolder.workspace_ref) ??
    pickable[0]
  if (
    chosen?.scope !== folderScope &&
    !runs.isFetching &&
    !registered.isFetching
  )
    setChosen({
      scope: folderScope,
      ...(initialDraft?.workspace
        ? {
            folder: linkedFolder?.root_path ?? initialDraft.workspace,
            workspace_ref: linkedFolder?.workspace_ref,
            typing: !linkedFolder,
          }
        : defaultFolder
          ? {
              folder: defaultFolder?.root_path ?? '',
              workspace_ref: defaultFolder?.workspace_ref,
            }
          : { folder: '' }),
    })
  const current = chosen?.scope === folderScope ? chosen : null
  const folder = current?.folder ?? ''
  const needsFolder =
    !!current && !current.workspace_ref && !folder.trim() && !current.selected
  // The folders sessions already use, to pick from; a path is typed only as the fallback.
  const [runOn, setRunOn] = useState('')
  // The folder's current checkout, or a worktree of the session's own (needs a registered folder).
  const [worktree, setWorktree] = useState<StartWorktree>({
    on: false,
    from: '',
  })
  // Documents attached to the work, saved in its folder at Start.
  const [documents, setDocuments] = useState<StartDocument[]>([])
  const [savingDocument, setSavingDocument] = useState('')
  // They are saved before the session starts, so only into a folder that exists and
  // takes writes: a new folder of its own has none yet, a worktree of its own does not
  // see the folder's files, and a read-only folder refuses every write. Elsewhere the
  // control is not offered, and documents attached before wait.
  const place = pickable.find((w) =>
    current?.workspace_ref
      ? w.workspace_ref === current.workspace_ref
      : !!current?.typing && w.root_path === folder.trim(),
  )
  const documentFolder = current?.workspace_ref
    ? !worktree.on
    : !!current?.typing && !!folder.trim()
  const documentsRefusal = !documentFolder
    ? t('start.documentsNeedFolder')
    : place?.mount_mode === 'ro'
      ? t('start.documentsReadOnly')
      : ''
  // Start holds while documents wait, and asks for the person's own words with them:
  // the session is named after its first message, never after the documents line.
  const documentsHold =
    documents.length === 0
      ? ''
      : documentsRefusal
        ? t('start.documentsWaiting')
        : !prompt.trim()
          ? t('start.documentsNeedWords')
          : ''
  // A new worktree of the session's own, as Start sends it and More options opens on it:
  // only with a registered folder, from the branch or commit the person named.
  const newWorktree =
    worktree.on && current?.workspace_ref
      ? {
          worktree: true,
          ...(worktree.from ? { worktreeFrom: worktree.from } : {}),
        }
      : {}
  const [more, setMore] = useState(false)
  // The person's own pick; until there is one, the permission the folder stores, else the
  // usual default. A stored Full is offered only to someone who may run it: the engine
  // refuses anyone else, so for them it is as if the folder had none.
  const restriction = usePermissionRestriction()
  const [picked, setPicked] = useState<PermissionChoice | null>(null)
  const storedPermission = (registered.data?.items ?? []).find(
    (w) =>
      !!current?.workspace_ref && w.workspace_ref === current.workspace_ref,
  )?.default_permission
  const folderPermission = PERMISSION_CHOICES.find(
    (c) =>
      !!storedPermission &&
      PERMISSION_NAME[c] === storedPermission &&
      mayChoosePermission(c, can('sessions:run:admin')),
  )
  const preferred = folderPermission ?? DEFAULT_PERMISSION
  const permission =
    picked ??
    (restriction.allows(PERMISSION_NAME[preferred])
      ? preferred
      : (PERMISSION_CHOICES.find((p) =>
          restriction.allows(PERMISSION_NAME[p]),
        ) ?? preferred))
  const permissionBlocked = !restriction.allows(PERMISSION_NAME[permission])
  const setPermission = setPicked
  // What More options opens on: what Start would send from here (the words, the folder,
  // what the agent may do and the working copy), so nothing the person chose is lost.
  const handoff: AdvancedLaunch = {
    message: prompt,
    ...(current?.workspace_ref ? { workspaceRef: current.workspace_ref } : {}),
    permission,
    ...newWorktree,
  }
  const [secretEnv, setSecretEnv] = useState<SecretEnvChoice[]>([])
  const [credentials, setCredentials] = useState<string[]>([])
  // The model, for a tool that runs on a key: its saved default unless the user picks one.
  // A pick belongs to the tool, key and sign-in it was made for; another one forgets it.
  const key = tool && toolReady && !profileRef ? boundKey(tool) : undefined
  const saved = key?.record.default_model || ''
  const tested = key?.record.models ?? []
  const noDefault = key?.modelRequired === true && !saved
  const scope = `${tool}:${key?.record.provider_ref ?? ''}:${profileRef}:${tenant}:${epoch}`
  const [pick, setPick] = useState<{
    scope: string
    model: string | null
    effort: string
  }>({ scope, model: null, effort: '' })
  if (pick.scope !== scope) setPick({ scope, model: null, effort: '' })
  const defaultModel =
    !defaults.tool || defaults.tool === tool ? defaults.model : ''
  const requestedModel =
    pick.scope === scope ? (pick.model ?? defaultModel) : defaultModel
  // The catalog of the profile the session runs under: the chosen one, else the profile
  // the engine's own rule would reuse (read only, nothing is created). A key has no
  // profile before its first launch: prefer its tested list, then release choices.
  const canReadProfiles = can('sessions:profile:read')
  const reuse = useReuseProfile(
    tool,
    toolReady && !profileRef && !key && canReadProfiles,
  )
  const catalogProfile =
    profileRef || (!key && reuse.data?.profile?.profile_ref) || ''
  const modelChoices = useModelChoices({
    profileRef: catalogProfile,
    driver: tool ?? '',
    model: requestedModel,
    enabled: toolReady,
    fallbackModel: saved,
    beforeFirstProfile: key
      ? tested.length === 0
      : reuse.data?.reason === 'own_login',
  })
  const keyModels = tested.length
    ? tested
    : [
        ...new Set([
          ...(modelChoices.catalog?.models.map((m) => m.id) ?? []),
          ...(saved ? [saved] : []),
        ]),
      ]
  const offeredModels = key
    ? keyModels
    : modelChoices.catalog?.models.map((m) => m.id)
  const usingDefaultModel = pick.scope !== scope || pick.model === null
  // The remembered effort holds while the remembered model does, and only as a level that
  // model takes.
  const defaultEffort =
    !defaults.tool || defaults.tool === tool ? defaults.effort : ''
  const effort = usingDefaultModel
    ? modelChoices.efforts?.includes(defaultEffort)
      ? defaultEffort
      : ''
    : pick.effort
  const model = usingDefaultModel
    ? defaultModel
      ? offeredModels?.includes(defaultModel)
        ? defaultModel
        : (offeredModels?.[0] ?? '')
      : ''
    : requestedModel
  const defaultsLoading =
    (!!defaultAccount && !!profiles?.loading) ||
    (usingDefaultModel &&
      !!defaultModel &&
      !key &&
      (reuse.isFetching || modelChoices.status === 'loading')) ||
    (usingDefaultModel &&
      !!defaultEffort &&
      !key &&
      (reuse.isFetching || modelChoices.status === 'loading'))
  const unavailableDefault =
    (!driver &&
      !!preferredTool &&
      (!defaultTool || unusableDefault === defaultScope) &&
      !isLoading &&
      !profileList.isLoading) ||
    (!!defaultAccount &&
      !profiles?.loading &&
      !profileList.isLoading &&
      !profiles?.choices.some((c) => c.value === defaultAccount && c.ready)) ||
    (!!defaults.folder && !!current && !preferredFolder) ||
    (usingDefaultModel &&
      !!defaultModel &&
      !defaultsLoading &&
      !offeredModels?.includes(defaultModel))
  const account = profileList.data?.accounts.find(
    (a) => a.account_ref === catalogProfile,
  )
  const speedScope = JSON.stringify([
    scope,
    catalogProfile,
    model,
    saved,
    account?.updated_at,
    account?.auth_source,
    account?.provider_record_ref,
    reuse.data?.profile?.updated_at,
    reuse.data?.profile?.provider_record_ref,
  ])
  const [speedChoice, setSpeedChoice] = useState<{
    scope: string
    fastMode?: boolean
    serviceTier?: string
  }>()
  const speed = speedChoice?.scope === speedScope ? speedChoice : undefined
  const speedModel =
    key ||
    account?.provider_record_ref ||
    reuse.data?.profile?.provider_record_ref
      ? undefined
      : (modelChoices.catalog?.models.find((m) => m.id === model) ??
        modelChoices.start)
  const speedUnavailable =
    (speed?.fastMode === true && speedModel?.supports_fast_mode !== true) ||
    (!!speed?.serviceTier &&
      !speedModel?.service_tiers?.includes(speed.serviceTier))
  // An unknown driver still needs a connection test when it has no model choices.
  const untested = noDefault && keyModels.length === 0
  const canReadProviders = can('sessions:provider:read')
  // A new typed path is registered by the engine (POST workspaces asks
  // sessions:workspace:admin); without it a typed path reaches only a folder
  // already registered, and the hint says so. The AI tools place opens on
  // sessions:account:read; without it the note says who readies a tool.
  const mayRegisterFolders = can('sessions:workspace:admin')
  const canOpenTools = can('sessions:account:read')
  // Attached documents are written into the folder (PUT files asks this).
  const mayAttach = can('sessions:workspace:write')
  // More options cannot take attached documents, nor a folder path typed in (the launch
  // lists registered folders only): it is held, and says how to keep them.
  const moreHeld =
    documents.length > 0
      ? t('start.moreHeldDocuments')
      : current?.typing && folder.trim()
        ? t('start.moreHeldFolder')
        : ''
  const openProviders = () => {
    onStarted?.()
    navigate({ to: '/providers' as '/' })
  }
  // A ready tool on a key that needs a model waits for one; both layouts say why.
  const needsKeyModel = toolReady && noDefault && !model
  const keyModelReason = untested
    ? t('start.noTestedModel')
    : t('start.chooseModel')
  // The launch authority: the sign-in, organization and credential the Start was pressed
  // under, checked at every dispatch, as the New session dialog's.
  const captureOwner = useStepUpOwner()
  const start = useMutation({
    mutationFn: async ({
      tenant,
      attempt,
    }: {
      tenant: string | null
      attempt: StepUpAttempt
      access: {
        sourceType: 'profile' | 'tool'
        sourceRef: string
        workspaceRef: string
      }
      /** What this start used, kept as the next start's choice (Settings › New sessions). */
      chosen: SessionDefaults
      /** The folder was a typed path: the engine's folder for it is the one kept. */
      typed: boolean
    }) => {
      const authority = {
        tenant,
        signal: attempt.signal,
        dispatchGuard: attempt.dispatchGuard,
      }
      // Attached documents are saved in the folder first: a typed path is registered for
      // that, as the launch would, and the session then starts in that folder.
      const typedFolder =
        documents.length > 0 && !current?.workspace_ref
          ? await ensureFolder(folder.trim(), authority)
          : undefined
      const workspaceRef = typedFolder?.workspace_ref ?? current?.workspace_ref
      const known =
        typedFolder ?? pickable.find((w) => w.workspace_ref === workspaceRef)
      // A typed path can name a folder registered read-only: say so before any write.
      if (documents.length > 0 && known?.mount_mode === 'ro')
        throw new Error(t('start.documentsReadOnly'))
      const paths =
        documents.length > 0 && workspaceRef
          ? await saveDocuments({
              documents,
              wref: workspaceRef,
              maxBytes: documentLimit(known),
              authority,
              onSaving: setSavingDocument,
              onSaved: (file, path) =>
                setDocuments((all) =>
                  all.map((d) =>
                    d.file === file
                      ? { ...d, saved: { ...d.saved, [workspaceRef]: path } }
                      : d,
                  ),
                ),
            })
          : []
      return launchSession(
        {
          quick: {
            driver: tool!,
            folder,
            workspace_ref: workspaceRef,
            permission,
            secretEnv,
            ...(credentials.length ? { credentials } : {}),
            ...(runOn ? { runOn } : {}),
            ...newWorktree,
            ...(model ? { model } : {}),
            ...(effort ? { effort } : {}),
            ...(speed?.fastMode !== undefined
              ? { fastMode: speed.fastMode }
              : {}),
            ...(speed?.serviceTier ? { serviceTier: speed.serviceTier } : {}),
            ...(profileRef ? { profileRef } : {}),
          },
          message: documentsMessage(prompt, paths),
        },
        authority,
      )
    },
    onSuccess: (run, { attempt, chosen, typed }) => {
      if (!attempt.current()) return
      useClientSettings
        .getState()
        .setSessionDefaults(
          typed && run.workspace_ref
            ? { ...chosen, folder: run.workspace_ref }
            : chosen,
        )
      onStarted?.()
      // The conversation in front, not the list: on a phone only one pane fits, and the
      // rail is the default one (SC 59 item 2: the new session needed an extra tap).
      navigate({
        to: '/sessions' as '/',
        search: { session: `run:${run.run_ref}`, pane: 'narrative' } as never,
      })
    },
  })
  const AccessRequestAction = PANEL_EXTENSIONS.accessRequestAction
  const failureReason = start.isError ? errorText(start.error) : ''
  const accessSourceType =
    start.variables?.access.sourceType ?? (profileRef ? 'profile' : 'tool')
  const accessSourceRef =
    start.variables?.access.sourceRef ?? (profileRef || tool)
  const accessWorkspaceRef =
    start.variables?.access.workspaceRef ?? current?.workspace_ref ?? ''
  if (isLoading) return <StateBlock state="loading" rows={composer ? 1 : 3} />
  if (!tool) {
    return <p className="text-body text-text-2">{t('start.needTool')}</p>
  }
  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    // A second Start would retire the launch in flight and leave its run unopened.
    if (
      start.isPending ||
      !current ||
      needsFolder ||
      defaultsLoading ||
      !toolReady ||
      speedUnavailable ||
      permissionBlocked ||
      !!documentsHold ||
      (noDefault && !model)
    )
      return
    const owner = captureOwner()
    start.mutate({
      tenant: owner.tenant,
      attempt: owner.begin(),
      access: {
        sourceType: profileRef ? 'profile' : 'tool',
        sourceRef: profileRef || tool,
        workspaceRef: current?.workspace_ref ?? '',
      },
      chosen: {
        tool,
        account: profileRef,
        model,
        effort,
        folder: current.workspace_ref ?? '',
      },
      // A typed path is registered by the engine and kept for the next start.
      typed: !!current.typing && !!folder.trim(),
    })
  }
  const pickFolder = (v: string) => {
    if (!current) return
    if (v === FOLDER_TYPED) {
      focusTyped.current = true
      return setChosen({
        ...current,
        typing: true,
        selected: false,
        workspace_ref: undefined,
      })
    }
    if (v === FOLDER_NEW)
      return setChosen({
        ...current,
        folder: '',
        workspace_ref: undefined,
        typing: false,
        selected: true,
      })
    const picked = pickable.find((w) => w.workspace_ref === v)
    if (picked)
      setChosen({
        ...current,
        folder: picked.root_path,
        workspace_ref: picked.workspace_ref,
        typing: false,
      })
  }
  const fallbackNote = unavailableDefault ? (
    <p role="status" className="text-caption text-text-2">
      {t('settings:newSessions.fallback')}
    </p>
  ) : null
  const notReadyNote =
    tool && !toolReady && !toolStepId ? (
      <p
        id={notReadyId}
        className="text-caption text-text-2"
        role="status"
        data-slot="tool-not-ready"
      >
        {profileRef && selectedProfile
          ? `${selectedProfile.label} · ${t(selectedProfile.missing === 'key' ? 'start.profileNeedsKey' : 'start.profileNotSignedIn')}`
          : (refusal(tool) ??
            t('start.notReady', { tool: toolName(tool) }))}{' '}
        {canOpenTools ? (
          <Button
            type="button"
            variant="link"
            onClick={() => {
              onStarted?.()
              navigate({ to: '/agent-tools' as '/' })
            }}
          >
            {t('start.openTools')}
          </Button>
        ) : (
          t('start.askAdmin')
        )}
      </p>
    ) : null
  const typedFolderField = current?.typing ? (
    <>
      <Input
        ref={typedFolder}
        aria-label={t('start.folder')}
        value={folder}
        onChange={(e) =>
          setChosen({
            ...current,
            folder: e.target.value,
            workspace_ref: undefined,
          })
        }
        placeholder="/home/you/project"
        spellCheck={false}
      />
      <p className="-mt-2 text-caption text-muted-foreground">
        {
          // An unreadable list is not an empty one; a principal who may not list
          // folders is not told of a list they were never to see.
          registered.isError && can('sessions:workspace:read')
            ? t('start.foldersUnreadable')
            : mayRegisterFolders
              ? t('start.folderHint')
              : t('start.folderHintGiven')
        }
      </p>
    </>
  ) : null
  const restrictionNotes = (
    <>
      {restriction.reason && !restriction.error ? (
        <p role="status" className="text-caption text-text-3">
          {restriction.reason}
        </p>
      ) : null}
      {restriction.error ? (
        <QueryErrorState error={restriction.error} retry={restriction.retry} />
      ) : restriction.retry ? (
        <Button type="button" variant="link" onClick={restriction.retry}>
          {t('common:actions.retry')}
        </Button>
      ) : null}
    </>
  )
  const startNotes = (
    <>
      {start.isError ? (
        needsApproval(start.error) ? (
          <p className="text-body text-danger" role="alert">
            {t('errors.needsApproval')}{' '}
            <Button
              variant="link"
              onClick={() => {
                setPermission('editsOnly')
                start.reset()
              }}
            >
              {t('permission.editsOnly')}
            </Button>
          </p>
        ) : (
          <p className="text-body text-danger" role="alert">
            {t('errors.start', { error: errorText(start.error) })}
            {start.error instanceof ApiError &&
            start.error.code === 'provider_default_model_unavailable' &&
            canReadProviders ? (
              <>
                {' '}
                <Button type="button" variant="link" onClick={openProviders}>
                  {t('start.openProviders')}
                </Button>
              </>
            ) : null}
          </p>
        )
      ) : null}
      {AccessRequestAction &&
      failureReason.includes('request access') &&
      (profileRef || tool) ? (
        <AccessRequestAction
          key={`${accessSourceType}:${accessSourceRef}:${accessWorkspaceRef}`}
          sourceType={accessSourceType}
          sourceRef={accessSourceRef}
          workspaceRef={accessWorkspaceRef}
        />
      ) : null}
      {picked === null && folderPermission ? (
        // The start is one click, so a folder's stored permission is said before it is used.
        <p
          className={`text-caption ${folderPermission === 'full' ? 'text-warning' : 'text-text-3'}`}
        >
          {t('start.folderPermission', {
            name: t(`permission.${folderPermission}`),
          })}
        </p>
      ) : null}
      {permission === 'auto' && tool && tool !== 'claude' ? (
        // Only Claude Code has an automatic mode; the engine makes any other tool ask,
        // and the person who chose Auto is told so before Start.
        <p className="text-caption text-text-3">
          {t('start.autoAsks', { tool: toolName(tool) })}
        </p>
      ) : null}
    </>
  )
  // The list hands focus back to its button when it closes; a typed path was just asked
  // for, so the field takes it.
  const focusTypedOnClose = (e: Event) => {
    if (!focusTyped.current) return
    focusTyped.current = false
    e.preventDefault()
    typedFolder.current?.focus()
  }
  if (composer) {
    const shownFolder = pickable.find(
      (w) => w.workspace_ref === current?.workspace_ref,
    )
    const folderText = current?.typing
      ? folder || t('start.folderTyped')
      : current?.workspace_ref
        ? shownFolder
          ? folderLabel(shownFolder)
          : folder
        : current?.selected
          ? t('start.folderNew')
          : t('start.folderChoose')
    const recent = pickable.slice(0, RECENT_FOLDERS)
    // A folder picked or remembered beyond the recent ones stays listed: Enter sends it.
    const folders = [
      ...(current?.workspace_ref &&
      !recent.some((w) => w.workspace_ref === current.workspace_ref)
        ? [{ value: current.workspace_ref, label: folderText }]
        : []),
      ...recent.map((w) => ({ value: w.workspace_ref, label: folderLabel(w) })),
    ]
    return (
      <form
        id={formId}
        className="flex min-w-0 flex-col gap-2 px-3 pb-3"
        onSubmit={submit}
      >
        {typedFolderField ? (
          <div className="flex flex-col gap-3">{typedFolderField}</div>
        ) : null}
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <ToolPill tool={tool} onTool={setDriver} profiles={profiles} />
          {toolReady ? (
            <>
              <ModelPill
                tool={tool}
                choices={modelChoices}
                model={model}
                onModel={(next) =>
                  setPick({
                    scope,
                    model: next,
                    // As the form's model fields: an effort the next model does not take,
                    // or a key's default that names no model, is forgotten.
                    effort: key
                      ? next === '' && !saved
                        ? ''
                        : effort
                      : effortAfterModel(
                          tool,
                          modelChoices.catalog,
                          next,
                          saved,
                          effort,
                        ),
                  })
                }
                {...(key
                  ? { tested: keyModels, keyDefault: saved, noDefault }
                  : {})}
              />
              <EffortPill
                efforts={modelChoices.efforts}
                value={effort}
                defaultEffort={modelChoices.start?.default_effort}
                onChange={(next) => setPick({ scope, model, effort: next })}
              />
            </>
          ) : null}
          {current ? (
            <FolderPill
              autoFocus={needsFolder && !prompt}
              label={folderText}
              value={
                current.typing
                  ? FOLDER_TYPED
                  : (current.workspace_ref ??
                    (current.selected ? FOLDER_NEW : ''))
              }
              folders={folders}
              onPick={pickFolder}
              onCloseAutoFocus={focusTypedOnClose}
              moreHeld={moreHeld}
              onMore={
                onAdvanced
                  ? () =>
                      onAdvanced(
                        {
                          ...handoff,
                          driver: tool,
                          ...(catalogProfile
                            ? { profileRef: catalogProfile, model, effort }
                            : {}),
                        },
                        field.current,
                      )
                  : undefined
              }
            />
          ) : (
            <Spinner />
          )}
          {current?.workspace_ref ? (
            <WorktreePill value={worktree} onChange={setWorktree} />
          ) : null}
          <AccessPill
            value={permission}
            choices={PERMISSION_CHOICES.filter((p) =>
              mayChoosePermission(p, can('sessions:run:admin')),
            )}
            allows={(p) => restriction.allows(PERMISSION_NAME[p])}
            reason={restriction.reason}
            onChange={setPermission}
          />
          <DisabledReason
            className="ms-auto max-w-full"
            mode="native"
            disabled={
              !current ||
              needsFolder ||
              defaultsLoading ||
              permissionBlocked ||
              start.isPending ||
              speedUnavailable
            }
            reason={
              start.isPending
                ? t('start.starting')
                : permissionBlocked
                  ? restriction.reason || t('common:commandPalette.deniedHelp')
                  : speedUnavailable
                    ? t('agentops:create.speedUnavailable')
                    : defaultsLoading
                      ? t('common:states.loading')
                      : needsFolder
                        ? t('start.folderChoose')
                        : t('start.readingFolder')
            }
          >
            <Button
              type="submit"
              variant="primary"
              size="icon"
              className="ms-auto pointer-coarse:size-11"
              aria-label={
                start.isPending ? t('start.starting') : t('start.start')
              }
              aria-describedby={
                tool && !toolReady
                  ? (toolStepId ?? notReadyId)
                  : needsKeyModel
                    ? keyModelId
                    : undefined
              }
              disabled={
                !current ||
                needsFolder ||
                defaultsLoading ||
                permissionBlocked ||
                start.isPending ||
                !toolReady ||
                speedUnavailable ||
                (noDefault && !model)
              }
            >
              {start.isPending ? (
                <Loader2 aria-hidden className="animate-spin" />
              ) : (
                <ArrowUp aria-hidden />
              )}
            </Button>
          </DisabledReason>
        </div>
        {fallbackNote}
        {notReadyNote}
        {needsKeyModel ? (
          <p id={keyModelId} role="status" className="text-caption text-text-2">
            {keyModelReason}
            {untested && canReadProviders ? (
              <>
                {' '}
                <Button type="button" variant="link" onClick={openProviders}>
                  {t('start.openProviders')}
                </Button>
              </>
            ) : null}
          </p>
        ) : null}
        {restrictionNotes}
        <LaunchPreflight />
        {startNotes}
      </form>
    )
  }
  // In the New session dialog the answers sit on its own band (the key that starts,
  // Cancel and Start); elsewhere Start goes across the form.
  const Answers = dialog ? DialogFooter : 'div'
  const startKey = sendKey === 'modEnter' ? chordHint('Mod+Enter') : 'Enter'
  return (
    <form id={formId} className="flex min-w-0 flex-col gap-4" onSubmit={submit}>
      {/* Every tool the engine drives; the chosen one, when it cannot start yet,
          says why in the engine's own sentence, with the way to AI tools. */}
      {fallbackNote}
      <Field label={t('start.tool')}>
        <Select value={tool} onValueChange={(v) => setDriver(v as SessionTool)}>
          <SelectTrigger aria-label={t('start.tool')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {sessionTools().map((d) => (
              <SelectItem key={d} value={d}>
                <span className="inline-flex items-center gap-1.5">
                  <ToolMark driver={d} />
                  {toolName(d)}
                </span>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      {profiles && profiles.choices.length > 1 ? (
        <Field label={t('start.profile')}>
          <Select
            value={profiles.selected || DEFAULT_PROFILE}
            onValueChange={(v) =>
              profiles.select(v === DEFAULT_PROFILE ? '' : v)
            }
          >
            <SelectTrigger aria-label={t('start.profile')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent className="max-w-[var(--radix-select-content-available-width)]">
              {profiles.choices.map((c) => (
                <SelectItem
                  key={c.value || DEFAULT_PROFILE}
                  className="h-auto min-h-8 whitespace-normal"
                  value={c.value || DEFAULT_PROFILE}
                  disabled={(!c.ready && c.value !== '') || c.atLimit}
                >
                  <ToolMark driver={tool} className="me-1.5 align-middle" />
                  <ProviderAccent accent={c.accent} />
                  {c.ready
                    ? c.label
                    : `${c.label} · ${t(c.missing === 'key' ? 'start.profileNeedsKey' : 'start.profileNotSignedIn')}`}
                  {c.usage ? (
                    <>
                      <UsageMeter usage={c.usage} />
                      <AccountUsage usage={c.usage} />
                    </>
                  ) : null}
                  {c.atLimit ? (
                    <AtLimit usage={c.usage} quiet className="ms-2" />
                  ) : null}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {/* An account at its limit is said before Start, with when it resets. */}
          <AtLimit usage={selectedProfile?.usage} className="mt-1 block" />
        </Field>
      ) : null}
      {notReadyNote}
      {key ? (
        <Field label={t('start.model')} description={t('start.modelChoice')}>
          <Select
            disabled={untested}
            // Always controlled: '' shows the placeholder, so a forgotten pick is gone.
            value={model || (noDefault ? '' : DEFAULT_MODEL)}
            onValueChange={(v) =>
              setPick({
                scope,
                model: v === DEFAULT_MODEL ? '' : v,
                // An effort is a level of a model: no model known, no effort kept.
                effort: v === DEFAULT_MODEL && !saved ? '' : effort,
              })
            }
          >
            <SelectTrigger aria-label={t('start.model')}>
              <SelectValue placeholder={t('start.model')} />
            </SelectTrigger>
            <SelectContent>
              {noDefault ? null : (
                <SelectItem value={DEFAULT_MODEL}>
                  <ToolMark driver={tool} className="me-1.5 align-middle" />
                  {saved
                    ? t('start.providerDefault', { model: saved })
                    : t('start.nativeDefault', { tool: toolName(tool) })}
                </SelectItem>
              )}
              {keyModels.map((m) => (
                <SelectItem key={m} value={m}>
                  <ToolMark driver={tool} className="me-1.5 align-middle" />
                  {m}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      ) : null}
      {tool && toolReady && (
        <SessionSkillsPreview
          profileRef={catalogProfile || undefined}
          folderRef={current?.workspace_ref}
        />
      )}
      {tool && toolReady ? (
        <ModelEffortFields
          profileRef={catalogProfile}
          driver={tool}
          model={model}
          effort={effort}
          onChange={(next) => setPick({ scope, ...next })}
          enabled
          fallbackModel={key ? saved : ''}
          withModel={!key}
        />
      ) : null}
      {tool && toolReady ? (
        <LaunchSpeedFields
          driver={tool}
          model={speedModel}
          fastMode={speed?.fastMode}
          serviceTier={speed?.serviceTier}
          onFastMode={(fastMode) =>
            setSpeedChoice({ scope: speedScope, fastMode })
          }
          onServiceTier={(serviceTier) =>
            setSpeedChoice({ scope: speedScope, serviceTier })
          }
        />
      ) : null}
      {speedUnavailable ? (
        <>
          <p role="alert" className="text-caption text-text-2">
            {t('agentops:create.speedUnavailable')}
          </p>
          <Button
            type="button"
            variant="link"
            onClick={() => setSpeedChoice(undefined)}
          >
            {t('agentops:create.speedDefault')}
          </Button>
        </>
      ) : null}
      {untested && canReadProviders ? (
        <Button
          type="button"
          variant="link"
          className="self-start"
          onClick={openProviders}
        >
          {t('start.openProviders')}
        </Button>
      ) : null}
      {current ? (
        <>
          <Field label={t('start.folder')}>
            <Select
              value={
                current.typing
                  ? FOLDER_TYPED
                  : (current.workspace_ref ??
                    (current.selected ? FOLDER_NEW : ''))
              }
              onValueChange={pickFolder}
            >
              <SelectTrigger aria-label={t('start.folder')}>
                <SelectValue placeholder={t('start.folderChoose')} />
              </SelectTrigger>
              <SelectContent
                className="max-w-[var(--radix-select-content-available-width)]"
                // The picker hands focus back to its button when it closes; a typed path
                // was just asked for, so the field takes it.
                onCloseAutoFocus={focusTypedOnClose}
              >
                {/* The list is a page of the registered folders: when it is cut, the picker
                  says so where the list is, and "Another folder…" reaches the rest. */}
                {listaRecortada(registered) ? (
                  <SelectGroup>
                    <SelectLabel>{t('lists.foldersHint')}</SelectLabel>
                  </SelectGroup>
                ) : null}
                {/* A folder the list stopped naming after it was picked stays shown: Start
                  sends what the form shows, and the engine refuses it in its own words. */}
                {current.workspace_ref &&
                !pickable.some(
                  (w) => w.workspace_ref === current.workspace_ref,
                ) ? (
                  <SelectItem
                    value={current.workspace_ref}
                    textValue={folder}
                    title={folder}
                    className={FOLDER_ITEM}
                  >
                    <PathText value={folder} copy={false} />
                  </SelectItem>
                ) : null}
                {pickable.map((w) => (
                  <SelectItem
                    key={w.workspace_ref}
                    value={w.workspace_ref}
                    textValue={folderLabel(w)}
                    title={folderLabel(w)}
                    className={FOLDER_ITEM}
                  >
                    <FolderLabel workspace={w} />
                  </SelectItem>
                ))}
                <SelectItem value={FOLDER_NEW}>
                  {t('start.folderNew')}
                </SelectItem>
                <SelectItem value={FOLDER_TYPED}>
                  {t('start.folderTyped')}
                </SelectItem>
              </SelectContent>
            </Select>
          </Field>
          {typedFolderField}
          {mayAttach ? (
            <StartDocuments
              documents={documents}
              onChange={setDocuments}
              saving={savingDocument}
              maxBytes={documentLimit(place)}
              refusal={documentsRefusal}
            />
          ) : null}
        </>
      ) : (
        <Spinner />
      )}
      {current?.workspace_ref ? (
        <fieldset className="flex flex-col gap-1.5">
          <legend className="text-body font-medium text-foreground">
            {t('start.worktreeLegend')}
          </legend>
          <WorktreeChoice value={worktree} onChange={setWorktree} />
        </fieldset>
      ) : null}
      {/* The same presets for every tool (N1); a launch a tool cannot honour is refused
          by the engine with one sentence, shown below. The current one is always on
          screen, with what it does. */}
      <div className="flex flex-col gap-1.5">
        <Field label={t('start.permission')}>
          <Select
            value={permission}
            onValueChange={(v) => {
              const next = PERMISSION_CHOICES.find((p) => p === v)
              if (next) setPermission(next)
            }}
          >
            <SelectTrigger
              aria-label={t('start.permission')}
              aria-describedby={accessId}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PERMISSION_CHOICES.filter(
                // The engine refuses Full and Auto to anyone who does not run sessions as
                // an administrator, so only they are offered them.
                (p) => mayChoosePermission(p, can('sessions:run:admin')),
              ).map((p) => (
                <SelectItem
                  key={p}
                  value={p}
                  className={p === 'full' ? 'text-warning' : undefined}
                  disabled={!restriction.allows(PERMISSION_NAME[p])}
                  title={
                    !restriction.allows(PERMISSION_NAME[p])
                      ? restriction.reason
                      : undefined
                  }
                >
                  {t(`permission.${p}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <p id={accessId} className="text-caption text-text-2">
          {t(`permissionSummary.${permission}`)}
        </p>
      </div>
      {RUN_ON_EXTENSION ? (
        <RUN_ON_EXTENSION
          value={runOn}
          onChange={setRunOn}
          pending={start.isPending}
        />
      ) : null}
      <LaunchPreflight />
      <div>
        <Button
          type="button"
          variant="link"
          onClick={() => setMore((v) => !v)}
          aria-expanded={more}
        >
          {t('start.more')}
        </Button>
      </div>
      {restrictionNotes}
      {more ? (
        <SessionSecrets value={secretEnv} onChange={setSecretEnv} />
      ) : null}
      {more ? (
        <SessionCredentials value={credentials} onChange={setCredentials} />
      ) : null}
      {more && onAdvanced ? (
        <DisabledReason
          className="self-start"
          disabled={!!moreHeld}
          reason={moreHeld || t('start.advanced')}
        >
          <Button
            type="button"
            variant="link"
            className="self-start"
            // The tool always goes with it, so the dialog reads its default profile for this
            // tool and no other. A tool on a key has no profile before its first launch, and
            // a model tested for the key is no model of a profile: only the tool is handed
            // over then, and while the reuse profile is still being read.
            onClick={() =>
              onAdvanced(
                catalogProfile
                  ? {
                      driver: tool,
                      profileRef: catalogProfile,
                      model,
                      effort,
                      ...(runOn ? { runOn } : {}),
                      ...(speed?.fastMode !== undefined
                        ? { fastMode: speed.fastMode }
                        : {}),
                      ...(speed?.serviceTier
                        ? { serviceTier: speed.serviceTier }
                        : {}),
                      ...handoff,
                    }
                  : {
                      driver: tool,
                      ...(runOn ? { runOn } : {}),
                      ...handoff,
                    },
              )
            }
          >
            {t('start.advanced')}
          </Button>
        </DisabledReason>
      ) : null}
      {startNotes}
      <Answers className={dialog ? 'sm:flex-wrap' : undefined}>
        {/* A tool that cannot start says why under the tool choice; a key that needs a
            model says it here, under Start. */}
        {dialog ? (
          <>
            <p className="me-auto self-center text-caption text-text-2">
              <Trans
                t={t}
                i18nKey="start.keyToStart"
                components={{ key: <Kbd>{startKey}</Kbd> }}
              />
            </p>
            <DialogClose asChild>
              <Button type="button" variant="ghost">
                {t('common:actions.cancel')}
              </Button>
            </DialogClose>
          </>
        ) : null}
        <DisabledReason
          // Start stays beside Cancel; a reason takes a line of its own.
          className={
            dialog
              ? 'contents [&>[data-slot=disabled-reason-text]]:basis-full [&>[data-slot=disabled-reason-text]]:justify-end'
              : 'w-full'
          }
          disabled={
            permissionBlocked ||
            !current ||
            needsFolder ||
            defaultsLoading ||
            speedUnavailable ||
            !!documentsHold ||
            needsKeyModel
          }
          reason={
            permissionBlocked
              ? (restriction.reason ?? '')
              : speedUnavailable
                ? t('agentops:create.speedUnavailable')
                : defaultsLoading
                  ? t('common:states.loading')
                  : !current
                    ? t('start.readingFolder')
                    : needsFolder
                      ? t('start.folderChoose')
                      : documentsHold || keyModelReason
          }
          mode="native"
        >
          <Button
            type="submit"
            variant={startVariant}
            size="lg"
            className={dialog ? undefined : 'w-full'}
            aria-describedby={
              tool && !toolReady ? (toolStepId ?? notReadyId) : undefined
            }
            disabled={
              !current ||
              needsFolder ||
              defaultsLoading ||
              permissionBlocked ||
              start.isPending ||
              !toolReady ||
              speedUnavailable ||
              !!documentsHold ||
              (noDefault && !model)
            }
          >
            {start.isPending ? (
              <Loader2 aria-hidden className="animate-spin" />
            ) : null}
            {start.isPending
              ? t('start.starting')
              : dialog
                ? t('start.startSession')
                : t('start.start')}
          </Button>
        </DisabledReason>
      </Answers>
    </form>
  )
}

/** The New session dialog every entry point opens. */
export function NewSessionDialog({
  open,
  onOpenChange,
  onAdvanced,
  initialDraft,
}: {
  initialDraft?: SessionDraft
  open: boolean
  onOpenChange: (open: boolean) => void
  onAdvanced?: (launch?: AdvancedLaunch) => void
}) {
  const { t } = useTranslation('firstHour')
  // A tool card's key write must not be cut off by the dialog's own close (X, Escape,
  // overlay): the write would then finish with no toast and no refresh.
  const keyBeingStored = useKeyBeingStored()
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => (keyBeingStored() ? undefined : onOpenChange(o))}
    >
      {/* Keep the release version in the status bar (24 px, over the 64 px phone bar
          below 761 px) clear of the expanded dialog, which is centred. */}
      <DialogContent className="max-h-[calc(100dvh-3rem)] max-w-lg max-[760px]:max-h-[calc(100dvh-11rem)]">
        <DialogHeader>
          <DialogTitle>{t('start.dialogTitle')}</DialogTitle>
          <DialogDescription>{t('start.dialogDescription')}</DialogDescription>
        </DialogHeader>
        <SessionReady
          dialog
          initialDraft={initialDraft}
          onStarted={() => onOpenChange(false)}
          onAdvanced={onAdvanced}
        />
      </DialogContent>
    </Dialog>
  )
}

/** Named accounts can start a session even when no default login is ready. Undefined
 * while the reads have not answered. */
function useCanStart(): boolean | undefined {
  const { ready, isLoading } = useReadyTools()
  const profiles = useProfileList()
  if (isLoading || profiles.isLoading) return undefined
  return (
    ready.length > 0 ||
    !!profiles.data?.accounts.some((a) =>
      sessionTools().some((d) => d === a.driver),
    )
  )
}

/** The tools a person can install and sign in from where they stand. */
function ToolCards() {
  return FIRST_HOUR_TOOLS.map((d) => <ToolCard key={d} driver={d} />)
}

/** The work surface before any session, drawn as an empty state: the brand mark over one
 * heading and one sentence, then one action. With a tool ready the action is the composer;
 * until then it is the card that gets the composer's tool ready (install, then sign in),
 * and the composer waits under it, its Start described by the sentence. Only a system
 * administrator installs or signs in a tool: anyone else gets no card, and the composer's
 * own note says who gets the tool ready. Folders the person's tools already worked in are
 * offered in one line under the composer once a session can start. */
export function WorkStart() {
  const { t } = useTranslation('firstHour')
  const { isSuperadmin } = useAuth()
  const sentenceId = useId()
  const canStart = useCanStart()
  const openAdvanced = useNewSessionDialog((s) => s.openAdvanced)
  const stopComposing = useNewSessionDialog((s) => s.stopComposing)
  const toolChoice = useState<SessionTool | null>(null)
  // With no tool ready the composer starts on the engine's first tool unless the person
  // picked another (StartSessionChoices). A tool the first hour installs gets its card here;
  // any other says its step under the composer, with the way to AI tools.
  const cardTool =
    canStart === false && isSuperadmin
      ? FIRST_HOUR_TOOLS.find((d) => d === (toolChoice[0] ?? sessionTools()[0]))
      : undefined
  const cardStatus = useToolStatus(cardTool ?? FIRST_HOUR_TOOLS[0], !!cardTool)
  const sentence = !cardTool
    ? t('steps.start.body')
    : cardStatus.data?.installed
      ? t('steps.signIn.body')
      : t('steps.install.body')
  return (
    <div
      data-testid="work-start"
      className="mx-auto flex w-full max-w-2xl flex-col gap-5 p-4 sm:p-6"
    >
      <div className="flex flex-col items-center gap-3 text-center">
        <BrandMark className="size-12 text-text" />
        <div>
          <h2 className="text-title font-semibold text-text">
            {t('steps.start.title')}
          </h2>
          <p id={sentenceId} className="text-body text-text-2">
            {sentence}
          </p>
        </div>
      </div>
      <PasskeyOffer />
      {cardTool ? <ToolCard driver={cardTool} primary /> : null}
      <StartSessionForm
        composer
        onAdvanced={openAdvanced}
        onStarted={stopComposing}
        toolChoice={toolChoice}
        toolStepId={cardTool ? sentenceId : undefined}
      />
      {canStart ? <BringFoldersStep /> : null}
    </div>
  )
}

export function SessionReady({
  onStarted,
  onAdvanced,
  initialDraft,
  dialog,
}: {
  initialDraft?: SessionDraft
  onStarted?: () => void
  onAdvanced?: (launch?: AdvancedLaunch) => void
  /** Shown in the New session dialog, whose band takes the form's answers. */
  dialog?: boolean
}) {
  const { t } = useTranslation('firstHour')
  const { isSuperadmin } = useAuth()
  const canStart = useCanStart()
  if (canStart === undefined) return <StateBlock state="loading" rows={3} />
  // Only a system administrator installs or signs in a tool: anyone else gets the form,
  // whose note under the tool says who gets it ready.
  if (canStart || !isSuperadmin)
    return (
      <StartSessionForm
        initialDraft={initialDraft}
        onStarted={onStarted}
        onAdvanced={onAdvanced}
        dialog={dialog}
      />
    )
  // No tool ready: the cards that get one ready, and the advanced dialog as the only link.
  return (
    <div className="flex flex-col gap-3">
      <ToolCards />
      {onAdvanced ? (
        <Button
          variant="link"
          className="self-start"
          onClick={() => onAdvanced()}
        >
          {t('start.advanced')}
        </Button>
      ) : null}
    </div>
  )
}

type StepState = 'done' | 'current' | 'later'

const STEP_TONE: Record<StepState, StatusTone> = {
  done: 'ok',
  current: 'warn',
  later: 'neutral',
}

/** One row of the checklist: a done row says what it did, with its check; the current row
 * is marked and holds its one action; a later row only says what comes. */
function Step({
  title,
  body,
  state,
  children,
}: {
  title: string
  body: ReactNode
  state: StepState
  /** The step's action, while it has one. */
  children?: ReactNode
}) {
  return (
    <BoxedListRow
      role="region"
      aria-label={title}
      aria-current={state === 'current' ? 'step' : undefined}
      className="flex-col items-stretch"
    >
      <div className="flex items-center gap-3">
        <StatusDot tone={STEP_TONE[state]} />
        <div className="min-w-0 flex-1">
          <h2
            className={cn(
              'text-body font-medium',
              state === 'later' ? 'text-text-2' : 'text-text',
            )}
          >
            {title}
          </h2>
          <p className="text-caption text-text-2">{body}</p>
        </div>
        {state === 'done' ? (
          <StatusGlyph status="done" showLabel={false} />
        ) : null}
      </div>
      {children}
    </BoxedListRow>
  )
}

/** The three steps of the setup wizard, as a checklist under a meter. */
export function FirstHourSteps() {
  const { t } = useTranslation('firstHour')
  const status = {
    claude: useToolStatus('claude'),
    codex: useToolStatus('codex'),
    grok: useToolStatus('grok'),
    opencode: useToolStatus('opencode'),
    'gemini-cli': useToolStatus('gemini-cli'),
    cursor: useToolStatus('cursor'),
  }
  // The steps install and sign in the session tools whose own login the console relays.
  const signInTools = sessionTools().filter(
    (driver): driver is SignInTool => driver in status,
  )
  const installedTools = signInTools.filter(
    (driver) => status[driver].data?.installed,
  )
  const readyTools = useReadyTools()
  const tools = signInTools.filter(
    (driver) =>
      FIRST_HOUR_TOOLS.some((tool) => tool === driver) ||
      installedTools.includes(driver) ||
      readyTools.ready.includes(driver),
  )
  const readySignInTools = signInTools.filter((driver) =>
    readyTools.ready.includes(driver),
  )
  const signedIn = readyTools.ready.length > 0
  // Installation is known even while a tool's provider is being checked or is refused.
  // A ready tool also proves installation when its status could not be read.
  const installed = signedIn || installedTools.length > 0
  // Unknown is not "no": while what the tools run on is still being read, the steps wait
  // instead of saying no tool is ready (binary 14: "2 of 3" for seconds, then "3 of 3").
  const deciding = !signedIn && readyTools.isLoading
  const boundary = useAuthBoundary()
  const runs = useQuery({
    queryKey: agentOpsKeys.runsScoped(boundary.tenant, boundary.epoch, {
      limit: STARTED_WINDOW,
    }),
    queryFn: ({ signal }) =>
      agentOpsApi.listRuns(
        { limit: STARTED_WINDOW },
        { tenant: boundary.tenant, signal },
      ),
    enabled: !!boundary.tenant,
  })
  // A session that failed did not start (Root 19:15Z): the step is done when one ran.
  const started = (runs.data?.items ?? []).some((r) => r.state !== 'failed')
  const done = [installed, signedIn, started].filter(Boolean).length
  const state = (isDone: boolean, isCurrent: boolean): StepState =>
    isDone ? 'done' : isCurrent ? 'current' : 'later'
  // What a ready tool runs on, in the words of its card: its own login, or a key or a
  // local model from Providers.
  const runsOn = (driver: SignInTool) => {
    const data = status[driver].data
    if (data?.signed_in)
      return data.account
        ? t('status.signedInAs', { account: data.account })
        : t('status.signedIn')
    const key = readyTools.boundKey(driver)?.record
    if (!key) return ''
    return t(key.kind === 'ollama' ? 'status.usesLocal' : 'status.usesKey', {
      name: key.display_name || key.kind,
    })
  }
  const installedList = [...new Set([...installedTools, ...readySignInTools])]
  const installedNames = installedList.map((d) => t(`tools.${d}`)).join(', ')
  const readyNames = readySignInTools.map((driver, i) => {
    const on = runsOn(driver)
    return (
      <Fragment key={driver}>
        {i > 0 ? ', ' : null}
        {t(`tools.${driver}`)}
        {on ? ' · ' : null}
        {on ? <span>{on}</span> : null}
      </Fragment>
    )
  })
  // A key or a local model needs no install first, and stays offered until a tool can
  // start a session. A page link to the Providers form, which brings the person back here
  // (keyHref).
  const orUse =
    signedIn || deciding ? null : (
      <p className="flex flex-wrap items-center gap-x-4 gap-y-1 text-body text-text-2">
        {t('steps.install.orUse')}
        <AppLink
          href={keyHref('anthropic')}
          className="text-foreground underline underline-offset-2"
        >
          {t('steps.install.addKey')}
        </AppLink>
        <AppLink
          href={keyHref('ollama')}
          className="text-foreground underline underline-offset-2"
        >
          {t('steps.install.addLocal')}
        </AppLink>
      </p>
    )
  return (
    <div className="flex flex-col gap-4">
      {deciding ? (
        <Spinner className="size-4" />
      ) : (
        <div className="flex flex-col gap-2">
          <p className="text-caption text-text-2" role="status">
            {t('steps.progress', { done })}
          </p>
          <Meter
            value={(done / STEP_COUNT) * 100}
            label={t('steps.progress', { done })}
            className="w-full max-w-xs"
          />
        </div>
      )}
      <BoxedList>
        <Step
          title={t('steps.install.title')}
          body={installedNames || t('steps.install.body')}
          state={state(installed, true)}
        >
          {installed ? null : (
            <>
              <div className="grid gap-3 md:grid-cols-2">
                {tools.map((d) => (
                  <ToolCard key={d} driver={d} part="install" />
                ))}
              </div>
              {orUse}
            </>
          )}
        </Step>
        <Step
          title={t('steps.signIn.title')}
          body={
            signedIn && readySignInTools.length > 0
              ? readyNames
              : t('steps.signIn.body')
          }
          state={state(signedIn, installed)}
        >
          {installed && !signedIn ? (
            <>
              <div className="grid gap-3 md:grid-cols-2">
                {installedTools.map((d) => (
                  <ToolCard key={d} driver={d} part="signIn" />
                ))}
              </div>
              {orUse}
            </>
          ) : null}
        </Step>
        {/* The last step keeps its Start: the next session starts here too. */}
        <Step
          title={t('steps.start.title')}
          body={t('steps.start.body')}
          state={state(started, signedIn)}
        >
          {signedIn ? (
            <>
              <BringFoldersStep />
              <StartSessionForm startVariant="secondary" />
            </>
          ) : null}
        </Step>
      </BoxedList>
    </div>
  )
}
