// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Download, Plus, Trash2 } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
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
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import type { TenantRequestOptions } from '@/lib/api/client'
import {
  isProtocolUnknown,
  protocolErrorCode,
  protocolVerdictOfError,
  runProtocolBindingSpecCreate,
} from './api'
import {
  listProtocolLocalResourceCatalog,
  protocolEndpointOptions,
  type ProtocolLocalResourceCatalog,
} from './catalog'
import {
  buildProtocolComposerExport,
  buildProtocolBindingSpecInput,
  defaultProtocolMapping,
  defaultProtocolComposerDraft,
  isPinnedVersionRef,
  protocolComposerMappingIssues,
  protocolMappingCatalogRoutes,
  protocolMappingCoverage,
  protocolMappingTargetsForSource,
  protocolMappingTransforms,
  protocolMappingOptions,
  protocolPlanDiff,
  protocolPlanMatchesApplied,
  PROTOCOL_MAPPING_SCHEMA_V1,
  successorProtocolComposerDraft,
  versionForProtocol,
} from './model'
import { BrokenNotice, ProtocolVerdictBadge, UnknownNotice } from './status'
import type {
  BindingDirection,
  BindingLocalKind,
  BindingProtocol,
  ProtocolBindingSpecPlan,
  ProtocolBindingSpecResult,
  ProtocolBindingSpec,
  ProtocolComposerDraft,
  ProtocolMappingCardinality,
  ProtocolMappingTransform,
  ProtocolSpecApplyOutcome,
} from './types'

const STEPS = [
  'connection',
  'resources',
  'mapping',
  'governance',
  'review',
] as const
const CARDINALITIES: ProtocolMappingCardinality[] = [
  'one_to_one',
  'one_to_many',
  'many_to_one',
]
const DIRECTIONS: BindingDirection[] = ['inbound', 'outbound', 'bidirectional']
const LOCAL_KINDS: BindingLocalKind[] = [
  'work_item',
  'agent',
  'model',
  'channel',
]

export interface ProtocolComposerPermissionPreview {
  createDraft: boolean | null
  activate: boolean | null
  localRead: Record<BindingLocalKind, boolean | null>
}

const UNKNOWN_PERMISSION_PREVIEW: ProtocolComposerPermissionPreview = {
  createDraft: null,
  activate: null,
  localRead: {
    work_item: null,
    agent: null,
    model: null,
    channel: null,
  },
}

type FlowPhase =
  | 'editing'
  | 'validating'
  | 'planning'
  | 'planned'
  | 'applying'
  | 'applied'
  | 'broken'
  | 'unknown'
  | 'failed'

function composerDraftError(draft: ProtocolComposerDraft): string | null {
  if (!draft.bindingKey.trim()) return 'bindingKey'
  if (!Number.isSafeInteger(draft.generation) || draft.generation < 1)
    return 'generation'
  if (!isPinnedVersionRef(draft.protocolVersion)) return 'protocolVersion'
  if (!draft.peerAuthority.trim()) return 'peerAuthority'
  if (!draft.localRef.trim()) return 'localRef'
  if (!draft.remoteResourceKind.trim()) return 'remoteResourceKind'
  if (!draft.remoteResourceRef.trim()) return 'remoteResourceRef'
  if (draft.mappingSchema.trim() !== PROTOCOL_MAPPING_SCHEMA_V1)
    return 'mappingSchema'
  if (
    draft.mapping.length === 0 ||
    draft.mapping.some((rule) => !rule.source.trim() || !rule.target.trim())
  )
    return 'mapping'
  if (protocolComposerMappingIssues(draft).length > 0) return 'mapping'
  if (
    draft.knownLosses.some(
      (loss) =>
        !loss.field.trim() ||
        !loss.reason_code.trim() ||
        (loss.accepted && !loss.acceptance_ref?.trim()),
    )
  )
    return 'losses'
  if (!draft.permissionProfileRef.trim()) return 'permissionProfile'
  return null
}

