// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { SecretRef } from './secret-ref'

describe('SecretRef (secrets are references, never values — docs/08 §3)', () => {
  it('renders only the reference name, prefixed and labelled', () => {
    render(<SecretRef name="$GITHUB_TOKEN" />)
    expect(screen.getByText('$GITHUB_TOKEN')).toBeInTheDocument()
    expect(screen.getByText('ref:')).toBeInTheDocument()
    // The hint makes the no-value contract explicit to the operator.
    expect(screen.getByTitle(/value is never shown here/i)).toBeInTheDocument()
  })

  it('shows a calm "no secret" placeholder when there is no reference', () => {
    render(<SecretRef name={null} />)
    expect(screen.getByText('No secret')).toBeInTheDocument()
  })

  it('by construction cannot render a value — its only input is a name', () => {
    // The component API has no `value` prop; a name that LOOKS like a token is
    // still rendered verbatim as a reference, never resolved/expanded.
    render(<SecretRef name="vault/prod/db#password" />)
    expect(screen.getByText('vault/prod/db#password')).toBeInTheDocument()
  })
})
