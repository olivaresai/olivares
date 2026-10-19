// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Add or edit one vault item: a name, what kind it is, and the fields of that kind. The
// password can be generated; nothing typed here is kept once the dialog closes.
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Dices, Eye, EyeOff, SlidersHorizontal } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { Segmented } from '@/components/ui/segmented'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  DEFAULT_PASSWORD_OPTIONS,
  vaultApi,
  vaultKeys,
  type PasswordOptions,
} from './api'
import {
  LABEL_RE,
  VAULT_KINDS,
  entryName,
  type VaultItem,
  type VaultKind,
  type VaultRow,
} from './model'
import './i18n'

export interface ItemFormProps {
  userId: string
  /** The item being edited, already revealed; omitted when adding. */
  existing?: { row: VaultRow; item: VaultItem }
  /** Labels already in the vault, so adding never silently replaces one. */
  taken: ReadonlySet<string>
  onCancel: () => void
  onSaved: () => void
}

/** Drops what a closed form left in the mutation cache: its variables hold the item. */
function useForgetMutations(key: readonly unknown[]) {
  const qc = useQueryClient()
  useEffect(
    () => () => {
      const cache = qc.getMutationCache()
      for (const m of cache.findAll({ mutationKey: key })) cache.remove(m)
    },
    [qc, key],
  )
}

export function ItemForm({
  userId,
  existing,
  taken,
  onCancel,
  onSaved,
}: ItemFormProps) {
  const { t } = useTranslation(['vault', 'common'])
  const boundary = useAuthBoundary()
  const isEdit = !!existing
  const [label, setLabel] = useState(existing?.row.label ?? '')
  const [type, setType] = useState<VaultKind>(existing?.item.type ?? 'login')
  const [username, setUsername] = useState(existing?.item.username ?? '')
  const [password, setPassword] = useState(existing?.item.password ?? '')
  const [url, setUrl] = useState(existing?.item.url ?? '')
  const [totp, setTotp] = useState(existing?.item.totp ?? '')
  const [note, setNote] = useState(existing?.item.note ?? '')
  const [description, setDescription] = useState(
    existing?.row.description ?? '',
  )
  const [showPassword, setShowPassword] = useState(false)
  const [options, setOptions] = useState<PasswordOptions>(
    DEFAULT_PASSWORD_OPTIONS,
  )

  const mutationKey = useMemo(
    () => ['vault', boundary.tenant, boundary.epoch, 'put'] as const,
    [boundary.tenant, boundary.epoch],
  )
  useForgetMutations(mutationKey)
  const save = usePrivilegedMutation<
    { name: string; item: VaultItem; description: string },
    unknown
  >({
    mutationKey,
    mutationFn: async (v, authority) => {
      const opts = { ...authority, tenant: boundary.tenant ?? undefined }
      if (!isEdit) {
        // The list on screen may be stale (another tab): saving over a name that exists
        // would replace that item, so look again just before writing.
        const current = await vaultApi.list(opts)
        if (current.secrets.some((s) => s.name === v.name))
          throw new Error(t('vault:form.nameTaken'))
      }
      return vaultApi.put(v.name, v.item, v.description, opts)
    },
    invalidateKeys: () => [vaultKeys.list(boundary.tenant, boundary.epoch)],
    successMessage: isEdit ? t('vault:saved') : t('vault:created'),
    onDone: onSaved,
  })

  const generate = useMutation({
    gcTime: 0, // the answer is a password: keep it only in the field
    mutationFn: () =>
      vaultApi.generate(options, { tenant: boundary.tenant ?? undefined }),
    onSuccess: (res) => {
      setPassword(res.password)
      setShowPassword(true)
    },
    onError: (err) =>
      toast.error(t('vault:form.generateFailed'), {
        description: err instanceof Error ? err.message : undefined,
      }),
  })

  const labelTaken = !isEdit && taken.has(label.trim())
  const labelError = label !== '' && !LABEL_RE.test(label.trim())
  const filled =
    type === 'login'
      ? username.trim() !== '' || password !== ''
      : type === 'note'
        ? note.trim() !== ''
        : totp.trim() !== ''
  const valid = (isEdit || LABEL_RE.test(label.trim())) && !labelTaken && filled

  const submit = () => {
    const item: VaultItem = { type }
    if (type === 'login') {
      item.username = username.trim()
      item.password = password
      item.url = url.trim()
    }
    if (type !== 'note') item.totp = totp.trim()
    item.note = note
    save.mutate({
      name: existing?.row.name ?? entryName(userId, label.trim()),
      item,
      description: description.trim(),
    })
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit
            ? t('vault:form.editTitle', { label: existing.row.label })
            : t('vault:form.addTitle')}
        </DialogTitle>
      </DialogHeader>
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault()
          if (valid && !save.isPending) submit()
        }}
      >
        {!isEdit && (
          <Field
            label={t('vault:form.name')}
            htmlFor="vault-name"
            description={t('vault:form.nameHint')}
            error={
              labelTaken
                ? t('vault:form.nameTaken')
                : labelError
                  ? t('vault:form.nameInvalid')
                  : undefined
            }
            required
          >
            <Input
              id="vault-name"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              autoFocus
              mono
              autoComplete="off"
              placeholder="bank"
            />
          </Field>
        )}
        {/* The kind of an existing item is fixed: switching it would drop fields on save. */}
        {!isEdit && (
          <Segmented
            aria-label={t('vault:form.type')}
            value={type}
            onValueChange={setType}
            options={VAULT_KINDS.map((kind) => ({
              value: kind,
              label: t(`vault:kind.${kind}`),
            }))}
          />
        )}
        {type === 'login' && (
          <>
            <Field label={t('vault:form.username')} htmlFor="vault-username">
              <Input
                id="vault-username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoComplete="off"
              />
            </Field>
            {/* The input is not the Field's only child: the render prop hands its id and
                labelling to the input, or the wrapper takes them and the input has no name. */}
            <Field label={t('vault:form.password')} htmlFor="vault-password">
              {(control) => (
                <div className="flex gap-2">
                  <Input
                    {...control}
                    type={showPassword ? 'text' : 'password'}
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    autoComplete="new-password"
                    mono
                  />
                  <Button
                    type="button"
                    variant="secondary"
                    size="icon"
                    aria-label={t(
                      showPassword ? 'vault:form.hide' : 'vault:form.show',
                    )}
                    onClick={() => setShowPassword((v) => !v)}
                  >
                    {showPassword ? <EyeOff /> : <Eye />}
                  </Button>
                  <Button
                    type="button"
                    variant="secondary"
                    disabled={generate.isPending}
                    onClick={() => generate.mutate()}
                  >
                    {generate.isPending ? (
                      <Spinner size="sm" aria-hidden />
                    ) : (
                      <Dices />
                    )}
                    {t('vault:form.generate')}
                  </Button>
                  <GeneratorOptions value={options} onChange={setOptions} />
                </div>
              )}
            </Field>
            <Field label={t('vault:form.url')} htmlFor="vault-url">
              <Input
                id="vault-url"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                autoComplete="off"
                placeholder="https://"
              />
            </Field>
          </>
        )}
        {type !== 'note' && (
          <Field
            label={t('vault:form.totp')}
            htmlFor="vault-totp"
            description={t('vault:form.totpHint')}
            required={type === 'totp'}
          >
            <Input
              id="vault-totp"
              value={totp}
              onChange={(e) => setTotp(e.target.value)}
              autoComplete="off"
              mono
            />
          </Field>
        )}
        <Field
          label={t('vault:form.note')}
          htmlFor="vault-note"
          required={type === 'note'}
        >
          <Textarea
            id="vault-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={type === 'note' ? 5 : 2}
          />
        </Field>
        <Field
          label={t('vault:form.description')}
          htmlFor="vault-description"
          description={t('vault:form.descriptionHint')}
        >
          <Input
            id="vault-description"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            autoComplete="off"
          />
        </Field>
        <DialogFooter>
          <Button
            type="button"
            variant="secondary"
            onClick={onCancel}
            disabled={save.isPending}
          >
            {t('common:actions.cancel')}
          </Button>
          <Button
            type="submit"
            variant="primary"
            disabled={!valid || save.isPending}
          >
            {save.isPending && <Spinner size="sm" aria-hidden />}
            {t('vault:form.save')}
          </Button>
        </DialogFooter>
      </form>
    </>
  )
}

