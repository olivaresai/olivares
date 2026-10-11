// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The first hour in three steps — install an agent tool, sign it in with its own
// login, start a session in a folder — and the one New session form every entry
// point opens. Every error says in one sentence what to do and offers the
// button that does it.
import {
  useMutation,
  useQuery,
  useQueryClient,
  type MutationKey,
} from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { Check, ExternalLink, Loader2 } from 'lucide-react'
import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { agentToolsKeys } from '@/features/agent-tools/api'
import { ListTruncationBadge } from '@/features/_intel/notices'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { DisabledReason } from '@/components/ui/disabled-reason'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Segmented } from '@/components/ui/segmented'
import { Spinner } from '@/components/ui/spinner'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { ToolReadinessDTO } from '@/features/agentops/types'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import type { ProviderKind } from '@/features/providers/types'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import type { RequestOptions } from '@/lib/api/client'
import { useStepUpOwner, type StepUpAttempt } from '@/stores/step-up'
import { useTenantStore } from '@/stores/tenant'
import { stoppedAtStartReason } from '@/features/agentops/launch-readiness'
import {
  DEFAULT_PERMISSION,
  launchSession,
  needsApproval,
  type PermissionChoice,
} from '@/features/agentops/session-launch'
import {
  FIRST_HOUR_TOOLS,
  SESSION_TOOLS,
  TOOL_NAMES,
  firstHourKeys,
  installLatest,
  lastFolder,
  signInApi,
  type SecretEnvChoice,
  type SessionTool,
  type SignInTool,
  type SignInFlow,
  type ToolKey,
} from './api'
import { SessionSecrets } from './session-secrets'
import { useSessionProfiles } from './session-profiles'
import './i18n'

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

/** The engine's code for a tool whose only key was refused at its last test. */
const KEY_REFUSED = 'key_refused'

/** "Use an API key instead", and the wizard's "Add an API key" or "Add a local model":
 * the Providers form on that provider, which brings the person back to this page once
 * the new key passes its test. */
function keyHref(kind: ProviderKind): string {
  const returnTo = window.location.pathname + window.location.search
  return `/providers?${new URLSearchParams({ add: kind, returnTo })}`
}

/** The link to add the key a tool runs on instead of its own login; none for a tool the
 * console has no key kind for. */
export function apiKeyHref(driver: string): string | undefined {
  const kind = (KEY_KIND as Partial<Record<string, ProviderKind>>)[driver]
  return kind ? keyHref(kind) : undefined
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
  })
}

/** What the engine says of every tool a session can run: ready, with what it would run
 * on (its own login, or a key or local model from Providers), or the one sentence that
 * says why not (not installed, nothing to run on, a refused key). One read, and one rule
 * in the engine for the console and the CLI (ARCH.C3); a refusal is data, not an error. */
