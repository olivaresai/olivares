// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { HTMLAttributes, ReactNode } from 'react'
import { cn } from '@/lib/utils'

/**
 * EmptyState — the calm "nothing here yet" placeholder for a list, table, or
 * panel that has no rows (a fresh project, a filtered view with no matches).
 * Centered, restrained, hairline-free: a muted icon chip, a short title, an
 * optional one-line explanation, and at most ONE action. This is NOT an error —
 * it must read as expected and unalarming (use ErrorState for failures, and
 * ForbiddenState for permission/paywall views).
 */
export interface EmptyStateProps extends Omit<
  HTMLAttributes<HTMLDivElement>,
  'title'
> {
  /** A lucide icon element, e.g. <Inbox />. Sized down to 20px in a muted chip. */
  icon?: ReactNode
  title: ReactNode
  description?: ReactNode
  /** A single CTA node (typically a <Button>), placed below the description. */
  action?: ReactNode
}

export function EmptyState({
  icon,
  title,
  description,
  action,
  className,
  ...props
}: EmptyStateProps) {
  return (
    <div
      // ⛔ `data-slot`, la convención del repo (`field`, `select-trigger`), porque `role="status"`
      // NO distingue: lo comparten los spinners y cualquier región viva. Un arnés que quisiera
      // saber «¿esta pantalla salió vacía?» no podía preguntarlo, y por eso el censo de capturas
      // en estado vacío era un número de un informe viejo en vez de una lista medida.
      data-slot="empty-state"
      // role="status" (aria-live polite) so when an async surface resolves to empty
      // — replacing a spinner — a screen reader hears the empty message instead of
      // silence (4.1.3). The genuine-error sibling ErrorState uses role="alert".
      role="status"
      className={cn(
        'flex flex-col items-center justify-center gap-3 px-6 py-12 text-center',
        className,
      )}
      {...props}
    >
      {icon ? (
        <div className="flex size-10 items-center justify-center rounded-lg bg-muted text-muted-foreground [&_svg]:size-5 [&_svg]:shrink-0">
          {icon}
        </div>
      ) : null}
      <div className="flex flex-col items-center gap-1.5">
        <p className="text-sm font-medium text-foreground">{title}</p>
        {description ? (
          <p className="max-w-sm text-sm text-muted-foreground">
            {description}
          </p>
        ) : null}
      </div>
      {action ? <div className="mt-1">{action}</div> : null}
    </div>
  )
}
