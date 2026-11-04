// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Link, useRouter, type AnyRouter } from '@tanstack/react-router'
import type { ComponentPropsWithRef } from 'react'

/**
 * A link to another page of the console, written as the address a person would read
 * (`/identity?tab=sso`). The router moves there without loading the document again, so
 * the session, the open dialogs and the drafts in memory stay; the address is still a
 * real `href`, so Ctrl/Cmd-click and middle-click open a new tab. A plain `<a href="/…">`
 * to a console page is refused by app-link.test.tsx. A query value that reads as JSON
 * (`?pack=123`) reaches the target quoted, so a target that needs it must read it with
 * useUrlState; a repeated key keeps its last value.
 */
export function AppLink({
  href,
  ...anchor
}: { href: string } & Omit<ComponentPropsWithRef<'a'>, 'href'>) {
  // Outside a RouterProvider (a view rendered on its own) the router answers undefined, as
  // the tab strip's does. The address is the same real href, so the anchor is the same link.
  const router = useRouter({ warn: false }) as AnyRouter | undefined
  if (!router) return <a {...anchor} href={href} />
  // The router takes route, query and fragment apart: a `to` holding all three
  // would read the query as part of the route.
  const target = new URL(href, window.location.origin)
  return (
    <Link
      {...anchor}
      to={target.pathname as never}
      search={Object.fromEntries(target.searchParams) as never}
      hash={target.hash.slice(1) || undefined}
    />
  )
}
