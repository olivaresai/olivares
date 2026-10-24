// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import { providersApi } from './api'
import { ProviderRotateDialog } from './provider-rotate-dialog'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: () => true, activeTenant: 'tenant', principal: null }),
}))

// The toast is hidden from assistive technology while the dialog is open (#2561):
// the refusal is announced on the key field itself.
it('announces a refused rotation on the key field', async () => {
  const user = userEvent.setup()
  vi.spyOn(providersApi, 'patch').mockRejectedValue(
    new Error('the engine refused the key'),
  )
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ProviderRotateDialog
        record={{
          provider_ref: 'prv_openai',
          kind: 'openai',
          display_name: 'OpenAI',
          state: 'active',
        }}
        open
        onOpenChange={() => {}}
      />
    </QueryClientProvider>,
  )
  const key = screen.getByLabelText(/^API key/)
  await user.type(key, 'synthetic-test-key')
  await user.click(screen.getByRole('button', { name: 'Rotate key' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'the engine refused the key',
  )
  expect(key).toHaveAccessibleDescription(/the engine refused the key/)
  expect(key).toBeInvalid()
})
