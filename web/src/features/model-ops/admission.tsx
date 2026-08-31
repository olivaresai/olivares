// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Admission tab — the security core. It authors the tenant signed-model
// TRUST POLICY and shows the recorded CURRENT verdict — one row per version, never a
// history: `handleAdmitVersion` lists by version with `Limit: 1` and UPDATES the row when it
// exists, so re-admitting REPLACES the verdict and earlier attempts are not retrievable from
// this route. The UI exposes each admission
// truth independently and never collapses them into a single "trusted" light: a policy
// may exist or not, enforcement may be on or off, a signature may verify or not — and
// crucially, whether a version would still pass a DEPLOYMENT is UNKNOWN here (only the
// engine re-checks the anchor at deploy time), so nothing is ever labelled "currently
// deployable". The policy is saved atomically via a dedicated command DTO (never a
// response spread), and removing a trust anchor or enabling enforcement is confirmed
// because it can make future deployments fail.
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { KvList, KvRow } from '@/components/ui/kv'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { EmptyState } from '@/components/ui/empty-state'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import {
  admissionDraftError,
  splitLines,
  splitPemBlocks,
} from '@/lib/admission/policy'
import { useAuth } from '@/lib/auth/context'
import { RelTimeLabel } from '@/features/shared'
import { ListTruncationBadge, SectionCard } from '@/features/_intel'
import { EVIDENCE_PAGE, modelsApi, modelsKeys } from '@/features/models/api'
import type {
  AdmissionPolicy,
  AdmissionPolicyInput,
  ModelAdmission,
} from '@/features/models/types'
import {
  AdmitDialog,
  ModeBadges,
  PostureBadge,
  SignerRoots,
  VerdictBody,
  deriveAdmissionModes,
  verdictPosture,
} from './shared'

const VERIFIED_ALL = '__all__'

export function AdmissionTab() {
  const { t } = useTranslation(['model-ops', 'common'])
  const { activeTenant, can } = useAuth()
  const canAdmin = can('models:admission:admin')
  const canAdmit = can('models:admission:write')
  const [editOpen, setEditOpen] = useState(false)

  const policyQ = useQuery({
    queryKey: modelsKeys.admissionPolicy(activeTenant),
    queryFn: () => modelsApi.admissionPolicy(),
  })

  return (
    <div className="flex flex-col gap-4">
      <SectionCard
        title={t('policy.title')}
        description={t('policy.description')}
        actions={
          canAdmin && policyQ.data ? (
            <Button
              variant="primary"
              size="sm"
              onClick={() => setEditOpen(true)}
            >
              {t('policy.edit')}
            </Button>
          ) : null
        }
      >
        {policyQ.isLoading ? (
          <Skeleton className="h-28 w-full" />
        ) : policyQ.error ? (
          <p className="text-sm text-muted-foreground">
            {t('policy.loadError')}
          </p>
        ) : policyQ.data ? (
          <PolicySummary policy={policyQ.data} />
        ) : null}
      </SectionCard>

      {canAdmin && policyQ.data && (
        <PolicyDialog
          policy={policyQ.data}
          open={editOpen}
          onOpenChange={setEditOpen}
        />
      )}

      <VerdictInventory canAdmit={canAdmit} />
    </div>
  )
}

/** The three honest policy states — unconfigured/observe, configured/observe,
 *  configured/enforce — plus the derived trust mode. Never a single trusted light. */
