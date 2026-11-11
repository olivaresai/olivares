// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

const NO_LIMIT = '0'

/** The windows an administrator picks from, in the seconds the API stores. */
const DURATIONS = [
  { seconds: '3600', label: 'durations.h1' },
  { seconds: '14400', label: 'durations.h4' },
  { seconds: '28800', label: 'durations.h8' },
  { seconds: '86400', label: 'durations.h24' },
  { seconds: '604800', label: 'durations.d7' },
  { seconds: '2592000', label: 'durations.d30' },
] as const

/**
 * DurationSelect picks a time window from a menu, in human units; `value` and `onChange`
 * carry the seconds the API takes, '' or '0' being no limit. A stored window the menu
 * does not list stays selected as its own entry, so opening a policy never changes it.
 */
export function DurationSelect({
  id,
  value,
  onChange,
}: {
  id: string
  value: string
  onChange: (seconds: string) => void
}) {
  const { t } = useTranslation('governance')
  const selected = value.trim() === '' ? NO_LIMIT : value.trim()
  const listed =
    selected === NO_LIMIT || DURATIONS.some((d) => d.seconds === selected)

  return (
    <Select value={selected} onValueChange={onChange}>
      <SelectTrigger id={id}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={NO_LIMIT}>{t('durations.none')}</SelectItem>
        {DURATIONS.map((d) => (
          <SelectItem key={d.seconds} value={d.seconds}>
            {t(d.label)}
          </SelectItem>
        ))}
        {!listed && (
          <SelectItem value={selected}>
            {t('durations.seconds', { value: selected })}
          </SelectItem>
        )}
      </SelectContent>
    </Select>
  )
}
