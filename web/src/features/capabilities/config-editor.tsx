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
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { useAuth } from '@/lib/auth/context'
import { looksLikeCredential } from '@/lib/credentials'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { capabilitiesApi, capabilitiesKeys } from './api'
import './i18n'
import { REF_KINDS, TRANSPORTS } from './types'
import type {
  ConfigDTO,
  ConfigInput,
  RefKind,
  SecretRefDTO,
  Transport,
} from './types'

interface DraftSecret extends SecretRefDTO {
  /** Stable client key so React keys survive reordering/removal. */
  _k: string
}

let secretKeySeq = 0
function newSecret(): DraftSecret {
  secretKeySeq += 1
  return {
    _k: `s${secretKeySeq}`,
    name: '',
    ref_kind: 'env',
    ref: '',
    hint: '',
  }
}

export interface ConfigEditorDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Existing config to edit; omit/undefined to create. */
  config?: ConfigDTO | null
  /** Prefill server_ref on create (e.g. from a server detail). */
  serverRef?: string
}

/**
 * ConfigEditorDialog is the privileged create/edit form for a managed MCP config.
 * It NEVER offers a secret-value input — only references (name / where it lives /
 * locator / optional masked hint), and it warns when the endpoint or a locator
 * looks like a real credential. The form itself is the confirmation surface: it
 * carries the audit-ledger notice and a deliberate submit, then runs the privileged
 * mutation (invalidate → toast → close). server_ref is immutable on edit.
 *
 * The form lives in a child that mounts fresh each time the dialog opens (Radix
 * unmounts closed content), so its initial state is seeded from props with plain
 * useState initializers — no resetting effect.
 */
