// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Plus, ScrollText, Trash2 } from 'lucide-react'
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
import { Label } from '@/components/ui/label'
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
import { deployApi, deployKeys } from './api'
import './i18n'
import { SUBJECT_KINDS, WIRING_MODES } from './types'
import type {
  DefinitionCreateInput,
  DefinitionDTO,
  DefinitionUpdateInput,
  DeploySpec,
  EnvRef,
  SubjectKind,
  WiringMode,
  WiringSpec,
} from './types'

// --- draft rows (stable client keys so React keys survive reorder/removal) ----

interface DraftEnv extends EnvRef {
  _k: string
}
interface DraftWiring extends WiringSpec {
  _k: string
}
interface DraftResource {
  _k: string
  key: string
  value: string
}

let seq = 0
const k = () => `r${seq++}`
const newEnv = (): DraftEnv => ({ _k: k(), name: '', secret_ref: '' })
const newWiring = (): DraftWiring => ({
  _k: k(),
  resource_kind: '',
  resource_ref: '',
  mode: 'read',
  secret_ref: '',
})
const newResource = (): DraftResource => ({ _k: k(), key: '', value: '' })

export interface DefinitionEditorDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Existing definition to edit; omit/undefined to declare a new one. */
  definition?: DefinitionDTO | null
}

/**
 * DefinitionEditorDialog is the privileged declare/edit form for a deployment
 * definition. It NEVER offers a secret-value input — env_refs and wirings carry
 * only secret REFERENCES (<scheme>:<locator>), and it warns when a ref or the
 * command/image looks like a real credential. The form itself is the confirmation
 * surface: it carries the control-plane-only notice, the audit-ledger notice and a
 * deliberate submit, then runs the privileged mutation (invalidate → toast → close).
 *
 * The form lives in a child that mounts fresh each time the dialog opens (Radix
 * unmounts closed content), so its initial state is seeded from props with plain
 * useState initializers — no resetting effect (lint: react-hooks/set-state-in-effect).
 */
