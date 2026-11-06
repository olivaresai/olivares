// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, within } from '@/test/intel'
import { skillsApi } from './api'
import { SkillCatalog } from './skill-catalog'
import type { SkillPack, SkillRevision } from './types'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't', can: () => false }),
}))
const pack: SkillPack = {
  id: 'ecc',
  name: 'ECC',
  latest_revision_id: 'r2',
  latest_revision: 2,
  state: 'enabled',
  version: 1,
  created_at: '',
  updated_at: '',
}
const revision: SkillRevision = {
  id: 'r2',
  pack_id: 'ecc',
  number: 2,
  source: { kind: 'builtin' },
  created_at: '',
  members: [
    { name: 'review', description: 'Inspect a diff', directory: 'review' },
    {
      name: 'agent:writer',
      kind: 'agent',
      description: 'Write articles',
      directory: 'writer',
    },
    {
      name: 'command:check',
      kind: 'command',
      description: 'Validate code',
      directory: 'check',
    },
    {
      name: 'rule:style',
      kind: 'rule',
      description: 'Formatting standards',
      directory: 'style',
    },
  ],
}

beforeEach(() => {
  vi.restoreAllMocks()
  vi.spyOn(skillsApi, 'pack').mockResolvedValue({
    pack,
    revisions: [revision, { ...revision, id: 'r1', number: 1, members: [] }],
    has_more_revisions: false,
  })
  vi.spyOn(skillsApi, 'packAssignments').mockResolvedValue({
    items: [
      {
        id: 'a',
        pack_id: 'ecc',
        pack_revision_id: 'r2',
        target_kind: 'profile',
        target_id: 'profile-all',
        members: null,
        version: 1,
      },
      {
        id: 'b',
        pack_id: 'ecc',
        pack_revision_id: 'r2',
        target_kind: 'folder',
        target_id: 'folder-selected',
        members: ['review'],
        version: 1,
      },
      {
        id: 'c',
        pack_id: 'ecc',
        pack_revision_id: 'r1',
        target_kind: 'agent',
        target_id: 'old-agent',
        members: null,
        version: 1,
      },
    ],
    has_more: false,
  })
})

it('lists every member kind and searches descriptions across packs', async () => {
  const open = vi.fn()
  renderIntel(<SkillCatalog packs={[pack]} onOpen={open} />)
  expect(await screen.findByText('review')).toBeInTheDocument()
  for (const name of ['agent:writer', 'command:check', 'rule:style'])
    expect(screen.getByText(name)).toBeInTheDocument()
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Search' }),
    'inspect',
  )
  expect(screen.getByText('review')).toBeInTheDocument()
  expect(screen.queryByText('agent:writer')).toBeNull()
  await userEvent.click(screen.getByText('review'))
  expect(open).toHaveBeenCalledWith('ecc', 'review')
})

it('shows only pins whose revision and member selection contain the item', async () => {
  renderIntel(<SkillCatalog packs={[pack]} onOpen={vi.fn()} />)
  const row = (await screen.findByText('review')).closest('tr')!
  expect(await within(row).findByText(/profile-all/)).toBeInTheDocument()
  expect(within(row).getByText(/folder-selected/)).toBeInTheDocument()
  expect(within(row).queryByText(/old-agent/)).toBeNull()
  const writer = screen.getByText('agent:writer').closest('tr')!
  expect(within(writer).queryByText(/folder-selected/)).toBeNull()
})

it('offers retry on a failed pack read instead of silently omitting it', async () => {
  vi.mocked(skillsApi.pack).mockRejectedValueOnce(new Error('offline'))
  renderIntel(<SkillCatalog packs={[pack]} onOpen={vi.fn()} />)
  await userEvent.click(await screen.findByRole('button', { name: /retry/i }))
  expect(await screen.findByText('review')).toBeInTheDocument()
})
