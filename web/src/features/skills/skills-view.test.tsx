// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, within } from '@/test/intel'
import { consoleApi } from '@/features/console/api'
import { skillsApi } from './api'
import { SkillsView } from './skills-view'
import type { SkillAssignment, SkillPack, SkillPackDetail } from './types'

const auth = vi.hoisted(() => ({
  can: (_permission: string): boolean => true,
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 'tenant-1', can: auth.can }),
}))

const pack: SkillPack = {
  id: 'pack-1',
  name: 'ecc',
  state: 'enabled',
  latest_revision_id: 'rev-2',
  latest_revision: 2,
  version: 3,
  created_at: '2026-10-01T10:00:00Z',
  updated_at: '2026-10-02T10:00:00Z',
}
const detail: SkillPackDetail = {
  pack,
  revisions: [
    {
      id: 'rev-2',
      pack_id: 'pack-1',
      number: 2,
      source: { kind: 'builtin', origin: 'ecc-2.2.3' },
      members: [
        {
          name: 'code-review',
          directory: 'code-review',
          description: 'Review a diff',
        },
      ],
      created_at: '2026-10-02T10:00:00Z',
    },
    {
      id: 'rev-1',
      pack_id: 'pack-1',
      number: 1,
      source: { kind: 'builtin' },
      members: [],
      created_at: '2026-10-01T10:00:00Z',
    },
  ],
  has_more_revisions: false,
}
const groupPin: SkillAssignment = {
  id: 'as-1',
  target_kind: 'agent_group',
  target_id: 'grp-1',
  pack_id: 'pack-1',
  pack_revision_id: 'rev-1',
  members: null,
  version: 4,
}
const orphanPin: SkillAssignment = {
  ...groupPin,
  id: 'as-2',
  target_kind: 'agent',
  target_id: 'agent-gone',
  pack_revision_id: 'rev-2',
}

function list<T>(items: T[]) {
  return { items, has_more: false } as never
}

beforeEach(() => {
  vi.restoreAllMocks()
  auth.can = () => true
  vi.spyOn(skillsApi, 'packs').mockResolvedValue(list([pack]))
  vi.spyOn(skillsApi, 'pack').mockResolvedValue(detail)
  vi.spyOn(skillsApi, 'packAssignments').mockResolvedValue(
    list([groupPin, orphanPin]),
  )
  vi.spyOn(consoleApi, 'listWorkspaces').mockResolvedValue(
    list([{ id: 'ws-1', name: 'Research' }]),
  )
  vi.spyOn(consoleApi, 'listAgentGroups').mockResolvedValue(
    list([{ id: 'grp-1', name: 'Reviewers' }]),
  )
  vi.spyOn(consoleApi, 'listAgents').mockResolvedValue(list([]))
})

async function openPack() {
  renderIntel(<SkillsView />)
  await userEvent.click(await screen.findByText('ecc'))
  return screen.findByRole('dialog')
}

describe('SkillsView', () => {
  it('lists the catalog and shows where a pack is assigned', async () => {
    const sheet = await openPack()
    expect(await within(sheet).findByText('code-review')).toBeInTheDocument()
    expect(skillsApi.packAssignments).toHaveBeenCalledWith('pack-1')
    const group = (await within(sheet).findByText('Reviewers')).closest('li')
    expect(group?.textContent).toContain('Agent group')
    expect(group?.textContent).toContain('revision 1')
    // A target the directory lists do not name (deleted or unreadable) shows its ID.
    expect(within(sheet).getByText('agent-gone')).toBeInTheDocument()
  })

  it('unassigns with the recorded version after confirmation', async () => {
    const unassign = vi
      .spyOn(skillsApi, 'unassign')
      .mockResolvedValue({ state: 'unassigned_for_new_conversations' })
    const sheet = await openPack()
    await userEvent.click(
      await within(sheet).findByRole('button', {
        name: 'Unassign from Reviewers',
      }),
    )
    const confirm = await screen.findByRole('dialog', {
      name: 'Unassign this skill pack?',
    })
    await userEvent.click(
      within(confirm).getByRole('button', { name: 'Unassign' }),
    )
    expect(unassign).toHaveBeenCalledWith(groupPin, { tenant: 'tenant-1' })
  })

  it('assigns the chosen revision to a department', async () => {
    const assign = vi
      .spyOn(skillsApi, 'assign')
      .mockResolvedValue({ state: 'pinned', assignment: groupPin })
    const sheet = await openPack()
    const submit = await within(sheet).findByRole('button', { name: 'Assign' })
    expect(submit).toBeDisabled()
    await userEvent.selectOptions(
      await within(sheet).findByLabelText('Target'),
      await within(sheet).findByRole('option', { name: 'Research' }),
    )
    await userEvent.click(submit)
    expect(assign).toHaveBeenCalledWith(
      {
        target_kind: 'workspace',
        target_id: 'ws-1',
        pack_revision_id: 'rev-2',
      },
      { tenant: 'tenant-1' },
    )
  })

  it('does not offer a target the pack is already pinned to', async () => {
    const sheet = await openPack()
    await userEvent.selectOptions(
      await within(sheet).findByLabelText('Assign to'),
      'agent_group',
    )
    expect(
      within(sheet).queryByRole('option', { name: 'Reviewers' }),
    ).not.toBeInTheDocument()
    expect(
      within(sheet).getByText('No targets of this kind that you can read.'),
    ).toBeInTheDocument()
  })

  it('offers no assignment change without the assignment permission', async () => {
    auth.can = (p) => p !== 'skills:assignment:write'
    const sheet = await openPack()
    await within(sheet).findByText('Reviewers')
    expect(
      within(sheet).queryByRole('button', { name: /^Unassign/ }),
    ).not.toBeInTheDocument()
    expect(
      within(sheet).queryByRole('button', { name: 'Assign' }),
    ).not.toBeInTheDocument()
  })
})
