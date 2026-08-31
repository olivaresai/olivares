// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// RecordingPolicyPanel — the recording-policy authoring console (the config surface
// of the privileged-session-recording module). It reads GET /v1/m/recording/config
// and writes PUT /config, both gated on the DISTINCT `recording:config:admin`
// permission — NOT the `recording:session:admin` that gates the sessions list. It is
// honest by construction, encoding what the backend actually enforces:
//   - `breakglass_always` is FORCED true server-side and is ABSENT from the PUT body,
//     so it renders as a LOCKED indicator, never a control — no policy can un-record
//     emergency access (the floor is permission-based, not namespace-based).
//   - `retention_enforced` is false: `retention_days` is a classification TAG, not an
//     auto-delete timer — no purge is wired yet, and the panel says so plainly.
//   - Editing recording routes is RBAC-only (permConfigAdmin, no requireAAL3), so
//     there is deliberately NO AAL3/WebAuthn step-up here — a ceremony the backend
//     never verifies would be a lie. Editing gates on can('recording:config:admin')
//     alone; without it (or on a 403 read) we show a calm notice, never a form whose
//     save is doomed to 403.
//   - No route lists the mounted module namespaces, so namespaces are authored as
//     removable chips + an add field; an unknown name is caught by the backend
//     (400 "unknown module namespace …") and surfaced VERBATIM, not swallowed.
import { useQuery } from '@tanstack/react-query'
import { Lock, Plus, X } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { IntelNotice } from '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { recordingApi, recordingKeys } from './api'
import type {
  ConsentMode,
  RecordingConfig,
  RecordingConfigInput,
} from './types'
import './i18n'

// Bounds mirror the backend validator (modules/recording/handlers.go handlePutConfig).
const IDLE_MIN = 60
const IDLE_MAX = 86400
const RETENTION_MIN = 1
const RETENTION_MAX = 3650
const NS_MAX = 64
const NS_LEN_MAX = 32
// Mirrors isNamespaceShaped: lowercase start, [a-z0-9_-] body, no trailing -/_.
const NS_SHAPE = /^[a-z]([a-z0-9_-]*[a-z0-9])?$/

const PERM_CONFIG_ADMIN = 'recording:config:admin'

export function RecordingPolicyPanel() {
  const { t } = useTranslation('recordings')
  const { activeTenant, can } = useAuth()
  const canAdmin = can(PERM_CONFIG_ADMIN)

  // Gate the read on the config permission itself: with only session:admin the
  // backend 403s GET /config, so `enabled` keeps us from firing a request whose
  // save would 403 — we show the honest "requires config admin" notice instead.
  const configQ = useQuery({
    queryKey: recordingKeys.config(activeTenant),
    queryFn: () => recordingApi.getConfig(),
    enabled: canAdmin,
  })

  // ⛔ LA CAPACIDAD PRIMERO. `!canAdmin` es un booleano del CLIENTE y la ceremonia no lo
  // desbloquea, así que sólo cuando `canAdmin` es cierto tiene sentido preguntar si lo que falta
  // es ASEGURAMIENTO. Y el `!necesitaCeremonia` no es cosmético: `isForbidden` es SÓLO el status
  // 403 (lib/api/errors.ts:59-61), así que sin él un `step_up_required` entraría también en
  // `forbidden` y se vería la acusación ADEMÁS de la ceremonia.
  const necesitaCeremonia =
    canAdmin &&
    configQ.error instanceof ApiError &&
    configQ.error.isStepUpRequired
  const forbidden =
    !canAdmin ||
    (!necesitaCeremonia &&
      configQ.error instanceof ApiError &&
      configQ.error.isForbidden)

  return (
    <Card>
      <CardHeader>
        <div className="min-w-0">
          <CardTitle>{t('policy.title')}</CardTitle>
          <CardDescription>{t('policy.subtitle')}</CardDescription>
        </div>
      </CardHeader>
      <CardContent>
        {necesitaCeremonia ? (
          <StepUpRequiredState
            action="generic"
            onElevated={() => void configQ.refetch()}
          />
        ) : forbidden ? (
          <ForbiddenState
            title={t('policy.forbidden.title')}
            description={t('policy.forbidden.description')}
          />
        ) : configQ.isLoading ? (
          <div role="status" className="flex justify-center py-8">
            <span className="sr-only">{t('policy.loading')}</span>
            <Spinner />
          </div>
        ) : configQ.isError ? (
          <ErrorState
            title={t('policy.error.title')}
            description={t('policy.error.description')}
            retry={() => void configQ.refetch()}
          />
        ) : configQ.data ? (
          <PolicyForm
            // Remount with fresh defaults whenever the persisted policy changes
            // (initial load + the refetch after a successful save), so the draft and
            // its dirty state reset to the server's truth without an effect.
            key={signatureOf(configQ.data)}
            config={configQ.data}
            tenant={activeTenant}
          />
        ) : null}
      </CardContent>
    </Card>
  )
}

