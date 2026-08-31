// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Onboarding wizard. The first hour of the operator WITHOUT touching a
// terminal: an ACTIONABLE stepper that executes the setup step by step and — the
// thesis of this session — turns a step green ONLY on REAL backend verification, never
// an optimistic guess. The four setup-status flags are weak proxies (identity == "more
// than one user exists", not a real IdP), so each step re-grounds its completion on the
// authoritative per-capability read: a non-default workspace exists, a member roster
// beyond the seed, a source whose live status is running, and a managed-settings PEP a
// host actually attested (claude-policy distribution `verified`). Progress is inherently
// resumable because the source of truth is the backend, not local state. Nearly every
// write needs an AAL3 step-up; the forms wrap the privileged action in RequireAssurance
// and surface the engine's honest denial rather than fake a success.
import './i18n'
import { useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  Check,
  Circle,
  Copy,
  Database,
  KeyRound,
  Plug,
  RefreshCw,
  Rocket,
  ScrollText,
  ShieldCheck,
  Users,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { cuentaConSuelo } from '@/features/workspace-dashboard/count-floor'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/empty-state'
import { ForbiddenState } from '@/components/ui/error-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { CaveatNotice } from '@/features/_intel'
import { AAL, RequireAssurance } from '@/features/identity/assurance'
// The console i18n namespace is registered too: the source step reuses the console's
// plugin-connector strings (pluginNote/testUnavailable) instead of duplicating them.
import '@/features/console/i18n'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { CustomFields, type CustomRow } from '@/features/console/custom-fields'
import type {
  ConnectorInfo,
  RosterMemberDTO,
  SourceRosterEntry,
  WorkspaceDTO,
} from '@/features/console/api'
import { claudePolicyApi } from '@/features/claude-policy/api'
import type { PolicyDistributionView } from '@/features/claude-policy/types'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
// The same name→slug derivation the first-organization form uses (src/lib/utils):
// one rule, so a workspace and an organization never disagree about what a name
// slugifies to.
import { slugify } from '@/lib/utils'
import { useTenantStore } from '@/stores/tenant'

const DISMISS_KEY = 'olivares.onboarding.dismissed'
const PEP_SURFACE = 'managed-settings'

/** A step's verified state. `awaiting` is the honest middle state for the PEP step:
 * published but no host has attested a check-in yet (never "active"). */
type StepState = 'verified' | 'pending' | 'awaiting'

export function OnboardingView() {
  const { t } = useTranslation(['onboarding', 'common'])
  const { can } = useAuth()
  const isAdmin = can('system:admin')
  const tenant = useTenantStore((s) => s.activeTenant)

  const [dismissed, setDismissed] = useState<boolean>(() => {
    try {
      return localStorage.getItem(DISMISS_KEY) === 'true'
    } catch {
      return false
    }
  })

  const enabled = isAdmin && !dismissed

  const setupQ = useQuery({
    queryKey: consoleKeys.setupStatus(),
    queryFn: () => consoleApi.setupStatus(),
    enabled,
  })
  const workspacesQ = useQuery({
    queryKey: consoleKeys.workspaces(tenant),
    queryFn: () => consoleApi.listWorkspaces(),
    enabled,
  })
  const membersQ = useQuery({
    queryKey: consoleKeys.members(tenant),
    queryFn: () => consoleApi.listMembers(),
    enabled,
  })
  const sourcesQ = useQuery({
    queryKey: consoleKeys.sources(),
    queryFn: () => consoleApi.listSources(),
    enabled,
  })
  const distributionQ = useQuery({
    queryKey: ['onboarding', tenant, 'pep-distribution'],
    queryFn: () => claudePolicyApi.getDistribution(PEP_SURFACE),
    enabled,
    retry: false,
  })

  if (!isAdmin) return <ForbiddenState />
  // Dismissed is a preference, not a dead end: the nav entry stays (registry-
  // driven), so the route must render an honest, reversible state — never a
  // blank page (E4b).
  if (dismissed) {
    return (
      <div className="flex flex-col gap-4 p-6">
        <EmptyState
          icon={<Rocket />}
          title={t('dismissed.title')}
          description={t('dismissed.description')}
          action={
            <Button
              variant="secondary"
              onClick={() => {
                try {
                  localStorage.removeItem(DISMISS_KEY)
                } catch {
                  // localStorage unavailable — state still resets for this session.
                }
                setDismissed(false)
              }}
            >
              {t('dismissed.resume')}
            </Button>
          }
        />
      </div>
    )
  }

  // Completion re-grounded on the authoritative read for each capability.
  // `?.steps?.` — every OTHER read below already tolerates a payload without its
  // collection (`?.items ?? []`, `?.sources ?? []`, `?.scopes ?? []`); this one did
  // not, so a setup-status response missing `steps` threw inside render and the
  // operator's FIRST screen became "This view crashed" instead of a wizard with one
  // unverified step. Absent evidence means NOT verified — the fail-closed reading.
  const dbReady =
    setupQ.data?.steps?.find((s) => s.id === 'database')?.completed ?? false
  const workspaces = workspacesQ.data?.items ?? []
  const hasWorkspace = workspaces.some((w) => !w.is_default)
  const members = membersQ.data?.items ?? []
  // `has_more === true` es la unica puerta: el motor lo dice o no se pinta el «≥». Mismo
  // criterio que `cuentaConSuelo`, que trata `false` y `undefined` como «es el total».
  const membersCapped = membersQ.data?.has_more === true
  const hasAdmin = members.length > 1
  const sources = sourcesQ.data?.sources ?? []
  const runningSource = sources.find((s) => s.status === 'running')
  const hasSource = sources.length > 0
  const scopes = distributionQ.data?.scopes ?? []
  const verifiedScope = scopes.find((s) => s.verified)
  const published = distributionQ.data?.latest_revision != null

  const pepState: StepState = verifiedScope
    ? 'verified'
    : published
      ? 'awaiting'
      : 'pending'

  const states: StepState[] = [
    dbReady ? 'verified' : 'pending',
    hasWorkspace ? 'verified' : 'pending',
    hasAdmin ? 'verified' : 'pending',
    // Verified only when a source is actually RUNNING (live ingestion), not merely
    // registered — a registered-but-failed source is "awaiting", never green. This
    // keeps the badge honest to the source's real backend status (the thesis).
    runningSource ? 'verified' : hasSource ? 'awaiting' : 'pending',
    pepState,
  ]
  const total = states.length
  const done = states.filter((s) => s === 'verified').length
  const allDone = done === total

  const refetchAll = () => {
    void setupQ.refetch()
    void workspacesQ.refetch()
    void membersQ.refetch()
    void sourcesQ.refetch()
    void distributionQ.refetch()
  }

  const handleDismiss = () => {
    try {
      localStorage.setItem(DISMISS_KEY, 'true')
    } catch {
      // localStorage unavailable — degrade gracefully.
    }
    setDismissed(true)
  }

  const anyLoading =
    setupQ.isLoading ||
    workspacesQ.isLoading ||
    membersQ.isLoading ||
    sourcesQ.isLoading
  const anyFetching =
    setupQ.isFetching ||
    workspacesQ.isFetching ||
    membersQ.isFetching ||
    sourcesQ.isFetching ||
    distributionQ.isFetching

  return (
    <div className="flex flex-col gap-4 p-6">
      <PageHeader
        icon={Rocket}
        title={t('title')}
        description={t('subtitle')}
        actions={
          <div className="flex items-center gap-2">
            <Badge variant={allDone ? 'success' : 'neutral'}>
              {t('progress', { done, total })}
            </Badge>
            <Button
              variant="ghost"
              size="sm"
              onClick={refetchAll}
              disabled={anyFetching}
            >
              <RefreshCw
                className={`size-3.5${anyFetching ? ' animate-spin' : ''}`}
              />
              {t('refresh')}
            </Button>
            <Button variant="ghost" size="sm" onClick={handleDismiss}>
              {t('dismiss')}
            </Button>
          </div>
        }
      />

      {allDone ? (
        <Card className="border-success/30 bg-success/5 p-4">
          <div className="flex items-center gap-3">
            <div className="flex size-8 items-center justify-center rounded-full bg-success/20 text-success">
              <Check className="size-4" />
            </div>
            <div>
              <p className="font-medium text-foreground">{t('allDone')}</p>
              <p className="text-sm text-muted-foreground">
                {t('allDoneHint')}
              </p>
            </div>
          </div>
        </Card>
      ) : null}

      {anyLoading ? (
        <div role="status" className="flex justify-center py-12">
          <span className="sr-only">{t('common:states.loading')}</span>
          <Spinner />
        </div>
      ) : (
        <div className="flex flex-col gap-3">
          <StepCard
            n={1}
            icon={Database}
            title={t('steps.infra.title')}
            hint={t('steps.infra.hint')}
            state={states[0]}
          >
            <CaveatNotice tone={dbReady ? 'neutral' : 'warning'}>
              {dbReady ? t('steps.infra.ready') : t('steps.infra.notReady')}
            </CaveatNotice>
          </StepCard>

          <StepCard
            n={2}
            icon={Rocket}
            title={t('steps.workspace.title')}
            hint={t('steps.workspace.hint')}
            state={states[1]}
          >
            <WorkspaceStep
              done={hasWorkspace}
              workspaces={workspaces}
              tenant={tenant}
              onDone={() => void workspacesQ.refetch()}
            />
          </StepCard>

          <StepCard
            n={3}
            icon={Users}
            title={t('steps.identity.title')}
            hint={t('steps.identity.hint')}
            state={states[2]}
          >
            <IdentityStep
              done={hasAdmin}
              members={members}
              membersCapped={membersCapped}
              tenant={tenant}
              onDone={() => void membersQ.refetch()}
            />
          </StepCard>

          <StepCard
            n={4}
            icon={Plug}
            title={t('steps.source.title')}
            hint={t('steps.source.hint')}
            state={states[3]}
          >
            <SourceStep
              done={hasSource}
              sources={sources}
              running={runningSource}
              onDone={() => void sourcesQ.refetch()}
            />
          </StepCard>

          <StepCard
            n={5}
            icon={ShieldCheck}
            title={t('steps.pep.title')}
            hint={t('steps.pep.hint')}
            state={states[4]}
          >
            <PepStep
              state={pepState}
              distribution={distributionQ.data}
              onRefresh={() => void distributionQ.refetch()}
            />
          </StepCard>
        </div>
      )}
    </div>
  )
}

// --- step shell --------------------------------------------------------------

function StepCard({
  n,
  icon: Icon,
  title,
  hint,
  state,
  children,
}: {
  n: number
  icon: LucideIcon
  title: string
  hint: string
  state: StepState
  children: ReactNode
}) {
  const { t } = useTranslation(['onboarding'])
  const verified = state === 'verified'
  return (
    <Card className="p-4">
      <div className="flex gap-3">
        <div
          className={`flex size-8 shrink-0 items-center justify-center rounded-full ${
            verified
              ? 'bg-success/20 text-success'
              : 'bg-muted text-muted-foreground'
          }`}
        >
          {verified ? (
            <Check className="size-4" />
          ) : (
            <Circle className="size-4" />
          )}
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <Icon className="size-4 text-muted-foreground" aria-hidden />
            <span className="text-sm font-medium text-foreground">
              {n}. {title}
            </span>
            <Badge
              variant={
                state === 'verified'
                  ? 'success'
                  : state === 'awaiting'
                    ? 'warning'
                    : 'outline'
              }
            >
              {state === 'verified'
                ? t('status.verified')
                : state === 'awaiting'
                  ? t('status.awaiting')
                  : t('status.pending')}
            </Badge>
          </div>
          <p className="mt-0.5 text-xs text-muted-foreground">{hint}</p>
          <div className="mt-3">{children}</div>
        </div>
      </div>
    </Card>
  )
}

/** A privileged inline form: the AAL3 step-up gates the body until the session
 * elevates, exactly like the residency dialog. */
function Privileged({
  action,
  children,
}: {
  action: string
  children: ReactNode
}) {
  return (
    <RequireAssurance minAal={AAL.HARDWARE} action={action}>
      {children}
    </RequireAssurance>
  )
}

// --- step 2: workspace -------------------------------------------------------

function WorkspaceStep({
  done,
  workspaces,
  tenant,
  onDone,
}: {
  done: boolean
  workspaces: WorkspaceDTO[]
  tenant: string | null
  onDone: () => void
}) {
  const { t } = useTranslation(['onboarding', 'common'])
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [slugEdited, setSlugEdited] = useState(false)

  const effectiveSlug = slugEdited ? slug : slugify(name)

  const mut = usePrivilegedMutation<void, WorkspaceDTO>({
    mutationFn: () =>
      consoleApi.createWorkspace({ name: name.trim(), slug: effectiveSlug }),
    invalidateKeys: [consoleKeys.workspaces(tenant)],
    successMessage: t('steps.workspace.created', { name: name.trim() }),
    onDone: () => {
      setName('')
      setSlug('')
      setSlugEdited(false)
      onDone()
    },
  })

  const named = workspaces.filter((w) => !w.is_default)
  const valid = name.trim() !== '' && effectiveSlug !== ''

  return (
    <div className="flex flex-col gap-3">
      {named.length > 0 ? (
        <div className="flex flex-wrap gap-1">
          {named.map((w) => (
            <Badge key={w.id} variant="outline" className="font-mono">
              {w.slug}
            </Badge>
          ))}
        </div>
      ) : null}
      <Privileged action="onboarding">
        <form
          className="flex flex-col gap-3 sm:flex-row sm:items-end"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid && !mut.isPending) mut.mutate()
          }}
        >
          <Field label={t('steps.workspace.name')} className="flex-1">
            {({ id }) => (
              <Input
                id={id}
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t('steps.workspace.namePlaceholder')}
                autoComplete="off"
              />
            )}
          </Field>
          <Field label={t('steps.workspace.slug')} className="flex-1">
            {({ id }) => (
              <Input
                id={id}
                value={effectiveSlug}
                onChange={(e) => {
                  setSlug(e.target.value)
                  setSlugEdited(true)
                }}
                mono
                autoComplete="off"
              />
            )}
          </Field>
          <Button
            type="submit"
            variant="primary"
            disabled={!valid || mut.isPending}
          >
            {mut.isPending && <Spinner size="sm" aria-hidden />}
            {done
              ? t('steps.workspace.addAnother')
              : t('steps.workspace.create')}
          </Button>
        </form>
      </Privileged>
    </div>
  )
}

