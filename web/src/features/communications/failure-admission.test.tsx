// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { classifyFailure } from './errors'
import { FailureNotice } from './failure-notice'
import './i18n'

describe('communication admission explanation', () => {
  it('names the directory admission requirement without suggesting a role change', () => {
    const failure = classifyFailure(
      new ApiError(
        403,
        'tenant_admission_required',
        'static engine explanation',
      ),
    )
    render(<FailureNotice failure={failure} />)
    expect(screen.getByText('Tenant admission required')).toBeInTheDocument()
    expect(
      screen.getByText(
        'Sign in with a tenant-admitted directory identity. Global bootstrap authority cannot operate workspace communications.',
      ),
    ).toBeInTheDocument()
    expect(screen.getByText('tenant_admission_required')).toBeInTheDocument()
    expect(
      screen.queryByText(/you do not have permission/i),
    ).not.toBeInTheDocument()
    expect(screen.getByRole('status')).toBeInTheDocument()
  })
  it('keeps unreadable evidence distinct from established admission denial', () => {
    const failure = classifyFailure(
      new ApiError(503, 'evidence_unavailable', 'evidence_unavailable'),
    )
    render(<FailureNotice failure={failure} />)
    expect(failure.kind).toBe('unavailable')
    expect(
      screen.queryByText('Tenant admission required'),
    ).not.toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('evidence_unavailable')
  })
})
