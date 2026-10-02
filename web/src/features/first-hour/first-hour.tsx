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
  useQueries,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { Check, ExternalLink, Loader2 } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Segmented } from '@/components/ui/segmented'
import { Textarea } from '@/components/ui/textarea'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { ProfilePreviewDTO } from '@/features/agentops/types'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { providerKeys, providersApi } from '@/features/providers/api'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { useTenantStore } from '@/stores/tenant'
import { stoppedAtStartReason } from '@/features/agentops/launch-readiness'
import {
  FIRST_HOUR_TOOLS,
  SESSION_TOOLS,
  TOOL_NAMES,
  firstHourKeys,
  DEFAULT_PERMISSION,
  installLatest,
  needsApproval,
  signInApi,
  startSession,
  type PermissionChoice,
  type SecretEnvChoice,
  type SessionTool,
  type SignInTool,
  type SignInFlow,
  type ToolKey,
} from './api'
import { SessionSecrets } from './session-secrets'
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

/** What the tool itself says on this node: installed, and signed in. Only a
 * system administrator may ask (the engine answers anyone else 403), so the
 * console does not ask for anyone else (WEB on 09b: two 403s per Home load). */
export function useToolStatus(driver: SignInTool) {
  const tenant = useTenantStore((s) => s.activeTenant)
  const { isSuperadmin } = useAuth()
  return useQuery({
    queryKey: firstHourKeys.signIn(tenant, driver),
    queryFn: ({ signal }) => signInApi.status(driver, tenant, signal),
    enabled: isSuperadmin,
  })
}

/** What a tool runs on: the engine's answer, or the known state of a tool with
 * nothing to run on yet (409: not installed, or nothing in Providers). */
export type RunsOn =
  ProfilePreviewDTO | { reason: 'none'; code: string; refusal: string }

/** What a new session of the tool would run on, in the engine's own words: its
 * login, or the key or local model from Providers the engine picks (HU 030: one
 * rule, in the engine, for the console and the CLI). An error is its refusal —
 * not installed, or nothing to run on — or a read this person may not make. */
export function useRunsOn(driver: SessionTool) {
  const tenant = useTenantStore((s) => s.activeTenant)
  const worthAsking = useInstalledFact(driver)
  return useQuery<RunsOn>({
    queryKey: firstHourKeys.runsOn(tenant, driver),
    queryFn: ({ signal }) => previewRunsOn(driver, signal),
    retry: false,
    enabled: worthAsking === true,
    staleTime: runsOnStaleTime,
  })
}

/** The engine's answer for a tool, or the known state of a tool with nothing to run
 * on yet: its 409 (not installed, or nothing in Providers) is data, not a failure
 * (WEB, ID on 09b), so a page asks once instead of at every mount. */
async function previewRunsOn(
  driver: SessionTool,
  signal: AbortSignal,
): Promise<RunsOn> {
  try {
    return await agentOpsApi.previewProfile(driver, { signal })
  } catch (err) {
    if (err instanceof ApiError && err.status === 409)
      return { reason: 'none', code: err.code, refusal: err.message }
    throw err
  }
}

/** "Nothing to run on yet" is kept for a minute; a sign-in, an install or a
 * Providers change asks again at once (they invalidate the first-hour reads). */
const runsOnStaleTime = (q: { state: { data?: RunsOn } }) =>
  q.state.data?.reason === 'none' ? 60_000 : 0

// HU 049: a fresh install's first page asked the engine's rule what each tool would run
// on while none was installed, and each 409 showed in the browser console as an error.
// The rule is asked only when it could say yes: not about a tool whose own status says
// it is not installed, nor about OpenCode (which never signs in and runs only on a
// Providers record) while the organization has none. Where the console cannot read that
// (no system administrator session, no Providers access, a failed read) it asks, as
// before. `undefined` only until the first read answers, so a refetch never turns a
// shown answer back into "still deciding".
function firstAnswer<T>(
  q: { isError: boolean; isFetched: boolean; data: T | undefined },
  yes: (data: T) => boolean,
): boolean | undefined {
  if (q.isError) return true
  if (q.data !== undefined) return yes(q.data)
  return q.isFetched ? true : undefined
}

/** Whether the tool may be installed: false only when its own status says it is not. */
function useInstalledFact(driver: SessionTool): boolean | undefined {
  const tenant = useTenantStore((s) => s.activeTenant)
  const { isSuperadmin } = useAuth()
  const readable = isSuperadmin === true && driver !== 'opencode'
  const status = useQuery({
    queryKey: firstHourKeys.signIn(tenant, driver as SignInTool),
    queryFn: ({ signal }) =>
      signInApi.status(driver as SignInTool, tenant, signal),
    enabled: readable,
  })
  if (!readable) return true
  return firstAnswer(status, (d) => d.installed !== false)
}

/** Whether OpenCode may have something to run on: false only while the organization
 * has no provider record. */
