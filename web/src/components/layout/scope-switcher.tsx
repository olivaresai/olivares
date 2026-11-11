// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SCOPE, ONE CONTROL: the organization and the workspace the next action
// applies to, in one menu, with one name on the bar: the workspace in force, or the
// organization when the work spans every workspace. The other level is in the tooltip
// and in the menu, where both are switched. It sits under the brand in the left
// navigation (sidebar.tsx), the column and the drawer alike, and only when there is a
// choice: one organization and one workspace draw nothing.
import { ChevronsUpDown } from 'lucide-react'
import { useRef, useState } from 'react'
import { Dialog } from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'
import {
  OrgListUnavailableContent,
  TenantMenuItems,
  useTenantChoices,
} from './tenant-switcher'
import { useWorkspaceChoices, WorkspaceMenuItems } from './workspace-switcher'

/** The face of the control: the scope's name in bold; orange stays for what needs a person. */
function ScopeFace({ name }: { name: string }) {
  return (
    <span className="min-w-0 flex-1 truncate text-left font-bold">{name}</span>
  )
}

const FACE_CLASS =
  'inline-flex h-[34px] w-full min-w-0 shrink items-center gap-2 rounded-ctl px-2 text-caption text-text'

export function ScopeSwitcher() {
  const tenant = useTenantChoices()
  const workspace = useWorkspaceChoices()
  const [showRecovery, setShowRecovery] = useState(false)
  const trigger = useRef<HTMLButtonElement>(null)

  const switchesTenant = !tenant.none && !tenant.fixed
  // One workspace is no choice: All and it are the same work. A workspace in force that
  // the list does not resolve, or no longer active, keeps the menu: the way back to All.
  const switchesWorkspace =
    workspace.workspaces.filter((w) => w.status === 'active').length > 1 ||
    (!!workspace.activeWorkspace && workspace.active?.status !== 'active')
  // ⛔ A workspace in force that the list does not resolve is said, never hidden behind
  //    the organization's name: the views still send its filter (workspace-switcher.tsx).
  const name =
    workspace.active?.name ??
    (workspace.activeWorkspace ? workspace.label : tenant.activeLabel)
  const title = tenant.none
    ? workspace.label
    : `${tenant.activeLabel} › ${workspace.label}`

  if (!switchesTenant && !switchesWorkspace) return null

  return (
    <Dialog open={showRecovery} onOpenChange={setShowRecovery}>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            ref={trigger}
            type="button"
            data-slot="scope-switcher"
            title={title}
            // The name starts with what the button shows (WCAG 2.5.3); the other level
            // follows, so a screen reader hears both.
            aria-label={
              name === tenant.activeLabel ? name : `${name}, ${title}`
            }
            className={cn(
              FACE_CLASS,
              'hover:bg-hover active:bg-[var(--pressed)]',
            )}
          >
            <ScopeFace name={name} />
            <ChevronsUpDown
              aria-hidden
              className="size-3.5 shrink-0 text-text-3"
            />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent
          align="start"
          className="max-h-[70vh] min-w-64 overflow-y-auto"
          onCloseAutoFocus={(event) => {
            if (showRecovery) event.preventDefault()
          }}
        >
          {switchesTenant ? (
            <TenantMenuItems
              choices={tenant}
              onRecovery={() => setShowRecovery(true)}
            />
          ) : null}
          {switchesTenant && switchesWorkspace ? (
            <DropdownMenuSeparator />
          ) : null}
          {switchesWorkspace ? (
            <WorkspaceMenuItems choices={workspace} />
          ) : null}
        </DropdownMenuContent>
      </DropdownMenu>
      {tenant.listUnavailable && (
        <OrgListUnavailableContent trigger={trigger} />
      )}
    </Dialog>
  )
}
