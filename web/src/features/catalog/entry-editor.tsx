// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, ScrollText } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
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
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { useAuth } from '@/lib/auth/context'
import { looksLikeCredential } from '@/lib/credentials'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { catalogApi, catalogKeys } from './api'
import './i18n'
import { admissionKind, ENTRY_KINDS } from './types'
import type { EntryDTO, EntryInput, EntryKind } from './types'

export interface EntryEditorDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Existing entry to edit (draft only); omit/undefined to create. */
  entry?: EntryDTO | null
}

/**
 * EntryEditorDialog is the privileged create/edit form for a catalog entry. It NEVER
 * offers a raw secret input — the spec is free JSON edited by the operator, and it
 * warns (looksLikeCredential, mirroring the engine guard) when a spec value looks
 * like an embedded credential so secrets stay referenced by name/locator. The form
 * is the confirmation surface: it carries the draft + audit-ledger notice and a
 * deliberate submit, then runs the privileged mutation (invalidate → toast → close).
 * On WRITE it sends ONLY the allowed fields — lifecycle/integrity fields are
 * server-managed; status is forced to 'draft' on create.
 *
 * The form lives in a child that mounts fresh each time the dialog opens (Radix
 * unmounts closed content), so its initial state is seeded from props with plain
 * useState initializers — no resetting effect (react-hooks/set-state-in-effect).
 */