function useOpenCodeRecordFact(): boolean | undefined {
  const tenant = useTenantStore((s) => s.activeTenant)
  const { can } = useAuth()
  const { epoch } = useAuthBoundary()
  const readable = can('sessions:provider:read')
  const params = { state: 'active', limit: 1 }
  const records = useQuery({
    queryKey: providerKeys.list(tenant, epoch, params),
    queryFn: ({ signal }) => providersApi.list(params, { signal }),
    enabled: readable,
  })
  if (!readable) return true
  return firstAnswer(records, (d) => d.items.length > 0)
}

/** Which tools can start a session: every tool the engine drives (Claude Code, Codex,
 * Grok Build, OpenCode), each ready when the engine has something to run it on (its own
 * login, or a key or local model from Providers). Now and the New session dialog both
 * read this, so they cannot disagree with each other or with the start. A tool that is
 * not ready carries the engine's own sentence (HU 043). */
export function useReadyTools() {
  const tenant = useTenantStore((s) => s.activeTenant)
  const worthAsking: Record<SessionTool, boolean | undefined> = {
    claude: useInstalledFact('claude'),
    codex: useInstalledFact('codex'),
    grok: useInstalledFact('grok'),
    opencode: useOpenCodeRecordFact(),
  }
  const answers = useQueries({
    queries: SESSION_TOOLS.map((driver) => ({
      queryKey: firstHourKeys.runsOn(tenant, driver),
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        previewRunsOn(driver, signal),
      retry: false,
      enabled: worthAsking[driver] === true,
      staleTime: runsOnStaleTime,
    })),
  })
  const ready = SESSION_TOOLS.filter(
    (_, i) => answers[i].isSuccess && answers[i].data.reason !== 'none',
  )
  const refusal = (driver: SessionTool): string | undefined => {
    const answer = answers[SESSION_TOOLS.indexOf(driver)]
    if (answer?.data?.reason === 'none') return answer.data.refusal
    return answer?.isError ? errorText(answer.error) : undefined
  }
  // Only the FIRST answer is waited for. A refused tool (an error, no data) is asked
  // again when another component mounts its query, and that refetch returns it to
  // pending: waiting on it unmounted the form that had just mounted it, in a loop of
  // one request per round trip (09 IP journey: about 1,700 GET resolve for Codex).
  const isLoading =
    SESSION_TOOLS.some((driver) => worthAsking[driver] === undefined) ||
    answers.some((q) => q.isLoading && !q.isFetched)
  return { ready, refusal, isLoading }
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
      qc.invalidateQueries({ queryKey: firstHourKeys.signIn(tenant, driver) }),
  })
  const runsOn = useRunsOn(driver)
  const installed = status.data?.installed ?? false
  const signedIn = status.data?.signed_in ?? false
  const key =
    !signedIn && runsOn.data?.reason === 'api_key'
      ? runsOn.data.provider
      : undefined
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
              <StatusLine ok={signedIn || !!key}>
                {signedIn
                  ? status.data?.account
                    ? t('status.signedInAs', { account: status.data.account })
                    : t('status.signedIn')
                  : key
                    ? t('status.usesKey', {
                        name: key.display_name || key.kind,
                      })
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
      {installed && !signedIn && part !== 'install' ? (
        <SignIn driver={driver} />
      ) : null}
    </div>
  )
}

/** The tool's own login, relayed: a link, and the code (Claude) or the device
 * code (Codex, Grok Build). The login is stored where the tool keeps it, on the server. */
