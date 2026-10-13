// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, within } from '@/test/intel'
import { agentOpsApi } from '@/features/agentops/api'
import { consoleApi } from '@/features/console/api'
import { http } from '@/lib/api/client'
import { ApiError } from '@/lib/api/errors'
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
  vi.spyOn(http, 'get').mockResolvedValue(
    list([
      {
        id: 'ecc-security',
        origin: 'https://github.com/example/skills',
        resolved_commit: 'abc',
        source_digest: 'a'.repeat(64),
      },
    ]),
  )
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
  vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue(list([]))
})

async function chooseWorkspace() {
  await screen.findByRole('option', { name: 'Research' })
  await userEvent.selectOptions(screen.getByLabelText('Workspace'), 'ws-1')
}

async function openPack() {
  renderIntel(<SkillsView />)
  await userEvent.click(await screen.findByText('ecc'))
  return screen.findByRole('dialog')
}

describe('SkillsView', () => {
  it('browses builtins and installs and pins the returned revision in one action', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      state: 'catalog_published',
      pack,
      revision: detail.revisions[0],
    })
    const assign = vi
      .spyOn(skillsApi, 'assign')
      .mockResolvedValue({ state: 'pinned', assignment: groupPin })
    renderIntel(<SkillsView />)
    await chooseWorkspace()
    await userEvent.click(
      await screen.findByRole('button', { name: 'Install ecc-security' }),
    )
    expect(post).toHaveBeenCalledWith(
      '/v1/m/skills/packs',
      {
        name: 'ecc-security',
        source: { kind: 'builtin', ref: 'ecc-security' },
      },
      expect.objectContaining({
        tenant: 'tenant-1',
        headers: { 'Idempotency-Key': expect.any(String) },
      }),
    )
    await screen.findByText('Skill pack installed and assigned')
    expect(assign).toHaveBeenCalledWith(
      {
        target_kind: 'workspace',
        target_id: 'ws-1',
        pack_revision_id: 'rev-2',
      },
      expect.objectContaining({ tenant: 'tenant-1' }),
    )
  })

  it('imports a Git source with its ref and subdirectory', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      state: 'catalog_published',
      pack,
      revision: detail.revisions[0],
    })
    renderIntel(<SkillsView />)
    await userEvent.click(await screen.findByText('From Git'))
    await userEvent.type(
      await screen.findByLabelText(/^Git URL/),
      'https://github.com/example/review.git',
    )
    await userEvent.type(screen.getByLabelText('Git ref'), 'v1')
    await userEvent.type(screen.getByLabelText('Subdirectory'), 'skills')
    await userEvent.click(
      screen.getByRole('button', { name: 'Install from Git' }),
    )
    await screen.findByText('Skill pack installed and assigned')
    expect(post).toHaveBeenCalledWith(
      '/v1/m/skills/packs',
      {
        name: 'review',
        source: {
          kind: 'git',
          url: 'https://github.com/example/review.git',
          ref: 'v1',
          subdir: 'skills',
        },
      },
      expect.objectContaining({ tenant: 'tenant-1' }),
    )
  })

  it('retries a failed workspace assignment without importing another revision', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      state: 'catalog_published',
      pack,
      revision: detail.revisions[0],
    })
    const assign = vi
      .spyOn(skillsApi, 'assign')
      .mockRejectedValueOnce(new Error('Assignment unavailable'))
      .mockResolvedValue({ state: 'pinned', assignment: groupPin })
    renderIntel(<SkillsView />)
    await chooseWorkspace()
    await userEvent.click(
      await screen.findByRole('button', { name: 'Install ecc-security' }),
    )
    await userEvent.click(
      await screen.findByRole('button', { name: 'Retry assignment' }),
    )
    await screen.findByText('Skill pack installed and assigned')
    expect(post).toHaveBeenCalledTimes(1)
    expect(assign).toHaveBeenCalledTimes(2)
  })

  it('reuses the import key after an unknown response', async () => {
    const post = vi
      .spyOn(http, 'post')
      .mockRejectedValueOnce(new Error('Response lost'))
      .mockResolvedValue({
        state: 'catalog_published',
        pack,
        revision: detail.revisions[0],
      })
    vi.spyOn(skillsApi, 'assign').mockResolvedValue({
      state: 'pinned',
      assignment: groupPin,
    })
    renderIntel(<SkillsView />)
    await userEvent.click(
      await screen.findByRole('button', { name: 'Install ecc-security' }),
    )
    await userEvent.click(
      await screen.findByRole('button', { name: 'Retry install' }),
    )
    await screen.findByText('Skill pack installed and assigned')
    expect(post).toHaveBeenCalledTimes(2)
    expect(post.mock.calls[1][2]?.headers).toEqual(
      post.mock.calls[0][2]?.headers,
    )
  })

  it('waits for workspace discovery before offering installation', async () => {
    let resolve!: (value: never) => void
    vi.spyOn(consoleApi, 'listWorkspaces').mockReturnValue(
      new Promise((r) => {
        resolve = r
      }),
    )
    renderIntel(<SkillsView />)
    const button = await screen.findByRole('button', {
      name: 'Install ecc-security',
    })
    expect(button).toBeDisabled()
    resolve(list([{ id: 'ws-1', name: 'Research' }]))
    await screen.findByRole('option', { name: 'Research' })
    expect(button).toBeEnabled()
  })

  it('shows a committed pack when assignment is denied', async () => {
    const installed = { ...pack, id: 'installed-1', name: 'ecc-security' }
    vi.spyOn(skillsApi, 'packs')
      .mockResolvedValueOnce(list([pack]))
      .mockResolvedValue(list([pack, installed]))
    vi.spyOn(http, 'post').mockResolvedValue({
      state: 'catalog_published',
      pack: installed,
      revision: detail.revisions[0],
    })
    vi.spyOn(skillsApi, 'assign').mockRejectedValue(
      new ApiError(403, 'forbidden', 'Assignment denied'),
    )
    renderIntel(<SkillsView />)
    await chooseWorkspace()
    await userEvent.click(
      await screen.findByRole('button', { name: 'Install ecc-security' }),
    )
    await screen.findByText(
      'Pack installed; workspace assignment failed: Assignment denied',
    )
    expect(
      await screen.findByRole('gridcell', { name: 'ecc-security' }),
    ).toBeInTheDocument()
  })

  it('recognizes a pin after its successful response was lost', async () => {
    vi.spyOn(http, 'post').mockResolvedValue({
      state: 'catalog_published',
      pack,
      revision: detail.revisions[0],
    })
    vi.spyOn(http, 'get').mockImplementation(async (path) =>
      path === '/v1/m/skills/assignments'
        ? list([
            {
              ...groupPin,
              target_kind: 'workspace',
              target_id: 'ws-1',
              pack_revision_id: 'rev-2',
              members: ['code-review'],
            },
          ])
        : list([{ id: 'ecc-security' }]),
    )
    const assign = vi
      .spyOn(skillsApi, 'assign')
      .mockRejectedValueOnce(new Error('Response lost'))
      .mockRejectedValue(new ApiError(409, 'conflict', 'Already pinned'))
    renderIntel(<SkillsView />)
    await chooseWorkspace()
    await userEvent.click(
      await screen.findByRole('button', { name: 'Install ecc-security' }),
    )
    await userEvent.click(
      await screen.findByRole('button', { name: 'Retry assignment' }),
    )
    await screen.findByText('Skill pack installed and assigned')
    expect(assign).toHaveBeenCalledTimes(1)
  })

  it('allows a distinct catalog name for another Git subdirectory', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      state: 'catalog_published',
      pack,
      revision: detail.revisions[0],
    })
    vi.spyOn(skillsApi, 'assign').mockResolvedValue({
      state: 'pinned',
      assignment: groupPin,
    })
    renderIntel(<SkillsView />)
    await userEvent.click(await screen.findByText('From Git'))
    await userEvent.type(
      screen.getByLabelText(/^Git URL/),
      'https://github.com/example/skills',
    )
    await userEvent.type(screen.getByLabelText('Pack name'), 'review-pack')
    await userEvent.type(screen.getByLabelText('Subdirectory'), 'review')
    await userEvent.click(
      screen.getByRole('button', { name: 'Install from Git' }),
    )
    await screen.findByText('Skill pack installed and assigned')
    expect(post).toHaveBeenCalledWith(
      '/v1/m/skills/packs',
      expect.objectContaining({ name: 'review-pack' }),
      expect.anything(),
    )
  })

  it('hides install actions without catalog write permission', async () => {
    auth.can = (p) => p !== 'skills:catalog:write'
    renderIntel(<SkillsView />)
    await screen.findByText('ecc-security')
    expect(
      screen.queryByRole('button', { name: 'Install ecc-security' }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Install from Git' }),
    ).not.toBeInTheDocument()
  })

  it('opens the pack a ?pack= link names (AI tools > Installed here)', async () => {
    window.history.pushState({}, '', '/skills?pack=pack-1')
    try {
      renderIntel(<SkillsView />)
      const sheet = await screen.findByRole('dialog')
      expect(await within(sheet).findByText('code-review')).toBeInTheDocument()
    } finally {
      window.history.pushState({}, '', '/')
    }
  })

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

  it('assigns a pack to a session folder', async () => {
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue(
      list([
        {
          workspace_ref: 'ref-1',
          name: 'payments',
          root_path: '/srv/payments',
        },
      ]),
    )
    const assign = vi
      .spyOn(skillsApi, 'assign')
      .mockResolvedValue({ state: 'pinned', assignment: groupPin })
    const sheet = await openPack()
    await userEvent.selectOptions(
      await within(sheet).findByLabelText('Assign to'),
      'folder',
    )
    await userEvent.selectOptions(
      within(sheet).getByLabelText('Target'),
      await within(sheet).findByRole('option', { name: 'payments' }),
    )
    await userEvent.click(within(sheet).getByRole('button', { name: 'Assign' }))
    expect(assign).toHaveBeenCalledWith(
      {
        target_kind: 'folder',
        target_id: 'ref-1',
        pack_revision_id: 'rev-2',
      },
      { tenant: 'tenant-1' },
    )
  })

  it('pins a skill that acts when loaded only after the operator approves it', async () => {
    vi.spyOn(skillsApi, 'pack').mockResolvedValue({
      ...detail,
      revisions: [
        {
          ...detail.revisions[0],
          members: [
            {
              name: 'deploy',
              directory: 'deploy',
              description: 'Ship it',
              active_content: ['allowed-tools'],
            },
          ],
        },
        detail.revisions[1],
      ],
    })
    const assign = vi
      .spyOn(skillsApi, 'assign')
      .mockResolvedValue({ state: 'pinned', assignment: groupPin })
    const sheet = await openPack()
    expect(
      await within(sheet).findByText('acts when loaded: allowed-tools'),
    ).toBeInTheDocument()
    await userEvent.selectOptions(
      await within(sheet).findByLabelText('Target'),
      await within(sheet).findByRole('option', { name: 'Research' }),
    )
    const submit = within(sheet).getByRole('button', { name: 'Assign' })
    expect(submit).toBeDisabled()
    await userEvent.click(
      within(sheet).getByRole('checkbox', { name: /I reviewed these skills/ }),
    )
    await userEvent.click(submit)
    expect(assign).toHaveBeenCalledWith(
      {
        target_kind: 'workspace',
        target_id: 'ws-1',
        pack_revision_id: 'rev-2',
        approve_active_content: true,
      },
      { tenant: 'tenant-1' },
    )
  })

  it('checks the recorded source, shows what changed and applies the reviewed digest', async () => {
    const digest = 'd'.repeat(64)
    vi.spyOn(skillsApi, 'updateCheck').mockResolvedValue({
      state: 'available',
      pack,
      current: { revision_id: 'rev-2', number: 2 },
      candidate: { manifest_digest: digest },
      diff: {
        members: [
          { name: 'plan', change: 'added', new_active_content: ['hooks'] },
        ],
        files: [{ path: 'plan/SKILL.md', change: 'added' }],
      },
    })
    const apply = vi.spyOn(skillsApi, 'updateApply').mockResolvedValue({
      state: 'catalog_published',
      pack,
      revision: { ...detail.revisions[0], number: 3 },
    })
    const sheet = await openPack()
    await userEvent.click(
      await within(sheet).findByRole('button', { name: 'Check for update' }),
    )
    expect(
      await within(sheet).findByText('An update is available over revision 2.'),
    ).toBeInTheDocument()
    expect(
      within(sheet).getByText(
        'plan now acts when loaded: hooks. Assigning it needs your approval.',
      ),
    ).toBeInTheDocument()
    await userEvent.click(
      within(sheet).getByRole('button', { name: 'Apply this update' }),
    )
    expect(apply).toHaveBeenCalledWith('pack-1', digest, expect.any(String), {
      tenant: 'tenant-1',
    })
  })

  it('says when the pack is current and applies nothing', async () => {
    vi.spyOn(skillsApi, 'updateCheck').mockResolvedValue({
      state: 'current',
      pack,
      current: { revision_id: 'rev-2', number: 2 },
    })
    const apply = vi.spyOn(skillsApi, 'updateApply')
    const sheet = await openPack()
    await userEvent.click(
      await within(sheet).findByRole('button', { name: 'Check for update' }),
    )
    expect(
      await within(sheet).findByText('Up to date at revision 2.'),
    ).toBeInTheDocument()
    expect(
      within(sheet).queryByRole('button', { name: 'Apply this update' }),
    ).not.toBeInTheDocument()
    expect(apply).not.toHaveBeenCalled()
  })

  it('moves a pin to the latest revision from its row', async () => {
    const repin = vi
      .spyOn(skillsApi, 'repin')
      .mockResolvedValue({ state: 'pinned', assignment: groupPin })
    const sheet = await openPack()
    // The pin already on the latest revision offers no move.
    expect(
      within(sheet).queryByRole('button', {
        name: 'Use latest: agent-gone',
      }),
    ).not.toBeInTheDocument()
    await userEvent.click(
      await within(sheet).findByRole('button', {
        name: 'Use latest: Reviewers',
      }),
    )
    expect(repin).toHaveBeenCalledWith(groupPin, 'rev-2', false, {
      tenant: 'tenant-1',
    })
  })

  it('asks for approval before moving a pin to a revision whose skills act when loaded', async () => {
    const acting = {
      ...detail.revisions[0],
      members: [
        {
          name: 'deploy',
          directory: 'deploy',
          description: 'Ship it',
          active_content: ['hooks'],
        },
      ],
    }
    vi.spyOn(skillsApi, 'pack').mockResolvedValue({
      ...detail,
      revisions: [acting, detail.revisions[1]],
    })
    const repin = vi
      .spyOn(skillsApi, 'repin')
      .mockResolvedValue({ state: 'pinned', assignment: groupPin })
    const sheet = await openPack()
    await userEvent.click(
      await within(sheet).findByRole('button', {
        name: 'Use latest: Reviewers',
      }),
    )
    expect(repin).not.toHaveBeenCalled()
    const confirm = await screen.findByRole('dialog', {
      name: 'Approve skills that act when loaded?',
    })
    await userEvent.click(
      within(confirm).getByRole('button', { name: 'Approve and use latest' }),
    )
    expect(repin).toHaveBeenCalledWith(groupPin, 'rev-2', true, {
      tenant: 'tenant-1',
    })
  })

  it('moves a pin without asking when the skills it selects do not act when loaded', async () => {
    const acting = {
      ...detail.revisions[0],
      members: [
        {
          name: 'deploy',
          directory: 'deploy',
          description: 'x',
          active_content: ['hooks'],
        },
        { name: 'review', directory: 'review', description: 'y' },
      ],
    }
    vi.spyOn(skillsApi, 'pack').mockResolvedValue({
      ...detail,
      revisions: [acting, detail.revisions[1]],
    })
    vi.spyOn(skillsApi, 'packAssignments').mockResolvedValue(
      list([{ ...groupPin, members: ['review'] }]),
    )
    const repin = vi
      .spyOn(skillsApi, 'repin')
      .mockResolvedValue({ state: 'pinned', assignment: groupPin })
    const sheet = await openPack()
    await userEvent.click(
      await within(sheet).findByRole('button', {
        name: 'Use latest: Reviewers',
      }),
    )
    expect(repin).toHaveBeenCalledWith(
      expect.objectContaining({ id: 'as-1', members: ['review'] }),
      'rev-2',
      false,
      { tenant: 'tenant-1' },
    )
  })

  it('installs a pack that acts when loaded without assigning it', async () => {
    vi.spyOn(http, 'post').mockResolvedValue({
      state: 'catalog_published',
      pack,
      revision: {
        ...detail.revisions[0],
        members: [
          {
            name: 'deploy',
            directory: 'deploy',
            description: 'Ship it',
            active_content: ['allowed-tools'],
          },
        ],
      },
    })
    const assign = vi.spyOn(skillsApi, 'assign')
    renderIntel(<SkillsView />)
    await chooseWorkspace()
    await userEvent.click(
      await screen.findByRole('button', { name: 'Install ecc-security' }),
    )
    expect(
      await screen.findAllByText(/it was not assigned: review it/),
    ).not.toHaveLength(0)
    expect(assign).not.toHaveBeenCalled()
  })
})
