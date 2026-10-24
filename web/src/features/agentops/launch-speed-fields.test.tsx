// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { LaunchSpeedFields } from './launch-speed-fields'
import './i18n'

describe('launch speed choices', () => {
  it('offers native default, on and explicit off only for a Claude model reporting support', async () => {
    const onFastMode = vi.fn()
    const user = userEvent.setup()
    render(
      <LaunchSpeedFields
        driver="claude"
        model={{ supports_fast_mode: true }}
        onFastMode={onFastMode}
        onServiceTier={vi.fn()}
      />,
    )
    await user.click(screen.getByRole('combobox', { name: 'Fast mode' }))
    await user.click(screen.getByRole('option', { name: 'On' }))
    expect(onFastMode).toHaveBeenLastCalledWith(true)
    await user.click(screen.getByRole('combobox', { name: 'Fast mode' }))
    await user.click(screen.getByRole('option', { name: 'Off' }))
    expect(onFastMode).toHaveBeenLastCalledWith(false)
  })

  it('offers the default and Off, not On, for a Claude model whose support is unknown', async () => {
    const onFastMode = vi.fn()
    const user = userEvent.setup()
    render(
      <LaunchSpeedFields
        driver="claude"
        model={{}}
        supportUnknown
        onFastMode={onFastMode}
        onServiceTier={vi.fn()}
      />,
    )
    await user.click(screen.getByRole('combobox', { name: 'Fast mode' }))
    expect(screen.getByRole('option', { name: 'On' })).toHaveAttribute(
      'aria-disabled',
      'true',
    )
    await user.click(screen.getByRole('option', { name: 'Off' }))
    expect(onFastMode).toHaveBeenLastCalledWith(false)
  })

  it('still shows nothing for a Claude model known not to report Fast mode', () => {
    render(
      <LaunchSpeedFields
        driver="claude"
        model={{ supports_fast_mode: false }}
        onFastMode={vi.fn()}
        onServiceTier={vi.fn()}
      />,
    )
    expect(screen.queryByRole('combobox', { name: 'Fast mode' })).toBeNull()
  })

  it('passes a vendor service tier unchanged for Codex', async () => {
    const onServiceTier = vi.fn()
    const user = userEvent.setup()
    render(
      <LaunchSpeedFields
        driver="codex"
        model={{ service_tiers: ['future-priority'] }}
        onFastMode={vi.fn()}
        onServiceTier={onServiceTier}
      />,
    )
    await user.click(screen.getByRole('combobox', { name: 'Service tier' }))
    await user.click(screen.getByRole('option', { name: 'future-priority' }))
    expect(onServiceTier).toHaveBeenCalledWith('future-priority')
  })

  it('shows no capability when the selected model or native mapping is unknown', () => {
    const { rerender } = render(
      <LaunchSpeedFields
        driver="claude"
        onFastMode={vi.fn()}
        onServiceTier={vi.fn()}
      />,
    )
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    rerender(
      <LaunchSpeedFields
        driver="grok"
        model={{ supports_fast_mode: true, service_tiers: ['priority'] }}
        onFastMode={vi.fn()}
        onServiceTier={vi.fn()}
      />,
    )
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
  })
})
