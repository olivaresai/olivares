// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Cedar/OPA policy-as-code authoring AND revision lifecycle (ADM-GOV-02), wired to
// the live routes at /v1/m/governance/pdp/: validate, explain, dry-run, versions,
// versions/{revision}, active, tests, publish, rollback.
//
// WHAT THE AUTHORED CEDAR POLICY ACTUALLY DOES. This screen used to say
// "deny-only / restrict-only: a policy can only NARROW an existing RBAC grant".
// That is FALSE in production and the whole chain was verified:
//   • cmd/olivares/boot.go:677 wires the governance engine as the POSITIVE scoped
//     authorizer — auth.NewAuthorizer(gov.RequestEvaluator(),
//     auth.WithScopedGrants(gov.ScopedGrants())).
//   • modules/governance/grants.go:767 returns auth.EffectGrant when Cedar answers
//     Allow; grants.go:36 states it outright ("This is why a permit can now GRANT").
//   • core/auth/authorizer.go:169-185 — `granted` from EffectGrant makes
//     `if !az.rbacAllows(req) && !granted` fall through, so an RBAC DENIAL IS
//     BYPASSED. The algebra is Allow = (RBAC OR Grant) AND NOT Forbid AND NOT
//     deny-overlay.
// So the true model is THREE-VALUED: a permit GRANTS within its resolved scope
// tree (beyond the RBAC baseline), a forbid RESTRICTS and overrides any grant
// (forbid-overrides-permit), and no match ABSTAINS so the RBAC decision stands.
// Two bounds stay true and are stated in the UI: a grant never reaches outside the
// scope tree it resolves to, and offline past the staleness bound a grant degrades
// to ABSTAIN rather than keep authorizing (grants.go:757-763) — a restriction stays
// enforced. Saying this correctly is acute now that this screen can PUBLISH.
//
// OPA/Rego is authoring + versioning ONLY: nothing is enforced from this process
// (the operator's own OPA sidecar owns Rego enforcement). So its primary action is
// labelled "Version", never "Publish", and its selected revision is badged
// "Selected (not enforced here)", never "Active"/"Enforcing".
//
// TWO FACTS THE UI KEEPS APART. `active` says which revision the STORE selects;
// `live_activation` says whether the running evaluator took it. "deferred" means
// committed and selected but NOT swapped — the PREVIOUS policy is still deciding
// requests — so it renders as a WARNING, never a success. There is no HTTP endpoint
// that reloads the PDP on demand, so no "reload now" button is offered; the real
// recovery paths are named instead.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { UseQueryResult } from '@tanstack/react-query'
import {
  FlaskConical,
  History,
  ScanSearch,
  Scale,
  Send,
  TriangleAlert,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { CodeDiff } from '@/components/ui/code-diff'
import { CodeEditor } from '@/components/ui/code-editor'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { CaveatNotice, SectionCard } from '@/features/_intel'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { claudePolicyApi, claudePolicyKeys } from './api'
import { DeclaredSection } from './components'
import type {
  PdpActivePolicy,
  PdpActiveSurface,
  PdpDecision,
  PdpEngine,
  PdpExampleRequest,
  PdpLiveActivation,
  PdpPublishResult,
  PdpRevision,
  PdpRollbackResult,
  PdpTestStatus,
  PdpValidateResult,
} from './types'

const ENGINES: readonly PdpEngine[] = ['cedar', 'opa']

// Seeded policy SOURCE (the document being edited), not UI chrome — it stays in the
// policy language, like the seeded JSON documents of the managed-* surfaces. Its
// comments state the real three-valued model, not the stale deny-overlay one.
const DEFAULT_SOURCE: Record<PdpEngine, string> = {
  cedar: `// A permit GRANTS within its resolved scope tree, beyond the RBAC baseline.
// A forbid RESTRICTS and overrides any grant. No match abstains: RBAC stands.
forbid(
  principal,
  action,
  resource
)
when { resource.sensitivity == "secret" };`,
  opa: `package olivares.authz

# Authoring + versioning only: nothing here is enforced by this process.
# Your own OPA sidecar owns Rego enforcement.
default allow := true

deny if {
  input.resource.sensitivity == "secret"
}`,
}

/** The outcome of the last publish/activation — the two results are shaped
 *  differently but render through the same honest live_activation switch. */
type LifecycleOutcome =
  | { kind: 'publish'; result: PdpPublishResult }
  | { kind: 'rollback'; result: PdpRollbackResult }

/** What we can honestly put on the left-hand side of the pre-publish diff. */
type DiffBase =
  | { kind: 'ready'; content: string }
  /** No revision is selected yet — a publish would create the FIRST one. Not an
   *  error, and not a reason to block publishing. */
  | { kind: 'none' }
  | { kind: 'loading' }
  /** The active revision could not be loaded. We say so and BLOCK publish rather
   *  than fall through to a silently empty diff. */
  | { kind: 'unavailable' }

export function CedarOpaView({ active }: { active: boolean }) {
  const { t } = useTranslation(['claudePolicy', 'common'])
  const { can, activeTenant } = useAuth()
  const canAuthor = can('governance:policy:admin')
  // validate/explain/dry-run are READ-tier routes on the engine (governance.go),
  // so they follow the read permission, not the admin one.
  const canRead = can('governance:policy:read')
  const queryClient = useQueryClient()

  const [engine, setEngine] = useState<PdpEngine>('cedar')
  const [source, setSource] = useState(DEFAULT_SOURCE.cedar)
  const [note, setNote] = useState('')
  const [validateResult, setValidateResult] =
    useState<PdpValidateResult | null>(null)
  const [decision, setDecision] = useState<PdpDecision | null>(null)
  const [confirmPublish, setConfirmPublish] = useState(false)
  const [rollbackTarget, setRollbackTarget] = useState<PdpRevision | null>(null)
  const [outcome, setOutcome] = useState<LifecycleOutcome | null>(null)

  // Example request (mirrors the engine's auth.Request / ResourceAttrs).
  const [req, setReq] = useState({
    principalKind: 'user',
    permission: 'memory:read',
    tenant: '',
    resourceKind: 'memory',
    resourceId: '',
    sensitivity: 'secret',
  })
  const exampleRequest = (): PdpExampleRequest => ({
    principal: { kind: req.principalKind },
    permission: req.permission,
    tenant: req.tenant || undefined,
    resource: {
      kind: req.resourceKind,
      id: req.resourceId || undefined,
      sensitivity: req.sensitivity || undefined,
    },
  })

  const activeQuery = useQuery({
    queryKey: claudePolicyKeys.pdpActive(activeTenant, engine),
    queryFn: () => claudePolicyApi.pdpActive(engine),
    enabled: active && canRead,
  })
  const versionsQuery = useQuery({
    queryKey: claudePolicyKeys.pdpVersions(activeTenant),
    queryFn: () => claudePolicyApi.pdpVersions(),
    enabled: active && canRead,
  })

  const activeRevision = activeQuery.data?.authored.present
    ? activeQuery.data.authored.revision
    : undefined
  // /pdp/tests WITHOUT a revision returns the NEWEST revision, which is NOT the
  // active one after an activation. Always ask for the active revision explicitly,
  // and ask for nothing at all when there is no active revision to ask about.
  const gateQuery = useQuery({
    queryKey: claudePolicyKeys.pdpTests(activeTenant, engine, activeRevision),
    queryFn: () => claudePolicyApi.pdpTestStatus(engine, activeRevision),
    enabled: active && canRead && activeRevision !== undefined,
  })

  const diffBase = resolveDiffBase(canRead, activeQuery)
  const surfaces = resolveSurfaces(canRead, activeQuery)
  // All three resolve from the SAME query, so the screen can never report the
  // surfaces as read while reporting the live state as unknown, or vice versa.
  const liveState = resolveLiveActivation(canRead, activeQuery)
  const publishBlocked =
    diffBase.kind === 'unavailable' || diffBase.kind === 'loading'

  // Activating an older revision is exactly as blind as publishing blind: the dialog
  // otherwise names a revision number and nothing about what it would put in force.
  // Read the target's stored source so the operator sees what changes before it
  // decides requests. Only the authored surface is diffable — see the union notice.
  const rollbackPreviewQuery = useQuery({
    queryKey: claudePolicyKeys.pdpVersion(
      activeTenant,
      rollbackTarget?.surface ?? engine,
      // 0 is never a real revision; the query is disabled while there is no target.
      rollbackTarget?.revision ?? 0,
    ),
    queryFn: () =>
      claudePolicyApi.pdpGetVersion(
        rollbackTarget!.surface,
        rollbackTarget!.revision,
      ),
    enabled: canRead && rollbackTarget !== null,
  })

  // The baseline MUST come from the target's own engine, not from the dropdown. The
  // version history lists both engines, so an operator reading Cedar can activate an
  // OPA revision — and diffing that against the active CEDAR policy would render a
  // fabricated Cedar-vs-Rego changeset labelled as what the activation would do.
  // When the target's engine is the selected one this resolves to the same cache
  // entry as activeQuery, so it costs nothing in the common case.
  const rollbackBaseQuery = useQuery({
    queryKey: claudePolicyKeys.pdpActive(
      activeTenant,
      rollbackTarget?.surface ?? engine,
    ),
    queryFn: () => claudePolicyApi.pdpActive(rollbackTarget!.surface),
    enabled: canRead && rollbackTarget !== null,
  })

  function switchEngine(e: PdpEngine) {
    setEngine(e)
    setSource(DEFAULT_SOURCE[e])
    setValidateResult(null)
    setDecision(null)
    setOutcome(null)
    setNote('')
  }

  // NO contract-pending branch. /pdp/validate, /pdp/explain and /pdp/dry-run are
  // all mounted (modules/governance/governance.go), so a 404/405/501 from them is
  // a real failure — telling the operator "the backend endpoint is not live yet …
  // Nothing is faked" in a calm info tone would be a roadmap note pasted over a
  // rejection, and it offers nothing to retry.
  const onError =
    (_what: 'validate' | 'explain' | 'dry-run') => (e: unknown) => {
      toast.error(
        t('common:errors.generic', { defaultValue: 'Something went wrong' }),
        {
          description: e instanceof Error ? e.message : undefined,
        },
      )
    }

  const validate = useMutation({
    mutationFn: () => claudePolicyApi.pdpValidate(engine, source),
    onSuccess: (r) => setValidateResult(r),
    onError: onError('validate'),
  })
  const explain = useMutation({
    mutationFn: () =>
      claudePolicyApi.pdpExplain(engine, source, exampleRequest()),
    onSuccess: (r) => setDecision(r),
    onError: onError('explain'),
  })
  const dryRun = useMutation({
    mutationFn: () =>
      claudePolicyApi.pdpDryRun(engine, source, exampleRequest()),
    onSuccess: (r) => setDecision(r),
    onError: onError('dry-run'),
  })

  // The privileged-mutation pattern, with ONE deliberate divergence from
  // usePrivilegedMutation: that hook always toast.success()es, and a green success
  // toast is a lie when live_activation is "deferred" (stored + selected, but the
  // PREVIOUS policy is still enforcing). The 403 handling is kept identical to it.
  async function invalidateLifecycle() {
    await queryClient.invalidateQueries({
      queryKey: claudePolicyKeys.pdp(activeTenant),
    })
  }

  // Dispatched by NAME, never by exclusion. The else-branch used to be
  // "versioned" — a calm success toast — so any value this console did not know
  // about would have been announced as the most benign outcome there is. An
  // unrecognized activation state is exactly the case where that is most likely
  // to be wrong, so it warns and says what it is.
  function announce(live: PdpLiveActivation | undefined) {
    switch (live) {
      case 'applied':
        toast.success(t('pdp.result.appliedToast'))
        return
      case 'deferred':
        toast.warning(t('pdp.result.deferredToast'))
        return
      case 'not_applicable':
        toast.success(t('pdp.result.versionedToast'))
        return
      default:
        toast.warning(t('pdp.result.unknownToast'))
    }
  }

  // El reporte vive en un solo sitio (use-privileged-mutation.ts:25-32).
  const report = useFailedActionReporter()

  function onLifecycleError(e: unknown) {
    // A 403 is a permission boundary, not a failure — the same calm warning
    // usePrivilegedMutation raises (normally the action is hidden when !can()).
    // ⛔ ASEGURAMIENTO ANTES QUE ROL. `isForbidden` es SÓLO el status 403
    // (lib/api/errors.ts:59-61) y un `step_up_required` lo satisface también, así que esta rama
    // acusaba al operador de un permiso que SÍ tiene y le escondía la salida.
    //
    // DEFENSA EN PROFUNDIDAD: los emisores medidos son cuatro familias —21 `requireAAL3` en
    // `core/api`, dos escrituras en `modules/governance`, el `requireStepUp` propio de
    // `modules/deploy` y los retornos de `core/auth/webauthn.go`— y esta ruta no está en ninguna
    // hoy. Se arregla porque el defecto es de FORMA y sobrevive al día en que el gate llegue.
    if (e instanceof ApiError && e.isStepUpRequired) {
      report(e)
      return
    }
    if (e instanceof ApiError && e.isForbidden) {
      toast.warning(t('common:privileged.notAuthorizedToast'))
      return
    }
    toast.error(
      t('common:errors.generic', { defaultValue: 'Something went wrong' }),
      { description: e instanceof Error ? e.message : undefined },
    )
  }

  const publishMutation = useMutation({
    mutationFn: () =>
      claudePolicyApi.pdpPublish(engine, source, note.trim() || undefined),
    onSuccess: async (result) => {
      setOutcome({ kind: 'publish', result })
      setConfirmPublish(false)
      await invalidateLifecycle()
      announce(result.live_activation)
    },
    onError: (e) => {
      setConfirmPublish(false)
      onLifecycleError(e)
    },
  })

  const rollbackMutation = useMutation({
    mutationFn: (target: PdpRevision) =>
      claudePolicyApi.pdpRollback(target.surface, target.revision),
    onSuccess: async (result) => {
      setOutcome({ kind: 'rollback', result })
      setRollbackTarget(null)
      await invalidateLifecycle()
      announce(result.live_activation)
    },
    onError: (e) => {
      setRollbackTarget(null)
      onLifecycleError(e)
    },
  })

  if (!active) return null

  const primaryLabel =
    engine === 'cedar' ? t('pdp.publish.action') : t('pdp.publish.actionOpa')

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="info">{t('pdp.liveBadge')}</Badge>
        <span className="text-xs text-muted-foreground">
          {t('pdp.liveHint')}
        </span>
      </div>

      {engine === 'cedar' ? (
        <CaveatNotice tone="info">
          <Scale
            className="mr-1 inline size-3.5 align-text-bottom"
            aria-hidden
          />
          <span className="font-medium">{t('pdp.cedarSemantics')}</span>{' '}
          <span className="text-muted-foreground">
            {t('pdp.cedarSemanticsBounds')}
          </span>
        </CaveatNotice>
      ) : (
        <CaveatNotice tone="info">{t('pdp.opaAuthoringOnly')}</CaveatNotice>
      )}

      {/* Above the editor on purpose: whether the revision on screen is the one
          deciding requests is the fact an operator needs BEFORE they change it,
          not one they discover below the fold after publishing. */}
      {canRead && <LiveActivationPanel engine={engine} state={liveState} />}

      <div className="grid gap-4 lg:grid-cols-[1fr_22rem]">
        <div className="flex min-w-0 flex-col gap-3">
          <div className="flex items-center gap-2">
            <span className="text-xs font-medium text-muted-foreground">
              {t('pdp.engine')}
            </span>
            <Select
              value={engine}
              onValueChange={(v) => switchEngine(v as PdpEngine)}
            >
              <SelectTrigger className="w-40" aria-label={t('pdp.engine')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="cedar">Cedar</SelectItem>
                <SelectItem value="opa">OPA / Rego</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <CodeEditor
            value={source}
            onChange={setSource}
            language={engine === 'cedar' ? 'cedar' : 'rego'}
            ariaLabel={t('pdp.editorLabel', { engine })}
            readOnly={!canAuthor}
            height="20rem"
          />

          {canRead && (
            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="secondary"
                size="sm"
                disabled={validate.isPending}
                onClick={() => validate.mutate()}
              >
                {validate.isPending ? (
                  <Spinner size="sm" aria-hidden />
                ) : (
                  <ScanSearch />
                )}
                {t('pdp.validate')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                disabled={explain.isPending}
                onClick={() => explain.mutate()}
              >
                {explain.isPending ? (
                  <Spinner size="sm" aria-hidden />
                ) : (
                  <ScanSearch />
                )}
                {t('pdp.explain')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                disabled={dryRun.isPending}
                onClick={() => dryRun.mutate()}
              >
                {dryRun.isPending ? (
                  <Spinner size="sm" aria-hidden />
                ) : (
                  <FlaskConical />
                )}
                {t('pdp.dryRun')}
              </Button>
            </div>
          )}

          {validateResult && (
            <SectionCard title={t('pdp.validateTitle')}>
              {validateResult.ok && validateResult.diagnostics.length === 0 ? (
                <p className="text-xs text-success">{t('pdp.valid')}</p>
              ) : (
                <ul className="flex flex-col gap-1">
                  {validateResult.diagnostics.map((d, i) => (
                    <li key={i} className="text-xs">
                      <Badge
                        variant={d.severity === 'error' ? 'danger' : 'warning'}
                      >
                        {d.severity}
                      </Badge>{' '}
                      <span className="text-muted-foreground">
                        {d.line ? `L${d.line}: ` : ''}
                      </span>
                      {d.message}
                    </li>
                  ))}
                </ul>
              )}
            </SectionCard>
          )}

          {decision && <DecisionPanel decision={decision} />}

          {canRead && (
            <PrePublishDiff
              engine={engine}
              base={diffBase}
              draft={source}
              surfaces={surfaces}
            />
          )}

          {canAuthor && (
            <div className="flex flex-col gap-2">
              <Field label={t('pdp.publish.noteLabel')} htmlFor="pdp-note">
                <Input
                  id="pdp-note"
                  value={note}
                  maxLength={200}
                  placeholder={t('pdp.publish.notePlaceholder')}
                  onChange={(e) => setNote(e.target.value)}
                />
              </Field>
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  variant="primary"
                  size="sm"
                  disabled={publishBlocked || publishMutation.isPending}
                  onClick={() => setConfirmPublish(true)}
                >
                  <Send />
                  {primaryLabel}
                </Button>
                {publishBlocked && (
                  <span className="text-xs text-warning">
                    {t('pdp.diff.publishBlocked')}
                  </span>
                )}
              </div>
            </div>
          )}

          {outcome && <LifecycleResultPanel outcome={outcome} />}

          {canRead && (
            <GatePanel
              engine={engine}
              revision={activeRevision}
              query={gateQuery}
              baseUnknown={activeQuery.isLoading || activeQuery.isError}
            />
          )}

          {canRead && (
            <DeclaredSection
              query={versionsQuery}
              what={t('pdp.history.what')}
              live
            >
              {(data) => (
                <VersionHistory
                  items={data.items ?? []}
                  canAuthor={canAuthor}
                  onActivate={setRollbackTarget}
                />
              )}
            </DeclaredSection>
          )}
        </div>

        <aside className="flex min-w-0 flex-col gap-3">
          <SectionCard
            title={t('pdp.exampleTitle')}
            description={t('pdp.exampleSub')}
          >
            <div className="flex flex-col gap-3">
              <Field label={t('pdp.principalKind')} htmlFor="pdp-pk">
                <Input
                  id="pdp-pk"
                  mono
                  value={req.principalKind}
                  onChange={(e) =>
                    setReq((r) => ({ ...r, principalKind: e.target.value }))
                  }
                />
              </Field>
              <Field label={t('pdp.permission')} htmlFor="pdp-perm">
                <Input
                  id="pdp-perm"
                  mono
                  value={req.permission}
                  onChange={(e) =>
                    setReq((r) => ({ ...r, permission: e.target.value }))
                  }
                />
              </Field>
              <Field label={t('pdp.resourceKind')} htmlFor="pdp-rk">
                <Input
                  id="pdp-rk"
                  mono
                  value={req.resourceKind}
                  onChange={(e) =>
                    setReq((r) => ({ ...r, resourceKind: e.target.value }))
                  }
                />
              </Field>
              <Field label={t('pdp.sensitivity')} htmlFor="pdp-sens">
                <Select
                  value={req.sensitivity}
                  onValueChange={(v) =>
                    setReq((r) => ({ ...r, sensitivity: v }))
                  }
                >
                  <SelectTrigger id="pdp-sens">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="public">public</SelectItem>
                    <SelectItem value="internal">internal</SelectItem>
                    <SelectItem value="secret">secret</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
            </div>
          </SectionCard>
        </aside>
      </div>

      <ConfirmDialog
        open={confirmPublish}
        onOpenChange={(o) => {
          if (!publishMutation.isPending) setConfirmPublish(o)
        }}
        tone="danger"
        title={
          engine === 'cedar'
            ? t('pdp.publish.confirmTitle')
            : t('pdp.publish.confirmTitleOpa')
        }
        description={
          engine === 'cedar'
            ? t('pdp.publish.confirmBody')
            : t('pdp.publish.confirmBodyOpa')
        }
        confirmLabel={primaryLabel}
        pending={publishMutation.isPending}
        onConfirm={() => publishMutation.mutate()}
      >
        <p className="text-xs text-muted-foreground">
          {engine === 'cedar'
            ? t('pdp.publish.confirmNote')
            : t('pdp.publish.confirmNoteOpa')}
        </p>
      </ConfirmDialog>

      <ConfirmDialog
        open={rollbackTarget !== null}
        onOpenChange={(o) => {
          if (!o && !rollbackMutation.isPending) setRollbackTarget(null)
        }}
        tone="danger"
        title={t('pdp.rollback.confirmTitle', {
          n: rollbackTarget?.revision ?? 0,
        })}
        description={t('pdp.rollback.confirmBody', {
          n: rollbackTarget?.revision ?? 0,
          engine: rollbackTarget?.surface ?? '',
        })}
        confirmLabel={t('pdp.rollback.confirmLabel', {
          n: rollbackTarget?.revision ?? 0,
        })}
        pending={rollbackMutation.isPending}
        onConfirm={() => {
          if (rollbackTarget) rollbackMutation.mutate(rollbackTarget)
        }}
      >
        <p className="text-xs text-muted-foreground">
          {rollbackTarget?.surface === 'opa'
            ? t('pdp.rollback.confirmNoteOpa')
            : t('pdp.rollback.confirmNote')}
        </p>
        {rollbackTarget && (
          <RollbackPreview
            target={rollbackTarget}
            query={rollbackPreviewQuery}
            baseQuery={rollbackBaseQuery}
          />
        )}
      </ConfirmDialog>
    </div>
  )
}

/** What can honestly be shown on the left of the pre-publish diff. An absent
 *  active revision is NOT the same fact as a failed read: the first is "there is
 *  nothing to compare against yet", the second is "we do not know what is live". */
function resolveDiffBase(
  canRead: boolean,
  query: {
    data?: PdpActivePolicy
    isLoading: boolean
    isError: boolean
  },
): DiffBase {
  if (!canRead) return { kind: 'unavailable' }
  if (query.isLoading) return { kind: 'loading' }
  if (query.isError || !query.data) return { kind: 'unavailable' }
  const authored = query.data.authored
  if (!authored.present) return { kind: 'none' }
  // An empty policy is a LEGAL published revision (the engine compiles ""), and it is
  // a meaningful diff base: it is how an operator sees that a draft adds the first
  // rules. Only a genuinely missing field means "we could not read it".
  if (authored.content === undefined) return { kind: 'unavailable' }
  return { kind: 'ready', content: authored.content }
}

/** What can honestly be said about the three contributing surfaces — the same
 *  shape as resolveDiffBase, and copied from it deliberately.
 *
 *  This used to be one falsy test (`s?.present ? … : "none"`) applied to a policy
 *  the component received as an optional prop, so a FAILED READ rendered
 *  "Managed — none / Adopted — none": the operator was told, in the panel whose
 *  whole job is to disclose that more is in force than what they edit, that
 *  nothing else was in force. The engine emits `present` WITHOUT omitempty
 *  exactly so a false is a MEASURED negative (pdp_authoring.go); collapsing it
 *  with "there is no object here" threw that guarantee away on the console side. */
type SurfacesState =
  | { kind: 'ready'; policy: PdpActivePolicy }
  | { kind: 'loading' }
  | { kind: 'unknown' }

function resolveSurfaces(
  canRead: boolean,
  query: {
    data?: PdpActivePolicy
    isLoading: boolean
    isError: boolean
  },
): SurfacesState {
  if (!canRead) return { kind: 'unknown' }
  if (query.isLoading) return { kind: 'loading' }
  if (query.isError || !query.data) return { kind: 'unknown' }
  return { kind: 'ready', policy: query.data }
}

/** Whether the process that served the read is deciding requests with the
 *  revision on screen. Three answers plus "could not find out" — never two. */
type LiveState =
  | { kind: 'known'; live: PdpLiveActivation; expired: boolean }
  | { kind: 'loading' }
  | { kind: 'unknown' }

/** The membership test is the point. `live_activation` is optional on the wire
 *  (an engine older than the field omits it), so anything that is not one of the
 *  three names it knows lands in `unknown` — it is never trusted as a value just
 *  because TypeScript would have typed it as one. */
function resolveLiveActivation(
  canRead: boolean,
  query: {
    data?: PdpActivePolicy
    isLoading: boolean
    isError: boolean
  },
): LiveState {
  if (!canRead) return { kind: 'unknown' }
  if (query.isLoading) return { kind: 'loading' }
  if (query.isError || !query.data) return { kind: 'unknown' }
  const live = query.data.live_activation
  if (
    live === 'applied' ||
    live === 'deferred' ||
    live === 'not_applicable' ||
    live === 'no_policy'
  ) {
    return { kind: 'known', live, expired: query.data.grants_expired === true }
  }
  return { kind: 'unknown' }
}

/** THE ANSWER TO THIS SCREEN'S CENTRAL QUESTION — "is the revision I am looking
 *  at the one deciding requests?" — and, until now, the one fact a reload
 *  destroyed. It lived only in the component state set by the last publish
 *  (`outcome`), so a refresh, a second operator or a different replica saw
 *  nothing, while the version history went on badging a revision "Active" from
 *  the STORE's selection alone. GET /pdp/active now measures it per process. */
function LiveActivationPanel({
  engine,
  state,
}: {
  engine: PdpEngine
  state: LiveState
}) {
  const { t } = useTranslation('claudePolicy')
  if (state.kind === 'loading') {
    return (
      <SectionCard title={t('pdp.live.title')}>
        <p className="text-xs text-muted-foreground">{t('pdp.live.loading')}</p>
      </SectionCard>
    )
  }
  if (state.kind === 'unknown') {
    return (
      <SectionCard title={t('pdp.live.title')}>
        <p className="flex items-start gap-1.5 text-xs text-warning">
          <TriangleAlert className="mt-px size-3.5 shrink-0" aria-hidden />
          {t('pdp.live.unknown')}
        </p>
      </SectionCard>
    )
  }
  const { live, expired } = state
  return (
    <SectionCard title={t('pdp.live.title')}>
      <div className="flex flex-wrap items-center gap-2">
        {live === 'applied' && (
          <>
            <Badge variant="success">{t('pdp.live.appliedBadge')}</Badge>
            <span className="text-xs text-muted-foreground">
              {t('pdp.live.appliedBody')}
            </span>
          </>
        )}
        {live === 'deferred' && (
          <>
            <Badge variant="warning">{t('pdp.live.deferredBadge')}</Badge>
            <span className="flex items-start gap-1.5 text-xs text-warning">
              <TriangleAlert className="mt-px size-3.5 shrink-0" aria-hidden />
              {t('pdp.live.deferredBody')}
            </span>
          </>
        )}
        {live === 'not_applicable' && (
          <>
            <Badge variant="info">{t('pdp.live.notApplicableBadge')}</Badge>
            <span className="text-xs text-muted-foreground">
              {t('pdp.live.notApplicableBody', { engine })}
            </span>
          </>
        )}
        {live === 'no_policy' && (
          <>
            <Badge variant="outline">{t('pdp.live.noPolicyBadge')}</Badge>
            <span className="text-xs text-muted-foreground">
              {t('pdp.live.noPolicyBody')}
            </span>
          </>
        )}
      </div>
      {/* A SEPARATE axis, and it has to be able to contradict the badge above:
          past the offline-staleness bound the engine still holds the selected
          policy — so this really is `applied` — while its POSITIVE grants have
          degraded to abstain. A green badge on its own would be a half-truth. */}
      {expired && (
        <p className="mt-2 flex items-start gap-1.5 text-xs text-warning">
          <TriangleAlert className="mt-px size-3.5 shrink-0" aria-hidden />
          {t('pdp.live.grantsExpired')}
        </p>
      )}
      {live !== 'not_applicable' && live !== 'no_policy' && (
        <p className="mt-2 text-xs text-muted-foreground">
          {t('pdp.live.processScope')}
        </p>
      )}
    </SectionCard>
  )
}

/** Diff of the DRAFT against the active AUTHORED revision, plus the disclosure
 *  that the authored surface is not the whole enforced policy. It never falls
 *  through to an empty diff: every non-ready state says which one it is. */
function PrePublishDiff({
  engine,
  base,
  draft,
  surfaces,
}: {
  engine: PdpEngine
  base: DiffBase
  draft: string
  /** The resolved read state — NOT a bare optional policy. Handing this
   *  component `activeQuery.data` was what let EnforcedSurfaces mistake a failed
   *  read for a measured absence: `undefined` carried no reason. */
  surfaces: SurfacesState
}) {
  const { t } = useTranslation('claudePolicy')
  const activePolicy = surfaces.kind === 'ready' ? surfaces.policy : undefined
  return (
    <SectionCard
      title={t('pdp.diff.title')}
      description={t('pdp.diff.subtitle')}
    >
      {base.kind === 'ready' && (
        <CodeDiff
          original={base.content}
          modified={draft}
          language={engine === 'cedar' ? 'cedar' : 'rego'}
          originalLabel={t('pdp.diff.activeLabel', {
            n: activePolicy?.authored.revision ?? 0,
          })}
          modifiedLabel={t('pdp.diff.draftLabel')}
          height="18rem"
        />
      )}
      {base.kind === 'loading' && (
        <p className="text-xs text-muted-foreground">{t('pdp.diff.loading')}</p>
      )}
      {base.kind === 'none' && (
        <p className="text-xs text-muted-foreground">
          {t('pdp.diff.noActive')}
        </p>
      )}
      {base.kind === 'unavailable' && (
        <p className="flex items-start gap-1.5 text-xs text-warning">
          <TriangleAlert className="mt-px size-3.5 shrink-0" aria-hidden />
          {t('pdp.diff.unavailable')}
        </p>
      )}
      <EnforcedSurfaces engine={engine} surfaces={surfaces} />
    </SectionCard>
  )
}

/** What activating an older revision would put in force, diffed against what is in
 *  force now. Activating a policy whose text you cannot see is the same blind
 *  operation as publishing one you have not diffed — this closes that half.
 *
 *  Like the pre-publish diff, it never renders an empty comparison: a failed read
 *  says so, because "we could not read r3" and "r3 is identical to what is live"
 *  look the same in an empty diff and mean opposite things. */
function RollbackPreview({
  target,
  query,
  baseQuery,
}: {
  target: PdpRevision
  query: {
    data?: PdpRevision
    isLoading: boolean
    isError: boolean
  }
  /** The active policy of the TARGET's engine — never the dropdown's. */
  baseQuery: {
    data?: PdpActivePolicy
    isLoading: boolean
    isError: boolean
  }
}) {
  const { t } = useTranslation('claudePolicy')
  const language = target.surface === 'cedar' ? 'cedar' : 'rego'
  if (query.isLoading || baseQuery.isLoading) {
    return (
      <p className="mt-2 text-xs text-muted-foreground">
        {t('pdp.rollback.previewLoading')}
      </p>
    )
  }
  const content = query.data?.content
  if (query.isError || content === undefined) {
    return (
      <p className="mt-2 flex items-start gap-1.5 text-xs text-warning">
        <TriangleAlert className="mt-px size-3.5 shrink-0" aria-hidden />
        {t('pdp.rollback.previewUnavailable', { n: target.revision })}
      </p>
    )
  }
  // A failed baseline read is NOT "nothing is active". Saying "there is nothing to
  // compare against" when the truth is "we could not find out" is the same lie the
  // rest of this screen exists to remove, so the two states are kept apart.
  if (baseQuery.isError || !baseQuery.data) {
    return (
      <p className="mt-2 flex items-start gap-1.5 text-xs text-warning">
        <TriangleAlert className="mt-px size-3.5 shrink-0" aria-hidden />
        {t('pdp.rollback.previewBaseUnavailable', { n: target.revision })}
      </p>
    )
  }
  const authored = baseQuery.data.authored
  // Nothing selected yet, or the selected revision IS the target: show the source
  // itself rather than a diff against an invented empty document.
  const base =
    authored.present &&
    authored.content !== undefined &&
    authored.revision !== target.revision
      ? authored
      : undefined
  if (!base) {
    return (
      <div className="mt-2">
        <p className="mb-1.5 text-xs text-muted-foreground">
          {t('pdp.rollback.previewNoActive', { n: target.revision })}
        </p>
        <CodeEditor
          value={content}
          onChange={() => {}}
          language={language}
          ariaLabel={t('pdp.rollback.previewLabel', { n: target.revision })}
          readOnly
          height="12rem"
        />
      </div>
    )
  }
  return (
    <div className="mt-2">
      <CodeDiff
        original={base.content ?? ''}
        modified={content}
        language={language}
        originalLabel={t('pdp.diff.activeLabel', { n: base.revision ?? 0 })}
        modifiedLabel={t('pdp.rollback.previewLabel', { n: target.revision })}
        height="14rem"
      />
    </div>
  )
}

/** Short digest of a contributing surface — presence, revision and a truncated
 *  sha256. Content is deliberately absent for managed/adopted. */
function EnforcedSurfaces({
  engine,
  surfaces,
}: {
  engine: PdpEngine
  surfaces: SurfacesState
}) {
  const { t } = useTranslation('claudePolicy')

  if (engine === 'opa') {
    // There is no managed/adopted Rego surface, and nothing is enforced from this
    // process at all — claiming a union here would be a different half-truth.
    return (
      <p className="mt-2 text-xs text-muted-foreground">
        {t('pdp.diff.opaScope')}
      </p>
    )
  }

  // "We could not read the active policy" is NOT "no other surface is in force",
  // and this list is the one place an operator learns that what they edit is not
  // the whole enforced policy. Saying "none" here on a failed read tells them the
  // opposite of the truth it exists to tell, so the read state is rendered ONCE,
  // for the whole list, before any surface is described.
  if (surfaces.kind === 'loading') {
    return (
      <p className="mt-2 text-xs text-muted-foreground">
        {t('pdp.diff.surfacesLoading')}
      </p>
    )
  }
  if (surfaces.kind === 'unknown') {
    return (
      <p className="mt-2 flex items-start gap-1.5 text-xs text-warning">
        <TriangleAlert className="mt-px size-3.5 shrink-0" aria-hidden />
        {t('pdp.diff.surfacesUnknown')}
      </p>
    )
  }

  // Reached only with a policy the engine actually returned, so a false `present`
  // here is the engine's measured negative — the shape is always emitted.
  const { policy } = surfaces
  const describe = (s?: PdpActiveSurface) =>
    s?.present
      ? t('pdp.diff.surfacePresent', {
          n: s.revision ?? 0,
          digest: shortDigest(s.sha256),
        })
      : t('pdp.diff.surfaceNone')

  return (
    <div className="mt-2 flex flex-col gap-1 text-xs text-muted-foreground">
      <p>{t('pdp.diff.unionNote')}</p>
      <ul className="flex flex-col gap-0.5">
        <li>
          <span className="font-medium text-foreground">
            {t('pdp.diff.surfaceAuthored')}
          </span>{' '}
          — {describe(policy.authored)}
        </li>
        <li>
          <span className="font-medium text-foreground">
            {t('pdp.diff.surfaceManaged')}
          </span>{' '}
          — {describe(policy.managed)}
        </li>
        <li>
          <span className="font-medium text-foreground">
            {t('pdp.diff.surfaceAdopted')}
          </span>{' '}
          — {describe(policy.adopted)}
        </li>
      </ul>
      <p>{t('pdp.diff.contentWithheld')}</p>
    </div>
  )
}

/** sha256:<hex> → a short hex prefix, or an em dash when the engine sent none. */
function shortDigest(sha?: string): string {
  if (!sha) return '—'
  return sha.replace(/^sha256:/, '').slice(0, 12)
}

/** The result of the last publish/activation. `deferred` is a WARNING: the store
 *  selects the revision but the PREVIOUS policy is still deciding requests. */
function LifecycleResultPanel({ outcome }: { outcome: LifecycleOutcome }) {
  const { t } = useTranslation('claudePolicy')
  const { result } = outcome
  const live = result.live_activation
  const revision =
    outcome.kind === 'publish'
      ? outcome.result.revision
      : outcome.result.to_revision
  const from =
    outcome.kind === 'rollback' ? outcome.result.from_revision : undefined

  // By NAME for all three, with the unknown case named too. The old chain ended
  // in a bare else that read "Revision stored" — the calmest of the three — so an
  // activation state this console could not name was reported as the harmless one.
  const title =
    live === 'deferred'
      ? t('pdp.result.deferredTitle')
      : live === 'applied'
        ? t('pdp.result.appliedTitle')
        : live === 'not_applicable'
          ? t('pdp.result.versionedTitle')
          : t('pdp.result.unknownTitle')

  return (
    <SectionCard title={title}>
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <Badge variant="outline">
          {t('pdp.result.revisionBadge', { n: revision })}
        </Badge>
        {from !== undefined && from > 0 && (
          <Badge variant="outline">
            {t('pdp.result.fromBadge', { n: from })}
          </Badge>
        )}
        {live === 'deferred' && (
          <Badge variant="warning">{t('pdp.result.deferredBadge')}</Badge>
        )}
        {live === 'applied' && (
          <Badge variant="success">{t('pdp.result.appliedBadge')}</Badge>
        )}
        {live === 'not_applicable' && (
          <Badge variant="info">{t('pdp.result.versionedBadge')}</Badge>
        )}
        {live !== 'deferred' &&
          live !== 'applied' &&
          live !== 'not_applicable' && (
            <Badge variant="warning">{t('pdp.result.unknownBadge')}</Badge>
          )}
      </div>

      {live === 'deferred' ? (
        <>
          <p className="mt-2 flex items-start gap-1.5 text-xs text-warning">
            <TriangleAlert className="mt-px size-3.5 shrink-0" aria-hidden />
            {t('pdp.result.deferredBody', { n: revision })}
          </p>
          {/* No "reload now" button exists to offer: nothing reloads the PDP over
              HTTP. Name the three paths that DO re-activate it. */}
          <p className="mt-1.5 text-xs text-muted-foreground">
            {t('pdp.result.deferredRecovery')}
          </p>
        </>
      ) : live === 'applied' ? (
        <p className="mt-2 text-xs text-muted-foreground">
          {t('pdp.result.appliedBody', { n: revision })}
        </p>
      ) : live === 'not_applicable' ? (
        <p className="mt-2 text-xs text-muted-foreground">
          {t('pdp.result.versionedBody', { n: revision })}
        </p>
      ) : (
        // Not "versioned". We do not know what happened to the live engine, and
        // saying the calmest of the three would be the fabrication this panel
        // exists to prevent.
        <p className="mt-2 flex items-start gap-1.5 text-xs text-warning">
          <TriangleAlert className="mt-px size-3.5 shrink-0" aria-hidden />
          {t('pdp.result.unknownBody', { n: revision })}
        </p>
      )}

      {/* The server's `note` is deliberately NOT echoed here. It states the same
          outcome this panel already states, but only in English, so rendering both
          showed a non-English operator the message twice — once localized, once
          not. The field stays in the API for programmatic clients; the console is
          responsible for saying it in the reader's language. */}
    </SectionCard>
  )
}

/** The stored compile/validate gate for ONE revision. It is a single stored
 *  artifact, never a behavioral suite, so we render its name + detail verbatim and
 *  never a "passed/total" score. */
function GatePanel({
  engine,
  revision,
  query,
  baseUnknown,
}: {
  engine: PdpEngine
  revision?: number
  query: UseQueryResult<PdpTestStatus>
  /** The active-revision read is loading or failed, so `revision` being undefined
   *  means "unknown", not "none". */
  baseUnknown: boolean
}) {
  const { t } = useTranslation('claudePolicy')

  return (
    <SectionCard
      title={t('pdp.gate.title')}
      description={t('pdp.gate.subtitle')}
    >
      {revision === undefined && baseUnknown ? (
        // "We could not find out which revision is active" is NOT "no revision is
        // active". The gate result is per-revision, so without the active revision
        // this panel has nothing to report — say that, do not assert an empty history.
        <p className="flex items-start gap-1.5 text-xs text-warning">
          <TriangleAlert className="mt-px size-3.5 shrink-0" aria-hidden />
          {t('pdp.gate.activeRevisionUnknown')}
        </p>
      ) : revision === undefined ? (
        <p className="text-xs text-muted-foreground">
          {t('pdp.gate.noActiveRevision', { engine })}
        </p>
      ) : (
        <DeclaredSection query={query} what={t('pdp.gate.what')} live>
          {(status) => (
            <div className="flex flex-col gap-2">
              <Badge variant="outline">
                {t('pdp.gate.forRevision', {
                  n: status.revision ?? revision,
                })}
              </Badge>
              {!status.available ? (
                // No counters here, ever: `reason` is the whole truth we have.
                <p className="text-xs text-warning">
                  {status.reason ?? t('pdp.gate.unavailableNoReason')}
                </p>
              ) : status.results && status.results.length > 0 ? (
                <ul className="flex flex-col gap-1.5">
                  {status.results.map((r) => (
                    <li
                      key={r.name}
                      className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-surface px-2.5 py-1.5 text-xs"
                    >
                      <Badge variant={r.passed ? 'success' : 'danger'}>
                        {r.passed ? t('pdp.gate.passed') : t('pdp.gate.failed')}
                      </Badge>
                      <code className="font-mono">{r.name}</code>
                      <span className="min-w-0 text-muted-foreground">
                        {r.detail}
                      </span>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="text-xs text-muted-foreground">
                  {t('pdp.gate.noResults')}
                </p>
              )}
            </div>
          )}
        </DeclaredSection>
      )}
    </SectionCard>
  )
}

/** Version history GROUPED BY ENGINE. /pdp/versions is one flat, not-globally-
 *  sorted list whose only engine marker is `surface`, and cedar and opa can BOTH
 *  have an active revision at once — so there is one active badge per group, never
 *  a single find() over the flat list, and the React key is composite because
 *  cedar r1 and opa r1 collide on the number alone. */
function VersionHistory({
  items,
  canAuthor,
  onActivate,
}: {
  items: PdpRevision[]
  canAuthor: boolean
  onActivate: (v: PdpRevision) => void
}) {
  const { t } = useTranslation('claudePolicy')
  return (
    <div className="flex flex-col gap-3">
      {ENGINES.map((eng) => {
        const rows = items
          .filter((v) => v.surface === eng)
          .slice()
          .sort((a, b) => b.revision - a.revision)
        const groupTitle =
          eng === 'cedar'
            ? t('pdp.history.cedarTitle')
            : t('pdp.history.opaTitle')
        return (
          <SectionCard
            key={eng}
            title={
              <span className="flex items-center gap-2">
                <History className="size-4" aria-hidden />
                {groupTitle}
              </span>
            }
          >
            {rows.length === 0 ? (
              <p className="text-xs text-muted-foreground">
                {t('pdp.history.empty')}
              </p>
            ) : (
              <ol className="flex flex-col gap-1.5" aria-label={groupTitle}>
                {rows.map((v) => (
                  <li
                    // Composite: revision numbers are per-surface and collide.
                    key={`${v.surface}:${v.revision}`}
                    className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-surface px-2.5 py-1.5 text-xs"
                  >
                    <Badge variant="outline">
                      {t('pdp.history.revision', { n: v.revision })}
                    </Badge>
                    <span className="text-muted-foreground">
                      {v.author ?? '—'}
                    </span>
                    <span className="text-muted-foreground">
                      {v.created_at ?? ''}
                    </span>
                    <Badge variant={v.validated ? 'success' : 'warning'}>
                      {v.validated
                        ? t('pdp.history.validated')
                        : t('pdp.history.notValidated')}
                    </Badge>
                    {/* `active` is omitempty on a bool: absent means false, and
                        `=== false` would never be true. */}
                    {v.active ? (
                      // INFO for both engines, not success for cedar. The text
                      // has always said "selected in the store" — the colour did
                      // not, and green is this console's vocabulary for "in
                      // force". A store selection is not an enforcement fact, and
                      // this badge is what survives a reload: painting it green
                      // made the history assert exactly what the live-activation
                      // panel above may be reporting as deferred.
                      <Badge variant="info" className="ml-auto">
                        {eng === 'cedar'
                          ? t('pdp.history.activeCedar')
                          : t('pdp.history.selectedOpa')}
                      </Badge>
                    ) : (
                      canAuthor && (
                        <Button
                          variant="ghost"
                          size="sm"
                          className="ml-auto"
                          onClick={() => onActivate(v)}
                        >
                          {t('pdp.history.activate')}
                        </Button>
                      )
                    )}
                  </li>
                ))}
              </ol>
            )}
          </SectionCard>
        )
      })}
    </div>
  )
}

function DecisionPanel({ decision }: { decision: PdpDecision }) {
  const { t } = useTranslation('claudePolicy')
  // The OPA dry-run is a CONSTANT: the route returns allow:true for every Rego
  // document because the authored policy is not deployed to the sidecar and
  // nothing can be evaluated here. Painting that green as "Allowed" reported a
  // permission the console had never measured — a probe that answers the same
  // for every input has not measured anything. The engine now says so in
  // `evaluated`; `undefined` means an older engine, and for OPA that answer was
  // constant in every version, so the engine name settles it without guessing.
  const evaluated = decision.evaluated ?? decision.engine !== 'opa'
  return (
    <SectionCard title={t('pdp.decisionTitle')}>
      <div className="flex items-center gap-2">
        {evaluated ? (
          <Badge variant={decision.allow ? 'success' : 'danger'}>
            {decision.allow ? t('pdp.allow') : t('pdp.deny')}
          </Badge>
        ) : (
          <Badge variant="warning">{t('pdp.notEvaluated')}</Badge>
        )}
        <Badge variant="outline">{decision.engine}</Badge>
        <span className="text-xs text-muted-foreground">{decision.reason}</span>
      </div>
      {decision.chain && decision.chain.length > 0 && (
        <ol className="mt-2 flex flex-col gap-1">
          {decision.chain.map((c, i) => (
            <li key={i} className="flex items-center gap-2 text-xs">
              <Badge variant={c.effect === 'forbid' ? 'danger' : 'outline'}>
                {c.effect}
              </Badge>
              <code className="font-mono text-muted-foreground">{c.rule}</code>
              {c.matched && (
                <span className="text-warning">{t('pdp.matched')}</span>
              )}
            </li>
          ))}
        </ol>
      )}
      <p className="mt-2 text-xs text-muted-foreground">
        {decision.engine === 'opa'
          ? t('pdp.decisionFooterOpa')
          : t('pdp.decisionFooter')}
      </p>
    </SectionCard>
  )
}
