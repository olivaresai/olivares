// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
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
import { Field } from '@/components/ui/field'
import { KvList, KvRow } from '@/components/ui/kv'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import {
  admissionDraftError,
  deriveAdmissionModes,
  splitLines,
  splitPemBlocks,
  type AdmissionMode,
} from '@/lib/admission/policy'
import { useAuth } from '@/lib/auth/context'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { RelTimeLabel } from '@/features/shared'
import { catalogApi, catalogKeys } from './api'
import { policyConfigured, type AdmissionKind } from './types'
import type { AdmissionPolicy, AdmissionPolicyInput } from './types'

/** The tenant's two admission policies. They are INDEPENDENT — the engine registers one
 *  route pair per kind and stores them under separate scopes — so they are shown side by
 *  side rather than behind a picker: an operator who tightened MCP admission and believes
 *  connectors moved with it has been misled by the UI, not by the engine. */
const KINDS: AdmissionKind[] = ['mcp', 'connector']

export function AdmissionPolicyTab() {
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      {KINDS.map((k) => (
        <PolicyCard key={k} kind={k} />
      ))}
    </div>
  )
}

function ModeBadges({ modes }: { modes: AdmissionMode[] }) {
  const { t } = useTranslation('catalog')
  return (
    <span className="flex flex-wrap gap-1">
      {modes.map((m) => (
        <Badge key={m} variant={m === 'empty' ? 'warning' : 'neutral'}>
          {t(`policy.modeName.${m}`)}
        </Badge>
      ))}
    </span>
  )
}

function PolicyCard({ kind }: { kind: AdmissionKind }) {
  const { t } = useTranslation(['catalog', 'common'])
  const { activeTenant, can } = useAuth()
  const canAdmin = can('catalog:entry:admin')
  const [editing, setEditing] = useState(false)

  const q = useQuery({
    queryKey: catalogKeys.admissionPolicy(activeTenant, kind),
    queryFn: () => catalogApi.admissionPolicy(kind),
  })

  return (
    <section className="rounded-lg border border-border p-4">
      <header className="mb-3 flex items-start justify-between gap-3">
        <div>
          <h3 className="text-sm font-medium text-foreground">
            {t(`policy.kind.${kind}.title`)}
          </h3>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t(`policy.kind.${kind}.body`)}
          </p>
        </div>
        {canAdmin && q.data && (
          <Button variant="ghost" size="sm" onClick={() => setEditing(true)}>
            {t('policy.edit')}
          </Button>
        )}
      </header>

      {q.isLoading && <Skeleton className="h-32 w-full" />}
      {q.error && (
        <div className="rounded-md border border-danger bg-danger-soft px-3 py-2">
          <p className="text-xs text-danger">{t('policy.loadFailed')}</p>
          <Button
            variant="ghost"
            size="sm"
            className="mt-1"
            onClick={() => q.refetch()}
          >
            {t('common:actions.retry')}
          </Button>
        </div>
      )}
      {q.data && <PolicySummary policy={q.data} />}

      {q.data && (
        <PolicyDialog
          kind={kind}
          policy={q.data}
          open={editing}
          onOpenChange={setEditing}
        />
      )}
    </section>
  )
}

/** The three honest states — unconfigured, configured/observe, configured/enforce. An
 *  unconfigured policy ADMITS EVERYTHING; it must never render as a neutral default. */
