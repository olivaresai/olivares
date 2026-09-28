// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { useId, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { KvList, KvRow } from '@/components/ui/kv'
import { Skeleton } from '@/components/ui/skeleton'
import { CaveatNotice } from '@/features/_intel'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { formatInt } from '@/lib/format'
import { cn } from '@/lib/utils'
import { inventoryApi, inventoryKeys, type CollectionSelection } from './api'
import { EvidenceTime } from './observation-history'
import type {
  CollectionCoverage as Coverage,
  CollectionQualifiedSuccess,
  CollectionResult,
} from './types'

/** The handler's own bound on source_id and environment_ref (coverage_read.go). */
const SELECTOR_MAX_BYTES = 128
const REVISION = /^[1-9][0-9]*$/

const COVERAGE_POLICY = {
  staleTime: 0,
  retry: false as const,
  refetchOnWindowFocus: false,
  refetchOnReconnect: false,
}

/** The label carries the meaning; the tone only repeats it. */
const COVERAGE_TONE: Record<Coverage, BadgeVariant> = {
  complete: 'success',
  partial: 'warning',
  unavailable: 'warning',
  unsupported: 'neutral',
  unknown: 'neutral',
}

type SelectorName = 'source' | 'revision' | 'environment'
type Draft = Record<SelectorName, string>
type SelectorError = 'required' | 'tooLong' | 'revisionInvalid'
type DraftErrors = Partial<Record<SelectorName, SelectorError>>

const SELECTOR_ORDER: SelectorName[] = ['source', 'revision', 'environment']
const utf8 = new TextEncoder()

function textError(value: string): SelectorError | undefined {
  if (value === '') return 'required'
  if (utf8.encode(value).length > SELECTOR_MAX_BYTES) return 'tooLong'
  return undefined
}

/** The contract's selector rules, checked before any read: nonempty ids of at most
 *  128 bytes and a positive whole revision. A revision JavaScript cannot carry
 *  exactly is refused rather than rounded into another registration. */
function parseDraft(draft: Draft): {
  selection: CollectionSelection | null
  errors: DraftErrors
} {
  const source = draft.source.trim()
  const revision = draft.revision.trim()
  const environment = draft.environment.trim()
  const errors: DraftErrors = {}
  const sourceError = textError(source)
  if (sourceError) errors.source = sourceError
  if (revision === '') errors.revision = 'required'
  else if (!REVISION.test(revision) || !Number.isSafeInteger(Number(revision)))
    errors.revision = 'revisionInvalid'
  const environmentError = textError(environment)
  if (environmentError) errors.environment = environmentError
  if (Object.keys(errors).length > 0) return { selection: null, errors }
  return {
    selection: {
      source_id: source,
      source_revision: Number(revision),
      environment_ref: environment,
    },
    errors,
  }
}

/**
 * CollectionCoverage — the collection evidence of ONE opened source registration.
 *
 * The console has no read that lists opened registrations under the inventory
 * permission (the source roster is a separate, deployment-wide read and carries no
 * environment), so the operator names the three selectors. Nothing is read until
 * all three pass the contract's rules; each valid submission reads once.
 *
 * The answer is shown as the contract states it: coverage describes this
 * registration's query over its observed interval, the current run and the
 * historical last qualified success are separate sections, an unmatched selection
 * is `unknown` (never zero or none), and `has_more` is only a list flag.
 *
 * Tenant-owned: InventoryView mounts this inside the tenant-keyed estate, so a
 * tenant switch retires the selection and its read.
 */
