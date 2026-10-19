// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { Field } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

const DEFAULT = '__default__'

export function LaunchSpeedFields({
  driver,
  model,
  fastMode,
  serviceTier,
  onFastMode,
  onServiceTier,
}: {
  driver: string
  model?: { supports_fast_mode?: boolean; service_tiers?: string[] }
  fastMode?: boolean
  serviceTier?: string
  onFastMode: (value: boolean | undefined) => void
  onServiceTier: (value: string) => void
}) {
  const { t } = useTranslation('agentops')
  return (
    <div className="flex flex-wrap gap-3">
      {driver === 'claude' &&
      (model?.supports_fast_mode === true || fastMode !== undefined) ? (
        <Field label={t('create.fastMode')}>
          <Select
            value={fastMode === undefined ? DEFAULT : fastMode ? 'on' : 'off'}
            onValueChange={(value) =>
              onFastMode(value === DEFAULT ? undefined : value === 'on')
            }
          >
            <SelectTrigger aria-label={t('create.fastMode')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={DEFAULT}>
                {t('create.effortDefault')}
              </SelectItem>
              <SelectItem
                value="on"
                disabled={model?.supports_fast_mode !== true}
              >
                {t('create.speedOn')}
              </SelectItem>
              <SelectItem value="off">{t('create.speedOff')}</SelectItem>
            </SelectContent>
          </Select>
        </Field>
      ) : null}
      {driver === 'codex' && model?.service_tiers?.length ? (
        <Field label={t('create.serviceTier')}>
          <Select
            value={serviceTier ? `tier:${serviceTier}` : DEFAULT}
            onValueChange={(value) =>
              onServiceTier(value === DEFAULT ? '' : value.slice(5))
            }
          >
            <SelectTrigger aria-label={t('create.serviceTier')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={DEFAULT}>
                {t('create.effortDefault')}
              </SelectItem>
              {model.service_tiers.map((tier) => (
                <SelectItem key={tier} value={`tier:${tier}`}>
                  {tier}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      ) : null}
    </div>
  )
}
