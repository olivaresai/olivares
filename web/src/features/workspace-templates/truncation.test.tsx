// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('./template-card', () => ({ TemplateCard: () => null }))
vi.mock('./template-editor', () => ({ TemplateEditor: () => null }))
vi.mock('@/features/agentops/run-create-dialog', () => ({
  RunCreateDialog: () => null,
}))
const list = vi.hoisted(() => vi.fn())
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, templatesApi: { ...actual.templatesApi, list } }
})
const { TemplatesView } = await import('./templates-view')

it('mounts the catalog badge from the loaded templates and has_more', async () => {
  list.mockResolvedValue({
    items: Array.from({ length: 12 }, (_, i) => ({ id: `tpl-${i}` })),
    has_more: true,
  })
  renderIntel(<TemplatesView />)
  expect(
    await screen.findByText('Loaded 12 templates; there are more'),
  ).toBeVisible()
})