function PolicySummary({ policy }: { policy: AdmissionPolicy }) {
  const { t } = useTranslation('model-ops')
  const modes = deriveAdmissionModes(policy)
  const configured = policy.configured !== false
  const enforce = policy.require_signed

  const stateKey = !configured
    ? 'unconfigured'
    : enforce
      ? 'enforce'
      : 'observe'

  return (
    <div className="flex flex-col gap-3">
      <div
        className={
          enforce
            ? 'rounded-md border border-accent-text bg-accent-soft px-3 py-2'
            : 'rounded-md border border-warning bg-warning-soft px-3 py-2'
        }
      >
        <p className="text-sm font-medium text-foreground">
          {t(`policy.state.${stateKey}.title`)}
        </p>
        <p className="mt-0.5 text-xs text-muted-foreground">
          {t(`policy.state.${stateKey}.body`)}
        </p>
      </div>

      <KvList>
        <KvRow label={t('policy.mode')}>
          <ModeBadges modes={modes} />
        </KvRow>
        <KvRow label={t('policy.requireSigned')}>
          {enforce ? t('policy.on') : t('policy.off')}
        </KvRow>
        <KvRow label={t('policy.requireDigests')}>
          {policy.require_artifact_digests ? t('policy.on') : t('policy.off')}
        </KvRow>
        {policy.allowed_identities && policy.allowed_identities.length > 0 && (
          <KvRow label={t('policy.identities')} align="start">
            <ul className="flex flex-col gap-0.5 font-mono text-xs">
              {policy.allowed_identities.map((v) => (
                <li key={v} className="break-all">
                  {v}
                </li>
              ))}
            </ul>
          </KvRow>
        )}
        {policy.allowed_issuers && policy.allowed_issuers.length > 0 && (
          <KvRow label={t('policy.issuers')} align="start">
            <ul className="flex flex-col gap-0.5 font-mono text-xs">
              {policy.allowed_issuers.map((v) => (
                <li key={v} className="break-all">
                  {v}
                </li>
              ))}
            </ul>
          </KvRow>
        )}
        <KvRow label={t('policy.trustedRoots')}>
          {policy.trusted_roots?.length ?? 0}
        </KvRow>
        <KvRow label={t('policy.trustedKeys')}>
          {policy.trusted_keys?.length ?? 0}
        </KvRow>
        {policy.attested_by && (
          <KvRow label={t('policy.attestedBy')} mono>
            {policy.attested_by}
          </KvRow>
        )}
        {policy.attested_at && (
          <KvRow label={t('policy.attestedAt')}>
            <RelTimeLabel ts={policy.attested_at} />
          </KvRow>
        )}
      </KvList>
    </div>
  )
}

// --- Policy editor ---------------------------------------------------------------

