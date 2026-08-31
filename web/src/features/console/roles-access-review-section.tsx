// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
/**
 * AccessReviewSection surfaces the AuthZEN reverse queries ("who can access
 * what?") and the sealed access-review evidence export. Searches are read-only
 * (no AAL3 step-up); the export is admin-tier and requires a hardware-bound step-up.
 * The backend remains the authority — this panel is a query interface only.
 */
import { useMutation } from '@tanstack/react-query'
import { ShieldCheck } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { AAL, RequireAssurance } from '@/features/identity/assurance'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  consoleApi,
  type AccessReviewPack,
  type AccessReviewRequest,
  type AuthZenEntityResult,
  type AuthZenSearchResponse,
} from './api'
import { FormError } from './roles-shared'

// --- main section ---------------------------------------------------------------

/**
 * AccessReviewSection exposes subject-search, resource-search, and sealed
 * access-review export. The permission gate is authz:read for searches and
 * authz:admin for the export tab (which also requires a hardware-bound step-up).
 */
export function AccessReviewSection() {
  const { t } = useTranslation('console')
  const { can } = useAuth()
  const canRead = can('authz:read')
  const canAdmin = can('authz:admin')

  if (!canRead) {
    return (
      <section className="flex flex-col gap-3">
        <div>
          <h2 className="text-base font-semibold text-foreground">
            {t('granular.accessReview.title')}
          </h2>
          <p className="max-w-2xl text-sm text-muted-foreground">
            {t('granular.accessReview.caption')}
          </p>
        </div>
        <EmptyState
          title={t('granular.accessReview.readOnlyNotice')}
          icon={<ShieldCheck />}
        />
      </section>
    )
  }

  return (
    <section className="flex flex-col gap-3">
      <div>
        <h2 className="text-base font-semibold text-foreground">
          {t('granular.accessReview.title')}
        </h2>
        <p className="max-w-2xl text-sm text-muted-foreground">
          {t('granular.accessReview.caption')}
        </p>
      </div>

      <Tabs defaultValue="subject">
        <TabsList>
          <TabsTrigger value="subject">
            {t('granular.accessReview.searchSubject')}
          </TabsTrigger>
          <TabsTrigger value="resource">
            {t('granular.accessReview.searchResource')}
          </TabsTrigger>
          {canAdmin && (
            <TabsTrigger value="export">
              {t('granular.accessReview.export')}
            </TabsTrigger>
          )}
        </TabsList>

        <TabsContent value="subject" className="pt-4">
          <SubjectSearchPanel />
        </TabsContent>

        <TabsContent value="resource" className="pt-4">
          <ResourceSearchPanel />
        </TabsContent>

        {canAdmin && (
          <TabsContent value="export" className="pt-4">
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <ExportPanel />
            </RequireAssurance>
          </TabsContent>
        )}
      </Tabs>
    </section>
  )
}

// --- subject search (who can do A on R?) ----------------------------------------

/** Variables for both fresh search and load-more calls. */
type SearchVars = { pageToken?: string; isNew: boolean }

function SubjectSearchPanel() {
  const { t } = useTranslation('console')
  const [resType, setResType] = useState('resource')
  const [resId, setResId] = useState('')
  const [actionName, setActionName] = useState('')
  const [results, setResults] = useState<AuthZenEntityResult[]>([])
  const [nextToken, setNextToken] = useState('')

  const mutation = useMutation<AuthZenSearchResponse, Error, SearchVars>({
    mutationFn: ({ pageToken }) =>
      consoleApi.searchSubjects({
        resource: { type: resType, id: resId.trim() || undefined },
        action: actionName.trim() ? { name: actionName.trim() } : undefined,
        page: pageToken ? { token: pageToken } : undefined,
      }),
    onSuccess: (data, { isNew }) => {
      if (isNew) {
        setResults(data.results)
      } else {
        setResults((prev) => [...prev, ...data.results])
      }
      setNextToken(data.page.next_token ?? '')
    },
  })

  const valid = resId.trim() !== '' && actionName.trim() !== ''

  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <Field
          label={t('granular.accessReview.resourceType')}
          htmlFor="ss-restype"
        >
          <Select value={resType} onValueChange={setResType}>
            <SelectTrigger id="ss-restype">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="resource">
                {t('granular.accessReview.resourceTypeResource')}
              </SelectItem>
              <SelectItem value="agent">
                {t('granular.accessReview.resourceTypeAgent')}
              </SelectItem>
              <SelectItem value="session">
                {t('granular.accessReview.resourceTypeSession')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>

        <Field
          label={t('granular.accessReview.resourceId')}
          htmlFor="ss-resid"
          description={t('granular.accessReview.resourceIdHint')}
          required
        >
          <Input
            id="ss-resid"
            value={resId}
            onChange={(e) => setResId(e.target.value)}
            mono
          />
        </Field>

        <Field
          label={t('granular.accessReview.actionName')}
          htmlFor="ss-action"
          description={t('granular.accessReview.actionNameHint')}
          required
        >
          <Input
            id="ss-action"
            value={actionName}
            onChange={(e) => setActionName(e.target.value)}
            mono
          />
        </Field>
      </div>

      <div>
        <Button
          onClick={() => mutation.mutate({ isNew: true })}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('granular.accessReview.run')}
        </Button>
      </div>

      {mutation.isError && (
        <p role="alert" className="text-sm text-danger">
          {t('granular.accessReview.loadError')}
        </p>
      )}

      {mutation.isSuccess && (
        <SearchResultsTable
          results={results}
          typeLabel={t('granular.accessReview.subjectType')}
          nextToken={nextToken}
          onLoadMore={() =>
            mutation.mutate({ pageToken: nextToken, isNew: false })
          }
          isPending={mutation.isPending}
        />
      )}
    </div>
  )
}

