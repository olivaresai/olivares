// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Suspense, type ComponentType, type ReactNode } from 'react'
import { Spinner } from '@/components/ui/spinner'

/** Calm centered spinner while a code-split view's chunk loads. */
function ViewLoading() {
  return (
    <div className="flex min-h-[40vh] items-center justify-center">
      <Spinner />
    </div>
  )
}

/** Wrap a lazily-loaded view in a Suspense boundary (the route tree renders the
 * element synchronously, so each lazy view carries its own boundary). */
export function lazyView<P extends object>(
  View: ComponentType<P>,
  props?: P,
): () => ReactNode {
  // `props` exists for the two-doors-one-room case: `/sessions` and `/agentops`
  // mount the SAME view with a different entrance, rather than the registry carrying
  // two near-identical components that would drift apart the first time one is edited.
  return () => (
    <Suspense fallback={<ViewLoading />}>
      <View {...((props ?? {}) as P)} />
    </Suspense>
  )
}