export function ConfigEditorDialog({
  open,
  onOpenChange,
  config,
  serverRef,
}: ConfigEditorDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        {open && (
          <ConfigForm
            config={config ?? null}
            serverRef={serverRef}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function ConfigForm({
  config,
  serverRef,
  onClose,
}: {
  config: ConfigDTO | null
  serverRef?: string
  onClose: () => void
}) {
  const { t } = useTranslation(['capabilities', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = !!config?.id

  const [serverRefValue, setServerRefValue] = useState(
    config?.server_ref ?? serverRef ?? '',
  )
  const [transport, setTransport] = useState<Transport>(
    config?.transport ?? 'stdio',
  )
  const [endpoint, setEndpoint] = useState(config?.endpoint ?? '')
  const [scope, setScope] = useState(config?.scope ?? '')
  const [enabled, setEnabled] = useState(config?.enabled ?? true)
  const [note, setNote] = useState(config?.note ?? '')
  const [secrets, setSecrets] = useState<DraftSecret[]>(() =>
    (config?.secret_refs ?? []).map((s) => ({
      ...s,
      _k: `e${secretKeySeq++}`,
    })),
  )

  const endpointWarn = looksLikeCredential(endpoint)
  const secretWarn = secrets.some((s) => looksLikeCredential(s.ref))
  const valid =
    serverRefValue.trim().length > 0 &&
    !endpointWarn &&
    !secretWarn &&
    secrets.every((s) => s.name.trim() && s.ref.trim())

  const mutation = usePrivilegedMutation<ConfigInput, ConfigDTO>({
    mutationFn: (input) =>
      isEdit
        ? capabilitiesApi.updateConfig(config!.id!, input)
        : capabilitiesApi.createConfig(input),
    invalidateKeys: () => [
      capabilitiesKeys.configs(activeTenant),
      capabilitiesKeys.servers(activeTenant),
      ...(isEdit ? [capabilitiesKeys.config(activeTenant, config!.id!)] : []),
    ],
    successMessage: isEdit ? t('editor.updated') : t('editor.created'),
    onDone: onClose,
  })

  function submit() {
    if (!valid) return
    const payload: ConfigInput = {
      server_ref: serverRefValue.trim(),
      transport,
      enabled,
      secret_refs: secrets.map(({ _k, ...rest }) => ({
        ...rest,
        hint: rest.hint?.trim() || undefined,
      })),
      ...(endpoint.trim() ? { endpoint: endpoint.trim() } : {}),
      ...(scope.trim() ? { scope: scope.trim() } : {}),
      ...(note.trim() ? { note: note.trim() } : {}),
    }
    mutation.mutate(payload)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit ? t('editor.editTitle') : t('editor.createTitle')}
        </DialogTitle>
        <DialogDescription>
          {isEdit
            ? t('editor.confirmUpdateBody', { server: serverRefValue })
            : t('editor.confirmCreateBody', { server: serverRefValue || '…' })}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={t('editor.serverRef')}
            htmlFor="cfg-server-ref"
            description={t('editor.serverRefHint')}
            required
          >
            <Input
              id="cfg-server-ref"
              value={serverRefValue}
              onChange={(e) => setServerRefValue(e.target.value)}
              disabled={isEdit}
              mono
            />
          </Field>
          <Field label={t('editor.transport')} htmlFor="cfg-transport">
            <Select
              value={transport}
              onValueChange={(v) => setTransport(v as Transport)}
            >
              <SelectTrigger id="cfg-transport">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {TRANSPORTS.map((tr) => (
                  <SelectItem key={tr} value={tr}>
                    {tr}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </div>

        <Field
          label={t('editor.endpoint')}
          htmlFor="cfg-endpoint"
          description={t('editor.endpointHint')}
          error={endpointWarn ? t('editor.credentialWarning') : undefined}
        >
          <Input
            id="cfg-endpoint"
            value={endpoint}
            onChange={(e) => setEndpoint(e.target.value)}
            aria-invalid={endpointWarn || undefined}
            mono
          />
        </Field>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={t('editor.scope')}
            htmlFor="cfg-scope"
            description={t('editor.scopeHint')}
          >
            <Input
              id="cfg-scope"
              value={scope}
              onChange={(e) => setScope(e.target.value)}
            />
          </Field>
          <div className="flex items-center gap-2 pt-6">
            <Switch
              id="cfg-enabled"
              checked={enabled}
              onCheckedChange={setEnabled}
            />
            <Label htmlFor="cfg-enabled">{t('editor.enabled')}</Label>
          </div>
        </div>

        <Field label={t('editor.note')} htmlFor="cfg-note">
          <Textarea
            id="cfg-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={2}
          />
        </Field>

        {/* Secret references — never values. */}
        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between">
            <Label>{t('editor.secretRefs')}</Label>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => setSecrets((s) => [...s, newSecret()])}
            >
              <Plus />
              {t('editor.addSecret')}
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            {t('editor.secretRefsHint')}
          </p>
          {secrets.length === 0 ? (
            <p className="rounded-md border border-dashed border-border px-3 py-2 text-xs text-muted-foreground">
              {t('editor.noSecrets')}
            </p>
          ) : (
            <div className="flex flex-col gap-3">
              {secrets.map((s, i) => {
                const warn = looksLikeCredential(s.ref)
                return (
                  <div
                    key={s._k}
                    className="grid grid-cols-1 gap-2 rounded-md border border-border bg-muted/40 p-2 sm:grid-cols-[1fr_1fr_auto]"
                  >
                    <Input
                      aria-label={t('editor.secretName')}
                      placeholder={t('editor.secretName')}
                      value={s.name}
                      onChange={(e) =>
                        setSecrets((arr) =>
                          arr.map((x, j) =>
                            j === i ? { ...x, name: e.target.value } : x,
                          ),
                        )
                      }
                      mono
                    />
                    <Select
                      value={s.ref_kind}
                      onValueChange={(v) =>
                        setSecrets((arr) =>
                          arr.map((x, j) =>
                            j === i ? { ...x, ref_kind: v as RefKind } : x,
                          ),
                        )
                      }
                    >
                      <SelectTrigger aria-label={t('editor.secretRefKind')}>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {REF_KINDS.map((k) => (
                          <SelectItem key={k} value={k}>
                            {t(`editor.refKind.${k}`)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      aria-label={t('editor.removeSecret')}
                      onClick={() =>
                        setSecrets((arr) => arr.filter((_, j) => j !== i))
                      }
                    >
                      <Trash2 />
                    </Button>
                    <Input
                      aria-label={t('editor.secretRef')}
                      placeholder={t('editor.secretRefPlaceholder')}
                      value={s.ref}
                      onChange={(e) =>
                        setSecrets((arr) =>
                          arr.map((x, j) =>
                            j === i ? { ...x, ref: e.target.value } : x,
                          ),
                        )
                      }
                      aria-invalid={warn || undefined}
                      mono
                      className="sm:col-span-2"
                    />
                    <Input
                      aria-label={t('editor.secretHint')}
                      placeholder={t('editor.secretHint')}
                      value={s.hint ?? ''}
                      maxLength={64}
                      onChange={(e) =>
                        setSecrets((arr) =>
                          arr.map((x, j) =>
                            j === i ? { ...x, hint: e.target.value } : x,
                          ),
                        )
                      }
                      className="sm:col-span-3"
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