// --- resource search (what can S do?) -------------------------------------------

function ResourceSearchPanel() {
  const { t } = useTranslation('console')
  const [subjectType, setSubjectType] = useState('user')
  const [subjectId, setSubjectId] = useState('')
  const [actionName, setActionName] = useState('')
  const [results, setResults] = useState<AuthZenEntityResult[]>([])
  const [nextToken, setNextToken] = useState('')

  const mutation = useMutation<AuthZenSearchResponse, Error, SearchVars>({
    mutationFn: ({ pageToken }) =>
      consoleApi.searchResources({
        subject: { type: subjectType, id: subjectId.trim() || undefined },
        action: actionName.trim() ? { name: actionName.trim() } : undefined,
        page: pageToken ? { token: pageToken } : undefined,
      }),
    onSuccess: (data, { isNew }) => {
      if (isNew) {
        setResults(data.results)
      } else {
        setResults((prev) => [...prev, ...data.results])
      }
      setNextToken(data.page.next_token ?? '')
    },
  })

  const valid = subjectId.trim() !== '' && actionName.trim() !== ''

  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <Field
          label={t('granular.accessReview.subjectType')}
          htmlFor="rs-subtype"
        >
          <Select value={subjectType} onValueChange={setSubjectType}>
            <SelectTrigger id="rs-subtype">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="user">
                {t('granular.accessReview.subjectTypeUser')}
              </SelectItem>
              <SelectItem value="token">
                {t('granular.accessReview.subjectTypeToken')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>

        <Field
          label={t('granular.accessReview.subjectId')}
          htmlFor="rs-subid"
          description={t('granular.accessReview.subjectIdHint')}
          required
        >
          <Input
            id="rs-subid"
            value={subjectId}
            onChange={(e) => setSubjectId(e.target.value)}
            mono
          />
        </Field>

        <Field
          label={t('granular.accessReview.actionName')}
          htmlFor="rs-action"
          description={t('granular.accessReview.actionNameHint')}
          required
        >
          <Input
            id="rs-action"
            value={actionName}
            onChange={(e) => setActionName(e.target.value)}
            mono
          />
        </Field>
      </div>

      <div>
        <Button
          onClick={() => mutation.mutate({ isNew: true })}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('granular.accessReview.run')}
        </Button>
      </div>

      {mutation.isError && (
        <p role="alert" className="text-sm text-danger">
          {t('granular.accessReview.loadError')}
        </p>
      )}

      {mutation.isSuccess && (
        <SearchResultsTable
          results={results}
          typeLabel={t('granular.accessReview.resourceType')}
          nextToken={nextToken}
          onLoadMore={() =>
            mutation.mutate({ pageToken: nextToken, isNew: false })
          }
          isPending={mutation.isPending}
        />
      )}
    </div>
  )
}

// --- shared search results table ------------------------------------------------

