// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ExternalLink } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { useClientSettings } from '@/features/settings/preferences'
import { EDITORS, editorLink, sshTarget } from '@/lib/editor-links'
import './i18n'

/**
 * A link that opens a file of a session's folder in the editor the person chose in Settings
 * (VS Code, Zed or a JetBrains IDE), at `line` when given. It is only a link: the editor's
 * own URL handler opens it, and nothing is asked of the engine. Nothing shows when the link
 * cannot be built (the editor, the path or the SSH host is not one a link can carry).
 */
export function OpenInEditor({
  root,
  file,
  line,
}: {
  root: string
  file: string
  line?: number
}) {
  const { t } = useTranslation('shared')
  const editor = useClientSettings((s) => s.editor)
  const editorSsh = useClientSettings((s) => s.editorSsh)
  const href = editorLink(
    editor,
    { root, file, line },
    sshTarget(editorSsh, window.location.hostname),
  )
  if (!href) return null
  return (
    <Button asChild variant="ghost" size="sm" className="shrink-0">
      <a href={href}>
        <ExternalLink className="size-3.5" aria-hidden />
        {t('openInEditor', { editor: EDITORS[editor].label })}
      </a>
    </Button>
  )
}
