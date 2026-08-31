// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import './i18n'

import type { RBACCatalogDTO } from './api'
import { classOptionsFor, PermissionMatrix } from './roles-shared'

//the console must not offer a permission the engine will reject. A module
// permission kind is grantable, but it carries exactly the permissions its module
// declared: "models:keys" has read and write and NO admin. Before this, the grid was a
// plain kind × verb cross-product, so widening the catalog would have put a checkbox on
// screen whose only possible outcome is a 400 from validatePerms.
const catalog: RBACCatalogDTO = {
  kinds: ['agent', 'models:keys', 'compliance:risk'],
  tree_kinds: ['agent'],
  permissions: [
    'models:keys:read',
    'models:keys:write',
    'compliance:risk:read',
    'compliance:risk:write',
    'compliance:risk:admin',
  ],
  verbs: ['read', 'write', 'admin'],
  builtin_roles: ['viewer', 'editor', 'admin', 'owner'],
  scope_trees: ['tenant', 'workspace', 'agent_group', 'folder'],
}

describe('PermissionMatrix', () => {
  it('offers every verb for a core tree kind', () => {
    render(<PermissionMatrix catalog={catalog} value={[]} onChange={vi.fn()} />)
    for (const verb of ['read', 'write', 'admin']) {
      expect(screen.getByLabelText(`agent:${verb}`)).toBeInTheDocument()
    }
  })

  it('offers only the module permissions the engine declared', () => {
    render(<PermissionMatrix catalog={catalog} value={[]} onChange={vi.fn()} />)
    expect(screen.getByLabelText('models:keys:read')).toBeInTheDocument()
    expect(screen.getByLabelText('models:keys:write')).toBeInTheDocument()
    // The one the backend would reject must not be offerable at all.
    expect(screen.queryByLabelText('models:keys:admin')).not.toBeInTheDocument()
    // A module kind that DOES declare admin still offers it — the rule is per
    // permission, not "module kinds lose their admin verb".
    expect(screen.getByLabelText('compliance:risk:admin')).toBeInTheDocument()
  })

  it('toggles a module permission through onChange', async () => {
    const onChange = vi.fn()
    render(
      <PermissionMatrix catalog={catalog} value={[]} onChange={onChange} />,
    )
    await userEvent.click(screen.getByLabelText('models:keys:write'))
    expect(onChange).toHaveBeenCalledWith(['models:keys:write'])
  })

  it('renders nothing offerable when the engine declares no module permissions', () => {
    const bare: RBACCatalogDTO = { ...catalog, permissions: [] }
    render(<PermissionMatrix catalog={bare} value={[]} onChange={vi.fn()} />)
    expect(screen.getByLabelText('agent:read')).toBeInTheDocument()
    for (const perm of ['models:keys:read', 'compliance:risk:read']) {
      expect(screen.queryByLabelText(perm)).not.toBeInTheDocument()
    }
  })
})

// A role form shows the grant grid and the exclusion grid at once, so a checkbox's
// accessible name must say WHICH grid it is in. Sharing the name made "agent:read" appear
// twice in one dialog with nothing to tell "grant this" from "subtract this" — two
// opposite actions announced identically.
describe('PermissionMatrix accessible names', () => {
  it('names an exclusion checkbox differently from a grant checkbox', () => {
    const { unmount } = render(
      <PermissionMatrix catalog={catalog} value={[]} onChange={vi.fn()} />,
    )
    expect(screen.getByLabelText('agent:read')).toBeInTheDocument()
    unmount()

    render(
      <PermissionMatrix
        catalog={catalog}
        value={[]}
        onChange={vi.fn()}
        excluding
      />,
    )
    // The plain permission name must no longer be the accessible name...
    expect(screen.queryByLabelText('agent:read')).not.toBeInTheDocument()
    // ...and the exclusion name must be findable and unique.
    expect(screen.getByLabelText(/agent:read/)).toBeInTheDocument()
  })

  it('still toggles the right permission when excluding', async () => {
    const onChange = vi.fn()
    render(
      <PermissionMatrix
        catalog={catalog}
        value={[]}
        onChange={onChange}
        excluding
      />,
    )
    await userEvent.click(screen.getByLabelText(/models:keys:write/))
    expect(onChange).toHaveBeenCalledWith(['models:keys:write'])
  })
})

// The scope-class picker is the second place the console could promise a grant the
// engine refuses: a module kind is grantable, but only a TENANT scope may filter by one.
describe('classOptionsFor', () => {
  it('offers only agents for an agent-group scope', () => {
    expect(classOptionsFor('agent_group', catalog)).toEqual(['agent'])
  })

  it('offers every grantable kind at tenant scope, module kinds included', () => {
    expect(classOptionsFor('tenant', catalog)).toEqual([
      'agent',
      'models:keys',
      'compliance:risk',
    ])
  })

  it('offers TREE kinds only on a workspace or folder scope', () => {
    for (const tree of ['workspace', 'folder']) {
      const got = classOptionsFor(tree, catalog)
      expect(got).toEqual(['agent'])
      expect(got).not.toContain('compliance:risk')
      expect(got).not.toContain('models:keys')
    }
  })

  it('offers nothing rather than guessing when the catalog has not loaded', () => {
    for (const tree of ['tenant', 'workspace', 'folder']) {
      expect(classOptionsFor(tree, undefined)).toEqual([])
    }
  })
})
