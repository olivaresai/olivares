// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import {
  Cable,
  Pencil,
  Plus,
  RotateCw,
  ShieldAlert,
  Trash2,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/components/ui/toaster'
import { AAL, RequireAssurance } from '@/features/identity/assurance'
import {
  normalizeSourceMode,
  SourceModeBadge,
  type NormalizedSourceMode,
} from '@/features/shared'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import {
  useFailedActionReporter,
  usePrivilegedMutation,
} from '@/lib/hooks/use-privileged-mutation'
import { useResumeGuard } from '@/lib/hooks/use-resume-guard'
import {
  type ConnectorInfo,
  consoleApi,
  consoleKeys,
  type ConnectorOnboardInput,
  type SourceApplyResult,
  type SourceReloadReport,
  type SourceRosterEntry,
} from './api'
import { ConnectorCatalog, HostingBadge } from './connector-catalog'
import { CustomFields, type CustomRow } from './custom-fields'

// A source name/handle: letters, digits and the separators `. _ - /` (mirrors the
// store's ValidateSourceName so the save button can explain itself).
const NAME_RE = /^[A-Za-z0-9._/-]{1,128}$/
const RESERVED_CONFIG_KEYS = new Set(['mode'])
type RosterSourceMode = 'export' | 'live'

function normalizeRosterMode(value?: string | null): RosterSourceMode {
  return normalizeSourceMode(value) === 'live' ? 'live' : 'export'
}

// statusVariant maps a live source status to a Badge tone (honest: failed is danger,
// not_wired is a low-emphasis outline — never a green "running" it is not).
function statusVariant(
  status: string,
): 'success' | 'danger' | 'warning' | 'neutral' | 'outline' {
  switch (status) {
    case 'running':
      return 'success'
    case 'failed':
      return 'danger'
    case 'stopped':
      return 'warning'
    case 'disabled':
      return 'neutral'
    default:
      return 'outline'
  }
}

/**
 * ConnectorsTab is the console panel for connector ONBOARDING: an operator adds,
 * configures, tests and removes a connector AND its credentials from the console —
 * sealed and persisted in the database — instead of editing the boot config file.
 * The add/edit form is rendered from the connector's declared ConfigFields (the
 * descriptor catalog); a SECRET field is entered inline and the engine seals it into
 * the store, persisting only a reference, then applies the change to the running
 * engine WITHOUT a restart. Deployment-wide, superadmin-only, step-up (AAL3)
 * protected — like the SSO and secrets panels it sits beside.
 */
