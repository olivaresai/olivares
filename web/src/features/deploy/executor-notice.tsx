// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import './i18n'

/** What Plan and Apply need and how an administrator connects it, in the words the
 * empty Definitions list uses. Shown above the list and in the detail sheet. */
export function NoExecutorNotice({ id }: { id?: string }) {
  const { t } = useTranslation('deploy')
  return (
    <div
      id={id}
      role="note"
      className="rounded-md border border-info-line bg-info-soft px-3 py-2 text-caption text-info"
    >
      <p className="font-medium">{t('noExecutor.title')}</p>
      <p>{t('noExecutor.description')}</p>
    </div>
  )
}