// --- step 3: identity --------------------------------------------------------

const ROLES = ['admin', 'editor', 'viewer', 'owner'] as const

function IdentityStep({
  done,
  members,
  membersCapped,
  tenant,
  onDone,
}: {
  done: boolean
  members: RosterMemberDTO[]
  membersCapped: boolean
  tenant: string | null
  onDone: () => void
}) {
  const { t } = useTranslation(['onboarding', 'common'])
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<string>('admin')
  const [mode, setMode] = useState<'invite' | 'password'>('invite')
  const [password, setPassword] = useState('')

  const mut = usePrivilegedMutation<void, { invite?: { accept_url: string } }>({
    mutationFn: () =>
      consoleApi.onboard({
        email: email.trim(),
        role,
        mode,
        ...(mode === 'password' ? { password } : {}),
      }),
    invalidateKeys: [consoleKeys.members(tenant), consoleKeys.invites(tenant)],
    successMessage: t('steps.identity.invited', { email: email.trim() }),
    onDone: () => {
      setEmail('')
      setPassword('')
      onDone()
    },
  })

  const emailValid = /.+@.+\..+/.test(email.trim())
  // A password onboarding needs a password; an invite lets the user set their own.
  const valid = emailValid && (mode === 'invite' || password.length >= 8)

  return (
    <div className="flex flex-col gap-3">
      {members.length > 0 ? (
        <p className="text-xs text-muted-foreground">
          {cuentaConSuelo(members.length, membersCapped, (n) =>
            t('steps.identity.memberCount', { count: n }),
          )}
        </p>
      ) : null}
      <Privileged action="onboarding">
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid && !mut.isPending) mut.mutate()
          }}
        >
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label={t('steps.identity.email')}>
              {({ id }) => (
                <Input
                  id={id}
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="admin@example.com"
                  autoComplete="off"
                />
              )}
            </Field>
            <Field label={t('steps.identity.role')}>
              {({ id }) => (
                <Select value={role} onValueChange={setRole}>
                  <SelectTrigger id={id} aria-label={t('steps.identity.role')}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {ROLES.map((r) => (
                      <SelectItem key={r} value={r}>
                        {r}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
            <Field label={t('steps.identity.mode')}>
              {({ id }) => (
                <Select
                  value={mode}
                  onValueChange={(v) => setMode(v as 'invite' | 'password')}
                >
                  <SelectTrigger id={id} aria-label={t('steps.identity.mode')}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="invite">
                      {t('steps.identity.modeInvite')}
                    </SelectItem>
                    <SelectItem value="password">
                      {t('steps.identity.modePassword')}
                    </SelectItem>
                  </SelectContent>
                </Select>
              )}
            </Field>
          </div>
          {mode === 'password' ? (
            <Field
              label={t('steps.identity.password')}
              description={t('steps.identity.passwordHint')}
            >
              {({ id }) => (
                <Input
                  id={id}
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoComplete="new-password"
                />
              )}
            </Field>
          ) : null}
          <div className="flex flex-wrap items-center gap-2">
            <Button
              type="submit"
              variant="primary"
              disabled={!valid || mut.isPending}
            >
              {mut.isPending && <Spinner size="sm" aria-hidden />}
              {done
                ? t('steps.identity.inviteAnother')
                : t('steps.identity.invite')}
            </Button>
            <span className="text-xs text-muted-foreground">
              {t('steps.identity.or')}
            </span>
            <Button asChild variant="secondary" size="sm">
              <Link to={'/console' as never}>{t('steps.identity.ssoCta')}</Link>
            </Button>
          </div>
        </form>
      </Privileged>
    </div>
  )
}

// --- step 4: source ----------------------------------------------------------

const STATUS_TONE: Record<
  string,
  'success' | 'danger' | 'neutral' | 'warning'
> = {
  running: 'success',
  failed: 'danger',
  stopped: 'neutral',
  disabled: 'neutral',
  not_wired: 'warning',
}

function SourceStep({
  done,
  sources,
  running,
  onDone,
}: {
  done: boolean
  sources: SourceRosterEntry[]
  running?: SourceRosterEntry
  onDone: () => void
}) {
  const { t } = useTranslation(['onboarding', 'common', 'console'])
  const activeTenant = useTenantStore((s) => s.activeTenant)
  const catalogQ = useQuery({
    queryKey: consoleKeys.connectors(),
    queryFn: () => consoleApi.listConnectors(),
  })
  const connectors = catalogQ.data?.connectors ?? []

  const [kind, setKind] = useState('')
  const [name, setName] = useState('')
  const [connTenant, setConnTenant] = useState(activeTenant ?? '')
  const [config, setConfig] = useState<Record<string, string>>({})
  const [secrets, setSecrets] = useState<Record<string, string>>({})
  const [custom, setCustom] = useState<CustomRow[]>([])
  const [testState, setTestState] = useState<'idle' | 'ok' | 'fail'>('idle')
  const [testNote, setTestNote] = useState('')

  const info: ConnectorInfo | undefined = connectors.find(
    (c) => c.kind === kind,
  )
  const fields = info?.fields ?? []
  // The ConnectorsTab detection (E4d): an out-of-process plugin kind carries no
  // host-known fields — that is NOT "needs no configuration", its settings are
  // entered as free-form key/value rows and validated when saved.
  const isPlugin = info?.transport === 'plugin' || info?.fields_known === false

  const buildInput = () => {
    const cfg: Record<string, string> = {}
    const sec: Record<string, string> = {}
    for (const f of fields) {
      if (f.secret) {
        const v = secrets[f.key] ?? ''
        if (v !== '') sec[f.key] = v
      } else {
        const v = config[f.key] ?? ''
        if (v !== '') cfg[f.key] = v
      }
    }
    for (const r of custom) {
      const k = r.key.trim()
      if (!k) continue
      if (r.secret) sec[k] = r.value
      else cfg[k] = r.value
    }
    return {
      name: name.trim(),
      kind,
      tenant: connTenant.trim(),
      enabled: true,
      config: cfg,
      secrets: sec,
    }
  }

  const requiredOk = fields.every((f) => {
    if (!f.required) return true
    return f.secret
      ? (secrets[f.key] ?? '') !== ''
      : (config[f.key] ?? '') !== ''
  })
  const valid =
    /^[a-zA-Z0-9._-]+$/.test(name.trim()) &&
    kind !== '' &&
    connTenant.trim() !== '' &&
    requiredOk

  const test = usePrivilegedMutation<void, { ok: boolean }>({
    mutationFn: () => consoleApi.testConnector(buildInput()),
    successMessage: t('steps.source.tested'),
    onDone: () => {
      setTestState('ok')
      setTestNote('')
    },
  })

  const register = usePrivilegedMutation<void, unknown>({
    mutationFn: () => consoleApi.putConnector(buildInput()),
    invalidateKeys: [consoleKeys.sources()],
    successMessage: t('steps.source.registered', { name: name.trim() }),
    onDone: () => {
      setName('')
      setConfig({})
      setSecrets({})
      setTestState('idle')
      onDone()
    },
  })

  return (
    <div className="flex flex-col gap-3">
      {sources.length > 0 ? (
        <div className="flex flex-col gap-1">
          {sources.map((s) => (
            <div key={s.name} className="flex items-center gap-2 text-xs">
              <span className="font-medium text-foreground">{s.name}</span>
              <span className="font-mono text-muted-foreground">{s.kind}</span>
              <Badge variant={STATUS_TONE[s.status] ?? 'neutral'}>
                {s.status}
              </Badge>
            </div>
          ))}
          {!running ? (
            <CaveatNotice tone="warning" className="mt-1">
              {t('steps.source.notRunning')}
            </CaveatNotice>
          ) : null}
        </div>
      ) : null}

      <Privileged action="onboarding">
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid && !register.isPending) register.mutate()
          }}
        >
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label={t('steps.source.kind')}>
              {({ id }) => (
                <Select
                  value={kind}
                  onValueChange={(v) => {
                    setKind(v)
                    setConfig({})
                    setSecrets({})
                    setCustom([])
                    setTestState('idle')
                  }}
                >
                  <SelectTrigger id={id} aria-label={t('steps.source.kind')}>
                    <SelectValue
                      placeholder={t('steps.source.kindPlaceholder')}
                    />
                  </SelectTrigger>
                  <SelectContent>
                    {connectors.map((c) => (
                      <SelectItem key={c.kind} value={c.kind}>
                        {c.title ?? c.kind}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
            <Field label={t('steps.source.name')}>
              {({ id }) => (
                <Input
                  id={id}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={t('steps.source.namePlaceholder')}
                  autoComplete="off"
                />
              )}
            </Field>
            <Field label={t('steps.source.tenant')}>
              {({ id }) => (
                <Input
                  id={id}
                  value={connTenant}
                  onChange={(e) => setConnTenant(e.target.value)}
                  mono
                  autoComplete="off"
                />
              )}
            </Field>
          </div>

          {kind !== '' && isPlugin ? (
            <CaveatNotice tone="neutral">
              {t('console:connectors.pluginNote')}
            </CaveatNotice>
          ) : kind !== '' && fields.length === 0 ? (
            <CaveatNotice tone="neutral">
              {t('steps.source.noFields')}
            </CaveatNotice>
          ) : null}

          {fields.map((f) => (
            <Field
              key={f.key}
              label={`${f.key}${f.required ? ' *' : ''}`}
              description={
                f.secret
                  ? t('steps.source.secretHint')
                  : (f.description ?? undefined)
              }
            >
              {({ id }) =>
                f.secret ? (
                  <Input
                    id={id}
                    type="password"
                    value={secrets[f.key] ?? ''}
                    onChange={(e) =>
                      setSecrets((s) => ({ ...s, [f.key]: e.target.value }))
                    }
                    placeholder="store:my-secret"
                    autoComplete="off"
                  />
                ) : (
                  <Input
                    id={id}
                    value={config[f.key] ?? f.default ?? ''}
                    onChange={(e) =>
                      setConfig((c) => ({ ...c, [f.key]: e.target.value }))
                    }
                    autoComplete="off"
                  />
                )
              }
            </Field>
          ))}

          {/* Free-form settings — the only editor for a plugin kind (E4d). */}
          {isPlugin ? (
            <CustomFields rows={custom} onChange={setCustom} />
          ) : null}

          {testState === 'ok' ? (
            <CaveatNotice tone="info">{t('steps.source.testOk')}</CaveatNotice>
          ) : testState === 'fail' ? (
            <CaveatNotice tone="warning">
              {testNote || t('steps.source.testFail')}
            </CaveatNotice>
          ) : null}

          <div className="flex flex-wrap items-center gap-2">
            <Button
              type="button"
              variant="secondary"
              disabled={!valid || test.isPending || isPlugin}
              title={
                isPlugin ? t('console:connectors.testUnavailable') : undefined
              }
              onClick={() =>
                test.mutate(undefined, {
                  onError: (err) => {
                    setTestState('fail')
                    setTestNote(err instanceof Error ? err.message : '')
                  },
                })
              }
            >
              {test.isPending && <Spinner size="sm" aria-hidden />}
              {t('steps.source.test')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!valid || register.isPending}
            >
              {register.isPending && <Spinner size="sm" aria-hidden />}
              {done ? t('steps.source.addAnother') : t('steps.source.register')}
            </Button>
          </div>
          <div className="flex items-start gap-2 text-xs text-muted-foreground">
            <KeyRound className="mt-px size-3.5 shrink-0" aria-hidden />
            <span>{t('steps.source.credByRef')}</span>
          </div>
        </form>
      </Privileged>
    </div>
  )
}

// --- step 5: PEP -------------------------------------------------------------

function PepStep({
  state,
  distribution,
  onRefresh,
}: {
  state: StepState
  distribution: PolicyDistributionView | undefined
  onRefresh: () => void
}) {
  const { t } = useTranslation(['onboarding', 'common'])
  const tenant = useTenantStore((s) => s.activeTenant)
  const revision = distribution?.latest_revision
  const scopes = distribution?.scopes ?? []
  const artifact = distribution?.artifact

  // Tenant-scoped: two tenants can share a revision number, so an untenanted key
  // would serve one tenant's cached policy body for another after a tenant switch.
  const versionQ = useQuery({
    queryKey: ['onboarding', tenant, 'pep-version', PEP_SURFACE, revision],
    queryFn: () => claudePolicyApi.getVersion(PEP_SURFACE, revision as number),
    enabled: revision != null,
  })
  const content = versionQ.data?.content

  const copy = async () => {
    if (!content) return
    try {
      await navigator.clipboard.writeText(content)
      toast.success(t('steps.pep.copied'))
    } catch {
      toast.error(t('steps.pep.copyFailed'))
    }
  }

  if (state === 'pending') {
    return (
      <div className="flex flex-col gap-3">
        <CaveatNotice tone="warning">
          {t('steps.pep.notPublished')}
        </CaveatNotice>
        <div>
          <Button asChild variant="primary" size="sm">
            <Link to={'/claude-policy' as never}>
              <ScrollText className="size-4" aria-hidden />
              {t('steps.pep.authorCta')}
            </Link>
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-3">
      <CaveatNotice tone={state === 'verified' ? 'info' : 'warning'}>
        {state === 'verified'
          ? t('steps.pep.active')
          : t('steps.pep.awaitingHint')}
      </CaveatNotice>

      {artifact ? (
        <div className="rounded-md border bg-muted/30 p-3 text-xs">
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            <span>
              {t('steps.pep.revision')}:{' '}
              <span className="font-mono text-foreground">
                {artifact.revision}
              </span>
            </span>
            <span>
              {t('steps.pep.fingerprint')}:{' '}
              <span className="font-mono text-foreground">
                {artifact.key_fingerprint}
              </span>
            </span>
          </div>
        </div>
      ) : null}

      {content ? (
        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-foreground">
              {t('steps.pep.snippet')}
            </span>
            <Button variant="ghost" size="sm" onClick={() => void copy()}>
              <Copy className="size-3.5" aria-hidden />
              {t('steps.pep.copy')}
            </Button>
          </div>
          <pre className="max-h-48 overflow-auto rounded-md border bg-background p-3 text-xs">
            <code>{content}</code>
          </pre>
        </div>
      ) : null}

      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-foreground">
          {t('steps.pep.checkins')}
        </span>
        <Button variant="ghost" size="sm" onClick={onRefresh}>
          <RefreshCw className="size-3.5" aria-hidden />
          {t('refresh')}
        </Button>
      </div>
      {scopes.length === 0 ? (
        <EmptyState title={t('steps.pep.noCheckins')} />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b text-left text-xs uppercase tracking-wider text-muted-foreground">
                <th className="py-2 pr-4 font-medium">
                  {t('steps.pep.colScope')}
                </th>
                <th className="py-2 pr-4 font-medium">
                  {t('steps.pep.colReporter')}
                </th>
                <th className="py-2 pr-4 font-medium">
                  {t('steps.pep.colRevision')}
                </th>
                <th className="py-2 pl-4 text-right font-medium">
                  {t('steps.pep.colVerified')}
                </th>
              </tr>
            </thead>
            <tbody>
              {scopes.map((s) => (
                <tr key={s.scope} className="border-b last:border-0">
                  <td className="py-2 pr-4 font-mono text-xs">{s.scope}</td>
                  <td className="py-2 pr-4 text-xs text-muted-foreground">
                    {s.reporter ?? '—'}
                  </td>
                  <td className="py-2 pr-4 font-mono text-xs">
                    {s.reported_revision ?? '—'}
                  </td>
                  <td className="py-2 pl-4 text-right">
                    {s.verified ? (
                      <Badge variant={s.current ? 'success' : 'warning'}>
                        {s.current
                          ? t('steps.pep.verifiedCurrent')
                          : t('steps.pep.verifiedStale')}
                      </Badge>
                    ) : (
                      <Badge variant="outline">
                        {t('steps.pep.unverified')}
                      </Badge>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

export default OnboardingView
