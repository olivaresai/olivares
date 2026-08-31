// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// HashChip renders a tamper-evidence FINGERPRINT (a hash / detail_hash / ledger
// hash) as truncated monospace hex with copy-the-full-value. It exists to make the
// minimal-data rule visible: there is no payload behind a hash to expand — only the
// fingerprint (docs/SECURITY-HARDENING.md). The tooltip says so.
import { Check, Copy } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'
import { truncateHash } from '@/lib/format'
// The `intel` namespace travels with the modules that translate: these are deep-
// imported across features (`@/features/_intel/notices`), where the barrel — and so
// the registration — is never in the chunk.
import './i18n'

export function HashChip({
  hash,
  label,
  head = 8,
  tail = 6,
  className,
}: {
  hash: string | null | undefined
  /** Optional leading caption (e.g. "fingerprint", "seq 12"). */
  label?: string
  head?: number
  tail?: number
  className?: string
}) {
  const { t } = useTranslation('intel')
  const [copied, setCopied] = useState(false)

  if (!hash) return <span className="text-muted-foreground">—</span>

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(hash)
      setCopied(true)
      setTimeout(() => setCopied(false), 1200)
    } catch {
      // Clipboard may be unavailable (insecure context / denied) — fail quietly.
    }
  }

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          onClick={copy}
          className={cn(
            'group inline-flex items-center gap-1.5 rounded-sm border border-border bg-muted px-1.5 py-0.5',
            'font-mono text-xs text-muted-foreground tabular-nums',
            'outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring',
            className,
          )}
          aria-label={t('hash.copy')}
        >
          {label ? (
            // No `/70`: the label is real text, and the extra transparency put
            // it at 3.54:1 (dark) / 3.26:1 (light) on `bg-muted`. At full opacity
            // the same token clears AA on that surface.
            <span className="text-muted-foreground">{label}</span>
          ) : null}
          <span>{truncateHash(hash, head, tail)}</span>
          {copied ? (
            <Check className="size-3 text-success" />
          ) : (
            <Copy className="size-3 opacity-40 group-hover:opacity-100" />
          )}
        </button>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">
        <p className="font-mono break-all">{hash}</p>
        <p className="mt-1 text-muted-foreground">{t('hash.hint')}</p>
      </TooltipContent>
    </Tooltip>
  )
}
