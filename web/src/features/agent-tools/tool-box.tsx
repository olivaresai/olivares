// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// One boxed list per installed tool (a GNOME boxed list): the tool row, then a row per
// profile, then "+ Add profile". What the engine prints about the install (paths, the
// probe, the log) lives behind the tool's menu, not in the list.
import { MoreHorizontal, Plus } from 'lucide-react'
import { useId, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { MonoMark } from '@/components/ui/mono-mark'

export function ToolBox({
  driver,
  name,
  version,
  onUpdate,
  onDetails,
  updateDisabled,
  profilesHref,
  onAdd,
  children,
}: {
  driver: string
  name: string
  version?: string
  /** Plan the latest release of the tool (the page's install review). */
  onUpdate?: () => void
  updateDisabled?: boolean
  /** Open the install details sheet. */
  onDetails: () => void
  /** The detailed profiles page; omitted for a tool that has no profiles. */
  profilesHref?: string
  /** Open Add profile; omitted for a tool that has no profiles. */
  onAdd?: () => void
  /** The profile rows (`li`) or, for a service, its own panel. */
  children: ReactNode
}) {
  const { t } = useTranslation('agentTools')
  const id = useId()
  return (
    <section
      id={`tool-${driver}`}
      tabIndex={-1}
      aria-labelledby={id}
      data-testid={`tool-${driver}`}
      className="overflow-hidden rounded-card border border-line bg-surface"
    >
      <div className="flex items-center gap-3 px-4 py-3">
        <MonoMark name={name} className="size-6" />
        <h2 id={id} className="text-body-l font-semibold text-text">
          {name}
        </h2>
        {version ? (
          <span className="text-overline text-text-3">{version}</span>
        ) : null}
        <span className="flex-1" />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              aria-label={t('tool.menu', { name })}
              title={t('tool.menu', { name })}
            >
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            {onUpdate ? (
              <DropdownMenuItem disabled={updateDisabled} onSelect={onUpdate}>
                {t('tool.update')}
              </DropdownMenuItem>
            ) : null}
            <DropdownMenuItem onSelect={onDetails}>
              {t('tool.details')}
            </DropdownMenuItem>
            {profilesHref ? (
              <DropdownMenuItem asChild>
                <a href={profilesHref}>{t('tool.profilesPage')}</a>
              </DropdownMenuItem>
            ) : null}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <ul className="divide-y divide-line border-t border-line">
        {children}
        {onAdd ? (
          <li>
            <Button
              variant="ghost"
              className="h-12 w-full justify-start rounded-none px-4 text-text-2"
              onClick={onAdd}
              aria-label={t('profiles.addTo', { name })}
            >
              <Plus aria-hidden />
              {t('profiles.add')}
            </Button>
          </li>
        ) : null}
      </ul>
    </section>
  )
}