function SearchResultsTable({
  results,
  typeLabel,
  nextToken,
  onLoadMore,
  isPending,
}: {
  results: AuthZenEntityResult[]
  typeLabel: string
  nextToken: string
  onLoadMore: () => void
  isPending: boolean
}) {
  const { t } = useTranslation('console')

  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm text-muted-foreground">
        {t('granular.accessReview.resultCount', { count: results.length })}
      </p>

      {results.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {t('granular.accessReview.noResults')}
        </p>
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">{typeLabel}</th>
                <th className="px-3 py-2 font-medium">
                  {t('granular.accessReview.resultId')}
                </th>
              </tr>
            </thead>
            <tbody>
              {results.map((r, i) => (
                <tr
                  key={`${r.type ?? ''}:${r.id ?? r.name ?? i}`}
                  className="border-t border-border"
                >
                  <td className="px-3 py-2">
                    <Badge variant="neutral">{r.type ?? '—'}</Badge>
                  </td>
                  <td className="px-3 py-2">
                    <span className="font-mono text-xs text-foreground">
                      {r.name ? `${r.id} (${r.name})` : (r.id ?? '—')}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {nextToken && (
        <div>
          <Button variant="secondary" onClick={onLoadMore} disabled={isPending}>
            {isPending && <Spinner size="sm" aria-hidden />}
            {t('granular.accessReview.loadMore')}
          </Button>
        </div>
      )}
    </div>
  )
}

// --- access-review export -------------------------------------------------------

function ExportPanel() {
  const { t } = useTranslation('console')
  const [resType, setResType] = useState('resource')
  const [resId, setResId] = useState('')
  const [pack, setPack] = useState<AccessReviewPack | null>(null)

  const exportMutation = usePrivilegedMutation<
    AccessReviewRequest,
    AccessReviewPack
  >({
    mutationFn: (req) => consoleApi.accessReviewExport(req),
    invalidateKeys: () => [],
    successMessage: (data) =>
      t('granular.accessReview.exportSuccess', {
        hash: data.integrity.pack_sha256.slice(0, 16),
      }),
    onDone: (data) => setPack(data),
  })

  const valid = resId.trim() !== ''

  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-muted-foreground">
        {t('granular.accessReview.exportHint')}
      </p>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <Field
          label={t('granular.accessReview.resourceType')}
          htmlFor="ex-restype"
        >
          <Select value={resType} onValueChange={setResType}>
            <SelectTrigger id="ex-restype">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="resource">
                {t('granular.accessReview.resourceTypeResource')}
              </SelectItem>
              <SelectItem value="agent">
                {t('granular.accessReview.resourceTypeAgent')}
              </SelectItem>
              <SelectItem value="session">
                {t('granular.accessReview.resourceTypeSession')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>

        <Field
          label={t('granular.accessReview.resourceId')}
          htmlFor="ex-resid"
          description={t('granular.accessReview.resourceIdHint')}
          required
        >
          <Input
            id="ex-resid"
            value={resId}
            onChange={(e) => setResId(e.target.value)}
            mono
          />
        </Field>
      </div>

      <div>
        <Button
          variant="primary"
          onClick={() =>
            exportMutation.mutate({
              resource: { type: resType, id: resId.trim() },
            })
          }
          disabled={!valid || exportMutation.isPending}
        >
          {exportMutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('granular.accessReview.runExport')}
        </Button>
      </div>

      <FormError error={exportMutation.error} />

      {pack && <AccessReviewPackView pack={pack} />}
    </div>
  )
}

// --- access-review pack display -------------------------------------------------

function AccessReviewPackView({ pack }: { pack: AccessReviewPack }) {
  const { t } = useTranslation('console')

  return (
    <div className="flex flex-col gap-4 rounded-lg border border-border p-4">
      {/* Header: entry count + sealed status */}
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <span className="text-sm font-medium text-foreground">
          {t('granular.accessReview.exportEntries')}: {pack.entries.length}
        </span>
        <Badge variant={pack.integrity.sealed ? 'success' : 'warning'}>
          {pack.integrity.sealed
            ? t('granular.accessReview.exportSealed')
            : t('granular.accessReview.exportNotSealed')}
        </Badge>
      </div>

      {/* Integrity section: SHA-256 + audit seq */}
      <div className="flex flex-col gap-1">
        <p className="text-xs font-medium text-muted-foreground">
          {t('granular.accessReview.exportIntegrity')}
        </p>
        <code className="break-all rounded bg-muted px-2 py-1 font-mono text-xs text-muted-foreground">
          {pack.integrity.pack_sha256}
        </code>
        {pack.integrity.audit_seq !== undefined && (
          <p className="text-xs text-muted-foreground">
            seq: {pack.integrity.audit_seq}
          </p>
        )}
      </div>

      {/* Entries table */}
      {pack.entries.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {t('granular.accessReview.noResults')}
        </p>
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">
                  {t('granular.accessReview.resultSubject')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('granular.accessReview.resultPermission')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('granular.accessReview.resultVia')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('granular.accessReview.resultReason')}
                </th>
              </tr>
            </thead>
            <tbody>
              {pack.entries.map((entry, i) => (
                <tr
                  key={`${entry.subject.type}:${entry.subject.id}:${entry.permission ?? i}`}
                  className="border-t border-border"
                >
                  <td className="px-3 py-2">
                    <span className="font-mono text-xs text-foreground">
                      {entry.subject.display ?? entry.subject.id}
                    </span>
                    <span className="ml-1 text-xs text-muted-foreground">
                      ({entry.subject.type})
                    </span>
                  </td>
                  <td className="px-3 py-2">
                    <code className="font-mono text-xs text-foreground">
                      {entry.permission}
                    </code>
                  </td>
                  <td className="px-3 py-2 text-xs text-muted-foreground">
                    {entry.via}
                  </td>
                  <td className="px-3 py-2 text-xs text-muted-foreground">
                    {entry.reason}
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
