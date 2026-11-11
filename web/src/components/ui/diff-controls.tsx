// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { usePreferencesStore } from '@/stores/preferences'
import { Button } from './button'
import { Segmented } from './segmented'
import '@/features/shared/i18n'

/** Shared by complete-file and source-patch viewers; saved with the user's preferences. */
export function DiffControls() {
  const { t } = useTranslation('shared')
  const display = usePreferencesStore((s) => s.diffDisplay)
  const setDisplay = usePreferencesStore((s) => s.setDiffDisplay)
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Segmented
        size="sm"
        aria-label={t('diff.layout')}
        value={display.layout}
        onValueChange={(layout) => setDisplay({ layout })}
        options={[
          { value: 'unified', label: t('diff.unified') },
          { value: 'split', label: t('diff.split') },
        ]}
      />
      <Button
        type="button"
        size="sm"
        variant={display.wrap ? 'secondary' : 'ghost'}
        aria-pressed={display.wrap}
        onClick={() => setDisplay({ wrap: !display.wrap })}
      >
        {t('diff.wrap')}
      </Button>
      <Button
        type="button"
        size="sm"
        variant={display.whitespace ? 'secondary' : 'ghost'}
        aria-pressed={display.whitespace}
        onClick={() => setDisplay({ whitespace: !display.whitespace })}
      >
        {t('diff.whitespace')}
      </Button>
    </div>
  )
}