export function CollectionCoverage({ tenant }: { tenant: string | null }) {
  const { t } = useTranslation('inventory')
  const headingId = useId()
  const [draft, setDraft] = useState<Draft>({
    source: '',
    revision: '',
    environment: '',
  })
  const [errors, setErrors] = useState<DraftErrors>({})
  const [submitted, setSubmitted] = useState<{
    selection: CollectionSelection
    attempt: number
  } | null>(null)
  const formRef = useRef<HTMLFormElement>(null)

  const onSubmit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const parsed = parseDraft(draft)
    setErrors(parsed.errors)
    if (parsed.selection === null) {
      // An invalid selection answers nothing, so no earlier answer stays beside it.
      setSubmitted(null)
      const first = SELECTOR_ORDER.find((name) => parsed.errors[name])
      const control = first && formRef.current?.elements.namedItem(first)
      if (control instanceof HTMLInputElement) control.focus()
      return
    }
    const selection = parsed.selection
    setSubmitted((prev) => ({ selection, attempt: (prev?.attempt ?? 0) + 1 }))
  }

  const errorText = (error: SelectorError | undefined) =>
    error === undefined
      ? undefined
      : error === 'tooLong'
        ? t('coverage.form.tooLong', { max: SELECTOR_MAX_BYTES })
        : t(`coverage.form.${error}`)

  const selectorField = (
    name: SelectorName,
    label: string,
    hint: string,
    inputMode?: 'numeric',
  ) => (
    <Field
      label={label}
      description={hint}
      error={errorText(errors[name])}
      required
    >
      <Input
        mono
        name={name}
        value={draft[name]}
        onChange={(e) => {
          const value = e.target.value
          setDraft((d) => ({ ...d, [name]: value }))
        }}
        inputMode={inputMode}
        autoComplete="off"
        spellCheck={false}
        aria-required="true"
      />
    </Field>
  )

  return (
    <section
      aria-labelledby={headingId}
      className="flex min-w-0 flex-col gap-4"
    >
      <div className="flex flex-col gap-1">
        <h2 id={headingId} className="text-body font-semibold text-foreground">
          {t('coverage.title')}
        </h2>
        <p className="text-caption text-muted-foreground">
          {t('coverage.intro')}
        </p>
      </div>
      <form
        ref={formRef}
        noValidate
        onSubmit={onSubmit}
        aria-label={t('coverage.form.legend')}
        className="grid items-start gap-3 md:grid-cols-3"
      >
        {selectorField(
          'source',
          t('coverage.form.sourceId'),
          t('coverage.form.sourceIdHint'),
        )}
        {selectorField(
          'revision',
          t('coverage.form.sourceRevision'),
          t('coverage.form.sourceRevisionHint'),
          'numeric',
        )}
        {selectorField(
          'environment',
          t('coverage.form.environmentRef'),
          t('coverage.form.environmentRefHint'),
        )}
        <div className="md:col-span-3">
          <Button type="submit" size="sm">
            {t('coverage.form.submit')}
          </Button>
        </div>
      </form>
      {submitted === null ? (
        <p className="text-body text-muted-foreground">{t('coverage.idle')}</p>
      ) : (
        <CoverageAnswer
          key={submitted.attempt}
          tenant={tenant}
          selection={submitted.selection}
        />
      )}
    </section>
  )
}

/** One read of one valid selection. Mounted per submission, so a query exists only
 *  once the selection is complete; the failure branch comes before the data branch,
 *  so a failed re-read is never shown as the earlier answer. */
function CoverageAnswer({
  tenant,
  selection,
}: {
  tenant: string | null
  selection: CollectionSelection
}) {
  const { t } = useTranslation('inventory')
  const query = useQuery({
    queryKey: inventoryKeys.collections(tenant, selection),
    queryFn: ({ signal }) =>
      inventoryApi.collections(selection, { tenant, signal }),
    ...COVERAGE_POLICY,
  })
  const reread = () => void query.refetch()

  if (query.isError) {
    return <CoverageFailure error={query.error} onRetry={reread} />
  }
  if (query.data === undefined) {
    return (
      <div role="status" aria-busy="true">
        <span className="sr-only">{t('coverage.loading')}</span>
        <Skeleton className="h-32 w-full" />
      </div>
    )
  }
  const item = query.data.items[0]!
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <CaveatNotice tone="info">{t('coverage.scopeNote')}</CaveatNotice>
      {query.data.has_more === true ? (
        <CaveatNotice>{t('coverage.hasMore')}</CaveatNotice>
      ) : null}
      <div className="grid min-w-0 gap-3 lg:grid-cols-2">
        <CurrentEvidence result={item.current} />
        <LastQualifiedSuccess success={item.last_qualified_success} />
      </div>
      <div>
        <Button
          type="button"
          variant="secondary"
          size="sm"
          onClick={reread}
          disabled={query.isFetching}
        >
          <RefreshCw
            aria-hidden="true"
            className={cn('size-3.5', query.isFetching && 'animate-spin')}
          />
          {t('coverage.refresh')}
        </Button>
      </div>
    </div>
  )
}

function EvidenceSection({
  title,
  children,
}: {
  title: string
  children: ReactNode
}) {
  const headingId = useId()
  return (
    <section
      aria-labelledby={headingId}
      className="min-w-0 rounded-md border border-border p-3"
    >
      <h3
        id={headingId}
        className="mb-2 text-caption font-semibold uppercase tracking-wide text-muted-foreground"
      >
        {title}
      </h3>
      {children}
    </section>
  )
}