function PolicySummary({ policy }: { policy: AdmissionPolicy }) {
  const { t } = useTranslation('catalog')
  const configured = policyConfigured(policy)
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
          <ModeBadges modes={deriveAdmissionModes(policy)} />
        </KvRow>
        <KvRow label={t('policy.requireSigned')}>
          {enforce ? t('policy.on') : t('policy.off')}
        </KvRow>
        <KvRow label={t('policy.requireSubjectDigest')}>
          {policy.require_subject_digest ? t('policy.on') : t('policy.off')}
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
        {policy.allowed_predicates && policy.allowed_predicates.length > 0 && (
          <KvRow label={t('policy.predicates')} align="start">
            <ul className="flex flex-col gap-0.5 font-mono text-xs">
              {policy.allowed_predicates.map((v) => (
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
        {/* Only a SAVED policy carries attestation; the unconfigured stub has none. */}
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

function PolicyDialog({
  kind,
  policy,
  open,
  onOpenChange,
}: {
  kind: AdmissionKind
  policy: AdmissionPolicy
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] max-w-2xl overflow-y-auto">
        {/* Mounted only while open so every edit starts from the SERVER's current policy,
            never from a stale draft left by a cancelled edit. */}
        {open && (
          <PolicyForm
            kind={kind}
            policy={policy}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function PolicyForm({
  kind,
  policy,
  onClose,
}: {
  kind: AdmissionKind
  policy: AdmissionPolicy
  onClose: () => void
}) {
  const { t } = useTranslation(['catalog', 'common'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()

  const [requireSigned, setRequireSigned] = useState(policy.require_signed)
  const [requireDigest, setRequireDigest] = useState(
    policy.require_subject_digest,
  )
  const [identities, setIdentities] = useState(
    (policy.allowed_identities ?? []).join('\n'),
  )
  const [issuers, setIssuers] = useState(
    (policy.allowed_issuers ?? []).join('\n'),
  )
  const [predicates, setPredicates] = useState(
    (policy.allowed_predicates ?? []).join('\n'),
  )
  const [roots, setRoots] = useState((policy.trusted_roots ?? []).join('\n\n'))
  const [keys, setKeys] = useState((policy.trusted_keys ?? []).join('\n\n'))
  // The unconfigured stub carries a SYNTHETIC note ("no … admission policy configured");
  // it is not operator text and must never be seeded, or it would be PUT back and
  // masquerade as authored.
  const [note, setNote] = useState(
    policyConfigured(policy) ? (policy.note ?? '') : '',
  )
  const [confirmOpen, setConfirmOpen] = useState(false)

  const identityList = useMemo(() => splitLines(identities), [identities])
  const issuerList = useMemo(() => splitLines(issuers), [issuers])
  const predicateList = useMemo(() => splitLines(predicates), [predicates])
  const rootList = useMemo(() => splitPemBlocks(roots), [roots])
  const keyList = useMemo(() => splitPemBlocks(keys), [keys])

  const modes = deriveAdmissionModes({
    allowed_identities: identityList,
    allowed_issuers: issuerList,
    trusted_keys: keyList,
    trusted_roots: rootList,
  })

  // Client-side mirror of the engine's 400s (`validate()`), shared with the signed-model
  // admission surface. The engine remains the authority; this only surfaces early.
  const draftError = admissionDraftError({
    require_signed: requireSigned,
    rootsText: roots,
    keysText: keys,
    allowed_identities: identityList,
    allowed_issuers: issuerList,
    allowed_predicates: predicateList,
    trusted_keys: keyList,
    trusted_roots: rootList,
  })
  const valid = draftError === null

  // Dangerous = turning enforcement on, tightening it with subject digests, or dropping a
  // trust anchor: each can make future admissions fail until entries are re-signed.
  // Anchors compare trimmed so a mere round-trip is not read as a removal.
  const prevRoots = new Set((policy.trusted_roots ?? []).map((r) => r.trim()))
  const prevKeys = new Set((policy.trusted_keys ?? []).map((k) => k.trim()))
  const removedAnchor =
    [...prevRoots].some((r) => !rootList.includes(r)) ||
    [...prevKeys].some((k) => !keyList.includes(k))
  const enablingEnforce = requireSigned && !policy.require_signed
  const tighteningDigest =
    requireSigned && requireDigest && !policy.require_subject_digest
  const needsConfirm = enablingEnforce || tighteningDigest || removedAnchor

  const mutation = useMutation({
    mutationFn: (body: AdmissionPolicyInput) =>
      catalogApi.putAdmissionPolicy(kind, body),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: catalogKeys.admissionPolicy(activeTenant, kind),
      })
      toast.success(t('policy.saved'))
      onClose()
    },
    onError: (err) => {
      report(err)
    },
  })

  /** Field by field — NEVER a spread of the GET response, so the unconfigured stub's
   *  `configured` and synthetic note cannot reach the fail-closed decoder. */
  function buildBody(): AdmissionPolicyInput {
    return {
      require_signed: requireSigned,
      require_subject_digest: requireDigest,
      ...(identityList.length ? { allowed_identities: identityList } : {}),
      ...(issuerList.length ? { allowed_issuers: issuerList } : {}),
      ...(predicateList.length ? { allowed_predicates: predicateList } : {}),
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
        <DialogTitle>{t(`policy.kind.${kind}.editTitle`)}</DialogTitle>
        <DialogDescription>{t('policy.editBody')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <div className="flex items-center justify-between rounded-md border border-border px-3 py-2">
          <span className="text-xs text-muted-foreground">
            {t('policy.derivedMode')}
          </span>
          <ModeBadges modes={modes} />
        </div>

        {draftError && (
          <div className="rounded-md border border-danger bg-danger-soft px-3 py-2">
            <p className="text-xs text-danger">
              {t(`policy.err.${draftError}`)}
            </p>
          </div>
        )}

        <Field
          label={t('policy.requireSigned')}
          htmlFor="cat-pol-signed"
          description={t('policy.requireSignedHint')}
        >
          <Switch
            id="cat-pol-signed"
            checked={requireSigned}
            onCheckedChange={setRequireSigned}
          />
        </Field>
        <Field
          label={t('policy.requireSubjectDigest')}
          htmlFor="cat-pol-digest"
          description={t('policy.requireSubjectDigestHint')}
        >
          <Switch
            id="cat-pol-digest"
            checked={requireDigest}
            onCheckedChange={setRequireDigest}
          />
        </Field>
        <Field
          label={t('policy.identities')}
          htmlFor="cat-pol-identities"
          description={t('policy.identitiesHint')}
        >
          <Textarea
            id="cat-pol-identities"
            rows={2}
            value={identities}
            onChange={(e) => setIdentities(e.target.value)}
          />
        </Field>
        <Field
          label={t('policy.issuers')}
          htmlFor="cat-pol-issuers"
          description={t('policy.issuersHint')}
        >
          <Textarea
            id="cat-pol-issuers"
            rows={2}
            value={issuers}
            onChange={(e) => setIssuers(e.target.value)}
          />
        </Field>
        <Field
          label={t('policy.predicates')}
          htmlFor="cat-pol-predicates"
          description={t('policy.predicatesHint')}
        >
          <Textarea
            id="cat-pol-predicates"
            rows={2}
            value={predicates}
            onChange={(e) => setPredicates(e.target.value)}
          />
        </Field>
        <Field
          label={t('policy.trustedRoots')}
          htmlFor="cat-pol-roots"
          description={t('policy.trustedRootsHint')}
        >
          <Textarea
            id="cat-pol-roots"
            rows={4}
            className="font-mono text-xs"
            value={roots}
            onChange={(e) => setRoots(e.target.value)}
          />
        </Field>
        <Field
          label={t('policy.trustedKeys')}
          htmlFor="cat-pol-keys"
          description={t('policy.trustedKeysHint')}
        >
          <Textarea
            id="cat-pol-keys"
            rows={4}
            className="font-mono text-xs"
            value={keys}
            onChange={(e) => setKeys(e.target.value)}
          />
        </Field>
        <Field label={t('policy.note')} htmlFor="cat-pol-note">
          <Textarea
            id="cat-pol-note"
            rows={2}
            value={note}
            onChange={(e) => setNote(e.target.value)}
          />
        </Field>
      </div>

      <DialogFooter>
        <Button variant="ghost" onClick={onClose}>
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={save}
          disabled={!valid || mutation.isPending}
        >
          {t('common:actions.save')}
        </Button>
      </DialogFooter>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('policy.confirmTitle')}
        description={t('policy.confirmBody')}
        confirmLabel={t('common:actions.save')}
        onConfirm={() => {
          setConfirmOpen(false)
          mutation.mutate(buildBody())
        }}
      />
    </>
  )
}
