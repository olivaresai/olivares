// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Checkbox } from '@/components/ui/checkbox'
import { Spinner } from '@/components/ui/spinner'
import type { RBACCatalogDTO } from './api'

// scopeLabel renders a grant/domain scope (tree + ref) as a human label.
export function scopeLabel(
  t: (k: string, o?: Record<string, unknown>) => string,
  tree: string,
  ref?: string,
): string {
  switch (tree) {
    case 'workspace':
      return t('roles.authority.workspaceScope', { ref: ref ?? '' })
    case 'agent_group':
      return t('roles.authority.groupScope', { ref: ref ?? '' })
    default:
      return t('roles.authority.tenantWide')
  }
}

// classOptionsFor returns the resource-class options a grant form may offer for a scope
// tree. It is the console's half of a backend rule (validateScopeRefs), extracted so it
// can be asserted directly rather than only through a rendered dialog:
//
//   - agent_group — only 'agent'. The scope resolver folds group membership onto agent
//     entities alone, so any other class would never match.
//   - tenant — every grantable kind, including module kinds: a tenant-scoped permit
//     carries no resource condition, so the class is a real filter there.
//   - workspace / folder — TREE kinds only. A module kind would project a permit with
//     `resource in Workspace::…`, which no module route can satisfy; the backend rejects
//     it, so offering it would be a checkbox whose only outcome is a 400.
export function classOptionsFor(
  scopeTree: string,
  catalog?: RBACCatalogDTO,
): string[] {
  if (scopeTree === 'agent_group') return ['agent']
  if (scopeTree === 'tenant') return catalog?.kinds ?? []
  return catalog?.tree_kinds ?? []
}

// PermissionMatrix renders the kind × verb checkbox grid used in role and
// permission-group authoring forms.
//
// the grid is no longer a plain cross-product. A CORE kind carries all three
// verbs by construction, but a MODULE kind carries exactly the permissions its module
// declared — "models:keys" has read and write and no admin. Offering the missing cell
// would produce a checkbox whose only possible outcome is a 400 from validatePerms,
// which is the console-contradicts-the-engine class this session was sent to close. A
// permission the engine does not know is rendered as an inert dash, not a checkbox.
export function PermissionMatrix({
  catalog,
  value,
  onChange,
  restrictKind,
  excluding = false,
}: {
  catalog?: RBACCatalogDTO
  value: string[]
  onChange: (next: string[]) => void
  restrictKind?: string
  /**
   * Renders the EXCLUSION grid. A role form shows two of these grids at once, so the
   * accessible name of a checkbox must say which one it belongs to: without this, a
   * screen-reader user hears "agent:read" twice in the same dialog with no way to tell
   * "grant this" from "subtract this" — two opposite actions under one name.
   */
  excluding?: boolean
}) {
  const { t } = useTranslation('console')
  const selected = useMemo(() => new Set(value), [value])
  // The module permissions the engine will accept, looked up whole.
  const grantable = useMemo(
    () => new Set(catalog?.permissions ?? []),
    [catalog?.permissions],
  )
  const treeKinds = useMemo(
    () => new Set(catalog?.tree_kinds ?? []),
    [catalog?.tree_kinds],
  )
  if (!catalog) return <Spinner size="sm" />
  const kinds = restrictKind
    ? catalog.kinds.filter((k) => k === restrictKind)
    : catalog.kinds
  // A core kind is grantable for every verb; a module kind only for the permissions the
  // mounted module actually declared.
  const offers = (kind: string, verb: string) =>
    treeKinds.has(kind) || grantable.has(`${kind}:${verb}`)
  const verbLabel: Record<string, string> = {
    read: t('roles.matrix.read'),
    write: t('roles.matrix.write'),
    admin: t('roles.matrix.admin'),
  }

  const toggle = (perm: string) => {
    const next = new Set(selected)
    if (next.has(perm)) next.delete(perm)
    else next.add(perm)
    onChange([...next].sort())
  }

  return (
    <div className="max-h-64 overflow-y-auto rounded-lg border border-border">
      <table className="w-full text-sm">
        <thead className="sticky top-0 bg-muted/60 text-left text-xs text-muted-foreground">
          <tr>
            <th className="px-3 py-2 font-medium">{t('roles.matrix.kind')}</th>
            {catalog.verbs.map((v) => (
              <th key={v} className="px-3 py-2 text-center font-medium">
                {verbLabel[v] ?? v}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {kinds.map((kind) => (
            <tr key={kind} className="border-t border-border">
              <td className="px-3 py-1.5 font-mono text-xs text-foreground">
                {kind}
              </td>
              {catalog.verbs.map((verb) => {
                const perm = `${kind}:${verb}`
                return (
                  <td key={verb} className="px-3 py-1.5 text-center">
                    {offers(kind, verb) ? (
                      <Checkbox
                        checked={selected.has(perm)}
                        onCheckedChange={() => toggle(perm)}
                        aria-label={
                          excluding
                            ? t('roles.matrix.excludeAria', { perm })
                            : perm
                        }
                      />
                    ) : (
                      <span
                        className="text-muted-foreground"
                        title={t('roles.matrix.notDeclared', { perm })}
                      >
                        —
                      </span>
                    )}
                  </td>
                )
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// FormError surfaces the engine's message inline (ceiling/validation), so a denied
// authoring action explains itself instead of failing silently.
export function FormError({ error }: { error: unknown }) {
  if (!(error instanceof Error) || !error.message) return null
  return (
    <p role="alert" className="text-sm text-danger">
      {error.message}
    </p>
  )
}
