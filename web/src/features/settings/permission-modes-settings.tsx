// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import {
  PERMISSION_CHOICES,
  PERMISSION_NAME,
  type PermissionChoice,
} from '@/features/agentops/session-launch'
import '@/features/first-hour/i18n'
import table from './permission-modes.gen.json'

// The table the engine writes from the code that builds each tool's launch
// (modules/sessions permission_modes.go); a test fails when this file is stale.
interface PermissionCell {
  driver: string
  receives: string[]
  asks_instead?: boolean
}
interface PermissionRow {
  name: string
  label: string
  summary: string
  default?: boolean
  tools: PermissionCell[]
}
const permissionTable: {
  tools: { driver: string; label: string }[]
  modes: PermissionRow[]
} = table

const choiceOf = (name: string): PermissionChoice | undefined =>
  PERMISSION_CHOICES.find((c) => PERMISSION_NAME[c] === name)

/** What each permission hands each tool, and where a tool asks instead. */
export function PermissionModesSettings() {
  const { t } = useTranslation(['settings', 'firstHour'])
  return (
    <div className="flex flex-col gap-3">
      <div
        className="overflow-x-auto"
        role="region"
        aria-label={t('settings:permissions.caption')}
        tabIndex={0}
      >
        {/* Fixed columns: the one long allowlist breaks inside its cell and does not take the
            width of the other tools. The table scrolls sideways where it cannot fit. */}
        <table className="w-full min-w-[64rem] table-fixed text-left text-body">
          <caption className="sr-only">
            {t('settings:permissions.caption')}
          </caption>
          <thead className="border-b border-border text-caption text-muted-foreground">
            <tr>
              <th scope="col" className="w-40 px-3 py-2 font-medium">
                {t('settings:permissions.permission')}
              </th>
              <th scope="col" className="w-52 px-3 py-2 font-medium">
                {t('settings:permissions.what')}
              </th>
              {permissionTable.tools.map((tool) => (
                <th
                  key={tool.driver}
                  scope="col"
                  className="px-3 py-2 font-medium"
                >
                  {tool.label}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {permissionTable.modes.map((mode) => {
              const choice = choiceOf(mode.name)
              if (!choice) return null
              return (
                <tr
                  key={mode.name}
                  className="border-b border-border align-top"
                  data-testid={`permission-row-${mode.name}`}
                >
                  <th scope="row" className="px-3 py-2 text-left font-medium">
                    <span className={choice === 'full' ? 'text-warning' : ''}>
                      {t(`firstHour:permission.${choice}`)}
                    </span>
                    {mode.default ? (
                      <Badge variant="neutral" className="ml-2">
                        {t('settings:permissions.default')}
                      </Badge>
                    ) : null}
                    <code className="block text-caption font-normal text-muted-foreground">
                      {mode.name}
                    </code>
                  </th>
                  <td className="px-3 py-2">
                    {t(`firstHour:permissionSummary.${choice}`)}
                  </td>
                  {mode.tools.map((cell) => (
                    <td key={cell.driver} className="px-3 py-2">
                      {cell.asks_instead ? (
                        <p className="mb-1 text-caption text-text-2">
                          {t('settings:permissions.asksInstead', {
                            name: t('firstHour:permission.ask'),
                          })}
                        </p>
                      ) : null}
                      {cell.receives.map((received) => (
                        <code
                          key={received}
                          className="block text-caption [overflow-wrap:anywhere]"
                        >
                          {received}
                        </code>
                      ))}
                    </td>
                  ))}
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      <p className="text-caption text-text-3">
        {t('settings:permissions.note')}
      </p>
    </div>
  )
}