function signatureOf(c: RecordingConfig): string {
  return [
    [...c.namespaces].join(','),
    c.consent,
    c.idle_seconds,
    c.retention_days,
    c.ai_summaries ? '1' : '0',
  ].join('|')
}

function PolicyForm({
  config,
  tenant,
}: {
  config: RecordingConfig
  tenant: string | null
}) {
  const { t } = useTranslation('recordings')

  const [namespaces, setNamespaces] = useState<string[]>(config.namespaces)
  const [nsInput, setNsInput] = useState('')
  const [nsError, setNsError] = useState<string | null>(null)
  const [consent, setConsent] = useState<ConsentMode>(config.consent)
  const [idle, setIdle] = useState(String(config.idle_seconds))
  const [retention, setRetention] = useState(String(config.retention_days))
  const [aiSummaries, setAiSummaries] = useState(config.ai_summaries)

  const save = usePrivilegedMutation<RecordingConfigInput, RecordingConfig>({
    mutationFn: (body) => recordingApi.updateConfig(body),
    invalidateKeys: [recordingKeys.config(tenant)],
    successMessage: t('policy.saved'),
  })

  const idleNum = Number(idle)
  const retentionNum = Number(retention)
  const idleInvalid =
    !Number.isInteger(idleNum) || idleNum < IDLE_MIN || idleNum > IDLE_MAX
  const retentionInvalid =
    !Number.isInteger(retentionNum) ||
    retentionNum < RETENTION_MIN ||
    retentionNum > RETENTION_MAX

  const dirty =
    !sameMembers(namespaces, config.namespaces) ||
    consent !== config.consent ||
    idleNum !== config.idle_seconds ||
    retentionNum !== config.retention_days ||
    aiSummaries !== config.ai_summaries

  const canSave = dirty && !idleInvalid && !retentionInvalid && !save.isPending

  function addNamespace() {
    const n = nsInput.trim()
    if (!n) return
    // Client-shape checks mirror the backend, but the ENGINE is authoritative on
    // whether a shaped name names a mounted module — an unknown one comes back as a
    // 400 we surface verbatim below (there is no catalog route to pre-populate).
    if (n.length > NS_LEN_MAX) {
      setNsError(t('policy.namespaces.tooLong', { max: NS_LEN_MAX }))
      return
    }
    if (!NS_SHAPE.test(n)) {
      setNsError(t('policy.namespaces.shape'))
      return
    }
    if (namespaces.includes(n)) {
      setNsError(t('policy.namespaces.duplicate'))
      return
    }
    if (namespaces.length >= NS_MAX) {
      setNsError(t('policy.namespaces.tooMany', { max: NS_MAX }))
      return
    }
    setNamespaces([...namespaces, n])
    setNsInput('')
    setNsError(null)
  }

  function removeNamespace(n: string) {
    setNamespaces(namespaces.filter((x) => x !== n))
    setNsError(null)
  }

  function submit() {
    if (!canSave) return
    save.mutate({
      namespaces,
      consent,
      idle_seconds: idleNum,
      retention_days: retentionNum,
      ai_summaries: aiSummaries,
    })
  }

  // The engine is authoritative on namespace validity and every other bound: it
  // answers a bad body with a 400 whose message we render VERBATIM rather than
  // swallow. A 403 is a permission race that usePrivilegedMutation toasts calmly.
  // ⛔ USO NEGADO, y también hay que partirlo: aquí se decide si el mensaje del motor se
  // muestra VERBATIM. Un `step_up_required` satisface `isForbidden` (sólo el status), así que
  // quedaba excluido del mensaje — correcto — pero por el motivo equivocado, y el día que
  // alguien cambie la condición se llevaría la ceremonia por delante. Se nombra lo que se
  // quiere excluir: los DOS 403, cada uno por su razón.
  const serverError =
    save.error instanceof ApiError &&
    !save.error.isForbidden &&
    !save.error.isStepUpRequired
      ? save.error.message
      : null

  return (
    <form
      className="flex flex-col gap-6"
      onSubmit={(e) => {
        e.preventDefault()
        submit()
      }}
    >
      {/* Break-glass floor — forced on server-side, so a locked indicator, never a
          control: no policy can un-record emergency access. */}
      <div className="flex items-start justify-between gap-4 rounded-md border border-border bg-muted/40 px-3 py-2.5">
        <span className="min-w-0">
          <span className="flex items-center gap-1.5 text-sm font-medium text-foreground">
            <Lock className="size-3.5 text-muted-foreground" aria-hidden />
            {t('policy.breakglass.label')}
          </span>
          <span className="mt-0.5 block text-xs text-muted-foreground">
            {t('policy.breakglass.hint')}
          </span>
        </span>
        <Badge variant="info">{t('policy.breakglass.badge')}</Badge>
      </div>

      {/* Recorded namespaces — chips + add field (no catalog route exists). */}
      <Field
        label={t('policy.namespaces.label')}
        description={t('policy.namespaces.hint')}
        error={nsError ?? undefined}
      >
        <div className="flex flex-col gap-2">
          {namespaces.length > 0 ? (
            <div className="flex flex-wrap gap-1.5">
              {namespaces.map((n) => (
                <Badge key={n} variant="neutral" className="gap-1 pr-1">
                  <span className="font-mono">{n}</span>
                  <button
                    type="button"
                    onClick={() => removeNamespace(n)}
                    aria-label={t('policy.namespaces.remove', { name: n })}
                    className="rounded-sm p-0.5 hover:bg-border-strong"
                  >
                    <X className="size-3" aria-hidden />
                  </button>
                </Badge>
              ))}
            </div>
          ) : (
            <p className="text-xs text-muted-foreground">
              {t('policy.namespaces.empty')}
            </p>
          )}
          <div className="flex gap-2">
            <Input
              value={nsInput}
              onChange={(e) => setNsInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  addNamespace()
                }
              }}
              placeholder={t('policy.namespaces.placeholder')}
              aria-label={t('policy.namespaces.addLabel')}
              mono
              className="max-w-xs"
            />
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={addNamespace}
              disabled={!nsInput.trim()}
            >
              <Plus aria-hidden />
              {t('policy.namespaces.add')}
            </Button>
          </div>
        </div>
      </Field>

      {/* Consent posture — "required" is the deny-closed high-assurance dial. */}
      <div className="flex items-start justify-between gap-4">
        <span className="min-w-0">
          <span className="text-sm font-medium text-foreground">
            {t('policy.consent.label')}
          </span>
          <span className="mt-0.5 block text-xs text-muted-foreground">
            {t('policy.consent.hint')}
          </span>
        </span>
        <Switch
          checked={consent === 'required'}
          onCheckedChange={(v) => setConsent(v ? 'required' : 'notice')}
          aria-label={t('policy.consent.label')}
        />
      </div>

      {/* Idle-seal timeout. */}
      <Field
        label={t('policy.idle.label')}
        description={t('policy.idle.hint')}
        error={idleInvalid ? t('policy.idle.error') : undefined}
      >
        {(p) => (
          <Input
            {...p}
            type="number"
            inputMode="numeric"
            min={IDLE_MIN}
            max={IDLE_MAX}
            value={idle}
            onChange={(e) => setIdle(e.target.value)}
            className="max-w-[10rem]"
          />
        )}
      </Field>

      {/* Retention CLASS tag — NOT an auto-delete timer (retention_enforced=false). */}
      <Field
        label={t('policy.retention.label')}
        description={t('policy.retention.hint')}
        error={retentionInvalid ? t('policy.retention.error') : undefined}
      >
        {(p) => (
          <div className="flex flex-col gap-2">
            <div className="flex items-center gap-2">
              <Input
                {...p}
                type="number"
                inputMode="numeric"
                min={RETENTION_MIN}
                max={RETENTION_MAX}
                value={retention}
                onChange={(e) => setRetention(e.target.value)}
                className="max-w-[10rem]"
              />
              <Badge
                variant={config.retention_enforced ? 'success' : 'outline'}
              >
                {config.retention_enforced
                  ? t('policy.retention.enforced')
                  : t('policy.retention.tag')}
              </Badge>
            </div>
            {!config.retention_enforced ? (
              <IntelNotice tone="neutral">
                {t('policy.retention.notEnforced')}
              </IntelNotice>
            ) : null}
          </div>
        )}
      </Field>

      {/* AI-derived summaries. */}
      <div className="flex items-start justify-between gap-4">
        <span className="min-w-0">
          <span className="text-sm font-medium text-foreground">
            {t('policy.ai.label')}
          </span>
          <span className="mt-0.5 block text-xs text-muted-foreground">
            {t('policy.ai.hint')}
          </span>
        </span>
        <Switch
          checked={aiSummaries}
          onCheckedChange={setAiSummaries}
          aria-label={t('policy.ai.label')}
        />
      </div>

      {serverError ? (
        <IntelNotice tone="warning">
          <span className="font-medium">{t('policy.saveFailed')}</span>{' '}
          <span className="text-muted-foreground">{serverError}</span>
        </IntelNotice>
      ) : null}

      <div className="flex items-center justify-end gap-2 border-t border-border pt-4">
        <Button type="submit" variant="primary" size="sm" disabled={!canSave}>
          {save.isPending ? <Spinner size="sm" aria-hidden /> : null}
          {t('policy.save')}
        </Button>
      </div>
    </form>
  )
}

function sameMembers(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  const sb = new Set(b)
  return a.every((x) => sb.has(x))
}
