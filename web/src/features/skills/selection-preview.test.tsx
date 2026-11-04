// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import { skillsApi } from './api'
import { consoleApi } from '@/features/console/api'
import { SelectionPreview, SessionSkillsPreview } from './selection-preview'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'tenant-1',
    can: () => true,
    principal: { user_id: 'owner' },
  }),
}))

it('shows the resolved members and forgets the old target while loading another', async () => {
  const selection = vi.spyOn(skillsApi, 'selection').mockResolvedValue({
    digest: 'digest',
    members: [
      {
        name: 'review',
        directory: 'review',
        pack_id: 'p',
        pack_revision_id: 'r',
      },
    ],
  })
  const view = renderIntel(<SelectionPreview kind="workspace" id="one" />)
  expect(
    await screen.findByText('This session gets 1 skill'),
  ).toBeInTheDocument()
  expect(screen.getByText('review')).toBeInTheDocument()
  selection.mockImplementation(() => new Promise(() => {}))
  view.rerender(<SelectionPreview kind="workspace" id="two" />)
  expect(screen.queryByText('review')).not.toBeInTheDocument()
})

it('resolves the default department, profile and folder together', async () => {
  vi.spyOn(consoleApi, 'listWorkspaces').mockResolvedValue({
    items: [{ id: 'default', is_default: true }],
    has_more: false,
  } as Awaited<ReturnType<typeof consoleApi.listWorkspaces>>)
  const selection = vi
    .spyOn(skillsApi, 'selection')
    .mockResolvedValue({ digest: 'empty', members: [] })
  renderIntel(
    <SessionSkillsPreview profileRef="profile-one" folderRef="folder-one" />,
  )
  expect(
    await screen.findByText('This session gets 0 skills'),
  ).toBeInTheDocument()
  expect(selection).toHaveBeenCalledWith(
    'workspace',
    'default',
    expect.any(AbortSignal),
    'profile-one',
    'folder-one',
  )
})
