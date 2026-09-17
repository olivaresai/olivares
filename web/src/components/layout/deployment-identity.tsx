// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHICH DEPLOYMENT IS THIS (D21).
//
// T3 Code keeps a status bar naming the worktree, the PR and the branch — the scope
// of whatever you do next, always on screen. `an internal design note (not shipped)`
// §3.14 adapts that rule rather than copying the bar: ours names the things an
// Olivares action applies to. On a SIGNED-OUT screen there is no tenant and no
// workspace yet, so the scope is the deployment: the address you are about to hand a
// password to, and the version that will answer.
//
// ⛔ IT PRINTS ONLY WHAT IT WAS TOLD, AND NEVER A PLACEHOLDER. `/v1/server-info` is
//    unauthenticated and already drives the setup gate, so this costs no request and
//    exposes nothing a caller could not already read. Until it answers, the version
//    is simply absent — a dash where a version belongs reads as "unknown version",
//    which is a different and worse claim than saying nothing.
//
// ⛔ AND IT DOES NOT PRINT THE LICENSEE. `server-info` carries `license.licensee`,
//    and putting a customer's name on an unauthenticated screen is a product
//    decision, not a layout one. The endpoint already discloses it to anyone who
//    asks; this component does not put it in front of anyone who did not.
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { useServerInfo } from '@/lib/hooks/use-server-info'

/** The console's own origin as an operator would read it, or null in a non-browser. */
function consoleHost(): string | null {
  if (typeof window === 'undefined') return null
  return window.location.host || null
}

export function DeploymentIdentity({ className }: { className?: string }) {
  const { t } = useTranslation('auth')
  const info = useServerInfo()
  const host = consoleHost()
  const version = info.data?.version
  if (!host && !version) return null
  return (
    <p
      className={cn('text-caption text-muted-foreground', className)}
      data-testid="deployment-identity"
    >
      <span className="sr-only">{t('shell.deployment')}: </span>
      {host ? <span className="font-mono">{host}</span> : null}
      {host && version ? <span aria-hidden> · </span> : null}
      {version ? <span className="font-mono">{version}</span> : null}
    </p>
  )
}
