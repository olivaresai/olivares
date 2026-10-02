// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Which values a provider may assert as multi-factor sign-in (ID, multi-IdP clarity). Each
// list is either the protocol's default rule (null) or exactly the values an administrator
// trusts; an empty exact list trusts none. The editor never prefills exact values: each one is
// an exception the administrator owns, not a restatement of the default rule.
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import type { AssuranceMapping } from './api'

export type MappingList = keyof AssuranceMapping

export const DEFAULT_MAPPING: AssuranceMapping = {
  amr: null,
  acr: null,
  saml_contexts: null,
}

/** The lists a protocol asserts: AMR and ACR for OIDC, authentication contexts for SAML. */
export function mappingLists(protocol: string): MappingList[] {
  return protocol === 'saml' ? ['saml_contexts'] : ['amr', 'acr']
}

const LABEL: Record<MappingList, string> = {
  amr: 'amr',
  acr: 'acr',
  saml_contexts: 'samlContexts',
}

/** One value per line; edges trimmed, blank lines dropped. */
export function parseValues(text: string): string[] {
  return text
    .split('\n')
    .map((v) => v.trim())
    .filter(Boolean)
}

const CONTROL = /\p{Cc}/u

/** The engine's rule for an exact list: at most 32 values, each at most 512 bytes, with no
 * wildcard and no control character. */
export function listProblem(values: string[] | null): boolean {
  if (values === null) return false
  if (values.length > 32) return true
  return values.some(
    (v) =>
      new TextEncoder().encode(v).length > 512 ||
      v.includes('*') ||
      CONTROL.test(v),
  )
}

export function mappingProblem(m: AssuranceMapping): boolean {
  return (Object.keys(DEFAULT_MAPPING) as MappingList[]).some((k) =>
    listProblem(m[k]),
  )
}

/** One list in words: the default rule, nothing, or the exact values. */
export function useListSummary(): (values: string[] | null) => string {
  const { t } = useTranslation('console')
  return (values) =>
    values === null
      ? t('sso.mapping.summary.default')
      : values.length === 0
        ? t('sso.mapping.summary.none')
        : t('sso.mapping.summary.values', { values: values.join(', ') })
}

/** The provider's mapping as the panel states it, one line per list its protocol uses. */
export function MappingSummary({
  protocol,
  mapping,
}: {
  protocol: string
  mapping?: AssuranceMapping | null
}) {
  const { t } = useTranslation('console')
  const summary = useListSummary()
  const m = mapping ?? DEFAULT_MAPPING
  return (
    <span className="flex flex-col">
      {mappingLists(protocol).map((k) => (
        <span key={k}>
          {t(`sso.mapping.${LABEL[k]}`)}: {summary(m[k])}
        </span>
      ))}
    </span>
  )
}

export function AssuranceMappingEditor({
  protocol,
  value,
  onChange,
}: {
  protocol: string
  value: AssuranceMapping
  onChange: (next: AssuranceMapping) => void
}) {
  const { t } = useTranslation('console')
  return (
    <div
      className="flex flex-col gap-3 border-t border-border pt-3"
      data-slot="sso-assurance-mapping"
    >
      <div>
        <h3 className="text-body font-medium text-foreground">
          {t('sso.mapping.title')}
        </h3>
        <p className="text-body text-muted-foreground">
          {t('sso.mapping.caption')}
        </p>
      </div>
      {mappingLists(protocol).map((k) => (
        <MappingListField
          key={k}
          list={k}
          values={value[k]}
          onChange={(v) => onChange({ ...value, [k]: v })}
        />
      ))}
      <div>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => onChange(DEFAULT_MAPPING)}
        >
          {t('sso.mapping.restore')}
        </Button>
      </div>
    </div>
  )
}

function MappingListField({
  list,
  values,
  onChange,
}: {
  list: MappingList
  values: string[] | null
  onChange: (next: string[] | null) => void
}) {
  const { t } = useTranslation('console')
  const [text, setText] = useState((values ?? []).join('\n'))
  const exact = values !== null
  const id = `sso-mapping-${list}`
  return (
    <div className="flex flex-col gap-1.5">
      <Field label={t(`sso.mapping.${LABEL[list]}`)} htmlFor={id}>
        <Select
          value={exact ? 'exact' : 'default'}
          onValueChange={(mode) =>
            onChange(mode === 'exact' ? parseValues(text) : null)
          }
        >
          <SelectTrigger id={id}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="default">
              {t('sso.mapping.mode.default')}
            </SelectItem>
            <SelectItem value="exact">{t('sso.mapping.mode.exact')}</SelectItem>
          </SelectContent>
        </Select>
      </Field>
      {exact ? (
        <Field
          label={t('sso.mapping.values')}
          htmlFor={`${id}-values`}
          description={t('sso.mapping.valuesHint')}
        >
          <Textarea
            id={`${id}-values`}
            value={text}
            onChange={(e) => {
              setText(e.target.value)
              onChange(parseValues(e.target.value))
            }}
            rows={3}
            mono
          />
        </Field>
      ) : null}
      {listProblem(values) ? (
        <p className="text-caption text-danger" role="alert">
          {t('sso.mapping.invalid')}
        </p>
      ) : null}
    </div>
  )
}
