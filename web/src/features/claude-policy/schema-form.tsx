// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// SchemaForm — a typed form generated from the VERIFIED KeyDescriptor metadata, so
// every managed-* surface gets schema-true controls (boolean→Switch, enum→Select,
// string[]→tag input, string/number→Input) without bespoke per-key widgets. Two-way
// bound to the JSON document in the parent (the CodeEditor is the power-user view of
// the same object). managed-only keys carry an explicit marker; every key links to
// its authoritative source. No secret VALUE is ever collected here.
import { ExternalLink } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { cn } from '@/lib/utils'
import type { KeyDescriptor } from './schema'

type Obj = Record<string, unknown>

function getPath(obj: Obj | undefined, path: string): unknown {
  if (!obj) return undefined
  return path.split('.').reduce<unknown>((o, k) => {
    if (o && typeof o === 'object' && !Array.isArray(o)) {
      return (o as Obj)[k]
    }
    return undefined
  }, obj)
}

/** Immutably set (or, when value is undefined, delete) a dotted path. */
function setPath(obj: Obj, path: string, value: unknown): Obj {
  const keys = path.split('.')
  const root: Obj = { ...obj }
  let cursor: Obj = root
  for (let i = 0; i < keys.length - 1; i++) {
    const k = keys[i]!
    const existing = cursor[k]
    const next: Obj =
      existing && typeof existing === 'object' && !Array.isArray(existing)
        ? { ...(existing as Obj) }
        : {}
    cursor[k] = next
    cursor = next
  }
  const leaf = keys[keys.length - 1]!
  if (value === undefined) delete cursor[leaf]
  else cursor[leaf] = value
  return root
}

export interface SchemaFormProps {
  keys: readonly KeyDescriptor[]
  /** Parsed document object (undefined when the JSON is empty/invalid). */
  value: Obj | undefined
  /** Strip this prefix from descriptor keys before pathing (e.g. "sandbox."). */
  basePrefix?: string
  disabled?: boolean
  onChange: (next: Obj) => void
}

export function SchemaForm({
  keys,
  value,
  basePrefix = '',
  disabled,
  onChange,
}: SchemaFormProps) {
  const { t } = useTranslation('claudePolicy')
  const base: Obj = value ?? {}

  const rel = (key: string) =>
    basePrefix && key.startsWith(basePrefix)
      ? key.slice(basePrefix.length)
      : key

  const update = (key: string, v: unknown) =>
    onChange(setPath(base, rel(key), v))

  return (
    <div
      className="flex flex-col divide-y divide-border"
      role="group"
      aria-label={t('form.label')}
    >
      {keys.map((d) => {
        const path = rel(d.key)
        const current = getPath(base, path)
        const fieldId = `field-${d.key.replace(/[^a-z0-9]/gi, '-')}`
        return (
          <div key={d.key} className="flex flex-col gap-1.5 py-3">
            <div className="flex flex-wrap items-center gap-2">
              <Label htmlFor={fieldId} className="font-mono text-xs">
                {d.key}
              </Label>
              {d.scope === 'managed-only' && (
                <Badge variant="outline" className="text-[0.65rem]">
                  {t('form.managedOnly')}
                </Badge>
              )}
              {d.toConfirm && (
                <Badge variant="warning" className="text-[0.65rem]">
                  {t('form.toConfirm')}
                </Badge>
              )}
              <a
                href={d.source}
                target="_blank"
                rel="noreferrer"
                className="ml-auto inline-flex items-center gap-1 text-[0.7rem] text-muted-foreground hover:text-accent-text"
              >
                {t('form.docs')} <ExternalLink className="size-3" aria-hidden />
              </a>
            </div>
            <p
              id={`${fieldId}-summary`}
              className="text-xs text-muted-foreground"
            >
              {d.summary}
            </p>
            <SchemaField
              id={fieldId}
              descriptor={d}
              value={current}
              disabled={disabled}
              onChange={(v) => update(d.key, v)}
            />
          </div>
        )
      })}
    </div>
  )
}

function SchemaField({
  id,
  descriptor,
  value,
  disabled,
  onChange,
}: {
  id: string
  descriptor: KeyDescriptor
  value: unknown
  disabled?: boolean
  onChange: (v: unknown) => void
}) {
  const { t } = useTranslation('claudePolicy')

  if (descriptor.type === 'boolean') {
    return (
      <div className="flex items-center gap-2">
        <Switch
          id={id}
          checked={value === true}
          disabled={disabled}
          onCheckedChange={(c) => onChange(c ? true : undefined)}
        />
        <Label htmlFor={id} className="text-xs text-muted-foreground">
          {value === true ? t('form.enabled') : t('form.unset')}
        </Label>
      </div>
    )
  }

  if (descriptor.type === 'enum' && descriptor.enum) {
    return (
      <Select
        value={typeof value === 'string' ? value : ''}
        onValueChange={(v) => onChange(v || undefined)}
        disabled={disabled}
      >
        <SelectTrigger id={id} className="max-w-xs">
          <SelectValue placeholder={t('form.unset')} />
        </SelectTrigger>
        <SelectContent>
          {descriptor.enum.map((opt) => (
            <SelectItem key={opt} value={opt}>
              {opt}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    )
  }

  if (descriptor.type === 'string[]') {
    const arr = Array.isArray(value) ? (value as unknown[]).map(String) : []
    return (
      <Input
        id={id}
        mono
        disabled={disabled}
        value={arr.join(', ')}
        placeholder={t('form.listPlaceholder')}
        aria-describedby={`${id}-summary`}
        onChange={(e) => {
          const items = e.target.value
            .split(',')
            .map((s) => s.trim())
            .filter(Boolean)
          onChange(items.length ? items : undefined)
        }}
      />
    )
  }

  if (descriptor.type === 'number') {
    return (
      <Input
        id={id}
        type="number"
        disabled={disabled}
        className="max-w-xs"
        value={typeof value === 'number' ? String(value) : ''}
        onChange={(e) => {
          const n = e.target.value === '' ? undefined : Number(e.target.value)
          onChange(Number.isNaN(n) ? undefined : n)
        }}
      />
    )
  }

  // string (+ fallthrough). Inline-credential guard lives at the editor level; this
  // form only ever collects references/labels, never secret values.
  return (
    <Input
      id={id}
      mono
      disabled={disabled}
      className={cn('max-w-md')}
      value={typeof value === 'string' ? value : ''}
      onChange={(e) => onChange(e.target.value || undefined)}
    />
  )
}