/** The length and character kinds of a generated password. */
function GeneratorOptions({
  value,
  onChange,
}: {
  value: PasswordOptions
  onChange: (next: PasswordOptions) => void
}) {
  const { t } = useTranslation('vault')
  const [draft, setDraft] = useState(String(value.length))
  const kinds = ['lower', 'upper', 'digits', 'symbols'] as const
  // At least one kind must stay chosen: the engine refuses a password of nothing.
  const chosen = kinds.filter((k) => value[k]).length
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="secondary"
          size="icon"
          aria-label={t('form.generateOptions')}
        >
          <SlidersHorizontal />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-64">
        <Field label={t('form.length')} htmlFor="vault-gen-length">
          <Input
            id="vault-gen-length"
            type="number"
            min={8}
            max={128}
            value={draft}
            onChange={(e) => {
              // Typing "32" passes through "3": only a length in range is taken while typing,
              // and the field is brought into range when it loses focus.
              setDraft(e.target.value)
              const n = Number(e.target.value)
              if (Number.isInteger(n) && n >= 8 && n <= 128)
                onChange({ ...value, length: n })
            }}
            onBlur={() => {
              const n = Math.min(
                128,
                Math.max(8, Math.trunc(Number(draft)) || 8),
              )
              setDraft(String(n))
              onChange({ ...value, length: n })
            }}
          />
        </Field>
        <div className="mt-3 flex flex-col gap-2">
          {kinds.map((kind) => (
            <label key={kind} className="flex items-center gap-2 text-body">
              <Checkbox
                checked={value[kind]}
                disabled={value[kind] && chosen === 1}
                onCheckedChange={(v) =>
                  onChange({ ...value, [kind]: v === true })
                }
              />
              {t(`form.${kind}`)}
            </label>
          ))}
        </div>
      </PopoverContent>
    </Popover>
  )
}