function PolicyDialog({
  policy,
  open,
  onOpenChange,
}: {
  policy: AdmissionPolicy
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] max-w-2xl overflow-y-auto">
        {open && (
          <PolicyForm policy={policy} onClose={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function PolicyForm({
  policy,
  onClose,
}: {
  policy: AdmissionPolicy
  onClose: () => void
}) {
  const { t } = useTranslation(['model-ops', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()

  const [requireSigned, setRequireSigned] = useState(policy.require_signed)
  const [requireDigests, setRequireDigests] = useState(
    policy.require_artifact_digests,
  )
  const [identities, setIdentities] = useState(
    (policy.allowed_identities ?? []).join('\n'),
  )
  const [issuers, setIssuers] = useState(
    (policy.allowed_issuers ?? []).join('\n'),
  )
  const [roots, setRoots] = useState((policy.trusted_roots ?? []).join('\n\n'))
  const [keys, setKeys] = useState((policy.trusted_keys ?? []).join('\n\n'))
  // The unconfigured stub carries a SYNTHETIC note ("observe mode — …"); it is not a
  // user note and must never be seeded into the editor (or it would be PUT back and
  // masquerade as authored text). Only seed a note from a real, saved policy.
  const [note, setNote] = useState(
    policy.configured === false ? '' : (policy.note ?? ''),
  )
  const [confirmOpen, setConfirmOpen] = useState(false)

  const identityList = useMemo(() => splitLines(identities), [identities])
  const issuerList = useMemo(() => splitLines(issuers), [issuers])
  const rootList = useMemo(() => splitPemBlocks(roots), [roots])
  const keyList = useMemo(() => splitPemBlocks(keys), [keys])

  const modes = deriveAdmissionModes({
    allowed_identities: identityList,
    allowed_issuers: issuerList,
    trusted_keys: keyList,
    trusted_roots: rootList,
  })

  // Client-side mirror of the engine's 400s — surface early, never post a known-invalid
  // combination (the engine remains the authority).
  const draftError = admissionDraftError({
    require_signed: requireSigned,
    rootsText: roots,
    keysText: keys,
    allowed_identities: identityList,
    allowed_issuers: issuerList,
    trusted_keys: keyList,
    trusted_roots: rootList,
  })
  // Models never sends predicates, so `emptyPredicate` is unreachable here by
  // construction; the other four map 1:1 onto this namespace's existing keys.
  const errorKey = draftError ? `policy.err.${draftError}` : null
  const valid = errorKey === null

  // A change is dangerous — and worth confirming — when it turns enforcement ON, tightens
  // it by requiring artifact digests, or removes a trust anchor that was configured: each
  // can make future deployment create/update fail until versions are re-admitted. Anchor
  // comparison normalises whitespace so a mere round-trip is not mistaken for a removal.
  const prevRoots = new Set((policy.trusted_roots ?? []).map((r) => r.trim()))
  const prevKeys = new Set((policy.trusted_keys ?? []).map((k) => k.trim()))
  const removedAnchor =
    [...prevRoots].some((r) => !rootList.includes(r)) ||
    [...prevKeys].some((k) => !keyList.includes(k))
  const enablingEnforce = requireSigned && !policy.require_signed
  const tighteningDigests =
    requireSigned && requireDigests && !policy.require_artifact_digests
  const needsConfirm = enablingEnforce || tighteningDigests || removedAnchor

  const mutation = useMutation({
    mutationFn: (body: AdmissionPolicyInput) =>
      modelsApi.putAdmissionPolicy(body),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: modelsKeys.admissionPolicy(activeTenant),
      })
      toast.success(t('policy.saved'))
      onClose()
    },
    onError: (err) => {
      report(err)
    },
  })

  /** Build the write body field-by-field — NEVER a spread of the GET response, so the
   *  unconfigured stub's `configured`/synthetic note can't reach the fail-closed decoder. */
  function buildBody(): AdmissionPolicyInput {
    return {
      require_signed: requireSigned,
      require_artifact_digests: requireDigests,
      ...(identityList.length ? { allowed_identities: identityList } : {}),
      ...(issuerList.length ? { allowed_issuers: issuerList } : {}),
      ...(keyList.length ? { trusted_keys: keyList } : {}),
      ...(rootList.length ? { trusted_roots: rootList } : {}),
      ...(note.trim() ? { note: note.trim() } : {}),
    }
  }

  function save() {
    if (!valid) return
    if (needsConfirm) {
      setConfirmOpen(true)
      return
    }
    mutation.mutate(buildBody())
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('policy.editTitle')}</DialogTitle>
        <DialogDescription>{t('policy.editBody')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <div className="flex items-center justify-between rounded-md border border-border px-3 py-2">
          <span className="text-xs text-muted-foreground">
            {t('policy.derivedMode')}
          </span>
          <ModeBadges modes={modes} />
        </div>

        {errorKey && (
          <div className="rounded-md border border-danger bg-danger-soft px-3 py-2">
            <p className="text-xs text-danger">{t(errorKey)}</p>
          </div>
        )}

        <Field
          label={t('policy.requireSigned')}
          htmlFor="pol-signed"
          description={t('policy.requireSignedHint')}
        >
          <Switch
            id="pol-signed"
            checked={requireSigned}
            onCheckedChange={setRequireSigned}
          />
        </Field>
        <Field
          label={t('policy.requireDigests')}
          htmlFor="pol-digests"
          description={t('policy.requireDigestsHint')}
        >
          <Switch
            id="pol-digests"
            checked={requireDigests}
            onCheckedChange={setRequireDigests}
          />
        </Field>

        <Field
          label={t('policy.trustedRoots')}
          htmlFor="pol-roots"
          description={t('policy.trustedRootsHint')}
        >
          <Textarea
            id="pol-roots"
            value={roots}
            onChange={(e) => setRoots(e.target.value)}
            placeholder={
              '-----BEGIN CERTIFICATE-----\n…\n-----END CERTIFICATE-----'
            }
            rows={5}
            className="font-mono text-xs"
          />
        </Field>
        <Field
          label={t('policy.trustedKeys')}
          htmlFor="pol-keys"
          description={t('policy.trustedKeysHint')}
        >
          <Textarea
            id="pol-keys"
            value={keys}
            onChange={(e) => setKeys(e.target.value)}
            placeholder={
              '-----BEGIN PUBLIC KEY-----\n…\n-----END PUBLIC KEY-----'
            }
            rows={4}
            className="font-mono text-xs"
          />
        </Field>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field
            label={t('policy.identities')}
            htmlFor="pol-identities"
            description={t('policy.identitiesHint')}
          >
            <Textarea
              id="pol-identities"
              value={identities}
              onChange={(e) => setIdentities(e.target.value)}
              rows={3}
              className="font-mono text-xs"
            />
          </Field>
          <Field
            label={t('policy.issuers')}
            htmlFor="pol-issuers"
            description={t('policy.issuersHint')}
          >
            <Textarea
              id="pol-issuers"
              value={issuers}
              onChange={(e) => setIssuers(e.target.value)}
              rows={3}
              className="font-mono text-xs"
            />
          </Field>
        </div>
        <Field label={t('policy.note')} htmlFor="pol-note">
          <Textarea
            id="pol-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={2}
          />
        </Field>
      </div>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={save}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('common:actions.save')}
        </Button>
      </DialogFooter>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('policy.confirmTitle')}
        description={t('policy.confirmBody')}
        confirmLabel={t('common:actions.save')}
        tone="danger"
        pending={mutation.isPending}
        onConfirm={() => {
          setConfirmOpen(false)
          mutation.mutate(buildBody())
        }}
      />
    </>
  )
}

// --- Verdict inventory -----------------------------------------------------------

function VerdictInventory({ canAdmit }: { canAdmit: boolean }) {
  const { t } = useTranslation(['model-ops', 'common'])
  const { activeTenant } = useAuth()
  const [verified, setVerified] = useState<string>(VERIFIED_ALL)
  const [selected, setSelected] = useState<ModelAdmission | null>(null)

  const filters = useMemo(() => {
    // "All" OMITS the parameter; verified=false is a real filter (unverified only).
    // ⛔ EL TECHO VA SIEMPRE: hay UNA fila por versión (el motor actualiza, no acumula), así que
    //    esta lista mide el tamaño del parque de versiones con intento de admisión. Sin `limit`
    //    salían las cien primeras por `id ASC` y la pantalla se leía «éstos son los veredictos».
    if (verified === VERIFIED_ALL) return { limit: EVIDENCE_PAGE }
    return { verified: verified === 'true', limit: EVIDENCE_PAGE }
  }, [verified])

  const query = useQuery({
    queryKey: modelsKeys.modelAdmissions(activeTenant, filters),
    queryFn: () => modelsApi.modelAdmissions(filters),
  })

  const columns = useMemo<TableColumn<ModelAdmission>[]>(
    () => [
      {
        accessorKey: 'version_ref',
        header: t('verdicts.columns.version'),
        cell: ({ row }) => (
          <span className="font-mono text-xs">{row.original.version_ref}</span>
        ),
      },
      {
        id: 'posture',
        header: t('verdicts.columns.posture'),
        cell: ({ row }) => (
          <PostureBadge posture={verdictPosture(row.original)} />
        ),
      },
      {
        accessorKey: 'method',
        header: t('verdicts.columns.method'),
        cell: ({ row }) =>
          row.original.method ? (
            <Badge variant="outline" className="font-mono text-[11px]">
              {row.original.method}
            </Badge>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        id: 'roots',
        header: t('verdicts.columns.roots'),
        cell: ({ row }) =>
          row.original.signer_roots && row.original.signer_roots.length > 0 ? (
            <SignerRoots roots={row.original.signer_roots} />
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        id: 'tlog',
        header: t('verdicts.columns.tlog'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {row.original.tlog_verified
              ? t('evidence.tlogVerified')
              : row.original.tlog_present
                ? t('evidence.tlogPresent')
                : t('evidence.tlogAbsent')}
          </span>
        ),
      },
      {
        accessorKey: 'attested_at',
        header: t('verdicts.columns.attestedAt'),
        cell: ({ row }) =>
          row.original.attested_at ? (
            <RelTimeLabel ts={row.original.attested_at} />
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
    ],
    [t],
  )

  return (
    <SectionCard
      title={t('verdicts.title')}
      description={t('verdicts.description')}
      noPadding
    >
      {/* Fuera del bloque de datos: un refetch fallido no debe dejar el aviso flotando. */}
      <ListTruncationBadge
        query={query}
        label={t('verdicts.truncated', {
          n: query.data?.items?.length ?? 0,
        })}
        hint={t('verdicts.truncatedHint')}
        className="px-3 pt-3"
      />

      <DataTable<ModelAdmission>
        label={t('verdicts.title')}
        columns={columns}
        data={query.data?.items ?? []}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        getRowId={(r) => r.id}
        onRowClick={(r) => setSelected(r)}
        searchable
        searchPlaceholder={t('verdicts.search')}
        toolbar={
          <Select value={verified} onValueChange={setVerified}>
            <SelectTrigger className="h-8 w-[10rem] text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={VERIFIED_ALL}>
                {t('verdicts.filters.all')}
              </SelectItem>
              <SelectItem value="true">
                {t('verdicts.filters.verified')}
              </SelectItem>
              <SelectItem value="false">
                {t('verdicts.filters.unverified')}
              </SelectItem>
            </SelectContent>
          </Select>
        }
        empty={
          <EmptyState
            title={t('empty.verdict.title')}
            description={t('empty.verdict.description')}
          />
        }
      />

      <VerdictDrawer
        verdict={selected}
        canAdmit={canAdmit}
        onClose={() => setSelected(null)}
      />
    </SectionCard>
  )
}

function VerdictDrawer({
  verdict,
  canAdmit,
  onClose,
}: {
  verdict: ModelAdmission | null
  canAdmit: boolean
  onClose: () => void
}) {
  const { t } = useTranslation('model-ops')
  const [reAdmitOpen, setReAdmitOpen] = useState(false)

  return (
    <Sheet
      open={!!verdict}
      onOpenChange={(o) => {
        if (!o) {
          setReAdmitOpen(false)
          onClose()
        }
      }}
    >
      <SheetContent className="flex w-full flex-col gap-0 sm:max-w-xl">
        {verdict && (
          <>
            <SheetHeader>
              <SheetTitle className="font-mono text-sm">
                {verdict.version_ref}
              </SheetTitle>
              <SheetDescription>
                {t('verdicts.drawerSubtitle')}
              </SheetDescription>
            </SheetHeader>
            <ScrollArea className="flex-1">
              <div className="flex flex-col gap-4 px-1 py-3">
                {canAdmit && (
                  <div>
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => setReAdmitOpen(true)}
                    >
                      <ShieldCheck />
                      {t('verdicts.reAdmit')}
                    </Button>
                  </div>
                )}
                <VerdictBody verdict={verdict} />
              </div>
            </ScrollArea>
            {canAdmit && (
              <AdmitDialog
                versionId={verdict.version_ref}
                versionLabel={verdict.version_ref}
                reAdmit
                open={reAdmitOpen}
                onOpenChange={setReAdmitOpen}
              />
            )}
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}