export function ConnectorsTab() {
  const { t } = useTranslation(['console', 'common', 'shared'])
  const { isSuperadmin } = useAuth()
  const [createOpen, setCreateOpen] = useState(false)
  const [editing, setEditing] = useState<SourceRosterEntry | null>(null)
  const [del, setDel] = useState<SourceRosterEntry | null>(null)
  const [reloadOpen, setReloadOpen] = useState(false)
  const [report, setReport] = useState<SourceReloadReport | null>(null)
  const [modeFilter, setModeFilter] = useState<NormalizedSourceMode | 'all'>(
    'all',
  )

  const catalog = useQuery({
    queryKey: consoleKeys.connectors(),
    queryFn: () => consoleApi.listConnectors(),
    enabled: isSuperadmin,
  })
  const sources = useQuery({
    queryKey: consoleKeys.sources(),
    queryFn: () => consoleApi.listSources(),
    enabled: isSuperadmin,
  })

  const deleteMutation = usePrivilegedMutation<string, SourceApplyResult>({
    mutationFn: (name) => consoleApi.deleteConnector(name),
    invalidateKeys: () => [consoleKeys.sources()],
    successMessage: t('console:connectors.deleted'),
    onDone: () => setDel(null),
  })

  // Reconcile the live source roster. On success we open a report panel that renders
  // the outcome honestly. The success toast is QUALIFIED, never a bare "reloaded":
  // rejections downgrade it to a partial, and a clean reload still carries the
  // restart caveat — because there is always boot-time config a reload cannot apply.
  const reloadMutation = usePrivilegedMutation<void, SourceReloadReport>({
    mutationFn: () => consoleApi.reloadRuntime(),
    invalidateKeys: () => [consoleKeys.sources(), consoleKeys.connectors()],
    successMessage: (r) =>
      r.rejected && r.rejected.length > 0
        ? t('console:connectors.reload.toastPartial')
        : t('console:connectors.reload.toastClean'),
    onDone: (r) => {
      setReloadOpen(false)
      setReport(r)
    },
  })

  if (!isSuperadmin) {
    return (
      <div className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-sm text-muted-foreground">
        <ShieldAlert
          className="mt-0.5 size-4 shrink-0 text-warning"
          aria-hidden
        />
        {t('console:connectors.superadminOnly')}
      </div>
    )
  }

  // The catalog endpoint answering 501 means this build did not wire connector
  // onboarding — surface that honestly rather than a generic error.
  const unavailable =
    catalog.error instanceof ApiError && catalog.error.status === 501

  const kinds = catalog.data?.connectors ?? []
  const rows = sources.data?.sources ?? []
  const filteredRows = rows.filter(
    (s) =>
      modeFilter === 'all' || normalizeSourceMode(s.source_mode) === modeFilter,
  )

  return (
    <div className="flex flex-col gap-4 pt-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold text-foreground">
            {t('console:connectors.title')}
          </h2>
          <p className="max-w-2xl text-sm text-muted-foreground">
            {t('console:connectors.caption')}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="secondary"
            onClick={() => setReloadOpen(true)}
            disabled={unavailable}
          >
            <RotateCw />
            {t('console:connectors.reload.button')}
          </Button>
          <Button
            onClick={() => setCreateOpen(true)}
            disabled={unavailable || kinds.length === 0}
          >
            <Plus />
            {t('console:connectors.add')}
          </Button>
        </div>
      </div>

      {unavailable ? (
        <p className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-sm text-warning">
          <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
          {t('console:connectors.unavailable')}
        </p>
      ) : sources.isLoading || catalog.isLoading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : sources.isError ? (
        <ErrorState retry={() => void sources.refetch()} />
      ) : rows.length === 0 ? (
        <EmptyState
          title={t('console:connectors.none')}
          description={t('console:connectors.noneHint')}
          icon={<Cable />}
        />
      ) : (
        <>
          <div className="flex justify-end">
            <Select
              value={modeFilter}
              onValueChange={(v) =>
                setModeFilter(v as NormalizedSourceMode | 'all')
              }
            >
              <SelectTrigger
                className="h-8 w-36"
                aria-label={t('console:connectors.modeFilter')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">
                  {t('console:connectors.modeAll')}
                </SelectItem>
                <SelectItem value="export">
                  {t('shared:sourceModes.export')}
                </SelectItem>
                <SelectItem value="live">
                  {t('shared:sourceModes.live')}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
          {filteredRows.length === 0 ? (
            <EmptyState
              title={t('console:connectors.noneForMode')}
              description={t('console:connectors.noneHint')}
              icon={<Cable />}
            />
          ) : (
            <div className="overflow-hidden rounded-lg border border-border">
              <table className="w-full text-sm">
                <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2 font-medium">
                      {t('console:connectors.colName')}
                    </th>
                    <th className="px-3 py-2 font-medium">
                      {t('console:connectors.colKind')}
                    </th>
                    <th className="px-3 py-2 font-medium">
                      {t('console:connectors.colMode')}
                    </th>
                    <th className="px-3 py-2 font-medium">
                      {t('console:connectors.colStatus')}
                    </th>
                    <th className="px-3 py-2" />
                  </tr>
                </thead>
                <tbody>
                  {filteredRows.map((s) => (
                    <tr
                      key={s.name}
                      className="border-t border-border align-top"
                    >
                      <td className="px-3 py-2">
                        <span className="font-mono text-xs text-foreground">
                          {s.name}
                        </span>
                      </td>
                      {/* NO hosting badge here, and that is a correction. It was
                          badged from the KIND's catalog entry, which answers where a
                          kind's DEFAULT endpoint points — not where THIS source's
                          configured one does. A self-managed GitLab (its descriptor
                          supports a private api_base) would have worn the kind's
                          gitlab.com "Vendor cloud" badge and read as SaaS. Kind-level
                          default metadata cannot truthfully answer instance-level
                          hosting, so the roster says nothing rather than something
                          false; the catalog below, which really is about kinds, does. */}
                      <td className="px-3 py-2 text-muted-foreground">
                        <span className="font-mono text-xs">
                          {s.kind || '—'}
                        </span>
                      </td>
                      <td className="px-3 py-2">
                        <SourceModeBadge value={s.source_mode} />
                      </td>
                      <td className="px-3 py-2">
                        <Badge variant={statusVariant(s.status)}>
                          {t(`console:connectors.status.${s.status}`, {
                            defaultValue: s.status,
                          })}
                        </Badge>
                      </td>
                      <td className="px-3 py-2 text-right">
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => setEditing(s)}
                          >
                            <Pencil />
                            {t('console:connectors.edit')}
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => setDel(s)}
                          >
                            <Trash2 />
                            {t('console:connectors.delete')}
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}

      {/* The CATALOG of what this build supports. Deliberately outside every
          RequireAssurance: GET /v1/console/connectors is a plain superadmin read
          (no AAL3 server-side), and until the console rendered it only inside
          the AAL3-gated add dialog — so a password-only operator could not see that
          openai, gemini and local were supported at all. */}
      {!unavailable && !catalog.isLoading && <ConnectorCatalog kinds={kinds} />}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {createOpen && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <ConnectorForm
                catalog={kinds}
                onClose={() => setCreateOpen(false)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <Dialog
        open={editing !== null}
        onOpenChange={(o) => !o && setEditing(null)}
      >
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {editing && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <ConnectorForm
                catalog={kinds}
                existing={editing}
                onClose={() => setEditing(null)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={del !== null}
        onOpenChange={(o) => !o && setDel(null)}
        title={t('console:connectors.deleteTitle')}
        description={t('console:connectors.deleteBody')}
        confirmLabel={t('console:connectors.delete')}
        tone="danger"
        pending={deleteMutation.isPending}
        onConfirm={() => del && deleteMutation.mutate(del.name)}
      />

      {/* (a) Confirm the DISRUPTIVE reload before firing. Below AAL3 the
          RequireAssurance below shows the step-up PROMPT in place of the warning;
          the confirm button is not itself client-disabled, so the ENFORCING
          boundary is the backend's own requireAAL3 (handlers_sources.go), which
          fails closed on a sub-AAL3 POST — same as the delete flow. */}
      <ConfirmDialog
        open={reloadOpen}
        onOpenChange={(o) => !o && setReloadOpen(false)}
        title={t('console:connectors.reload.confirmTitle')}
        description={t('console:connectors.reload.confirmBody')}
        confirmLabel={t('console:connectors.reload.confirmLabel')}
        tone="danger"
        pending={reloadMutation.isPending}
        onConfirm={() => reloadMutation.mutate()}
      >
        <RequireAssurance minAal={AAL.HARDWARE} action="console">
          <span className="flex items-start gap-2 rounded-lg border border-danger/40 bg-danger/5 px-3 py-2 text-danger">
            <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
            {t('console:connectors.reload.confirmDisrupt')}
          </span>
        </RequireAssurance>
      </ConfirmDialog>

      <Dialog
        open={report !== null}
        onOpenChange={(o) => !o && setReport(null)}
      >
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {report && (
            <ReloadReport report={report} onClose={() => setReport(null)} />
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}

// SummaryRow renders one label/value line of the reconcile diff.
function SummaryRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-start justify-between gap-4 px-4 py-2.5">
      <dt className="text-sm text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words text-right text-sm text-foreground">
        {value}
      </dd>
    </div>
  )
}

/**
 * ReloadReport renders the outcome of a runtime reload HONESTLY. Three invariants:
 *  (b) it is scoped to the source roster + license — it never claims to reload the
 *      access engine, and its copy never mentions that engine by name;
 *  (c) `requires_restart` is surfaced as a WARNING every time, even on a clean reload
 *      where every other array is empty — those domains are read at boot and a reload
 *      does not touch them;
 *  (d) `rejected` sources are listed per name + reason and downgrade the outcome to a
 *      partial — never a plain success.
 */
function ReloadReport({
  report,
  onClose,
}: {
  report: SourceReloadReport
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const added = report.added ?? []
  const removed = report.removed ?? []
  const rotated = report.rotated ?? []
  const rejected = report.rejected ?? []
  const requiresRestart = report.requires_restart ?? []

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('console:connectors.reload.reportTitle')}</DialogTitle>
        <DialogDescription>
          {t('console:connectors.reload.reportCaption')}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        {/* (d) Rejections qualify the outcome as PARTIAL, listed per name + reason. */}
        {rejected.length > 0 && (
          <div className="flex flex-col gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3">
            <p className="flex items-center gap-2 text-sm font-medium text-warning">
              <ShieldAlert className="size-4 shrink-0" aria-hidden />
              {t('console:connectors.reload.partialTitle')}
            </p>
            <p className="text-sm text-muted-foreground">
              {t('console:connectors.reload.partialBody')}
            </p>
            <ul className="flex flex-col gap-1 text-sm">
              {rejected.map((r) => (
                <li key={r.name}>
                  <span className="font-mono text-xs text-foreground">
                    {r.name}
                  </span>
                  <span className="text-muted-foreground"> — {r.reason}</span>
                </li>
              ))}
            </ul>
          </div>
        )}

        {/* The reconcile diff — the honest outcome, whatever it is. */}
        <dl className="divide-y divide-border rounded-lg border border-border">
          {added.length > 0 && (
            <SummaryRow
              label={t('console:connectors.reload.added')}
              value={added.join(', ')}
            />
          )}
          {removed.length > 0 && (
            <SummaryRow
              label={t('console:connectors.reload.removed')}
              value={removed.join(', ')}
            />
          )}
          {rotated.length > 0 && (
            <SummaryRow
              label={t('console:connectors.reload.rotated')}
              value={rotated.join(', ')}
            />
          )}
          <SummaryRow
            label={t('console:connectors.reload.unchanged')}
            value={String(report.unchanged)}
          />
        </dl>

        {/* (c) ALWAYS surface requires_restart as a WARNING — even on a clean reload. */}
        {requiresRestart.length > 0 && (
          <div className="flex flex-col gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3">
            <p className="flex items-center gap-2 text-sm font-medium text-warning">
              <ShieldAlert className="size-4 shrink-0" aria-hidden />
              {t('console:connectors.reload.restartTitle')}
            </p>
            <p className="text-sm text-muted-foreground">
              {t('console:connectors.reload.restartBody')}
            </p>
            <ul className="flex list-disc flex-col gap-1 pl-5 text-sm text-muted-foreground">
              {requiresRestart.map((d) => (
                <li key={d}>{d}</li>
              ))}
            </ul>
          </div>
        )}
      </div>

      <DialogFooter>
        <Button variant="secondary" onClick={onClose}>
          {t('common:actions.close')}
        </Button>
      </DialogFooter>
    </>
  )
}

function ConnectorForm({
  catalog,
  existing,
  onClose,
}: {
  catalog: ConnectorInfo[]
  existing?: SourceRosterEntry
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common', 'shared'])
  const isEdit = !!existing
  const [kind, setKind] = useState(existing?.kind ?? catalog[0]?.kind ?? '')
  const [sourceMode, setSourceMode] = useState<RosterSourceMode>(() =>
    normalizeRosterMode(existing?.source_mode ?? existing?.config?.mode),
  )
  const info = useMemo(
    () => catalog.find((c) => c.kind === kind),
    [catalog, kind],
  )
  const initialInfo = useMemo(
    () =>
      existing ? catalog.find((c) => c.kind === existing.kind) : undefined,
    [catalog, existing],
  )

  const [name, setName] = useState(existing?.name ?? '')
  const [tenant, setTenant] = useState(existing?.tenant ?? '')
  const [enabled, setEnabled] = useState(existing?.enabled ?? true)
  const [poll, setPoll] = useState(String(existing?.poll_seconds ?? 0))

  // Non-secret descriptor field values, prefilled on edit from the stored config.
  const [cfg, setCfg] = useState<Record<string, string>>(() => {
    const out: Record<string, string> = {}
    if (existing?.config) {
      for (const f of initialInfo?.fields ?? []) {
        if (
          !f.secret &&
          !RESERVED_CONFIG_KEYS.has(f.key) &&
          existing.config[f.key] !== undefined
        ) {
          out[f.key] = existing.config[f.key]
        }
      }
    }
    return out
  })
  // Secret field values are ALWAYS blank on open (we never receive the value); a
  // blank field on edit keeps the stored sealed value.
  const [secrets, setSecrets] = useState<Record<string, string>>({})
  // Free-form settings: the only editor for an out-of-process (plugin) kind whose
  // fields the host does not know, and a place for extra keys on any kind. Prefilled
  // on edit from config keys not covered by the descriptor.
  const [custom, setCustom] = useState<CustomRow[]>(() => {
    const known = new Set((initialInfo?.fields ?? []).map((f) => f.key))
    for (const k of RESERVED_CONFIG_KEYS) known.add(k)
    const rows: CustomRow[] = []
    if (existing?.config) {
      for (const [k, v] of Object.entries(existing.config)) {
        if (!known.has(k)) rows.push({ key: k, value: v, secret: false })
      }
    }
    return rows
  })

  // On create the operator may switch kinds; reset the per-kind field state so values
  // never leak from one connector's form to another's.
  function changeKind(k: string) {
    setKind(k)
    setSourceMode('export')
    setCfg({})
    setSecrets({})
    setCustom([])
  }

  const fields = (info?.fields ?? []).filter(
    (f) => !RESERVED_CONFIG_KEYS.has(f.key),
  )
  const isPlugin = info?.transport === 'plugin' || info?.fields_known === false

  function buildInput(): ConnectorOnboardInput {
    const config: Record<string, string> = {}
    const secretsOut: Record<string, string> = {}
    for (const f of fields) {
      if (f.secret) {
        // "" = keep stored (edit) / unset (create); a typed value is sealed by the engine.
        secretsOut[f.key] = secrets[f.key] ?? ''
      } else {
        const v = cfg[f.key] ?? ''
        if (v !== '') config[f.key] = v
      }
    }
    for (const r of custom) {
      const k = r.key.trim()
      if (!k) continue
      if (r.secret) secretsOut[k] = r.value
      else config[k] = r.value
    }
    if (sourceMode === 'live' || existing?.config?.mode !== undefined) {
      config.mode = sourceMode
    }
    return {
      name: name.trim(),
      kind,
      tenant: tenant.trim(),
      enabled,
      poll_seconds: Number(poll) || 0,
      config,
      secrets: secretsOut,
    }
  }

  const save = usePrivilegedMutation<void, SourceApplyResult>({
    mutationFn: () => consoleApi.putConnector(buildInput()),
    invalidateKeys: () => [consoleKeys.sources(), consoleKeys.connectors()],
    successMessage: (res) =>
      res.applied
        ? t('console:connectors.saved')
        : t('console:connectors.savedNotApplied', { note: res.note ?? '' }),
    onDone: onClose,
  })

  // La política de reporte vive en un solo sitio: una llamada escrita a mano conserva su
  // `catch` para la limpieza y DELEGA el reporte (use-privileged-mutation.ts:25-32).
  const report = useFailedActionReporter('console')
  // No ejecutes la petición de un formulario ya desmontado: ver use-resume-guard.ts.
  const guardarReanudacion = useResumeGuard()
  const [testing, setTesting] = useState(false)
  async function test() {
    setTesting(true)
    try {
      await consoleApi.testConnector(buildInput())
      toast.success(t('console:connectors.tested'))
    } catch (err) {
      // ⛔ ASEGURAMIENTO ANTES QUE ROJO. Este `test` es una escritura gateada por AAL3
      // (core/api/server.go:721 → handleTestConnector), y este `catch` pintaba cualquier `ApiError.message` en rojo —
      // incluido el `step_up_required`, que NO es un fallo sino una ceremonia pendiente.
      //
      // Y NO BASTA con que el diálogo esté envuelto en `RequireAssurance`: ese pre-gate decide
      // sobre el `principal.aal` CACHEADO (identity/assurance.tsx:49-78) y `whoami` no tiene
      // `refetchInterval` (lib/auth/context.tsx:68-78), mientras el motor degrada AAL3 a AAL1
      // a los 15 minutos (core/auth/assurance.go:31-54). La caché puede decir AAL3 con el
      // motor en AAL1: el pre-gate deja pasar y el rechazo llega igual.
      // **Pre-gateado no es cubierto** — lo levantó el contraste de.
      if (err instanceof ApiError && err.isStepUpRequired) {
        report(
          err,
          guardarReanudacion(() => void test()),
        )
        return
      }
      const msg =
        err instanceof ApiError
          ? err.message
          : t('common:errors.generic', { defaultValue: 'Failed' })
      toast.error(msg)
    } finally {
      setTesting(false)
    }
  }

  // Required non-secret fields must be filled; a required secret field needs a value
  // on create (on edit a blank keeps the stored one).
  const requiredOk = fields.every((f) => {
    if (!f.required) return true
    if (f.secret) return isEdit || (secrets[f.key] ?? '') !== ''
    return (cfg[f.key] ?? '') !== ''
  })
  const nameValid = isEdit || NAME_RE.test(name.trim())
  const valid = nameValid && kind !== '' && tenant.trim() !== '' && requiredOk

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit
            ? t('console:connectors.editTitle')
            : t('console:connectors.createTitle')}
        </DialogTitle>
        <DialogDescription>{t('console:connectors.caption')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={t('console:connectors.kind')}
            htmlFor="conn-kind"
            description={t('console:connectors.kindHint')}
          >
            <Select value={kind} onValueChange={changeKind} disabled={isEdit}>
              <SelectTrigger id="conn-kind">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {catalog.map((c) => (
                  <SelectItem key={c.kind} value={c.kind}>
                    <span className="flex items-center gap-2">
                      {c.title ? `${c.title} (${c.kind})` : c.kind}
                      {/* Only SELF-HOSTED is called out while choosing: it is the one
                          answer that changes what the operator must have running. */}
                      {c.hosting === 'self_hosted' && (
                        <HostingBadge hosting={c.hosting} />
                      )}
                    </span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('console:connectors.name')}
            htmlFor="conn-name"
            required
          >
            <Input
              id="conn-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              mono
              disabled={isEdit}
              placeholder="vault-prod"
            />
          </Field>
        </div>

        {info?.description && (
          <p className="text-xs text-muted-foreground">{info.description}</p>
        )}

        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={t('console:connectors.tenant')}
            htmlFor="conn-tenant"
            description={t('console:connectors.tenantHint')}
            required
          >
            <Input
              id="conn-tenant"
              value={tenant}
              onChange={(e) => setTenant(e.target.value)}
              mono
            />
          </Field>
          <Field
            label={t('console:connectors.pollSeconds')}
            htmlFor="conn-poll"
            description={t('console:connectors.pollSecondsHint')}
          >
            <Input
              id="conn-poll"
              type="number"
              min={0}
              value={poll}
              onChange={(e) => setPoll(e.target.value)}
            />
          </Field>
        </div>

        <Field
          label={t('console:connectors.mode')}
          htmlFor="conn-mode"
          description={t('console:connectors.modeHint')}
        >
          <Select
            value={sourceMode}
            onValueChange={(v) => setSourceMode(normalizeRosterMode(v))}
          >
            <SelectTrigger id="conn-mode">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="export">
                {t('shared:sourceModes.export')}
              </SelectItem>
              <SelectItem value="live">
                {t('shared:sourceModes.live')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>

        <div className="flex items-center gap-2">
          <Switch
            id="conn-enabled"
            checked={enabled}
            onCheckedChange={setEnabled}
          />
          <Label htmlFor="conn-enabled">
            {t('console:connectors.enabled')}
          </Label>
        </div>

        {/* Descriptor-driven fields for an in-process connector. */}
        {fields.map((f) => {
          const fid = `conn-f-${f.key}`
          if (f.secret) {
            const isSet = isEdit && !!existing?.config?.[f.key]
            return (
              <Field
                key={f.key}
                label={f.key}
                htmlFor={fid}
                description={
                  isSet
                    ? t('console:connectors.secretSet')
                    : f.description || undefined
                }
                required={f.required && !isEdit}
              >
                <Input
                  id={fid}
                  type="password"
                  autoComplete="new-password"
                  value={secrets[f.key] ?? ''}
                  onChange={(e) =>
                    setSecrets((s) => ({ ...s, [f.key]: e.target.value }))
                  }
                />
              </Field>
            )
          }
          if (f.type === 'bool') {
            return (
              <div key={f.key} className="flex items-center gap-2">
                <Switch
                  id={fid}
                  checked={(cfg[f.key] ?? f.default) === 'true'}
                  onCheckedChange={(v) =>
                    setCfg((c) => ({ ...c, [f.key]: v ? 'true' : 'false' }))
                  }
                />
                <Label htmlFor={fid}>{f.description || f.key}</Label>
              </div>
            )
          }
          return (
            <Field
              key={f.key}
              label={f.key}
              htmlFor={fid}
              description={f.description || undefined}
              required={f.required}
            >
              <Input
                id={fid}
                type={f.type === 'int' ? 'number' : 'text'}
                mono={f.type !== 'string'}
                value={cfg[f.key] ?? ''}
                placeholder={f.default || undefined}
                onChange={(e) =>
                  setCfg((c) => ({ ...c, [f.key]: e.target.value }))
                }
              />
            </Field>
          )
        })}

        {/* Free-form settings: required for a plugin kind, optional otherwise. */}
        {isPlugin && (
          <p className="text-xs text-muted-foreground">
            {t('console:connectors.pluginNote')}
          </p>
        )}
        <CustomFields rows={custom} onChange={setCustom} />
      </div>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={() => void test()}
          disabled={!valid || testing || save.isPending || isPlugin}
          title={isPlugin ? t('console:connectors.testUnavailable') : undefined}
        >
          {testing && <Spinner size="sm" aria-hidden />}
          {t('console:connectors.test')}
        </Button>
        <Button
          variant="primary"
          onClick={() => save.mutate()}
          disabled={!valid || save.isPending}
        >
          {save.isPending && <Spinner size="sm" aria-hidden />}
          {t('console:connectors.save')}
        </Button>
      </DialogFooter>
    </>
  )
}