export function EntryEditorDialog({
  open,
  onOpenChange,
  entry,
}: EntryEditorDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        {open && (
          <EntryForm
            entry={entry ?? null}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function specToText(spec: Record<string, unknown> | undefined): string {
  if (!spec || Object.keys(spec).length === 0) return ''
  return JSON.stringify(spec, null, 2)
}

/** Sorted-key stringify — mirrors the backend's canonical marshalSpec (Go json.Marshal
 * sorts map keys) so a no-op reformat/reorder compares equal, not "changed". */
function stableStringify(v: unknown): string {
  if (v === null || typeof v !== 'object') return JSON.stringify(v) ?? 'null'
  if (Array.isArray(v)) return `[${v.map(stableStringify).join(',')}]`
  const o = v as Record<string, unknown>
  return `{${Object.keys(o)
    .sort()
    .map((k) => `${JSON.stringify(k)}:${stableStringify(o[k])}`)
    .join(',')}}`
}

/** Normalize an artifact digest the same way the connector gate does
 * (normalizeConnectorDigest): lowercase, strip an optional sha256: prefix, require
 * 64 hex chars — else "". So a case/prefix-only edit is not a "change". */
function normDigest(v: unknown): string {
  if (typeof v !== 'string') return ''
  const d = v
    .trim()
    .toLowerCase()
    .replace(/^sha256:/, '')
  return /^[0-9a-f]{64}$/.test(d) ? d : ''
}

/** Detect any string value in a (possibly nested) spec object that looks like a credential. */
function specHasCredential(value: unknown): boolean {
  if (typeof value === 'string') return looksLikeCredential(value)
  if (Array.isArray(value)) return value.some(specHasCredential)
  if (value && typeof value === 'object') {
    return Object.values(value as Record<string, unknown>).some(
      specHasCredential,
    )
  }
  return false
}

function EntryForm({
  entry,
  onClose,
}: {
  entry: EntryDTO | null
  onClose: () => void
}) {
  const { t } = useTranslation(['catalog', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = !!entry?.id

  const [kind, setKind] = useState<EntryKind>(entry?.kind ?? 'agent')
  const [name, setName] = useState(entry?.name ?? '')
  const [slug, setSlug] = useState(entry?.slug ?? '')
  const [version, setVersion] = useState(entry?.version ?? '')
  const [summary, setSummary] = useState(entry?.summary ?? '')
  const [ownerRef, setOwnerRef] = useState(entry?.owner_ref ?? '')
  const [specText, setSpecText] = useState(() => specToText(entry?.spec))

  // Parse the spec JSON once per render to drive validation + the credential guard.
  let specObj: Record<string, unknown> | undefined
  let specInvalid = false
  if (specText.trim()) {
    try {
      const parsed = JSON.parse(specText)
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        specObj = parsed as Record<string, unknown>
      } else {
        specInvalid = true
      }
    } catch {
      specInvalid = true
    }
  }
  const specWarn = !!specObj && specHasCredential(specObj)

  // Admission-stale warning (Item 4): editing the spec of an already-admitted
  // mcp/connector draft invalidates its recorded attestation verdict — the MCP gate
  // deletes it on save (invalidateMCPAdmission), the connector gate refuses a stale
  // digest at approve. Warn before the operator saves so the forced re-admit is not a
  // surprise. Keyed on the entry's STORED kind (the verdict belongs to it).
  const gatedKind = isEdit && entry ? admissionKind(entry.kind) : null
  const admissionQ = useQuery({
    queryKey: catalogKeys.admissions(
      activeTenant,
      gatedKind ?? 'mcp',
      entry?.id ?? '',
    ),
    queryFn: () => catalogApi.listAdmissions(gatedKind!, entry!.id!),
    enabled: !!gatedKind && !!entry?.id,
  })
  // ⛔ AQUI NO VA AVISO DE RECORTE, y es una decision, no un olvido. `admissionQ` no pinta
  //    ninguna lista en este editor: su unico consumo es el predicado de abajo, «¿hay ALGUN
  //    veredicto?». Ese predicado **no cambia con el recorte** — con cien admisiones de
  //    quinientas, `length > 0` sigue siendo cierto—, asi que un aviso aqui seria ruido sobre
  //    una decision que el recorte no altera, y un aviso que sale donde no importa ensena a
  //    ignorarlo donde si. El aviso vive donde la lista se VE: `admission-panel.tsx`.
  const hasVerdict = (admissionQ.data?.items?.length ?? 0) > 0
  // What actually invalidates the verdict differs by kind, so warn honestly:
  //   - mcp: ANY served-spec change (the gate deletes the verdict on save).
  //   - connector: ONLY a spec.artifact_digest change (the approve gate re-binds on
  //     that field; editing other fields keeps the verdict valid, so no false alarm).
  // Both compare the CANONICAL value (mirroring marshalSpec / normalizeConnectorDigest),
  // not raw text, so a reformat or a sha256:-case change raises no false alarm.
  const specChanged =
    stableStringify(specObj ?? null) !== stableStringify(entry?.spec ?? null)
  const digestChanged =
    normDigest(specObj?.artifact_digest) !==
    normDigest(entry?.spec?.artifact_digest)
  const invalidatingEdit =
    gatedKind === 'connector' ? digestChanged : specChanged
  const showAdmissionStale = !!gatedKind && hasVerdict && invalidatingEdit

  const valid =
    name.trim().length > 0 &&
    slug.trim().length > 0 &&
    version.trim().length > 0 &&
    !specInvalid &&
    !specWarn

  const mutation = usePrivilegedMutation<EntryInput, EntryDTO>({
    mutationFn: (input) =>
      isEdit
        ? catalogApi.updateEntry(entry!.id!, input)
        : catalogApi.createEntry(input),
    invalidateKeys: () => [
      catalogKeys.entries(activeTenant),
      ...(isEdit ? [catalogKeys.entry(activeTenant, entry!.id!)] : []),
      // An mcp served-spec edit deletes the admission verdict server-side (and a kind
      // flip can too); refresh the panel's admissions query so it never shows a stale
      // "verified" for a verdict the engine already removed.
      ...(isEdit && gatedKind
        ? [catalogKeys.admissions(activeTenant, gatedKind, entry!.id!)]
        : []),
    ],
    successMessage: isEdit ? t('editor.updated') : t('editor.created'),
    onDone: onClose,
  })

  function submit() {
    if (!valid) return
    // Send ONLY the allowed write fields — never status/hash/sig/approved_* (the
    // backend rejects unknown fields, and forces status='draft' on create).
    const payload: EntryInput = {
      kind,
      name: name.trim(),
      slug: slug.trim(),
      version: version.trim(),
      ...(summary.trim() ? { summary: summary.trim() } : {}),
      ...(ownerRef.trim() ? { owner_ref: ownerRef.trim() } : {}),
      ...(specObj ? { spec: specObj } : {}),
    }
    mutation.mutate(payload)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit ? t('editor.editTitle') : t('editor.createTitle')}
        </DialogTitle>
        <DialogDescription>{t('editor.draftNotice')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t('editor.kind')} htmlFor="entry-kind">
            <Select value={kind} onValueChange={(v) => setKind(v as EntryKind)}>
              <SelectTrigger id="entry-kind">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ENTRY_KINDS.map((k) => (
                  <SelectItem key={k} value={k}>
                    {t(`kind.${k}`, { defaultValue: k })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label={t('editor.name')} htmlFor="entry-name" required>
            <Input
              id="entry-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={t('editor.slug')}
            htmlFor="entry-slug"
            description={t('editor.slugHint')}
            required
          >
            <Input
              id="entry-slug"
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              mono
            />
          </Field>
          <Field
            label={t('editor.version')}
            htmlFor="entry-version"
            description={t('editor.versionHint')}
            required
          >
            <Input
              id="entry-version"
              value={version}
              onChange={(e) => setVersion(e.target.value)}
              mono
            />
          </Field>
        </div>

        <Field label={t('editor.summary')} htmlFor="entry-summary">
          <Textarea
            id="entry-summary"
            value={summary}
            onChange={(e) => setSummary(e.target.value)}
            rows={2}
          />
        </Field>

        <Field
          label={t('editor.ownerRef')}
          htmlFor="entry-owner"
          description={t('editor.ownerRefHint')}
        >
          <Input
            id="entry-owner"
            value={ownerRef}
            onChange={(e) => setOwnerRef(e.target.value)}
            mono
          />
        </Field>

        <Field
          label={t('editor.spec')}
          htmlFor="entry-spec"
          description={t('editor.specHint')}
          error={
            specInvalid
              ? t('editor.specInvalid')
              : specWarn
                ? t('editor.credentialWarning')
                : undefined
          }
        >
          <Textarea
            id="entry-spec"
            value={specText}
            onChange={(e) => setSpecText(e.target.value)}
            placeholder={t('editor.specPlaceholder')}
            aria-invalid={specInvalid || specWarn || undefined}
            rows={8}
            className="font-mono text-xs"
          />
        </Field>
      </div>

      {showAdmissionStale && (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-md border border-warning-line bg-warning-soft px-3 py-2 text-xs text-warning"
        >
          <AlertTriangle className="size-4 shrink-0" aria-hidden />
          <span>
            {t('editor.admissionStaleWarning', {
              kind: t(`kind.${entry!.kind}`, { defaultValue: entry!.kind }),
            })}
          </span>
        </div>
      )}

      <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <ScrollText className="size-3.5 shrink-0" aria-hidden />
        {t('common:privileged.auditedNotice')}
      </p>

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
          onClick={submit}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {isEdit ? t('editor.save') : t('editor.create')}
        </Button>
      </DialogFooter>
    </>
  )
}
