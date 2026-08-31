// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ScrollText } from 'lucide-react'
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
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { knowledgeApi, knowledgeKeys } from './api'
import './i18n'
import {
  ENFORCEMENT_MODES,
  VALIDATION_MODES,
  type DataContractInput,
  type DataProductDTO,
  type DataProductInput,
  type EnforcementMode,
  type ValidationMode,
} from './types'

export interface DataProductEditorDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Existing product to edit; omit/undefined to create. */
  product?: DataProductDTO | null
}

/**
 * DataProductEditorDialog is the privileged create/edit form for a data product.
 * On create, it optionally creates a contract if a schema definition is provided.
 * The form lives in a child that mounts fresh each time the dialog opens.
 */
export function DataProductEditorDialog({
  open,
  onOpenChange,
  product,
}: DataProductEditorDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        {open && (
          <ProductForm
            product={product ?? null}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function ProductForm({
  product,
  onClose,
}: {
  product: DataProductDTO | null
  onClose: () => void
}) {
  const { t } = useTranslation(['knowledge', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = !!product?.id

  // Data product fields.
  const [name, setName] = useState(product?.name ?? '')
  const [description, setDescription] = useState(product?.description ?? '')
  const [ownerRef, setOwnerRef] = useState(product?.owner_ref ?? '')
  const [kbRef, setKbRef] = useState(product?.kb_ref ?? '')
  const [freshnessSla, setFreshnessSla] = useState(
    String(product?.freshness_sla_seconds ?? 86400),
  )
  const [availabilityTarget, setAvailabilityTarget] = useState(
    product?.availability_target ?? '',
  )
  const [enforcementMode, setEnforcementMode] = useState<EnforcementMode>(
    product?.enforcement_mode ?? 'observe',
  )
  const [tagsJson, setTagsJson] = useState(
    product?.tags ? JSON.stringify(product.tags, null, 2) : '',
  )

  // Contract fields (create-only).
  const [schemaJson, setSchemaJson] = useState('')
  const [validationMode, setValidationMode] = useState<ValidationMode>('strict')
  const [completenessThreshold, setCompletenessThreshold] = useState('80')
  const [freshnessOverride, setFreshnessOverride] = useState('0')
  const [contractNote, setContractNote] = useState('')

  const valid = name.trim().length > 0 && ownerRef.trim().length > 0

  const mutation = usePrivilegedMutation<DataProductInput, DataProductDTO>({
    mutationFn: (input) =>
      isEdit
        ? knowledgeApi.updateDataProduct(product!.id, input)
        : knowledgeApi.createDataProduct(input),
    invalidateKeys: () => [
      knowledgeKeys.dataProducts(activeTenant),
      ...(isEdit ? [knowledgeKeys.dataProduct(activeTenant, product!.id)] : []),
    ],
    successMessage: isEdit
      ? t('dataProducts.editProduct')
      : t('dataProducts.newProduct'),
    onDone: async (data) => {
      // If creating and a schema was provided, also create the contract.
      if (!isEdit && schemaJson.trim()) {
        try {
          const contractInput: DataContractInput = {
            schema_definition: JSON.parse(schemaJson),
            validation_mode: validationMode,
            completeness_threshold: Number(completenessThreshold) || 80,
            freshness_override_seconds: Number(freshnessOverride) || 0,
            note: contractNote.trim() || undefined,
          }
          await knowledgeApi.createContract(data.id, contractInput)
        } catch {
          // Contract creation failure is non-fatal; the product was created.
        }
      }
      onClose()
    },
  })

  function submit() {
    if (!valid) return
    let tags: Record<string, string> | undefined
    if (tagsJson.trim()) {
      try {
        tags = JSON.parse(tagsJson)
      } catch {
        return // invalid JSON
      }
    }
    const payload: DataProductInput = {
      name: name.trim(),
      description: description.trim() || undefined,
      owner_ref: ownerRef.trim(),
      kb_ref: kbRef.trim() || undefined,
      freshness_sla_seconds: Number(freshnessSla) || 86400,
      availability_target: availabilityTarget.trim() || undefined,
      enforcement_mode: enforcementMode,
      tags,
    }
    mutation.mutate(payload)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit
            ? t('dataProducts.editProduct')
            : t('dataProducts.newProduct')}
        </DialogTitle>
        <DialogDescription>{t('dataProducts.subtitle')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field label={t('dataProducts.name')} htmlFor="dp-name" required>
          <Input
            id="dp-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>

        <Field label={t('dataProducts.description')} htmlFor="dp-description">
          <Textarea
            id="dp-description"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            rows={2}
          />
        </Field>

        <Field label={t('dataProducts.owner')} htmlFor="dp-owner" required>
          <Input
            id="dp-owner"
            value={ownerRef}
            onChange={(e) => setOwnerRef(e.target.value)}
            mono
          />
        </Field>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t('dataProducts.kbBinding')} htmlFor="dp-kb">
            <Input
              id="dp-kb"
              value={kbRef}
              onChange={(e) => setKbRef(e.target.value)}
              placeholder={t('dataProducts.kbUnbound')}
              mono
            />
          </Field>

          <Field
            label={t('dataProducts.slaDays')}
            htmlFor="dp-sla"
            description="Seconds"
          >
            <Input
              id="dp-sla"
              type="number"
              value={freshnessSla}
              onChange={(e) => setFreshnessSla(e.target.value)}
              mono
            />
          </Field>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={t('dataProducts.availabilityTarget')}
            htmlFor="dp-avail"
          >
            <Input
              id="dp-avail"
              value={availabilityTarget}
              onChange={(e) => setAvailabilityTarget(e.target.value)}
              placeholder="99.9%"
            />
          </Field>

          <Field
            label={t('dataProducts.enforcementMode')}
            htmlFor="dp-enforcement"
          >
            <Select
              value={enforcementMode}
              onValueChange={(v) => setEnforcementMode(v as EnforcementMode)}
            >
              <SelectTrigger id="dp-enforcement">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ENFORCEMENT_MODES.map((m) => (
                  <SelectItem key={m} value={m}>
                    {t(`dataProducts.enforcement.${m}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </div>

        <Field label={t('dataProducts.tags')} htmlFor="dp-tags">
          <Textarea
            id="dp-tags"
            value={tagsJson}
            onChange={(e) => setTagsJson(e.target.value)}
            placeholder='{"team": "data-eng"}'
            rows={2}
            mono
          />
        </Field>

        {/* Contract section (create-only). */}
        {!isEdit && (
          <>
            <div className="mt-2 border-t border-border pt-4">
              <h4 className="mb-2 text-sm font-medium text-foreground">
                {t('dataProducts.contract.title')}
              </h4>
            </div>

            <Field
              label={t('dataProducts.contract.schema')}
              htmlFor="dp-schema"
            >
              <Textarea
                id="dp-schema"
                value={schemaJson}
                onChange={(e) => setSchemaJson(e.target.value)}
                placeholder='{"type": "object", "properties": {...}}'
                rows={4}
                mono
              />
            </Field>

            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label={t('dataProducts.contract.validationMode')}
                htmlFor="dp-val-mode"
              >
                <Select
                  value={validationMode}
                  onValueChange={(v) => setValidationMode(v as ValidationMode)}
                >
                  <SelectTrigger id="dp-val-mode">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {VALIDATION_MODES.map((m) => (
                      <SelectItem key={m} value={m}>
                        {t(`dataProducts.contract.modes.${m}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>

              <Field
                label={t('dataProducts.contract.completenessThreshold')}
                htmlFor="dp-completeness"
              >
                <Input
                  id="dp-completeness"
                  type="number"
                  min={0}
                  max={100}
                  value={completenessThreshold}
                  onChange={(e) => setCompletenessThreshold(e.target.value)}
                  mono
                />
              </Field>
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label={t('dataProducts.contract.freshnessOverride')}
                htmlFor="dp-freshness-override"
                description="Seconds (0 = use product SLA)"
              >
                <Input
                  id="dp-freshness-override"
                  type="number"
                  value={freshnessOverride}
                  onChange={(e) => setFreshnessOverride(e.target.value)}
                  mono
                />
              </Field>

              <Field
                label={t('dataProducts.contract.note')}
                htmlFor="dp-contract-note"
              >
                <Input
                  id="dp-contract-note"
                  value={contractNote}
                  onChange={(e) => setContractNote(e.target.value)}
                />
              </Field>
            </div>
          </>
        )}
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
          {isEdit ? t('common:actions.save') : t('dataProducts.newProduct')}
        </Button>
      </DialogFooter>
    </>
  )
}