function useToolsReadiness(enabled = true) {
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
 * not ready carries the engine's own sentence (HU 043). */
export function useReadyTools(enabled = true) {
  const answer = useToolsReadiness(enabled)
  const { t } = useTranslation('firstHour')
  const of = (driver: SessionTool) =>
    answer.data?.tools.find((tool) => tool.driver === driver)
  const ready = SESSION_TOOLS.filter((driver) => of(driver)?.ready === true)
  const refusal = (driver: SessionTool): string | undefined => {
    const tool = of(driver)
    // The key's refusal reads in the person's language, with the key's name (HU2-17).
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
}: {
  /** Grok Build is installed from AI tools' own review, so it shows `part="signIn"`. */
  driver: SignInTool
  part?: 'all' | 'install' | 'signIn'
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
  // The provider refused this key at its last test: the tool runs on nothing (HU2-17).
  const keyRefused = answer?.code === KEY_REFUSED
  const ready = signedIn || (!!key && answer?.ready === true)
  const failedJob =
    install.data && install.data.state !== 'succeeded'
      ? install.data
      : undefined
  return (
    <div
      className="flex flex-col gap-3 rounded-[10px] border border-line p-4"
      data-testid={`tool-${driver}`}
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-body font-semibold text-text">
          {t(`tools.${driver}`)}
        </span>
        {status.isLoading ? (
          <Loader2 aria-hidden className="size-4 animate-spin text-text-3" />
        ) : (
          <>
            {part !== 'signIn' ? (
              <StatusLine ok={installed}>
                {installed ? t('status.installed') : t('status.notInstalled')}
              </StatusLine>
            ) : null}
            {installed && part !== 'install' ? (
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
          <Button
            variant="primary"
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
        <SignIn driver={driver} />
      ) : null}
    </div>
  )
}

/** The tool's own login, relayed: a link, and the code (Claude) or the device
 * code (Codex, Grok Build). The login is stored where the tool keeps it, on the server. */
/** The API key each sign-in tool can run on instead of its own login (HU2-18). */
const KEY_KIND: Partial<Record<SignInTool, ProviderKind>> = {
  claude: 'anthropic',
  codex: 'openai',
  grok: 'xai',
  opencode: 'openai',
  'gemini-cli': 'gemini',
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

export function SignIn({
  driver,
  accountRef,
  tenantId,
  onSignedIn,
  mutationScope,
  requestOptions,
  autoStart = false,
}: {
  driver: SignInTool
  accountRef?: string
  tenantId?: string
  onSignedIn?: () => void
  mutationScope?: MutationKey
  requestOptions?: () => RequestOptions
  /** Start the login when this mounts: the person already chose to sign in (Add profile
   * continues here), so a second button would only repeat the choice. */
  autoStart?: boolean
}) {
  const { t } = useTranslation('firstHour')
  const qc = useQueryClient()
  const activeTenant = useTenantStore((s) => s.activeTenant)
  const tenant = tenantId ?? activeTenant
  const [flow, setFlow] = useState<SignInFlow | null>(null)
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  // The poll could not reach the server (not a login that is gone): said, not awaited.
  const [lostTouch, setLostTouch] = useState(false)
  const authority = useRef<RequestOptions | undefined>(undefined)
  const captureRequest = () => {
    const options = requestOptions?.()
    authority.current = options
    return options
  }
  // HU2-06: the login the server still holds survives a reload. It is taken up once, from the
  // status the row already reads; a cancelled one is not brought back. Only the default row
  // does this: the status it reads is the organization's default account, not another one's.
  const ownsDefault = !accountRef && tenant === activeTenant
  const statusRead = useToolStatus(driver, ownsDefault)
  const pending = statusRead.data?.pending
  // A resumed login is only known once the status is read: start nothing before.
  const statusSettled = !ownsDefault || !statusRead.isPending
  // Once per mounted row: after a resume, a start or a cancel, the status's snapshot of a
  // pending login is never taken up again here.
  const [resumeSpent, setResumeSpent] = useState(false)
  if (
    ownsDefault &&
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
      accountRef
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
    onSuccess: (f) => {
      setFlow(f)
      setCode('')
    },
    onError: (err) => setError(errorText(err)),
  })
  const keyKind = KEY_KIND[driver]
  const autoStarted = useRef(false)
  useEffect(() => {
    // A login a reload resumed is the one being completed: never start another over it.
    if (!autoStart || autoStarted.current || flow || !statusSettled) return
    autoStarted.current = true
    start.mutate(captureRequest())
    // Once per mount: a failed start offers the button, never a loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoStart, statusSettled])
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
    if (flow?.state === 'signed_in') {
      if (!accountRef) {
        void qc.invalidateQueries({
          queryKey: firstHourKeys.signIn(tenant, driver),
        })
        void qc.invalidateQueries({ queryKey: firstHourKeys.readiness(tenant) })
      }
      onSignedIn?.()
    }
  }, [flow?.state, driver, qc, tenant, accountRef, onSignedIn])
  // Stops the tool's login on this server (its process ends) and forgets it here.
  const cancel = () => {
    if (flow) {
      setResumeSpent(true)
      void (
        accountRef
          ? signInApi.cancel(flow.id, captureRequest())
          : signInApi.cancel(flow.id)
      ).catch(() => undefined)
    }
    setFlow(null)
    setCode('')
  }

  if (!flow || flow.state === 'failed') {
    return (
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-3">
          <Button
            variant="primary"
            onClick={() => start.mutate(captureRequest())}
            disabled={start.isPending}
          >
            {start.isPending ? (
              <Loader2 aria-hidden className="animate-spin" />
            ) : null}
            {driver === 'claude'
              ? t('actions.signInClaude')
              : driver === 'gemini-cli'
                ? t('actions.signInGoogle')
                : driver === 'grok'
                  ? t('actions.signInGrok')
                  : driver === 'codex' || driver === 'opencode'
                    ? t('actions.signInChatGPT')
                    : t('actions.signIn')}
          </Button>
          {!accountRef && keyKind ? (
            <a
              href={keyHref(keyKind)}
              className="text-caption text-text-2 underline underline-offset-2"
            >
              {t('actions.useApiKey')}
            </a>
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
  const pastesCode =
    driver === 'claude' ||
    driver === 'gemini-cli' ||
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
            {t('signIn.openLink')}
          </a>
        </Button>
      ) : null}
      {driver !== 'claude' && flow.user_code ? (
        <div className="flex flex-col gap-1">
          <span className="text-caption text-text-2">
            {t('signIn.enterCode')}
          </span>
          <DeviceCode code={flow.user_code} />
          <span className="flex items-center gap-2 text-caption text-text-2">
            <Loader2 aria-hidden className="size-3.5 animate-spin" />
            {t('signIn.waiting')}
          </span>
          <span className="text-caption text-text-3">
            {t('signIn.expires')}
          </span>
          <Button
            type="button"
            variant="ghost"
            className="self-start"
            onClick={cancel}
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
            label={t('signIn.codeLabel')}
            description={flow.message || t('signIn.codeHint')}
          >
            <Input
              autoFocus
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
            <Button type="button" variant="ghost" onClick={cancel}>
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

/** The Select value of "the key's or the tool's default". A model ID has no spaces, so it is
 * never this. */
const DEFAULT_MODEL = ' default'
/** The Select value of the default login: Radix refuses an empty value. */
const DEFAULT_PROFILE = ' default-login'

const PERMISSIONS: PermissionChoice[] = [
  'editsAndCommands',
  'editsOnly',
  'readOnly',
  'ask',
]

/** How many recent sessions the "Start a session" step looks at for one that ran, and
 * the New session form for the last folder. */
const STARTED_WINDOW = 20

/** How many registered folders, newest first, the New session form matches the last
 * folder against; one beyond them gives a new folder instead. */
const FOLDER_WINDOW = 200

/** The one New session form: tool, folder, first message; the rest under More. */
export function StartSessionForm({
  onStarted,
  onAdvanced,
}: {
  onStarted?: () => void
  /** The advanced launch dialog, offered inside More options: one expander (Root, 09b). */
  onAdvanced?: () => void
}) {
  const { t } = useTranslation('firstHour')
  const navigate = useNavigate()
  const { ready, refusal, boundKey, isLoading } = useReadyTools()
  const { can } = useAuth()
  const tenant = useTenantStore((s) => s.activeTenant)
  const boundary = useAuthBoundary()
  const { epoch } = boundary
  const [driver, setDriver] = useState<SessionTool | null>(null)
  // Every tool can be chosen; one that cannot start yet says why in ONE line, for the
  // chosen tool only (Root, 09b capture review: three "Install X first" lines at once).
  const tool = driver ?? ready[0]
  // Start points at the sentence under the tool choice when that tool cannot start (#1083).
  const notReadyId = useId()
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
  const registered = useQuery({
    queryKey: [
      ...agentOpsKeys.boundaryScope(boundary.tenant, epoch),
      'workspaces',
      { state: 'active', limit: FOLDER_WINDOW },
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.listWorkspaces(
        { state: 'active', limit: FOLDER_WINDOW },
        { tenant: boundary.tenant, signal },
      ),
    enabled: !!boundary.tenant,
    staleTime: 0,
  })
  // A tool with several profiles asks which one; with one, the engine picks as it always
  // did. A named profile that is ready can start the tool even when its default login
  // is not.
  const profiles = useSessionProfiles(
    tool,
    runs.data?.items ?? [],
    !!tool && ready.includes(tool),
  )
  const profileRef = profiles?.selected ?? ''
  const profileReady =
    profileRef !== '' &&
    !!profiles?.choices.find((c) => c.value === profileRef)?.ready
  const toolReady = !!tool && (ready.includes(tool) || profileReady)
  // Taken once the reads settle and kept, so a refetch never moves the folder under the
  // user. It belongs to the tenant and sign-in it was read for, like the model pick.
  const folderScope = `${boundary.tenant}:${epoch}`
  const [chosen, setChosen] = useState<{
    scope: string
    folder: string
    workspace_ref?: string
    editing: boolean
  } | null>(null)
  if (
    chosen?.scope !== folderScope &&
    !runs.isFetching &&
    !registered.isFetching
  )
    setChosen({
      scope: folderScope,
      ...lastFolder(runs.data?.items ?? [], registered.data?.items ?? []),
      editing: false,
    })
  const current = chosen?.scope === folderScope ? chosen : null
  const folder = current?.folder ?? ''
  const [prompt, setPrompt] = useState('')
  const [more, setMore] = useState(false)
  const [permission, setPermission] =
    useState<PermissionChoice>(DEFAULT_PERMISSION)
  const [secretEnv, setSecretEnv] = useState<SecretEnvChoice[]>([])
  // The model, for a tool that runs on a key: its saved default unless the user picks one.
  // A pick belongs to the tool, key and sign-in it was made for; another one forgets it.
  const key = tool && toolReady && !profileRef ? boundKey(tool) : undefined
  const saved = key?.record.default_model || ''
  const tested = key?.record.models ?? []
  const noDefault = key?.modelRequired === true && !saved
  const scope = `${tool}:${key?.record.provider_ref ?? ''}:${tenant}:${epoch}`
  const [pick, setPick] = useState({ scope, model: '' })
  if (pick.scope !== scope) setPick({ scope, model: '' })
  const model = pick.scope === scope ? pick.model : ''
  // A key with no tested model, for a tool that needs one, can start nothing yet: its remedy
  // is a connection test in Providers.
  const untested = noDefault && tested.length === 0
  const canReadProviders = can('sessions:provider:read')
  const openProviders = () => {
    onStarted?.()
    navigate({ to: '/providers' as '/' })
  }
  // The launch authority: the sign-in, organization and credential the Start was pressed
  // under, checked at every dispatch, as the New session dialog's.
  const captureOwner = useStepUpOwner()
  const start = useMutation({
    mutationFn: ({
      tenant,
      attempt,
    }: {
      tenant: string | null
      attempt: StepUpAttempt
    }) =>
      launchSession(
        {
          quick: {
            driver: tool!,
            folder,
            workspace_ref: current?.workspace_ref,
            permission,
            secretEnv,
            ...(model ? { model } : {}),
            ...(profileRef ? { profileRef } : {}),
          },
          message: prompt,
        },
        {
          tenant,
          signal: attempt.signal,
          dispatchGuard: attempt.dispatchGuard,
        },
      ),
    onSuccess: (run, { attempt }) => {
      if (!attempt.current()) return
      onStarted?.()
      // The conversation in front, not the list: on a phone only one pane fits, and the
      // rail is the default one (SC 59 item 2: the new session needed an extra tap).
      navigate({
        to: '/sessions' as '/',
        search: { session: `run:${run.run_ref}`, pane: 'narrative' } as never,
      })
    },
  })
  if (isLoading) {
    return <Loader2 aria-hidden className="size-5 animate-spin text-text-3" />
  }
  if (!tool) {
    return <p className="text-body text-text-2">{t('start.needTool')}</p>
  }
  return (
    <form
      className="flex flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault()
        // A second Start would retire the launch in flight and leave its run unopened.
        if (start.isPending) return
        const owner = captureOwner()
        start.mutate({ tenant: owner.tenant, attempt: owner.begin() })
      }}
    >
      {/* Every tool the engine drives (HU 043); the chosen one, when it cannot start yet,
          says why in the engine's own sentence, with the way to AI tools. */}
      <Field label={t('start.tool')}>
        <Segmented
          aria-label={t('start.tool')}
          className="flex-wrap"
          value={tool}
          onValueChange={(v) => setDriver(v as SessionTool)}
          options={SESSION_TOOLS.map((d) => ({
            value: d,
            label: TOOL_NAMES[d],
          }))}
        />
      </Field>
      {profiles ? (
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
            <SelectContent>
              {profiles.choices.map((c) => (
                <SelectItem
                  key={c.value || DEFAULT_PROFILE}
                  value={c.value || DEFAULT_PROFILE}
                  disabled={!c.ready && c.value !== ''}
                >
                  {c.ready
                    ? c.label
                    : `${c.label} · ${t(c.missing === 'key' ? 'start.profileNeedsKey' : 'start.profileNotSignedIn')}`}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      ) : null}
      {tool && !toolReady ? (
        <p
          id={notReadyId}
          className="text-caption text-text-2"
          role="status"
          data-slot="tool-not-ready"
        >
          {refusal(tool) ?? t('start.notReady', { tool: TOOL_NAMES[tool] })}{' '}
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
        </p>
      ) : null}
      {key ? (
        <Field label={t('start.model')} description={t('start.modelChoice')}>
          <Select
            disabled={untested}
            // Always controlled: '' shows the placeholder, so a forgotten pick is gone.
            value={model || (noDefault ? '' : DEFAULT_MODEL)}
            onValueChange={(v) =>
              setPick({ scope, model: v === DEFAULT_MODEL ? '' : v })
            }
          >
            <SelectTrigger aria-label={t('start.model')}>
              <SelectValue placeholder={t('start.model')} />
            </SelectTrigger>
            <SelectContent>
              {noDefault ? null : (
                <SelectItem value={DEFAULT_MODEL}>
                  {saved
                    ? t('start.providerDefault', { model: saved })
                    : t('start.nativeDefault', { tool: TOOL_NAMES[tool] })}
                </SelectItem>
              )}
              {tested.map((m) => (
                <SelectItem key={m} value={m}>
                  {m}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
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
      {current?.editing ? (
        <Field label={t('start.folder')} description={t('start.folderHint')}>
          <Input
            autoFocus
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
        </Field>
      ) : (
        <div className="flex flex-col gap-1.5" data-slot="session-folder">
          <span className="text-body font-medium text-foreground">
            {t('start.folder')}
          </span>
          {current ? (
            <p className="flex flex-wrap items-center gap-x-2 text-body">
              <span className="break-all font-mono text-text">
                {folder || t('start.folderNew')}
              </span>
              <Button
                type="button"
                variant="link"
                onClick={() => setChosen({ ...current, editing: true })}
              >
                {t('start.folderChange')}
              </Button>
            </p>
          ) : (
            <Loader2 aria-hidden className="size-4 animate-spin text-text-3" />
          )}
        </div>
      )}
      <ListTruncationBadge
        query={runs}
        filas={runs.data?.items.length ?? 0}
        label={t('lists.runsTruncated', {
          count: runs.data?.items.length ?? 0,
        })}
        hint={t('lists.runsHint')}
        className="p-0 [&_span]:leading-relaxed [&_span]:whitespace-normal"
      />
      <ListTruncationBadge
        query={registered}
        filas={registered.data?.items.length ?? 0}
        label={t('lists.foldersTruncated', {
          count: registered.data?.items.length ?? 0,
        })}
        hint={t('lists.foldersHint')}
        className="p-0 [&_span]:leading-relaxed [&_span]:whitespace-normal"
      />
      <Field label={t('start.prompt')}>
        <Textarea
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          placeholder={t('start.promptPlaceholder')}
          rows={3}
        />
      </Field>
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
      {more ? (
        // The same presets for every tool (N1); a launch a tool cannot honour is refused
        // by the engine with one sentence, shown below.
        <Field label={t('start.permission')}>
          <div
            className="flex flex-col gap-1.5"
            role="radiogroup"
            aria-label={t('start.permission')}
          >
            {PERMISSIONS.map((p) => (
              <label
                key={p}
                className="flex items-center gap-2 text-body text-text"
              >
                <input
                  type="radio"
                  name="permission"
                  value={p}
                  checked={permission === p}
                  onChange={() => setPermission(p)}
                />
                {t(`permission.${p}`)}
              </label>
            ))}
          </div>
        </Field>
      ) : null}
      {more ? (
        <SessionSecrets value={secretEnv} onChange={setSecretEnv} />
      ) : null}
      {more && onAdvanced ? (
        <Button
          type="button"
          variant="link"
          className="self-start"
          onClick={onAdvanced}
        >
          {t('start.advanced')}
        </Button>
      ) : null}
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
      <div className="flex flex-wrap items-center gap-3">
        {/* A tool that cannot start says why under the tool choice; a key that needs a
            model says it here, beside Start. */}
        <DisabledReason
          disabled={!current || (toolReady && noDefault && !model)}
          reason={
            !current
              ? t('start.readingFolder')
              : untested
                ? t('start.noTestedModel')
                : t('start.chooseModel')
          }
          mode="native"
        >
          <Button
            type="submit"
            variant="primary"
            size="lg"
            aria-describedby={tool && !toolReady ? notReadyId : undefined}
            disabled={
              !current || start.isPending || !toolReady || (noDefault && !model)
            }
          >
            {start.isPending ? (
              <Loader2 aria-hidden className="animate-spin" />
            ) : null}
            {start.isPending ? t('start.starting') : t('start.start')}
          </Button>
        </DisabledReason>
      </div>
    </form>
  )
}

/** The New session dialog every entry point opens. */
export function NewSessionDialog({
  open,
  onOpenChange,
  onAdvanced,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onAdvanced?: () => void
}) {
  const { t } = useTranslation('firstHour')
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* Keep the phone brand and release version above the expanded dialog. */}
      <DialogContent className="max-w-lg max-[760px]:max-h-[calc(100dvh-7rem)]">
        <DialogHeader>
          <DialogTitle>{t('start.dialogTitle')}</DialogTitle>
          <DialogDescription>{t('start.dialogDescription')}</DialogDescription>
        </DialogHeader>
        <SessionReady
          onStarted={() => onOpenChange(false)}
          onAdvanced={onAdvanced}
        />
      </DialogContent>
    </Dialog>
  )
}

/** Start form when a tool is ready; otherwise the tool cards that get one ready. */
export function SessionReady({
  onStarted,
  onAdvanced,
}: {
  onStarted?: () => void
  onAdvanced?: () => void
}) {
  const { t } = useTranslation('firstHour')
  const { ready, isLoading } = useReadyTools()
  if (isLoading)
    return <Loader2 aria-hidden className="size-5 animate-spin text-text-3" />
  if (ready.length > 0)
    return <StartSessionForm onStarted={onStarted} onAdvanced={onAdvanced} />
  // No tool ready: the cards that get one ready, and the advanced dialog as the only link.
  return (
    <div className="flex flex-col gap-3">
      {FIRST_HOUR_TOOLS.map((d) => (
        <ToolCard key={d} driver={d} />
      ))}
      {onAdvanced ? (
        <Button variant="link" className="self-start" onClick={onAdvanced}>
          {t('start.advanced')}
        </Button>
      ) : null}
    </div>
  )
}

function Step({
  n,
  title,
  body,
  done,
  children,
}: {
  n: number
  title: string
  body: string
  done: boolean
  children: ReactNode
}) {
  return (
    <section
      className="flex gap-4 rounded-[12px] border border-line p-5"
      aria-label={title}
    >
      <div
        className={
          done
            ? 'flex size-8 shrink-0 items-center justify-center rounded-full bg-success-soft text-success'
            : 'flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-text-2'
        }
        aria-hidden
      >
        {done ? <Check className="size-4" /> : n}
      </div>
      <div className="flex min-w-0 flex-1 flex-col gap-3">
        <div>
          <h2 className="text-body-l font-semibold text-text">{title}</h2>
          <p className="text-body text-text-2">{body}</p>
        </div>
        {children}
      </div>
    </section>
  )
}

/** The three steps of the setup wizard. */
export function FirstHourSteps() {
  const { t } = useTranslation('firstHour')
  const status = {
    claude: useToolStatus('claude'),
    codex: useToolStatus('codex'),
    grok: useToolStatus('grok'),
    opencode: useToolStatus('opencode'),
    'gemini-cli': useToolStatus('gemini-cli'),
  }
  const installedTools = SESSION_TOOLS.filter(
    (driver) => status[driver].data?.installed,
  )
  const readyTools = useReadyTools()
  const tools = SESSION_TOOLS.filter(
    (driver) =>
      FIRST_HOUR_TOOLS.some((tool) => tool === driver) ||
      installedTools.includes(driver) ||
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
  return (
    <div className="flex flex-col gap-4">
      {deciding ? (
        <Spinner className="size-4" />
      ) : (
        <p className="text-caption text-text-2" role="status">
          {t('steps.progress', { done })}
        </p>
      )}
      <Step
        n={1}
        title={t('steps.install.title')}
        body={t('steps.install.body')}
        done={installed}
      >
        <div className="grid gap-3 md:grid-cols-2">
          {tools.map((d) => (
            <ToolCard key={d} driver={d} part="install" />
          ))}
        </div>
        {signedIn || deciding ? null : (
          // A key or a local model needs no install first, and stays offered until a tool
          // can start a session. The same Providers form as a tool's "Use an API key
          // instead", which brings the person back here.
          <p className="flex flex-wrap items-center gap-x-4 gap-y-1 text-body text-text-2">
            {t('steps.install.orUse')}
            <a
              href={keyHref('anthropic')}
              className="text-accent-text underline underline-offset-2"
            >
              {t('steps.install.addKey')}
            </a>
            <a
              href={keyHref('ollama')}
              className="text-accent-text underline underline-offset-2"
            >
              {t('steps.install.addLocal')}
            </a>
          </p>
        )}
      </Step>
      <Step
        n={2}
        title={t('steps.signIn.title')}
        body={t('steps.signIn.body')}
        done={signedIn}
      >
        {installed ? (
          <div className="grid gap-3 md:grid-cols-2">
            {tools
              .filter(
                (d) =>
                  readyTools.ready.includes(d) || installedTools.includes(d),
              )
              .map((d) => (
                <ToolCard key={d} driver={d} part="signIn" />
              ))}
          </div>
        ) : (
          <p className="text-body text-text-2">{t('steps.signIn.waiting')}</p>
        )}
      </Step>
      <Step
        n={3}
        title={t('steps.start.title')}
        body={t('steps.start.body')}
        done={started}
      >
        {signedIn ? null : (
          <ListTruncationBadge
            query={runs}
            filas={runs.data?.items.length ?? 0}
            label={t('lists.runsTruncated', {
              count: runs.data?.items.length ?? 0,
            })}
            hint={t('lists.runsHint')}
            className="p-0 [&_span]:leading-relaxed [&_span]:whitespace-normal"
          />
        )}
        {signedIn ? (
          <StartSessionForm />
        ) : deciding ? null : (
          <p className="text-body text-text-2">{t('start.needTool')}</p>
        )}
      </Step>
    </div>
  )
}