export function ProtocolComposerDialog({
  open,
  workspaceId,
  request,
  onOpenChange,
  onCreated,
  catalogSpecs = [],
  catalogSpecsComplete = false,
  catalogScope = '',
  permissions,
}: {
  open: boolean
  workspaceId: string
  request: TenantRequestOptions
  onOpenChange: (open: boolean) => void
  onCreated: (result: ProtocolBindingSpecResult) => void
  catalogSpecs?: ProtocolBindingSpec[]
  catalogSpecsComplete?: boolean
  catalogScope?: string
  permissions?: ProtocolComposerPermissionPreview
}) {
  const { t } = useTranslation('protocolBindings')
  const [step, setStep] = useState(0)
  const [draft, setDraft] = useState(defaultProtocolComposerDraft)
  const [phase, setPhase] = useState<FlowPhase>('editing')
  const [validation, setValidation] = useState<ProtocolBindingSpecPlan | null>(
    null,
  )
  const [plan, setPlan] = useState<ProtocolBindingSpecPlan | null>(null)
  const [outcome, setOutcome] = useState<ProtocolSpecApplyOutcome | null>(null)
  const [errorCode, setErrorCode] = useState<string | null>(null)
  const [operation] = useState(() => ({
    request,
    intentKey: crypto.randomUUID(),
  }))
  const [generationMode, setGenerationMode] = useState<'initial' | 'successor'>(
    'initial',
  )
  const permissionPreview = permissions ?? UNKNOWN_PERMISSION_PREVIEW
  const activeSpecs = useMemo(
    () => catalogSpecs.filter((spec) => spec.state === 'active'),
    [catalogSpecs],
  )
  const endpointOptions = useMemo(
    () => protocolEndpointOptions(activeSpecs, draft.protocol),
    [activeSpecs, draft.protocol],
  )

  const input = useMemo(
    () => buildProtocolBindingSpecInput(workspaceId, draft),
    [workspaceId, draft],
  )
  const formError = composerDraftError(draft)

  const change = <K extends keyof ProtocolComposerDraft>(
    key: K,
    value: ProtocolComposerDraft[K],
  ) => {
    setDraft((current) => ({ ...current, [key]: value }))
    setValidation(null)
    setPlan(null)
    setOutcome(null)
    setErrorCode(null)
    setPhase('editing')
  }

  const replaceDraft = (value: ProtocolComposerDraft) => {
    setDraft(value)
    setValidation(null)
    setPlan(null)
    setOutcome(null)
    setErrorCode(null)
    setPhase('editing')
  }

  const inspect = async () => {
    if (formError) {
      setErrorCode(`composer_${formError}`)
      setPhase('failed')
      return
    }
    setErrorCode(null)
    setPhase('validating')
    try {
      const checked = await runProtocolBindingSpecCreate(
        input,
        'validate',
        operation.request,
      )
      setValidation(checked)
      if (checked.verdict === 'UNKNOWN') {
        setPhase('unknown')
        return
      }
      if (checked.verdict !== 'CLEAN') {
        setPhase('broken')
        return
      }
      setPhase('planning')
      const planned = await runProtocolBindingSpecCreate(
        input,
        'plan',
        operation.request,
      )
      setPlan(planned)
      if (
        planned.operation !== 'draft' ||
        planned.workspace_id !== input.workspace_id ||
        planned.generation !== input.generation ||
        (planned.prior_active_id ?? '') !== (input.supersedes_id ?? '')
      ) {
        setErrorCode('plan_input_mismatch')
        setPhase('broken')
      } else if (planned.verdict === 'UNKNOWN') setPhase('unknown')
      else if (planned.verdict === 'BROKEN' || !planned.plan_hash)
        setPhase('broken')
      else setPhase('planned')
    } catch (error) {
      setErrorCode(protocolErrorCode(error))
      if (isProtocolUnknown(error)) setPhase('unknown')
      else if (protocolVerdictOfError(error) === 'BROKEN') setPhase('broken')
      else setPhase('failed')
    }
  }

  const apply = async () => {
    if (!plan?.plan_hash || !operation.intentKey) return
    setPhase('applying')
    try {
      const applied = await runProtocolBindingSpecCreate(
        input,
        'apply',
        operation.request,
        {
          idempotencyKey: operation.intentKey,
          planHash: plan.plan_hash,
        },
      )
      setOutcome(applied)
      if (applied.result.verdict === 'UNKNOWN') {
        setPhase('unknown')
      } else if (
        applied.result.verdict !== 'CLEAN' ||
        applied.result.spec?.state !== 'draft' ||
        !protocolPlanMatchesApplied(plan, applied.result)
      ) {
        setErrorCode(
          protocolPlanMatchesApplied(plan, applied.result)
            ? applied.result.code
            : 'apply_plan_mismatch',
        )
        setPhase('broken')
      } else {
        setPhase('applied')
        onCreated(applied.result)
      }
    } catch (error) {
      setErrorCode(protocolErrorCode(error))
      if (isProtocolUnknown(error)) setPhase('unknown')
      else if (protocolVerdictOfError(error) === 'BROKEN') setPhase('broken')
      else setPhase('failed')
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] max-w-4xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t('composer.title')}</DialogTitle>
          <DialogDescription>{t('composer.description')}</DialogDescription>
        </DialogHeader>

        <ol
          className="grid grid-cols-5 gap-1"
          aria-label={t('composer.progress')}
        >
          {STEPS.map((name, index) => (
            <li key={name}>
              <button
                type="button"
                className="w-full rounded-md border border-border px-2 py-2 text-left text-xs disabled:opacity-50"
                aria-current={step === index ? 'step' : undefined}
                disabled={index > step + 1 || phase === 'applying'}
                onClick={() => setStep(index)}
              >
                <span className="block font-mono text-muted-foreground">
                  {index + 1}
                </span>
                <span className="hidden sm:block">
                  {t(`composer.steps.${name}`)}
                </span>
              </button>
            </li>
          ))}
        </ol>

        <div className="min-h-80 space-y-4 py-2">
          {step === 0 ? (
            <ConnectionStep
              draft={draft}
              change={change}
              generationMode={generationMode}
              setGenerationMode={(mode) => {
                setGenerationMode(mode)
                replaceDraft(defaultProtocolComposerDraft())
              }}
              activeSpecs={activeSpecs}
              catalogSpecsComplete={catalogSpecsComplete}
              endpointOptions={endpointOptions}
              replaceDraft={replaceDraft}
            />
          ) : null}
          {step === 1 ? (
            <ResourcesStep
              draft={draft}
              change={change}
              workspaceId={workspaceId}
              request={operation.request}
              catalogScope={catalogScope}
              catalogAllowed={
                permissionPreview.localRead[draft.localKind] === true
              }
            />
          ) : null}
          {step === 2 ? <MappingStep draft={draft} change={change} /> : null}
          {step === 3 ? (
            <GovernanceStep
              draft={draft}
              change={change}
              permissions={permissionPreview}
              knownPermissionProfiles={Array.from(
                new Set(
                  activeSpecs
                    .filter(
                      (spec) =>
                        spec.protocol === draft.protocol &&
                        spec.direction === draft.direction &&
                        spec.local_kind === draft.localKind,
                    )
                    .map((spec) => spec.permission_profile_ref),
                ),
              )}
            />
          ) : null}
          {step === 4 ? (
            <ReviewStep
              workspaceId={workspaceId}
              draft={draft}
              validation={validation}
              plan={plan}
              outcome={outcome}
              intentKey={operation.intentKey}
            />
          ) : null}

          {phase === 'validating' ? (
            <p role="status" className="text-sm text-muted-foreground">
              {t('composer.flow.validating')}
            </p>
          ) : null}
          {phase === 'planning' ? (
            <p role="status" className="text-sm text-muted-foreground">
              {t('composer.flow.planning')}
            </p>
          ) : null}
          {phase === 'applying' ? (
            <p role="status" className="text-sm text-muted-foreground">
              {t('composer.flow.applying')}
            </p>
          ) : null}
          {phase === 'unknown' ? <UnknownNotice code={errorCode} /> : null}
          {phase === 'broken' ? <BrokenNotice code={errorCode} /> : null}
          {phase === 'failed' ? (
            <BrokenNotice code={errorCode}>
              {t('composer.flow.failed')}
            </BrokenNotice>
          ) : null}
          {phase === 'applied' && outcome ? (
            <div
              role="status"
              className="rounded-md border border-success-line bg-success-soft p-3 text-sm text-success"
            >
              <p className="font-medium">{t('composer.flow.created')}</p>
              <p className="mt-1 text-xs">
                {outcome.result.spec.validation.verdict === 'CLEAN' &&
                outcome.result.spec.validation.observed_at
                  ? t('composer.flow.createdVerified')
                  : t('composer.flow.createdUnverified')}
              </p>
              {outcome.replayed ? (
                <p className="mt-1 text-xs">{t('outcome.replayed')}</p>
              ) : null}
            </div>
          ) : null}
        </div>

        <DialogFooter className="items-center sm:justify-between">
          <p className="mr-auto font-mono text-[11px] text-muted-foreground">
            {t('composer.intentKey')}: {operation.intentKey || '—'}
          </p>
          {step > 0 && phase !== 'applied' ? (
            <Button
              type="button"
              variant="outline"
              disabled={
                phase === 'validating' ||
                phase === 'planning' ||
                phase === 'applying'
              }
              onClick={() => setStep((current) => current - 1)}
            >
              {t('common.back')}
            </Button>
          ) : null}
          {step < STEPS.length - 1 ? (
            <Button
              type="button"
              onClick={() => setStep((current) => current + 1)}
            >
              {t('common.next')}
            </Button>
          ) : phase === 'planned' ? (
            <Button type="button" onClick={() => void apply()}>
              {t('composer.flow.createDraft')}
            </Button>
          ) : phase === 'applying' ||
            phase === 'applied' ? null : plan?.plan_hash &&
            (phase === 'failed' || phase === 'unknown') ? (
            <Button type="button" onClick={() => void apply()}>
              {t('composer.flow.retrySameKey')}
            </Button>
          ) : (
            <Button type="button" onClick={() => void inspect()}>
              {t('composer.flow.inspect')}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

type ChangeDraft = <K extends keyof ProtocolComposerDraft>(
  key: K,
  value: ProtocolComposerDraft[K],
) => void

function ConnectionStep({
  draft,
  change,
  generationMode,
  setGenerationMode,
  activeSpecs,
  catalogSpecsComplete,
  endpointOptions,
  replaceDraft,
}: {
  draft: ProtocolComposerDraft
  change: ChangeDraft
  generationMode: 'initial' | 'successor'
  setGenerationMode: (mode: 'initial' | 'successor') => void
  activeSpecs: ProtocolBindingSpec[]
  catalogSpecsComplete: boolean
  endpointOptions: ReturnType<typeof protocolEndpointOptions>
  replaceDraft: (draft: ProtocolComposerDraft) => void
}) {
  const { t } = useTranslation('protocolBindings')
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field
        label={t('composer.generationMode.label')}
        description={t('composer.generationMode.help')}
        required
        className="sm:col-span-2"
      >
        {(props) => (
          <Select
            value={generationMode}
            onValueChange={(value) =>
              setGenerationMode(value as 'initial' | 'successor')
            }
          >
            <SelectTrigger {...props}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="initial">
                {t('composer.generationMode.initial')}
              </SelectItem>
              <SelectItem value="successor">
                {t('composer.generationMode.successor')}
              </SelectItem>
            </SelectContent>
          </Select>
        )}
      </Field>
      {generationMode === 'successor' ? (
        <Field
          label={t('composer.generationMode.predecessor')}
          description={
            catalogSpecsComplete
              ? t('composer.generationMode.predecessorHelp')
              : t('composer.generationMode.partialCatalog')
          }
          required
          className="sm:col-span-2"
        >
          {(props) => (
            <Select
              value={draft.supersedesId || undefined}
              onValueChange={(value) => {
                const predecessor = activeSpecs.find(
                  (spec) => spec.id === value,
                )
                if (predecessor)
                  replaceDraft(successorProtocolComposerDraft(predecessor))
              }}
            >
              <SelectTrigger {...props}>
                <SelectValue
                  placeholder={t('composer.generationMode.selectPredecessor')}
                />
              </SelectTrigger>
              <SelectContent>
                {activeSpecs.map((spec) => (
                  <SelectItem key={spec.id} value={spec.id}>
                    {spec.binding_key} · {t('fields.generation')}{' '}
                    {spec.generation} · {spec.protocol.toUpperCase()}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </Field>
      ) : null}
      <Field label={t('fields.bindingKey')} required>
        <Input
          readOnly={generationMode === 'successor'}
          value={draft.bindingKey}
          onChange={(e) => change('bindingKey', e.target.value)}
        />
      </Field>
      <Field label={t('fields.generation')} required>
        <Input type="number" min={1} readOnly value={draft.generation} />
      </Field>
      <Field label={t('fields.protocol')} required>
        {(props) => (
          <Select
            value={draft.protocol}
            onValueChange={(value) => {
              const protocol = value as BindingProtocol
              const direction =
                protocol === 'mcp' ? 'outbound' : draft.direction
              change('protocol', protocol)
              change('protocolVersion', versionForProtocol(protocol))
              change('direction', direction)
              change(
                'remoteResourceKind',
                protocol === 'a2a' ? 'agent' : 'tasks',
              )
              change(
                'mapping',
                defaultProtocolMapping(protocol, direction, draft.localKind),
              )
            }}
          >
            <SelectTrigger {...props}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="a2a">A2A</SelectItem>
              <SelectItem value="mcp">MCP</SelectItem>
            </SelectContent>
          </Select>
        )}
      </Field>
      <Field
        label={t('composer.endpointCatalog.label')}
        description={t('composer.endpointCatalog.help')}
        className="sm:col-span-2"
      >
        {(props) => (
          <Select
            value={undefined}
            onValueChange={(value) => {
              const endpoint = endpointOptions.find(
                (candidate) => candidate.id === value,
              )
              if (!endpoint) return
              change('peerAuthority', endpoint.peerAuthority)
              change('remoteResourceKind', endpoint.remoteResourceKind)
              change('remoteResourceRef', endpoint.remoteResourceRef)
            }}
          >
            <SelectTrigger {...props}>
              <SelectValue
                placeholder={
                  endpointOptions.length > 0
                    ? t('composer.endpointCatalog.select')
                    : t('composer.endpointCatalog.none')
                }
              />
            </SelectTrigger>
            <SelectContent>
              {endpointOptions.map((endpoint) => (
                <SelectItem key={endpoint.id} value={endpoint.id}>
                  {endpoint.label} · {endpoint.detail}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </Field>
      <Field
        label={t('fields.protocolVersion')}
        description={t('composer.pinnedVersionHelp')}
        error={
          isPinnedVersionRef(draft.protocolVersion)
            ? undefined
            : t('composer.pinnedVersionError')
        }
        required
      >
        <Input
          value={draft.protocolVersion}
          onChange={(e) => change('protocolVersion', e.target.value)}
        />
      </Field>
      <Field label={t('fields.direction')} required>
        {(props) => (
          <Select
            value={draft.direction}
            onValueChange={(value) => {
              const direction = value as BindingDirection
              change('direction', direction)
              change(
                'mapping',
                defaultProtocolMapping(
                  draft.protocol,
                  direction,
                  draft.localKind,
                ),
              )
            }}
          >
            <SelectTrigger {...props}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {DIRECTIONS.filter(
                (direction) =>
                  draft.protocol === 'a2a' || direction === 'outbound',
              ).map((direction) => (
                <SelectItem key={direction} value={direction}>
                  {t(`direction.${direction}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </Field>
      <Field
        label={t('fields.peerAuthority')}
        description={t('composer.peerHelp')}
        required
        className="sm:col-span-2"
      >
        <Input
          value={draft.peerAuthority}
          onChange={(e) => change('peerAuthority', e.target.value)}
        />
      </Field>
    </div>
  )
}

function ResourcesStep({
  draft,
  change,
  workspaceId,
  request,
  catalogScope,
  catalogAllowed,
}: {
  draft: ProtocolComposerDraft
  change: ChangeDraft
  workspaceId: string
  request: TenantRequestOptions
  catalogScope: string
  catalogAllowed: boolean
}) {
  const { t } = useTranslation('protocolBindings')
  const catalogKey = `${catalogScope}:${draft.localKind}:${workspaceId}`
  const [catalogSnapshot, setCatalogSnapshot] = useState<{
    key: string
    state: 'ready' | 'unavailable'
    catalog: ProtocolLocalResourceCatalog | null
  } | null>(null)
  const currentSnapshot =
    catalogSnapshot?.key === catalogKey ? catalogSnapshot : null
  const catalog = currentSnapshot?.catalog ?? null
  const catalogState = !catalogAllowed
    ? 'unavailable'
    : (currentSnapshot?.state ?? 'loading')

  useEffect(() => {
    if (!catalogAllowed) return
    const controller = new AbortController()
    let current = true
    void listProtocolLocalResourceCatalog(
      draft.localKind,
      workspaceId,
      request,
      controller.signal,
    )
      .then((result) => {
        if (!current) return
        setCatalogSnapshot({
          key: catalogKey,
          state: result.available ? 'ready' : 'unavailable',
          catalog: result,
        })
      })
      .catch(() => {
        if (current)
          setCatalogSnapshot({
            key: catalogKey,
            state: 'unavailable',
            catalog: null,
          })
      })
    return () => {
      current = false
      controller.abort()
    }
  }, [catalogAllowed, catalogKey, draft.localKind, request, workspaceId])

  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label={t('fields.localKind')} required>
        {(props) => (
          <Select
            value={draft.localKind}
            onValueChange={(value) => {
              const localKind = value as BindingLocalKind
              change('localKind', localKind)
              change('localRef', '')
              change(
                'mapping',
                defaultProtocolMapping(
                  draft.protocol,
                  draft.direction,
                  localKind,
                ),
              )
            }}
          >
            <SelectTrigger {...props}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {LOCAL_KINDS.map((kind) => (
                <SelectItem key={kind} value={kind}>
                  {t(`localKind.${kind}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </Field>
      <Field
        label={t('composer.resourceCatalog.label')}
        description={
          catalogState === 'loading'
            ? t('composer.resourceCatalog.loading')
            : catalogState === 'unavailable'
              ? t('composer.resourceCatalog.unavailable')
              : catalog?.hasMore
                ? t('composer.resourceCatalog.truncated')
                : t('composer.resourceCatalog.help')
        }
      >
        {(props) => (
          <Select
            value={
              catalog?.options.some((option) => option.id === draft.localRef)
                ? draft.localRef
                : undefined
            }
            disabled={catalogState !== 'ready' || catalog?.options.length === 0}
            onValueChange={(value) => change('localRef', value)}
          >
            <SelectTrigger {...props}>
              <SelectValue
                placeholder={
                  catalogState === 'loading'
                    ? t('composer.resourceCatalog.loading')
                    : t('composer.resourceCatalog.select')
                }
              />
            </SelectTrigger>
            <SelectContent>
              {catalog?.options.map((option) => (
                <SelectItem key={option.id} value={option.id}>
                  {option.label} · {option.detail}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </Field>
      <Field
        label={t('fields.localRef')}
        description={t('composer.resourceCatalog.exactFallback')}
        required
      >
        <Input
          value={draft.localRef}
          onChange={(e) => change('localRef', e.target.value)}
        />
      </Field>
      <Field label={t('fields.remoteResourceKind')} required>
        <Input
          value={draft.remoteResourceKind}
          onChange={(e) => change('remoteResourceKind', e.target.value)}
        />
      </Field>
      <Field label={t('fields.remoteResourceRef')} required>
        <Input
          value={draft.remoteResourceRef}
          onChange={(e) => change('remoteResourceRef', e.target.value)}
        />
      </Field>
      <div className="sm:col-span-2 rounded-md border border-border bg-muted/30 p-3 text-xs text-muted-foreground">
        {t('composer.resourceHelp')}
      </div>
    </div>
  )
}

function MappingStep({
  draft,
  change,
}: {
  draft: ProtocolComposerDraft
  change: ChangeDraft
}) {
  const { t } = useTranslation('protocolBindings')
  const options = protocolMappingOptions(
    draft.protocol,
    draft.direction,
    draft.localKind,
  )
  const routes = protocolMappingCatalogRoutes(
    draft.protocol,
    draft.direction,
    draft.localKind,
  )
  const updateRule = (
    index: number,
    patch: Partial<ProtocolComposerDraft['mapping'][number]>,
  ) =>
    change(
      'mapping',
      draft.mapping.map((rule, current) =>
        current === index ? { ...rule, ...patch } : rule,
      ),
    )
  const updateRulePair = (index: number, source: string, target: string) => {
    const cardinality: ProtocolMappingCardinality = 'one_to_one'
    const transform =
      protocolMappingTransforms(
        draft.protocol,
        draft.direction,
        draft.localKind,
        source,
        target,
        cardinality,
      )[0] ?? 'text'
    updateRule(index, { source, target, cardinality, transform })
  }
  const addRule = () => {
    const usedTargets = new Set(draft.mapping.map((rule) => rule.target))
    for (const route of routes) {
      const target = route.targets.find((item) => !usedTargets.has(item.name))
      const source =
        route.sources.find((item) => item.required) ?? route.sources[0]
      if (!target || !source) continue
      const cardinality: ProtocolMappingCardinality = 'one_to_one'
      const transform =
        protocolMappingTransforms(
          draft.protocol,
          draft.direction,
          draft.localKind,
          source.name,
          target.name,
          cardinality,
        )[0] ?? 'text'
      change('mapping', [
        ...draft.mapping,
        {
          source: source.name,
          target: target.name,
          cardinality,
          transform,
        },
      ])
      return
    }
  }
  const canAddRule = routes.some((route) =>
    route.targets.some(
      (target) => !draft.mapping.some((rule) => rule.target === target.name),
    ),
  )
  return (
    <div className="space-y-4">
      <Field label={t('fields.mappingSchema')} required>
        <Input readOnly value={draft.mappingSchema} />
      </Field>
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-sm font-medium">{t('composer.mapping.title')}</h3>
          <p className="text-xs text-muted-foreground">
            {t('composer.mapping.help')}
          </p>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!canAddRule}
          onClick={addRule}
        >
          <Plus className="size-4" /> {t('composer.mapping.add')}
        </Button>
      </div>
      {draft.mapping.map((rule, index) => {
        const targets = protocolMappingTargetsForSource(
          draft.protocol,
          draft.direction,
          draft.localKind,
          rule.source,
        ).filter(
          (target) =>
            target === rule.target ||
            !draft.mapping.some(
              (candidate, candidateIndex) =>
                candidateIndex !== index && candidate.target === target,
            ),
        )
        const cardinalities = CARDINALITIES.filter(
          (cardinality) =>
            protocolMappingTransforms(
              draft.protocol,
              draft.direction,
              draft.localKind,
              rule.source,
              rule.target,
              cardinality,
            ).length > 0,
        )
        const transforms = protocolMappingTransforms(
          draft.protocol,
          draft.direction,
          draft.localKind,
          rule.source,
          rule.target,
          rule.cardinality,
        )
        return (
          <div
            key={index}
            className="grid gap-3 rounded-md border border-border p-3 sm:grid-cols-2"
          >
            <Field label={t('fields.source')} required>
              {(props) => (
                <Select
                  value={rule.source}
                  onValueChange={(value) => {
                    const nextTargets = protocolMappingTargetsForSource(
                      draft.protocol,
                      draft.direction,
                      draft.localKind,
                      value,
                    ).filter(
                      (target) =>
                        target === rule.target ||
                        !draft.mapping.some(
                          (candidate, candidateIndex) =>
                            candidateIndex !== index &&
                            candidate.target === target,
                        ),
                    )
                    updateRulePair(
                      index,
                      value,
                      nextTargets.includes(rule.target)
                        ? rule.target
                        : (nextTargets[0] ?? ''),
                    )
                  }}
                >
                  <SelectTrigger {...props}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {options.sources.map((value) => (
                      <SelectItem key={value} value={value}>
                        {value}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
            <Field label={t('fields.target')} required>
              {(props) => (
                <Select
                  value={rule.target}
                  onValueChange={(value) =>
                    updateRulePair(index, rule.source, value)
                  }
                >
                  <SelectTrigger {...props}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {targets.map((value) => (
                      <SelectItem key={value} value={value}>
                        {value}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
            <Field label={t('fields.cardinality')} required>
              {(props) => (
                <Select
                  value={rule.cardinality}
                  onValueChange={(value) => {
                    const cardinality = value as ProtocolMappingCardinality
                    const nextTransforms = protocolMappingTransforms(
                      draft.protocol,
                      draft.direction,
                      draft.localKind,
                      rule.source,
                      rule.target,
                      cardinality,
                    )
                    updateRule(index, {
                      cardinality,
                      transform: nextTransforms.includes(rule.transform)
                        ? rule.transform
                        : (nextTransforms[0] ?? rule.transform),
                    })
                  }}
                >
                  <SelectTrigger {...props}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {cardinalities.map((value) => (
                      <SelectItem key={value} value={value}>
                        {t(`cardinality.${value}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
            <Field label={t('fields.transform')} required>
              {(props) => (
                <Select
                  value={rule.transform}
                  onValueChange={(value) =>
                    updateRule(index, {
                      transform: value as ProtocolMappingTransform,
                    })
                  }
                >
                  <SelectTrigger {...props}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {transforms.map((value) => (
                      <SelectItem key={value} value={value}>
                        {t(`transform.${value}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={draft.mapping.length === 1}
              onClick={() =>
                change(
                  'mapping',
                  draft.mapping.filter((_, current) => current !== index),
                )
              }
            >
              <Trash2 className="size-4" /> {t('common.remove')}
            </Button>
          </div>
        )
      })}
      <MappingCoverageMatrix draft={draft} change={change} />
    </div>
  )
}

function MappingCoverageMatrix({
  draft,
  change,
}: {
  draft: ProtocolComposerDraft
  change: ChangeDraft
}) {
  const { t } = useTranslation('protocolBindings')
  const coverage = protocolMappingCoverage(draft)
  return (
    <section
      className="overflow-hidden rounded-md border border-border"
      aria-label={t('composer.coverage.title')}
    >
      <div className="border-b border-border bg-muted/30 p-3">
        <h3 className="text-sm font-medium">{t('composer.coverage.title')}</h3>
        <p className="mt-1 text-xs text-muted-foreground">
          {t('composer.coverage.help')}
        </p>
      </div>
      <div className="max-h-72 overflow-auto">
        <table className="w-full text-left text-xs">
          <thead className="sticky top-0 bg-background text-muted-foreground">
            <tr>
              <th className="px-3 py-2 font-medium">
                {t('composer.coverage.route')}
              </th>
              <th className="px-3 py-2 font-medium">
                {t('composer.coverage.field')}
              </th>
              <th className="px-3 py-2 font-medium">
                {t('composer.coverage.result')}
              </th>
              <th className="px-3 py-2 font-medium">
                <span className="sr-only">{t('composer.coverage.action')}</span>
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {coverage.map((item) => {
              const hasLoss = draft.knownLosses.some(
                (loss) => loss.field === item.field,
              )
              const variant =
                item.status === 'mapped'
                  ? 'success'
                  : item.status === 'accepted_loss'
                    ? 'info'
                    : item.status === 'missing'
                      ? 'danger'
                      : item.status === 'declared_loss'
                        ? 'warning'
                        : 'neutral'
              return (
                <tr
                  key={`${item.direction}:${item.role}:${item.field}`}
                  className={item.required ? 'bg-warning-soft/20' : undefined}
                >
                  <td className="whitespace-nowrap px-3 py-2 text-muted-foreground">
                    {t(`direction.${item.direction}`)} ·{' '}
                    {t(`composer.coverage.${item.role}`)}
                  </td>
                  <td className="px-3 py-2 font-mono">
                    {item.field}
                    {item.required ? (
                      <span
                        className="ml-2 text-danger"
                        aria-label={t('composer.coverage.required')}
                      >
                        *
                      </span>
                    ) : null}
                  </td>
                  <td className="px-3 py-2">
                    <Badge variant={variant}>
                      {t(`composer.coverage.status.${item.status}`)}
                    </Badge>
                  </td>
                  <td className="px-3 py-2 text-right">
                    {item.status !== 'mapped' && !hasLoss ? (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() =>
                          change('knownLosses', [
                            ...draft.knownLosses,
                            {
                              field: item.field,
                              reason_code: '',
                              accepted: false,
                            },
                          ])
                        }
                      >
                        {t('composer.coverage.declareLoss')}
                      </Button>
                    ) : null}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function GovernanceStep({
  draft,
  change,
  permissions,
  knownPermissionProfiles,
}: {
  draft: ProtocolComposerDraft
  change: ChangeDraft
  permissions: ProtocolComposerPermissionPreview
  knownPermissionProfiles: string[]
}) {
  const { t } = useTranslation('protocolBindings')
  const mappingFields = Array.from(
    new Set(
      protocolMappingCatalogRoutes(
        draft.protocol,
        draft.direction,
        draft.localKind,
      ).flatMap((route) => [
        ...route.sources.map((item) => item.name),
        ...route.targets.map((item) => item.name),
      ]),
    ),
  )
  const updateLoss = (
    index: number,
    patch: Partial<ProtocolComposerDraft['knownLosses'][number]>,
  ) =>
    change(
      'knownLosses',
      draft.knownLosses.map((loss, current) =>
        current === index ? { ...loss, ...patch } : loss,
      ),
    )
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-sm font-medium">{t('composer.losses.title')}</h3>
          <p className="text-xs text-muted-foreground">
            {t('composer.losses.help')}
          </p>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={mappingFields.every((field) =>
            draft.knownLosses.some((loss) => loss.field === field),
          )}
          onClick={() =>
            change('knownLosses', [
              ...draft.knownLosses,
              {
                field:
                  mappingFields.find(
                    (field) =>
                      !draft.knownLosses.some((loss) => loss.field === field),
                  ) ?? '',
                reason_code: '',
                accepted: false,
              },
            ])
          }
        >
          <Plus className="size-4" /> {t('composer.losses.add')}
        </Button>
      </div>
      {draft.knownLosses.length === 0 ? (
        <p className="rounded-md border border-border p-3 text-xs text-muted-foreground">
          {t('composer.losses.none')}
        </p>
      ) : null}
      {draft.knownLosses.map((loss, index) => (
        <div
          key={index}
          className="grid gap-3 rounded-md border border-border p-3 sm:grid-cols-2"
        >
          <Field label={t('fields.lossField')} required>
            {(props) => (
              <Select
                value={loss.field}
                onValueChange={(value) => updateLoss(index, { field: value })}
              >
                <SelectTrigger {...props}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {mappingFields.map((value) => (
                    <SelectItem key={value} value={value}>
                      {value}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </Field>
          <Field label={t('fields.reasonCode')} required>
            <Input
              value={loss.reason_code}
              onChange={(e) =>
                updateLoss(index, { reason_code: e.target.value })
              }
            />
          </Field>
          <div className="flex items-center gap-2">
            <Checkbox
              id={`loss-${index}-accepted`}
              checked={loss.accepted}
              onCheckedChange={(checked) =>
                updateLoss(index, {
                  accepted: checked === true,
                  acceptance_ref:
                    checked === true ? loss.acceptance_ref : undefined,
                })
              }
            />
            <label htmlFor={`loss-${index}-accepted`} className="text-sm">
              {t('fields.lossAccepted')}
            </label>
          </div>
          <Field label={t('fields.acceptanceRef')} required={loss.accepted}>
            <Input
              disabled={!loss.accepted}
              value={loss.acceptance_ref ?? ''}
              onChange={(e) =>
                updateLoss(index, { acceptance_ref: e.target.value })
              }
            />
          </Field>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() =>
              change(
                'knownLosses',
                draft.knownLosses.filter((_, current) => current !== index),
              )
            }
          >
            <Trash2 className="size-4" /> {t('common.remove')}
          </Button>
        </div>
      ))}
      <Field
        label={t('fields.ruleRefs')}
        description={t('composer.ruleRefsHelp')}
      >
        <Textarea
          rows={3}
          value={draft.ruleRefsText}
          onChange={(e) => change('ruleRefsText', e.target.value)}
        />
      </Field>
      <Field label={t('fields.permissionProfile')} required>
        <Input
          value={draft.permissionProfileRef}
          onChange={(e) => change('permissionProfileRef', e.target.value)}
        />
      </Field>
      {knownPermissionProfiles.length > 0 ? (
        <Field
          label={t('composer.permissions.knownProfiles')}
          description={t('composer.permissions.knownProfilesHelp')}
        >
          {(props) => (
            <Select
              value={
                knownPermissionProfiles.includes(draft.permissionProfileRef)
                  ? draft.permissionProfileRef
                  : undefined
              }
              onValueChange={(value) => change('permissionProfileRef', value)}
            >
              <SelectTrigger {...props}>
                <SelectValue
                  placeholder={t('composer.permissions.selectProfile')}
                />
              </SelectTrigger>
              <SelectContent>
                {knownPermissionProfiles.map((value) => (
                  <SelectItem key={value} value={value}>
                    {value}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </Field>
      ) : null}
      <PermissionPreview draft={draft} permissions={permissions} />
    </div>
  )
}

function PermissionPreview({
  draft,
  permissions,
}: {
  draft: ProtocolComposerDraft
  permissions: ProtocolComposerPermissionPreview
}) {
  const { t } = useTranslation('protocolBindings')
  const rows = [
    {
      permission: 'sessions:protocol-binding:write',
      granted: permissions.createDraft,
    },
    {
      permission: 'sessions:protocol-binding:admin',
      granted: permissions.activate,
    },
    {
      permission:
        draft.localKind === 'work_item'
          ? 'sessions:work:read'
          : draft.localKind === 'agent'
            ? 'agent:read'
            : draft.localKind === 'model'
              ? 'models:catalog:read'
              : 'sessions:channel:read',
      granted: permissions.localRead[draft.localKind],
    },
  ]
  return (
    <section
      className="rounded-md border border-border p-3"
      aria-label={t('composer.permissions.title')}
    >
      <h3 className="text-sm font-medium">{t('composer.permissions.title')}</h3>
      <p className="mt-1 text-xs text-muted-foreground">
        {t('composer.permissions.help')}
      </p>
      <ul className="mt-3 space-y-2">
        {rows.map((row) => (
          <li
            key={row.permission}
            className="flex items-center justify-between gap-3 text-xs"
          >
            <code>{row.permission}</code>
            <Badge
              variant={
                row.granted === true
                  ? 'success'
                  : row.granted === false
                    ? 'warning'
                    : 'neutral'
              }
            >
              {t(
                row.granted === true
                  ? 'composer.permissions.granted'
                  : row.granted === false
                    ? 'composer.permissions.notGranted'
                    : 'composer.permissions.unknown',
              )}
            </Badge>
          </li>
        ))}
      </ul>
    </section>
  )
}

function ReviewStep({
  workspaceId,
  draft,
  validation,
  plan,
  outcome,
  intentKey,
}: {
  workspaceId: string
  draft: ProtocolComposerDraft
  validation: ProtocolBindingSpecPlan | null
  plan: ProtocolBindingSpecPlan | null
  outcome: ProtocolSpecApplyOutcome | null
  intentKey: string
}) {
  const { t } = useTranslation('protocolBindings')
  const exportJSON = () => {
    const documentValue = buildProtocolComposerExport(
      workspaceId,
      draft,
      validation,
      plan,
      outcome,
    )
    const blob = new Blob([JSON.stringify(documentValue, null, 2)], {
      type: 'application/json',
    })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    const filenameKey =
      draft.bindingKey
        .trim()
        .replace(/[^a-zA-Z0-9._-]+/g, '-')
        .slice(0, 96) || 'protocol-binding'
    link.download = `${filenameKey}-generation-${draft.generation}.json`
    document.body.appendChild(link)
    link.click()
    link.remove()
    URL.revokeObjectURL(url)
  }
  return (
    <div className="space-y-4">
      <div className="rounded-md border border-warning-line bg-warning-soft p-3 text-sm text-warning">
        <div className="flex gap-2">
          <AlertTriangle
            className="mt-0.5 size-4 shrink-0"
            aria-hidden="true"
          />
          <div>
            <p className="font-medium">
              {t('composer.review.unverifiedTitle')}
            </p>
            <p className="mt-1 text-xs">
              {t('composer.review.unverifiedBody')}
            </p>
          </div>
        </div>
      </div>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
        <dt className="text-muted-foreground">{t('fields.bindingKey')}</dt>
        <dd className="font-mono text-xs">{draft.bindingKey || '—'}</dd>
        <dt className="text-muted-foreground">{t('fields.protocol')}</dt>
        <dd>
          <Badge variant="outline">
            {draft.protocol.toUpperCase()} {draft.protocolVersion}
          </Badge>
        </dd>
        <dt className="text-muted-foreground">{t('fields.direction')}</dt>
        <dd>{t(`direction.${draft.direction}`)}</dd>
        <dt className="text-muted-foreground">{t('fields.peerAuthority')}</dt>
        <dd className="break-all font-mono text-xs">
          {draft.peerAuthority || '—'}
        </dd>
        <dt className="text-muted-foreground">
          {t('composer.review.resources')}
        </dt>
        <dd className="font-mono text-xs">
          {draft.localKind}:{draft.localRef || '—'} → {draft.remoteResourceKind}
          :{draft.remoteResourceRef || '—'}
        </dd>
        <dt className="text-muted-foreground">
          {t('composer.review.mappings')}
        </dt>
        <dd>{draft.mapping.length}</dd>
        <dt className="text-muted-foreground">{t('composer.review.losses')}</dt>
        <dd>{draft.knownLosses.length}</dd>
        <dt className="text-muted-foreground">{t('fields.currency')}</dt>
        <dd>
          <Badge variant="info">{t('currency.pinned')}</Badge>
        </dd>
        <dt className="text-muted-foreground">{t('composer.intentKey')}</dt>
        <dd className="break-all font-mono text-xs">{intentKey}</dd>
      </dl>
      <Button type="button" variant="outline" onClick={exportJSON}>
        <Download className="size-4" aria-hidden="true" />
        {t('composer.review.exportJSON')}
      </Button>
      {validation ? (
        <PlanSummary
          label={t('composer.review.validation')}
          plan={validation}
        />
      ) : null}
      {plan ? (
        <PlanSummary label={t('composer.review.plan')} plan={plan} />
      ) : null}
      {plan ? <PlanDiff plan={plan} applied={outcome?.result} /> : null}
      {outcome ? (
        <PlanSummary
          label={t('composer.review.result')}
          plan={outcome.result}
        />
      ) : null}
    </div>
  )
}

function PlanDiff({
  plan,
  applied,
}: {
  plan: ProtocolBindingSpecPlan
  applied?: ProtocolBindingSpecResult
}) {
  const { t } = useTranslation('protocolBindings')
  const rows = protocolPlanDiff(plan, applied)
  return (
    <section
      className="overflow-hidden rounded-md border border-border"
      aria-label={t('composer.diff.title')}
    >
      <div className="border-b border-border bg-muted/30 p-3">
        <h3 className="text-sm font-medium">{t('composer.diff.title')}</h3>
        <p className="mt-1 text-xs text-muted-foreground">
          {applied
            ? t('composer.diff.appliedHelp')
            : t('composer.diff.plannedHelp')}
        </p>
      </div>
      <dl className="grid grid-cols-[minmax(8rem,auto)_1fr] gap-x-3 gap-y-2 p-3 text-xs">
        {rows.map((row) => (
          <div key={row.field} className="contents">
            <dt className="text-muted-foreground">
              {t(`composer.diff.fields.${row.field}`)}
            </dt>
            <dd className="min-w-0">
              <p className="break-all font-mono">{row.planned || '—'}</p>
              {row.applied !== undefined ? (
                <div className="mt-1 flex items-center gap-2">
                  <Badge variant={row.matches ? 'success' : 'danger'}>
                    {t(
                      row.matches
                        ? 'composer.diff.match'
                        : 'composer.diff.mismatch',
                    )}
                  </Badge>
                  {!row.matches ? (
                    <code className="break-all">{row.applied || '—'}</code>
                  ) : null}
                </div>
              ) : null}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  )
}

function PlanSummary({
  label,
  plan,
}: {
  label: string
  plan: ProtocolBindingSpecPlan
}) {
  const { t } = useTranslation('protocolBindings')
  return (
    <section className="rounded-md border border-border p-3" aria-label={label}>
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-medium">{label}</h3>
        <ProtocolVerdictBadge verdict={plan.verdict} />
      </div>
      <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
        <dt className="text-muted-foreground">{t('fields.code')}</dt>
        <dd className="font-mono">{plan.code}</dd>
        <dt className="text-muted-foreground">{t('detail.validation')}</dt>
        <dd className="space-y-1">
          <ProtocolVerdictBadge verdict={plan.validation.verdict} />
          <p className="font-mono">{plan.validation.code}</p>
          <p className="text-muted-foreground">
            {plan.validation.observed_at ?? t('detail.notObserved')}
          </p>
        </dd>
        <dt className="text-muted-foreground">{t('fields.planHash')}</dt>
        <dd className="break-all font-mono">{plan.plan_hash || '—'}</dd>
        <dt className="text-muted-foreground">{t('fields.specHash')}</dt>
        <dd className="break-all font-mono">{plan.spec_hash || '—'}</dd>
        <dt className="text-muted-foreground">{t('fields.mappingHash')}</dt>
        <dd className="break-all font-mono">{plan.mapping_hash || '—'}</dd>
        <dt className="text-muted-foreground">{t('fields.lossesHash')}</dt>
        <dd className="break-all font-mono">{plan.losses_hash || '—'}</dd>
      </dl>
    </section>
  )
}