function CoverageBadge({ value }: { value: Coverage }) {
  const { t } = useTranslation('inventory')
  return (
    <Badge variant={COVERAGE_TONE[value] ?? 'neutral'}>
      {t(`coverage.values.${value}`, { defaultValue: value })}
    </Badge>
  )
}

function Mono({ value }: { value: string | undefined }) {
  const { t } = useTranslation('inventory')
  if (value === undefined || value === '') {
    return (
      <span className="font-sans text-muted-foreground">
        {t('coverage.fields.notRecorded')}
      </span>
    )
  }
  return <span className="break-all">{value}</span>
}

/** Registration echo: what the engine says this answer is for. */
function RegistrationRows({
  of,
}: {
  of: Pick<
    CollectionResult,
    'source_id' | 'source_revision' | 'environment_ref'
  >
}) {
  const { t } = useTranslation('inventory')
  return (
    <>
      <KvRow label={t('coverage.fields.sourceId')} mono align="start">
        <Mono value={of.source_id} />
      </KvRow>
      <KvRow label={t('coverage.fields.sourceRevision')} mono>
        {String(of.source_revision)}
      </KvRow>
      <KvRow label={t('coverage.fields.environmentRef')} mono align="start">
        <Mono value={of.environment_ref} />
      </KvRow>
    </>
  )
}

/** Scope and instants shared by the current run and the qualified success. */
function ScopeRows({
  of,
}: {
  of: Partial<
    Pick<
      CollectionQualifiedSuccess,
      | 'scope_contract'
      | 'family'
      | 'requested_scope'
      | 'fulfilled_scope'
      | 'host_started_at'
      | 'host_finished_at'
      | 'producer_started_at'
      | 'producer_finished_at'
    >
  >
}) {
  const { t } = useTranslation('inventory')
  const notRecorded = t('coverage.fields.notRecorded')
  return (
    <>
      <KvRow label={t('coverage.fields.scopeContract')} mono align="start">
        <Mono value={of.scope_contract} />
      </KvRow>
      <KvRow label={t('coverage.fields.family')} mono align="start">
        <Mono value={of.family} />
      </KvRow>
      <KvRow label={t('coverage.fields.requestedScope')} mono align="start">
        <Mono value={of.requested_scope} />
      </KvRow>
      <KvRow label={t('coverage.fields.fulfilledScope')} mono align="start">
        <Mono value={of.fulfilled_scope} />
      </KvRow>
      <KvRow label={t('coverage.fields.hostStarted')} align="start">
        <EvidenceTime ts={of.host_started_at} unknown={notRecorded} />
      </KvRow>
      <KvRow label={t('coverage.fields.hostFinished')} align="start">
        <EvidenceTime ts={of.host_finished_at} unknown={notRecorded} />
      </KvRow>
      <KvRow label={t('coverage.fields.producerStarted')} align="start">
        <EvidenceTime ts={of.producer_started_at} unknown={notRecorded} />
      </KvRow>
      <KvRow label={t('coverage.fields.producerFinished')} align="start">
        <EvidenceTime ts={of.producer_finished_at} unknown={notRecorded} />
      </KvRow>
    </>
  )
}

