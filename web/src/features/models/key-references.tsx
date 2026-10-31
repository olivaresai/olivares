// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Governed references to provider keys and workspaces: a masked hint, never a secret. They
// sit in AI tools beside the API keys sessions run on (#3388), so a provider key
// has one destination; their read and write tiers are the models module's own.
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
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
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import {
  AsyncSection,
  ListTruncationBadge,
  IntelNotice,
  SectionCard,
} from '@/features/_intel'
import { EVIDENCE_PAGE, modelsApi, modelsKeys } from './api'
import { KeyRefsTable } from './components'
import './i18n'

/** The anchor /models?tab=keys lands on (features/registry.tsx TAB_ALIASES). */
export const KEY_REFERENCES_ANCHOR = 'provider-key-references'

export function ProviderKeyReferences() {
  const { t } = useTranslation('models')
  const { activeTenant, can } = useAuth()
  const params = { limit: EVIDENCE_PAGE }
  const keysQ = useQuery({
    queryKey: modelsKeys.keys(activeTenant, params),
    queryFn: () => modelsApi.keys(params),
  })
  const canKeys = can('models:keys:write')
  const [keyOpen, setKeyOpen] = useState(false)
  return (
    <div id={KEY_REFERENCES_ANCHOR} className="scroll-mt-4">
      <SectionCard
        title={t('keys.title')}
        description={t('keys.description')}
        actions={
          canKeys ? (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setKeyOpen(true)}
            >
              <Plus />
              {t('keys.new')}
            </Button>
          ) : null
        }
      >
        <IntelNotice tone="info" className="mb-3">
          {t('keys.maskedOnly')}
        </IntelNotice>
        <ListTruncationBadge
          query={keysQ}
          label={t('keys.truncated', {
            n: keysQ.data?.items?.length ?? 0,
          })}
          hint={t('keys.truncatedHint')}
        />
        <AsyncSection query={keysQ} skeletonHeight={180}>
          {(list) =>
            list.items.length === 0 ? (
              <EmptyState
                description={t('keys.emptyHint')}
                title={t('keys.empty')}
              />
            ) : (
              <KeyRefsTable keys={list.items} />
            )
          }
        </AsyncSection>
      </SectionCard>
      {canKeys ? <KeyDialog open={keyOpen} onOpenChange={setKeyOpen} /> : null}
    </div>
  )
}

// --- key reference dialog (NEVER a secret) -----------------------------------

function KeyDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation(['models', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [provider, setProvider] = useState('')
  const [kind, setKind] = useState('api_key')
  const [extId, setExtId] = useState('')
  const [owner, setOwner] = useState('')

  const create = useMutation({
    mutationFn: () =>
      modelsApi.createKey({
        ref_kind: kind,
        provider_ref: provider.trim(),
        ext_id: extId.trim(),
        name: name.trim(),
        owner_ref: owner.trim() || undefined,
      }),
    onSuccess: () => {
      toast.success(t('keys.dialog.created'))
      void qc.invalidateQueries({ queryKey: modelsKeys.keys(activeTenant) })
      onOpenChange(false)
      setName('')
      setProvider('')
      setExtId('')
      setOwner('')
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  const valid = name.trim() && provider.trim() && extId.trim()

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('keys.dialog.title')}</DialogTitle>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid) create.mutate()
          }}
        >
          <IntelNotice tone="info">{t('keys.dialog.noSecretNote')}</IntelNotice>
          <Field label={t('keys.dialog.name')} required>
            {({ id }) => (
              <Input
                id={id}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            )}
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('keys.dialog.provider')} required>
              {({ id }) => (
                <Input
                  id={id}
                  value={provider}
                  onChange={(e) => setProvider(e.target.value)}
                />
              )}
            </Field>
            <Field label={t('keys.dialog.kind')}>
              <Select value={kind} onValueChange={setKind}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="api_key">
                    {t('keys.kind.api_key')}
                  </SelectItem>
                  <SelectItem value="workspace">
                    {t('keys.kind.workspace')}
                  </SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </div>
          <Field
            label={t('keys.dialog.extId')}
            description={t('keys.dialog.extIdHint')}
            required
          >
            {({ id }) => (
              <Input
                id={id}
                value={extId}
                onChange={(e) => setExtId(e.target.value)}
              />
            )}
          </Field>
          <Field label={t('keys.dialog.owner')}>
            {({ id }) => (
              <Input
                id={id}
                value={owner}
                onChange={(e) => setOwner(e.target.value)}
              />
            )}
          </Field>
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!valid || create.isPending}
            >
              {t('keys.dialog.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
