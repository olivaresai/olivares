// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// useConditionNotice — say a standing condition while it holds (see lib/notify.ts).
//
// `notice` has three states. A notice: the condition holds, it is told when it starts or its
// `signature` changes. `null`: the read answered and the condition does not hold, so it is
// over, its toast comes down and the person's dismissal is forgotten, even on a page that never
// told it (the condition may have cleared while the person was away). `undefined`: not known
// yet (the read is pending), so nothing is said and nothing is forgotten. Leaving the page does
// NOT end a condition: a person who closed "the engine cannot be reached" and came back to the
// same unreachable engine must not be told again. Leaving does take the toast down: an error
// toast never expires, and its action belongs to a page that is gone.
//
// The notice is read from a ref, not from the effect's dependencies: its `action` is a new
// function every render, and an effect that re-ran for it would tell the condition every render.
// For the same reason the toast's action calls the CURRENT `action`, not the one from the render
// that told it: a callback that checks a permission must see the permission as it is now.
import { useEffect, useRef } from 'react'
import { notify, resolveNotice, type Notice } from '@/lib/notify'

export function useConditionNotice(
  condition: string,
  notice: Omit<Notice, 'condition'> | null | undefined,
) {
  const latest = useRef(notice)
  useEffect(() => {
    latest.current = notice
  })
  useEffect(
    () => () => resolveNotice(condition, { keepDismissal: true }),
    [condition],
  )
  const known = notice !== undefined
  const holds = !!notice
  const signature = notice?.signature
  useEffect(() => {
    if (!known) return
    const current = latest.current
    if (holds && current) {
      notify({
        ...current,
        condition,
        action: current.action && {
          label: current.action.label,
          onClick: () => latest.current?.action?.onClick(),
        },
      })
    } else {
      resolveNotice(condition)
    }
  }, [condition, known, holds, signature])
}
