// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// TURNING AN MCP SERVER ON IS A TRUST DECISION, MADE WITH THE TOOL LIST IN VIEW (Root,
// 2026-10-02, after SR2 on MC 96d90be0). A server's own annotations are hints: they never let a
// tool run without approval by themselves. The engine proposes the tools whose hints say
// read-only (proposed_allow); this dialog pre-selects them, the administrator confirms or
// changes the choice, and the explicit list is what is sent. Every other tool asks first; a
// tool an explicit list already leaves out stays not allowed.
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import type { MCPServer, MCPToolPolicy } from './mcp-gateway-api'

/** What an enabled server lets sessions do with each tested tool. */
export function toolTrust(row: MCPServer): {
  allow: string[]
  ask: string[]
  deny: string[]
} {
  const out = {
    allow: [] as string[],
    ask: [] as string[],
    deny: [] as string[],
  }
  for (const tool of row.probe.tools ?? []) {
    const policy = row.allowed_tools?.find((p) => p.name === tool.name)
    if (!policy) out.deny.push(tool.name)
    else if (policy.destructive) out.ask.push(tool.name)
    else out.allow.push(tool.name)
  }
  return out
}

/** A list someone set keeps its choices; otherwise the server's read-only proposal. */
export function initialAllow(row: MCPServer): Set<string> {
  const tested = new Set((row.probe.tools ?? []).map((t) => t.name))
  if (row.allowed_tools?.length)
    return new Set(
      row.allowed_tools
        .filter((p) => !p.destructive && tested.has(p.name))
        .map((p) => p.name),
    )
  return new Set((row.proposed_allow ?? []).filter((n) => tested.has(n)))
}

/** The tools a set list leaves out: not allowed, and not offered here. */
function deniedBySetList(row: MCPServer): Set<string> {
  if (!row.allowed_tools?.length) return new Set()
  return new Set(
    (row.probe.tools ?? [])
      .map((t) => t.name)
      .filter((n) => !row.allowed_tools!.some((p) => p.name === n)),
  )
}

/** The explicit list the engine receives: each offered tool runs without approval or asks. */
export function enableList(
  row: MCPServer,
  allow: ReadonlySet<string>,
): MCPToolPolicy[] {
  const denied = deniedBySetList(row)
  return (row.probe.tools ?? [])
    .filter((t) => !denied.has(t.name))
    .map((t) => {
      const set = row.allowed_tools?.find((p) => p.name === t.name)
      return {
        name: t.name,
        required_scope: set?.required_scope || 'tools:call',
        destructive: !allow.has(t.name),
      }
    })
}

export function MCPEnableDialog({
  row,
  onClose,
  onConfirm,
  busy,
}: {
  row: MCPServer
  onClose: () => void
  onConfirm: (allowed: MCPToolPolicy[]) => void
  busy: boolean
}) {
  const { t } = useTranslation('console')
  const [allow, setAllow] = useState(() => initialAllow(row))
  const proposed = new Set(row.proposed_allow ?? [])
  const denied = deniedBySetList(row)
  return (
    <Dialog open onOpenChange={(open) => (open ? undefined : onClose())}>
      <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
        <DialogHeader>
          <DialogTitle>
            {t('mcpGateway.enableDialog.title', { name: row.name })}
          </DialogTitle>
          <DialogDescription>
            {t('mcpGateway.enableDialog.body')}
          </DialogDescription>
        </DialogHeader>
        <ul className="flex flex-col gap-2" data-slot="mcp-enable-tools">
          {(row.probe.tools ?? []).map((tool) => {
            const id = `mcp-allow-${tool.name}`
            return (
              <li
                key={tool.name}
                className="flex min-w-0 items-start gap-2 break-all"
              >
                {denied.has(tool.name) ? (
                  <span className="text-caption text-muted-foreground">
                    <code>{tool.name}</code> ·{' '}
                    {t('mcpGateway.enableDialog.notAllowed')}
                  </span>
                ) : (
                  <>
                    <input
                      id={id}
                      type="checkbox"
                      className="mt-1"
                      checked={allow.has(tool.name)}
                      onChange={(e) => {
                        const next = new Set(allow)
                        if (e.target.checked) next.add(tool.name)
                        else next.delete(tool.name)
                        setAllow(next)
                      }}
                      aria-label={t('mcpGateway.enableDialog.runWithout', {
                        tool: tool.name,
                      })}
                    />
                    <label htmlFor={id} className="min-w-0 flex-1">
                      <code>{tool.name}</code>
                      {proposed.has(tool.name) ? (
                        <Badge variant="neutral" className="ml-2">
                          {t('mcpGateway.enableDialog.readOnlyHint')}
                        </Badge>
                      ) : null}
                    </label>
                  </>
                )}
              </li>
            )
          })}
        </ul>
        <p className="text-caption text-muted-foreground">
          {t('mcpGateway.enableDialog.others')}
        </p>
        <DialogFooter>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            {t('common:actions.cancel')}
          </Button>
          <Button
            variant="primary"
            disabled={busy}
            onClick={() => onConfirm(enableList(row, allow))}
          >
            {t('mcpGateway.enableDialog.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