export function SignIn({ driver }: { driver: SignInTool }) {
  const { t } = useTranslation('firstHour')
  const qc = useQueryClient()
  const tenant = useTenantStore((s) => s.activeTenant)
  const [flow, setFlow] = useState<SignInFlow | null>(null)
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  const start = useMutation({
    mutationFn: () => signInApi.start(driver, tenant),
    onSuccess: (f) => {
      setFlow(f)
      setError(null)
    },
    onError: (err) => setError(errorText(err)),
  })
  const submit = useMutation({
    mutationFn: () => signInApi.code(flow!.id, code.trim()),
    onSuccess: (f) => {
      setFlow(f)
      setCode('')
    },
    onError: (err) => setError(errorText(err)),
  })
  const live = flow && flow.state !== 'signed_in' && flow.state !== 'failed'
  useEffect(() => {
    if (!flow || !live) return
    const id = setInterval(async () => {
      try {
        setFlow(await signInApi.get(flow.id))
      } catch {
        // the next tick retries; a vanished login reads as failed below
      }
    }, 1500)
    return () => clearInterval(id)
  }, [flow, live])
  useEffect(() => {
    if (flow?.state === 'signed_in')
      void qc.invalidateQueries({
        queryKey: firstHourKeys.signIn(tenant, driver),
      })
  }, [flow?.state, driver, qc, tenant])
  // Stops the tool's login on this server (its process ends) and forgets it here.
  const cancel = () => {
    if (flow) void signInApi.cancel(flow.id).catch(() => undefined)
    setFlow(null)
    setCode('')
  }

  if (!flow || flow.state === 'failed') {
    return (
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-3">
          <Button
            variant="primary"
            onClick={() => start.mutate()}
            disabled={start.isPending}
          >
            {start.isPending ? (
              <Loader2 aria-hidden className="animate-spin" />
            ) : null}
            {driver === 'claude'
              ? t('actions.signInClaude')
              : driver === 'grok'
                ? t('actions.signInGrok')
                : t('actions.signInChatGPT')}
          </Button>
          <a
            href="/providers"
            className="text-caption text-text-2 underline underline-offset-2"
          >
            {t('actions.useApiKey')}
          </a>
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
  return (
    <div className="flex flex-col gap-3 rounded-[8px] bg-surface p-3">
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
          <code
            className="text-title font-semibold tracking-wider text-text"
            data-testid="device-code"
          >
            {flow.user_code}
          </code>
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
      {driver === 'claude' ? (
        <form
          className="flex flex-col gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            if (code.trim()) submit.mutate()
          }}
        >
          <Field
            label={t('signIn.codeLabel')}
            description={flow.message || t('signIn.codeHint')}
          >
            <Input
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

const PERMISSIONS: PermissionChoice[] = [
  'editsAndCommands',
  'editsOnly',
  'readOnly',
  'ask',
]

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
  const { ready, refusal, isLoading } = useReadyTools()
  const [driver, setDriver] = useState<SessionTool | null>(null)
  // Every tool can be chosen; one that cannot start yet says why in ONE line, for the
  // chosen tool only (Root, 09b capture review: three "Install X first" lines at once).
  const tool = driver ?? ready[0]
  const toolReady = !!tool && ready.includes(tool)
  const [folder, setFolder] = useState('')
  const [prompt, setPrompt] = useState('')
  const [more, setMore] = useState(false)
  const [permission, setPermission] =
    useState<PermissionChoice>(DEFAULT_PERMISSION)
  const [secretEnv, setSecretEnv] = useState<SecretEnvChoice[]>([])
  const start = useMutation({
    mutationFn: () =>
      startSession({
        driver: tool!,
        folder,
        prompt,
        permission,
        secretEnv,
      }),
    onSuccess: (run) => {
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
        start.mutate()
      }}
    >
      {/* Every tool the engine drives (HU 043); the chosen one, when it cannot start yet,
          says why in the engine's own sentence, with the way to AI tools. */}
      <Field label={t('start.tool')}>
        <Segmented
          aria-label={t('start.tool')}
          value={tool}
          onValueChange={(v) => setDriver(v as SessionTool)}
          options={SESSION_TOOLS.map((d) => ({
            value: d,
            label: TOOL_NAMES[d],
          }))}
        />
      </Field>
      {tool && !toolReady ? (
        <p
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
      <Field label={t('start.folder')} description={t('start.folderHint')}>
        <Input
          value={folder}
          onChange={(e) => setFolder(e.target.value)}
          placeholder="/home/you/project"
          spellCheck={false}
        />
      </Field>
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
          </p>
        )
      ) : null}
      <div className="flex flex-wrap items-center gap-3">
        <Button
          type="submit"
          variant="primary"
          size="lg"
          disabled={start.isPending || !toolReady}
        >
          {start.isPending ? (
            <Loader2 aria-hidden className="animate-spin" />
          ) : null}
          {start.isPending ? t('start.starting') : t('start.start')}
        </Button>
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
      <DialogContent className="max-w-lg">
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
  const claude = useToolStatus('claude')
  const codex = useToolStatus('codex')
  const installed = [claude.data, codex.data].some((s) => s?.installed)
  const signedIn = useReadyTools().ready.length > 0
  const boundary = useAuthBoundary()
  const runs = useQuery({
    queryKey: agentOpsKeys.runsScoped(boundary.tenant, boundary.epoch, {
      limit: 1,
    }),
    queryFn: ({ signal }) =>
      agentOpsApi.listRuns({ limit: 1 }, { tenant: boundary.tenant, signal }),
    enabled: !!boundary.tenant,
  })
  const started = (runs.data?.items.length ?? 0) > 0
  const done = [installed, signedIn, started].filter(Boolean).length
  return (
    <div className="flex flex-col gap-4">
      <p className="text-caption text-text-2" role="status">
        {t('steps.progress', { done })}
      </p>
      <Step
        n={1}
        title={t('steps.install.title')}
        body={t('steps.install.body')}
        done={installed}
      >
        <div className="grid gap-3 md:grid-cols-2">
          {FIRST_HOUR_TOOLS.map((d) => (
            <ToolCard key={d} driver={d} part="install" />
          ))}
        </div>
      </Step>
      <Step
        n={2}
        title={t('steps.signIn.title')}
        body={t('steps.signIn.body')}
        done={signedIn}
      >
        {installed ? (
          <div className="grid gap-3 md:grid-cols-2">
            {FIRST_HOUR_TOOLS.filter(
              (d) => (d === 'claude' ? claude.data : codex.data)?.installed,
            ).map((d) => (
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
        {signedIn ? (
          <StartSessionForm />
        ) : (
          <p className="text-body text-text-2">{t('start.needTool')}</p>
        )}
      </Step>
    </div>
  )
}
