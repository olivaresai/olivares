// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Plus, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'

/** One free-form connector setting: a key/value pair, optionally sealed as a secret. */
export interface CustomRow {
  key: string
  value: string
  secret: boolean
}

// CustomFields is a small key/value/secret-row editor for settings not described by
// the connector's schema (the only editor for an out-of-process plugin kind). Shared
// by the ConnectorsTab form and the onboarding wizard's source step (E4d), so a
// plugin kind is configurable wherever it is offered.
export function CustomFields({
  rows,
  onChange,
}: {
  rows: CustomRow[]
  onChange: (rows: CustomRow[]) => void
}) {
  const { t } = useTranslation(['console'])
  const set = (i: number, patch: Partial<CustomRow>) =>
    onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  return (
    <div className="flex flex-col gap-2">
      {rows.map((r, i) => (
        <div key={i} className="flex items-end gap-2">
          <div className="flex-1">
            <Input
              aria-label={t('console:connectors.customKey')}
              placeholder={t('console:connectors.customKey')}
              value={r.key}
              mono
              onChange={(e) => set(i, { key: e.target.value })}
            />
          </div>
          <div className="flex-1">
            <Input
              aria-label={t('console:connectors.customValue')}
              placeholder={t('console:connectors.customValue')}
              type={r.secret ? 'password' : 'text'}
              autoComplete="new-password"
              value={r.value}
              onChange={(e) => set(i, { value: e.target.value })}
            />
          </div>
          <label className="flex items-center gap-1 pb-2 text-xs text-muted-foreground">
            <Switch
              checked={r.secret}
              onCheckedChange={(v) => set(i, { secret: v })}
              aria-label={t('console:connectors.customSecret')}
            />
            {t('console:connectors.customSecret')}
          </label>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => onChange(rows.filter((_, j) => j !== i))}
            aria-label={t('console:connectors.customRemove')}
          >
            <Trash2 />
          </Button>
        </div>
      ))}
      <div>
        {/* type=button: inside the wizard's <form> a default submit button would
            submit-and-register instead of adding a row. */}
        <Button
          type="button"
          variant="secondary"
          size="sm"
          onClick={() =>
            onChange([...rows, { key: '', value: '', secret: false }])
          }
        >
          <Plus />
          {t('console:connectors.addField')}
        </Button>
      </div>
    </div>
  )
}