function CurrentEvidence({ result }: { result: CollectionResult }) {
  const { t } = useTranslation('inventory')
  // No run: every figure below would be a zero default, not an observation.
  const hasRun = result.run_id !== ''
  // The host records the expected count when it closes the run (FinishRun).
  const closed = (result.host_finished_at ?? '') !== ''
  const emptyCompletion =
    result.coverage === 'complete' && closed && result.expected_count === 0
  return (
    <EvidenceSection title={t('coverage.current.title')}>
      <KvList>
        <KvRow label={t('coverage.current.coverage')}>
          <CoverageBadge value={result.coverage} />
        </KvRow>
        <RegistrationRows of={result} />
        <KvRow label={t('coverage.current.reason')} mono align="start">
          {result.reason === '' ? (
            <span className="font-sans text-muted-foreground">
              {t('coverage.current.reasonNone')}
            </span>
          ) : (
            <span className="break-all">{result.reason}</span>
          )}
        </KvRow>
        <KvRow label={t('coverage.current.projection')} align="start">
          <span>
            {t(`coverage.projections.${result.projection}`, {
              defaultValue: result.projection,
            })}
          </span>
          <span className="mt-0.5 block text-caption text-muted-foreground">
            {t('coverage.current.projectionHint')}
          </span>
        </KvRow>
      </KvList>
      {result.coverage === 'unknown' ? (
        <p className="mt-2 text-caption text-muted-foreground">
          {t('coverage.unknownNote')}
        </p>
      ) : null}
      {hasRun ? (
        <KvList className="mt-2">
          <KvRow label={t('coverage.current.runId')} mono align="start">
            <Mono value={result.run_id} />
          </KvRow>
          <KvRow label={t('coverage.current.runOrder')} mono>
            {formatInt(result.run_order)}
          </KvRow>
          <KvRow label={t('coverage.current.admitted')} mono>
            {formatInt(result.admitted_count)}
          </KvRow>
          <KvRow label={t('coverage.current.committed')} mono>
            {formatInt(result.committed_count)}
          </KvRow>
          <KvRow
            label={t('coverage.current.expected')}
            mono={closed}
            align={closed ? 'right' : 'start'}
          >
            {closed ? (
              formatInt(result.expected_count)
            ) : (
              <span className="text-muted-foreground">
                {t('coverage.current.expectedOpen')}
              </span>
            )}
          </KvRow>
          <ScopeRows of={result} />
          <KvRow label={t('coverage.fields.qualified')} align="start">
            <EvidenceTime
              ts={result.qualified_at}
              unknown={t('coverage.fields.notRecorded')}
            />
          </KvRow>
          {result.rejection_reason ? (
            <KvRow label={t('coverage.current.rejection')} mono align="start">
              <span className="break-all">{result.rejection_reason}</span>
            </KvRow>
          ) : null}
        </KvList>
      ) : (
        <p className="mt-2 text-body text-muted-foreground">
          {t('coverage.current.noRun')}
        </p>
      )}
      {emptyCompletion ? (
        <p className="mt-2 text-caption text-muted-foreground">
          {t('coverage.current.emptyComplete')}
        </p>
      ) : null}
    </EvidenceSection>
  )
}

function LastQualifiedSuccess({
  success,
}: {
  success: CollectionQualifiedSuccess | undefined
}) {
  const { t } = useTranslation('inventory')
  return (
    <EvidenceSection title={t('coverage.lastQualified.title')}>
      {success === undefined ? (
        <p className="text-body text-muted-foreground">
          {t('coverage.lastQualified.absent')}
        </p>
      ) : (
        <>
          <p className="text-caption text-muted-foreground">
            {t('coverage.lastQualified.note')}
          </p>
          <KvList className="mt-2">
            <KvRow label={t('coverage.current.runId')} mono align="start">
              <Mono value={success.run_id} />
            </KvRow>
            <KvRow label={t('coverage.fields.qualified')} align="start">
              <EvidenceTime
                ts={success.qualified_at}
                unknown={t('coverage.fields.notRecorded')}
              />
            </KvRow>
            <RegistrationRows of={success} />
            <KvRow label={t('coverage.current.expected')} mono>
              {formatInt(success.expected_count)}
            </KvRow>
            <ScopeRows of={success} />
          </KvList>
        </>
      )}
    </EvidenceSection>
  )
}

function CoverageFailure({
  error,
  onRetry,
}: {
  error: unknown
  onRetry: () => void
}) {
  const { t } = useTranslation(['inventory', 'errors', 'common'])
  if (error instanceof ApiError && error.status === 400) {
    // The selection itself was refused: re-reading it cannot help.
    return (
      <ErrorState
        title={t('inventory:coverage.error.invalidTitle')}
        description={t('inventory:coverage.error.invalid')}
        requestId={error.requestId}
      />
    )
  }
  if (error instanceof ApiError && error.status === 423) {
    return (
      <ErrorState
        title={t('inventory:coverage.error.suspendedTitle')}
        description={t('inventory:coverage.error.suspended')}
        retry={onRetry}
        requestId={error.requestId}
      />
    )
  }
  if (error instanceof ApiError && error.isStepUpRequired) {
    return <StepUpRequiredState action="generic" onElevated={onRetry} />
  }
  if (error instanceof ApiError && error.isForbidden) {
    return (
      <div className="flex flex-col items-center gap-3">
        <ForbiddenState
          title={t('errors:forbidden.title')}
          description={t('errors:forbidden.description')}
        />
        <Button type="button" variant="secondary" size="sm" onClick={onRetry}>
          {t('common:actions.retry')}
        </Button>
      </div>
    )
  }
  const isNetwork = error instanceof NetworkError
  return (
    <ErrorState
      title={
        isNetwork ? t('errors:network.title') : t('errors:serverError.title')
      }
      description={
        isNetwork
          ? t('errors:network.description')
          : t('errors:serverError.description')
      }
      retry={onRetry}
      requestId={error instanceof ApiError ? error.requestId : undefined}
    />
  )
}
