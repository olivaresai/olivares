// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A writable code editor is a text box: its border is what tells an operator where to type, so
// it is a control boundary and takes `ctl-border` (3:1 on every surface, WCAG 2.2 SC 1.4.11),
// like the text field and the text area. A read-only viewer is a container that shows a
// document; it keeps the hairline container edge.
import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { CodeEditor } from './code-editor'

const host = (c: HTMLElement) =>
  c.querySelector('[data-slot="code-editor"]') as HTMLElement

describe('the code editor boundary', () => {
  it('a writable editor draws its boundary with ctl-border', () => {
    const { container } = render(
      <CodeEditor value="permit;" language="cedar" ariaLabel="Policy" />,
    )
    expect(host(container)).toHaveClass('border-ctl-border')
    expect(host(container)).not.toHaveClass('border-border-strong')
  })

  it('a read-only viewer keeps the hairline container edge', () => {
    const { container } = render(
      <CodeEditor
        value="permit;"
        language="cedar"
        ariaLabel="Policy"
        readOnly
      />,
    )
    expect(host(container)).toHaveClass('border-border-strong')
    expect(host(container)).not.toHaveClass('border-ctl-border')
  })

  it('an invalid writable editor draws its boundary in the danger color', () => {
    const { container } = render(
      <CodeEditor value="{" language="json" ariaLabel="Settings" invalid />,
    )
    expect(host(container)).toHaveClass('border-danger')
    expect(host(container)).not.toHaveClass('border-ctl-border')
  })
})