export function DefinitionEditorDialog({
  open,
  onOpenChange,
  definition,
}: DefinitionEditorDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] max-w-3xl overflow-y-auto">
        {open && (
          <DefinitionForm
            definition={definition ?? null}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function DefinitionForm({
  definition,
  onClose,
}: {
  definition: DefinitionDTO | null
  onClose: () => void
}) {
  const { t } = useTranslation(['deploy', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = !!definition?.id
  const spec = definition?.spec ?? null

  // Identity / placement fields (create-only for subject/name/environment).
  const [subjectKind, setSubjectKind] = useState<SubjectKind>(
    definition?.subject_kind ?? 'agent',
  )
  const [subjectRef, setSubjectRef] = useState(definition?.subject_ref ?? '')
  const [name, setName] = useState(definition?.name ?? '')
  const [environment, setEnvironment] = useState(definition?.environment ?? '')
  const [target, setTarget] = useState(definition?.target ?? '')
  const [runtime, setRuntime] = useState(definition?.runtime ?? '')
  const [sourceRef, setSourceRef] = useState(definition?.source_ref ?? '')
  const [note, setNote] = useState('')

  // Spec fields.
  const [image, setImage] = useState(spec?.image ?? '')
  const [command, setCommand] = useState(spec?.command ?? '')
  const [replicas, setReplicas] = useState(
    spec?.replicas != null ? String(spec.replicas) : '',
  )
  const [resources, setResources] = useState<DraftResource[]>(() =>
    Object.entries(spec?.resources ?? {}).map(([key, value]) => ({
      _k: k(),
      key,
      value,
    })),
  )
  const [envRefs, setEnvRefs] = useState<DraftEnv[]>(() =>
    (spec?.env_refs ?? []).map((e) => ({ ...e, _k: k() })),
  )
  const [wirings, setWirings] = useState<DraftWiring[]>(() =>
    (spec?.wirings ?? []).map((w) => ({
      ...w,
      secret_ref: w.secret_ref ?? '',
      _k: k(),
    })),
  )

  const commandWarn = looksLikeCredential(command) || looksLikeCredential(image)
  const credWarn =
    envRefs.some((e) => looksLikeCredential(e.secret_ref)) ||
    wirings.some((w) => looksLikeCredential(w.secret_ref))

  const replicasNum = replicas.trim() ? Number(replicas) : 0
  const replicasValid =
    !replicas.trim() || (Number.isInteger(replicasNum) && replicasNum >= 0)

  const baseValid =
    name.trim().length > 0 &&
    subjectRef.trim().length > 0 &&
    environment.trim().length > 0 &&
    target.trim().length > 0
  const rowsValid =
    envRefs.every((e) => e.name.trim() && e.secret_ref.trim()) &&
    wirings.every((w) => w.resource_kind.trim() && w.resource_ref.trim()) &&
    resources.every((r) => r.key.trim() && r.value.trim())
  const valid =
    baseValid && rowsValid && replicasValid && !commandWarn && !credWarn

  const mutation = usePrivilegedMutation<
    DefinitionCreateInput | DefinitionUpdateInput,
    DefinitionDTO
  >({
    mutationFn: (input) =>
      isEdit
        ? deployApi.updateDefinition(
            definition!.id,
            input as DefinitionUpdateInput,
          )
        : deployApi.createDefinition(input as DefinitionCreateInput),
    invalidateKeys: () => [
      deployKeys.definitions(activeTenant),
      ...(isEdit
        ? [
            deployKeys.definition(activeTenant, definition!.id),
            deployKeys.revisions(activeTenant, definition!.id),
          ]
        : []),
    ],
    successMessage: isEdit ? t('editor.updated') : t('editor.created'),
    onDone: onClose,
  })

  function buildSpec(): DeploySpec {
    const out: DeploySpec = {}
    if (image.trim()) out.image = image.trim()
    if (command.trim()) out.command = command.trim()
    if (replicas.trim()) out.replicas = replicasNum
    if (resources.length > 0) {
      out.resources = Object.fromEntries(
        resources.map((r) => [r.key.trim(), r.value.trim()]),
      )
    }
    if (envRefs.length > 0) {
      out.env_refs = envRefs.map((e) => ({
        name: e.name.trim(),
        secret_ref: e.secret_ref.trim(),
      }))
    }
    if (wirings.length > 0) {
      out.wirings = wirings.map((w) => ({
        resource_kind: w.resource_kind.trim(),
        resource_ref: w.resource_ref.trim(),
        mode: w.mode,
        ...(w.secret_ref?.trim() ? { secret_ref: w.secret_ref.trim() } : {}),
      }))
    }
    return out
  }

  function submit() {
    if (!valid) return
    const builtSpec = buildSpec()
    if (isEdit) {
      const payload: DefinitionUpdateInput = {
        spec: builtSpec,
        ...(target.trim() ? { target: target.trim() } : {}),
        ...(sourceRef.trim() ? { source_ref: sourceRef.trim() } : {}),
        ...(note.trim() ? { note: note.trim() } : {}),
      }
      mutation.mutate(payload)
    } else {
      const payload: DefinitionCreateInput = {
        subject_kind: subjectKind,
        subject_ref: subjectRef.trim(),
        name: name.trim(),
        environment: environment.trim(),
        target: target.trim(),
        spec: builtSpec,
        ...(runtime.trim() ? { runtime: runtime.trim() } : {}),
        ...(sourceRef.trim() ? { source_ref: sourceRef.trim() } : {}),
      }
      mutation.mutate(payload)
    }
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit ? t('editor.editTitle') : t('editor.createTitle')}
        </DialogTitle>
        <DialogDescription>
          {isEdit
            ? t('editor.confirmUpdateBody', { name: name || '…' })
            : t('editor.confirmCreateBody', { name: name || '…' })}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        {/* Identity / placement. */}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t('editor.subjectKind')} htmlFor="def-subject-kind">
            <Select
              value={subjectKind}
              onValueChange={(v) => setSubjectKind(v as SubjectKind)}
              disabled={isEdit}
            >
              <SelectTrigger id="def-subject-kind">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {SUBJECT_KINDS.map((sk) => (
                  <SelectItem key={sk} value={sk}>
                    {sk}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('editor.subjectRef')}
            htmlFor="def-subject-ref"
            description={t('editor.subjectRefHint')}
            required
          >
            <Input
              id="def-subject-ref"
              value={subjectRef}
              onChange={(e) => setSubjectRef(e.target.value)}
              disabled={isEdit}
              mono
            />
          </Field>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={t('editor.name')}
            htmlFor="def-name"
            description={t('editor.nameHint')}
            required
          >
            <Input
              id="def-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              disabled={isEdit}
              mono
            />
          </Field>
          <Field
            label={t('editor.environment')}
            htmlFor="def-env"
            description={t('editor.environmentHint')}
            required
          >
            <Input
              id="def-env"
              value={environment}
              onChange={(e) => setEnvironment(e.target.value)}
              disabled={isEdit}
              mono
            />
          </Field>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={t('editor.target')}
            htmlFor="def-target"
            description={t('editor.targetHint')}
            required
          >
            <Input
              id="def-target"
              value={target}
              onChange={(e) => setTarget(e.target.value)}
              mono
            />
          </Field>
          <Field
            label={t('editor.runtime')}
            htmlFor="def-runtime"
            description={t('editor.runtimeHint')}
          >
            <Input
              id="def-runtime"
              value={runtime}
              onChange={(e) => setRuntime(e.target.value)}
              disabled={isEdit}
              mono
            />
          </Field>
        </div>

        <Field
          label={t('editor.sourceRef')}
          htmlFor="def-source-ref"
          description={t('editor.sourceRefHint')}
        >
          <Input
            id="def-source-ref"
            value={sourceRef}
            onChange={(e) => setSourceRef(e.target.value)}
            mono
          />
        </Field>

        {isEdit && (
          <Field
            label={t('editor.note')}
            htmlFor="def-note"
            description={t('editor.noteHint')}
          >
            <Textarea
              id="def-note"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              rows={2}
            />
          </Field>
        )}

        <div className="border-t border-border pt-3">
          <h3 className="text-sm font-medium text-foreground">
            {t('editor.specSection')}
          </h3>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={t('editor.image')}
            htmlFor="def-image"
            description={t('editor.imageHint')}
            error={
              looksLikeCredential(image)
                ? t('editor.credentialWarning')
                : undefined
            }
          >
            <Input
              id="def-image"
              value={image}
              onChange={(e) => setImage(e.target.value)}
              aria-invalid={looksLikeCredential(image) || undefined}
              mono
            />
          </Field>
          <Field
            label={t('editor.replicas')}
            htmlFor="def-replicas"
            error={!replicasValid ? t('common:validation.required') : undefined}
          >
            <Input
              id="def-replicas"
              type="number"
              min={0}
              value={replicas}
              onChange={(e) => setReplicas(e.target.value)}
              aria-invalid={!replicasValid || undefined}
            />
          </Field>
        </div>

        <Field
          label={t('editor.command')}
          htmlFor="def-command"
          description={t('editor.commandHint')}
          error={
            looksLikeCredential(command)
              ? t('editor.credentialWarning')
              : undefined
          }
        >
          <Input
            id="def-command"
            value={command}
            onChange={(e) => setCommand(e.target.value)}
            aria-invalid={looksLikeCredential(command) || undefined}
            mono
          />
        </Field>

        {/* Resources (key/value, non-sensitive). */}
        <RowEditor
          label={t('editor.resources')}
          hint={t('editor.resourcesHint')}
          addLabel={t('editor.addResource')}
          emptyLabel=""
          rows={resources}
          onAdd={() => setResources((a) => [...a, newResource()])}
          onRemove={(i) => setResources((a) => a.filter((_, j) => j !== i))}
          renderRow={(r, i) => (
            <>
              <Input
                aria-label={t('editor.resourceKey')}
                placeholder={t('editor.resourceKey')}
                value={r.key}
                onChange={(e) =>
                  setResources((a) =>
                    a.map((x, j) =>
                      j === i ? { ...x, key: e.target.value } : x,
                    ),
                  )
                }
                mono
              />
              <Input
                aria-label={t('editor.resourceValue')}
                placeholder={t('editor.resourceValue')}
                value={r.value}
                onChange={(e) =>
                  setResources((a) =>
                    a.map((x, j) =>
                      j === i ? { ...x, value: e.target.value } : x,
                    ),
                  )
                }
                mono
              />
            </>
          )}
        />

        {/* Env references — secret REFERENCES only, never values. */}
        <RowEditor
          label={t('editor.envRefs')}
          hint={t('editor.envRefsHint')}
          addLabel={t('editor.addEnvRef')}
          emptyLabel={t('editor.noEnvRefs')}
          rows={envRefs}
          onAdd={() => setEnvRefs((a) => [...a, newEnv()])}
          onRemove={(i) => setEnvRefs((a) => a.filter((_, j) => j !== i))}
          renderRow={(e, i) => {
            const warn = looksLikeCredential(e.secret_ref)
            return (
              <>
                <Input
                  aria-label={t('editor.envName')}
                  placeholder={t('editor.envName')}
                  value={e.name}
                  onChange={(ev) =>
                    setEnvRefs((a) =>
                      a.map((x, j) =>
                        j === i ? { ...x, name: ev.target.value } : x,
                      ),
                    )
                  }
                  mono
                />
                <Input
                  aria-label={t('editor.envSecretRef')}
                  placeholder={t('editor.secretRefPlaceholder')}
                  value={e.secret_ref}
                  onChange={(ev) =>
                    setEnvRefs((a) =>
                      a.map((x, j) =>
                        j === i ? { ...x, secret_ref: ev.target.value } : x,
                      ),
                    )
                  }
                  aria-invalid={warn || undefined}
                  mono
                />
                {warn && (
                  <p role="alert" className="text-xs text-danger sm:col-span-2">
                    {t('editor.credentialWarning')}
                  </p>
                )}
              </>
            )
          }}
        />

        {/* Wirings — agent→resource with an optional secret REFERENCE. */}
        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between">
            <Label>{t('editor.wirings')}</Label>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => setWirings((a) => [...a, newWiring()])}
            >
              <Plus />
              {t('editor.addWiring')}
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            {t('editor.wiringsHint')}
          </p>
          {wirings.length === 0 ? (
            <p className="rounded-md border border-dashed border-border px-3 py-2 text-xs text-muted-foreground">
              {t('editor.noWirings')}
            </p>
          ) : (
            <div className="flex flex-col gap-3">
              {wirings.map((w, i) => {
                const warn = looksLikeCredential(w.secret_ref)
                return (
                  <div
                    key={w._k}
                    className="grid grid-cols-1 gap-2 rounded-md border border-border bg-muted/40 p-2 sm:grid-cols-[1fr_1fr_auto]"
                  >
                    <Input
                      aria-label={t('editor.wiringResourceKind')}
                      placeholder={t('editor.wiringResourceKind')}
                      value={w.resource_kind}
                      onChange={(e) =>
                        setWirings((a) =>
                          a.map((x, j) =>
                            j === i
                              ? { ...x, resource_kind: e.target.value }
                              : x,
                          ),
                        )
                      }
                      mono
                    />
                    <Input
                      aria-label={t('editor.wiringResourceRef')}
                      placeholder={t('editor.wiringResourceRef')}
                      value={w.resource_ref}
                      onChange={(e) =>
                        setWirings((a) =>
                          a.map((x, j) =>
                            j === i
                              ? { ...x, resource_ref: e.target.value }
                              : x,
                          ),
                        )
                      }
                      mono
                    />
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      aria-label={t('editor.remove')}
                      onClick={() =>
                        setWirings((a) => a.filter((_, j) => j !== i))
                      }
                    >
                      <Trash2 />
                    </Button>
                    <Select
                      value={w.mode}
                      onValueChange={(v) =>
                        setWirings((a) =>
                          a.map((x, j) =>
                            j === i ? { ...x, mode: v as WiringMode } : x,
                          ),
                        )
                      }
                    >
                      <SelectTrigger aria-label={t('editor.wiringMode')}>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {WIRING_MODES.map((m) => (
                          <SelectItem key={m} value={m}>
                            {t(`spec.mode.${m}`, { defaultValue: m })}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <Input
                      aria-label={t('editor.wiringSecretRef')}
                      placeholder={t('editor.secretRefPlaceholder')}
                      value={w.secret_ref ?? ''}
                      onChange={(e) =>
                        setWirings((a) =>
                          a.map((x, j) =>
                            j === i ? { ...x, secret_ref: e.target.value } : x,
                          ),
                        )
                      }
                      aria-invalid={warn || undefined}
                      mono
                      className="sm:col-span-2"
                    />
                    {warn && (
                      <p
                        role="alert"
                        className="text-xs text-danger sm:col-span-3"
                      >
                        {t('editor.credentialWarning')}
                      </p>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </div>
      </div>

      <p className="rounded-md border border-info-line bg-info-soft px-3 py-2 text-xs text-info">
        {t('editor.controlPlaneNote')}
      </p>
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

/** A small add/remove list editor for two-column rows (resources / env refs). */
function RowEditor<T extends { _k: string }>({
  label,
  hint,
  addLabel,
  emptyLabel,
  rows,
  onAdd,
  onRemove,
  renderRow,
}: {
  label: string
  hint: string
  addLabel: string
  emptyLabel: string
  rows: T[]
  onAdd: () => void
  onRemove: (i: number) => void
  renderRow: (row: T, i: number) => React.ReactNode
}) {
  const { t } = useTranslation('deploy')
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <Label>{label}</Label>
        <Button type="button" variant="ghost" size="sm" onClick={onAdd}>
          <Plus />
          {addLabel}
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">{hint}</p>
      {rows.length === 0 ? (
        emptyLabel ? (
          <p className="rounded-md border border-dashed border-border px-3 py-2 text-xs text-muted-foreground">
            {emptyLabel}
          </p>
        ) : null
      ) : (
        <div className="flex flex-col gap-2">
          {rows.map((row, i) => (
            <div
              key={row._k}
              className="grid grid-cols-1 gap-2 rounded-md border border-border bg-muted/40 p-2 sm:grid-cols-[1fr_1fr_auto]"
            >
              {renderRow(row, i)}
              <Button
                type="button"
                variant="ghost"
                size="icon"
                aria-label={t('editor.remove')}
                onClick={() => onRemove(i)}
              >
                <Trash2 />
              </Button>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
